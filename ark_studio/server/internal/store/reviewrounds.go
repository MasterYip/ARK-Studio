package store

// Go only: review requests (docs/agents/working-mode.md, "Review requests").
//
// Every time a unit enters review a review round opens: it records who
// asked, an optional short note saying what to look at (review_note, with
// anchors, see anchors.go) and the baseline the reviewer last saw: the body
// and attachments as they were when a person last accepted or sent the unit
// back, or, without such a decision, when the unit entered review.  Accept,
// Send back (any status change that ends the review) closes the round.
//
// Rounds live in review_rounds, created on first use like agent_messages:
// projects that never use reviews keep the frozen Python schema, and units
// that were in review before this table existed keep working (round numbers
// then come from the activity log).

import (
	"context"
	"crypto/sha256"
	"regexp"
	"strings"
	"sync"

	"arkstudio/go/internal/compat/pydiff"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/sqlitedb"
)

// MaxReviewNote is the longest review_note (characters).
const MaxReviewNote = 500

const reviewSchema = `CREATE TABLE IF NOT EXISTS review_rounds(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  unit_id INTEGER NOT NULL REFERENCES units(id) ON DELETE CASCADE,
  round INTEGER NOT NULL,
  opened_at TEXT NOT NULL,
  opened_by TEXT NOT NULL,
  note TEXT NOT NULL DEFAULT '',
  note_by TEXT NOT NULL DEFAULT '',
  base_kind TEXT NOT NULL DEFAULT 'entered',
  base_at TEXT NOT NULL,
  base_body TEXT NOT NULL DEFAULT '',
  base_media INTEGER NOT NULL DEFAULT 0,
  closed_at TEXT,
  closed_by TEXT,
  outcome TEXT,
  close_body TEXT,
  close_media INTEGER
);
CREATE INDEX IF NOT EXISTS review_rounds_unit ON review_rounds(unit_id, round);
CREATE TABLE IF NOT EXISTS review_groups(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  title TEXT NOT NULL,
  summary TEXT NOT NULL DEFAULT '',
  note TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'review',
  round INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  created_by TEXT NOT NULL,
  opened_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS review_group_members(
  group_id INTEGER NOT NULL REFERENCES review_groups(id) ON DELETE CASCADE,
  unit_id INTEGER NOT NULL REFERENCES units(id) ON DELETE CASCADE,
  position INTEGER NOT NULL DEFAULT 0,
  note TEXT NOT NULL DEFAULT '',
  outcome TEXT NOT NULL DEFAULT '',
  decided_at TEXT,
  decided_by TEXT,
  base_kind TEXT NOT NULL DEFAULT 'entered',
  base_at TEXT NOT NULL DEFAULT '',
  base_body TEXT NOT NULL DEFAULT '',
  base_media INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(group_id, unit_id)
);
CREATE INDEX IF NOT EXISTS review_group_members_unit ON review_group_members(unit_id);
CREATE TABLE IF NOT EXISTS review_group_comments(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  group_id INTEGER NOT NULL REFERENCES review_groups(id) ON DELETE CASCADE,
  round INTEGER NOT NULL DEFAULT 1,
  author TEXT NOT NULL,
  kind TEXT NOT NULL,
  body TEXT NOT NULL,
  created_at TEXT NOT NULL,
  resolved_at TEXT,
  resolved_by TEXT
);`

func ensureReviewTables(ctx context.Context, q sqlitedb.Querier) error {
	_, err := q.ExecContext(ctx, reviewSchema)
	return err
}

func hasReviewTables(ctx context.Context, q sqlitedb.Querier) (bool, error) {
	_, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT 1 FROM sqlite_master WHERE type='table' AND name='review_rounds'")
	return ok, err
}

// isHumanActor: a decision by a person (not an agent, the assistant or a migration).
func isHumanActor(actor string) bool { return strings.HasPrefix(actor, "human:") }

