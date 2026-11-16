// Package infohub ports arkstudio/importer.py: the read-only InfoHub source
// (browse, graph, unit detail, summary) and the imports into an ARK store.
package infohub

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pytext"
	"arkstudio/go/internal/rnd"
	"arkstudio/go/internal/sqlitedb"
	"arkstudio/go/internal/store"
)

var kindToType = map[string]string{
	"topic": "idea", "idea": "idea", "task": "task", "todo": "task", "event": "milestone",
	"deadline": "milestone", "note": "note", "digest": "note", "announcement": "note",
	"question": "question", "decision": "decision", "paper": "related_work", "resource": "resource",
}

var kindColors = map[string]string{
	"topic": "#3b82f6", "todo": "#ef4444", "event": "#a855f7", "decision": "#f59e0b", "question": "#eab308",
	"announcement": "#14b8a6", "resource": "#10b981", "digest": "#64748b", "note": "#94a3b8",
}

const maxSample = 8

func storeErr(format string, args ...any) error {
	return &store.Error{Message: fmt.Sprintf(format, args...)}
}

// truthy is bool(value) for a sqlite3 value.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case int64:
		return x != 0
	case float64:
		return x != 0
	case string:
		return x != ""
	case []byte:
		return len(x) > 0
	}
	return true
}

func orV(v any, def any) any {
	if truthy(v) {
		return v
	}
	return def
}

// str is str(value) for a sqlite3 value.
func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return pyjson.FloatRepr(x, false)
	case nil:
		return "None"
	case []byte:
		return "b" + pyjson.Repr(string(x))
	}
	return fmt.Sprint(v)
}

// short is _short(text, n).
func short(v any, n int) string {
	text := strings.Join(strings.FieldsFunc(str(orV(v, "")), pytext.IsSpace), " ")
	if pytext.Len(text) <= n {
		return text
	}
	return pytext.Prefix(text, n-1) + "…"
}

// dateFromEpoch is _date_from_epoch(value).
func dateFromEpoch(v any) any {
	if v == nil || v == "" {
		return nil
	}
	var stamp int64
	switch x := v.(type) {
	case int64:
		stamp = x
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) || math.Abs(x) > 9e18 {
			return nil
		}
		stamp = int64(x)
	case string:
		n, err := pytext.Int(x)
		if err != nil {
			return nil
		}
		stamp = n
	default:
		return nil
	}
	if stamp > 10_000_000_000 {
		stamp = floorDiv(stamp, 1000)
	}
	t, ok := utcFromTimestamp(float64(stamp))
	if !ok {
		return nil
	}
	return t.Format("2006-01-02")
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// utcFromTimestamp is datetime.fromtimestamp(value, timezone.utc) where it
// succeeds; false where CPython raises a caught error (OSError on Windows for
// stamps before 1970 or after the year 3000, ValueError for years outside
// 1..9999).
func utcFromTimestamp(v float64) (time.Time, bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return time.Time{}, false
	}
	us := math.RoundToEven(v * 1e6)
	sec := math.Floor(us / 1e6)
	// gmtime_s on Windows: -43200 .. 3000-12-31T23:59:59 + 12h.
	if runtime.GOOS == "windows" && (sec < -43200 || sec > 32536850399) {
		return time.Time{}, false
	}
	t := time.Unix(int64(sec), int64(us-sec*1e6)*1000).UTC()
	if t.Year() < 1 || t.Year() > 9999 {
		return time.Time{}, false
	}
	return t, true
}

// pyFloatText is float(str) (no hex, underscores between digits, Unicode
// digits, inf/nan words).
func pyFloatText(s string) (float64, bool) {
	t := pytext.Strip(s)
	var b strings.Builder
	prev := false
	for i, r := range t {
		if r == '_' {
			next := i + 1
			if !prev || next >= len(t) || !(t[next] >= '0' && t[next] <= '9') && !unicode.Is(unicode.Nd, rune(t[next])) {
				return 0, false
			}
			continue
		}
		if unicode.Is(unicode.Nd, r) {
			n, _ := pytext.Int(string(r))
			b.WriteByte(byte('0' + n))
			prev = true
			continue
		}
		prev = false
		if r > 127 || r == 'x' || r == 'X' || r == 'p' || r == 'P' {
			return 0, false
		}
		b.WriteRune(r)
	}
	f, err := strconv.ParseFloat(b.String(), 64)
	if err != nil {
		var ne *strconv.NumError
		if errors.As(err, &ne) && ne.Err == strconv.ErrRange {
			return f, true
		}
		return 0, false
	}
	return f, true
}

// FloatArg is server._float(value, default, low, high).
func FloatArg(s string, def, low, high float64) float64 {
	f, ok := pyFloatText(s)
	if !ok || f != f {
		return def
	}
	return math.Max(low, math.Min(high, f))
}

// Source is importer.InfoHubSource.
type Source struct {
	Path    string
	db      *sql.DB
	columns map[string]bool
}

// ResolvePath is Path(path).expanduser().resolve().
func ResolvePath(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			p = home + p[1:]
		}
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		if r2, err := filepath.Abs(real); err == nil {
			return r2
		}
	}
	return abs
}

var sqliteCode = regexp.MustCompile(`^(?:[A-Za-z ()_]+: )?(.*?)(?: \(\d+\))?$`)

// pyMessage is str(sqlite3.Error) for a modernc error (the SQLite message
// without the result-code prefix and suffix).
func pyMessage(err error) string {
	msg := err.Error()
	if m := sqliteCode.FindStringSubmatch(msg); m != nil {
		msg = m[1]
	}
	return msg
}

// Open is InfoHubSource(path).
func Open(path string) (*Source, error) {
	full := ResolvePath(path)
	if st, err := os.Stat(full); err != nil || !st.Mode().IsRegular() {
		return nil, storeErr("InfoHub database not found: %s", full)
	}
	dsn := "file:" + (&url.URL{Path: filepath.ToSlash(full)}).EscapedPath() + "?mode=ro&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, storeErr("not a readable InfoHub database: %s", pyMessage(err))
	}
	db.SetMaxOpenConns(1)
	src := &Source{Path: full, db: db, columns: map[string]bool{}}
	if _, err := sqlitedb.Query(context.Background(), db, "SELECT 1 FROM units LIMIT 1"); err != nil {
		db.Close()
		return nil, storeErr("not a readable InfoHub database: %s", pyMessage(err))
	}
	rows, err := sqlitedb.Query(context.Background(), db, "PRAGMA table_info(units)")
	if err != nil {
		db.Close()
		return nil, err
	}
	for _, r := range rows {
		src.columns[r.Str("name")] = true
	}
	return src, nil
}

