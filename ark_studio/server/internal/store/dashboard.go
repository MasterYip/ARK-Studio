package store

import (
	"context"
	"sort"
	"strings"

	"arkstudio/go/internal/platform/clock"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/sqlitedb"
)

// NeedsMediaLimit is NEEDS_MEDIA_LIMIT.
const NeedsMediaLimit = 40

// PosterFrames is set by the server when it can make video poster frames
// (ffmpeg installed): previews then carry poster_url.
var PosterFrames bool

func httpMedia(url any) bool {
	u := strings.ToLower(pyjson.Str(orNilStr(url)))
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "data:")
}

// thumbableImage: the formats the thumbnail route decodes.
func thumbableImage(url any) bool {
	path := strings.ToLower(pyjson.Str(orNilStr(url)))
	path, _, _ = strings.Cut(path, "?")
	path, _, _ = strings.Cut(path, "#")
	return hasSuffixAny(path, []string{".png", ".jpg", ".jpeg", ".gif"})
}

var (
	previewImageExt = []string{".png", ".jpg", ".jpeg", ".webp", ".gif", ".svg", ".avif", ".bmp"}
	previewVideoExt = []string{".mp4", ".webm", ".mov", ".m4v", ".ogv"}
	evidenceIn      = []any{"supports", "validates", "part_of"}
	evidenceOut     = []any{"produces"}
	noPreview       = []any{"assigned_to", "runs_on"}
)

// mediaPreviewKind is media_preview_kind for a raw row or a media dict.
func mediaPreviewKind(kind any, url any, isImage bool) string {
	k := strings.ToLower(pyjson.Str(orNilStr(kind)))
	path := strings.ToLower(pyjson.Str(orNilStr(url)))
	path, _, _ = strings.Cut(path, "?")
	path, _, _ = strings.Cut(path, "#")
	if isImage || k == "image" || hasSuffixAny(path, previewImageExt) {
		return "image"
	}
	if k == "video" || hasSuffixAny(path, previewVideoExt) {
		return "video"
	}
	return ""
}

func orNilStr(v any) any {
	if !pyjson.Truthy(v) {
		return ""
	}
	return v
}

