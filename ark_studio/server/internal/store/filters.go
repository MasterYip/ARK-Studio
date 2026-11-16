package store

import (
	"context"
	"sort"
	"strings"

	"arkstudio/go/internal/platform/clock"

	"arkstudio/go/internal/platform/pyerr"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/sqlitedb"
)

// Filters is the filter dict of Store._where.  List values are Python
// iterables already expanded (see Iter); scalar values keep their decoded
// type because Python binds them to SQL unchanged.
type Filters struct {
	Q                                                     any
	Types, Origins, Statuses, Importance, Tags, TagsUnset []any
	Assignees, IDs                                        []any
	TypesNone, Roots, Slim, Overdue, HasDates             bool
	StartFrom, StartTo, UpdatedSince, CreatedSince        any
	Sort                                                  string
	Me                                                    string
	Limit, Offset                                         int64
}

// Iter is Python iteration over a decoded JSON value used as `f.get(k) or []`.
func Iter(v any) ([]any, error) {
	if !pyjson.Truthy(v) {
		return nil, nil
	}
	switch x := v.(type) {
	case []any:
		return x, nil
	case string:
		out := []any{}
		for _, r := range x {
			out = append(out, string(r))
		}
		return out, nil
	case *pyjson.Object:
		out := []any{}
		for _, k := range x.Keys() {
			out = append(out, k)
		}
		return out, nil
	}
	return nil, &pyerr.ValueError{Message: "'" + pyTypeName(v) + "' object is not iterable"}
}

func pyTypeName(v any) string {
	switch v.(type) {
	case bool:
		return "bool"
	case int64:
		return "int"
	case float64:
		return "float"
	}
	return "object"
}

// FiltersFromObject builds Filters from a topic rule ({**rule, "me": ""}).
func FiltersFromObject(o *pyjson.Object) (Filters, error) {
	var f Filters
	var err error
	lists := []struct {
		key string
		dst *[]any
	}{{"types", &f.Types}, {"origins", &f.Origins}, {"statuses", &f.Statuses}, {"importance", &f.Importance},
		{"tags", &f.Tags}, {"tags_unset", &f.TagsUnset}, {"assignees", &f.Assignees}, {"ids", &f.IDs}}
	for _, l := range lists {
		if *l.dst, err = Iter(o.Value(l.key)); err != nil {
			return f, err
		}
	}
	f.Q = o.Value("q")
	f.TypesNone = pyjson.Truthy(o.Value("types_none"))
	f.Roots = pyjson.Truthy(o.Value("roots"))
	f.Overdue = pyjson.Truthy(o.Value("overdue"))
	f.HasDates = pyjson.Truthy(o.Value("has_dates"))
	f.StartFrom, f.StartTo = o.Value("start_from"), o.Value("start_to")
	f.UpdatedSince, f.CreatedSince = o.Value("updated_since"), o.Value("created_since")
	return f, nil
}

// TagNamespace is tags.tag_namespace.
func TagNamespace(tag string) string {
	if ns, _, ok := strings.Cut(tag, ":"); ok {
		return ns
	}
	return "tag"
}

// TagValue is tags.tag_value.
func TagValue(tag string) string {
	if _, v, ok := strings.Cut(tag, ":"); ok {
		return v
	}
	return tag
}

func truthyItems(items []any) []any {
	var out []any
	for _, v := range items {
		if pyjson.Truthy(v) {
			out = append(out, v)
		}
	}
	return out
}

func repeatMarks(n int) string { return sqlitedb.Marks(n) }

