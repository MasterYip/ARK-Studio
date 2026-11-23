package store

// Splitting and merging projects (Go only; agent guide:
// docs/agents/project-content.md).
//
// A split moves branches (roots and their part_of / derived_from
// descendants, see clusters.go) into a new project; a merge moves every
// unit of a project into another one.  PlanMove is the dry run; ApplyMove:
//
//  1. journal + online backups of both databases in <source>/moves/<id>/;
//  2. media files hard-linked (same disk) or copied into the target, and the
//     source's text history fetched into the target's history.git;
//  3. one source transaction: the plan is checked against the database
//     (change_counter), the moved units become tombstones in the recycle bin
//     (archived=1, meta_json.moved_to, their links saved in the unit.archive
//     row as a normal delete does) and unit_aliases rows point at the new
//     place;
//  4. one target transaction: units, tags, links between moved units,
//     media rows, comments, topic members, text history heads and aliases.
//
// A failure after step 3 undoes the source transaction; a failure in
// step 2 or 4 deletes the files it created.  UndoMove reverses a finished
// move.  Uids stay when none is taken in the target; one clash renumbers
// the whole batch from the target's counters (D-0015).  [[UID]] and media references in the moved
// bodies and comments are rewritten; a [[UID]] that points at a unit staying
// behind becomes [[<source project id>/UID]].  A link between a moved and a
// staying unit cannot live in one database: with cross_links "keep" both
// ends record it in meta_json.external_links, with "drop" it is only kept
// in the source's recycle-bin row (an undo restores it).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/sqlitedb"
)

// MovedError is a write to a unit that was moved to another project
// (HTTP 409 UNIT_MOVED).
type MovedError struct {
	UID, Project, ProjectName, NewUID string
}

func (e *MovedError) Error() string {
	name := e.ProjectName
	if name == "" {
		name = e.Project
	}
	return fmt.Sprintf("%s was moved to project %s as %s", e.UID, name, e.NewUID)
}

// movedTo returns the moved_to object of a unit row, or nil.
func movedTo(row sqlitedb.Row) *pyjson.Object {
	if row.Int("archived") == 0 {
		return nil
	}
	m, _ := parseDetail(row.Get("meta_json")).Value("moved_to").(*pyjson.Object)
	return m
}

// refuseMoved is the write guard on a moved unit's tombstone.
func refuseMoved(row sqlitedb.Row) error {
	m := movedTo(row)
	if m == nil {
		return nil
	}
	return &MovedError{UID: row.Str("uid"), Project: pyjson.Str(m.Value("project")),
		ProjectName: pyjson.Str(m.Value("project_name")), NewUID: pyjson.Str(m.Value("uid"))}
}

const aliasSchema = `CREATE TABLE IF NOT EXISTS unit_aliases(
  old_project TEXT NOT NULL,
  old_uid TEXT NOT NULL,
  new_project TEXT NOT NULL,
  new_uid TEXT NOT NULL,
  move_id TEXT NOT NULL,
  moved_at TEXT NOT NULL,
  moved_by TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(old_project, old_uid)
);
CREATE INDEX IF NOT EXISTS unit_aliases_move ON unit_aliases(move_id);`

func ensureAliasTable(ctx context.Context, q sqlitedb.Querier) error {
	_, err := q.ExecContext(ctx, aliasSchema)
	return err
}

// Alias is where an old uid lives now ("" when no alias is known).
func (s *Store) Alias(ctx context.Context, project, uid string) (newProject, newUID string, err error) {
	err = s.read(ctx, func(q sqlitedb.Querier) error {
		if _, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT 1 FROM sqlite_master WHERE type='table' AND name='unit_aliases'"); err != nil || !ok {
			return err
		}
		row, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT new_project, new_uid FROM unit_aliases WHERE old_project=? AND old_uid=?", project, uid)
		if ok {
			newProject, newUID = row.Str("new_project"), row.Str("new_uid")
		}
		return err
	})
	return
}

// MoveRequest selects what to move.
type MoveRequest struct {
	Units        []string // uids or ids in the source (a split: branch roots)
	WithChildren bool     // add every part_of descendant
	All          bool     // a merge: every live unit of the source
	NewProject   bool     // the target was created for this move (its counters follow the source)
	CrossLinks   string   // "keep" (default): cut links become external_links; "drop"
	Exclude      []string // uids a merge leaves behind (the plan's own decision unit)
	PlanID       string   // the plan carried out (journal only)
	SourceID     string
	SourceName   string
	TargetID     string // "" with Target == nil: a new, empty project
	TargetName   string
}

type moveUnit struct {
	row            sqlitedb.Row
	id             int64
	oldUID, newUID string
	newID          int64
}

type moveMedia struct {
	row      sqlitedb.Row
	oldID    int64
	owner    int64 // source unit id the copy is attached to
	borrowed bool  // attached to a unit that stays: copied for a reference
	newURL   string
	newID    int64
}

type moveFile struct {
	src, rel string
	size     int64
}

type crossLink struct {
	row      sqlitedb.Row
	moved    int64 // the moved end (source unit id)
	other    sqlitedb.Row
	outgoing bool // the moved unit is the link's source
}

type movePlan struct {
	req          MoveRequest
	orig         MoveRequest // the request as given (req.Units is filled in for a merge)
	units        []*moveUnit
	byID         map[int64]*moveUnit
	uidMap       map[string]string
	staying      map[string]bool
	renumbered   int64
	links        []sqlitedb.Row
	cross        []crossLink
	media        []*moveMedia
	mediaByOld   map[int64]*moveMedia
	files        map[string]*moveFile // source url -> file
	comments     []sqlitedb.Row
	tags         map[int64][]string
	members      []sqlitedb.Row
	counters     map[string]int64
	plainRefs    int64
	missingMedia int64
	history      int64
	historyRows  []sqlitedb.Row
	reviewGroups int64
	srcChanges   int64
	dstChanges   int64
	dstPath      string
	hash         string
}

var (
	moveRefRe   = regexp.MustCompile(`(\\?)\[\[([A-Za-z0-9][A-Za-z0-9_-]*)\]\]`)
	moveMediaRe = regexp.MustCompile(`(media:|/api/media/)(\d+)(/raw)?`)
	plainUIDRe  = regexp.MustCompile(`\b([A-Z]{1,3}-\d{4})\b`)
	moveFenceRe = regexp.MustCompile("^[ \t]{0,3}(`{3,}|~{3,})")
)