// cleanReviewNote validates a review_note value ("" = none).
func cleanReviewNote(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", storeErr("review_note must be a string")
	}
	text = pytext.Strip(strings.ReplaceAll(text, "\r\n", "\n"))
	if pytext.Len(text) > MaxReviewNote {
		return "", storeErr("review_note is longer than %d characters", MaxReviewNote)
	}
	return text, nil
}

func maxMediaID(ctx context.Context, q sqlitedb.Querier, unitID int64) (int64, error) {
	r, _, err := sqlitedb.QueryOne(ctx, q, "SELECT COALESCE(MAX(id),0) n FROM unit_media WHERE unit_id=?", unitID)
	return r.Int("n"), err
}

// legacyReviewEntries counts how often the activity log saw the unit enter review.
func legacyReviewEntries(ctx context.Context, q sqlitedb.Querier, unitID int64) (int64, error) {
	r, _, err := sqlitedb.QueryOne(ctx, q, `SELECT COUNT(*) n FROM activity WHERE unit_id=? AND action='unit.needs'
                   AND json_extract(detail_json,'$.to')='review'`, unitID)
	return r.Int("n"), err
}

// reviewBaseline is the snapshot the next round compares against: the
// latest one a person closed, else nothing (ok false).
func reviewBaseline(ctx context.Context, q sqlitedb.Querier, unitID int64) (body string, media int64, at string, ok bool, err error) {
	r, found, err := sqlitedb.QueryOne(ctx, q, `SELECT close_body, close_media, closed_at FROM review_rounds
                   WHERE unit_id=? AND closed_at IS NOT NULL AND closed_by LIKE 'human:%' AND close_body IS NOT NULL
                   ORDER BY round DESC, id DESC LIMIT 1`, unitID)
	if err != nil || !found {
		return "", 0, "", false, err
	}
	return r.Str("close_body"), r.Int("close_media"), r.Str("closed_at"), true, nil
}

// openReviewRound starts a round for a unit that now is in review.
// entering is false when the unit was already in review (a legacy unit that
// gets its first note): its activity log already counts that entry.
func openReviewRound(w *wtx, unitID int64, body, note, actor string, entering bool) error {
	ctx, q := w.ctx, w.q
	if err := ensureReviewTables(ctx, q); err != nil {
		return err
	}
	r, _, err := sqlitedb.QueryOne(ctx, q, "SELECT COALESCE(MAX(round),0) n FROM review_rounds WHERE unit_id=?", unitID)
	if err != nil {
		return err
	}
	round := r.Int("n")
	if round == 0 {
		if round, err = legacyReviewEntries(ctx, q, unitID); err != nil {
			return err
		}
		if !entering && round > 0 {
			round--
		}
	}
	round++
	now := clock.UTCNow()
	baseKind, baseAt, baseBody := "entered", now, body
	baseMedia, err := maxMediaID(ctx, q, unitID)
	if err != nil {
		return err
	}
	if b, m, at, ok, err := reviewBaseline(ctx, q, unitID); err != nil {
		return err
	} else if ok {
		baseKind, baseAt, baseBody, baseMedia = "review", at, b, m
	}
	noteBy := ""
	if note != "" {
		noteBy = actor
	}
	_, err = q.ExecContext(ctx, `INSERT INTO review_rounds(unit_id,round,opened_at,opened_by,note,note_by,base_kind,base_at,
                   base_body,base_media) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		unitID, round, now, actor, note, noteBy, baseKind, baseAt, baseBody, baseMedia)
	return err
}

// closeReviewRound ends the open round (outcome: the new needs: value).  A
// unit that entered review before rounds existed gets a closed row, so the
// next round can compare against what the person decided on.
func closeReviewRound(w *wtx, unitID int64, body, outcome, actor string) error {
	ctx, q := w.ctx, w.q
	if err := ensureReviewTables(ctx, q); err != nil {
		return err
	}
	media, err := maxMediaID(ctx, q, unitID)
	if err != nil {
		return err
	}
	now := clock.UTCNow()
	open, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT id FROM review_rounds WHERE unit_id=? AND closed_at IS NULL ORDER BY round DESC LIMIT 1", unitID)
	if err != nil {
		return err
	}
	if !ok {
		n, err := legacyReviewEntries(ctx, q, unitID)
		if err != nil {
			return err
		}
		if n < 1 {
			n = 1
		}
		_, err = q.ExecContext(ctx, `INSERT INTO review_rounds(unit_id,round,opened_at,opened_by,base_at,base_body,base_media,
                   closed_at,closed_by,outcome,close_body,close_media) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			unitID, n, now, "", now, body, media, now, actor, outcome, body, media)
		return err
	}
	_, err = q.ExecContext(ctx, "UPDATE review_rounds SET closed_at=?, closed_by=?, outcome=?, close_body=?, close_media=? WHERE id=?",
		now, actor, outcome, body, media, open.Int("id"))
	return err
}