// where is Store._where.
func (s *Store) where(ctx context.Context, q sqlitedb.Querier, f Filters) (string, []any, error) {
	clauses := []string{"u.archived=0"}
	var args []any
	if f.TypesNone {
		clauses = append(clauses, "0")
	}
	if text := pytext.Strip(strOrEmpty(f.Q)); text != "" {
		like := "%" + text + "%"
		clauses = append(clauses, "(u.title LIKE ? OR u.body LIKE ? OR u.uid LIKE ?)")
		args = append(args, like, like, like)
	}
	types, origins := truthyItems(f.Types), truthyItems(f.Origins)
	switch {
	case len(types) > 0 && len(origins) > 0:
		clauses = append(clauses, "(u.type IN ("+repeatMarks(len(types))+") OR u.origin IN ("+repeatMarks(len(origins))+"))")
		args = append(append(args, types...), origins...)
	case len(types) > 0:
		clauses = append(clauses, "u.type IN ("+repeatMarks(len(types))+")")
		args = append(args, types...)
	case len(origins) > 0:
		clauses = append(clauses, "u.origin IN ("+repeatMarks(len(origins))+")")
		args = append(args, origins...)
	}
	if st := truthyItems(f.Statuses); len(st) > 0 {
		clauses = append(clauses, "u.status IN ("+repeatMarks(len(st))+")")
		args = append(args, st...)
	}
	if im := truthyItems(f.Importance); len(im) > 0 {
		clauses = append(clauses, "u.importance IN ("+repeatMarks(len(im))+")")
		args = append(args, im...)
	}
	tags := truthyItems(f.Tags)
	var unset []string
	for _, ns := range f.TagsUnset {
		if t := pyjson.Str(ns); t != "" {
			unset = append(unset, t)
		}
	}
	if len(tags) > 0 || len(unset) > 0 {
		groups := map[string][]any{}
		var nsOrder []string
		seenTag := map[string]bool{}
		for _, t := range tags {
			tag := pyjson.Str(t)
			if seenTag[tag] {
				continue
			}
			seenTag[tag] = true
			ns := TagNamespace(tag)
			if _, ok := groups[ns]; !ok {
				nsOrder = append(nsOrder, ns)
			}
			groups[ns] = append(groups[ns], t)
		}
		unsetSet := map[string]bool{}
		for _, ns := range unset {
			unsetSet[ns] = true
		}
		seenNS := map[string]bool{}
		for _, ns := range append(append([]string{}, nsOrder...), unset...) {
			if seenNS[ns] {
				continue
			}
			seenNS[ns] = true
			var parts []string
			values := groups[ns]
			if len(values) > 0 {
				parts = append(parts, "u.id IN (SELECT unit_id FROM unit_tags WHERE tag IN ("+repeatMarks(len(values))+"))")
			}
			if unsetSet[ns] {
				parts = append(parts, "NOT EXISTS (SELECT 1 FROM unit_tags t WHERE t.unit_id=u.id AND substr(t.tag, 1, ?)=?)")
			}
			if len(parts) == 0 {
				continue
			}
			clause := parts[0]
			if len(parts) > 1 {
				clause = "(" + strings.Join(parts, " OR ") + ")"
			}
			clauses = append(clauses, clause)
			args = append(args, values...)
			if unsetSet[ns] {
				args = append(args, int64(pytext.Len(ns)+1), ns+":")
			}
		}
	}
	if f.Roots {
		clauses = append(clauses, "u.id NOT IN (SELECT source_id FROM unit_links WHERE relation='part_of')")
	}
	if len(f.IDs) > 0 {
		var idArgs []any
		for _, v := range f.IDs {
			n, err := PyInt(v)
			if err != nil {
				return "", nil, err
			}
			idArgs = append(idArgs, n)
		}
		clauses = append(clauses, "u.id IN ("+repeatMarks(len(idArgs))+")")
		args = append(args, idArgs...)
	}
	from, to := f.StartFrom, f.StartTo
	switch {
	case pyjson.Truthy(from) && pyjson.Truthy(to):
		clauses = append(clauses, "((u.start_at IS NOT NULL AND u.start_at BETWEEN ? AND ?) OR (u.end_at IS NOT NULL AND u.end_at BETWEEN ? AND ?))")
		args = append(args, from, to, from, to)
	case pyjson.Truthy(from):
		clauses = append(clauses, "((u.start_at IS NOT NULL AND u.start_at >= ?) OR (u.end_at IS NOT NULL AND u.end_at >= ?))")
		args = append(args, from, from)
	case pyjson.Truthy(to):
		clauses = append(clauses, "((u.start_at IS NOT NULL AND u.start_at <= ?) OR (u.end_at IS NOT NULL AND u.end_at <= ?))")
		args = append(args, to, to)
	}
	if f.Overdue {
		clauses = append(clauses, "(u.end_at IS NOT NULL AND u.end_at < ? AND u.status NOT IN ('done','abandoned','archived'))")
		args = append(args, clock.Today())
	}
	if f.HasDates {
		clauses = append(clauses, "(u.start_at IS NOT NULL OR u.end_at IS NOT NULL)")
	}
	if pyjson.Truthy(f.UpdatedSince) {
		clauses = append(clauses, "u.updated_at >= ?")
		args = append(args, pyjson.Str(f.UpdatedSince))
	}
	if pyjson.Truthy(f.CreatedSince) {
		clauses = append(clauses, "u.created_at >= ?")
		args = append(args, pyjson.Str(f.CreatedSince))
	}
	matched, active, err := s.assigneeFilterIDs(ctx, q, f)
	if err != nil {
		return "", nil, err
	}
	if active {
		clauses = append(clauses, "u.id IN (SELECT value FROM json_each(?))")
		args = append(args, string(pyjson.Dumps(matched)))
	}
	return strings.Join(clauses, " AND "), args, nil
}