// rewriteMoved rewrites [[UID]] and media references outside fenced code.
func (p *movePlan) rewrite(text string) string {
	lines := strings.SplitAfter(text, "\n")
	fence := ""
	for i, line := range lines {
		if m := moveFenceRe.FindStringSubmatch(line); m != nil {
			mark := m[1][:1]
			if fence == "" {
				fence = mark
			} else if fence == mark {
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		line = moveRefRe.ReplaceAllStringFunc(line, func(s string) string {
			m := moveRefRe.FindStringSubmatch(s)
			if m[1] != "" {
				return s
			}
			if n, ok := p.uidMap[m[2]]; ok {
				return "[[" + n + "]]"
			}
			if p.staying[m[2]] {
				return "[[" + p.req.SourceID + "/" + m[2] + "]]"
			}
			return s
		})
		line = moveMediaRe.ReplaceAllStringFunc(line, func(s string) string {
			m := moveMediaRe.FindStringSubmatch(s)
			if m[1] == "/api/media/" && m[3] == "" {
				return s
			}
			n, _ := strconv.ParseInt(m[2], 10, 64)
			if mm, ok := p.mediaByOld[n]; ok && mm.newID > 0 {
				return m[1] + strconv.FormatInt(mm.newID, 10) + m[3]
			}
			return s
		})
		lines[i] = line
	}
	return strings.Join(lines, "")
}

func changeCount(ctx context.Context, q sqlitedb.Querier) (int64, error) {
	r, _, err := sqlitedb.QueryOne(ctx, q, "SELECT COALESCE(MAX(n),0) n FROM change_counter")
	return r.Int("n"), err
}

// readSource fills the source side of the plan from q.
func (p *movePlan) readSource(ctx context.Context, q sqlitedb.Querier) error {
	p.byID = map[int64]*moveUnit{}
	var err error
	if p.srcChanges, err = changeCount(ctx, q); err != nil {
		return err
	}
	queue := []int64{}
	if p.req.All {
		rows, err := sqlitedb.Query(ctx, q, "SELECT uid FROM units WHERE archived=0 ORDER BY id")
		if err != nil {
			return err
		}
		skip := map[string]bool{}
		for _, x := range p.req.Exclude {
			skip[x] = true
		}
		p.req.Units = nil
		for _, r := range rows {
			if !skip[r.Str("uid")] {
				p.req.Units = append(p.req.Units, r.Str("uid"))
			}
		}
	}
	for _, ref := range p.req.Units {
		row, err := unitRowAny(ctx, q, ref)
		if err != nil {
			return err
		}
		if row.Int("archived") != 0 {
			return storeErr("unit %s is in the recycle bin", row.Str("uid"))
		}
		if _, ok := p.byID[row.Int("id")]; !ok {
			p.byID[row.Int("id")] = &moveUnit{row: row, id: row.Int("id"), oldUID: row.Str("uid")}
			queue = append(queue, row.Int("id"))
		}
	}
	if len(p.byID) == 0 {
		return storeErr("select at least one unit to move")
	}
	if p.req.WithChildren && !p.req.All {
		// Descendants as in SplitClusters: part_of, else derived_from.
		parents, err := treeParents(ctx, q)
		if err != nil {
			return err
		}
		children := childrenOf(parents)
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			for _, c := range children[id] {
				if _, ok := p.byID[c]; ok {
					continue
				}
				row, err := unitRowAny(ctx, q, c)
				if err != nil {
					return err
				}
				p.byID[c] = &moveUnit{row: row, id: c, oldUID: row.Str("uid")}
				queue = append(queue, c)
			}
		}
	}
	for _, u := range p.byID {
		p.units = append(p.units, u)
	}
	sort.Slice(p.units, func(i, j int) bool { return p.units[i].id < p.units[j].id })
	ids := make([]any, len(p.units))
	for i, u := range p.units {
		ids[i] = u.id
	}
	in := "(" + sqlitedb.Marks(len(ids)) + ")"

	p.staying = map[string]bool{}
	rows, err := sqlitedb.Query(ctx, q, "SELECT id, uid FROM units WHERE archived=0")
	if err != nil {
		return err
	}
	for _, r := range rows {
		if _, ok := p.byID[r.Int("id")]; !ok {
			p.staying[r.Str("uid")] = true
		}
	}

	links, err := sqlitedb.Query(ctx, q, "SELECT * FROM unit_links WHERE source_id IN "+in+" OR target_id IN "+in+" ORDER BY id", append(ids, ids...)...)
	if err != nil {
		return err
	}
	for _, l := range links {
		_, s := p.byID[l.Int("source_id")]
		_, t := p.byID[l.Int("target_id")]
		if s && t {
			p.links = append(p.links, l)
			continue
		}
		c := crossLink{row: l, outgoing: s, moved: l.Int("source_id")}
		otherID := l.Int("target_id")
		if !s {
			c.moved, otherID = l.Int("target_id"), l.Int("source_id")
		}
		other, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT id, uid, title, archived, meta_json FROM units WHERE id=?", otherID)
		if err != nil {
			return err
		}
		if ok {
			c.other = other
			p.cross = append(p.cross, c)
		}
	}

	p.mediaByOld = map[int64]*moveMedia{}
	p.files = map[string]*moveFile{}
	own, err := sqlitedb.Query(ctx, q, "SELECT * FROM unit_media WHERE unit_id IN "+in+" ORDER BY id", ids...)
	if err != nil {
		return err
	}
	for _, m := range own {
		mm := &moveMedia{row: m, oldID: m.Int("id"), owner: m.Int("unit_id")}
		p.media = append(p.media, mm)
		p.mediaByOld[mm.oldID] = mm
	}
	if p.comments, err = sqlitedb.Query(ctx, q, "SELECT * FROM unit_comments WHERE unit_id IN "+in+" ORDER BY id", ids...); err != nil {
		return err
	}
	// Media other units carry but moved bodies and comments show.
	borrow := func(unitID int64, text string) error {
		for _, ref := range mediaRefs(text) {
			if _, ok := p.mediaByOld[ref]; ok {
				continue
			}
			m, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT * FROM unit_media WHERE id=?", ref)
			if err != nil {
				return err
			}
			if !ok {
				p.missingMedia++
				continue
			}
			mm := &moveMedia{row: m, oldID: ref, owner: unitID, borrowed: true}
			p.media = append(p.media, mm)
			p.mediaByOld[ref] = mm
		}
		return nil
	}
	for _, u := range p.units {
		body := u.row.Str("body")
		if err := borrow(u.id, body); err != nil {
			return err
		}
		p.plainRefs += countPlain(body)
	}
	for _, c := range p.comments {
		if err := borrow(c.Int("unit_id"), c.Str("body")); err != nil {
			return err
		}
	}
	for _, mm := range p.media {
		url := mm.row.Str("url")
		if !projectRelative(url) {
			continue
		}
		if _, ok := p.files[url]; ok {
			continue
		}
		src := filepath.Join(srcPath(ctx), filepath.FromSlash(url))
		f := &moveFile{src: src, rel: filepath.ToSlash(filepath.Clean(filepath.FromSlash(url)))}
		if fi, err := os.Stat(src); err == nil && fi.Mode().IsRegular() {
			f.size = fi.Size()
		} else {
			f.src = "" // missing on disk: the row is copied, the file is not
		}
		p.files[url] = f
	}

	p.tags = map[int64][]string{}
	tagRows, err := sqlitedb.Query(ctx, q, "SELECT unit_id, tag FROM unit_tags WHERE unit_id IN "+in+" ORDER BY unit_id, tag", ids...)
	if err != nil {
		return err
	}
	for _, r := range tagRows {
		p.tags[r.Int("unit_id")] = append(p.tags[r.Int("unit_id")], r.Str("tag"))
	}
	if p.members, err = sqlitedb.Query(ctx, q, "SELECT * FROM topic_members WHERE topic_id IN "+in+" AND unit_id IN "+in, append(ids, ids...)...); err != nil {
		return err
	}
	if p.historyRows, err = sqlitedb.Query(ctx, q, "SELECT * FROM node_text_history WHERE unit_id IN "+in+" ORDER BY unit_id", ids...); err != nil {
		return err
	}
	for _, r := range p.historyRows {
		if r.Int("enabled") != 0 {
			p.history++
		}
	}
	if ok, err := hasReviewTables(ctx, q); err == nil && ok {
		r, _, err := sqlitedb.QueryOne(ctx, q, "SELECT COUNT(*) n FROM review_group_members WHERE unit_id IN "+in, ids...)
		if err != nil {
			return err
		}
		p.reviewGroups = r.Int("n")
	}
	counters, err := sqlitedb.Query(ctx, q, "SELECT prefix, n FROM counters")
	if err != nil {
		return err
	}
	p.counters = map[string]int64{}
	for _, c := range counters {
		p.counters[c.Str("prefix")] = c.Int("n")
	}
	return nil
}

type srcPathKey struct{}

func srcPath(ctx context.Context) string { s, _ := ctx.Value(srcPathKey{}).(string); return s }

func countPlain(text string) int64 {
	stripped := moveRefRe.ReplaceAllString(text, "")
	return int64(len(plainUIDRe.FindAllString(stripped, -1)))
}

// readTarget numbers the moved units for the target (nil: a new project).
func (p *movePlan) readTarget(ctx context.Context, q sqlitedb.Querier) error {
	taken := map[string]bool{}
	counters := map[string]int64{}
	if q != nil {
		var err error
		if p.dstChanges, err = changeCount(ctx, q); err != nil {
			return err
		}
		rows, err := sqlitedb.Query(ctx, q, "SELECT uid FROM units")
		if err != nil {
			return err
		}
		for _, r := range rows {
			taken[r.Str("uid")] = true
		}
		crows, err := sqlitedb.Query(ctx, q, "SELECT prefix, n FROM counters")
		if err != nil {
			return err
		}
		for _, c := range crows {
			counters[c.Str("prefix")] = c.Int("n")
		}
	}
	// D-0015: every uid is kept when none of them is taken in the target (a
	// split into a new project keeps all numbers); one clash renumbers the
	// whole batch from the target's counters, in source id order.  The old
	// uids go into unit_aliases either way.
	p.uidMap = map[string]string{}
	clash := false
	for _, u := range p.units {
		if taken[u.oldUID] {
			clash = true
			break
		}
	}
	p.renumbered = 0
	for _, u := range p.units {
		if !clash {
			u.newUID = u.oldUID
			continue
		}
		prefix := typePrefix(u.row.Str("type"))
		n := counters[prefix] + 1
		for taken[prefix+"-"+pad4(n)] {
			n++
		}
		counters[prefix] = n
		u.newUID = prefix + "-" + pad4(n)
		taken[u.newUID] = true
		if u.newUID != u.oldUID {
			p.renumbered++
		}
	}
	for _, u := range p.units {
		p.uidMap[u.oldUID] = u.newUID
	}
	p.hash = p.digest()
	return nil
}

// digest is the plan_hash: what the plan moves, as content (planDeps:
// unit versions, links, media, comments, tags), so a later write elsewhere
// in either project does not make an approved plan stale, but any change to
// what moves does.  The new uids are not part of it: they are numbered
// again when the plan is applied.
func (p *movePlan) digest() string { return p.deps().digest() }

func (s *Store) projectID() string { return pyjson.Str(s.Manifest.Get("id")) }

func (s *Store) planMove(ctx context.Context, dst *Store, req MoveRequest) (*movePlan, error) {
	if dst == s {
		return nil, storeErr("the target is the source project")
	}
	switch req.CrossLinks {
	case "":
		req.CrossLinks = "keep"
	case "keep", "drop":
	default:
		return nil, storeErr("cross_links must be keep or drop")
	}
	p := &movePlan{req: req, orig: req}
	ctx = context.WithValue(ctx, srcPathKey{}, s.Path)
	if err := s.read(ctx, func(q sqlitedb.Querier) error { return p.readSource(ctx, q) }); err != nil {
		return nil, err
	}
	if dst == nil {
		return p, p.readTarget(ctx, nil)
	}
	p.dstPath = dst.Path
	return p, dst.read(ctx, func(q sqlitedb.Querier) error { return p.readTarget(ctx, q) })
}

// PlanMove is the dry run: what ApplyMove would do, and the plan_hash it
// must be given.
func (s *Store) PlanMove(ctx context.Context, dst *Store, req MoveRequest) (*pyjson.Object, error) {
	p, err := s.planMove(ctx, dst, req)
	if err != nil {
		return nil, err
	}
	return p.summary(), nil
}

func (p *movePlan) summary() *pyjson.Object {
	units := []any{}
	for _, u := range p.units {
		units = append(units, pyjson.NewObject("uid", u.oldUID, "new_uid", u.newUID, "type", u.row.Get("type"), "title", u.row.Get("title")))
	}
	cross := []any{}
	for _, c := range p.cross {
		cross = append(cross, pyjson.NewObject("uid", p.byID[c.moved].oldUID, "relation", c.row.Get("relation"),
			"direction", map[bool]string{true: "out", false: "in"}[c.outgoing], "other_uid", c.other.Get("uid"),
			"other_title", c.other.Get("title")))
	}
	var bytes, files, missing int64
	for _, f := range p.files {
		if f.src == "" {
			missing++
			continue
		}
		files++
		bytes += f.size
	}
	borrowed := int64(0)
	for _, m := range p.media {
		if m.borrowed {
			borrowed++
		}
	}
	return pyjson.NewObject(
		"plan_hash", p.hash, "source", p.req.SourceID, "target", p.req.TargetID, "new_project", p.req.TargetID == "",
		"mode", map[bool]string{true: "merge", false: "split"}[p.req.All], "cross_link_mode", p.req.CrossLinks,
		"renumbered", p.renumbered, "units", units, "unit_count", int64(len(p.units)),
		"links", int64(len(p.links)), "cross_links", cross,
		"media", int64(len(p.media)), "media_borrowed", borrowed, "files", files, "bytes", bytes, "files_missing", missing,
		"media_refs_missing", p.missingMedia, "comments", int64(len(p.comments)), "topic_members", int64(len(p.members)),
		"plain_refs", p.plainRefs, "text_history", p.history, "review_groups", p.reviewGroups)
}

// MoveJournal is <source>/moves/<id>/journal.json.
type MoveJournal struct {
	ID         string            `json:"id"`
	State      string            `json:"state"` // started, source_done, done, failed, undone
	Source     string            `json:"source"`
	SourceName string            `json:"source_name"`
	Target     string            `json:"target"`
	TargetName string            `json:"target_name"`
	TargetPath string            `json:"target_path"`
	At         string            `json:"at"`
	By         string            `json:"by"`
	UIDs       map[string]string `json:"uids"`  // old -> new
	Files      []string          `json:"files"` // created in the target, relative
	Error      string            `json:"error,omitempty"`
	PlanID     string            `json:"plan,omitempty"` // the move plan it carries out (moveplans.go)
	Mode       string            `json:"mode,omitempty"` // split or merge
}

func moveDir(projectPath, id string) string { return filepath.Join(projectPath, "moves", id) }

func (j *MoveJournal) save(projectPath string) error {
	dir := moveDir(projectPath, j.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "journal.json"), []byte(pyjson.DumpsIndent(journalObject(j), 2)), 0o644)
}