// openRoundNote is the note of the unit's open round ("" when none; ok false
// without an open round).
func openRoundNote(ctx context.Context, q sqlitedb.Querier, unitID int64) (string, bool, error) {
	if has, err := hasReviewTables(ctx, q); err != nil || !has {
		return "", false, err
	}
	r, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT note FROM review_rounds WHERE unit_id=? AND closed_at IS NULL ORDER BY round DESC LIMIT 1", unitID)
	return r.Str("note"), ok, err
}

// reviewTransition applies the round side of a needs: change (and a new
// note).  note == nil: no review_note in the request.
func reviewTransition(w *wtx, unitID int64, oldNeeds, newNeeds, body, actor string, note *string) error {
	switch {
	case newNeeds == "review" && oldNeeds != "review":
		text := ""
		if note != nil {
			text = *note
		}
		return openReviewRound(w, unitID, body, text, actor, true)
	case oldNeeds == "review" && newNeeds != "review":
		return closeReviewRound(w, unitID, body, newNeeds, actor)
	case newNeeds == "review" && note != nil:
		_, ok, err := openRoundNote(w.ctx, w.q, unitID)
		if err != nil {
			return err
		}
		if !ok {
			return openReviewRound(w, unitID, body, *note, actor, false)
		}
		noteBy := actor
		if *note == "" {
			noteBy = ""
		}
		_, err = w.q.ExecContext(w.ctx, "UPDATE review_rounds SET note=?, note_by=? WHERE unit_id=? AND closed_at IS NULL", *note, noteBy, unitID)
		return err
	}
	return nil
}

// ------------------------------------------------------------------ diff

// reviewDiffContext is how many unchanged lines stay around a change.
const reviewDiffContext = 2

type reviewDiffResult struct {
	hunks          []any
	added, removed int64
	addedText      string // the added lines, for the "what changed" fallback
}

var reviewDiffCache = struct {
	sync.Mutex
	m map[[32]byte]reviewDiffResult
}{m: map[[32]byte]reviewDiffResult{}}

// reviewDiff is a line diff of base -> body: hunks of {op: same|add|del|skip,
// lines} (skip: {op, n} for unchanged lines left out), cached by content.
func reviewDiff(base, body string) reviewDiffResult {
	h := sha256.New()
	h.Write([]byte(base))
	h.Write([]byte{0})
	h.Write([]byte(body))
	var key [32]byte
	copy(key[:], h.Sum(nil))
	reviewDiffCache.Lock()
	if r, ok := reviewDiffCache.m[key]; ok {
		reviewDiffCache.Unlock()
		return r
	}
	reviewDiffCache.Unlock()
	r := computeReviewDiff(base, body)
	reviewDiffCache.Lock()
	if len(reviewDiffCache.m) >= 512 {
		reviewDiffCache.m = map[[32]byte]reviewDiffResult{}
	}
	reviewDiffCache.m[key] = r
	reviewDiffCache.Unlock()
	return r
}

func splitBodyLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(text, "\n"), "\n")
}

