package store

import (
	"context"
	"sort"

	"arkstudio/go/internal/platform/pyerr"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/sqlitedb"
)

// Port of the topic read path of store.py: _subgraph, _rule_ids,
// _resolve_members, _member_tree, _topic_edges, topic(), topics(),
// topics_of(), _member_counts().

// memberRef is one resolved member: (unit_id, row|None, "manual"|"rule").
type memberRef struct {
	id     int64
	row    *sqlitedb.Row
	source string
}

type subgraph struct {
	rule      *pyjson.Object // nil = None
	relations []any          // nil = None
	hidden    []int64
}

func (sg subgraph) object() *pyjson.Object {
	var rule, relations any
	if sg.rule != nil {
		rule = sg.rule
	}
	if sg.relations != nil {
		relations = sg.relations
	}
	hidden := sg.hidden
	if hidden == nil {
		hidden = []int64{}
	}
	return pyjson.NewObject("rule", rule, "relations", relations, "hidden_links", hidden)
}

var relationSet = func() map[string]bool {
	out := map[string]bool{}
	for _, r := range Relations {
		out[r] = true
	}
	return out
}()

// topicSubgraph is Store._subgraph.
func topicSubgraph(topic sqlitedb.Row) (subgraph, error) {
	var sg subgraph
	raw, _ := parseDetail(topic.Get("meta_json")).Value("subgraph").(*pyjson.Object)
	if raw == nil {
		raw = pyjson.NewObject()
	}
	sg.rule, _ = raw.Value("rule").(*pyjson.Object)
	if list, ok := raw.Value("relations").([]any); ok {
		sg.relations = []any{}
		for _, r := range list {
			if s, ok := r.(string); ok && relationSet[s] {
				sg.relations = append(sg.relations, s)
			}
		}
	}
	items, err := Iter(raw.Value("hidden_links"))
	if err != nil {
		return sg, err
	}
	seen := map[int64]bool{}
	for _, item := range items {
		if !pytext.IsDigit(pyjson.Str(item)) {
			continue
		}
		n, err := PyInt(item)
		if err != nil {
			return sg, err
		}
		if !seen[n] {
			seen[n] = true
			sg.hidden = append(sg.hidden, n)
		}
	}
	sort.Slice(sg.hidden, func(i, j int) bool { return sg.hidden[i] < sg.hidden[j] })
	return sg, nil
}

// ruleIDs is Store._rule_ids.
func (s *Store) ruleIDs(ctx context.Context, q sqlitedb.Querier, rule *pyjson.Object, exclude int64) ([]int64, error) {
	if !pyjson.Truthy(rule) {
		return nil, nil
	}
	f, err := FiltersFromObject(rule)
	if err != nil {
		return nil, err
	}
	f.Me = ""
	where, args, err := s.where(ctx, q, f)
	if err != nil {
		return nil, err
	}
	rows, err := sqlitedb.Query(ctx, q, "SELECT u.id FROM units u WHERE "+where+" AND u.id<>? ORDER BY u.created_at ASC, u.id ASC LIMIT ?",
		append(append([]any{}, args...), exclude, int64(maxTopicMembers))...)
	if err != nil {
		return nil, err
	}
	out := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = r.Int("id")
	}
	seq := map[int64]int64{}
	for off := 0; off < len(out); off += 400 {
		batch := sqlitedb.Args(out[off:min(off+400, len(out))])
		tags, err := groupRows(ctx, q, "SELECT unit_id, tag FROM unit_tags WHERE unit_id IN ({marks}) AND tag LIKE 'seq:%'", batch)
		if err != nil {
			return nil, err
		}
		for _, t := range tags {
			value := pytext.SliceFrom(t.Str("tag"), 4)
			if pytext.IsDigit(value) {
				n, err := pytext.Int(value)
				if err != nil {
					return nil, &pyerr.ValueError{Message: err.Error()}
				}
				if old, ok := seq[t.Int("unit_id")]; !ok || n < old {
					seq[t.Int("unit_id")] = n
				}
			}
		}
	}
	index := map[int64]int{}
	for i, id := range out {
		index[id] = i
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		sa, oka := seq[a]
		sb, okb := seq[b]
		if oka != okb {
			return oka
		}
		if sa != sb {
			return sa < sb
		}
		return index[a] < index[b]
	})
	return out, nil
}