// Close closes the read-only connection.
func (s *Source) Close() { s.db.Close() }

func (s *Source) query(ctx context.Context, q string, args ...any) ([]sqlitedb.Row, error) {
	rows, err := sqlitedb.Query(ctx, s.db, q, args...)
	if err != nil && ctx.Err() == nil {
		return nil, &SQLError{pyMessage(err)}
	}
	return rows, err
}

// SQLError is a sqlite3.Error of the InfoHub database (str(exc) text): GET
// answers it as "Internal error", POST as "Database error".
type SQLError struct{ Message string }

func (e *SQLError) Error() string { return e.Message }

// noKey is the IndexError of sqlite3.Row[name] for a missing column.
var noKey = &store.InternalError{Message: "No item with that key"}

// strict checks the columns Python reads as row[name] without an
// `in self.columns` guard (sqlite3.Row raises IndexError for a missing one).
type strict struct{ r sqlitedb.Row }

func (g *strict) need(names ...string) error {
	for _, n := range names {
		if !hasCol(g.r, n) {
			return noKey
		}
	}
	return nil
}

// col is `row[name] if name in self.columns else def`.
func (s *Source) col(r sqlitedb.Row, name string, def any) any {
	if s.columns[name] {
		return r.Get(name)
	}
	return def
}

func hasCol(r sqlitedb.Row, name string) bool {
	for _, c := range r.Cols {
		if strings.EqualFold(c, name) {
			return true
		}
	}
	return false
}

// get is sqlite3.Row[name] (case-insensitive, first match).
func get(r sqlitedb.Row, name string) any {
	for i, c := range r.Cols {
		if strings.EqualFold(c, name) {
			return r.Vals[i]
		}
	}
	return nil
}

func rowCol(r sqlitedb.Row, name string, def any) any {
	if hasCol(r, name) {
		return get(r, name)
	}
	return def
}

func countOf(rows []sqlitedb.Row) int64 {
	if len(rows) == 0 {
		return 0
	}
	return rows[0].Int("n")
}

// Summary is InfoHubSource.summary().
func (s *Source) Summary(ctx context.Context) (*pyjson.Object, error) {
	var counts [3]int64
	for i, table := range []string{"units", "unit_tags", "unit_links"} {
		rows, err := s.query(ctx, "SELECT COUNT(*) n FROM "+table)
		if err != nil {
			return nil, err
		}
		counts[i] = countOf(rows)
	}
	kindRows, err := s.query(ctx, "SELECT kind, COUNT(*) n FROM units GROUP BY kind ORDER BY n DESC")
	if err != nil {
		return nil, err
	}
	kinds := []any{}
	topics := any(int64(0))
	found := false
	for _, r := range kindRows {
		kind := orV(r.Get("kind"), "unknown")
		kinds = append(kinds, pyjson.NewObject("kind", kind, "count", r.Get("n")))
		if kind == "topic" && !found {
			topics, found = r.Get("n"), true
		}
	}
	sample := []any{}
	if counts[0] != 0 {
		rows, err := s.query(ctx, "SELECT key, kind, title, summary FROM units "+
			"ORDER BY CASE WHEN kind='topic' THEN 0 ELSE 1 END, id LIMIT ?", maxSample)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			summary := any("")
			if s.columns["summary"] {
				summary = r.Get("summary")
			}
			sample = append(sample, pyjson.NewObject("key", r.Get("key"), "kind", r.Get("kind"),
				"title", r.Get("title"), "summary", short(summary, 160)))
		}
	}
	st, err := os.Stat(s.Path)
	if err != nil {
		return nil, err
	}
	mtime := float64(st.ModTime().UnixNano()) / 1e9
	stamp, _ := utcFromTimestamp(mtime)
	return pyjson.NewObject("path", s.Path, "exists", true, "units", counts[0], "topics", topics,
		"tags", counts[1], "links", counts[2], "kinds", kinds, "sample", sample,
		"mtime", stamp.Format("2006-01-02T15:04:05")+"+00:00"), nil
}