func journalObject(j *MoveJournal) *pyjson.Object {
	uids := pyjson.NewObject()
	keys := make([]string, 0, len(j.UIDs))
	for k := range j.UIDs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		uids.Set(k, j.UIDs[k])
	}
	files := make([]any, len(j.Files))
	for i, f := range j.Files {
		files[i] = f
	}
	return pyjson.NewObject("id", j.ID, "state", j.State, "source", j.Source, "source_name", j.SourceName,
		"target", j.Target, "target_name", j.TargetName, "target_path", j.TargetPath, "at", j.At, "by", j.By,
		"uids", uids, "files", files, "error", j.Error, "plan", j.PlanID, "mode", j.Mode)
}

// LoadMoveJournal reads a move's journal from the source project.
func LoadMoveJournal(projectPath, id string) (*MoveJournal, error) {
	if !regexp.MustCompile(`^mv_[0-9a-f]{12}$`).MatchString(id) {
		return nil, storeErr("unknown move: %s", id)
	}
	raw, err := os.ReadFile(filepath.Join(moveDir(projectPath, id), "journal.json"))
	if err != nil {
		return nil, storeErr("unknown move: %s", id)
	}
	o, ok := pyjson.LoadsObject(string(raw))
	if !ok {
		return nil, storeErr("unreadable move journal: %s", id)
	}
	j := &MoveJournal{ID: pyjson.Str(o.Value("id")), State: pyjson.Str(o.Value("state")), Source: pyjson.Str(o.Value("source")),
		SourceName: pyjson.Str(o.Value("source_name")), Target: pyjson.Str(o.Value("target")), TargetName: pyjson.Str(o.Value("target_name")),
		TargetPath: pyjson.Str(o.Value("target_path")), At: pyjson.Str(o.Value("at")), By: pyjson.Str(o.Value("by")), UIDs: map[string]string{},
		PlanID: str(o, "plan"), Mode: str(o, "mode"), Error: str(o, "error")}
	if u, ok := o.Value("uids").(*pyjson.Object); ok {
		for _, k := range u.Keys() {
			j.UIDs[k] = pyjson.Str(u.Value(k))
		}
	}
	if f, ok := o.Value("files").([]any); ok {
		for _, x := range f {
			j.Files = append(j.Files, pyjson.Str(x))
		}
	}
	return j, nil
}

