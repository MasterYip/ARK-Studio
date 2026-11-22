package store

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/platform/textfile"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/sqlitedb"
)

// CommentKinds is COMMENT_KINDS.
var CommentKinds = []string{"comment", "sent_back", "decided", "accepted", "agent_reply"}

const maxComment = 8000

// cleanText is _clean_text(value, field, max_len) (required=False).
func cleanText(value any, field string, maxLen int) (string, error) {
	text := ""
	if value != nil {
		text = pyjson.Str(value)
	}
	text = strings.ReplaceAll(text, "\x00", "")
	if pytext.Len(text) > maxLen {
		return "", storeErr("%s is longer than %d characters", field, maxLen)
	}
	return text, nil
}

// logActivity is Store.log_activity(commit=False): the row now, the JSONL
// mirror after the commit (see appendLog).
func logActivity(ctx context.Context, q sqlitedb.Querier, actor, action string, unitID any, detail *pyjson.Object) (logLine, error) {
	at := clock.UTCNow()
	_, err := q.ExecContext(ctx, "INSERT INTO activity(at,actor,action,unit_id,detail_json) VALUES(?,?,?,?,?)",
		at, actor, action, unitID, string(pyjson.Dumps(detail)))
	line := pyjson.Dumps(pyjson.NewObject("at", at, "actor", actor, "action", action, "unit_id", unitID, "detail", detail))
	if err == nil {
		observeActivity(ctx, actor, action, unitID, detail) // activityobs.go (Go only)
	}
	return logLine(line), err
}

type logLine []byte

func (s *Store) appendLog(lines ...logLine) {
	f, err := os.OpenFile(s.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return // Python ignores OSError here too
	}
	defer f.Close()
	for _, l := range lines {
		f.Write(textfile.Encode(string(l) + "\n")) //nolint:errcheck
	}
}

// touchManifest is Store.touch_manifest: updated = now, rewrite project.json.
// It runs holding the writer.  When more writers are queued, the file
// rewrite (a create + rename, the largest part of a small write on Windows)
// is left to the next write in the queue or, if none of them touches the
// manifest, to the flusher: project.json then reflects the last write of a
// burst, as it would after Python's per-write rewrites.  Without queued
// writers (every sequential request) the file is rewritten here, exactly as
// before.  The in-memory manifest, which every route reads, is always
// current.
func (s *Store) touchManifest() {
	s.countWrite()
	if s.Manifest == nil {
		return
	}
	s.Manifest.mu.Lock()
	if s.Manifest.obj.Len() == 0 {
		s.Manifest.mu.Unlock()
		return
	}
	s.Manifest.obj.Set("updated", clock.UTCNow())
	s.Manifest.mu.Unlock()
	if s.DB.Waiting() {
		s.manifestDirty.Store(true)
		select {
		case s.manifestKick <- struct{}{}:
		default:
		}
		return
	}
	s.writeManifestNow()
}

// writeManifestNow encodes the current manifest and rewrites project.json.
func (s *Store) writeManifestNow() {
	s.manifestDirty.Store(false)
	s.Manifest.mu.Lock()
	data := pyjson.DumpsIndent(s.Manifest.obj, 2)
	s.Manifest.mu.Unlock()
	if WriteManifest(s.Path, data) != nil {
		// Python ignores OSError; keep the rewrite pending so the next write,
		// a flush or Close tries again instead of leaving the file stale.
		s.manifestDirty.Store(true)
	}
}

// manifestFlusher writes a deferred project.json once the writers queued
// at the time have passed (it queues for the writer like them).
func (s *Store) manifestFlusher() {
	for {
		select {
		case <-s.closing:
			return
		case <-s.manifestKick:
		}
		s.DB.Locked(context.Background(), s.FlushManifest) //nolint:errcheck // closed: Close flushes
	}
}

// FlushManifest rewrites project.json now if a write deferred it.  Callers
// that read the file from disk (black-box snapshots) call it first.
func (s *Store) FlushManifest() {
	if s.Manifest != nil && s.manifestDirty.Load() {
		s.writeManifestNow()
	}
}

// manifestFileMu serializes project.json rewrites per folder: they share
// the project.json.tmp name.
var manifestFileMu sync.Map // folder -> *sync.Mutex

