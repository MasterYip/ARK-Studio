package store

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/sqlitedb"
)

// Port of arkstudio/overview.py: project_overview(store, members=...).

var (
	ovClosed  = []any{"done", "abandoned", "archived"}
	ovSupport = []any{"supports", "validates"}
	ovRefute  = []any{"contradicts"}
	ovQuiet   = []any{"link.create", "link.delete", "link.update", "media.update"}
	ovStages  = []struct {
		id    string
		types []string
	}{
		{"capture", []string{"idea", "source"}},
		{"plan", []string{"story", "related_work", "method", "experiment"}},
		{"evidence", []string{"question", "claim", "evidence"}},
		{"execute", []string{"task", "milestone", "run"}},
		{"make", []string{"production", "artifact"}},
		{"share", []string{"outreach"}},
	}
)

const ovDays, ovListLimit = 14, 6

func isClosed(status string) bool {
	return status == "done" || status == "abandoned" || status == "archived"
}

func ovBrief(r sqlitedb.Row, owners map[int64]assigneeEntry) *pyjson.Object {
	o := pyjson.NewObject("id", r.Get("id"), "uid", r.Get("uid"), "type", r.Get("type"), "title", r.Get("title"),
		"status", r.Get("status"), "importance", r.Get("importance"), "start_at", r.Get("start_at"),
		"end_at", r.Get("end_at"), "updated_at", r.Get("updated_at"))
	if owners != nil {
		own := r.Get("assignee")
		if !pyjson.Truthy(own) {
			own = owners[r.Int("id")].nameOrNil()
		}
		o.Set("assignee_effective", own)
	}
	return o
}

// statusChange is overview._status_change.
func statusChange(detail *pyjson.Object) (any, any) {
	if st, ok := detail.Value("status").(*pyjson.Object); ok {
		return st.Value("from"), st.Value("to")
	}
	return nil, nil
}

func isoDay(t time.Time) string { return t.Format("2006-01-02") }