func newMoveID() string {
	b := make([]byte, 6)
	rand.Read(b) //nolint:errcheck
	return "mv_" + hex.EncodeToString(b)
}

// moveFailHook lets tests fail a step ("files", "source", "target").
var moveFailHook func(step string) error

func failAt(step string) error {
	if moveFailHook != nil {
		return moveFailHook(step)
	}
	return nil
}

// ApplyMove moves the planned units from s into dst.  planHash is the
// PlanMove hash the user saw; a changed project refuses with a store error.
func (s *Store) ApplyMove(ctx context.Context, dst *Store, req MoveRequest, planHash, actor string) (*pyjson.Object, error) {
	if dst == nil {
		return nil, storeErr("the target project is missing")
	}
	p, err := s.planMove(ctx, dst, req)
	if err != nil {
		return nil, err
	}
	if planHash == "" || p.hash != planHash {
		return nil, &MoveStaleError{}
	}
	now := clock.UTCNow()
	j := &MoveJournal{ID: newMoveID(), State: "started", Source: req.SourceID, SourceName: req.SourceName, Target: req.TargetID,
		TargetName: req.TargetName, TargetPath: dst.Path, At: now, By: actor, UIDs: p.uidMap, PlanID: req.PlanID,
		Mode: map[bool]string{true: "merge", false: "split"}[req.All]}
	if err := j.save(s.Path); err != nil {
		return nil, err
	}
	dir := moveDir(s.Path, j.ID)
	if err := sqlitedb.BackupFile(ctx, filepath.Join(s.Path, "ark.sqlite3"), filepath.Join(dir, "source.sqlite3")); err != nil {
		return nil, err
	}
	if err := sqlitedb.BackupFile(ctx, filepath.Join(dst.Path, "ark.sqlite3"), filepath.Join(dir, "target.sqlite3")); err != nil {
		return nil, err
	}
	fail := func(err error) (*pyjson.Object, error) {
		for _, rel := range j.Files {
			os.Remove(filepath.Join(dst.Path, filepath.FromSlash(rel)))
		}
		j.State, j.Error = "failed", err.Error()
		j.save(s.Path) //nolint:errcheck
		return nil, err
	}
	// Files.
	used := map[string]bool{}
	urls := make([]string, 0, len(p.files))
	for url := range p.files {
		urls = append(urls, url)
	}
	sort.Strings(urls)
	newURL := map[string]string{}
	for _, url := range urls {
		f := p.files[url]
		rel := freeRel(dst.Path, f.rel, used)
		newURL[url] = rel
		if f.src == "" {
			continue
		}
		if err := linkFile(f.src, filepath.Join(dst.Path, filepath.FromSlash(rel))); err != nil {
			return fail(err)
		}
		j.Files = append(j.Files, rel)
		// Every file is in the journal before the next one is made, so a
		// restart can remove them all.
		if len(j.Files)%50 == 0 {
			if err := j.save(s.Path); err != nil {
				return fail(err)
			}
		}
	}
	for _, m := range p.media {
		m.newURL = m.row.Str("url")
		if n, ok := newURL[m.newURL]; ok {
			m.newURL = n
		}
	}
	// Text history: the target fetches the source's revisions, so the
	// copied heads stay valid there.
	if len(p.historyRows) > 0 {
		if err := dst.history().Import(ctx, s.history().Path); err != nil {
			return fail(err)
		}
	}
	if err := failAt("files"); err != nil {
		return fail(err)
	}
	if err := j.save(s.Path); err != nil {
		return fail(err)
	}
	// Source: tombstones and aliases.
	if err := s.moveOutTx(ctx, p, j, actor); err != nil {
		return fail(err)
	}
	j.State = "source_done"
	j.save(s.Path)                          //nolint:errcheck
	if err := failAt("crash"); err != nil { // tests: the process stops here (no rollback)
		return nil, err
	}
	// Target.
	err = dst.moveInTx(ctx, p, j, actor)
	if err == nil {
		err = failAt("target")
	}
	if err != nil {
		if uerr := s.restoreSource(ctx, j, actor); uerr != nil {
			err = errors.Join(err, fmt.Errorf("source rollback failed (backup in %s): %w", dir, uerr))
		}
		if uerr := dst.removeMovedIn(ctx, j); uerr != nil {
			err = errors.Join(err, fmt.Errorf("target cleanup failed (backup in %s): %w", dir, uerr))
		}
		return fail(err)
	}
	j.State = "done"
	if err := j.save(s.Path); err != nil {
		return nil, err
	}
	out := p.summary()
	out.Set("move_id", j.ID).Set("state", j.State)
	return out, nil
}