// Split is InfoHubSource._split(value) for a query string.
func Split(value string) []string {
	out := []string{}
	for _, item := range strings.Split(value, ",") {
		if t := pytext.Strip(item); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// Filter is the shared browse/graph filter contract.
type Filter struct {
	Q, Kind, Tag, Tags, Status, Order string
	Days                              float64
	Mine, Roots                       bool
	Limit, Offset                     int64
}

func (s *Source) queryRows(ctx context.Context, f Filter, kinds, tags []string) (int64, []sqlitedb.Row, error) {
	var clauses []string
	var args []any
	if len(kinds) > 0 {
		clauses = append(clauses, "kind IN ("+sqlitedb.Marks(len(kinds))+")")
		for _, k := range kinds {
			args = append(args, k)
		}
	}
	if f.Status != "" && f.Status != "all" {
		clauses = append(clauses, "status=?")
		args = append(args, f.Status)
	} else if s.columns["status"] {
		clauses = append(clauses, "status != 'dropped'")
	}
	if s.columns["for_me"] && f.Mine {
		clauses = append(clauses, "for_me=1")
	}
	if s.columns["last_activity_at"] && f.Days != 0 {
		clauses = append(clauses, "COALESCE(last_activity_at, updated_at, 0) >= ?")
		now := float64(time.Now().UnixNano()) / 1e9
		args = append(args, int64(now-f.Days*86400))
	}
	if f.Roots {
		clauses = append(clauses, "id NOT IN (SELECT child_id FROM unit_links)")
	}
	var order []string
	groups := map[string][]string{}
	for _, tag := range tags {
		ns, _, _ := strings.Cut(tag, ":")
		if _, ok := groups[ns]; !ok {
			order = append(order, ns)
		}
		groups[ns] = append(groups[ns], tag)
	}
	for _, ns := range order {
		group := groups[ns]
		clauses = append(clauses, "id IN (SELECT unit_id FROM unit_tags WHERE tag IN ("+sqlitedb.Marks(len(group))+"))")
		for _, t := range group {
			args = append(args, t)
		}
	}
	for _, term := range strings.FieldsFunc(f.Q, pytext.IsSpace) {
		cols := []string{"title", "key"}
		if s.columns["summary"] {
			cols = append(cols, "summary")
		}
		parts := make([]string, len(cols))
		for i, c := range cols {
			parts[i] = c + " LIKE ?"
			args = append(args, "%"+term+"%")
		}
		clauses = append(clauses, "("+strings.Join(parts, " OR ")+")")
	}
	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + strings.Join(clauses, " AND ")
	}
	totalRows, err := s.query(ctx, "SELECT COUNT(*) n FROM units "+where, args...)
	if err != nil {
		return 0, nil, err
	}
	limit := limitOf(f.Limit, 60)
	offset := max(int64(0), f.Offset)
	orderSQL, ok := map[string]string{
		"activity": "COALESCE(last_activity_at, updated_at, 0) DESC, id DESC",
		"oldest":   "COALESCE(last_activity_at, updated_at, 0) ASC, id ASC",
		"due":      "COALESCE(due_at, start_at) IS NULL, COALESCE(due_at, start_at), id DESC",
		"title":    "title COLLATE NOCASE ASC, id ASC",
		"created":  "created_at DESC, id DESC",
	}[f.Order]
	if !ok {
		orderSQL = "COALESCE(updated_at, 0) DESC, id DESC"
	}
	if !s.columns["updated_at"] {
		orderSQL = "id DESC"
	}
	rows, err := s.query(ctx, "SELECT * FROM units "+where+" ORDER BY "+orderSQL+" LIMIT ? OFFSET ?",
		append(args, limit, offset)...)
	return countOf(totalRows), rows, err
}

func limitOf(n, def int64) int64 {
	if n == 0 {
		n = def
	}
	return max(1, min(n, 2000))
}

// importedRefs is the set of infohub: source refs in the ARK store.
func importedRefs(ctx context.Context, st *store.Store) (map[string]bool, error) {
	out := map[string]bool{}
	if st == nil {
		return out, nil
	}
	err := st.ReadQuery(ctx, func(q sqlitedb.Querier) error {
		rows, err := sqlitedb.Query(ctx, q, "SELECT source_ref FROM units WHERE source_ref LIKE 'infohub:%'")
		for _, r := range rows {
			out[str(r.Get("source_ref"))] = true
		}
		return err
	})
	return out, err
}

// directCounts is {unit_id: messages} (empty when unit_messages is missing).
func (s *Source) directCounts(ctx context.Context) map[any]int64 {
	out := map[any]int64{}
	rows, err := s.query(ctx, "SELECT unit_id, COUNT(*) n FROM unit_messages GROUP BY unit_id")
	if err != nil {
		return map[any]int64{}
	}
	for _, r := range rows {
		out[key(r.Get("unit_id"))] = r.Int("n")
	}
	return out
}

// key normalizes a sqlite3 value for use as a Python dict key (1 == 1.0).
func key(v any) any {
	if f, ok := v.(float64); ok && f == math.Trunc(f) && math.Abs(f) < 1e18 {
		return int64(f)
	}
	return v
}

func unitKey(r sqlitedb.Row) any {
	if truthy(r.Get("key")) {
		return r.Get("key")
	}
	return "id-" + str(r.Get("id"))
}

func (s *Source) items(ctx context.Context, rows []sqlitedb.Row, st *store.Store) ([]*pyjson.Object, error) {
	imported, err := importedRefs(ctx, st)
	if err != nil {
		return nil, err
	}
	direct := s.directCounts(ctx)
	out := []*pyjson.Object{}
	for _, r := range rows {
		if err := (&strict{r}).need("key", "id", "kind", "title"); err != nil {
			return nil, err
		}
		k := unitKey(r)
		tags, err := s.tagsFor(ctx, r.Get("id"))
		if err != nil {
			return nil, err
		}
		shown := tags
		if len(shown) > 10 {
			shown = shown[:10]
		}
		forMe := false
		if s.columns["for_me"] {
			forMe = truthy(r.Get("for_me"))
		}
		var dueAt, updatedAt any
		if s.columns["due_at"] {
			dueAt = r.Get("due_at")
		}
		if s.columns["updated_at"] {
			updatedAt = r.Get("updated_at")
		}
		out = append(out, pyjson.NewObject(
			"id", r.Get("id"), "key", k, "kind", orV(r.Get("kind"), "unknown"), "title", orV(r.Get("title"), "Untitled"),
			"summary", short(s.col(r, "summary", ""), 240), "status", s.col(r, "status", ""),
			"priority", s.col(r, "priority", ""), "due_at", dueAt, "for_me", forMe,
			"n_messages", direct[key(r.Get("id"))], "tags", anyList(shown), "source", sourceTag(tags),
			"updated_at", updatedAt, "imported", imported["infohub:"+str(k)]))
	}
	return out, nil
}

func anyList(items []string) []any {
	out := make([]any, len(items))
	for i, s := range items {
		out[i] = s
	}
	return out
}

func sourceTag(tags []string) string {
	for _, t := range tags {
		if strings.HasPrefix(t, "source:") {
			_, v, _ := strings.Cut(t, ":")
			return v
		}
	}
	return ""
}

// pyRound1 is round(x, 1).
func pyRound1(x float64) float64 {
	f, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'f', 1, 64), 64)
	return f
}