// resolveMembers is Store._resolve_members: members in narrative order and
// the excluded ids.
func (s *Store) resolveMembers(ctx context.Context, q sqlitedb.Querier, topic sqlitedb.Row) ([]memberRef, []int64, error) {
	sg, err := topicSubgraph(topic)
	if err != nil {
		return nil, nil, err
	}
	rows, err := sqlitedb.Query(ctx, q, `SELECT tm.* FROM topic_members tm JOIN units u ON u.id=tm.unit_id
               WHERE tm.topic_id=? AND u.archived=0
               ORDER BY tm.position, tm.added_at, tm.unit_id`, topic.Int("id"))
	if err != nil {
		return nil, nil, err
	}
	var excluded []int64
	excludedSet, hasRow := map[int64]bool{}, map[int64]bool{}
	for _, r := range rows {
		if r.Str("mode") == "exclude" {
			excluded = append(excluded, r.Int("unit_id"))
			excludedSet[r.Int("unit_id")] = true
		}
		hasRow[r.Int("unit_id")] = true
	}
	ruleIDs, err := s.ruleIDs(ctx, q, sg.rule, topic.Int("id"))
	if err != nil {
		return nil, nil, err
	}
	var matched []int64
	matchedSet := map[int64]bool{}
	for _, id := range ruleIDs {
		if !excludedSet[id] {
			matched = append(matched, id)
			matchedSet[id] = true
		}
	}
	var members []memberRef
	for i := range rows {
		r := rows[i]
		switch r.Str("mode") {
		case "include":
			members = append(members, memberRef{r.Int("unit_id"), &rows[i], "manual"})
		case "layout":
			if matchedSet[r.Int("unit_id")] {
				members = append(members, memberRef{r.Int("unit_id"), &rows[i], "rule"})
			}
		}
	}
	for _, id := range matched {
		if !hasRow[id] {
			members = append(members, memberRef{id, nil, "rule"})
		}
	}
	return members, excluded, nil
}

// memberCounts is Store._member_counts.
func (s *Store) memberCounts(ctx context.Context, q sqlitedb.Querier, topicIDs []int64) (map[int64]int64, error) {
	out := map[int64]int64{}
	for _, id := range topicIDs {
		row, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT * FROM units WHERE id=?", id)
		if err != nil {
			return nil, err
		}
		if ok {
			members, _, err := s.resolveMembers(ctx, q, row)
			if err != nil {
				return nil, err
			}
			out[id] = int64(len(members))
		}
	}
	return out, nil
}

// topicsOf is Store.topics_of.
func (s *Store) topicsOf(ctx context.Context, q sqlitedb.Querier, unitID int64) ([]any, error) {
	rows, err := sqlitedb.Query(ctx, q, "SELECT * FROM units WHERE archived=0 AND type='topic' AND id<>? ORDER BY title", unitID)
	if err != nil {
		return nil, err
	}
	out := []any{}
	for _, r := range rows {
		members, _, err := s.resolveMembers(ctx, q, r)
		if err != nil {
			return nil, err
		}
		for _, m := range members {
			if m.id == unitID {
				out = append(out, pyjson.NewObject("id", r.Get("id"), "uid", r.Get("uid"), "title", r.Get("title"), "type", r.Get("type")))
				break
			}
		}
	}
	return out, nil
}

type treeItem struct {
	id           int64
	parent       any // int64 or nil
	parentSource any // "topic" | "link" | nil
	depth        int64
	path         string
	children     int64
}

