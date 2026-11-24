package store

import (
	"context"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"arkstudio/go/internal/platform/clock"

	"arkstudio/go/internal/platform/pyerr"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/sqlitedb"
)

// Port of the unit/link/media write path of store.py: create_unit,
// update_unit, archive_unit, restore_unit, insert_between, create_link,
// update_link, delete_link, add_media, update_media, delete_media.

const (
	maxTitle   = 240
	maxBody    = 200_000
	maxURL     = 2048
	maxCaption = 500
)

var (
	dateRe     = pytext.MustCompile(`^\d{4}-\d{2}-\d{2}\n?$`)
	assigneeRe = regexp.MustCompile(`^[A-Za-z0-9_.@+-]{1,64}$`)
	reserved   = map[string]bool{"type": true, "status": true, "importance": true}
)

// Who carries the per-request hooks Python installs on the store in
// App.require(): the assignee resolver and the creating account.
type Who struct {
	// Resolve is App.resolve_assignee bound to the project (nil = format check only).
	Resolve func(name string) (string, error)
	// Creator is App.actor (the signed-in account or the local owner).
	Creator func() string
}

// wtx is one write transaction: the activity lines (mirrored to
// logs/activity.jsonl), the touched units for the snapshot log, and whether
// _after_write (commit + touch_manifest) runs.
type wtx struct {
	ctx     context.Context
	q       sqlitedb.Querier
	lines   []logLine
	units   []int64
	full    bool // structure changed: the graph snapshot must rebuild
	noTouch bool
	// batch (Go only, batch.go) tags every activity entry of a multi-unit
	// operation with one id, so the entries of one batch edit can be grouped.
	batch string
}

func (w *wtx) log(actor, action string, unitID any, detail *pyjson.Object) error {
	if w.batch != "" && detail != nil {
		detail.Set("batch", w.batch)
	}
	line, err := logActivity(w.ctx, w.q, actor, action, unitID, detail)
	if err != nil {
		return err
	}
	w.lines = append(w.lines, line)
	return nil
}

// write runs fn in one transaction ("with self.lock, self.con:") and then
// _after_write.  Activity lines are appended even when the transaction rolls
// back: Python writes the JSONL mirror inside log_activity.
func (s *Store) write(ctx context.Context, fn func(w *wtx) error) error {
	w := &wtx{ctx: ctx}
	var before string
	began := false
	err := s.DB.Write(ctx, func(q sqlitedb.Querier) error {
		before, began = s.beginWrite(ctx, q), true
		w.q = q
		return fn(w)
	}, func() {
		s.appendLog(w.lines...)
		if !w.noTouch {
			s.touchManifest()
		}
		units := w.units
		if w.full {
			units = nil
		} else if units == nil {
			units = []int64{}
		}
		s.noteWrite(ctx, before, units)
	})
	if err != nil {
		if began {
			s.writeFailed()
		}
		if len(w.lines) > 0 {
			s.appendLog(w.lines...)
		}
	}
	return err
}

// cleanTextField is _clean_text(value, field, max_len, required).
func cleanTextField(value any, field string, maxLen int, required bool) (string, error) {
	text := ""
	if value != nil {
		text = pyjson.Str(value)
	}
	text = strings.ReplaceAll(text, "\x00", "")
	if required {
		text = pytext.Strip(text)
		if text == "" {
			return "", storeErr("%s is required", field)
		}
	}
	if pytext.Len(text) > maxLen {
		return "", storeErr("%s is longer than %d characters", field, maxLen)
	}
	return text, nil
}

// cleanDate is _clean_date ("" = None).
func cleanDate(value any, field string) (any, error) {
	if value == nil || value == "" {
		return nil, nil
	}
	text := pytext.Strip(pyjson.Str(value))
	if !dateRe.MatchString(text) {
		return nil, storeErr("%s must be YYYY-MM-DD", field)
	}
	if !realDate(text) {
		return nil, storeErr("%s is not a real date: %s", field, text)
	}
	return text, nil
}