// Browse is InfoHubSource.browse(store, ...).
func (s *Source) Browse(ctx context.Context, st *store.Store, f Filter) (*pyjson.Object, error) {
	kinds := Split(f.Kind)
	tags := Split(f.Tags)
	if len(tags) == 0 {
		tags = Split(f.Tag)
	}
	total, rows, err := s.queryRows(ctx, f, kinds, tags)
	if err != nil {
		return nil, err
	}
	items, err := s.items(ctx, rows, st)
	if err != nil {
		return nil, err
	}
	kindRows, err := s.query(ctx, "SELECT kind, COUNT(*) n FROM units WHERE status != 'dropped' GROUP BY kind ORDER BY n DESC")
	if err != nil {
		return nil, err
	}
	kindFacet := []any{}
	for _, r := range kindRows {
		kindFacet = append(kindFacet, pyjson.NewObject("kind", orV(r.Get("kind"), "unknown"), "count", r.Get("n")))
	}
	tagRows, err := s.query(ctx, "SELECT tag, COUNT(*) n FROM unit_tags GROUP BY tag ORDER BY n DESC")
	if err != nil {
		return nil, err
	}
	tagFacet := []any{}
	for _, r := range tagRows {
		tagFacet = append(tagFacet, pyjson.NewObject("tag", r.Get("tag"), "count", r.Get("n")))
	}
	var timeCols []string
	for _, c := range []string{"last_activity_at", "updated_at", "created_at"} {
		if s.columns[c] {
			timeCols = append(timeCols, c)
		}
	}
	var oldest any
	if len(timeCols) > 0 {
		where := ""
		if s.columns["status"] {
			where = " WHERE status != 'dropped'"
		}
		if r, err := s.query(ctx, "SELECT MIN(COALESCE("+strings.Join(timeCols, ",")+")) value FROM units"+where); err == nil && len(r) > 0 {
			oldest = r[0].Get("value")
		}
	}
	age := 365.0
	if truthy(oldest) {
		var o float64
		switch x := oldest.(type) {
		case int64:
			o = float64(x)
		case float64:
			o = x
		default:
			return nil, &store.InternalError{Message: "unsupported operand type(s) for -: 'float' and '" + pyTypeName(oldest) + "'"}
		}
		now := float64(time.Now().UnixNano()) / 1e9
		age = math.Max(1.0, (now-o)/86400)
	}
	list := make([]any, len(items))
	for i, it := range items {
		list[i] = it
	}
	return pyjson.NewObject("path", s.Path, "total", total, "items", list, "kinds", kindFacet, "tags", tagFacet,
		"max_days", pyRound1(math.Min(3650.0, age)), "offset", max(int64(0), f.Offset), "limit", limitOf(f.Limit, 60)), nil
}

func pyTypeName(v any) string {
	switch v.(type) {
	case string:
		return "str"
	case []byte:
		return "bytes"
	case nil:
		return "NoneType"
	}
	return store.PyTypeName(v)
}

// Graph is InfoHubSource.graph(store, ...).
func (s *Source) Graph(ctx context.Context, st *store.Store, f Filter, connected, withContext bool) (*pyjson.Object, error) {
	data, err := s.Browse(ctx, st, f)
	if err != nil {
		return nil, err
	}
	var items []*pyjson.Object
	for _, it := range data.Value("items").([]any) {
		items = append(items, it.(*pyjson.Object))
	}
	ids := map[any]bool{}
	var idOrder []any
	for _, it := range items {
		k := key(it.Value("id"))
		if !ids[k] {
			ids[k] = true
			idOrder = append(idOrder, it.Value("id"))
		}
	}
	seed := map[any]bool{}
	for k := range ids {
		seed[k] = true
	}
	if withContext && len(ids) > 0 {
		marks := sqlitedb.Marks(len(idOrder))
		args := append(append(append([]any{}, idOrder...), idOrder...), f.Limit)
		rows, err := s.query(ctx, "SELECT * FROM units WHERE status != 'dropped' AND id IN ("+
			"SELECT parent_id FROM unit_links WHERE child_id IN ("+marks+") UNION "+
			"SELECT child_id FROM unit_links WHERE parent_id IN ("+marks+")) LIMIT ?", args...)
		if err != nil {
			return nil, err
		}
		more, err := s.items(ctx, rows, st)
		if err != nil {
			return nil, err
		}
		for _, it := range more {
			k := key(it.Value("id"))
			if !ids[k] {
				it.Set("seed", false)
				items = append(items, it)
				ids[k] = true
				idOrder = append(idOrder, it.Value("id"))
			}
		}
	}
	for _, it := range items {
		if !it.Has("seed") {
			it.Set("seed", true)
		}
	}
	direct := s.directCounts(ctx)
	children := map[any][]any{}
	linkRows, err := s.query(ctx, "SELECT parent_id, child_id FROM unit_links")
	if err != nil {
		return nil, err
	}
	for _, r := range linkRows {
		p := key(r.Get("parent_id"))
		children[p] = append(children[p], r.Get("child_id"))
	}
	total := messageTotals(direct, children)
	maxMessages := int64(1)
	if len(items) > 0 {
		maxMessages = math.MinInt64
		for _, it := range items {
			maxMessages = max(maxMessages, total(it.Value("id")))
		}
	}
	nodes := []*pyjson.Object{}
	for _, it := range items {
		kind := pytext.Lower(str(orV(it.Value("kind"), "unknown")))
		n := total(it.Value("id"))
		ratio := float64(max(0, n)) / float64(max(1, maxMessages))
		typ, ok := kindToType[kind]
		if !ok {
			typ = "idea"
		}
		color, ok := kindColors[kind]
		if !ok {
			color = "#94a3b8"
		}
		nodes = append(nodes, pyjson.NewObject(
			"id", it.Value("id"), "uid", it.Value("key"), "key", it.Value("key"), "kind", kind, "type", typ,
			"title", it.Value("title"), "status", orV(it.Value("status"), "open"), "importance", "p2",
			"tags", it.Value("tags"), "summary", it.Value("summary"), "imported", it.Value("imported"),
			"for_me", it.Value("for_me"), "seed", it.Value("seed"), "n_messages", n,
			"radius", 5+21*math.Pow(ratio, 0.5), "color", color, "source", it.Value("source"),
			"due_at", it.Value("due_at"), "degree", int64(0)))
	}
	type edge struct {
		obj            *pyjson.Object
		source, target any
	}
	var edges []edge
	if len(ids) > 0 {
		marks := sqlitedb.Marks(len(idOrder))
		rows, err := s.query(ctx, "SELECT parent_id, child_id, relation FROM unit_links "+
			"WHERE parent_id IN ("+marks+") AND child_id IN ("+marks+")", append(append([]any{}, idOrder...), idOrder...)...)
		if err != nil {
			return nil, err
		}
		for i, r := range rows {
			relation := orV(r.Get("relation"), "part_of")
			src, dst := r.Get("parent_id"), r.Get("child_id")
			if relation == "part_of" {
				src, dst = dst, src
			}
			if !ids[key(src)] || !ids[key(dst)] || key(src) == key(dst) {
				continue
			}
			edges = append(edges, edge{pyjson.NewObject("id", int64(i+1), "source", src, "target", dst, "relation", relation), src, dst})
		}
	}
	if connected && len(edges) > 0 {
		linked := map[any]bool{}
		for _, e := range edges {
			linked[key(e.source)], linked[key(e.target)] = true, true
		}
		kept := nodes[:0]
		for _, n := range nodes {
			if linked[key(n.Value("id"))] {
				kept = append(kept, n)
			}
		}
		nodes = kept
	}
	degree := map[any]int64{}
	for _, e := range edges {
		degree[key(e.source)]++
		degree[key(e.target)]++
	}
	nodeIDs := map[any]bool{}
	nodeList := make([]any, len(nodes))
	for i, n := range nodes {
		n.Set("degree", degree[key(n.Value("id"))])
		nodeIDs[key(n.Value("id"))] = true
		nodeList[i] = n
	}
	edgeList := make([]any, len(edges))
	for i, e := range edges {
		edgeList[i] = e.obj
	}
	common := 0
	for k := range seed {
		if nodeIDs[k] {
			common++
		}
	}
	return pyjson.NewObject("path", data.Value("path"), "total", data.Value("total"), "matched_count", int64(len(seed)),
		"context_count", int64(len(nodes)-common), "nodes", nodeList, "edges", edgeList,
		"kinds", data.Value("kinds"), "tags", data.Value("tags"), "max_days", data.Value("max_days")), nil
}