func computeReviewDiff(base, body string) reviewDiffResult {
	a, b := splitBodyLines(base), splitBodyLines(body)
	out := reviewDiffResult{hunks: []any{}}
	if base == body {
		return out
	}
	var added []string
	push := func(op string, lines []string) {
		if len(lines) == 0 {
			return
		}
		list := make([]any, len(lines))
		for i, l := range lines {
			list[i] = l
		}
		out.hunks = append(out.hunks, pyjson.NewObject("op", op, "lines", list))
	}
	ops := pydiff.Opcodes(a, b)
	for i, op := range ops {
		switch op.Tag {
		case "equal":
			lines := a[op.I1:op.I2]
			first, last := i == 0, i == len(ops)-1
			keepHead, keepTail := reviewDiffContext, reviewDiffContext
			if first {
				keepHead = 0
			}
			if last {
				keepTail = 0
			}
			if len(lines) <= keepHead+keepTail+1 {
				push("same", lines)
				continue
			}
			push("same", lines[:keepHead])
			out.hunks = append(out.hunks, pyjson.NewObject("op", "skip", "n", int64(len(lines)-keepHead-keepTail)))
			push("same", lines[len(lines)-keepTail:])
		case "delete":
			push("del", a[op.I1:op.I2])
			out.removed += int64(op.I2 - op.I1)
		case "insert":
			push("add", b[op.J1:op.J2])
			out.added += int64(op.J2 - op.J1)
			added = append(added, b[op.J1:op.J2]...)
		case "replace":
			push("del", a[op.I1:op.I2])
			push("add", b[op.J1:op.J2])
			out.removed += int64(op.I2 - op.I1)
			out.added += int64(op.J2 - op.J1)
			added = append(added, b[op.J1:op.J2]...)
		}
	}
	out.addedText = strings.Join(added, "\n")
	return out
}

// lastSection is the body's newest section: from its last heading on, or
// its last paragraph.
func lastSection(body string) string {
	lines := splitBodyLines(body)
	for i := len(lines) - 1; i >= 0; i-- {
		if atxHeadingRe.MatchString(lines[i]) && strings.HasPrefix(strings.TrimSpace(lines[i]), "#") {
			return strings.Join(lines[i:], "\n")
		}
	}
	paragraphs := strings.Split(strings.TrimSpace(strings.Join(lines, "\n")), "\n\n")
	return paragraphs[len(paragraphs)-1]
}

// ------------------------------------------------------------------ reads

// Hints the Needs you cards use when a review spans several units.
var (
	deliveryCommitRe = regexp.MustCompile(`@\s*([0-9a-f]{7,40})\b`)
	deliveryBranchRe = regexp.MustCompile(`\b((?:agent|feat|fix|exp|perf)/[A-Za-z0-9._/-]+[A-Za-z0-9])`)
	verdictRe        = regexp.MustCompile(`(?im)(?:独立评审|独立审阅|评审结论|审阅结论|independent review|reviewer verdict|verdict)\s*[:：]\s*(.+)$`)
)

var deliveryLabelRe = regexp.MustCompile(`^\s*(?:#{1,6}\s*)?([^\n（(]{2,40}?)\s*[（(][^）)\n]{0,80}?(?:agent|feat|fix|exp|perf)/`)

// deliveryOf is the delivery a body names first: its branch, commit and the
// label before them ("HOI-215 B2（agent/... @ 0035966"); nil when none.
func deliveryOf(title, body string) any {
	text := pytext.Prefix(body, 3000) + "\n" + title
	branch, commit, label := "", "", ""
	if m := deliveryBranchRe.FindStringSubmatch(text); m != nil {
		branch = m[1]
	}
	if m := deliveryCommitRe.FindStringSubmatch(text); m != nil {
		commit = m[1]
	}
	if branch == "" && commit == "" {
		return nil
	}
	if m := deliveryLabelRe.FindStringSubmatch(body); m != nil {
		label = strings.TrimSpace(m[1])
	}
	return pyjson.NewObject("branch", nilIfEmpty(branch), "commit", nilIfEmpty(commit), "label", nilIfEmpty(label))
}

// reviewVerdict is an independent reviewer's verdict written in the body.
func reviewVerdict(body string) string {
	m := verdictRe.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	text := strings.TrimSpace(m[1])
	if i := strings.IndexAny(text, "。；;，,（("); i > 0 {
		text = text[:i]
	}
	return pytext.Prefix(strings.TrimSpace(pytext.Excerpt(text, 80)), 80)
}