// MoveStaleError: what the plan covers changed since it was shown (HTTP
// 409); Reasons name what (empty: only the plan_hash differs).
type MoveStaleError struct {
	Reasons []StaleReason
}

func (e *MoveStaleError) Error() string {
	if len(e.Reasons) == 0 {
		return "the projects changed since this preview; preview the move again"
	}
	parts := make([]string, len(e.Reasons))
	for i, r := range e.Reasons {
		parts[i] = r.message()
	}
	return "the plan is out of date: " + strings.Join(parts, "; ")
}

// Units are the source uids the reasons name.
func (e *MoveStaleError) Units() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, r := range e.Reasons {
		for _, u := range r.Units {
			if !seen[u] {
				seen[u] = true
				out = append(out, u)
			}
		}
	}
	return out
}

func (s *Store) moveOutTx(ctx context.Context, p *movePlan, j *MoveJournal, actor string) error {
	return s.write(ctx, func(w *wtx) error {
		w.full = true
		if n, err := changeCount(ctx, w.q); err != nil {
			return err
		} else if n != p.srcChanges {
			// Something was written since the plan was read: stale only
			// when it touched what moves.
			fresh := &movePlan{req: p.orig, orig: p.orig}
			if err := fresh.readSource(context.WithValue(ctx, srcPathKey{}, s.Path), w.q); err != nil {
				return &MoveStaleError{Reasons: []StaleReason{{Code: "unavailable", Detail: err.Error()}}}
			}
			if r := diffDeps(p.deps(), fresh.deps()); len(r) > 0 {
				return &MoveStaleError{Reasons: r}
			}
		}
		if err := failAt("source"); err != nil {
			return err
		}
		if err := ensureAliasTable(ctx, w.q); err != nil {
			return err
		}
		now := clock.UTCNow()
		for _, u := range p.units {
			links, err := sqlitedb.Query(ctx, w.q, `SELECT l.id, l.source_id, l.target_id, l.relation, l.note, l.created_at,
                   l.created_by, s.uid suid, t.uid tuid FROM unit_links l JOIN units s ON s.id=l.source_id
                   JOIN units t ON t.id=l.target_id WHERE l.source_id=? OR l.target_id=? ORDER BY l.id`, u.id, u.id)
			if err != nil {
				return err
			}
			if _, err := w.q.ExecContext(ctx, "DELETE FROM unit_links WHERE source_id=? OR target_id=?", u.id, u.id); err != nil {
				return err
			}
			meta := parseDetail(u.row.Get("meta_json"))
			meta.Set("moved_to", pyjson.NewObject("project", j.Target, "project_name", j.TargetName, "uid", u.newUID,
				"move_id", j.ID, "at", now))
			if _, err := w.q.ExecContext(ctx, "UPDATE units SET archived=1, meta_json=?, updated_at=? WHERE id=?",
				string(pyjson.Dumps(meta)), now, u.id); err != nil {
				return err
			}
			if err := syncNeeds(w, u.row, true, actor); err != nil {
				return err
			}
			if err := w.log(actor, "unit.archive", u.id, pyjson.NewObject("uid", u.oldUID, "title", u.row.Get("title"),
				"links_deleted", int64(len(links)), "links", trashLinks(links),
				"moved_to", pyjson.NewObject("project", j.Target, "project_name", j.TargetName, "uid", u.newUID, "move_id", j.ID))); err != nil {
				return err
			}
			if _, err := w.q.ExecContext(ctx, `INSERT OR REPLACE INTO unit_aliases(old_project,old_uid,new_project,new_uid,move_id,moved_at,moved_by)
                   VALUES(?,?,?,?,?,?,?)`, j.Source, u.oldUID, j.Target, u.newUID, j.ID, now, actor); err != nil {
				return err
			}
		}
		// The staying end of each cut link remembers it (unless dropped).
		for _, c := range p.cross {
			if p.req.CrossLinks == "drop" {
				break
			}
			if c.other.Int("archived") != 0 {
				continue
			}
			if err := addExternal(ctx, w.q, c.other.Int("id"), pyjson.NewObject("relation", c.row.Get("relation"),
				"direction", map[bool]string{true: "in", false: "out"}[c.outgoing], "project", j.Target, "project_name", j.TargetName,
				"uid", p.byID[c.moved].newUID, "title", p.byID[c.moved].row.Get("title"), "move_id", j.ID)); err != nil {
				return err
			}
		}
		return w.log(actor, "project.move_out", nil, pyjson.NewObject("move_id", j.ID, "target", j.Target,
			"target_name", j.TargetName, "units", int64(len(p.units))))
	})
}