// memberTree is Store._member_tree.
func memberTree(ctx context.Context, q sqlitedb.Querier, resolved []memberRef) ([]treeItem, error) {
	order := map[int64]int{}
	var ids []int64
	for n, m := range resolved {
		if _, ok := order[m.id]; !ok {
			order[m.id] = n
			ids = append(ids, m.id)
		} else {
			order[m.id] = n // dict comprehension: the last position wins
		}
	}
	// Python: order = {m[0]: n for n, m in enumerate(resolved)}; the dict
	// keeps the first insertion position but the last value.
	ids = nil
	seenID := map[int64]bool{}
	for _, m := range resolved {
		if !seenID[m.id] {
			seenID[m.id] = true
			ids = append(ids, m.id)
		}
	}
	auto := map[int64]int64{}
	if len(ids) > 0 {
		all := sqlitedb.Args(ids)
		for off := 0; off < len(ids); off += 400 {
			batch := sqlitedb.Args(ids[off:min(off+400, len(ids))])
			rows, err := sqlitedb.Query(ctx, q, "SELECT source_id, target_id FROM unit_links WHERE relation='part_of'\n"+
				"                            AND source_id IN ("+sqlitedb.Marks(len(batch))+") AND target_id IN ("+sqlitedb.Marks(len(all))+") ORDER BY id",
				append(append([]any{}, batch...), all...)...)
			if err != nil {
				return nil, err
			}
			for _, r := range rows {
				if _, ok := auto[r.Int("source_id")]; !ok {
					auto[r.Int("source_id")] = r.Int("target_id")
				}
			}
		}
	}
	parent := map[int64]any{}
	source := map[int64]any{}
	for _, m := range resolved {
		var explicit any
		if m.row != nil {
			explicit = m.row.Get("parent_id")
		}
		ex, isInt := explicit.(int64)
		_, exInOrder := order[ex]
		a, hasAuto := auto[m.id]
		_, autoInOrder := order[a]
		switch {
		case isInt && ex == 0:
			parent[m.id], source[m.id] = nil, "topic"
		case isInt && exInOrder && ex != m.id:
			parent[m.id], source[m.id] = ex, "topic"
		case hasAuto && autoInOrder && a != m.id:
			parent[m.id], source[m.id] = a, "link"
		default:
			parent[m.id], source[m.id] = nil, nil
		}
	}
	for _, id := range ids {
		seen := map[int64]bool{id: true}
		cursor := parent[id]
		for cursor != nil {
			c := cursor.(int64)
			if seen[c] {
				parent[id], source[id] = nil, nil
				break
			}
			seen[c] = true
			cursor = parent[c]
		}
	}
	sorted := append([]int64(nil), ids...)
	sort.SliceStable(sorted, func(i, j int) bool { return order[sorted[i]] < order[sorted[j]] })
	children := map[any][]int64{}
	for _, id := range sorted {
		key := parent[id]
		children[key] = append(children[key], id)
	}
	var out []treeItem
	var walk func(node any, depth int64, prefix string)
	walk = func(node any, depth int64, prefix string) {
		for n, child := range children[node] {
			path := prefix + itoa(n+1)
			out = append(out, treeItem{id: child, parent: parent[child], parentSource: source[child],
				depth: depth, path: path, children: int64(len(children[child]))})
			walk(child, depth+1, path+".")
		}
	}
	walk(nil, 0, "")
	return out, nil
}

func itoa(n int) string { return pyjson.Str(int64(n)) }

// topicEdges is Store._topic_edges.
func topicEdges(ctx context.Context, q sqlitedb.Querier, ids []int64, sg subgraph) ([]*pyjson.Object, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	hidden := map[int64]bool{}
	for _, h := range sg.hidden {
		hidden[h] = true
	}
	all := sqlitedb.Args(ids)
	rows, err := sqlitedb.Query(ctx, q, `SELECT id, source_id AS source, target_id AS target, relation, note FROM unit_links
                    WHERE source_id IN (`+sqlitedb.Marks(len(all))+`) AND target_id IN (`+sqlitedb.Marks(len(all))+`) ORDER BY id`,
		append(append([]any{}, all...), all...)...)
	if err != nil {
		return nil, err
	}
	out := make([]*pyjson.Object, len(rows))
	for i, r := range rows {
		o := rowObject(r)
		isHidden := hidden[r.Int("id")]
		if !isHidden && sg.relations != nil {
			isHidden = !containsAny(sg.relations, r.Str("relation"))
		}
		o.Set("hidden", isHidden)
		out[i] = o
	}
	return out, nil
}