// WriteManifest is db.write_manifest for already-encoded JSON.
func WriteManifest(folder string, encoded []byte) error {
	mu, _ := manifestFileMu.LoadOrStore(folder, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(folder, "project.json.tmp")
	if err := os.WriteFile(tmp, textfile.Encode(string(encoded)+"\n"), 0o644); err != nil {
		return err
	}
	return renameReplacing(tmp, filepath.Join(folder, "project.json"))
}

// renameReplacing is os.Rename, retried for a short while on Windows: there
// a rename onto a file that someone has open (a reader of project.json, the
// Python CLI, a virus scanner) fails with "Access is denied" until the
// handle is closed.
func renameReplacing(from, to string) error {
	err := os.Rename(from, to)
	if err == nil || runtime.GOOS != "windows" {
		return err
	}
	deadline := time.Now().Add(2 * time.Second)
	for wait := time.Millisecond; time.Now().Before(deadline); wait = min(2*wait, 50*time.Millisecond) {
		time.Sleep(wait)
		if err = os.Rename(from, to); err == nil {
			return nil
		}
	}
	return err
}

func isAgent(actor string) bool { return strings.HasPrefix(actor, "agent:") }

// AddComment is Store.add_comment.
func (s *Store) AddComment(ctx context.Context, ref string, body any, kind string, replyTo any, author, actor string) (*pyjson.Object, error) {
	text, err := cleanText(body, "comment", maxComment)
	if err != nil {
		return nil, err
	}
	text = pytext.Strip(text)
	if text == "" {
		return nil, storeErr("comment text is required")
	}
	kind = pytext.Strip(kind)
	if kind == "" {
		kind = "comment"
	}
	if !containsString(CommentKinds, kind) {
		return nil, storeErr("comment kind must be one of %s", strings.Join(CommentKinds, ", "))
	}
	agent := isAgent(actor)
	if agent && kind != "agent_reply" {
		return nil, storeErr("agents can only add agent_reply comments")
	}
	if !agent && kind == "agent_reply" {
		return nil, storeErr("agent_reply comments come from agents")
	}
	if agent {
		author = actor
	}
	if author == "" {
		author = "user"
	}
	author, err = cleanText(author, "author", 80)
	if err != nil {
		return nil, err
	}
	if author = pytext.Strip(author); author == "" {
		author = "user"
	}
	var created sqlitedb.Row
	var line logLine
	var unitID int64
	var before string
	began := false
	err = s.DB.Write(ctx, func(q sqlitedb.Querier) error {
		before, began = s.beginWrite(ctx, q), true
		row, err := unitRow(ctx, q, ref)
		unitID = row.Int("id")
		if err != nil {
			return err
		}
		if err := refuseMoved(row); err != nil { // move.go
			return err
		}
		var parent any
		if replyTo != nil && replyTo != "" {
			parentID, err := PyInt(replyTo)
			if err != nil {
				return storeErr("reply_to must be a comment id")
			}
			p, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT * FROM unit_comments WHERE id=? AND unit_id=?", parentID, row.Int("id"))
			if err != nil {
				return err
			}
			if !ok {
				return storeErr("comment not found on this unit: %d", parentID)
			}
			parent = p.Get("id")
		}
		res, err := q.ExecContext(ctx, "INSERT INTO unit_comments(unit_id,author,kind,body,created_at,reply_to) VALUES(?,?,?,?,?,?)",
			row.Int("id"), author, kind, text, clock.UTCNow(), parent)
		if err != nil {
			return err
		}
		commentID, _ := res.LastInsertId()
		line, err = logActivity(ctx, q, actor, "comment.create", row.Int("id"), pyjson.NewObject(
			"uid", row.Get("uid"), "title", row.Get("title"), "comment_id", commentID, "kind", kind,
			"reply_to", parent, "excerpt", pytext.Excerpt(text, 120)))
		if err != nil {
			return err
		}
		created, _, err = sqlitedb.QueryOne(ctx, q, "SELECT * FROM unit_comments WHERE id=?", commentID)
		return err
	}, func() {
		s.appendLog(line)
		s.touchManifest()
		s.noteWrite(ctx, before, []int64{unitID})
	})
	if err != nil && began {
		s.writeFailed()
	}
	if err != nil {
		return nil, err
	}
	return commentDict(created), nil
}

// ResolveComment is Store.resolve_comment.
func (s *Store) ResolveComment(ctx context.Context, ref, commentRef string, resolved bool, author, actor string) (*pyjson.Object, error) {
	commentID, err := pytext.Int(commentRef)
	if err != nil {
		return nil, storeErr("comment id must be an integer")
	}
	by := author
	if isAgent(actor) {
		by = actor
	} else if by == "" {
		by = "user"
	}
	var fresh sqlitedb.Row
	var lines []logLine
	var unitID int64
	var before string
	began := false
	err = s.DB.Write(ctx, func(q sqlitedb.Querier) error {
		before, began = s.beginWrite(ctx, q), true
		row, err := unitRow(ctx, q, ref)
		unitID = row.Int("id")
		if err != nil {
			return err
		}
		c, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT * FROM unit_comments WHERE id=? AND unit_id=?", commentID, row.Int("id"))
		if err != nil {
			return err
		}
		if !ok {
			return storeErr("comment not found on this unit: %d", commentID)
		}
		if pyjson.Truthy(c.Get("resolved_at")) != resolved {
			if resolved {
				_, err = q.ExecContext(ctx, "UPDATE unit_comments SET resolved_at=?, resolved_by=? WHERE id=?", clock.UTCNow(), by, commentID)
			} else {
				_, err = q.ExecContext(ctx, "UPDATE unit_comments SET resolved_at=?, resolved_by=? WHERE id=?", nil, nil, commentID)
			}
			if err != nil {
				return err
			}
			action := "comment.reopen"
			if resolved {
				action = "comment.resolve"
			}
			line, err := logActivity(ctx, q, actor, action, row.Int("id"), pyjson.NewObject(
				"uid", row.Get("uid"), "title", row.Get("title"), "comment_id", commentID,
				"kind", c.Get("kind"), "excerpt", pytext.Excerpt(c.Str("body"), 120)))
			if err != nil {
				return err
			}
			lines = append(lines, line)
		}
		fresh, _, err = sqlitedb.QueryOne(ctx, q, "SELECT * FROM unit_comments WHERE id=?", commentID)
		return err
	}, func() {
		s.appendLog(lines...)
		s.touchManifest()
		s.noteWrite(ctx, before, []int64{unitID})
	})
	if err != nil && began {
		s.writeFailed()
	}
	if err != nil {
		return nil, err
	}
	return commentDict(fresh), nil
}

// BackfillCreatorAssignees is Store.backfill_creator_assignees: once per
// database (meta flag), assign unassigned units to their creating account.
func (s *Store) BackfillCreatorAssignees(ctx context.Context, isAccount func(string) (string, bool)) error {
	if s.creatorBackfilled.Load() {
		return nil
	}
	s.backfillMu.Lock()
	defer s.backfillMu.Unlock()
	if s.creatorBackfilled.Load() {
		return nil
	}
	changed := 0
	var before string
	began := false
	err := s.DB.Write(ctx, func(q sqlitedb.Querier) error {
		before, began = s.beginWrite(ctx, q), true
		flag, _, err := MetaGet(ctx, q, CreatorAssigneeMigrated)
		if err != nil || flag == "1" {
			return err
		}
		rows, err := sqlitedb.Query(ctx, q, `SELECT u.id, u.uid, u.title,
                  (SELECT a.actor FROM activity a WHERE a.unit_id=u.id AND a.action='unit.create'
                   ORDER BY a.id LIMIT 1) AS creator
           FROM units u WHERE u.assignee IS NULL`)
		if err != nil {
			return err
		}
		for _, r := range rows {
			creator := pytext.Strip(strOrEmpty(r.Get("creator")))
			if creator == "" || strings.Contains(creator, ":") || pseudoActors[strings.ToLower(creator)] {
				continue
			}
			name, ok := isAccount(creator)
			if !ok {
				continue
			}
			if _, err := q.ExecContext(ctx, "UPDATE units SET assignee=? WHERE id=? AND assignee IS NULL", name, r.Int("id")); err != nil {
				return err
			}
			if err := insertActivity(ctx, q, MigrationActor, BackfillAction, r.Int("id"), pyjson.NewObject(
				"uid", r.Get("uid"), "title", r.Get("title"), "fields", []any{"assignee"}, "backfill", true,
				"assignee", pyjson.NewObject("from", nil, "to", name, "source", "unit.create"))); err != nil {
				return err
			}
			changed++
		}
		return MetaSet(ctx, q, CreatorAssigneeMigrated, "1")
	}, func() { s.noteWrite(ctx, before, nil) })
	if err != nil && began {
		s.writeFailed()
	}
	if err != nil {
		return err
	}
	s.creatorBackfilled.Store(true)
	if changed > 0 {
		s.touchManifest()
	}
	return nil
}

var pseudoActors = map[string]bool{"system": true, "human": true, "agent": true, "assistant": true, "import": true,
	"user": true, "ui": true, "api": true, "migration": true, "seed": true, "demo": true}

// Mutate changes the shared manifest under its lock and returns the encoded
// project.json text (db.write_manifest's json.dumps(indent=2)).
func (m *Manifest) Mutate(fn func(o *pyjson.Object) error) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := fn(m.obj); err != nil {
		return nil, err
	}
	return pyjson.DumpsIndent(m.obj, 2), nil
}

// LogActivity is Store.log_activity(commit=True): one row, the JSONL
// mirror, no manifest touch.
func (s *Store) LogActivity(ctx context.Context, actor, action string, unitID any, detail *pyjson.Object) error {
	return s.write(ctx, func(w *wtx) error {
		w.noTouch = true
		return w.log(actor, action, unitID, detail)
	})
}

// SetMeta is Store.set_meta.
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	return s.write(ctx, func(w *wtx) error {
		w.noTouch = true
		return MetaSet(ctx, w.q, key, value)
	})
}