// reviewRounds loads every round of the units, by unit, oldest first.
func reviewRoundsFor(ctx context.Context, q sqlitedb.Querier, idList []any) (map[int64][]sqlitedb.Row, error) {
	out := map[int64][]sqlitedb.Row{}
	if has, err := hasReviewTables(ctx, q); err != nil || !has {
		return out, err
	}
	rows, err := groupRows(ctx, q, "SELECT * FROM review_rounds WHERE unit_id IN ({marks}) ORDER BY unit_id, round, id", idList)
	for _, r := range rows {
		out[r.Int("unit_id")] = append(out[r.Int("unit_id")], r)
	}
	return out, err
}

// legacySince is when each unit last entered review per the activity log.
func legacySince(ctx context.Context, q sqlitedb.Querier, idList []any) (map[int64][2]any, error) {
	out := map[int64][2]any{}
	rows, err := groupRows(ctx, q, `SELECT unit_id, MAX(at) at, COUNT(*) n FROM activity WHERE unit_id IN ({marks})
                   AND action='unit.needs' AND json_extract(detail_json,'$.to')='review' GROUP BY unit_id`, idList)
	for _, r := range rows {
		out[r.Int("unit_id")] = [2]any{r.Get("at"), r.Int("n")}
	}
	return out, err
}

func roundSummary(r sqlitedb.Row) *pyjson.Object {
	return pyjson.NewObject("round", r.Get("round"), "opened_at", nilIfEmpty(r.Str("opened_at")),
		"opened_by", nilIfEmpty(r.Str("opened_by")), "note", r.Get("note"), "outcome", r.Get("outcome"),
		"closed_at", r.Get("closed_at"), "closed_by", r.Get("closed_by"))
}

// reviewState is what a review card needs about one unit in review.
type reviewState struct {
	round     int64
	since     any
	openedBy  any
	note      string
	baseKind  string
	baseAt    any
	baseBody  string
	baseMedia int64
	hasBase   bool
	prev      *sqlitedb.Row
}

func stateOf(row sqlitedb.Row, rounds []sqlitedb.Row, legacy [2]any) reviewState {
	st := reviewState{since: row.Get("updated_at")}
	var open *sqlitedb.Row
	for i := range rounds {
		if rounds[i].Get("closed_at") == nil {
			open = &rounds[i]
		}
	}
	if open != nil {
		st.round, st.since, st.openedBy, st.note = open.Int("round"), open.Get("opened_at"), nilIfEmpty(open.Str("opened_by")), open.Str("note")
		st.baseKind, st.baseAt, st.baseBody, st.baseMedia, st.hasBase = open.Str("base_kind"), open.Get("base_at"), open.Str("base_body"), open.Int("base_media"), true
		for i := range rounds {
			if rounds[i].Int("round") < st.round && rounds[i].Get("closed_at") != nil {
				st.prev = &rounds[i]
			}
		}
		return st
	}
	// Legacy: in review since before rounds existed.
	if legacy[0] != nil {
		st.since = legacy[0]
		st.round = legacy[1].(int64)
	}
	if st.round < 1 {
		st.round = 1
	}
	if n := len(rounds); n > 0 {
		last := rounds[n-1]
		st.prev = &last
		if isHumanActor(last.Str("closed_by")) && last.Get("close_body") != nil {
			st.baseKind, st.baseAt, st.baseBody, st.baseMedia, st.hasBase = "review", last.Get("closed_at"), last.Str("close_body"), last.Int("close_media"), true
		}
		if st.round <= last.Int("round") {
			st.round = last.Int("round") + 1
		}
	}
	return st
}

