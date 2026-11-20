package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"arkstudio/go/internal/platform/clock"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/sqlitedb"
)

// Port of arkstudio/db.py: connect(), migrate() and the data migrations.

// Migration meta flags (db.py).
const (
	BodyNotesMigrated       = "migration.body_notes_v4"
	NeedsTagsMigrated       = "migration.needs_tags_v1"
	DefaultDatesMigrated    = "migration.default_dates_v5"
	CreatorAssigneeMigrated = "migration.creator_assignee_v5"
	MigrationActor          = "system:migration"
	BackfillAction          = "unit.backfill"
)

// OpenProjectDB is db.connect(<project>/ark.sqlite3): WAL, synchronous NORMAL,
// foreign keys, SCHEMA, migrate(), schema_version.
func OpenProjectDB(path string, readers int) (*sqlitedb.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sqlitedb.Open(path, sqlitedb.Options{Journal: "WAL", Readers: readers, ForeignKeys: true,
		MmapBytes: 256 << 20, CacheKiB: 8 << 10})
	if err != nil {
		return nil, err
	}
	if err := db.Exec(ProjectSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	ctx := context.Background()
	err = db.Write(ctx, func(q sqlitedb.Querier) error { return migrate(ctx, q) }, nil)
	if err == nil {
		// db.connect: REVISION_SCHEMA (change_counter + triggers) after migrate().
		err = db.Exec(RevisionSchema)
	}
	if err == nil {
		// v11: CHANGE_LOG_SCHEMA (change_log + triggers), then a log identity.
		err = db.Exec(ChangeLogSchema)
	}
	if err == nil {
		err = db.Write(ctx, func(q sqlitedb.Querier) error {
			if id, _, err := MetaGet(ctx, q, ChangeLogID); err != nil {
				return err
			} else if id == "" {
				if _, err := newChangeLogID(ctx, q); err != nil {
					return err
				}
			}
			_, err := q.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES('schema_version',?) "+
				"ON CONFLICT(key) DO UPDATE SET value=excluded.value", strconv.Itoa(SchemaVersion))
			return err
		}, nil)
	}
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

func columns(ctx context.Context, q sqlitedb.Querier, table string) (map[string]bool, error) {
	rows, err := sqlitedb.Query(ctx, q, "PRAGMA table_info("+table+")")
	out := map[string]bool{}
	for _, r := range rows {
		out[r.Str("name")] = true
	}
	return out, err
}

// MetaGet is db.meta_get.
func MetaGet(ctx context.Context, q sqlitedb.Querier, key string) (string, bool, error) {
	r, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT value FROM meta WHERE key=?", key)
	return r.Str("value"), ok, err
}

// MetaSet is db.meta_set.
func MetaSet(ctx context.Context, q sqlitedb.Querier, key, value string) error {
	_, err := q.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value)
	return err
}

func migrate(ctx context.Context, q sqlitedb.Querier) error {
	cols, err := columns(ctx, q, "node_text_pending")
	if err != nil {
		return err
	}
	if !cols["checked_at"] {
		if _, err := q.ExecContext(ctx, "ALTER TABLE node_text_pending ADD COLUMN checked_at REAL NOT NULL DEFAULT 0"); err != nil {
			return err
		}
	}
	if cols, err = columns(ctx, q, "units"); err != nil {
		return err
	}
	if !cols["assignee"] {
		if _, err := q.ExecContext(ctx, "ALTER TABLE units ADD COLUMN assignee TEXT"); err != nil {
			return err
		}
	}
	if _, err := q.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS units_assignee ON units(assignee)"); err != nil {
		return err
	}
	if cols, err = columns(ctx, q, "chat_messages"); err != nil {
		return err
	}
	if len(cols) > 0 && !cols["user"] {
		// v10: per-user Assistant chat history.  The old rows keep user ''
		// until Store.AdoptLegacyChat gives them to the project owner.
		if _, err := q.ExecContext(ctx, "ALTER TABLE chat_messages ADD COLUMN user TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	if _, err := q.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS chat_user ON chat_messages(user, id)"); err != nil {
		return err
	}
	if cols, err = columns(ctx, q, "topic_members"); err != nil {
		return err
	}
	if len(cols) > 0 && !cols["parent_id"] {
		if _, err := q.ExecContext(ctx, "ALTER TABLE topic_members ADD COLUMN parent_id INTEGER"); err != nil {
			return err
		}
	}
	if len(cols) > 0 && !cols["w"] {
		// v12: a member's node-graph card width (null = automatic).
		if _, err := q.ExecContext(ctx, "ALTER TABLE topic_members ADD COLUMN w REAL"); err != nil {
			return err
		}
	}
	steps := []struct {
		flag string
		run  func(context.Context, sqlitedb.Querier) error
	}{{BodyNotesMigrated, migrateBodyNotes}, {NeedsTagsMigrated, backfillNeedsTags}, {DefaultDatesMigrated, backfillDefaultDates}}
	for _, step := range steps {
		value, _, err := MetaGet(ctx, q, step.flag)
		if err != nil {
			return err
		}
		if value == "1" {
			continue
		}
		if err := step.run(ctx, q); err != nil {
			return err
		}
		if err := MetaSet(ctx, q, step.flag, "1"); err != nil {
			return err
		}
	}
	return nil
}