func containsAny(items []any, want string) bool {
	for _, v := range items {
		if v == want {
			return true
		}
	}
	return false
}

// rowObject is dict(row).
func rowObject(r sqlitedb.Row) *pyjson.Object {
	o := pyjson.NewObject()
	for i, c := range r.Cols {
		o.Set(c, r.Vals[i])
	}
	return o
}

// RowObject is dict(row) for other packages.
func RowObject(r sqlitedb.Row) *pyjson.Object { return rowObject(r) }

// Topic is Store.topic(ref, full=...).
func (s *Store) Topic(ctx context.Context, ref string, full bool) (*pyjson.Object, error) {
	var out *pyjson.Object
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		row, err := unitRow(ctx, q, ref)
		if err != nil {
			return err
		}
		if row.Str("type") != "topic" {
			return storeErr("%s is not a topic", row.Str("uid"))
		}
		hydrated, err := s.hydrate(ctx, q, []sqlitedb.Row{row}, true)
		if err != nil {
			return err
		}
		out, err = s.topicPayload(ctx, q, row, hydrated[0], full)
		return err
	})
	return out, err
}

// topicPayload is the body of Topic for the topic row (its id and
// meta_json are read) and the topic object to send (Go only: BranchTopic
// passes a row that is not stored).
func (s *Store) topicPayload(ctx context.Context, q sqlitedb.Querier, row sqlitedb.Row, topic *pyjson.Object, full bool) (*pyjson.Object, error) {
	var out *pyjson.Object
	err := func() error {
		sg, err := topicSubgraph(row)
		if err != nil {
			return err
		}
		resolved, excluded, err := s.resolveMembers(ctx, q, row)
		if err != nil {
			return err
		}
		var ids []int64
		for _, m := range resolved {
			ids = append(ids, m.id)
		}
		units := map[int64]*pyjson.Object{}
		for off := 0; off < len(ids); off += 400 {
			batch := sqlitedb.Args(ids[off:min(off+400, len(ids))])
			rows, err := groupRows(ctx, q, "SELECT * FROM units WHERE id IN ({marks})", batch)
			if err != nil {
				return err
			}
			items, err := s.hydrate(ctx, q, rows, full)
			if err != nil {
				return err
			}
			for _, u := range items {
				units[u.Value("id").(int64)] = u
			}
		}
		byID := map[int64]memberRef{}
		for _, m := range resolved {
			byID[m.id] = m // dict comprehension: last one wins
		}
		tree, err := memberTree(ctx, q, resolved)
		if err != nil {
			return err
		}
		members := []*pyjson.Object{}
		for step, item := range tree {
			m := byID[item.id]
			unit := units[item.id]
			var x, y, w any
			caption := any("")
			if m.row != nil {
				x, y, w, caption = m.row.Get("x"), m.row.Get("y"), m.row.Get("w"), m.row.Get("caption")
			}
			unit.Set("member", pyjson.NewObject("step", int64(step+1), "path", item.path, "depth", item.depth,
				"parent", item.parent, "parent_source", item.parentSource, "children", item.children,
				"source", m.source, "x", x, "y", y, "w", w, "caption", caption))
			members = append(members, unit)
		}
		memberIDs := make([]int64, len(members))
		for i, u := range members {
			memberIDs[i] = u.Value("id").(int64)
		}
		edges, err := topicEdges(ctx, q, memberIDs, sg)
		if err != nil {
			return err
		}
		degree := map[int64]int64{}
		for _, e := range edges {
			if e.Value("hidden") == false {
				degree[e.Value("source").(int64)]++
				degree[e.Value("target").(int64)]++
			}
		}
		for _, u := range members {
			u.Set("degree", degree[u.Value("id").(int64)])
		}
		excludedUnits := []any{}
		if len(excluded) > 0 {
			rows, err := groupRows(ctx, q, "SELECT * FROM units WHERE id IN ({marks}) ORDER BY title", sqlitedb.Args(excluded))
			if err != nil {
				return err
			}
			for _, r := range rows {
				excludedUnits = append(excludedUnits, unitBrief(r))
			}
		}
		archived, _, err := sqlitedb.QueryOne(ctx, q, `SELECT COUNT(*) n FROM topic_members tm JOIN units u ON u.id=tm.unit_id
                   WHERE tm.topic_id=? AND tm.mode='include' AND u.archived=1`, row.Int("id"))
		if err != nil {
			return err
		}
		if edges == nil {
			edges = []*pyjson.Object{}
		}
		out = pyjson.NewObject("topic", topic, "members", members, "edges", edges, "excluded", excludedUnits,
			"archived", archived.Get("n"), "subgraph", sg.object())
		return nil
	}()
	return out, err
}