func strOrEmpty(v any) string {
	if !pyjson.Truthy(v) {
		return ""
	}
	return pyjson.Str(v)
}

// PyInt is int(value) for a decoded JSON value.
func PyInt(v any) (int64, error) {
	switch x := v.(type) {
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	case int64:
		return x, nil
	case float64:
		return int64(x), nil
	case string:
		n, err := pytext.Int(x)
		if err != nil {
			return 0, &pyerr.ValueError{Message: err.Error()}
		}
		return n, nil
	}
	return 0, &pyerr.ValueError{Message: "int() argument must be a string, a bytes-like object or a real number, not '" + pyTypeName(v) + "'"}
}

// assigneeFilterIDs is Store._assignee_filter_ids.
func (s *Store) assigneeFilterIDs(ctx context.Context, q sqlitedb.Querier, f Filters) ([]int64, bool, error) {
	var wanted []string
	for _, a := range f.Assignees {
		if t := pytext.Strip(pyjson.Str(a)); t != "" {
			wanted = append(wanted, t)
		}
	}
	if len(wanted) == 0 {
		return nil, false, nil
	}
	wantNone := false
	names := map[string]bool{}
	for _, v := range wanted {
		low := strings.ToLower(v)
		switch {
		case low == "none" || low == "unassigned":
			wantNone = true
		case low == "me":
			if me := strings.ToLower(pytext.Strip(f.Me)); me != "" {
				names[me] = true
			}
		default:
			names[low] = true
		}
	}
	c, err := s.assigneeIndexCache(ctx, q)
	if err != nil {
		return nil, false, err
	}
	out := []int64{}
	for _, id := range c.order {
		e := c.index[id]
		if (!e.hasName && wantNone) || (e.hasName && names[strings.ToLower(e.name)]) {
			out = append(out, id)
		}
	}
	return out, true, nil
}

// order is Store._order.
func order(sortKey string) string {
	switch sortKey {
	case "created":
		return "u.created_at DESC, u.id DESC"
	case "title":
		return "u.title COLLATE NOCASE ASC"
	case "start":
		return "CASE WHEN u.start_at IS NULL THEN 1 ELSE 0 END, u.start_at ASC, u.end_at ASC"
	case "end":
		return "CASE WHEN u.end_at IS NULL THEN 1 ELSE 0 END, u.end_at ASC, u.start_at ASC"
	case "importance":
		return "CASE u.importance WHEN 'p0' THEN 0 WHEN 'p1' THEN 1 WHEN 'p2' THEN 2 ELSE 3 END, u.updated_at DESC"
	case "type":
		return "u.type ASC, u.title COLLATE NOCASE ASC"
	}
	return "u.updated_at DESC, u.id DESC"
}

func clamp(n, lo, hi int64) int64 { return max(lo, min(hi, n)) }

// ListUnits is Store.list_units.
func (s *Store) ListUnits(ctx context.Context, f Filters, includeFacets bool) (*pyjson.Object, error) {
	var out *pyjson.Object
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		var err error
		out, err = s.listUnits(ctx, q, f, includeFacets)
		return err
	})
	return out, err
}