var noteKinds = map[string]string{
	"User comment": "comment", "Sent back by user": "sent_back",
	"Decided by user": "decided", "Agent reply": "agent_reply",
}

var noteLine = pytext.MustCompile(`^\*\*(User comment|Sent back by user|Decided by user|Agent reply) \((\d{4}-\d{2}-\d{2})\):\*\* (.*)\z`)

type bodyNote struct {
	kind, day, text string
	remove          bool
}

// parseBodyNotes is db.parse_body_notes.
func parseBodyNotes(body string) ([]bodyNote, string) {
	lines := strings.Split(body, "\n")
	var notes []bodyNote
	drop := map[int]bool{}
	for i := 0; i < len(lines); {
		m := noteLine.FindStringSubmatch(strings.TrimRight(lines[i], "\r"))
		if m == nil {
			i++
			continue
		}
		var tail []string
		j := i + 1
		for j < len(lines) && pytext.Strip(lines[j]) != "" && !noteLine.MatchString(strings.TrimRight(lines[j], "\r")) {
			tail = append(tail, pytext.Strip(lines[j]))
			j++
		}
		text := pytext.Strip(strings.Join(append([]string{pytext.Strip(m[3])}, tail...), " "))
		exact := len(tail) == 0
		if text != "" {
			notes = append(notes, bodyNote{noteKinds[m[1]], m[2], text, exact})
		}
		if exact {
			drop[i] = true
		}
		i = j
	}
	if len(drop) == 0 {
		return notes, body
	}
	var out []string
	skipBlank := false
	for index, line := range lines {
		if drop[index] {
			if len(out) == 0 || pytext.Strip(out[len(out)-1]) == "" {
				skipBlank = true
			}
			continue
		}
		if skipBlank && pytext.Strip(line) == "" {
			continue
		}
		skipBlank = false
		out = append(out, line)
	}
	for len(out) > 0 && pytext.Strip(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	newBody := strings.Join(out, "\n")
	if newBody != "" && strings.HasSuffix(body, "\n") {
		newBody += "\n"
	}
	return notes, newBody
}

func insertActivity(ctx context.Context, q sqlitedb.Querier, actor, action string, unitID any, detail *pyjson.Object) error {
	_, err := q.ExecContext(ctx, "INSERT INTO activity(at,actor,action,unit_id,detail_json) VALUES(?,?,?,?,?)",
		clock.UTCNow(), actor, action, unitID, string(pyjson.Dumps(detail)))
	return err
}

func migrateBodyNotes(ctx context.Context, q sqlitedb.Querier) error {
	rows, err := sqlitedb.Query(ctx, q, "SELECT id, uid, title, body FROM units WHERE body LIKE '%**%(____-__-__):** %'")
	if err != nil {
		return err
	}
	inserted := map[int64]bool{}
	for _, row := range rows {
		notes, newBody := parseBodyNotes(row.Str("body"))
		if len(notes) == 0 {
			continue
		}
		var lastUser any
		moved := 0
		for _, note := range notes {
			author := "user"
			if note.kind == "agent_reply" {
				author = "agent"
			}
			created := note.day + "T00:00:00+00:00"
			existing, found, err := sqlitedb.QueryOne(ctx, q,
				"SELECT id FROM unit_comments WHERE unit_id=? AND kind=? AND author=? AND body=? AND created_at=?",
				row.Int("id"), note.kind, author, note.text, created)
			if err != nil {
				return err
			}
			var commentID int64
			if found && !inserted[existing.Int("id")] {
				commentID = existing.Int("id")
			} else {
				var replyTo any
				if note.kind == "agent_reply" {
					replyTo = lastUser
				}
				res, err := q.ExecContext(ctx, "INSERT INTO unit_comments(unit_id,author,kind,body,created_at,reply_to) VALUES(?,?,?,?,?,?)",
					row.Int("id"), author, note.kind, note.text, created, replyTo)
				if err != nil {
					return err
				}
				commentID, _ = res.LastInsertId()
				inserted[commentID] = true
				moved++
			}
			if note.kind != "agent_reply" {
				lastUser = commentID
			}
		}
		if newBody != row.Str("body") {
			if _, err := q.ExecContext(ctx, "UPDATE units SET body=? WHERE id=?", newBody, row.Int("id")); err != nil {
				return err
			}
		}
		if moved > 0 {
			removed := int64(0)
			for _, n := range notes {
				if n.remove {
					removed++
				}
			}
			detail := pyjson.NewObject("uid", row.Get("uid"), "title", row.Get("title"), "count", int64(moved), "removed_from_body", removed)
			if err := insertActivity(ctx, q, MigrationActor, "comment.migrate", row.Int("id"), detail); err != nil {
				return err
			}
		}
	}
	return nil
}

func backfillNeedsTags(ctx context.Context, q sqlitedb.Querier) error {
	rows, err := sqlitedb.Query(ctx, q, `SELECT id, uid, title, CASE WHEN status='review' THEN 'review' ELSE 'decision' END AS need
           FROM units u WHERE archived=0
             AND (status='review' OR (type='decision' AND status NOT IN ('done','abandoned','archived')))
             AND NOT EXISTS (SELECT 1 FROM unit_tags t WHERE t.unit_id=u.id
                             AND substr(t.tag,1,6)='needs:')`)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := q.ExecContext(ctx, "INSERT OR IGNORE INTO unit_tags(unit_id,tag) VALUES(?,?)", row.Int("id"), "needs:"+row.Str("need")); err != nil {
			return err
		}
		detail := pyjson.NewObject("uid", row.Get("uid"), "title", row.Get("title"), "from", nil, "to", row.Get("need"), "backfill", true)
		if err := insertActivity(ctx, q, MigrationActor, "unit.needs", row.Int("id"), detail); err != nil {
			return err
		}
	}
	return nil
}