// Topics is Store.topics().
func (s *Store) Topics(ctx context.Context) (*pyjson.Object, error) {
	var out *pyjson.Object
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		rows, err := sqlitedb.Query(ctx, q, "SELECT * FROM units WHERE archived=0 AND type='topic' ORDER BY updated_at DESC, id DESC")
		if err != nil {
			return err
		}
		items, err := s.hydrate(ctx, q, rows, false)
		if err != nil {
			return err
		}
		rank := map[string]int{}
		for n, t := range DefaultTopics {
			rank[t[0]] = n
		}
		defaultOf := func(item *pyjson.Object) (string, bool) {
			meta, _ := item.Value("meta").(*pyjson.Object)
			key, ok := meta.Value("default").(string)
			if !ok {
				return "", false
			}
			_, known := rank[key]
			return key, known
		}
		idx := make([]int, len(rows))
		for i := range idx {
			idx[i] = i
		}
		rankOf := func(i int) int {
			if key, ok := defaultOf(items[i]); ok {
				return rank[key]
			}
			return 99
		}
		sort.SliceStable(idx, func(a, b int) bool { return rankOf(idx[a]) < rankOf(idx[b]) })
		ordered := make([]*pyjson.Object, len(idx))
		for n, i := range idx {
			item, row := items[i], rows[i]
			ordered[n] = item
			if key, ok := defaultOf(item); ok {
				item.Set("default", key)
			} else {
				item.Set("default", nil)
			}
			sg, err := topicSubgraph(row)
			if err != nil {
				return err
			}
			resolved, _, err := s.resolveMembers(ctx, q, row)
			if err != nil {
				return err
			}
			if len(resolved) > 60 {
				resolved = resolved[:60]
			}
			ids := make([]int64, len(resolved))
			for k, m := range resolved {
				ids[k] = m.id
			}
			briefs := map[int64]sqlitedb.Row{}
			if len(ids) > 0 {
				brows, err := groupRows(ctx, q, "SELECT id, uid, title, type FROM units WHERE id IN ({marks})", sqlitedb.Args(ids))
				if err != nil {
					return err
				}
				for _, b := range brows {
					briefs[b.Int("id")] = b
				}
			}
			members := []any{}
			for _, m := range resolved {
				b, ok := briefs[m.id]
				if !ok {
					continue
				}
				var x, y any
				if m.row != nil {
					x, y = m.row.Get("x"), m.row.Get("y")
				}
				members = append(members, pyjson.NewObject("id", m.id, "uid", b.Get("uid"), "title", b.Get("title"),
					"type", b.Get("type"), "x", x, "y", y))
			}
			edges, err := topicEdges(ctx, q, ids, sg)
			if err != nil {
				return err
			}
			pairs := []any{}
			for _, e := range edges {
				if e.Value("hidden") == false && len(pairs) < 200 {
					pairs = append(pairs, []any{e.Value("source"), e.Value("target")})
				}
			}
			item.Set("preview", pyjson.NewObject("members", members, "edges", pairs, "rule", sg.rule != nil))
		}
		out = pyjson.NewObject("items", ordered, "total", int64(len(ordered)))
		return nil
	})
	return out, err
}

// DefaultTopicsDone reports the meta flag ensure_default_topics sets.
func (s *Store) DefaultTopicsDone(ctx context.Context) (bool, error) {
	var done bool
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		v, _, err := MetaGet(ctx, q, DefaultTopicsFlag)
		done = v == "1"
		return err
	})
	return done, err
}