func (s *Store) listUnits(ctx context.Context, q sqlitedb.Querier, f Filters, includeFacets bool) (*pyjson.Object, error) {
	where, args, err := s.where(ctx, q, f)
	if err != nil {
		return nil, err
	}
	totalRow, _, err := sqlitedb.QueryOne(ctx, q, "SELECT COUNT(*) n FROM units u WHERE "+where, args...)
	if err != nil {
		return nil, err
	}
	limit := f.Limit
	if limit == 0 {
		limit = 200
	}
	limit = clamp(limit, 1, 1000)
	offset := max(0, f.Offset)
	rows, err := sqlitedb.Query(ctx, q, "SELECT u.* FROM units u WHERE "+where+" ORDER BY "+order(f.Sort)+" LIMIT ? OFFSET ?",
		append(append([]any{}, args...), limit, offset)...)
	if err != nil {
		return nil, err
	}
	items, err := s.hydrate(ctx, q, rows, true)
	if err != nil {
		return nil, err
	}
	if f.Slim {
		for _, item := range items {
			body := strOrEmpty(item.Delete("body"))
			item.Set("excerpt", pytext.Excerpt(body, 240))
			item.Set("body_truncated", pytext.Len(body) > 240)
		}
	}
	out := pyjson.NewObject("items", items, "total", totalRow.Get("n"), "limit", limit, "offset", offset)
	if includeFacets {
		facets, err := s.facets(ctx, q, f)
		if err != nil {
			return nil, err
		}
		out.Set("facets", facets)
	}
	return out, nil
}

var reservedNS = map[string]bool{"type": true, "status": true, "importance": true}