// Stats is Store.stats.
func (s *Store) stats(ctx context.Context, q sqlitedb.Querier) (*pyjson.Object, error) {
	group := func(query string, args ...any) (*pyjson.Object, int64, error) {
		rows, err := sqlitedb.Query(ctx, q, query, args...)
		o, total := pyjson.NewObject(), int64(0)
		for _, r := range rows {
			o.Set(pyjson.Str(r.Vals[0]), r.Get("n"))
			total += r.Int("n")
		}
		return o, total, err
	}
	byType, total, err := group("SELECT type, COUNT(*) n FROM units WHERE archived=0 GROUP BY type")
	if err != nil {
		return nil, err
	}
	byStatus, _, err := group("SELECT status, COUNT(*) n FROM units WHERE archived=0 GROUP BY status")
	if err != nil {
		return nil, err
	}
	byImportance, _, err := group("SELECT importance, COUNT(*) n FROM units WHERE archived=0 GROUP BY importance")
	if err != nil {
		return nil, err
	}
	one := func(query string, args ...any) (any, error) {
		r, _, err := sqlitedb.QueryOne(ctx, q, query, args...)
		return r.Get("n"), err
	}
	links, err := one("SELECT COUNT(*) n FROM unit_links")
	if err != nil {
		return nil, err
	}
	media, err := one("SELECT COUNT(*) n FROM unit_media")
	if err != nil {
		return nil, err
	}
	overdue, err := one(`SELECT COUNT(*) n FROM units WHERE archived=0 AND type='task'
                   AND status NOT IN ('done','abandoned','archived') AND end_at IS NOT NULL AND end_at < ?`, clock.Today())
	if err != nil {
		return nil, err
	}
	taskByStatus, taskTotal, err := group("SELECT status, COUNT(*) n FROM units WHERE archived=0 AND type='task' GROUP BY status")
	if err != nil {
		return nil, err
	}
	taskOpen := int64(0)
	for _, st := range []string{"inbox", "open", "active", "review"} {
		if v, ok := taskByStatus.Value(st).(int64); ok {
			taskOpen += v
		}
	}
	stageRows, err := sqlitedb.Query(ctx, q, `SELECT u.type, CASE WHEN u.status!='done' AND EXISTS
                       (SELECT 1 FROM unit_tags t WHERE t.unit_id=u.id AND t.tag='status:blocked')
                       THEN 'blocked' ELSE u.status END state, COUNT(*) n
                   FROM units u WHERE u.archived=0 AND u.status NOT IN ('abandoned','archived')
                   GROUP BY u.type, state`)
	if err != nil {
		return nil, err
	}
	stageMap := []struct {
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
	stages := []any{}
	for _, st := range stageMap {
		counts := map[string]int64{}
		for _, r := range stageRows {
			if !containsString(st.types, r.Str("type")) {
				continue
			}
			counts["count"] += r.Int("n")
			if state := r.Str("state"); state == "done" || state == "active" || state == "review" || state == "blocked" {
				counts[state] += r.Int("n")
			}
			if r.Str("state") != "done" {
				counts["open"] += r.Int("n")
			}
		}
		stages = append(stages, pyjson.NewObject("id", st.id, "types", st.types, "count", counts["count"],
			"done", counts["done"], "active", counts["active"], "review", counts["review"],
			"blocked", counts["blocked"], "open", counts["open"]))
	}
	return pyjson.NewObject("total", total, "by_type", byType, "by_status", byStatus, "by_importance", byImportance,
		"links", links, "media", media, "overdue", overdue,
		"tasks", pyjson.NewObject("total", taskTotal, "open", taskOpen, "by_status", taskByStatus),
		"stages", stages), nil
}

func containsString(items []string, want string) bool {
	for _, s := range items {
		if s == want {
			return true
		}
	}
	return false
}

// nextActions is Store.next_actions.
func (s *Store) nextActions(ctx context.Context, q sqlitedb.Querier) ([]any, error) {
	today := clock.Today()
	queries := []struct {
		sql  string
		args []any
	}{
		{`SELECT * FROM units WHERE archived=0 AND type='task'
                   AND status NOT IN ('done','abandoned','archived') AND end_at IS NOT NULL AND end_at < ?
                   ORDER BY end_at ASC LIMIT 3`, []any{today}},
		{"SELECT * FROM units WHERE archived=0 AND (status='blocked' OR EXISTS " +
			"(SELECT 1 FROM unit_tags t WHERE t.unit_id=units.id AND t.tag='status:blocked')) " +
			"ORDER BY updated_at ASC LIMIT 2", nil},
		{`SELECT u.* FROM units u WHERE u.archived=0 AND u.type='idea'
                   AND NOT EXISTS (SELECT 1 FROM unit_links l WHERE l.source_id=u.id OR l.target_id=u.id)
                   ORDER BY u.updated_at DESC LIMIT 2`, nil},
		{`SELECT u.* FROM units u WHERE u.archived=0 AND u.type='claim'
                   AND NOT EXISTS (SELECT 1 FROM unit_links l JOIN units s ON s.id=l.source_id
                                   WHERE l.target_id=u.id AND l.relation IN ('supports','validates'))
                   ORDER BY u.updated_at DESC LIMIT 2`, nil},
		{`SELECT u.* FROM units u WHERE u.archived=0 AND u.type='task'
                   AND u.status IN ('open','active','review')
                   AND NOT EXISTS (SELECT 1 FROM unit_links l WHERE l.source_id=u.id AND l.relation='assigned_to')
                   ORDER BY u.end_at ASC LIMIT 2`, nil},
	}
	results := make([][]sqlitedb.Row, len(queries))
	for i, qq := range queries {
		rows, err := sqlitedb.Query(ctx, q, qq.sql, qq.args...)
		if err != nil {
			return nil, err
		}
		results[i] = rows
	}
	overdue, blocked, ideas, claims, assign := results[0], results[1], results[2], results[3], results[4]
	var actions []*pyjson.Object
	add := func(rows []sqlitedb.Row, kind, severity, detail, tab string, withDue bool) {
		for _, r := range rows {
			d := detail
			if withDue {
				d = "Due " + pyjson.Str(r.Get("end_at"))
			}
			actions = append(actions, pyjson.NewObject("kind", kind, "severity", severity, "unit", unitBrief(r),
				"title", r.Get("title"), "detail", d, "target", pyjson.NewObject("tab", tab, "unit_id", r.Get("id"))))
		}
	}
	add(overdue, "overdue", "high", "", "tasks", true)
	add(blocked, "blocked", "high", "Marked blocked", "map", false)
	add(claims, "claim", "medium", "No supporting evidence linked yet", "map", false)
	add(assign, "assign", "medium", "Active task without an agent", "tasks", false)
	add(ideas, "idea", "low", "Idea has no connection yet", "ideas", false)
	out := []any{}
	seen := map[int64]bool{}
	for _, a := range actions {
		id := a.Value("unit").(*pyjson.Object).Value("id").(int64)
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, a)
		if len(out) == 8 {
			break
		}
	}
	return out, nil
}