// previousRound is the earlier round and the people's comments made from
// its start until this round opened, with whether each is resolved.
func previousRound(prev *sqlitedb.Row, since any, comments []sqlitedb.Row) *pyjson.Object {
	from := ""
	if prev != nil {
		from = prev.Str("opened_at")
	}
	until := pyjson.Str(since)
	list := []any{}
	open := int64(0)
	for _, c := range comments {
		at := c.Str("created_at")
		if at < from || (until != "" && at > until) || c.Str("kind") == "agent_reply" {
			continue
		}
		resolved := pyjson.Truthy(c.Get("resolved_at"))
		if !resolved {
			open++
		}
		list = append(list, pyjson.NewObject("id", c.Get("id"), "kind", c.Get("kind"), "author", c.Get("author"),
			"body", pytext.Prefix(c.Str("body"), 600), "created_at", c.Get("created_at"), "resolved", resolved))
	}
	if len(list) > 5 {
		list = list[len(list)-5:]
	}
	if prev == nil && len(list) == 0 {
		return nil
	}
	out := pyjson.NewObject("comments", list, "open", open)
	if prev != nil {
		out.Set("round", prev.Get("round")).Set("outcome", prev.Get("outcome")).Set("closed_at", prev.Get("closed_at")).
			Set("closed_by", prev.Get("closed_by")).Set("note", prev.Get("note"))
	} else {
		out.Set("round", nil).Set("outcome", nil).Set("closed_at", nil).Set("closed_by", nil).Set("note", "")
	}
	return out
}

// Relations that tie the units of one delivery together.
var reviewGroupRelations = []any{"part_of", "derived_from", "produces", "supports", "validates", "supersedes"}

// needsRelations reads the parents (part_of / derived_from targets) and the
// links between the listed units, by unit id.
func needsRelations(ctx context.Context, q sqlitedb.Querier, rows []sqlitedb.Row) (parents, links map[int64][]any, err error) {
	parents, links = map[int64][]any{}, map[int64][]any{}
	allArgs := make([]any, len(rows))
	byID := map[int64]int{}
	for i, r := range rows {
		allArgs[i] = r.Int("id")
		byID[r.Int("id")] = i
	}
	lrows, err := groupRows(ctx, q, `SELECT l.source_id, l.target_id, l.relation, t.uid tuid, t.title ttitle, t.type ttype,
                   t.status tstatus FROM unit_links l JOIN units t ON t.id=l.target_id
                   WHERE l.source_id IN ({marks}) AND t.archived=0 ORDER BY l.id`, allArgs)
	if err != nil {
		return nil, nil, err
	}
	for _, l := range lrows {
		src, dst, rel := l.Int("source_id"), l.Int("target_id"), l.Str("relation")
		if rel == "part_of" || rel == "derived_from" {
			parents[src] = append(parents[src], pyjson.NewObject("uid", l.Get("tuid"), "title", l.Get("ttitle"),
				"type", l.Get("ttype"), "status", l.Get("tstatus"), "relation", rel))
		}
		if j, ok := byID[dst]; ok && containsAny(reviewGroupRelations, rel) && dst != src {
			links[src] = append(links[src], pyjson.NewObject("uid", rows[j].Get("uid"), "relation", rel, "dir", "out"))
			links[dst] = append(links[dst], pyjson.NewObject("uid", rows[byID[src]].Get("uid"), "relation", rel, "dir", "in"))
		}
	}
	return parents, links, nil
}

// needsCandidateOf is what the automatic grouping reads of one Needs you
// row: since is when it entered review, delivery is deliveryOf's value.
func needsCandidateOf(r sqlitedb.Row, since, delivery any, parents, links []any) *needsCandidate {
	c := &needsCandidate{uid: r.Str("uid"), typ: r.Str("type"), kind: "review", title: r.Str("title")}
	if c.typ == "decision" {
		c.kind = "decision"
	}
	c.since = parseNeedsTime(since)
	if d, ok := delivery.(*pyjson.Object); ok {
		c.branch, _ = d.Value("branch").(string)
		c.commit, _ = d.Value("commit").(string)
		c.label, _ = d.Value("label").(string)
	}
	for _, p := range parents {
		o := p.(*pyjson.Object)
		uid, _ := o.Value("uid").(string)
		title, _ := o.Value("title").(string)
		c.parents = append(c.parents, needsRef{uid: uid, title: title})
	}
	for _, l := range links {
		o := l.(*pyjson.Object)
		uid, _ := o.Value("uid").(string)
		rel, _ := o.Value("relation").(string)
		dir, _ := o.Value("dir").(string)
		c.links = append(c.links, needsLink{uid: uid, relation: rel, dir: dir})
	}
	return c
}