// messageTotals is the memoized message_total(unit_id) closure of graph()
// and unit(): direct messages plus every descendant branch, cycles cut.
func messageTotals(direct map[any]int64, children map[any][]any) func(any) int64 {
	memo := map[any]int64{}
	var walk func(id any, seen map[any]bool) int64
	walk = func(id any, seen map[any]bool) int64 {
		k := key(id)
		if v, ok := memo[k]; ok {
			return v
		}
		if len(seen) == 0 {
			seen = map[any]bool{}
		}
		if seen[k] {
			return 0
		}
		seen[k] = true
		total := direct[k]
		for _, child := range children[k] {
			total += walk(child, seen)
		}
		memo[k] = total
		return total
	}
	return func(id any) int64 { return walk(id, nil) }
}

// iso is InfoHubSource._iso(value).
func iso(v any) any {
	if v == nil || v == "" {
		return nil
	}
	var f float64
	switch x := v.(type) {
	case int64:
		f = float64(x)
	case float64:
		f = x
	case string:
		var ok bool
		if f, ok = pyFloatText(x); !ok {
			return nil
		}
	default:
		return nil
	}
	t, ok := utcFromTimestamp(f)
	if !ok {
		return nil
	}
	return t.Format("2006-01-02T15:04:05") + "+00:00"
}

func (s *Source) findUnit(ctx context.Context, k string) (sqlitedb.Row, error) {
	numeric := strings.TrimPrefix(k, "id-")
	if !strings.HasPrefix(k, "id-") {
		numeric = k
	}
	rows, err := s.query(ctx, "SELECT * FROM units WHERE key=? OR CAST(id AS TEXT)=? OR id=?", k, numeric, numeric)
	if err != nil {
		return sqlitedb.Row{}, err
	}
	if len(rows) == 0 {
		return sqlitedb.Row{}, storeErr("InfoHub unit not found: %s", k)
	}
	return rows[0], nil
}