type previews struct {
	media []any
	own   int64
}

// previewMediaBatch is Store._preview_media_batch + _preview_media.
func (s *Store) previewMediaBatch(ctx context.Context, q sqlitedb.Querier, idList []int64, limit int) (map[int64]previews, error) {
	media := map[int64][]sqlitedb.Row{}
	load := func(unitIDs []int64) error {
		for off := 0; off < len(unitIDs); off += 400 {
			batch := sqlitedb.Args(unitIDs[off:min(off+400, len(unitIDs))])
			rows, err := groupRows(ctx, q, "SELECT * FROM unit_media WHERE unit_id IN ({marks}) ORDER BY created_at DESC,id DESC", batch)
			if err != nil {
				return err
			}
			for _, r := range rows {
				media[r.Int("unit_id")] = append(media[r.Int("unit_id")], r)
			}
		}
		return nil
	}
	if err := load(idList); err != nil {
		return nil, err
	}
	var missing []int64
	for _, id := range idList {
		has := false
		for _, r := range media[id] {
			if mediaPreviewKind(r.Get("kind"), r.Get("url"), false) != "" {
				has = true
				break
			}
		}
		if !has {
			missing = append(missing, id)
		}
	}
	neighbours := map[int64][]sqlitedb.Row{}
	for off := 0; off < len(missing); off += 400 {
		batch := sqlitedb.Args(missing[off:min(off+400, len(missing))])
		marks := sqlitedb.Marks(len(batch))
		args := append(append(append(append(append(append([]any{}, evidenceIn...), batch...), noPreview...), evidenceOut...), batch...), noPreview...)
		rows, err := sqlitedb.Query(ctx, q, `SELECT unit_id,id,uid,title,type,updated_at,MIN(rank) rank FROM (
                    SELECT l.target_id unit_id,u.id,u.uid,u.title,u.type,u.updated_at,
                           CASE WHEN l.relation IN (?,?,?) THEN 0 ELSE 1 END rank
                    FROM unit_links l JOIN units u ON u.id=l.source_id
                    WHERE l.target_id IN (`+marks+`) AND u.archived=0 AND u.id!=l.target_id
                      AND l.relation NOT IN (?,?)
                    UNION ALL
                    SELECT l.source_id unit_id,u.id,u.uid,u.title,u.type,u.updated_at,
                           CASE WHEN l.relation IN (?) THEN 0 ELSE 1 END rank
                    FROM unit_links l JOIN units u ON u.id=l.target_id
                    WHERE l.source_id IN (`+marks+`) AND u.archived=0 AND u.id!=l.source_id
                      AND l.relation NOT IN (?,?)
                ) GROUP BY unit_id,id ORDER BY rank,updated_at DESC,id DESC`, args...)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			neighbours[r.Int("unit_id")] = append(neighbours[r.Int("unit_id")], r)
		}
	}
	inIDs := map[int64]bool{}
	for _, id := range idList {
		inIDs[id] = true
	}
	adjSet := map[int64]bool{}
	var adjacent []int64
	for _, rows := range neighbours {
		for _, r := range rows {
			if id := r.Int("id"); !inIDs[id] && !adjSet[id] {
				adjSet[id] = true
				adjacent = append(adjacent, id)
			}
		}
	}
	sort.Slice(adjacent, func(i, j int) bool { return adjacent[i] < adjacent[j] })
	if err := load(adjacent); err != nil {
		return nil, err
	}
	usable := func(rows []sqlitedb.Row, source *sqlitedb.Row) []*pyjson.Object {
		var out []*pyjson.Object
		for _, r := range rows {
			item := mediaDict(r)
			kind := mediaPreviewKind(item.Value("kind"), item.Value("url"), item.Value("is_image") == true)
			if kind == "" {
				continue
			}
			item.Set("preview_kind", kind)
			local := !httpMedia(item.Value("url"))
			if kind == "image" {
				// A small JPEG for list previews (server media_thumb.go) when
				// the server can make one; the original otherwise.
				thumb := item.Value("raw_url")
				if local && thumbableImage(item.Value("url")) {
					thumb = "/api/media/" + pyjson.Str(item.Value("id")) + "/thumb"
				}
				item.Set("thumb_url", thumb)
			} else {
				item.Set("thumb_url", nil)
				if local && PosterFrames {
					item.Set("poster_url", "/api/media/"+pyjson.Str(item.Value("id"))+"/thumb")
				}
			}
			if source != nil {
				item.Set("from", pyjson.NewObject("id", source.Get("id"), "uid", source.Get("uid"),
					"title", source.Get("title"), "type", source.Get("type")))
			}
			out = append(out, item)
		}
		return out
	}
	unique := func(items []*pyjson.Object) []*pyjson.Object {
		seen := map[string]bool{}
		var out []*pyjson.Object
		for _, it := range items {
			key := pyjson.Str(it.Value("url"))
			if !pyjson.Truthy(it.Value("url")) {
				key = pyjson.Str(it.Value("id"))
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, it)
		}
		return out
	}
	take := func(items []*pyjson.Object) []any {
		out := []any{}
		for i, it := range items {
			if i == limit {
				break
			}
			out = append(out, it)
		}
		return out
	}
	result := map[int64]previews{}
	for _, id := range idList {
		own := unique(usable(media[id], nil))
		if len(own) > 0 {
			result[id] = previews{take(own), int64(len(own))}
			continue
		}
		var found []*pyjson.Object
		for i := range neighbours[id] {
			n := neighbours[id][i]
			rows := append([]sqlitedb.Row(nil), media[n.Int("id")]...)
			sort.SliceStable(rows, func(a, b int) bool { return rows[a].Int("id") > rows[b].Int("id") })
			found = append(found, usable(rows, &n)...)
			if len(found) >= limit*4 {
				break
			}
		}
		sort.SliceStable(found, func(a, b int) bool {
			ca, cb := strOrEmpty(found[a].Value("created_at")), strOrEmpty(found[b].Value("created_at"))
			if ca != cb {
				return ca > cb
			}
			return found[a].Value("id").(int64) > found[b].Value("id").(int64)
		})
		result[id] = previews{take(unique(found)), 0}
	}
	return result, nil
}