// enrichReviews adds the review fields to the Needs you items (rows and
// items in the same order).
func (s *Store) enrichReviews(ctx context.Context, q sqlitedb.Querier, rows []sqlitedb.Row, items []*pyjson.Object) error {
	if len(rows) == 0 {
		return nil
	}
	var reviewIDs []int64
	for _, r := range rows {
		if r.Str("status") == "review" {
			reviewIDs = append(reviewIDs, r.Int("id"))
		}
	}
	idArgs := sqlitedb.Args(reviewIDs)
	rounds, err := reviewRoundsFor(ctx, q, idArgs)
	if err != nil {
		return err
	}
	legacy, err := legacySince(ctx, q, idArgs)
	if err != nil {
		return err
	}
	comments := map[int64][]sqlitedb.Row{}
	crows, err := groupRows(ctx, q, `SELECT id, unit_id, author, kind, body, created_at, resolved_at FROM unit_comments
                   WHERE unit_id IN ({marks}) ORDER BY created_at, id`, idArgs)
	if err != nil {
		return err
	}
	for _, c := range crows {
		comments[c.Int("unit_id")] = append(comments[c.Int("unit_id")], c)
	}
	mediaIDs := map[int64][]int64{}
	mrows, err := groupRows(ctx, q, "SELECT unit_id, id FROM unit_media WHERE unit_id IN ({marks}) ORDER BY id", idArgs)
	if err != nil {
		return err
	}
	for _, m := range mrows {
		mediaIDs[m.Int("unit_id")] = append(mediaIDs[m.Int("unit_id")], m.Int("id"))
	}
	parents, links, err := needsRelations(ctx, q, rows)
	if err != nil {
		return err
	}
	for i, r := range rows {
		id := r.Int("id")
		item := items[i]
		item.Set("parents", orEmpty(parents[id])).Set("links", orEmpty(links[id])).
			Set("delivery", deliveryOf(r.Str("title"), r.Str("body"))).
			Set("verdict", nilIfEmpty(reviewVerdict(r.Str("body"))))
		if r.Str("status") != "review" {
			continue
		}
		st := stateOf(r, rounds[id], legacy[id])
		anchors, _, err := resolveAnchors(ctx, q, st.note, r.Str("uid"), r.Str("body"))
		if err != nil {
			return err
		}
		item.Set("review_round", st.round).Set("review_since", st.since).Set("review_by", st.openedBy).
			Set("review_note", st.note).Set("review_anchors", anchors)
		focus := ""
		if st.hasBase {
			d := reviewDiff(st.baseBody, r.Str("body"))
			newMedia := int64(0)
			for _, m := range mediaIDs[id] {
				if m > st.baseMedia {
					newMedia++
				}
			}
			item.Set("review_base", pyjson.NewObject("kind", st.baseKind, "at", st.baseAt)).
				Set("review_changes", pyjson.NewObject("added", d.added, "removed", d.removed, "media", newMedia))
			for _, m := range item.Value("media").([]any) {
				if o, ok := m.(*pyjson.Object); ok && o.Value("from") == nil {
					if mid, ok := o.Value("id").(int64); ok && mid > st.baseMedia {
						o.Set("new", true)
					}
				}
			}
			if st.note == "" && strings.TrimSpace(d.addedText) != "" {
				focus = d.addedText
			}
		} else {
			item.Set("review_base", nil).Set("review_changes", nil)
		}
		if st.note == "" {
			if focus == "" {
				focus = lastSection(r.Str("body"))
			}
			item.Set("review_focus", pytext.Excerpt(pytext.NoteLineRe.ReplaceAllString(focus, ""), 320))
		} else {
			item.Set("review_focus", nil)
		}
		item.Set("review_prev", previousRound(st.prev, st.since, comments[id]))
	}
	// One card for the units of one delivery (needsgroups.go).
	cands := make([]*needsCandidate, len(rows))
	for i, r := range rows {
		since := items[i].Value("review_since")
		if since == nil {
			since = r.Get("updated_at")
		}
		cands[i] = needsCandidateOf(r, since, items[i].Value("delivery"), parents[r.Int("id")], links[r.Int("id")])
	}
	cards := needsAutoGroups(cands)
	for i, c := range cands {
		if c.kind != "review" {
			continue
		}
		if card, ok := cards[i]; ok {
			items[i].Set("auto_group", card)
		} else {
			items[i].Set("auto_group", nil)
		}
	}
	return nil
}