// addExternal appends a cross-project link record to a unit's meta_json.
func addExternal(ctx context.Context, q sqlitedb.Querier, unitID int64, link *pyjson.Object) error {
	row, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT meta_json FROM units WHERE id=?", unitID)
	if err != nil || !ok {
		return err
	}
	meta := parseDetail(row.Get("meta_json"))
	list, _ := meta.Value("external_links").([]any)
	meta.Set("external_links", append(list, link))
	_, err = q.ExecContext(ctx, "UPDATE units SET meta_json=? WHERE id=?", string(pyjson.Dumps(meta)), unitID)
	return err
}

// dropExternal removes a move's cross-project link records from every unit.
func dropExternal(ctx context.Context, q sqlitedb.Querier, moveID string) error {
	rows, err := sqlitedb.Query(ctx, q, "SELECT id, meta_json FROM units WHERE meta_json LIKE ?", "%"+moveID+"%")
	if err != nil {
		return err
	}
	for _, r := range rows {
		meta := parseDetail(r.Get("meta_json"))
		list, _ := meta.Value("external_links").([]any)
		keep := []any{}
		for _, item := range list {
			if o, ok := item.(*pyjson.Object); ok && pyjson.Str(o.Value("move_id")) == moveID {
				continue
			}
			keep = append(keep, item)
		}
		if len(keep) == len(list) {
			continue
		}
		if len(keep) == 0 {
			meta.Delete("external_links")
		} else {
			meta.Set("external_links", keep)
		}
		if _, err := q.ExecContext(ctx, "UPDATE units SET meta_json=? WHERE id=?", string(pyjson.Dumps(meta)), r.Int("id")); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) moveInTx(ctx context.Context, p *movePlan, j *MoveJournal, actor string) error {
	return s.write(ctx, func(w *wtx) error {
		w.full = true
		if n, err := changeCount(ctx, w.q); err != nil {
			return err
		} else if n != p.dstChanges {
			// Units were written here since the uids were numbered: only a
			// uid taken in the meantime stops the move.
			var taken []string
			for _, u := range p.units {
				if _, ok, err := sqlitedb.QueryOne(ctx, w.q, "SELECT 1 FROM units WHERE uid=?", u.newUID); err != nil {
					return err
				} else if ok {
					taken = append(taken, u.newUID)
				}
			}
			if len(taken) > 0 {
				return &MoveStaleError{Reasons: []StaleReason{{Code: "ids_taken", Units: taken}}}
			}
		}
		if err := ensureAliasTable(ctx, w.q); err != nil {
			return err
		}
		now := clock.UTCNow()
		// Units first (bodies are rewritten once media ids are known).
		for _, u := range p.units {
			r := u.row
			meta := parseDetail(r.Get("meta_json"))
			meta.Delete("external_links")
			meta.Set("moved_from", pyjson.NewObject("project", j.Source, "project_name", j.SourceName, "uid", u.oldUID,
				"move_id", j.ID, "at", now))
			sourceRef := r.Get("source_ref")
			if sourceRef != nil {
				if _, taken, err := sqlitedb.QueryOne(ctx, w.q, "SELECT 1 FROM units WHERE source_ref=?", sourceRef); err != nil {
					return err
				} else if taken {
					sourceRef = nil
				}
			}
			res, err := w.q.ExecContext(ctx, `INSERT INTO units(uid,type,title,body,status,importance,start_at,end_at,created_at,
                   updated_at,origin,source_ref,source_title,meta_json,archived,assignee) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,?)`,
				u.newUID, r.Get("type"), r.Get("title"), "", r.Get("status"), r.Get("importance"), r.Get("start_at"), r.Get("end_at"),
				r.Get("created_at"), now, r.Get("origin"), sourceRef, r.Get("source_title"), string(pyjson.Dumps(meta)), r.Get("assignee"))
			if err != nil {
				return err
			}
			if u.newID, err = res.LastInsertId(); err != nil {
				return err
			}
			for _, tag := range p.tags[u.id] {
				if _, err := w.q.ExecContext(ctx, "INSERT OR IGNORE INTO unit_tags(unit_id,tag) VALUES(?,?)", u.newID, tag); err != nil {
					return err
				}
			}
		}
		for _, m := range p.media {
			r := m.row
			res, err := w.q.ExecContext(ctx, "INSERT INTO unit_media(unit_id,url,kind,caption,created_at) VALUES(?,?,?,?,?)",
				p.byID[m.owner].newID, m.newURL, r.Get("kind"), r.Get("caption"), r.Get("created_at"))
			if err != nil {
				return err
			}
			if m.newID, err = res.LastInsertId(); err != nil {
				return err
			}
		}
		for _, u := range p.units {
			if _, err := w.q.ExecContext(ctx, "UPDATE units SET body=? WHERE id=?", p.rewrite(u.row.Str("body")), u.newID); err != nil {
				return err
			}
		}
		for _, r := range p.historyRows {
			if _, err := w.q.ExecContext(ctx, "INSERT INTO node_text_history(unit_id,enabled,head) VALUES(?,?,?)",
				p.byID[r.Int("unit_id")].newID, r.Get("enabled"), r.Get("head")); err != nil {
				return err
			}
		}
		linkMap := map[int64]int64{}
		for _, l := range p.links {
			res, err := w.q.ExecContext(ctx, "INSERT INTO unit_links(source_id,target_id,relation,note,created_at,created_by) VALUES(?,?,?,?,?,?)",
				p.byID[l.Int("source_id")].newID, p.byID[l.Int("target_id")].newID, l.Get("relation"), l.Get("note"), l.Get("created_at"), l.Get("created_by"))
			if err != nil {
				return err
			}
			if linkMap[l.Int("id")], err = res.LastInsertId(); err != nil {
				return err
			}
		}
		commentMap := map[int64]int64{}
		for _, c := range p.comments {
			var reply any
			if c.Get("reply_to") != nil {
				if n, ok := commentMap[c.Int("reply_to")]; ok {
					reply = n
				}
			}
			res, err := w.q.ExecContext(ctx, `INSERT INTO unit_comments(unit_id,author,kind,body,created_at,reply_to,resolved_at,resolved_by)
                   VALUES(?,?,?,?,?,?,?,?)`, p.byID[c.Int("unit_id")].newID, c.Get("author"), c.Get("kind"), p.rewrite(c.Str("body")),
				c.Get("created_at"), reply, c.Get("resolved_at"), c.Get("resolved_by"))
			if err != nil {
				return err
			}
			if commentMap[c.Int("id")], err = res.LastInsertId(); err != nil {
				return err
			}
		}
		for _, m := range p.members {
			parent := m.Get("parent_id")
			if parent != nil && m.Int("parent_id") != 0 {
				if pu, ok := p.byID[m.Int("parent_id")]; ok {
					parent = pu.newID
				} else {
					parent = nil
				}
			}
			if _, err := w.q.ExecContext(ctx, `INSERT INTO topic_members(topic_id,unit_id,mode,position,parent_id,x,y,w,caption,added_at,added_by,updated_at)
                   VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, p.byID[m.Int("topic_id")].newID, p.byID[m.Int("unit_id")].newID, m.Get("mode"),
				m.Get("position"), parent, m.Get("x"), m.Get("y"), m.Get("w"), m.Get("caption"), m.Get("added_at"), m.Get("added_by"), m.Get("updated_at")); err != nil {
				return err
			}
		}
		// Topic subgraphs name hidden links by id.
		for _, u := range p.units {
			if u.row.Str("type") != "topic" {
				continue
			}
			meta := parseDetail(u.row.Get("meta_json"))
			sub, ok := meta.Value("subgraph").(*pyjson.Object)
			if !ok {
				continue
			}
			hidden, _ := sub.Value("hidden_links").([]any)
			if len(hidden) == 0 {
				continue
			}
			mapped := []any{}
			for _, h := range hidden {
				if n, ok := h.(int64); ok {
					if nn, ok := linkMap[n]; ok {
						mapped = append(mapped, nn)
					}
				}
			}
			row, _, err := sqlitedb.QueryOne(ctx, w.q, "SELECT meta_json FROM units WHERE id=?", u.newID)
			if err != nil {
				return err
			}
			newMeta := parseDetail(row.Get("meta_json"))
			if s2, ok := newMeta.Value("subgraph").(*pyjson.Object); ok {
				s2.Set("hidden_links", mapped)
			}
			if _, err := w.q.ExecContext(ctx, "UPDATE units SET meta_json=? WHERE id=?", string(pyjson.Dumps(newMeta)), u.newID); err != nil {
				return err
			}
		}
		for _, c := range p.cross {
			if p.req.CrossLinks == "drop" {
				break
			}
			if err := addExternal(ctx, w.q, p.byID[c.moved].newID, pyjson.NewObject("relation", c.row.Get("relation"),
				"direction", map[bool]string{true: "out", false: "in"}[c.outgoing], "project", j.Source, "project_name", j.SourceName,
				"uid", c.other.Get("uid"), "title", c.other.Get("title"), "move_id", j.ID)); err != nil {
				return err
			}
		}
		if p.req.NewProject {
			for prefix, n := range p.counters {
				if _, err := w.q.ExecContext(ctx, `INSERT INTO counters(prefix,n) VALUES(?,?)
                       ON CONFLICT(prefix) DO UPDATE SET n=MAX(n, excluded.n)`, prefix, n); err != nil {
					return err
				}
			}
		}
		{
			for _, u := range p.units {
				prefix := typePrefix(u.row.Str("type"))
				n, _ := strconv.ParseInt(strings.TrimPrefix(u.newUID, prefix+"-"), 10, 64)
				if _, err := w.q.ExecContext(ctx, `INSERT INTO counters(prefix,n) VALUES(?,?)
                       ON CONFLICT(prefix) DO UPDATE SET n=MAX(n, excluded.n)`, prefix, n); err != nil {
					return err
				}
			}
		}
		for _, u := range p.units {
			if _, err := w.q.ExecContext(ctx, `INSERT OR REPLACE INTO unit_aliases(old_project,old_uid,new_project,new_uid,move_id,moved_at,moved_by)
                   VALUES(?,?,?,?,?,?,?)`, j.Source, u.oldUID, j.Target, u.newUID, j.ID, now, actor); err != nil {
				return err
			}
			if err := w.log(actor, "unit.move_in", u.newID, pyjson.NewObject("uid", u.newUID, "title", u.row.Get("title"),
				"from_project", j.Source, "from_uid", u.oldUID, "move_id", j.ID)); err != nil {
				return err
			}
		}
		return nil
	})
}

// restoreSource takes a move's tombstones out of the recycle bin again.
func (s *Store) restoreSource(ctx context.Context, j *MoveJournal, actor string) error {
	return s.write(ctx, func(w *wtx) error {
		w.full = true
		rows, err := sqlitedb.Query(ctx, w.q, "SELECT * FROM units WHERE archived=1 AND meta_json LIKE ? ORDER BY id", "%"+j.ID+"%")
		if err != nil {
			return err
		}
		var back []sqlitedb.Row
		for _, row := range rows {
			m := movedTo(row)
			if m == nil || pyjson.Str(m.Value("move_id")) != j.ID {
				continue
			}
			meta := parseDetail(row.Get("meta_json"))
			meta.Delete("moved_to")
			if _, err := w.q.ExecContext(ctx, "UPDATE units SET archived=0, meta_json=?, updated_at=? WHERE id=?",
				string(pyjson.Dumps(meta)), clock.UTCNow(), row.Int("id")); err != nil {
				return err
			}
			if err := syncNeeds(w, row, false, actor); err != nil {
				return err
			}
			back = append(back, row)
		}
		// Links come back once every moved unit is live again: a link
		// between two of them was saved with whichever went first.
		for _, row := range back {
			if _, err := restoreTrashLinks(w, row); err != nil {
				return err
			}
			if err := w.log(actor, "unit.restore", row.Int("id"), pyjson.NewObject("uid", row.Get("uid"), "title", row.Get("title"),
				"move_id", j.ID)); err != nil {
				return err
			}
		}
		if err := dropExternal(ctx, w.q, j.ID); err != nil {
			return err
		}
		if err := ensureAliasTable(ctx, w.q); err != nil {
			return err
		}
		_, err = w.q.ExecContext(ctx, "DELETE FROM unit_aliases WHERE move_id=?", j.ID)
		return err
	})
}

// removeMovedIn deletes what a move inserted into the target.
func (s *Store) removeMovedIn(ctx context.Context, j *MoveJournal) error {
	err := s.write(ctx, func(w *wtx) error {
		w.full = true
		rows, err := sqlitedb.Query(ctx, w.q, "SELECT id, meta_json FROM units WHERE meta_json LIKE ?", "%"+j.ID+"%")
		if err != nil {
			return err
		}
		for _, r := range rows {
			from, _ := parseDetail(r.Get("meta_json")).Value("moved_from").(*pyjson.Object)
			if from == nil || pyjson.Str(from.Value("move_id")) != j.ID {
				continue
			}
			if _, err := w.q.ExecContext(ctx, "DELETE FROM units WHERE id=?", r.Int("id")); err != nil {
				return err
			}
		}
		if err := ensureAliasTable(ctx, w.q); err != nil {
			return err
		}
		_, err = w.q.ExecContext(ctx, "DELETE FROM unit_aliases WHERE move_id=?", j.ID)
		return err
	})
	if err != nil {
		return err
	}
	for _, rel := range j.Files {
		os.Remove(filepath.Join(s.Path, filepath.FromSlash(rel)))
	}
	return nil
}

// UndoMove reverses a finished move: the target loses the moved units and
// the files the move created; the source gets its units back.  Units edited
// in the target since the move refuse unless force.
func (s *Store) UndoMove(ctx context.Context, dst *Store, moveID string, force bool, actor string) (*pyjson.Object, error) {
	j, err := LoadMoveJournal(s.Path, moveID)
	if err != nil {
		return nil, err
	}
	if j.State != "done" {
		return nil, storeErr("move %s cannot be undone (state %s)", moveID, j.State)
	}
	if at, err := time.Parse(time.RFC3339, j.At); err == nil && time.Since(at) > MoveUndoWindow {
		return nil, storeErr("move %s is older than %d days and can no longer be undone", moveID, int(MoveUndoWindow.Hours()/24))
	}
	if !force {
		edited := []any{}
		err := dst.read(ctx, func(q sqlitedb.Querier) error {
			rows, err := sqlitedb.Query(ctx, q, "SELECT uid, updated_at, meta_json FROM units WHERE meta_json LIKE ?", "%"+j.ID+"%")
			for _, r := range rows {
				from, _ := parseDetail(r.Get("meta_json")).Value("moved_from").(*pyjson.Object)
				if from != nil && pyjson.Str(from.Value("move_id")) == j.ID && r.Str("updated_at") > pyjson.Str(from.Value("at")) {
					edited = append(edited, r.Str("uid"))
				}
			}
			return err
		})
		if err != nil {
			return nil, err
		}
		if len(edited) > 0 {
			return nil, storeErr("units changed in the target since the move: %s", strings.Join(stringsOf(edited), ", "))
		}
	}
	if err := dst.removeMovedIn(ctx, j); err != nil {
		return nil, err
	}
	if err := s.restoreSource(ctx, j, actor); err != nil {
		return nil, err
	}
	j.State = "undone"
	if err := j.save(s.Path); err != nil {
		return nil, err
	}
	return pyjson.NewObject("move_id", j.ID, "state", j.State, "units", int64(len(j.UIDs))), nil
}

func stringsOf(items []any) []string {
	out := make([]string, len(items))
	for i, x := range items {
		out[i] = pyjson.Str(x)
	}
	return out
}

// MergedError: the project was merged into another one and is read-only
// (HTTP 409 PROJECT_MERGED).
type MergedError struct{ Project, ProjectName string }

func (e *MergedError) Error() string {
	name := e.ProjectName
	if name == "" {
		name = e.Project
	}
	return "this project was merged into " + name + " and is read-only"
}

func (s *Store) refuseMerged() error {
	if s.Manifest == nil {
		return nil
	}
	m, ok := s.Manifest.Get("merged_into").(*pyjson.Object)
	if !ok {
		return nil
	}
	return &MergedError{Project: pyjson.Str(m.Value("project")), ProjectName: pyjson.Str(m.Value("project_name"))}
}

// SetMerged marks the project merged into target (nil clears the mark) and
// rewrites project.json.
func (s *Store) SetMerged(target *pyjson.Object) error {
	encoded, err := s.Manifest.Mutate(func(o *pyjson.Object) error {
		if target == nil {
			o.Delete("merged_into")
		} else {
			o.Set("merged_into", target)
		}
		o.Set("updated", clock.UTCNow())
		return nil
	})
	if err != nil {
		return err
	}
	return WriteManifest(s.Path, encoded)
}

// MoveUndoWindow is how long a finished move can be undone (D-0017) and how
// long its database backups are kept.
var MoveUndoWindow = 30 * 24 * time.Hour

// MoveJournals lists the journals of the moves made from a project.
func MoveJournals(projectPath string) []*MoveJournal {
	entries, _ := os.ReadDir(filepath.Join(projectPath, "moves"))
	var out []*MoveJournal
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "mv_") {
			continue
		}
		if j, err := LoadMoveJournal(projectPath, e.Name()); err == nil {
			out = append(out, j)
		}
	}
	return out
}

// Interrupted is a move the server stopped in the middle of (a restart).
func (j *MoveJournal) Interrupted() bool { return j.State == "started" || j.State == "source_done" }

// RecoverMove rolls back a move a restart interrupted: the source gets its
// units back, the target loses what the move inserted and the files it
// made (dst may be nil when the target project is gone).
func (s *Store) RecoverMove(ctx context.Context, dst *Store, j *MoveJournal) error {
	if !j.Interrupted() {
		return nil
	}
	var errs []error
	if err := s.restoreSource(ctx, j, "system:move-recovery"); err != nil {
		errs = append(errs, err)
	}
	if dst != nil {
		if err := dst.removeMovedIn(ctx, j); err != nil {
			errs = append(errs, err)
		}
	} else {
		for _, rel := range j.Files {
			os.Remove(filepath.Join(j.TargetPath, filepath.FromSlash(rel)))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	j.State, j.Error = "failed", "interrupted by a server restart; rolled back"
	return j.save(s.Path)
}

// CleanMoveBackups deletes the database backups of moves older than the
// undo window (the journal stays).  It returns how many moves it cleaned.
func CleanMoveBackups(projectPath string, now time.Time) int {
	n := 0
	for _, j := range MoveJournals(projectPath) {
		at, err := time.Parse(time.RFC3339, j.At)
		if err != nil || j.Interrupted() || now.Sub(at) <= MoveUndoWindow {
			continue
		}
		cleaned := false
		for _, name := range []string{"source.sqlite3", "target.sqlite3"} {
			if os.Remove(filepath.Join(moveDir(projectPath, j.ID), name)) == nil {
				cleaned = true
			}
		}
		if cleaned {
			n++
		}
	}
	return n
}