// needsYou is Store.needs_you.
func (s *Store) needsYou(ctx context.Context, q sqlitedb.Querier) ([]any, error) {
	rows, err := sqlitedb.Query(ctx, q, `SELECT * FROM units WHERE archived=0
                   AND (status='review' OR (type='decision' AND status!='done'))
                   ORDER BY CASE WHEN type='decision' THEN 0 ELSE 1 END, updated_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	var owners map[int64]assigneeEntry
	if len(rows) > 0 {
		if owners, err = s.assigneeIndex(ctx, q); err != nil {
			return nil, err
		}
	}
	idList := make([]int64, len(rows))
	for i, r := range rows {
		idList[i] = r.Int("id")
	}
	counts, err := commentCounts(ctx, q, sqlitedb.Args(idList))
	if err != nil {
		return nil, err
	}
	pv, err := s.previewMediaBatch(ctx, q, idList, NeedsMediaLimit)
	if err != nil {
		return nil, err
	}
	items := []any{}
	objects := make([]*pyjson.Object, 0, len(rows))
	var fromIDs []int64
	fromItems := map[int64][]*pyjson.Object{}
	for _, r := range rows {
		id := r.Int("id")
		text, truncated := pytext.MarkdownExcerpt(r.Str("body"), 400)
		e := owners[id]
		kind := "review"
		if r.Str("type") == "decision" {
			kind = "decision"
		}
		own := r.Get("assignee")
		effective := own
		if !pyjson.Truthy(own) {
			effective = e.nameOrNil()
		}
		item := unitBrief(r)
		item.Set("updated_at", r.Get("updated_at")).Set("kind", kind).Set("excerpt", text).
			Set("excerpt_plain", pytext.Excerpt(pytext.NoteLineRe.ReplaceAllString(r.Str("body"), ""), 320)).
			Set("excerpt_truncated", truncated).Set("comments", counts[id][0]).Set("comments_open", counts[id][1]).
			Set("media", pv[id].media).Set("own_media", pv[id].own).Set("assignee", own).
			Set("assignee_effective", effective).Set("assignee_from", nil)
		if !pyjson.Truthy(own) && e.from != 0 {
			if _, ok := fromItems[e.from]; !ok {
				fromIDs = append(fromIDs, e.from)
			}
			fromItems[e.from] = append(fromItems[e.from], item)
		}
		items = append(items, item)
		objects = append(objects, item)
	}
	// Go only: review requests (reviewrounds.go) and review groups.
	if err := s.enrichReviews(ctx, q, rows, objects); err != nil {
		return nil, err
	}
	groups, err := s.reviewGroups(ctx, q, "review", true)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		o := g.(*pyjson.Object)
		o.Set("uid", o.Value("id")).Set("type", "review_group").Set("updated_at", o.Value("opened_at")).
			Set("review_since", o.Value("opened_at")).Set("review_round", o.Value("round")).
			Set("assignee", nil).Set("assignee_effective", nil).Set("assignee_from", nil)
	}
	items = append(groups, items...)
	if len(fromIDs) > 0 {
		sort.Slice(fromIDs, func(i, j int) bool { return fromIDs[i] < fromIDs[j] })
		brows, err := groupRows(ctx, q, "SELECT id, uid, title, type FROM units WHERE id IN ({marks})", sqlitedb.Args(fromIDs))
		if err != nil {
			return nil, err
		}
		briefs := map[int64]*pyjson.Object{}
		for _, b := range brows {
			briefs[b.Int("id")] = pyjson.NewObject("id", b.Get("id"), "uid", b.Get("uid"), "title", b.Get("title"), "type", b.Get("type"))
		}
		for id, list := range fromItems {
			for _, item := range list {
				if b, ok := briefs[id]; ok {
					item.Set("assignee_from", b)
				}
			}
		}
	}
	return items, nil
}

// ProjectPayload is Store.project_payload.
func (s *Store) ProjectPayload(ctx context.Context) (*pyjson.Object, error) {
	var out *pyjson.Object
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		stats, err := s.stats(ctx, q)
		if err != nil {
			return err
		}
		activity, err := activityRows(ctx, q, 12, nil)
		if err != nil {
			return err
		}
		actions, err := s.nextActions(ctx, q)
		if err != nil {
			return err
		}
		needs, err := s.needsYou(ctx, q)
		if err != nil {
			return err
		}
		manifest := s.Manifest.Snapshot()
		var updated any
		if manifest.Len() > 0 {
			updated = manifest.Value("updated")
		}
		out = pyjson.NewObject("manifest", manifest, "path", s.Path, "stats", stats, "activity", activity,
			"next_actions", actions, "needs_you", needs, "updated_at", updated)
		return nil
	})
	return out, err
}