// ReviewInfo is GET /api/units/{ref}/review: the unit's review rounds, the
// open one's note and anchors, and what changed since the reviewer's last
// decision (line diff of the body plus the attachments added since).
func (s *Store) ReviewInfo(ctx context.Context, ref string) (*pyjson.Object, error) {
	var out *pyjson.Object
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		row, err := unitRow(ctx, q, ref)
		if err != nil {
			return err
		}
		id := row.Int("id")
		args := []any{id}
		rounds, err := reviewRoundsFor(ctx, q, args)
		if err != nil {
			return err
		}
		legacy, err := legacySince(ctx, q, args)
		if err != nil {
			return err
		}
		history := []any{}
		for _, r := range rounds[id] {
			history = append(history, roundSummary(r))
		}
		out = pyjson.NewObject("uid", row.Get("uid"), "title", row.Get("title"), "status", row.Get("status"),
			"in_review", row.Str("status") == "review", "rounds", history)
		groups, err := groupsOfUnit(ctx, q, id)
		if err != nil {
			return err
		}
		out.Set("groups", groups)
		if row.Str("status") != "review" {
			return nil
		}
		st := stateOf(row, rounds[id], legacy[id])
		anchors, warnings, err := resolveAnchors(ctx, q, st.note, row.Str("uid"), row.Str("body"))
		if err != nil {
			return err
		}
		crows, err := sqlitedb.Query(ctx, q, `SELECT id, unit_id, author, kind, body, created_at, resolved_at FROM unit_comments
                   WHERE unit_id=? ORDER BY created_at, id`, id)
		if err != nil {
			return err
		}
		out.Set("round", st.round).Set("since", st.since).Set("opened_by", st.openedBy).Set("note", st.note).
			Set("anchors", anchors).Set("warnings", warnings).Set("previous", previousRound(st.prev, st.since, crows))
		if !st.hasBase {
			out.Set("base", nil).Set("diff", nil).Set("media_new", []any{})
			return nil
		}
		d := reviewDiff(st.baseBody, row.Str("body"))
		mrows, err := sqlitedb.Query(ctx, q, "SELECT * FROM unit_media WHERE unit_id=? AND id>? ORDER BY id", id, st.baseMedia)
		if err != nil {
			return err
		}
		media := []any{}
		for _, m := range mrows {
			item := mediaDict(m)
			item.Set("preview_kind", nilIfEmpty(mediaPreviewKind(item.Value("kind"), item.Value("url"), item.Value("is_image") == true)))
			media = append(media, item)
		}
		out.Set("base", pyjson.NewObject("kind", st.baseKind, "at", st.baseAt)).
			Set("diff", pyjson.NewObject("hunks", d.hunks, "added", d.added, "removed", d.removed)).
			Set("media_new", media)
		return nil
	})
	return out, err
}

// ReviewNoteWarnings checks the anchors of a unit's review note (the
// response of a status change that sets one).
func (s *Store) ReviewNoteWarnings(ctx context.Context, ref, note string) ([]any, error) {
	var out []any
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		row, err := unitRow(ctx, q, ref)
		if err != nil {
			return err
		}
		_, out, err = resolveAnchors(ctx, q, note, row.Str("uid"), row.Str("body"))
		return err
	})
	return out, err
}