// realDate is date.fromisoformat (the C parser: ASCII digits only).
func realDate(text string) bool {
	if len(text) != 10 {
		return false
	}
	for i, c := range []byte(text) {
		if i == 4 || i == 7 {
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	y, _ := strconv.Atoi(text[0:4])
	m, _ := strconv.Atoi(text[5:7])
	d, _ := strconv.Atoi(text[8:10])
	if y < 1 || m < 1 || m > 12 || d < 1 {
		return false
	}
	days := [13]int{0, 31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	if m == 2 && (y%4 == 0 && (y%100 != 0 || y%400 == 0)) {
		return d <= 29
	}
	return d <= days[m]
}

func strOrNil(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// listOf is list(value) / iterating “value or []“: a falsy value is
// empty, str/list/dict iterate, anything else is a TypeError.
func listOf(v any) ([]any, error) {
	if !pyjson.Truthy(v) {
		return nil, nil
	}
	switch v.(type) {
	case []any, string, *pyjson.Object:
		return Iter(v)
	}
	return nil, &pyerr.InternalError{Message: "'" + pyerr.TypeName(v) + "' object is not iterable"}
}

// unitRowAny is _unit_row(ref) for any decoded JSON value.
func unitRowAny(ctx context.Context, q sqlitedb.Querier, ref any) (sqlitedb.Row, error) {
	switch x := ref.(type) {
	case string:
		if pytext.IsDigit(x) {
			if _, err := pytext.Int(x); err != nil {
				if _, isInt := err.(*pytext.IntError); !isInt {
					return sqlitedb.Row{}, &pyerr.InternalError{Message: "Python int too large to convert to SQLite INTEGER"}
				}
			}
		}
		return unitRow(ctx, q, x)
	case bool, int64:
		n := int64(0)
		if b, ok := x.(bool); ok {
			if b {
				n = 1
			}
		} else {
			n = x.(int64)
		}
		row, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT * FROM units WHERE id=?", n)
		if err != nil {
			return row, err
		}
		if !ok {
			return row, storeErr("unit not found: %s", pyjson.Str(ref))
		}
		return row, nil
	case *big.Int:
		return sqlitedb.Row{}, &pyerr.InternalError{Message: "Python int too large to convert to SQLite INTEGER"}
	}
	text := pyjson.Str(ref)
	row, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT * FROM units WHERE uid=?", text)
	if err != nil {
		return row, err
	}
	if !ok {
		return row, storeErr("unit not found: %s", text)
	}
	return row, nil
}

// nextUID is Store._next_uid.
func nextUID(ctx context.Context, q sqlitedb.Querier, typeName string) (string, error) {
	prefix := typePrefix(typeName)
	row, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT n FROM counters WHERE prefix=?", prefix)
	if err != nil {
		return "", err
	}
	n := int64(1)
	if ok {
		n = row.Int("n") + 1
	}
	var uid string
	for {
		uid = prefix + "-" + pad4(n)
		_, exists, err := sqlitedb.QueryOne(ctx, q, "SELECT 1 FROM units WHERE uid=?", uid)
		if err != nil {
			return "", err
		}
		if !exists {
			break
		}
		n++
	}
	_, err = q.ExecContext(ctx, "INSERT INTO counters(prefix,n) VALUES(?,?) ON CONFLICT(prefix) DO UPDATE SET n=excluded.n", prefix, n)
	return uid, err
}

func pad4(n int64) string {
	s := strconv.FormatInt(n, 10)
	for len(s) < 4 {
		s = "0" + s
	}
	return s
}

func writeTags(ctx context.Context, q sqlitedb.Querier, unitID int64, tags []string) error {
	if _, err := q.ExecContext(ctx, "DELETE FROM unit_tags WHERE unit_id=?", unitID); err != nil {
		return err
	}
	for _, t := range tags {
		if _, err := q.ExecContext(ctx, "INSERT OR IGNORE INTO unit_tags(unit_id,tag) VALUES(?,?)", unitID, t); err != nil {
			return err
		}
	}
	return nil
}

func logNeeds(w *wtx, actor string, unitID int64, uid, title any, old, new string) error {
	return w.log(actor, "unit.needs", unitID, pyjson.NewObject("uid", uid, "title", title,
		"from", nilIfEmpty(old), "to", nilIfEmpty(new)))
}

// logNeedsNote is logNeeds with the review note of a unit entering review
// (Go only: the agents' update feed and the activity list show it).
func logNeedsNote(w *wtx, actor string, unitID int64, uid, title any, old, new string, note *string) error {
	detail := pyjson.NewObject("uid", uid, "title", title, "from", nilIfEmpty(old), "to", nilIfEmpty(new))
	if note != nil && *note != "" && new == "review" {
		detail.Set("review_note", *note)
	}
	return w.log(actor, "unit.needs", unitID, detail)
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// cleanAssignee is Store._clean_assignee ("" result = None).
func (who Who) cleanAssignee(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", storeErr("assignee must be a username string or null")
	}
	text = pytext.Strip(text)
	if text == "" {
		return "", nil
	}
	if who.Resolve != nil {
		return who.Resolve(text)
	}
	if pytext.Len(text) > 64 || !assigneeRe.MatchString(text) {
		return "", storeErr("assignee is not a valid username: %s", pyjson.Repr(text))
	}
	return text, nil
}

// creatorAssignee is Store._creator_assignee.
func (who Who) creatorAssignee(actor string) string {
	if strings.HasPrefix(actor, "system") {
		return ""
	}
	name := ""
	if who.Creator != nil {
		name = who.Creator()
	}
	name = pytext.Strip(name)
	if name == "" {
		return ""
	}
	out, err := who.cleanAssignee(name)
	if err != nil {
		return ""
	}
	return out
}

func tagsOfRow(ctx context.Context, q sqlitedb.Querier, unitID int64, ordered bool) ([]string, error) {
	query := "SELECT tag FROM unit_tags WHERE unit_id=?"
	if ordered {
		query += " ORDER BY tag"
	}
	rows, err := sqlitedb.Query(ctx, q, query, unitID)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Str("tag")
	}
	return out, nil
}

func pick(tags []string, ns string) string {
	for _, t := range tags {
		if TagNamespace(t) == ns {
			return TagValue(t)
		}
	}
	return ""
}

// CreateUnit is Store.create_unit; it returns get_unit of the new unit.
func (s *Store) CreateUnit(ctx context.Context, data *pyjson.Object, actor string, who Who) (*pyjson.Object, error) {
	id, err := s.createUnit(ctx, data, actor, who)
	if err != nil {
		return nil, err
	}
	return s.GetUnit(ctx, strconv.FormatInt(id, 10))
}

func (s *Store) createUnit(ctx context.Context, data *pyjson.Object, actor string, who Who) (int64, error) {
	return s.createUnitIn(ctx, nil, data, actor, who)
}

// createUnitIn is createUnit inside an open write when outer is not nil (Go
// only, batch.go): the unit row goes into that transaction, and links/media
// in data are not supported there.
func (s *Store) createUnitIn(ctx context.Context, outer *wtx, data *pyjson.Object, actor string, who Who) (int64, error) {
	if err := s.refuseMerged(); err != nil { // move.go: a merged project is read-only
		return 0, err
	}
	title, err := cleanTextField(data.Value("title"), "title", maxTitle, true)
	if err != nil {
		return 0, err
	}
	body, err := cleanTextField(data.Value("body"), "body", maxBody, false)
	if err != nil {
		return 0, err
	}
	typeRaw := data.Value("type")
	typeName := "note"
	if pyjson.Truthy(typeRaw) {
		typeName = pyjson.Str(typeRaw)
	}
	typeName = pytext.Lower(pytext.Strip(typeName))
	rawTags, err := listOf(data.Value("tags"))
	if err != nil {
		return 0, err
	}
	tags, err := normalizeTags(s.resolveMarkers(withoutNeeds(rawTags)), typeName, data.Value("status"), data.Value("importance"))
	if err != nil {
		return 0, err
	}
	typeName, status, importance := pick(tags, "type"), pick(tags, "status"), pick(tags, "importance")
	if typeName == "topic" {
		if err := s.checkTopicDesign(ctx, data); err != nil {
			return 0, err
		}
	}
	needs := needsTransition("", typeName, status, false)
	if needs != "" {
		tags = append(tags, needsNS+":"+needs)
	}
	var reviewNote *string
	if data.Has("review_note") {
		note, err := cleanReviewNote(data.Value("review_note"))
		if err != nil {
			return 0, err
		}
		if needs != "review" {
			return 0, storeErr("review_note needs status review")
		}
		reviewNote = &note
	}
	if IsAgentActor(actor) && needs == "review" { // Go only (reviewnote_required.go)
		note := ""
		if reviewNote != nil {
			note = *reviewNote
		}
		if err := checkAgentUnitReviewNote("The new unit", note, body, false); err != nil {
			return 0, err
		}
	}
	startAt, err := cleanDate(data.Value("start_at"), "start_at")
	if err != nil {
		return 0, err
	}
	endAt, err := cleanDate(data.Value("end_at"), "end_at")
	if err != nil {
		return 0, err
	}
	if startAt != nil && endAt != nil && endAt.(string) < startAt.(string) {
		return 0, storeErr("end_at is before start_at")
	}
	today := clock.Today()
	if startAt == nil && !(endAt != nil && endAt.(string) < today) {
		startAt = today
	}
	if endAt == nil && status == "done" && !(startAt != nil && today < startAt.(string)) {
		endAt = today
	}
	meta := data.Value("meta")
	if !pyjson.Truthy(meta) {
		meta = pyjson.NewObject()
	}
	if _, ok := meta.(*pyjson.Object); !ok {
		return 0, storeErr("meta must be an object")
	}
	sourceRef, err := cleanTextField(data.Value("source_ref"), "source_ref", 400, false)
	if err != nil {
		return 0, err
	}
	var assignee string
	if data.Has("assignee") {
		if assignee, err = who.cleanAssignee(data.Value("assignee")); err != nil {
			return 0, err
		}
	} else {
		assignee = who.creatorAssignee(actor)
	}
	now := clock.UTCNow()
	originRaw := data.Value("origin")
	origin := "manual"
	if pyjson.Truthy(originRaw) {
		origin = pyjson.Str(originRaw)
	}
	origin = pytext.Prefix(origin, 40)
	var unitID int64
	insert := func(w *wtx) error {
		w.full = true
		uid, err := nextUID(ctx, w.q, typeName)
		if err != nil {
			return err
		}
		sourceTitle, err := cleanTextField(data.Value("source_title"), "source_title", 300, false)
		if err != nil {
			return err
		}
		res, err := w.q.ExecContext(ctx, `INSERT INTO units(uid,type,title,body,status,importance,start_at,end_at,
                   created_at,updated_at,origin,source_ref,source_title,meta_json,assignee)
                   VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			uid, typeName, title, body, status, importance, startAt, endAt, now, now, origin,
			nilIfEmpty(sourceRef), nilIfEmpty(sourceTitle), string(pyjson.Dumps(meta)), nilIfEmpty(assignee))
		if err != nil {
			return err
		}
		unitID, _ = res.LastInsertId()
		if err := writeTags(ctx, w.q, unitID, tags); err != nil {
			return err
		}
		detail := pyjson.NewObject("uid", uid, "title", title, "type", typeName, "status", status)
		if assignee != "" {
			detail.Set("assignee", assignee)
		}
		if err := w.log(actor, "unit.create", unitID, detail); err != nil {
			return err
		}
		if assignee != "" && data.Has("assignee") {
			if err := recordAssignment(w, unitID, assignee, who.whoAssigns(), actor); err != nil {
				return err
			}
		}
		if needs != "" {
			if err := reviewTransition(w, unitID, "", needs, body, actor, reviewNote); err != nil {
				return err
			}
			return logNeedsNote(w, actor, unitID, uid, title, "", needs, reviewNote)
		}
		return nil
	}
	if outer != nil {
		if pyjson.Truthy(data.Value("links")) || pyjson.Truthy(data.Value("media")) ||
			pyjson.Truthy(data.Value("members")) || pyjson.Truthy(data.Value("design")) {
			return 0, storeErr("links, media and topic members cannot be set here")
		}
		return unitID, insert(outer)
	}
	if err = s.write(ctx, insert); err != nil {
		return 0, err
	}
	links, err := listOf(data.Value("links"))
	if err != nil {
		return 0, err
	}
	for _, item := range links {
		link, ok := item.(*pyjson.Object)
		if !ok {
			return 0, attrErr(item, "get")
		}
		target := link.Value("target")
		if !pyjson.Truthy(target) {
			target = link.Value("target_id")
		}
		relation := link.Value("relation")
		if !pyjson.Truthy(relation) {
			relation = "relates_to"
		}
		note := link.Value("note")
		if !pyjson.Truthy(note) {
			note = ""
		}
		if _, err := s.CreateLink(ctx, unitID, target, relation, note, actor); err != nil && !isStoreErr(err) {
			return 0, err
		}
	}
	media, err := listOf(data.Value("media"))
	if err != nil {
		return 0, err
	}
	for _, item := range media {
		m, ok := item.(*pyjson.Object)
		if !ok {
			return 0, attrErr(item, "get")
		}
		kind := m.Value("kind")
		if !pyjson.Truthy(kind) {
			kind = "link"
		}
		caption := m.Value("caption")
		if !pyjson.Truthy(caption) {
			caption = ""
		}
		if _, err := s.AddMedia(ctx, unitID, m.Value("url"), kind, caption, actor); err != nil && !isStoreErr(err) {
			return 0, err
		}
	}
	if typeName == "topic" && (pyjson.Truthy(data.Value("members")) || pyjson.Truthy(data.Value("design"))) {
		design, _ := data.Value("design").(*pyjson.Object)
		if design == nil {
			design = pyjson.NewObject()
		}
		add := data.Value("members")
		if !pyjson.Truthy(add) {
			add = design.Value("add")
		}
		if !pyjson.Truthy(add) {
			add = []any{}
		}
		body := design.Copy().Set("add", add)
		if _, err := s.DesignTopic(ctx, unitID, body, actor); err != nil {
			return 0, err
		}
	}
	return unitID, nil
}

func isStoreErr(err error) bool {
	_, ok := err.(*Error)
	return ok
}

// UpdateUnit is Store.update_unit (history_message "Save text").
func (s *Store) UpdateUnit(ctx context.Context, ref any, patch *pyjson.Object, actor string, who Who) (*pyjson.Object, error) {
	id, err := s.updateUnit(ctx, ref, patch, actor, who, "Save text")
	if err != nil {
		return nil, err
	}
	return s.GetUnit(ctx, strconv.FormatInt(id, 10))
}

func (s *Store) updateUnit(ctx context.Context, ref any, patch *pyjson.Object, actor string, who Who, historyMessage string) (int64, error) {
	var unitID int64
	err := s.write(ctx, func(w *wtx) error {
		var err error
		unitID, err = s.updateUnitTx(w, ref, patch, actor, who, historyMessage)
		return err
	})
	return unitID, err
}

// updateUnitTx is the body of update_unit inside an open write (Python
// nests it in the caller's “with self.con“ for restore/apply).
func (s *Store) updateUnitTx(w *wtx, ref any, patch *pyjson.Object, actor string, who Who, historyMessage string) (int64, error) {
	ctx := w.ctx
	var unitID int64
	err := func() error {
		q := w.q
		row, err := unitRowAny(ctx, q, ref)
		if err != nil {
			return err
		}
		if err := refuseMoved(row); err != nil { // move.go
			return err
		}
		unitID = row.Int("id")
		w.units = []int64{unitID}
		var changed []string
		title := row.Str("title")
		if patch.Has("title") {
			if title, err = cleanTextField(patch.Value("title"), "title", maxTitle, true); err != nil {
				return err
			}
			changed = append(changed, "title")
		}
		body := row.Str("body")
		if patch.Has("body") {
			if body, err = cleanTextField(patch.Value("body"), "body", maxBody, false); err != nil {
				return err
			}
			changed = append(changed, "body")
		}
		startAt := row.Get("start_at")
		if patch.Has("start_at") {
			if startAt, err = cleanDate(patch.Value("start_at"), "start_at"); err != nil {
				return err
			}
			changed = append(changed, "start_at")
		}
		endAt := row.Get("end_at")
		if patch.Has("end_at") {
			if endAt, err = cleanDate(patch.Value("end_at"), "end_at"); err != nil {
				return err
			}
			changed = append(changed, "end_at")
		}
		if pyjson.Truthy(startAt) && pyjson.Truthy(endAt) && strOrNil(endAt) < strOrNil(startAt) {
			return storeErr("end_at is before start_at")
		}
		oldTags, err := tagsOfRow(ctx, q, unitID, true)
		if err != nil {
			return err
		}
		getOrRow := func(key string) any {
			if v, ok := patch.Get(key); ok {
				return v
			}
			return row.Get(key)
		}
		var source []any
		if want, ok := patch.Get("tags"); !ok || want == nil {
			for _, t := range oldTags {
				if !reserved[TagNamespace(t)] {
					source = append(source, t)
				}
			}
		} else if source, err = listOf(want); err != nil {
			return err
		}
		source = s.resolveMarkers(withoutNeeds(source))
		oldNeeds := needsOf(oldTags)
		tags, err := normalizeTags(source, getOrRow("type"), getOrRow("status"), getOrRow("importance"))
		if err != nil {
			return err
		}
		typeName, status, importance := pick(tags, "type"), pick(tags, "status"), pick(tags, "importance")
		needs := needsTransition(oldNeeds, typeName, status, row.Int("archived") != 0)
		if needs != "" {
			tags = append(tags, needsNS+":"+needs)
		}
		if typeName != row.Str("type") {
			changed = append(changed, "type")
		}
		if status != row.Str("status") {
			changed = append(changed, "status")
			today := clock.Today()
			if status == "done" && endAt == nil && !(pyjson.Truthy(startAt) && today < strOrNil(startAt)) {
				endAt = today
				changed = append(changed, "end_at")
			}
		}
		if importance != row.Str("importance") {
			changed = append(changed, "importance")
		}
		if !sameSorted(tags, oldTags) {
			changed = append(changed, "tags")
		}
		// Go only: review_note, the reviewer's pointer for this review round.
		var reviewNote *string
		if patch.Has("review_note") {
			note, err := cleanReviewNote(patch.Value("review_note"))
			if err != nil {
				return err
			}
			if needs != "review" {
				return storeErr("review_note needs status review (set it together with status review, or while the unit is in review)")
			}
			reviewNote = &note
			if oldNeeds == "review" {
				current, open, err := openRoundNote(ctx, q, unitID)
				if err != nil {
					return err
				}
				if !open || current != note {
					changed = append(changed, "review_note")
				}
			}
		}
		// Go only: an agent hands off with a note (reviewnote_required.go).
		if IsAgentActor(actor) && needs == "review" && (oldNeeds != "review" || reviewNote != nil) {
			note := ""
			if reviewNote != nil {
				note = *reviewNote
			}
			if err := checkAgentUnitReviewNote(row.Str("uid"), note, body, oldNeeds == "sent-back"); err != nil {
				return err
			}
		}
		meta := row.Get("meta_json")
		if patch.Has("meta") {
			m, ok := patch.Value("meta").(*pyjson.Object)
			if !ok {
				return storeErr("meta must be an object")
			}
			meta = string(pyjson.Dumps(m))
			changed = append(changed, "meta")
		}
		oldAssignee := row.Get("assignee")
		assignee := oldAssignee
		if patch.Has("assignee") {
			raw := patch.Value("assignee")
			rs, isStr := raw.(string)
			oa, _ := oldAssignee.(string)
			if !(isStr && oa != "" && pytext.Lower(pytext.Strip(rs)) == pytext.Lower(oa)) {
				name, err := who.cleanAssignee(raw)
				if err != nil {
					return err
				}
				assignee = nilIfEmpty(name)
			}
			if !sameValue(assignee, oldAssignee) {
				changed = append(changed, "assignee")
			}
		}
		if len(changed) == 0 {
			w.noTouch = true
			return nil
		}
		if containsString(changed, "assignee") {
			w.full = true // inherited assignees of descendants change
		}
		if _, err := q.ExecContext(ctx, `UPDATE units SET title=?,body=?,type=?,status=?,importance=?,start_at=?,end_at=?,
                   meta_json=?,assignee=?,updated_at=? WHERE id=?`,
			title, body, typeName, status, importance, startAt, endAt, meta, assignee, clock.UTCNow(), unitID); err != nil {
			return err
		}
		if title != row.Str("title") || body != row.Str("body") {
			w.full = true // parent/neighbour titles appear in other nodes
			if historyMessage != "Save text" || !strings.HasPrefix(actor, "human:") {
				pending, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT actor FROM node_text_pending WHERE unit_id=?", unitID)
				if err != nil {
					return err
				}
				if ok {
					if err := s.recordTextHistory(w, unitID, row.Str("uid"), row.Str("title"), row.Str("body"),
						pending.Str("actor"), "Save pending human text"); err != nil {
						return err
					}
				}
			}
			if err := s.recordTextHistory(w, unitID, row.Str("uid"), title, body, actor, historyMessage); err != nil {
				return err
			}
		}
		if err := writeTags(ctx, q, unitID, tags); err != nil {
			return err
		}
		if err := reviewTransition(w, unitID, oldNeeds, needs, body, actor, reviewNote); err != nil {
			return err
		}
		fields := sortedUnique(changed)
		detail := pyjson.NewObject("uid", row.Get("uid"), "title", title, "fields", fields)
		if containsString(changed, "status") {
			detail.Set("status", pyjson.NewObject("from", row.Get("status"), "to", status))
		}
		if containsString(changed, "assignee") {
			detail.Set("assignee", pyjson.NewObject("from", oldAssignee, "to", assignee))
			to, _ := assignee.(string)
			if err := recordAssignment(w, unitID, to, who.whoAssigns(), actor); err != nil {
				return err
			}
		}
		if err := w.log(actor, "unit.update", unitID, detail); err != nil {
			return err
		}
		if needs != oldNeeds {
			return logNeedsNote(w, actor, unitID, row.Get("uid"), title, oldNeeds, needs, reviewNote)
		}
		return nil
	}()
	return unitID, err
}

func sameSorted(a, b []string) bool {
	x, y := sortedCopy(a), sortedCopy(b)
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

func sortedCopy(a []string) []string {
	out := append([]string(nil), a...)
	sortStrings(out)
	return out
}

func sortedUnique(a []string) []any {
	seen := map[string]bool{}
	var u []string
	for _, x := range a {
		if !seen[x] {
			seen[x] = true
			u = append(u, x)
		}
	}
	sortStrings(u)
	out := make([]any, len(u))
	for i, x := range u {
		out[i] = x
	}
	return out
}

// sameValue is Python == for SQLite cell values.
func sameValue(a, b any) bool { return PyEqual(a, b) }

// syncNeeds is Store._sync_needs.
func syncNeeds(w *wtx, row sqlitedb.Row, archived bool, actor string) error {
	tags, err := tagsOfRow(w.ctx, w.q, row.Int("id"), false)
	if err != nil {
		return err
	}
	old := needsOf(tags)
	next := needsTransition(old, row.Str("type"), row.Str("status"), archived)
	if next == old {
		return nil
	}
	if _, err := w.q.ExecContext(w.ctx, "DELETE FROM unit_tags WHERE unit_id=? AND substr(tag,1,?)=?",
		row.Int("id"), len(needsNS)+1, needsNS+":"); err != nil {
		return err
	}
	if next != "" {
		if _, err := w.q.ExecContext(w.ctx, "INSERT OR IGNORE INTO unit_tags(unit_id,tag) VALUES(?,?)",
			row.Int("id"), needsNS+":"+next); err != nil {
			return err
		}
	}
	if err := reviewTransition(w, row.Int("id"), old, next, row.Str("body"), actor, nil); err != nil {
		return err
	}
	return logNeeds(w, actor, row.Int("id"), row.Get("uid"), row.Get("title"), old, next)
}

// ArchiveUnit is Store.archive_unit.
func (s *Store) ArchiveUnit(ctx context.Context, ref any, actor string) (*pyjson.Object, error) {
	var out *pyjson.Object
	err := s.write(ctx, func(w *wtx) error {
		w.full = true
		row, err := unitRowAny(ctx, w.q, ref)
		if err != nil {
			return err
		}
		if _, err := w.q.ExecContext(ctx, "UPDATE units SET archived=1, updated_at=? WHERE id=?", clock.UTCNow(), row.Int("id")); err != nil {
			return err
		}
		if err := syncNeeds(w, row, true, actor); err != nil {
			return err
		}
		links, err := sqlitedb.Query(ctx, w.q, `SELECT l.id, l.source_id, l.target_id, l.relation, l.note, l.created_at,
                   l.created_by, s.uid suid, t.uid tuid FROM unit_links l JOIN units s ON s.id=l.source_id
                   JOIN units t ON t.id=l.target_id WHERE l.source_id=? OR l.target_id=? ORDER BY l.id`,
			row.Int("id"), row.Int("id"))
		if err != nil {
			return err
		}
		if _, err := w.q.ExecContext(ctx, "DELETE FROM unit_links WHERE source_id=? OR target_id=?", row.Int("id"), row.Int("id")); err != nil {
			return err
		}
		// The removed links go into the activity row so that a restore from
		// the recycle bin can put the node back where it was (trash.go).
		if err := w.log(actor, "unit.archive", row.Int("id"), pyjson.NewObject("uid", row.Get("uid"),
			"title", row.Get("title"), "links_deleted", int64(len(links)), "links", trashLinks(links))); err != nil {
			return err
		}
		out = pyjson.NewObject("id", row.Get("id"), "uid", row.Get("uid"), "archived", true, "links_deleted", int64(len(links)))
		return nil
	})
	return out, err
}

// RestoreUnit is Store.restore_unit.
func (s *Store) RestoreUnit(ctx context.Context, ref any, actor string) (*pyjson.Object, error) {
	var out *pyjson.Object
	err := s.write(ctx, func(w *wtx) error {
		w.full = true
		row, err := unitRowAny(ctx, w.q, ref)
		if err != nil {
			return err
		}
		if err := refuseMoved(row); err != nil { // move.go: undo the move instead
			return err
		}
		if _, err := w.q.ExecContext(ctx, "UPDATE units SET archived=0, updated_at=? WHERE id=?", clock.UTCNow(), row.Int("id")); err != nil {
			return err
		}
		if err := syncNeeds(w, row, false, actor); err != nil {
			return err
		}
		restored := int64(0)
		if row.Int("archived") != 0 {
			if restored, err = restoreTrashLinks(w, row); err != nil {
				return err
			}
		}
		if err := w.log(actor, "unit.restore", row.Int("id"), pyjson.NewObject("uid", row.Get("uid"), "title", row.Get("title"),
			"links_restored", restored)); err != nil {
			return err
		}
		out = pyjson.NewObject("id", row.Get("id"), "uid", row.Get("uid"), "archived", false)
		return nil
	})
	return out, err
}

// InsertBetween is Store.insert_between.
func (s *Store) InsertBetween(ctx context.Context, childRef any, title, typeName any, actor string, who Who) (*pyjson.Object, error) {
	var child, parent sqlitedb.Row
	var parentLink sqlitedb.Row
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		var err error
		if child, err = unitRowAny(ctx, q, childRef); err != nil {
			return err
		}
		var ok bool
		parentLink, ok, err = sqlitedb.QueryOne(ctx, q, "SELECT * FROM unit_links WHERE source_id=? AND relation='part_of' ORDER BY id LIMIT 1", child.Int("id"))
		if err != nil {
			return err
		}
		if !ok {
			return storeErr("This node has no parent to insert between")
		}
		parent, err = unitRowAny(ctx, q, parentLink.Int("target_id"))
		return err
	})
	if err != nil {
		return nil, err
	}
	unit, err := s.CreateUnit(ctx, pyjson.NewObject("type", typeName, "title", title, "status", "open",
		"importance", "p2", "tags", []any{"origin:manual"}, "origin", "manual"), actor, who)
	if err != nil {
		return nil, err
	}
	removed, err := s.DeleteLink(ctx, parentLink.Int("id"), actor)
	if err != nil {
		return nil, err
	}
	newID, _ := unit.Value("id").(int64)
	toParent, err := s.CreateLink(ctx, newID, parent.Int("id"), "part_of", "Inserted between nodes", actor)
	if err != nil {
		return nil, err
	}
	fromChild, err := s.CreateLink(ctx, child.Int("id"), newID, "part_of", "Inserted between nodes", actor)
	if err != nil {
		return nil, err
	}
	err = s.write(ctx, func(w *wtx) error {
		w.noTouch = true
		return w.log(actor, "unit.insert_between", child.Int("id"), pyjson.NewObject("child", child.Get("uid"),
			"parent", parent.Get("uid"), "new", unit.Value("uid")))
	})
	if err != nil {
		return nil, err
	}
	return pyjson.NewObject(
		"unit", unit,
		"parent", pyjson.NewObject("id", parent.Get("id"), "uid", parent.Get("uid"), "title", parent.Get("title")),
		"child", pyjson.NewObject("id", child.Get("id"), "uid", child.Get("uid"), "title", child.Get("title")),
		"removed_link", removed,
		"links", []any{toParent, fromChild}), nil
}

// relationArg is str(relation or "").strip().
func relationArg(relation any) string {
	text := ""
	if pyjson.Truthy(relation) {
		text = pyjson.Str(relation)
	}
	return pytext.Strip(text)
}

// CreateLink is Store.create_link.
func (s *Store) CreateLink(ctx context.Context, source, target, relation, note any, actor string) (*pyjson.Object, error) {
	rel := relationArg(relation)
	if !relationSet[rel] {
		return nil, storeErr("unknown relation: %s", pyjson.Repr(rel))
	}
	var out *pyjson.Object
	err := s.write(ctx, func(w *wtx) error {
		w.full = true
		src, err := unitRowAny(ctx, w.q, source)
		if err != nil {
			return err
		}
		dst, err := unitRowAny(ctx, w.q, target)
		if err != nil {
			return err
		}
		if err := refuseMoved(src); err != nil { // move.go
			return err
		}
		if err := refuseMoved(dst); err != nil {
			return err
		}
		if src.Int("id") == dst.Int("id") {
			return storeErr("a unit cannot link to itself")
		}
		existing, ok, err := sqlitedb.QueryOne(ctx, w.q, "SELECT * FROM unit_links WHERE source_id=? AND target_id=? AND relation=?",
			src.Int("id"), dst.Int("id"), rel)
		if err != nil {
			return err
		}
		if ok {
			w.noTouch, w.full = true, false
			out = pyjson.NewObject("id", existing.Get("id"), "already", true)
			return nil
		}
		text, err := cleanTextField(note, "note", 500, false)
		if err != nil {
			return err
		}
		res, err := w.q.ExecContext(ctx, "INSERT INTO unit_links(source_id,target_id,relation,note,created_at,created_by) VALUES(?,?,?,?,?,?)",
			src.Int("id"), dst.Int("id"), rel, text, clock.UTCNow(), actor)
		if err != nil {
			return err
		}
		linkID, _ := res.LastInsertId()
		if err := w.log(actor, "link.create", src.Int("id"), pyjson.NewObject("from", src.Get("uid"), "to", dst.Get("uid"),
			"relation", rel, "link_id", linkID)); err != nil {
			return err
		}
		out = pyjson.NewObject("id", linkID, "source", src.Get("uid"), "target", dst.Get("uid"), "relation", rel)
		return nil
	})
	return out, err
}

// UpdateLink is Store.update_link.
func (s *Store) UpdateLink(ctx context.Context, linkID int64, relation any, actor string) (*pyjson.Object, error) {
	rel := relationArg(relation)
	if !relationSet[rel] {
		return nil, storeErr("unknown relation: %s", pyjson.Repr(rel))
	}
	var out *pyjson.Object
	err := s.write(ctx, func(w *wtx) error {
		w.full = true
		row, ok, err := sqlitedb.QueryOne(ctx, w.q, "SELECT * FROM unit_links WHERE id=?", linkID)
		if err != nil {
			return err
		}
		if !ok {
			return storeErr("link not found: %d", linkID)
		}
		if _, err := w.q.ExecContext(ctx, "UPDATE unit_links SET relation=? WHERE id=?", rel, linkID); err != nil {
			return err
		}
		if err := w.log(actor, "link.update", row.Get("source_id"), pyjson.NewObject("link_id", linkID,
			"from", row.Get("relation"), "to", rel)); err != nil {
			return err
		}
		out = pyjson.NewObject("id", linkID, "relation", rel, "updated", true)
		return nil
	})
	return out, err
}

// DeleteLink is Store.delete_link.
func (s *Store) DeleteLink(ctx context.Context, linkID int64, actor string) (*pyjson.Object, error) {
	var out *pyjson.Object
	err := s.write(ctx, func(w *wtx) error {
		w.full = true
		row, ok, err := sqlitedb.QueryOne(ctx, w.q, `SELECT l.*, s.uid suid, s.title stitle, t.uid tuid, t.title ttitle
                   FROM unit_links l JOIN units s ON s.id=l.source_id JOIN units t ON t.id=l.target_id
                   WHERE l.id=?`, linkID)
		if err != nil {
			return err
		}
		if !ok {
			return storeErr("link not found: %d", linkID)
		}
		if _, err := w.q.ExecContext(ctx, "DELETE FROM unit_links WHERE id=?", row.Get("id")); err != nil {
			return err
		}
		if err := w.log(actor, "link.delete", row.Get("source_id"), pyjson.NewObject("from", row.Get("suid"),
			"to", row.Get("tuid"), "relation", row.Get("relation"))); err != nil {
			return err
		}
		out = pyjson.NewObject("id", row.Get("id"), "deleted", true)
		return nil
	})
	return out, err
}

var (
	mediaURLRe = regexp.MustCompile(`^(https?://|file://|\.{0,2}/|[A-Za-z]:[\\/])`)
	schemeRe   = regexp.MustCompile(`^[a-z][a-z0-9+.-]*\n?$`)
)

// AddMedia is Store.add_media.
func (s *Store) AddMedia(ctx context.Context, ref, url, kind, caption any, actor string) (*pyjson.Object, error) {
	u, err := cleanTextField(url, "url", maxURL, true)
	if err != nil {
		return nil, err
	}
	if !mediaURLRe.MatchString(u) && strings.Contains(u, "://") {
		scheme := pytext.Lower(u[:strings.Index(u, "://")])
		if scheme == "javascript" || scheme == "data" || scheme == "vbscript" || !schemeRe.MatchString(scheme) {
			return nil, storeErr("media URL scheme is not allowed")
		}
	}
	if !pyjson.Truthy(kind) {
		kind = "link"
	}
	k, err := cleanTextField(kind, "kind", 40, false)
	if err != nil {
		return nil, err
	}
	c, err := cleanTextField(caption, "caption", 300, false)
	if err != nil {
		return nil, err
	}
	var out *pyjson.Object
	err = s.write(ctx, func(w *wtx) error {
		row, err := unitRowAny(ctx, w.q, ref)
		if err != nil {
			return err
		}
		if err := refuseMoved(row); err != nil { // move.go
			return err
		}
		w.units = []int64{row.Int("id")}
		res, err := w.q.ExecContext(ctx, "INSERT INTO unit_media(unit_id,url,kind,caption,created_at) VALUES(?,?,?,?,?)",
			row.Int("id"), u, k, c, clock.UTCNow())
		if err != nil {
			return err
		}
		mediaID, _ := res.LastInsertId()
		if err := w.log(actor, "media.create", row.Int("id"), pyjson.NewObject("uid", row.Get("uid"), "url", u, "kind", k)); err != nil {
			return err
		}
		out = pyjson.NewObject("id", mediaID, "url", u, "kind", k, "caption", c)
		return nil
	})
	return out, err
}

// UpdateMedia is Store.update_media.
func (s *Store) UpdateMedia(ctx context.Context, mediaID int64, patch *pyjson.Object, actor string) (*pyjson.Object, error) {
	var out *pyjson.Object
	err := s.write(ctx, func(w *wtx) error {
		row, ok, err := sqlitedb.QueryOne(ctx, w.q, "SELECT * FROM unit_media WHERE id=?", mediaID)
		if err != nil {
			return err
		}
		if !ok {
			return storeErr("media not found: %d", mediaID)
		}
		w.units = []int64{row.Int("unit_id")}
		caption, kind := row.Get("caption"), row.Get("kind")
		if patch.Has("caption") {
			c, err := cleanTextField(patch.Value("caption"), "caption", 300, false)
			if err != nil {
				return err
			}
			caption = pytext.Strip(c)
		}
		if patch.Has("kind") {
			k := patch.Value("kind")
			if !pyjson.Truthy(k) {
				k = "link"
			}
			if kind, err = cleanTextField(k, "kind", 40, false); err != nil {
				return err
			}
		}
		if _, err := w.q.ExecContext(ctx, "UPDATE unit_media SET caption=?, kind=? WHERE id=?", caption, kind, row.Get("id")); err != nil {
			return err
		}
		if err := w.log(actor, "media.update", row.Get("unit_id"), pyjson.NewObject("url", row.Get("url"), "caption", caption)); err != nil {
			return err
		}
		out = pyjson.NewObject("id", row.Get("id"), "url", row.Get("url"), "kind", kind, "caption", caption)
		return nil
	})
	return out, err
}

// DeleteMedia is Store.delete_media.
func (s *Store) DeleteMedia(ctx context.Context, mediaID int64, actor string) (*pyjson.Object, error) {
	var out *pyjson.Object
	err := s.write(ctx, func(w *wtx) error {
		row, ok, err := sqlitedb.QueryOne(ctx, w.q, "SELECT * FROM unit_media WHERE id=?", mediaID)
		if err != nil {
			return err
		}
		if !ok {
			return storeErr("media not found: %d", mediaID)
		}
		w.units = []int64{row.Int("unit_id")}
		if _, err := w.q.ExecContext(ctx, "DELETE FROM unit_media WHERE id=?", row.Get("id")); err != nil {
			return err
		}
		if err := w.log(actor, "media.delete", row.Get("unit_id"), pyjson.NewObject("url", row.Get("url"))); err != nil {
			return err
		}
		out = pyjson.NewObject("id", row.Get("id"), "deleted", true)
		return nil
	})
	return out, err
}

// pyFloat is float(value) for a decoded JSON value; ok=false is the
// ValueError/TypeError Python raises.  Huge ints overflow (OverflowError).
func pyFloat(v any) (float64, bool, error) {
	switch x := v.(type) {
	case bool:
		if x {
			return 1, true, nil
		}
		return 0, true, nil
	case int64:
		return float64(x), true, nil
	case float64:
		return x, true, nil
	case *big.Int:
		f, _ := new(big.Float).SetInt(x).Float64()
		if math.IsInf(f, 0) {
			return 0, false, &pyerr.InternalError{Message: "int too large to convert to float"}
		}
		return f, true, nil
	case string:
		// float(str): Unicode digits and whitespace, underscores, inf/nan.
		f, ok := pytext.Float(x)
		return f, ok, nil
	}
	return 0, false, nil
}

// pyRound is round(x, n) for a finite float (correctly rounded, half-even
// on the exact binary value, like CPython).
func pyRound(x float64, n int) float64 {
	f, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'f', n, 64), 64)
	return f
}