var dayRe = pytext.MustCompile(`^\d{4}-\d{2}-\d{2}\z`)

// day is db._day: the first ten characters when they form a real date.
func day(value any) (string, bool) {
	s, _ := value.(string)
	if value != nil && s == "" {
		s = pyjson.Str(value)
	}
	text := pytext.Prefix(s, 10)
	if !dayRe.MatchString(text) {
		return "", false
	}
	parts := strings.Split(text, "-")
	y, _ := pytext.Int(parts[0])
	m, _ := pytext.Int(parts[1])
	d, _ := pytext.Int(parts[2])
	if y < 1 || m < 1 || m > 12 || d < 1 {
		return "", false
	}
	if t := time.Date(int(y), time.Month(m), int(d), 0, 0, 0, 0, time.UTC); t.Day() != int(d) {
		return "", false
	}
	return text, true
}

func backfillDefaultDates(ctx context.Context, q sqlitedb.Querier) error {
	rows, err := sqlitedb.Query(ctx, q, "SELECT id, uid, title, status, start_at, end_at, created_at, updated_at FROM units "+
		"WHERE start_at IS NULL OR (status='done' AND end_at IS NULL)")
	if err != nil {
		return err
	}
	var doneIDs []any
	for _, r := range rows {
		if r.Str("status") == "done" && r.Get("end_at") == nil {
			doneIDs = append(doneIDs, r.Int("id"))
		}
	}
	completed := map[int64]string{}
	for start := 0; start < len(doneIDs); start += 500 {
		chunk := doneIDs[start:min(start+500, len(doneIDs))]
		acts, err := sqlitedb.Query(ctx, q, "SELECT unit_id, at, detail_json FROM activity WHERE action='unit.update' "+
			"AND unit_id IN ("+sqlitedb.Marks(len(chunk))+") ORDER BY id", chunk...)
		if err != nil {
			return err
		}
		for _, a := range acts {
			detail, ok := pyjson.LoadsObject(nonEmpty(a.Str("detail_json"), "{}"))
			if !ok {
				continue
			}
			fields, isList := detail.Value("fields").([]any)
			if detail.Value("fields") != nil && !pyjson.Truthy(detail.Value("fields")) {
				fields, isList = nil, true
			}
			d, okDay := day(a.Get("at"))
			if okDay && isList && containsStr(fields, "status") {
				completed[a.Int("unit_id")] = d
			}
		}
	}
	for _, r := range rows {
		start, end := r.Get("start_at"), r.Get("end_at")
		var fields []any
		detail := pyjson.NewObject("uid", r.Get("uid"), "title", r.Get("title"), "fields", nil, "backfill", true)
		if start == nil {
			if d, ok := day(r.Get("created_at")); ok {
				if e, _ := end.(string); !(e != "" && e < d) {
					start = d
					fields = append(fields, "start_at")
					detail.Set("start_at", pyjson.NewObject("from", nil, "to", d, "source", "created_at"))
				}
			}
		}
		if r.Str("status") == "done" && end == nil {
			source := "updated_at"
			d, ok := completed[r.Int("id")]
			if ok {
				source = "activity"
			} else {
				d, ok = day(r.Get("updated_at"))
			}
			if ok {
				if s, _ := start.(string); !(s != "" && d < s) {
					end = d
					fields = append(fields, "end_at")
					detail.Set("end_at", pyjson.NewObject("from", nil, "to", d, "source", source))
				}
			}
		}
		if len(fields) == 0 {
			continue
		}
		detail.Set("fields", fields)
		if _, err := q.ExecContext(ctx, "UPDATE units SET start_at=?, end_at=? WHERE id=?", start, end, r.Int("id")); err != nil {
			return err
		}
		if err := insertActivity(ctx, q, MigrationActor, BackfillAction, r.Int("id"), detail); err != nil {
			return err
		}
	}
	return nil
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func containsStr(items []any, want string) bool {
	for _, v := range items {
		if s, ok := v.(string); ok && s == want {
			return true
		}
	}
	return false
}