// Unit is InfoHubSource.unit(key, store).
func (s *Source) Unit(ctx context.Context, k string, st *store.Store) (*pyjson.Object, error) {
	row, err := s.findUnit(ctx, k)
	if err != nil {
		return nil, err
	}
	if err := (&strict{row}).need("id"); err != nil {
		return nil, err
	}
	unitID := row.Get("id")
	tags, err := s.tagsFor(ctx, unitID)
	if err != nil {
		return nil, err
	}
	messages := []any{}
	if rows, err := s.query(ctx, "SELECT m.* FROM unit_messages um JOIN messages m ON m.id=um.message_id "+
		"WHERE um.unit_id=? ORDER BY COALESCE(m.sent_at,0) ASC, m.id ASC LIMIT 500", unitID); err == nil {
		for _, m := range rows {
			text := orV(rowCol(m, "text", ""), "")
			messages = append(messages, pyjson.NewObject(
				"id", get(m, "id"), "source", rowCol(m, "source", ""), "channel", rowCol(m, "channel", ""),
				"channel_name", rowCol(m, "channel_name", ""), "sender", rowCol(m, "sender", ""),
				"sender_name", rowCol(m, "sender_name", ""), "sent_at", rowCol(m, "sent_at", nil),
				"sent_at_iso", iso(rowCol(m, "sent_at", nil)), "kind", rowCol(m, "kind", ""), "text", text,
				"link", rowCol(m, "link", ""), "mentions_me", hasCol(m, "mentions_me") && truthy(get(m, "mentions_me")),
				"from_me", hasCol(m, "from_me") && truthy(get(m, "from_me")), "priority", rowCol(m, "priority", ""),
				"labels", rowCol(m, "labels", "")))
		}
	}
	direct := map[any]int64{}
	childMap := map[any][]any{}
	if rows, err := s.query(ctx, "SELECT unit_id, COUNT(*) n FROM unit_messages GROUP BY unit_id"); err == nil {
		for _, r := range rows {
			direct[key(r.Get("unit_id"))] = r.Int("n")
		}
		if links, err := s.query(ctx, "SELECT parent_id, child_id FROM unit_links"); err == nil {
			for _, r := range links {
				p := key(r.Get("parent_id"))
				childMap[p] = append(childMap[p], r.Get("child_id"))
			}
		}
	}
	total := messageTotals(direct, childMap)
	childInfo := func(r sqlitedb.Row, relation any) (*pyjson.Object, error) {
		ctags, err := s.tagsFor(ctx, get(r, "id"))
		if err != nil {
			return nil, err
		}
		k := get(r, "key")
		if !truthy(k) {
			k = "id-" + str(get(r, "id"))
		}
		return pyjson.NewObject(
			"key", k, "title", orV(get(r, "title"), "Untitled"), "kind", orV(get(r, "kind"), "unknown"),
			"status", rowCol(r, "status", ""), "relation", orV(relation, "part_of"),
			"summary", short(rowCol(r, "summary", ""), 280), "tags", anyList(ctags),
			"n_children", int64(len(childMap[key(get(r, "id"))])), "n_messages", direct[key(get(r, "id"))],
			"n_messages_total", total(get(r, "id")),
			"last_activity_at_iso", iso(rowCol(r, "last_activity_at", nil)), "updated_at_iso", iso(rowCol(r, "updated_at", nil))), nil
	}
	parents, children := []any{}, []any{}
	pRows, perr := s.query(ctx, "SELECT u.*, l.relation FROM unit_links l JOIN units u ON u.id=l.parent_id "+
		"WHERE l.child_id=? ORDER BY u.title", unitID)
	if perr == nil {
		var built []any
		for _, r := range pRows {
			info, err := childInfo(r, get(r, "relation"))
			if err != nil {
				return nil, err
			}
			built = append(built, info)
		}
		cRows, cerr := s.query(ctx, "SELECT u.*, l.relation FROM unit_links l JOIN units u ON u.id=l.child_id "+
			"WHERE l.parent_id=? ORDER BY u.title", unitID)
		if cerr == nil {
			parents = append(parents, built...)
			for _, r := range cRows {
				info, err := childInfo(r, get(r, "relation"))
				if err != nil {
					return nil, err
				}
				children = append(children, info)
			}
		} else {
			parents = append(parents, built...)
		}
	}
	sourceKey := get(row, "key")
	if !truthy(sourceKey) {
		sourceKey = "id-" + str(unitID)
	}
	sourceRef := "infohub:" + str(sourceKey)
	imported := false
	if st != nil {
		err := st.ReadQuery(ctx, func(q sqlitedb.Querier) error {
			_, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT 1 FROM units WHERE source_ref=?", sourceRef)
			imported = ok
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	boolCol := func(name string) bool { return hasCol(row, name) && truthy(get(row, name)) }
	return pyjson.NewObject(
		"id", unitID, "key", sourceKey, "kind", orV(get(row, "kind"), "unknown"), "title", orV(get(row, "title"), "Untitled"),
		"summary", rowCol(row, "summary", ""), "status", rowCol(row, "status", ""), "priority", rowCol(row, "priority", ""),
		"origin", rowCol(row, "origin", ""), "confidence", rowCol(row, "confidence", nil),
		"for_me", boolCol("for_me"), "polished", boolCol("polished"),
		"due_at", rowCol(row, "due_at", nil), "start_at", rowCol(row, "start_at", nil), "end_at", rowCol(row, "end_at", nil),
		"created_at", rowCol(row, "created_at", nil), "updated_at", rowCol(row, "updated_at", nil),
		"last_activity_at", rowCol(row, "last_activity_at", nil),
		"created_at_iso", iso(rowCol(row, "created_at", nil)), "updated_at_iso", iso(rowCol(row, "updated_at", nil)),
		"last_activity_at_iso", iso(rowCol(row, "last_activity_at", nil)), "due_at_iso", iso(rowCol(row, "due_at", nil)),
		"tags", anyList(tags), "source", sourceTag(tags), "messages", messages, "n_messages", int64(len(messages)),
		"n_messages_total", total(unitID), "n_children", int64(len(children)), "parents", parents, "children", children,
		"imported", imported, "source_ref", sourceRef), nil
}

func (s *Source) summaryText(r sqlitedb.Row) string {
	return str(orV(s.col(r, "summary", ""), ""))
}

// importBody is InfoHubSource._import_body(row).
func (s *Source) importBody(ctx context.Context, r sqlitedb.Row) string {
	summary := pytext.Strip(s.summaryText(r))
	channel := s.col(r, "channel_name", "")
	var lines []string
	rows, err := s.query(ctx, "SELECT m.* FROM unit_messages um JOIN messages m ON m.id=um.message_id "+
		"WHERE um.unit_id=? ORDER BY COALESCE(m.sent_at,0) ASC, m.id ASC LIMIT 80", r.Get("id"))
	if err != nil {
		rows = nil
	}
	seen := map[string]bool{}
	for _, m := range rows {
		text := strings.Join(strings.FieldsFunc(str(rowCol(m, "text", "")), pytext.IsSpace), " ")
		if text == "" || seen[text] {
			continue
		}
		seen[text] = true
		sender := orV(rowCol(m, "sender_name", ""), nil)
		if sender == nil {
			sender = orV(rowCol(m, "sender", ""), "unknown")
		}
		lines = append(lines, "- "+str(sender)+": "+pytext.Prefix(text, 180))
	}
	if len(lines) > 8 {
		lines = lines[len(lines)-8:]
	}
	var parts []string
	if summary != "" {
		parts = append(parts, pytext.Prefix(summary, 1200))
	}
	if len(lines) > 0 {
		parts = append(parts, "Conversation (condensed):\n"+strings.Join(lines, "\n"))
	}
	if truthy(channel) {
		ref := r.Get("id")
		if hasCol(r, "key") {
			ref = r.Get("key")
		}
		parts = append(parts, "InfoHub: "+str(ref)+" · "+str(channel))
	}
	return pytext.Prefix(strings.Join(parts, "\n\n"), 6000)
}

// importance is InfoHubSource._importance(row).
func (s *Source) importance(r sqlitedb.Row) string {
	v := pytext.Strip(pytext.Lower(str(orV(s.col(r, "priority", ""), ""))))
	switch v {
	case "p0", "p1", "p2", "p3":
		return v
	case "high", "critical", "urgent":
		return "p0"
	case "medium", "normal":
		return "p2"
	case "low":
		return "p3"
	}
	return "p2"
}

// tagsFor is InfoHubSource._tags_for(unit_id).
func (s *Source) tagsFor(ctx context.Context, unitID any) ([]string, error) {
	rows, err := s.query(ctx, "SELECT tag FROM unit_tags WHERE unit_id=?", unitID)
	if err != nil {
		return nil, err
	}
	tags := []string{}
	for _, r := range rows {
		tag := pytext.Strip(str(orV(r.Get("tag"), "")))
		ns, _, _ := strings.Cut(tag, ":")
		if tag == "" || ns == "type" || ns == "status" || ns == "importance" {
			continue
		}
		tag = strings.ReplaceAll(tag, " ", "-")
		if !strings.Contains(tag, ":") {
			tag = "tag:" + tag
		}
		tags = append(tags, pytext.Prefix(tag, 96))
	}
	return tags, nil
}

func isAlnum(r rune) bool {
	return unicode.IsLetter(r) || unicode.In(r, unicode.Nd, unicode.Nl, unicode.No)
}

// tagValue is importer._tag_value(value).
func tagValue(v string) string {
	var b strings.Builder
	for _, r := range pytext.Lower(v) {
		if isAlnum(r) || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	text := pytext.Prefix(strings.Trim(b.String(), "-"), 48)
	if text == "" {
		return "unknown"
	}
	return text
}

func sourceID(ctx context.Context, st *store.Store, ref string) (any, bool, error) {
	var id any
	var found bool
	err := st.ReadQuery(ctx, func(q sqlitedb.Querier) error {
		row, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT id FROM units WHERE source_ref=?", ref)
		if ok {
			id, found = row.Get("id"), true
		}
		return err
	})
	return id, found, err
}

func (s *Source) dates(r sqlitedb.Row) (any, any) {
	start := dateFromEpoch(s.col(r, "start_at", nil))
	due := dateFromEpoch(s.col(r, "due_at", nil))
	end := dateFromEpoch(s.col(r, "end_at", nil))
	if !truthy(end) {
		end = due
	}
	return start, end
}

func sliceText(v any, n int) any {
	t, ok := v.(string)
	if !ok {
		return v
	}
	return pytext.Prefix(t, n)
}

func isStoreErr(err error) bool {
	var se *store.Error
	return errors.As(err, &se)
}

// ImportKey is InfoHubSource.import_key(store, key, actor, as_idea, copy).
func (s *Source) ImportKey(ctx context.Context, st *store.Store, k string, actor string, asIdea, copyUnit bool, who store.Who) (*pyjson.Object, error) {
	row, err := s.findUnit(ctx, k)
	if err != nil {
		return nil, err
	}
	if err := (&strict{row}).need("key", "id"); err != nil {
		return nil, err
	}
	sourceKey := unitKey(row)
	sourceRef := "infohub:" + str(sourceKey)
	var existing any
	var found bool
	if !copyUnit {
		if existing, found, err = sourceID(ctx, st, sourceRef); err != nil {
			return nil, err
		}
	} else {
		sourceRef += "#copy-" + rnd.UUID4Hex()[:10]
	}
	if found {
		current, err := st.GetUnitAny(ctx, existing)
		if err != nil {
			return nil, err
		}
		patch := pyjson.NewObject()
		if t, _ := current.Value("type").(string); asIdea && t != "idea" && t != "question" && t != "source" {
			patch.Set("type", "idea")
		}
		body := pytext.Strip(pyjson.Str(orV(current.Value("body"), "")))
		if body == "" || strings.HasPrefix(body, "InfoHub source:") {
			if text := s.importBody(ctx, row); text != "" {
				patch.Set("body", text)
			}
		}
		if patch.Len() > 0 {
			if _, err := st.UpdateUnit(ctx, existing, patch, actor, who); err == nil {
				if current, err = st.GetUnitAny(ctx, existing); err != nil {
					return nil, err
				}
			} else if !isStoreErr(err) {
				return nil, err
			}
		}
		return pyjson.NewObject("unit", current, "imported", false, "skipped", true), nil
	}
	if err := (&strict{row}).need("kind", "title", "status"); err != nil {
		return nil, err
	}
	kind := pytext.Lower(str(orV(row.Get("kind"), "topic")))
	tags, err := s.tagsFor(ctx, row.Get("id"))
	if err != nil {
		return nil, err
	}
	tags = append(tags, "origin:infohub", "system:infohub", "ih-kind:"+tagValue(kind))
	start, end := s.dates(row)
	body := s.importBody(ctx, row)
	channel := s.col(row, "channel_name", "")
	mapped := "idea"
	if !asIdea {
		if t, ok := kindToType[kind]; ok {
			mapped = t
		}
	}
	status := "open"
	if orV(row.Get("status"), "") == "done" {
		status = "done"
	}
	sourceTitle := "InfoHub"
	if truthy(channel) {
		sourceTitle = "InfoHub · " + str(channel)
	}
	unit, err := st.CreateUnit(ctx, pyjson.NewObject(
		"type", mapped, "title", sliceText(orV(row.Get("title"), "Untitled"), 240),
		"body", pytext.Prefix(body, 200_000), "tags", anyList(tags), "status", status,
		"importance", s.importance(row), "start_at", start, "end_at", end, "origin", "infohub",
		"source_ref", sourceRef, "source_title", sourceTitle,
		"meta", pyjson.NewObject("infohub", pyjson.NewObject("key", sourceKey, "kind", kind, "status", row.Get("status"),
			"summary", pytext.Prefix(s.summaryText(row), 4000)))), actor, who)
	if err != nil {
		return nil, err
	}
	if s.columns["parent_id"] && truthy(row.Get("parent_id")) {
		if parents, err := s.query(ctx, "SELECT key FROM units WHERE id=?", row.Get("parent_id")); err == nil && len(parents) > 0 {
			if id, ok, err := sourceID(ctx, st, "infohub:"+str(parents[0].Get("key"))); err != nil {
				return nil, err
			} else if ok {
				if _, err := st.CreateLink(ctx, unit.Value("id"), id, "part_of", "Imported hierarchy", actor); err != nil && !isStoreErr(err) {
					return nil, err
				}
			}
		} else if err != nil {
			return nil, err
		}
	}
	if err := st.LogActivity(ctx, actor, "import.infohub.unit", unit.Value("id"),
		pyjson.NewObject("path", s.Path, "key", sourceKey, "kind", kind)); err != nil {
		return nil, err
	}
	return pyjson.NewObject("unit", unit, "imported", true, "skipped", false), nil
}

// ImportInto is InfoHubSource.import_into(store, mode, update, actor).
func (s *Source) ImportInto(ctx context.Context, st *store.Store, mode string, update bool, actor string, who store.Who) (*pyjson.Object, error) {
	if mode != "topics" && mode != "all" {
		return nil, storeErr("mode must be topics or all")
	}
	where := ""
	if mode == "topics" {
		where = "WHERE kind='topic'"
	}
	rows, err := s.query(ctx, "SELECT * FROM units "+where+" ORDER BY id")
	if err != nil {
		return nil, err
	}
	var imported, skipped, updated, linkCount int64
	oldToNew := map[any]any{}
	for _, row := range rows {
		if err := (&strict{row}).need("id", "key"); err != nil {
			return nil, err
		}
		oldID := row.Get("id")
		sourceRef := "infohub:" + str(unitKey(row))
		existing, found, err := sourceID(ctx, st, sourceRef)
		if err != nil {
			return nil, err
		}
		if found {
			if update {
				if err := (&strict{row}).need("title", "summary", "status"); err != nil {
					return nil, err
				}
				status := "open"
				if row.Get("status") == "done" {
					status = "done"
				}
				if _, err := st.UpdateUnit(ctx, existing, pyjson.NewObject(
					"title", orV(row.Get("title"), "Untitled"), "body", orV(row.Get("summary"), ""),
					"status", status, "importance", s.importance(row)), actor, who); err != nil {
					return nil, err
				}
				updated++
			} else {
				skipped++
			}
			oldToNew[key(oldID)] = existing
			continue
		}
		if err := (&strict{row}).need("kind", "status", "summary", "title"); err != nil {
			return nil, err
		}
		kind := pytext.Lower(str(orV(row.Get("kind"), "topic")))
		typ, ok := kindToType[kind]
		if !ok {
			typ = "idea"
		}
		tags, err := s.tagsFor(ctx, oldID)
		if err != nil {
			return nil, err
		}
		tags = append(tags, "origin:infohub", "system:infohub", "ih-kind:"+tagValue(kind))
		status := "open"
		if orV(row.Get("status"), "") == "done" {
			status = "done"
		}
		start, end := s.dates(row)
		body := str(orV(row.Get("summary"), ""))
		channel := s.col(row, "channel_name", "")
		if truthy(channel) {
			body = pytext.Strip(body + "\n\nImported from InfoHub channel: " + str(channel))
		}
		sourceTitle := "InfoHub"
		if truthy(channel) {
			sourceTitle = "InfoHub · " + str(channel)
		}
		unit, err := st.CreateUnit(ctx, pyjson.NewObject(
			"type", typ, "title", sliceText(orV(row.Get("title"), "Untitled"), 240),
			"body", pytext.Prefix(body, 200_000), "tags", anyList(tags), "status", status,
			"importance", s.importance(row), "start_at", start, "end_at", end, "origin", "infohub",
			"source_ref", sourceRef, "source_title", sourceTitle,
			"meta", pyjson.NewObject("infohub", pyjson.NewObject("key", unitKey(row), "kind", kind, "status", row.Get("status")))), actor, who)
		if err != nil {
			if isStoreErr(err) {
				skipped++
				continue
			}
			return nil, err
		}
		oldToNew[key(oldID)] = unit.Value("id")
		imported++
	}
	links, err := s.query(ctx, "SELECT * FROM unit_links")
	if err != nil {
		return nil, err
	}
	for _, l := range links {
		relation := orV(l.Get("relation"), "part_of")
		src, dst := oldToNew[key(l.Get("parent_id"))], oldToNew[key(l.Get("child_id"))]
		if relation == "part_of" {
			src, dst = dst, src
		}
		if !pyjson.Truthy(src) || !pyjson.Truthy(dst) || pyjson.Str(src) == pyjson.Str(dst) {
			continue
		}
		made, err := st.CreateLink(ctx, src, dst, relation, "Imported from InfoHub", actor)
		if err != nil {
			if isStoreErr(err) {
				continue
			}
			return nil, err
		}
		if !pyjson.Truthy(made.Value("already")) {
			linkCount++
		}
	}
	if err := st.LogActivity(ctx, actor, "import.infohub", nil, pyjson.NewObject(
		"path", s.Path, "mode", mode, "imported", imported, "skipped", skipped, "updated", updated, "links", linkCount)); err != nil {
		return nil, err
	}
	return pyjson.NewObject("imported", imported, "skipped", skipped, "updated", updated, "links", linkCount,
		"total", int64(len(rows)), "mode", mode), nil
}

// Candidates is importer.default_candidates().
func Candidates() []any {
	var paths []string
	if env := os.Getenv("INFOHUB_DB"); env != "" {
		paths = append(paths, env)
	}
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	for _, root := range []string{cwd, filepath.Join(cwd, "info-hub"), filepath.Join(filepath.Dir(cwd), "info-hub"), filepath.Join(home, "info-hub")} {
		paths = append(paths, filepath.Join(root, "data", "hub.db"), filepath.Join(root, "infohub.db"))
	}
	seen := map[string]bool{}
	out := []any{}
	for _, p := range paths {
		full := ResolvePath(p)
		k := strings.ToLower(full)
		if seen[k] {
			continue
		}
		seen[k] = true
		if st, err := os.Stat(full); err == nil && st.Mode().IsRegular() {
			out = append(out, pyjson.NewObject("path", full, "size", st.Size()))
		}
	}
	return out
}

// Preview is importer.preview(path).
func Preview(ctx context.Context, path string) (*pyjson.Object, error) {
	src, err := Open(path)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	return src.Summary(ctx)
}

var _ = sort.Strings