// ProjectOverview is overview.project_overview.  storeMS is the elapsed
// time (reported, never compared).
func (s *Store) ProjectOverview(ctx context.Context, members []string) (*pyjson.Object, error) {
	started := time.Now()
	now := Clock().UTC()
	todayD := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	today := isoDay(todayD)
	weekAgo := isoDay(todayD.AddDate(0, 0, -7))
	twoWeeks := isoDay(todayD.AddDate(0, 0, -14))
	soonEnd := isoDay(todayD.AddDate(0, 0, 7))
	closed := sqlitedb.Marks(3)
	var out *pyjson.Object
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		owners, err := s.assigneeIndex(ctx, q)
		if err != nil {
			return err
		}
		live, err := sqlitedb.Query(ctx, q, "SELECT id, type, status, end_at, assignee FROM units WHERE archived=0")
		if err != nil {
			return err
		}
		liveIDs := map[int64]bool{}
		for _, r := range live {
			liveIDs[r.Int("id")] = true
		}
		links, err := sqlitedb.Query(ctx, q, "SELECT source_id, target_id, relation FROM unit_links")
		if err != nil {
			return err
		}
		degree := map[int64]int64{}
		edges := []any{}
		for _, l := range links {
			a, b := l.Int("source_id"), l.Int("target_id")
			if liveIDs[a] && liveIDs[b] {
				edges = append(edges, pyjson.NewObject("source", a, "target", b, "relation", l.Get("relation")))
				degree[a]++
				degree[b]++
			}
		}
		nodes := make([]any, len(live))
		for i, r := range live {
			nodes[i] = pyjson.NewObject("id", r.Get("id"), "type", r.Get("type"), "status", r.Get("status"),
				"degree", degree[r.Int("id")])
		}
		tagRows, err := sqlitedb.Query(ctx, q, "SELECT unit_id FROM unit_tags WHERE tag='status:blocked'")
		if err != nil {
			return err
		}
		blockedTag := map[int64]bool{}
		for _, r := range tagRows {
			blockedTag[r.Int("unit_id")] = true
		}

		prior := map[int64]any{}
		archivedThen := map[int64]bool{}
		acts, err := sqlitedb.Query(ctx, q, "SELECT unit_id, action, detail_json FROM activity WHERE at >= ? AND unit_id IS NOT NULL "+
			"AND action IN ('unit.create','unit.update','unit.archive','unit.restore') ORDER BY id", weekAgo)
		if err != nil {
			return err
		}
		for _, r := range acts {
			uid := r.Int("unit_id")
			switch r.Str("action") {
			case "unit.create":
				if _, ok := prior[uid]; !ok {
					prior[uid] = nil
				}
				continue
			case "unit.archive":
				if _, ok := archivedThen[uid]; !ok {
					archivedThen[uid] = false
				}
				continue
			case "unit.restore":
				if _, ok := archivedThen[uid]; !ok {
					archivedThen[uid] = true
				}
				continue
			}
			old, _ := statusChange(parseDetail(r.Get("detail_json")))
			if _, has := prior[uid]; pyjson.Truthy(old) && !has {
				prior[uid] = old
			}
		}
		archivedRows := map[int64]sqlitedb.Row{}
		if len(archivedThen) > 0 {
			rows, err := sqlitedb.Query(ctx, q, "SELECT id, type, status, end_at FROM units WHERE archived=1")
			if err != nil {
				return err
			}
			for _, r := range rows {
				archivedRows[r.Int("id")] = r
			}
		}

		createdDays := map[string]int64{}
		rows, err := sqlitedb.Query(ctx, q, "SELECT substr(created_at,1,10) d, COUNT(*) n FROM units WHERE created_at >= ? GROUP BY d", twoWeeks)
		if err != nil {
			return err
		}
		for _, r := range rows {
			createdDays[pyjson.Str(r.Get("d"))] = r.Int("n")
		}
		completedDays := map[string]map[int64]bool{}
		eventsDays := map[string]int64{}
		rows, err = sqlitedb.Query(ctx, q, "SELECT substr(a.at,1,10) d, a.action, a.unit_id, a.detail_json, u.status "+
			"FROM activity a LEFT JOIN units u ON u.id=a.unit_id WHERE a.at >= ?", twoWeeks)
		if err != nil {
			return err
		}
		for _, r := range rows {
			day, action := pyjson.Str(r.Get("d")), r.Str("action")
			if !containsAny(ovQuiet, action) {
				eventsDays[day]++
			}
			if action != "unit.update" || r.Get("unit_id") == nil {
				continue
			}
			detail := parseDetail(r.Get("detail_json"))
			_, nw := statusChange(detail)
			done := false
			if nw != nil {
				done = nw == "done"
			} else {
				fields, _ := Iter(detail.Value("fields"))
				done = containsAny(fields, "status") && r.Get("status") == "done"
			}
			if done {
				if completedDays[day] == nil {
					completedDays[day] = map[int64]bool{}
				}
				completedDays[day][r.Int("unit_id")] = true
			}
		}

		brief := "id, uid, type, title, status, importance, start_at, end_at, updated_at, assignee"
		count := func(query string, args ...any) (int64, error) {
			r, _, err := sqlitedb.QueryOne(ctx, q, query, args...)
			return r.Int("n"), err
		}
		overdueTotal, err := count("SELECT COUNT(*) n FROM units WHERE archived=0 AND status NOT IN ("+closed+") "+
			"AND end_at IS NOT NULL AND end_at < ?", append(append([]any{}, ovClosed...), today)...)
		if err != nil {
			return err
		}
		overdueRows, err := sqlitedb.Query(ctx, q, "SELECT "+brief+" FROM units WHERE archived=0 AND status NOT IN ("+closed+") "+
			"AND end_at IS NOT NULL AND end_at < ? ORDER BY end_at ASC, id LIMIT ?", append(append([]any{}, ovClosed...), today, int64(ovListLimit))...)
		if err != nil {
			return err
		}
		soonTotal, err := count("SELECT COUNT(*) n FROM units WHERE archived=0 AND status NOT IN ("+closed+") "+
			"AND end_at >= ? AND end_at <= ?", append(append([]any{}, ovClosed...), today, soonEnd)...)
		if err != nil {
			return err
		}
		soonRows, err := sqlitedb.Query(ctx, q, "SELECT "+brief+" FROM units WHERE archived=0 AND status NOT IN ("+closed+") "+
			"AND end_at >= ? AND end_at <= ? ORDER BY end_at ASC, id LIMIT ?", append(append([]any{}, ovClosed...), today, soonEnd, int64(ovListLimit))...)
		if err != nil {
			return err
		}
		blockedWhere := "archived=0 AND status NOT IN ('done','abandoned','archived') AND (status='blocked' OR id IN " +
			"(SELECT unit_id FROM unit_tags WHERE tag='status:blocked'))"
		blockedTotal, err := count("SELECT COUNT(*) n FROM units WHERE " + blockedWhere)
		if err != nil {
			return err
		}
		blockedRows, err := sqlitedb.Query(ctx, q, "SELECT "+brief+" FROM units WHERE "+blockedWhere+" ORDER BY updated_at ASC LIMIT ?", int64(ovListLimit))
		if err != nil {
			return err
		}
		blockers := map[int64][]any{}
		if len(blockedRows) > 0 {
			bids := ids(blockedRows)
			marks := sqlitedb.Marks(len(bids))
			rows, err := sqlitedb.Query(ctx, q, `SELECT l.target_id unit, 'blocks' relation, u.uid, u.title, u.status, u.type
                      FROM unit_links l JOIN units u ON u.id=l.source_id
                     WHERE l.relation='blocks' AND l.target_id IN (`+marks+`)
                    UNION ALL
                    SELECT l.source_id unit, 'depends_on' relation, u.uid, u.title, u.status, u.type
                      FROM unit_links l JOIN units u ON u.id=l.target_id
                     WHERE l.relation='depends_on' AND l.source_id IN (`+marks+`)`, append(append([]any{}, bids...), bids...)...)
			if err != nil {
				return err
			}
			for _, r := range rows {
				blockers[r.Int("unit")] = append(blockers[r.Int("unit")], pyjson.NewObject("uid", r.Get("uid"),
					"title", r.Get("title"), "status", r.Get("status"), "type", r.Get("type"), "relation", r.Get("relation")))
			}
		}
		qCounts, _, err := sqlitedb.QueryOne(ctx, q, "SELECT COUNT(*) n, SUM(CASE WHEN status NOT IN ("+closed+") THEN 1 ELSE 0 END) o "+
			"FROM units WHERE archived=0 AND type='question'", ovClosed...)
		if err != nil {
			return err
		}
		qLatest, err := sqlitedb.Query(ctx, q, "SELECT "+brief+" FROM units WHERE archived=0 AND type='question' "+
			"AND status NOT IN ("+closed+") ORDER BY updated_at DESC, id DESC LIMIT 3", ovClosed...)
		if err != nil {
			return err
		}
		prefixed := []string{}
		for _, c := range strings.Split(brief, ",") {
			prefixed = append(prefixed, "u."+strings.TrimSpace(c))
		}
		claims, err := sqlitedb.Query(ctx, q, "SELECT "+strings.Join(prefixed, ", ")+",\n"+
			"                   EXISTS(SELECT 1 FROM unit_links l WHERE l.target_id=u.id AND l.relation IN (?,?)) sup,\n"+
			"                   EXISTS(SELECT 1 FROM unit_links l WHERE l.target_id=u.id AND l.relation IN (?)) ref\n"+
			"                FROM units u WHERE u.archived=0 AND u.type='claim'\n"+
			"                ORDER BY u.updated_at DESC, u.id DESC", append(append([]any{}, ovSupport...), ovRefute...)...)
		if err != nil {
			return err
		}
		expRows, err := sqlitedb.Query(ctx, q, "SELECT status, COUNT(*) n FROM units WHERE archived=0 AND type='experiment' GROUP BY status")
		if err != nil {
			return err
		}
		expLatest, err := sqlitedb.Query(ctx, q, "SELECT "+brief+" FROM units WHERE archived=0 AND type='experiment' "+
			"ORDER BY updated_at DESC, id DESC LIMIT 3")
		if err != nil {
			return err
		}
		activityRows, err := sqlitedb.Query(ctx, q, `SELECT a.id, a.at, a.actor, a.action, a.unit_id, a.detail_json, u.uid, u.title, u.type
                  FROM activity a LEFT JOIN units u ON u.id=a.unit_id
                 WHERE a.action NOT IN (?,?,?,?) ORDER BY a.at DESC, a.id DESC LIMIT 40`, ovQuiet...)
		if err != nil {
			return err
		}

		isBlocked := func(uid int64, status string) bool {
			return status == "blocked" || (blockedTag[uid] && !isClosed(status))
		}
		now := map[string]int64{}
		prev := map[string]int64{}
		stageOf := map[string]string{}
		stages := map[string]map[string]int64{}
		for _, st := range ovStages {
			stages[st.id] = map[string]int64{}
			for _, t := range st.types {
				stageOf[t] = st.id
			}
		}
		type bucket struct {
			username                               string
			open, active, review, blocked, overdue int64
			order                                  int
		}
		load := map[string]*bucket{}
		var loadOrder []string
		unassigned := &bucket{}
		bumpPrev := func(uid int64, status any, endAt any, changed bool) {
			st, ok := status.(string)
			if status == nil || !ok {
				return
			}
			if st == "active" {
				prev["active"]++
			}
			if st == "review" {
				prev["review"]++
			}
			if changed {
				if st == "blocked" {
					prev["blocked"]++
				}
			} else if isBlocked(uid, st) {
				prev["blocked"]++
			}
			if e, _ := endAt.(string); !isClosed(st) && e != "" && e < weekAgo {
				prev["overdue"]++
			}
		}
		for _, r := range live {
			uid, typeName, status := r.Int("id"), r.Str("type"), r.Str("status")
			endAt := r.Str("end_at")
			open := !isClosed(status)
			overdue := open && endAt != "" && endAt < today
			blocked := isBlocked(uid, status)
			if status == "active" {
				now["active"]++
			}
			if status == "review" {
				now["review"]++
			}
			if blocked {
				now["blocked"]++
			}
			if overdue {
				now["overdue"]++
			}
			if open {
				now["open"]++
			}
			if sid, ok := stageOf[typeName]; ok && status != "abandoned" && status != "archived" {
				st := stages[sid]
				st["count"]++
				st["done"] += b2i(status == "done")
				st["active"] += b2i(status == "active" && !blocked)
				st["review"] += b2i(status == "review" && !blocked)
				st["blocked"] += b2i(blocked)
				st["open"] += b2i(open)
			}
			if open {
				name := pyjson.Str(r.Get("assignee"))
				if !pyjson.Truthy(r.Get("assignee")) {
					e := owners[uid]
					name = ""
					if e.hasName {
						name = e.name
					}
				}
				bk := unassigned
				if name != "" {
					key := strings.ToLower(name)
					if load[key] == nil {
						load[key] = &bucket{username: name}
						loadOrder = append(loadOrder, key)
					}
					bk = load[key]
				}
				bk.open++
				bk.active += b2i(status == "active")
				bk.review += b2i(status == "review")
				bk.blocked += b2i(blocked)
				bk.overdue += b2i(overdue)
			}
			if archivedThen[uid] {
				continue
			}
			if p, ok := prior[uid]; ok {
				bumpPrev(uid, p, r.Get("end_at"), true)
			} else {
				bumpPrev(uid, status, r.Get("end_at"), false)
			}
		}
		for _, uid := range sortedKeys(archivedThen) {
			row, ok := archivedRows[uid]
			if ok && !archivedThen[uid] {
				if p, has := prior[uid]; has {
					bumpPrev(uid, p, row.Get("end_at"), true)
				} else {
					bumpPrev(uid, row.Get("status"), row.Get("end_at"), false)
				}
			}
		}
		kpis := pyjson.NewObject()
		for _, k := range []string{"active", "blocked", "review", "overdue"} {
			kpis.Set(k, pyjson.NewObject("now", now[k], "prev", prev[k], "delta", now[k]-prev[k]))
		}
		kpis.Set("open", pyjson.NewObject("now", now["open"]))
		days := []any{}
		for offset := ovDays - 1; offset >= 0; offset-- {
			day := isoDay(todayD.AddDate(0, 0, -offset))
			days = append(days, pyjson.NewObject("date", day, "created", createdDays[day],
				"completed", int64(len(completedDays[day])), "events", eventsDays[day]))
		}
		tomorrow := isoDay(todayD.AddDate(0, 0, 1))
		weekStart := isoDay(todayD.AddDate(0, 0, -6))
		prevStart := isoDay(todayD.AddDate(0, 0, -13))
		windowCount := func(start, end string) int64 {
			var n int64
			for d, v := range createdDays {
				if start <= d && d < end {
					n += v
				}
			}
			return n
		}
		windowDone := func(start, end string) int64 {
			var n int64
			for d, v := range completedDays {
				if start <= d && d < end {
					n += int64(len(v))
				}
			}
			return n
		}
		kpis.Set("created", pyjson.NewObject("week", windowCount(weekStart, tomorrow), "prev", windowCount(prevStart, weekStart)))
		kpis.Set("completed", pyjson.NewObject("week", windowDone(weekStart, tomorrow), "prev", windowDone(prevStart, weekStart)))

		for _, name := range members {
			if name == "" {
				continue
			}
			key := strings.ToLower(name)
			if load[key] == nil {
				load[key] = &bucket{username: name}
				loadOrder = append(loadOrder, key)
			}
		}
		memberSet := map[string]bool{}
		for _, m := range members {
			if m != "" {
				memberSet[strings.ToLower(m)] = true
			}
		}
		var buckets []*bucket
		for _, key := range loadOrder {
			buckets = append(buckets, load[key])
		}
		sort.SliceStable(buckets, func(i, j int) bool {
			a, b := buckets[i], buckets[j]
			if a.open != b.open {
				return a.open > b.open
			}
			if a.active != b.active {
				return a.active > b.active
			}
			return strings.ToLower(a.username) < strings.ToLower(b.username)
		})
		memberRows := []any{}
		for _, b := range buckets {
			memberRows = append(memberRows, pyjson.NewObject("username", b.username, "open", b.open, "active", b.active,
				"review", b.review, "blocked", b.blocked, "overdue", b.overdue, "member", memberSet[strings.ToLower(b.username)]))
		}
		briefs := func(rows []sqlitedb.Row) []any {
			out := []any{}
			for _, r := range rows {
				out = append(out, ovBrief(r, owners))
			}
			return out
		}
		var supported, contested, lacking int64
		var lackingRows []sqlitedb.Row
		for _, r := range claims {
			sup, ref := r.Int("sup") != 0, r.Int("ref") != 0
			if sup {
				supported++
			}
			if ref {
				contested++
			}
			if !sup && !ref {
				lacking++
				if !isClosed(r.Str("status")) {
					lackingRows = append(lackingRows, r)
				}
			}
		}
		expStatus := pyjson.NewObject()
		var expTotal int64
		for _, r := range expRows {
			expStatus.Set(pyjson.Str(r.Get("status")), r.Get("n"))
			expTotal += r.Int("n")
		}
		lackingItems := lackingRows
		if len(lackingItems) > 5 {
			lackingItems = lackingItems[:5]
		}
		research := pyjson.NewObject(
			"questions", pyjson.NewObject("total", qCounts.Int("n"), "open", qCounts.Int("o"), "latest", briefs(qLatest)),
			"claims", pyjson.NewObject("total", int64(len(claims)), "supported", supported, "contested", contested,
				"lacking", lacking, "lacking_open", int64(len(lackingRows)), "lacking_items", briefs(lackingItems)),
			"experiments", pyjson.NewObject("total", expTotal, "by_status", expStatus, "latest", briefs(expLatest)))
		activity := []any{}
		for _, r := range activityRows {
			activity = append(activity, pyjson.NewObject("id", r.Get("id"), "at", r.Get("at"), "actor", r.Get("actor"),
				"action", r.Get("action"), "unit_id", r.Get("unit_id"), "unit_uid", r.Get("uid"),
				"unit_title", r.Get("title"), "unit_type", r.Get("type"), "detail", parseDetail(r.Get("detail_json"))))
		}
		stageList := []any{}
		for _, st := range ovStages {
			c := stages[st.id]
			stageList = append(stageList, pyjson.NewObject("id", st.id, "types", st.types, "count", c["count"],
				"done", c["done"], "active", c["active"], "review", c["review"], "blocked", c["blocked"], "open", c["open"]))
		}
		blockedItems := []any{}
		for _, r := range blockedRows {
			b := ovBrief(r, owners)
			list := blockers[r.Int("id")]
			if list == nil {
				list = []any{}
			}
			blockedItems = append(blockedItems, b.Set("blockers", list))
		}
		out = pyjson.NewObject("today", today, "graph", pyjson.NewObject("nodes", nodes, "edges", edges),
			"kpis", kpis, "days", days, "stages", stageList, "members", memberRows,
			"unassigned", pyjson.NewObject("open", unassigned.open, "active", unassigned.active,
				"review", unassigned.review, "blocked", unassigned.blocked, "overdue", unassigned.overdue),
			"due", pyjson.NewObject("overdue", briefs(overdueRows), "overdue_total", overdueTotal,
				"soon", briefs(soonRows), "soon_total", soonTotal),
			"blocked", pyjson.NewObject("total", blockedTotal, "items", blockedItems),
			"research", research, "activity", activity,
			"store_ms", math.Round(float64(time.Since(started).Microseconds())/10)/100)
		return nil
	})
	return out, err
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func sortedKeys(m map[int64]bool) []int64 {
	out := make([]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
