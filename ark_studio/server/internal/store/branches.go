package store

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/sqlitedb"
)

// Lines of work (Go only, Q-0003, 2026-11-26).  A line is a tag of one
// namespace, "branch:<name>" by default (settings.branches.prefix).  A unit
// that serves several lines carries several such tags (a merge); a line that
// splits gets a new tag on the new sub-lines.  Nothing else is stored:
//
//   Branches          every line of the project with its size, open units,
//                     what waits in Needs you, last activity, the ARK sessions
//                     that wrote to it (display only), its status (active,
//                     quiet, stalled, done) and the stored topic that shows it
//                     (a topic whose rule is exactly that tag), plus the lines
//                     of each Needs you item.  Kept per project revision.
//   BranchTopic       a line as a topic page that is not stored: the units
//                     carrying the tag, laid out like a rule topic.
//   BranchSuggestions tags to add to units without one, from the part_of /
//                     derived_from structure only (never from sessions: one
//                     session often works on several lines).  Applied only
//                     after a person reviewed them.
//
// Tags are set only by people and by agents following the rules (C2: carry
// the parent's line tags over when the work belongs to the same line); the
// server never copies them (the user, 2026-11-26: inheritance "不一定准").

// DefaultBranchPrefix is the tag namespace of lines when the project sets none.
const DefaultBranchPrefix = "branch"

// BranchStalledDays: a line with something waiting in Needs you and no
// activity for this long is stalled.
const BranchStalledDays = 3

// branchActiveDays: a line with activity this recent is active.
const branchActiveDays = 14

// branchRelations are the links a line follows (child -> parent).
var branchRelations = []string{"part_of", "derived_from"}

var branchPrefixRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,23}$`)

// ValidBranchPrefix reports whether p can be the line tag namespace.
func ValidBranchPrefix(p string) bool {
	return branchPrefixRe.MatchString(p) && p != "needs" && p != "type" && p != "status" && p != "importance" && p != "marker"
}

// BranchPrefix is the project's line tag namespace (settings.branches.prefix).
func (s *Store) BranchPrefix() string {
	if s.Manifest == nil {
		return DefaultBranchPrefix
	}
	settings, _ := s.Manifest.Get("settings").(*pyjson.Object)
	branches, _ := settings.Value("branches").(*pyjson.Object)
	if p, ok := branches.Value("prefix").(string); ok && ValidBranchPrefix(p) {
		return p
	}
	return DefaultBranchPrefix
}

// branchTags is the line tags of each given unit (all units when ids is nil).
func branchTags(ctx context.Context, q sqlitedb.Querier, prefix string, ids []int64) (map[int64][]string, error) {
	out := map[int64][]string{}
	add := func(rows []sqlitedb.Row) {
		for _, r := range rows {
			out[r.Int("unit_id")] = append(out[r.Int("unit_id")], r.Str("tag"))
		}
	}
	head := prefix + ":"
	if ids == nil {
		rows, err := sqlitedb.Query(ctx, q, "SELECT t.unit_id, t.tag FROM unit_tags t JOIN units u ON u.id=t.unit_id "+
			"WHERE u.archived=0 AND substr(t.tag,1,?)=? ORDER BY t.unit_id, t.tag", int64(len(head)), head)
		if err != nil {
			return nil, err
		}
		add(rows)
		return out, nil
	}
	for off := 0; off < len(ids); off += 400 {
		batch := sqlitedb.Args(ids[off:min(off+400, len(ids))])
		rows, err := groupRows(ctx, q, "SELECT unit_id, tag FROM unit_tags WHERE unit_id IN ({marks}) AND substr(tag,1,?)=? ORDER BY unit_id, tag",
			batch, int64(len(head)), head)
		if err != nil {
			return nil, err
		}
		add(rows)
	}
	return out, nil
}

func branchName(prefix, tag string) string { return strings.TrimPrefix(tag, prefix+":") }

// branchInfo is one line while Branches adds it up.
type branchInfo struct {
	tag, name                     string
	units, open, reviews, decided int64
	decisions                     int64
	last                          string
	sessions                      map[string]string // session id -> last write
	topic                         *pyjson.Object
}

type branchEntry struct {
	key  string
	data *pyjson.Object
}

type branchCache struct {
	mu      sync.Mutex
	entries []branchEntry
}

func (c *branchCache) get(key string) *pyjson.Object {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.entries {
		if e.key == key {
			return e.data
		}
	}
	return nil
}

func (c *branchCache) put(key string, data *pyjson.Object) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append([]branchEntry{{key, data}}, c.entries...)
	if len(c.entries) > 4 {
		c.entries = c.entries[:4]
	}
}

// branchCacheKey is the project revision, the prefix and the hour (status
// depends on the time).
func (s *Store) branchCacheKey(ctx context.Context, extra string) (string, error) {
	rev, err := s.Revision(ctx)
	if err != nil {
		return "", err
	}
	return strings.Join([]string{rev, s.BranchPrefix(), clock.Now().UTC().Format("2006-01-02T15"), extra}, "|"), nil
}

// Branches lists the project's lines (see the file comment).  The reply is a
// copy the caller may change.
func (s *Store) Branches(ctx context.Context) (*pyjson.Object, error) {
	key, err := s.branchCacheKey(ctx, "list")
	if err != nil {
		return nil, err
	}
	if data := s.branches.get(key); data != nil {
		return data.Copy(), nil
	}
	data, err := s.branchList(ctx)
	if err != nil {
		return nil, err
	}
	s.branches.put(key, data)
	return data.Copy(), nil
}

func (s *Store) branchList(ctx context.Context) (*pyjson.Object, error) {
	prefix := s.BranchPrefix()
	now := clock.Now().UTC()
	var out *pyjson.Object
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		tags, err := branchTags(ctx, q, prefix, nil)
		if err != nil {
			return err
		}
		lines := map[string]*branchInfo{}
		var ids []int64
		for id, list := range tags {
			ids = append(ids, id)
			for _, tag := range list {
				if lines[tag] == nil {
					lines[tag] = &branchInfo{tag: tag, name: branchName(prefix, tag), sessions: map[string]string{}}
				}
			}
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		units := map[int64]sqlitedb.Row{}
		for off := 0; off < len(ids); off += 400 {
			batch := sqlitedb.Args(ids[off:min(off+400, len(ids))])
			rows, err := groupRows(ctx, q, "SELECT id, uid, type, status, updated_at FROM units WHERE id IN ({marks})", batch)
			if err != nil {
				return err
			}
			for _, r := range rows {
				units[r.Int("id")] = r
			}
			acts, err := groupRows(ctx, q, "SELECT unit_id, MAX(at) at FROM activity WHERE unit_id IN ({marks}) GROUP BY unit_id", batch)
			if err != nil {
				return err
			}
			for _, a := range acts {
				for _, tag := range tags[a.Int("unit_id")] {
					if l := lines[tag]; a.Str("at") > l.last {
						l.last = a.Str("at")
					}
				}
			}
			// The ARK sessions that wrote to the line (activityobs.go), for
			// display only: a session often works on several lines.
			sess, err := groupRows(ctx, q, "SELECT unit_id, at, detail_json FROM activity WHERE unit_id IN ({marks}) "+
				"AND actor LIKE 'agent:%' AND detail_json LIKE '%\"session\"%'", batch)
			if err != nil {
				return err
			}
			for _, a := range sess {
				id, _ := parseDetail(a.Get("detail_json")).Value("session").(string)
				if id == "" {
					continue
				}
				for _, tag := range tags[a.Int("unit_id")] {
					if l := lines[tag]; a.Str("at") > l.sessions[id] {
						l.sessions[id] = a.Str("at")
					}
				}
			}
		}
		needs := pyjson.NewObject()
		untagged := int64(0)
		for _, id := range ids {
			r, ok := units[id]
			if !ok {
				continue
			}
			pending := r.Str("status") == "review" || IsOpenDecision(r.Str("type"), r.Str("status"))
			for _, tag := range tags[id] {
				l := lines[tag]
				l.units++
				if !IsClosedStatus(r.Str("status")) {
					l.open++
				}
				if r.Str("updated_at") > l.last {
					l.last = r.Str("updated_at")
				}
				if r.Str("status") == "review" {
					l.reviews++
				} else if IsOpenDecision(r.Str("type"), r.Str("status")) {
					l.decisions++
				}
			}
			if pending {
				needs.Set(r.Str("uid"), branchNames(prefix, tags[id]))
			}
		}
		// Units waiting in Needs you without a line.
		pendingRows, err := sqlitedb.Query(ctx, q, "SELECT id FROM units WHERE "+needsYouWhere)
		if err != nil {
			return err
		}
		for _, r := range pendingRows {
			if len(tags[r.Int("id")]) == 0 {
				untagged++
			}
		}
		// Review groups: the lines of their members.
		if has, err := hasReviewTables(ctx, q); err != nil {
			return err
		} else if has {
			rows, err := sqlitedb.Query(ctx, q, "SELECT g.id gid, m.unit_id FROM review_groups g JOIN review_group_members m ON m.group_id=g.id "+
				"WHERE g.status='review' ORDER BY g.id, m.position")
			if err != nil {
				return err
			}
			groupTags := map[int64][]string{}
			var order []int64
			for _, r := range rows {
				gid := r.Int("gid")
				if _, ok := groupTags[gid]; !ok {
					order = append(order, gid)
					groupTags[gid] = []string{}
				}
				for _, tag := range tags[r.Int("unit_id")] {
					if !containsString(groupTags[gid], tag) {
						groupTags[gid] = append(groupTags[gid], tag)
					}
				}
			}
			for _, gid := range order {
				ref := "RG-" + strconv.FormatInt(gid, 10)
				if len(groupTags[gid]) > 0 {
					needs.Set(ref, branchNames(prefix, groupTags[gid]))
					for _, tag := range groupTags[gid] {
						lines[tag].reviews++
					}
				} else {
					untagged++
				}
			}
		}
		topics, err := branchTopics(ctx, q, prefix)
		if err != nil {
			return err
		}
		for tag, topic := range topics {
			if l := lines[tag]; l != nil {
				l.topic = topic
			}
		}
		list := make([]*branchInfo, 0, len(lines))
		for _, l := range lines {
			list = append(list, l)
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].last != list[j].last {
				return list[i].last > list[j].last
			}
			return list[i].name < list[j].name
		})
		items := []any{}
		for _, l := range list {
			items = append(items, l.object(now))
		}
		out = pyjson.NewObject("prefix", prefix, "stalled_days", int64(BranchStalledDays), "items", items,
			"needs", needs, "untagged_pending", untagged)
		return nil
	})
	return out, err
}

func branchNames(prefix string, tags []string) []any {
	out := make([]any, 0, len(tags))
	for _, tag := range tags {
		out = append(out, branchName(prefix, tag))
	}
	return out
}

// BranchStatus is a line's status from its open units, what waits in Needs
// you and its last activity: "done" (no open unit), "stalled" (something
// waits and nothing happened for BranchStalledDays), "active" (activity in
// the last two weeks) or "quiet".
func BranchStatus(open, pending int64, last string, now time.Time) string {
	if open == 0 && pending == 0 {
		return "done"
	}
	at, ok := parseActivityTime(last)
	if pending > 0 && (!ok || now.Sub(at) >= BranchStalledDays*24*time.Hour) {
		return "stalled"
	}
	if ok && now.Sub(at) < branchActiveDays*24*time.Hour {
		return "active"
	}
	return "quiet"
}

func parseActivityTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

func (l *branchInfo) object(now time.Time) *pyjson.Object {
	pending := l.reviews + l.decisions
	type seen struct{ id, at string }
	var sessions []seen
	for id, at := range l.sessions {
		sessions = append(sessions, seen{id, at})
	}
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].at != sessions[j].at {
			return sessions[i].at > sessions[j].at
		}
		return sessions[i].id < sessions[j].id
	})
	recent := []any{}
	for i, s := range sessions {
		if i == 5 {
			break
		}
		recent = append(recent, s.id)
	}
	var last any
	if l.last != "" {
		last = l.last
	}
	var topic any
	if l.topic != nil {
		topic = l.topic
	}
	status := BranchStatus(l.open, pending, l.last, now)
	return pyjson.NewObject("tag", l.tag, "name", l.name, "parent", lineParent(l.name), "depth", int64(strings.Count(l.name, "/")),
		"units", l.units, "open", l.open,
		"pending", pending, "reviews", l.reviews, "decisions", l.decisions, "last_activity", last,
		"status", status, "stalled", status == "stalled", "sessions", recent, "session_count", int64(len(sessions)),
		"topic", topic, "ref", branchRef(l.topic, l.tag))
}

// branchRef is what the topic page opens for a line: its stored topic, else
// the tag (a virtual topic, BranchTopic).
func branchRef(topic *pyjson.Object, tag string) string {
	if topic != nil {
		return pyjson.Str(topic.Value("uid"))
	}
	return tag
}

// branchTopics maps line tags to the stored topic that shows them: a topic
// whose rule selects exactly that one tag (the oldest when several do).
func branchTopics(ctx context.Context, q sqlitedb.Querier, prefix string) (map[string]*pyjson.Object, error) {
	rows, err := sqlitedb.Query(ctx, q, "SELECT id, uid, title, meta_json FROM units WHERE archived=0 AND type='topic' ORDER BY id")
	if err != nil {
		return nil, err
	}
	out := map[string]*pyjson.Object{}
	for _, r := range rows {
		tag := ruleBranchTag(r, prefix)
		if tag == "" || out[tag] != nil {
			continue
		}
		out[tag] = pyjson.NewObject("id", r.Get("id"), "uid", r.Get("uid"), "title", r.Get("title"), "via", "rule")
	}
	// A topic that itself carries exactly one line tag (ARK_Develop's TP
	// topics: TP-0005 line:frontend) shows that line when no rule does.
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.Int("id")
	}
	carried, err := branchTags(ctx, q, prefix, ids)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		tags := carried[r.Int("id")]
		if len(tags) != 1 || out[tags[0]] != nil {
			continue
		}
		out[tags[0]] = pyjson.NewObject("id", r.Get("id"), "uid", r.Get("uid"), "title", r.Get("title"), "via", "tag")
	}
	return out, nil
}

// ruleBranchTag is the line tag a topic's rule selects ("" when the rule is
// not exactly one line tag).
func ruleBranchTag(topic sqlitedb.Row, prefix string) string {
	sg, err := topicSubgraph(topic)
	if err != nil || sg.rule == nil {
		return ""
	}
	tags, _ := sg.rule.Value("tags").([]any)
	key := "tags"
	if trees, _ := sg.rule.Value("tag_trees").([]any); len(trees) == 1 && len(tags) == 0 {
		tags, key = trees, "tag_trees"
	}
	if len(tags) != 1 {
		return ""
	}
	tag, _ := tags[0].(string)
	if !strings.HasPrefix(tag, prefix+":") || len(tag) == len(prefix)+1 {
		return ""
	}
	for _, k := range sg.rule.Keys() {
		if k != key && pyjson.Truthy(sg.rule.Value(k)) {
			return ""
		}
	}
	return tag
}

// BranchTag is the tag of line name ("" when it cannot be a tag).
func (s *Store) BranchTag(name string) string {
	prefix := s.BranchPrefix()
	name = strings.TrimPrefix(pytext.Strip(name), prefix+":")
	ns, value, err := splitTag(prefix + ":" + name)
	if err != nil || ns != prefix || !ValidLinePath(value) {
		return ""
	}
	return ns + ":" + value
}

// BranchTopic is a line as a topic page (the Topic payload) that is not
// stored: its members are the units carrying the tag.  The topic carries
// "virtual": true, "branch" (the name) and "uid" = the tag.
//
// With exact false (the default page) the members are the line and every
// line below it ("hoi" shows "hoi/pilot"), rule {"tag_trees": [tag]}; exact
// shows only the line itself, rule {"tags": [tag]}.
func (s *Store) BranchTopic(ctx context.Context, name string, full, exact bool) (*pyjson.Object, error) {
	tag := s.BranchTag(name)
	if tag == "" {
		return nil, storeErr("not a line: %s", pyjson.Repr(name))
	}
	prefix := s.BranchPrefix()
	rule := pyjson.NewObject("tag_trees", []any{tag})
	if exact {
		rule = pyjson.NewObject("tags", []any{tag})
	}
	meta := pyjson.NewObject("branch", branchName(prefix, tag), "virtual", true, "descendants", !exact,
		"subgraph", pyjson.NewObject("rule", rule))
	row := sqlitedb.Row{Cols: []string{"id", "meta_json"}, Vals: []any{int64(0), string(pyjson.Dumps(meta))}}
	var out *pyjson.Object
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		last := ""
		ids, err := s.ruleIDs(ctx, q, rule, 0)
		if err != nil {
			return err
		}
		if len(ids) > 0 {
			r, _, err := sqlitedb.QueryOne(ctx, q, "SELECT MAX(updated_at) m FROM units WHERE id IN ("+sqlitedb.Marks(min(len(ids), 400))+")",
				sqlitedb.Args(ids[:min(len(ids), 400)])...)
			if err != nil {
				return err
			}
			last = r.Str("m")
		}
		var updated any
		if last != "" {
			updated = last
		}
		topic := pyjson.NewObject("id", nil, "uid", tag, "type", "topic", "title", branchName(prefix, tag), "body", "",
			"status", "open", "importance", "p2", "meta", meta, "tags", []any{}, "archived", false,
			"updated_at", updated, "created_at", nil, "virtual", true, "branch", branchName(prefix, tag))
		out, err = s.topicPayload(ctx, q, row, topic, full)
		return err
	})
	return out, err
}

// ------------------------------------------------------------ suggestions

// BranchSuggestOptions tune BranchSuggestions.
type BranchSuggestOptions struct {
	Depth     int      // parent steps followed (default 6)
	Relations []string // links followed (default part_of, derived_from)
	MinUnits  int      // units a new line needs besides its root (default 3)
	Limit     int      // inherit suggestions listed (default 300)
	// StopAtTopics ends the climb at a topic (BranchSuggestions sets it
	// unless KeepClimbing).
	StopAtTopics bool
	KeepClimbing bool
}

type suggestUnit struct {
	id                int64
	uid, typ, title   string
	status, createdAt string
	parents           []int64
	tags              []string
}

// BranchSuggestions proposes line tags for units without one, from the
// part_of / derived_from structure: "inherit" (the nearest tagged ancestor's
// lines) and "new_lines" (a root goal without a line whose subtree has at
// least MinUnits units).  Nothing is written.
func (s *Store) BranchSuggestions(ctx context.Context, opt BranchSuggestOptions) (*pyjson.Object, error) {
	if opt.Depth <= 0 {
		opt.Depth = 6
	}
	if len(opt.Relations) == 0 {
		opt.Relations = []string{"part_of"}
	}
	opt.StopAtTopics = !opt.KeepClimbing
	if opt.MinUnits <= 0 {
		opt.MinUnits = 3
	}
	if opt.Limit <= 0 {
		opt.Limit = 300
	}
	key, err := s.branchCacheKey(ctx, "suggest:"+strconv.Itoa(opt.Depth)+":"+strings.Join(opt.Relations, ",")+":"+
		strconv.Itoa(opt.MinUnits)+":"+strconv.Itoa(opt.Limit)+":"+strconv.FormatBool(opt.StopAtTopics))
	if err != nil {
		return nil, err
	}
	if data := s.branches.get(key); data != nil {
		return data.Copy(), nil
	}
	prefix := s.BranchPrefix()
	var out *pyjson.Object
	err = s.read(ctx, func(q sqlitedb.Querier) error {
		rows, err := sqlitedb.Query(ctx, q, "SELECT id, uid, type, title, status, created_at FROM units WHERE archived=0 ORDER BY id")
		if err != nil {
			return err
		}
		units := map[int64]*suggestUnit{}
		var order []int64
		for _, r := range rows {
			units[r.Int("id")] = &suggestUnit{id: r.Int("id"), uid: r.Str("uid"), typ: r.Str("type"), title: r.Str("title"),
				status: r.Str("status"), createdAt: r.Str("created_at")}
			order = append(order, r.Int("id"))
		}
		rels := make([]any, len(opt.Relations))
		for i, r := range opt.Relations {
			rels[i] = r
		}
		links, err := sqlitedb.Query(ctx, q, "SELECT source_id, target_id FROM unit_links WHERE relation IN ("+
			sqlitedb.Marks(len(rels))+") ORDER BY id", rels...)
		if err != nil {
			return err
		}
		for _, l := range links {
			src, dst := units[l.Int("source_id")], units[l.Int("target_id")]
			if src == nil || dst == nil || src == dst {
				continue
			}
			src.parents = append(src.parents, dst.id)
		}
		tags, err := branchTags(ctx, q, prefix, nil)
		if err != nil {
			return err
		}
		for id, list := range tags {
			if u := units[id]; u != nil {
				u.tags = list
			}
		}
		out = suggestBranches(units, order, prefix, opt)
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.branches.put(key, out)
	return out.Copy(), nil
}

// suggestBranches is BranchSuggestions over loaded units (pure).
func suggestBranches(units map[int64]*suggestUnit, order []int64, prefix string, opt BranchSuggestOptions) *pyjson.Object {
	brief := func(u *suggestUnit) *pyjson.Object {
		return pyjson.NewObject("id", u.id, "uid", u.uid, "type", u.typ, "title", u.title, "status", u.status)
	}
	inherit := []any{}
	inheritN := 0
	rootUnits := map[int64][]int64{} // root id -> untagged units below it
	var roots []int64
	for _, id := range order {
		u := units[id]
		if len(u.tags) > 0 || u.typ == "topic" {
			continue
		}
		// Breadth-first up the parents: the nearest level with a tagged unit.
		seen := map[int64]bool{id: true}
		level := []int64{id}
		var found []string
		var via []any
		var top []int64
		for depth := 0; depth < opt.Depth && len(level) > 0 && found == nil; depth++ {
			var next []int64
			for _, cur := range level {
				parents := units[cur].parents
				if len(parents) == 0 && cur != id {
					top = append(top, cur)
				}
				for _, p := range parents {
					if seen[p] {
						continue
					}
					seen[p] = true
					next = append(next, p)
				}
			}
			var climb []int64
			for _, p := range next {
				if len(units[p].tags) > 0 {
					for _, tag := range units[p].tags {
						if !containsString(found, tag) {
							found = append(found, tag)
						}
					}
					via = append(via, units[p].uid)
				}
				// A topic is a line someone already drew: the climb stops
				// there (above it usually sits one umbrella task that would
				// swallow every line).
				if opt.StopAtTopics && units[p].typ == "topic" {
					if !containsInt64(top, p) {
						top = append(top, p)
					}
					continue
				}
				climb = append(climb, p)
			}
			level = climb
		}
		if found != nil {
			inheritN++
			if len(inherit) < opt.Limit {
				sort.Strings(found)
				inherit = append(inherit, brief(u).Set("tags", toAnyStrings(found)).Set("via", via))
			}
			continue
		}
		for _, cur := range level { // depth exhausted: the farthest units reached count as roots
			if cur != id && !containsInt64(top, cur) {
				top = append(top, cur)
			}
		}
		for _, r := range top {
			if _, ok := rootUnits[r]; !ok {
				roots = append(roots, r)
			}
			rootUnits[r] = append(rootUnits[r], id)
		}
	}
	// A unit under several roots (a merge) is listed under each.
	newLines := []any{}
	merges := int64(0)
	count := map[int64]int{}
	for _, r := range roots {
		for _, id := range rootUnits[r] {
			count[id]++
		}
	}
	for _, n := range count {
		if n > 1 {
			merges++
		}
	}
	sort.SliceStable(roots, func(i, j int) bool { return len(rootUnits[roots[i]]) > len(rootUnits[roots[j]]) })
	usedNames := map[string]bool{}
	for _, r := range roots {
		members := rootUnits[r]
		if len(members) < opt.MinUnits {
			continue
		}
		root := units[r]
		name := suggestBranchName(root, usedNames)
		list := make([]any, 0, len(members))
		for _, id := range members {
			list = append(list, units[id].uid)
		}
		rootTags := []any{}
		if len(root.tags) == 0 {
			rootTags = append(rootTags, root.uid)
		}
		newLines = append(newLines, pyjson.NewObject("root", brief(root), "name", name, "tag", prefix+":"+name,
			"units", list, "count", int64(len(members)), "include_root", rootTags, "root_is_topic", root.typ == "topic"))
	}
	return pyjson.NewObject("prefix", prefix, "depth", int64(opt.Depth), "relations", toAnyStrings(opt.Relations),
		"stop_at_topics", opt.StopAtTopics,
		"inherit", inherit, "inherit_total", int64(inheritN), "new_lines", newLines, "shared_units", merges)
}

var slugDropRe = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// suggestBranchName is a short tag value for a new line: the words of the
// root's title (letters and digits of any script, lowercase, joined by "-",
// at most 32 characters), else its uid.
func suggestBranchName(root *suggestUnit, used map[string]bool) string {
	words := strings.Fields(slugDropRe.ReplaceAllString(strings.ToLower(root.title), " "))
	name := ""
	for _, w := range words {
		if len([]rune(name))+len([]rune(w))+1 > 32 {
			break
		}
		if name != "" {
			name += "-"
		}
		name += w
	}
	if len([]rune(name)) < 2 {
		name = strings.ToLower(root.uid)
	}
	base, n := name, 2
	for used[name] {
		name = base + "-" + strconv.Itoa(n)
		n++
	}
	used[name] = true
	return name
}

func toAnyStrings(list []string) []any {
	out := make([]any, len(list))
	for i, s := range list {
		out[i] = s
	}
	return out
}

func containsInt64(list []int64, v int64) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// setNeedsBranches gives each Needs you item (units, and review groups by
// their members) a "branches" list of line names when it has lines.
func (s *Store) setNeedsBranches(ctx context.Context, q sqlitedb.Querier, units []*pyjson.Object, groups []any) error {
	if len(units) == 0 && len(groups) == 0 {
		return nil
	}
	prefix := s.BranchPrefix()
	head := prefix + ":"
	rows, err := sqlitedb.Query(ctx, q, "SELECT u.uid, t.tag FROM unit_tags t JOIN units u ON u.id=t.unit_id "+
		"WHERE u.archived=0 AND substr(t.tag,1,?)=? ORDER BY u.uid, t.tag", int64(len(head)), head)
	if err != nil || len(rows) == 0 {
		return err
	}
	byUID := map[string][]string{}
	for _, r := range rows {
		byUID[r.Str("uid")] = append(byUID[r.Str("uid")], r.Str("tag"))
	}
	for _, item := range units {
		if tags := byUID[pyjson.Str(item.Value("uid"))]; len(tags) > 0 {
			item.Set("branches", branchNames(prefix, tags))
		}
	}
	for _, g := range groups {
		o, ok := g.(*pyjson.Object)
		if !ok {
			continue
		}
		var tags []string
		members, _ := o.Value("members").([]any)
		for _, m := range members {
			mo, ok := m.(*pyjson.Object)
			if !ok {
				continue
			}
			for _, tag := range byUID[pyjson.Str(mo.Value("uid"))] {
				if !containsString(tags, tag) {
					tags = append(tags, tag)
				}
			}
		}
		if len(tags) > 0 {
			o.Set("branches", branchNames(prefix, tags))
		}
	}
	return nil
}

// OrphanUnits lists live units (not topics) without a part_of or
// derived_from parent, newest first (Go only; scheduled tasks' tool).
func (s *Store) OrphanUnits(ctx context.Context, limit int) (*pyjson.Object, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	var out *pyjson.Object
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		rows, err := sqlitedb.Query(ctx, q, "SELECT id, uid, type, title, status, updated_at FROM units u WHERE archived=0 AND type<>'topic' "+
			"AND NOT EXISTS (SELECT 1 FROM unit_links l WHERE l.source_id=u.id AND l.relation IN ('part_of','derived_from')) "+
			"ORDER BY updated_at DESC, id DESC")
		if err != nil {
			return err
		}
		items := []any{}
		for i, r := range rows {
			if i == limit {
				break
			}
			items = append(items, pyjson.NewObject("uid", r.Get("uid"), "type", r.Get("type"), "title", r.Get("title"),
				"status", r.Get("status"), "updated_at", r.Get("updated_at")))
		}
		out = pyjson.NewObject("items", items, "total", int64(len(rows)))
		return nil
	})
	return out, err
}

// ValidLinePath reports whether a line name is a path of non-empty
// segments separated by "/" ("hoi/pilot/stairs"; the user, 2026-11-26:
// "branch可以允许采用类git风格命名 例如 主线/分线/分分线形式").
func ValidLinePath(name string) bool {
	if name == "" {
		return false
	}
	for _, seg := range strings.Split(name, "/") {
		if strings.TrimSpace(seg) == "" {
			return false
		}
	}
	return true
}

// checkLineTags refuses line tags whose path has an empty segment.
func (s *Store) checkLineTags(tags []string) error {
	head := s.BranchPrefix() + ":"
	for _, tag := range tags {
		if strings.HasPrefix(tag, head) && !ValidLinePath(tag[len(head):]) {
			return storeErr("bad line tag %s: a line path is names separated by / (for example %shoi/pilot), with no empty part", pyjson.Repr(tag), head)
		}
	}
	return nil
}

// lineParent is the parent path of a line ("" at the top).
func lineParent(name string) string {
	if i := strings.LastIndex(name, "/"); i > 0 {
		return name[:i]
	}
	return ""
}

// ------------------------------------------------------------ rename

// RenameLine moves line from (and every line below it) to to: on each unit
// the tag "<prefix>:from[/rest]" becomes "<prefix>:to[/rest]", in one batch.
// units limits it to those uids (undo passes the uids of the rename it
// reverses).  dryRun only lists the changes.  Stored topics whose rule names
// a moved line follow.  The reply lists {uid, title, from, to} per tag
// change and the batch id.
func (s *Store) RenameLine(ctx context.Context, from, to string, units []string, dryRun bool, actor string, who Who) (*pyjson.Object, error) {
	prefix := s.BranchPrefix()
	fromTag, toTag := s.BranchTag(from), s.BranchTag(to)
	if fromTag == "" || toTag == "" {
		return nil, storeErr("from and to must be line names such as hoi/pilot")
	}
	if fromTag == toTag {
		return nil, storeErr("the new name is the old one")
	}
	only := map[string]bool{}
	for _, u := range units {
		only[u] = true
	}
	type change struct {
		uid, title, from, to string
	}
	var changes []change
	var topics []sqlitedb.Row
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		rows, err := sqlitedb.Query(ctx, q, "SELECT u.uid, u.title, t.tag FROM unit_tags t JOIN units u ON u.id=t.unit_id "+
			"WHERE u.archived=0 AND (t.tag=? OR substr(t.tag,1,?)=?) ORDER BY u.id, t.tag",
			fromTag, int64(len([]rune(fromTag))+1), fromTag+"/")
		if err != nil {
			return err
		}
		for _, r := range rows {
			if len(only) > 0 && !only[r.Str("uid")] {
				continue
			}
			next := toTag + strings.TrimPrefix(r.Str("tag"), fromTag)
			if _, _, err := splitTag(next); err != nil {
				return storeErr("%s would become %s: %s", r.Str("tag"), next, err.Error())
			}
			changes = append(changes, change{r.Str("uid"), r.Str("title"), r.Str("tag"), next})
		}
		topics, err = sqlitedb.Query(ctx, q, "SELECT id, uid, meta_json FROM units WHERE archived=0 AND type='topic'")
		return err
	})
	if err != nil {
		return nil, err
	}
	list := []any{}
	for _, c := range changes {
		list = append(list, pyjson.NewObject("uid", c.uid, "title", c.title, "from", c.from, "to", c.to))
	}
	// Topics whose rule is a moved line.
	var moved []any
	type ruleMove struct {
		id   int64
		rule *pyjson.Object
	}
	var ruleMoves []ruleMove
	for _, r := range topics {
		sg, err := topicSubgraph(r)
		if err != nil || sg.rule == nil {
			continue
		}
		rule := sg.rule.Copy()
		hit := false
		for _, key := range []string{"tags", "tag_trees"} {
			vals, _ := rule.Value(key).([]any)
			next := make([]any, len(vals))
			for i, v := range vals {
				tag := pyjson.Str(v)
				if tag == fromTag || strings.HasPrefix(tag, fromTag+"/") {
					tag, hit = toTag+strings.TrimPrefix(tag, fromTag), true
				}
				next[i] = tag
			}
			if len(vals) > 0 {
				rule.Set(key, next)
			}
		}
		if hit && len(only) == 0 || hit && only[r.Str("uid")] {
			moved = append(moved, r.Str("uid"))
			ruleMoves = append(ruleMoves, ruleMove{r.Int("id"), rule})
		}
	}
	out := pyjson.NewObject("prefix", prefix, "from", fromTag, "to", toTag, "changes", list, "units", int64(len(uniqueUIDs(changes, func(c change) string { return c.uid }))),
		"topics", orEmptyList(moved), "dry_run", dryRun)
	if dryRun || (len(changes) == 0 && len(ruleMoves) == 0) {
		return out, nil
	}
	byUnit := map[string]*BatchItem{}
	var order []string
	for _, c := range changes {
		it := byUnit[c.uid]
		if it == nil {
			it = &BatchItem{Ref: c.uid}
			byUnit[c.uid] = it
			order = append(order, c.uid)
		}
		it.TagsRemove = append(it.TagsRemove, c.from)
		it.TagsAdd = append(it.TagsAdd, c.to)
	}
	batch := NewBatchID()
	for off := 0; off < len(order); off += MaxBatchUnits {
		var items []BatchItem
		for _, uid := range order[off:min(off+MaxBatchUnits, len(order))] {
			items = append(items, *byUnit[uid])
		}
		if _, err := s.BatchUpdate(ctx, items, batch, actor, who); err != nil {
			return nil, err
		}
	}
	for _, m := range ruleMoves {
		if _, err := s.DesignTopic(ctx, m.id, pyjson.NewObject("rule", m.rule), actor); err != nil {
			return nil, err
		}
	}
	return out.Set("batch", batch), nil
}

func uniqueUIDs[T any](list []T, key func(T) string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range list {
		if k := key(x); !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

func orEmptyList(v []any) []any {
	if v == nil {
		return []any{}
	}
	return v
}