// facets is Store.facets.
func (s *Store) facets(ctx context.Context, q sqlitedb.Querier, base Filters) (*pyjson.Object, error) {
	countMap := func(query string, args []any, key string) (*pyjson.Object, error) {
		rows, err := sqlitedb.Query(ctx, q, query, args...)
		o := pyjson.NewObject()
		for _, r := range rows {
			o.Set(pyjson.Str(r.Get(key)), r.Get("n"))
		}
		return o, err
	}
	ft := base
	ft.Types, ft.TypesNone = nil, false
	typeWhere, typeArgs, err := s.where(ctx, q, ft)
	if err != nil {
		return nil, err
	}
	fs := base
	fs.Statuses = nil
	statusWhere, statusArgs, err := s.where(ctx, q, fs)
	if err != nil {
		return nil, err
	}
	fi := base
	fi.Importance = nil
	impWhere, impArgs, err := s.where(ctx, q, fi)
	if err != nil {
		return nil, err
	}
	fg := base
	fg.Tags, fg.TagsUnset = nil, nil
	tagWhere, tagArgs, err := s.where(ctx, q, fg)
	if err != nil {
		return nil, err
	}
	types, err := countMap("SELECT u.type, COUNT(*) n FROM units u WHERE "+typeWhere+" GROUP BY u.type ORDER BY n DESC", typeArgs, "type")
	if err != nil {
		return nil, err
	}
	statuses, err := countMap("SELECT u.status, COUNT(*) n FROM units u WHERE "+statusWhere+" GROUP BY u.status", statusArgs, "status")
	if err != nil {
		return nil, err
	}
	importance, err := countMap("SELECT u.importance, COUNT(*) n FROM units u WHERE "+impWhere+" GROUP BY u.importance", impArgs, "importance")
	if err != nil {
		return nil, err
	}
	tagRows, err := sqlitedb.Query(ctx, q, `SELECT t.tag, COUNT(DISTINCT t.unit_id) n
                    FROM unit_tags t JOIN units u ON u.id=t.unit_id
                    WHERE `+tagWhere+` GROUP BY t.tag ORDER BY n DESC, t.tag ASC`, tagArgs...)
	if err != nil {
		return nil, err
	}
	type tagCount struct {
		tag string
		n   int64
	}
	var tagCounts []tagCount
	seen := map[string]bool{}
	for _, r := range tagRows {
		tag := r.Str("tag")
		if reservedNS[TagNamespace(tag)] {
			continue
		}
		if seen[tag] {
			continue
		}
		seen[tag] = true
		tagCounts = append(tagCounts, tagCount{tag, r.Int("n")})
	}
	type nsItem struct {
		obj   *pyjson.Object
		count int64
		value string
	}
	namespaces := map[string][]nsItem{}
	var nsNames []string
	for _, tc := range tagCounts {
		ns := TagNamespace(tc.tag)
		if _, ok := namespaces[ns]; !ok {
			nsNames = append(nsNames, ns)
		}
		namespaces[ns] = append(namespaces[ns], nsItem{pyjson.NewObject("tag", tc.tag, "namespace", ns, "value", TagValue(tc.tag), "count", tc.n), tc.n, TagValue(tc.tag)})
	}
	nsObj := pyjson.NewObject()
	sorted := append([]string(nil), nsNames...)
	sort.Strings(sorted)
	for _, ns := range sorted {
		items := namespaces[ns]
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].count != items[j].count {
				return items[i].count > items[j].count
			}
			return strings.ToLower(items[i].value) < strings.ToLower(items[j].value)
		})
		list := make([]any, len(items))
		for i, it := range items {
			list[i] = it.obj
		}
		nsObj.Set(ns, list)
	}
	// Python runs one COUNT(*) ... NOT EXISTS(substr(tag,1,len(ns)+1)=ns||':')
	// per namespace (a full scan each).  One pass gives the same numbers: a
	// unit "has" ns exactly when one of its tags starts with "ns:", and a
	// namespace never contains ':' itself.
	unsetObj := pyjson.NewObject()
	if len(nsNames) > 0 {
		pairs, err := sqlitedb.Query(ctx, q, "SELECT u.id, ut.tag FROM units u LEFT JOIN unit_tags ut ON ut.unit_id=u.id WHERE "+tagWhere, tagArgs...)
		if err != nil {
			return nil, err
		}
		has := map[string]map[int64]bool{}
		unitsSeen := map[int64]bool{}
		for _, r := range pairs {
			id := r.Int("id")
			unitsSeen[id] = true
			if tag, ok := r.Get("tag").(string); ok {
				if ns, _, found := strings.Cut(tag, ":"); found {
					if has[ns] == nil {
						has[ns] = map[int64]bool{}
					}
					has[ns][id] = true
				}
			}
		}
		for _, ns := range nsNames {
			unsetObj.Set(ns, int64(len(unitsSeen)-len(has[ns])))
		}
	}
	fa := base
	fa.Assignees = nil
	whereA, argsA, err := s.where(ctx, q, fa)
	if err != nil {
		return nil, err
	}
	index, err := s.assigneeIndex(ctx, q)
	if err != nil {
		return nil, err
	}
	idRows, err := sqlitedb.Query(ctx, q, "SELECT u.id FROM units u WHERE "+whereA, argsA...)
	if err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	var names []string
	unassigned := int64(0)
	for _, r := range idRows {
		e := index[r.Int("id")]
		if e.hasName && e.name != "" {
			if _, ok := counts[e.name]; !ok {
				names = append(names, e.name)
			}
			counts[e.name]++
		} else {
			unassigned++
		}
	}
	sort.SliceStable(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	assignees := pyjson.NewObject()
	for _, n := range names {
		assignees.Set(n, counts[n])
	}
	span, _, err := sqlitedb.QueryOne(ctx, q, `SELECT MIN(COALESCE(start_at, end_at)) AS min_date,
                      MAX(COALESCE(end_at, start_at)) AS max_date,
                      SUM(CASE WHEN start_at IS NULL AND end_at IS NULL THEN 1 ELSE 0 END) AS undated
                 FROM units WHERE archived=0`)
	if err != nil {
		return nil, err
	}
	tagList := make([]any, len(tagCounts))
	for i, tc := range tagCounts {
		tagList[i] = pyjson.NewObject("tag", tc.tag, "count", tc.n)
	}
	return pyjson.NewObject(
		"types", types, "statuses", statuses, "importance", importance, "tags", tagList,
		"namespaces", nsObj, "unset", unsetObj, "assignees", assignees, "unassigned", unassigned,
		"time", pyjson.NewObject("min", span.Get("min_date"), "max", span.Get("max_date"), "undated", span.Int("undated")),
	), nil
}
