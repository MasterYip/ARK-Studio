package store

import (
	"context"
	"os"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/sqlitedb"
)

// CommentUserKinds is COMMENT_USER_KINDS.
var CommentUserKinds = []any{"comment", "sent_back", "decided", "accepted"}

func groupRows(ctx context.Context, q sqlitedb.Querier, query string, idList []any, extra ...any) ([]sqlitedb.Row, error) {
	if len(idList) == 0 {
		return nil, nil
	}
	return sqlitedb.Query(ctx, q, strings.Replace(query, "{marks}", sqlitedb.Marks(len(idList)), -1), append(append([]any{}, idList...), extra...)...)
}

// tagsFor is Store._tags_for.
func tagsFor(ctx context.Context, q sqlitedb.Querier, idList []any) (map[int64][]string, error) {
	out := map[int64][]string{}
	rows, err := groupRows(ctx, q, "SELECT unit_id, tag FROM unit_tags WHERE unit_id IN ({marks}) ORDER BY tag", idList)
	for _, r := range rows {
		out[r.Int("unit_id")] = append(out[r.Int("unit_id")], r.Str("tag"))
	}
	return out, err
}

// linkCounts is Store._link_counts.
func linkCounts(ctx context.Context, q sqlitedb.Querier, idList []any) (map[int64]int64, map[int64]int64, error) {
	outN, inN := map[int64]int64{}, map[int64]int64{}
	rows, err := groupRows(ctx, q, "SELECT source_id, COUNT(*) n FROM unit_links WHERE source_id IN ({marks}) GROUP BY source_id", idList)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range rows {
		outN[r.Int("source_id")] = r.Int("n")
	}
	rows, err = groupRows(ctx, q, "SELECT target_id, COUNT(*) n FROM unit_links WHERE target_id IN ({marks}) GROUP BY target_id", idList)
	for _, r := range rows {
		inN[r.Int("target_id")] = r.Int("n")
	}
	return outN, inN, err
}

// assigneeLinks is Store._assignees (assigned_to link targets).
func assigneeLinks(ctx context.Context, q sqlitedb.Querier, idList []any) (map[int64][]any, error) {
	out := map[int64][]any{}
	rows, err := groupRows(ctx, q, `SELECT l.source_id, u.id, u.uid, u.title, u.status, u.type
                FROM unit_links l JOIN units u ON u.id=l.target_id
                WHERE l.relation='assigned_to' AND l.source_id IN ({marks})`, idList)
	for _, r := range rows {
		out[r.Int("source_id")] = append(out[r.Int("source_id")], pyjson.NewObject(
			"id", r.Get("id"), "uid", r.Get("uid"), "title", r.Get("title"), "status", r.Get("status"), "type", r.Get("type")))
	}
	return out, err
}

// parents is Store._parents.
func parents(ctx context.Context, q sqlitedb.Querier, idList []any) (map[int64][]any, error) {
	out := map[int64][]any{}
	rows, err := groupRows(ctx, q, `SELECT l.source_id, u.id, u.uid, u.title, u.type, l.relation
                FROM unit_links l JOIN units u ON u.id=l.target_id
                WHERE l.relation='part_of' AND l.source_id IN ({marks})`, idList)
	for _, r := range rows {
		out[r.Int("source_id")] = append(out[r.Int("source_id")], pyjson.NewObject(
			"id", r.Get("id"), "uid", r.Get("uid"), "title", r.Get("title"), "type", r.Get("type")))
	}
	return out, err
}

var mediaImageExt = []string{".png", ".jpg", ".jpeg", ".webp", ".gif", ".svg", ".avif"}

func hasSuffixAny(s string, suffixes []string) bool {
	for _, suf := range suffixes {
		if strings.HasSuffix(s, suf) {
			return true
		}
	}
	return false
}

// mediaDict is Store._media_dict.
func mediaDict(r sqlitedb.Row) *pyjson.Object {
	url := r.Str("url")
	lower, _, _ := strings.Cut(strings.ToLower(url), "?")
	return pyjson.NewObject("id", r.Get("id"), "url", r.Get("url"), "kind", r.Get("kind"),
		"caption", r.Get("caption"), "created_at", r.Get("created_at"),
		"is_image", hasSuffixAny(lower, mediaImageExt), "raw_url", "/api/media/"+pyjson.Str(r.Get("id"))+"/raw")
}

// mediaPreview is Store._media_preview.
func mediaPreview(ctx context.Context, q sqlitedb.Querier, idList []any) (map[int64][]any, error) {
	out := map[int64][]any{}
	rows, err := groupRows(ctx, q, "SELECT * FROM unit_media WHERE unit_id IN ({marks}) ORDER BY id", idList)
	for _, r := range rows {
		out[r.Int("unit_id")] = append(out[r.Int("unit_id")], mediaDict(r))
	}
	return out, err
}

// commentCounts is Store._comment_counts: (user comments, unresolved).
func commentCounts(ctx context.Context, q sqlitedb.Querier, idList []any) (map[int64][2]int64, error) {
	out := map[int64][2]int64{}
	rows, err := groupRows(ctx, q, `SELECT unit_id, COUNT(*) n, SUM(CASE WHEN resolved_at IS NULL THEN 1 ELSE 0 END) open
                FROM unit_comments WHERE unit_id IN ({marks}) AND kind IN (?,?,?,?)
                GROUP BY unit_id`, idList, CommentUserKinds...)
	for _, r := range rows {
		out[r.Int("unit_id")] = [2]int64{r.Int("n"), r.Int("open")}
	}
	return out, err
}

func orEmpty(v []any) []any {
	if v == nil {
		return []any{}
	}
	return v
}

// serialize is Store._serialize.
func serialize(r sqlitedb.Row, tags []string, outN, inN int64, par, media, assignees []any,
	includeBody bool, effName any, effFrom any, comments [2]int64) *pyjson.Object {
	own := r.Get("assignee")
	effective := own
	if !pyjson.Truthy(own) {
		effective = effName
	}
	var from any
	if !pyjson.Truthy(own) {
		from = effFrom
	}
	if tags == nil {
		tags = []string{}
	}
	o := pyjson.NewObject(
		"id", r.Get("id"), "uid", r.Get("uid"), "type", r.Get("type"), "title", r.Get("title"),
		"status", r.Get("status"), "importance", r.Get("importance"), "start_at", r.Get("start_at"),
		"end_at", r.Get("end_at"), "created_at", r.Get("created_at"), "updated_at", r.Get("updated_at"),
		"origin", r.Get("origin"), "source_ref", r.Get("source_ref"), "source_title", r.Get("source_title"),
		"meta", parseDetail(r.Get("meta_json")), "archived", pyjson.Truthy(r.Get("archived")),
		"tags", tags, "out_count", outN, "in_count", inN, "parents", orEmpty(par),
		"media", orEmpty(media), "assignees", orEmpty(assignees),
		"assignee", own, "assignee_effective", effective, "assignee_from", from,
		"comments_count", comments[0], "comments_open", comments[1])
	if includeBody {
		o.Set("body", r.Get("body"))
	} else {
		o.Set("excerpt", pytext.Excerpt(r.Str("body"), 240))
	}
	return o
}

// hydrate is Store.hydrate.
// related holds the per-unit inputs of serialize().
type related struct {
	tags           map[int64][]string
	outN, inN      map[int64]int64
	parents, media map[int64][]any
	assignees      map[int64][]any
	comments       map[int64][2]int64
}

func (s *Store) hydrate(ctx context.Context, q sqlitedb.Querier, rows []sqlitedb.Row, includeBody bool) ([]*pyjson.Object, error) {
	rel, err := relatedFor(ctx, q, ids(rows))
	if err != nil {
		return nil, err
	}
	return s.hydrateWith(ctx, q, rows, includeBody, rel)
}

// relatedFor runs Store.hydrate's batched IN queries.
func relatedFor(ctx context.Context, q sqlitedb.Querier, idList []any) (*related, error) {
	var r related
	var err error
	if r.tags, err = tagsFor(ctx, q, idList); err != nil {
		return nil, err
	}
	if r.outN, r.inN, err = linkCounts(ctx, q, idList); err != nil {
		return nil, err
	}
	if r.parents, err = parents(ctx, q, idList); err != nil {
		return nil, err
	}
	if r.media, err = mediaPreview(ctx, q, idList); err != nil {
		return nil, err
	}
	if r.assignees, err = assigneeLinks(ctx, q, idList); err != nil {
		return nil, err
	}
	if r.comments, err = commentCounts(ctx, q, idList); err != nil {
		return nil, err
	}
	return &r, nil
}

// relatedAllLive is relatedFor(every live unit) with one query per table
// instead of batches of 400 ids.  Per-unit orders equal the IN queries':
// tags by tag, media by id, part_of/assigned_to rows through the
// UNIQUE(source_id, target_id, relation) index (target, then relation).
func relatedAllLive(ctx context.Context, q sqlitedb.Querier) (*related, error) {
	r := related{tags: map[int64][]string{}, outN: map[int64]int64{}, inN: map[int64]int64{},
		parents: map[int64][]any{}, media: map[int64][]any{}, assignees: map[int64][]any{}, comments: map[int64][2]int64{}}
	live := "SELECT id FROM units WHERE archived=0"
	rows, err := sqlitedb.Query(ctx, q, "SELECT unit_id, tag FROM unit_tags WHERE unit_id IN ("+live+") ORDER BY unit_id, tag")
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		r.tags[row.Int("unit_id")] = append(r.tags[row.Int("unit_id")], row.Str("tag"))
	}
	if rows, err = sqlitedb.Query(ctx, q, "SELECT source_id, COUNT(*) n FROM unit_links WHERE source_id IN ("+live+") GROUP BY source_id"); err != nil {
		return nil, err
	}
	for _, row := range rows {
		r.outN[row.Int("source_id")] = row.Int("n")
	}
	if rows, err = sqlitedb.Query(ctx, q, "SELECT target_id, COUNT(*) n FROM unit_links WHERE target_id IN ("+live+") GROUP BY target_id"); err != nil {
		return nil, err
	}
	for _, row := range rows {
		r.inN[row.Int("target_id")] = row.Int("n")
	}
	if rows, err = sqlitedb.Query(ctx, q, `SELECT l.source_id, l.relation, u.id, u.uid, u.title, u.type, u.status
                FROM unit_links l JOIN units u ON u.id=l.target_id
                WHERE l.relation IN ('part_of','assigned_to') AND l.source_id IN (`+live+`)
                ORDER BY l.source_id, l.target_id, l.relation`); err != nil {
		return nil, err
	}
	for _, row := range rows {
		src := row.Int("source_id")
		if row.Str("relation") == "part_of" {
			r.parents[src] = append(r.parents[src], pyjson.NewObject("id", row.Get("id"), "uid", row.Get("uid"),
				"title", row.Get("title"), "type", row.Get("type")))
		} else {
			r.assignees[src] = append(r.assignees[src], pyjson.NewObject("id", row.Get("id"), "uid", row.Get("uid"),
				"title", row.Get("title"), "status", row.Get("status"), "type", row.Get("type")))
		}
	}
	if rows, err = sqlitedb.Query(ctx, q, "SELECT * FROM unit_media WHERE unit_id IN ("+live+") ORDER BY unit_id, id"); err != nil {
		return nil, err
	}
	for _, row := range rows {
		r.media[row.Int("unit_id")] = append(r.media[row.Int("unit_id")], mediaDict(row))
	}
	if rows, err = sqlitedb.Query(ctx, q, `SELECT unit_id, COUNT(*) n, SUM(CASE WHEN resolved_at IS NULL THEN 1 ELSE 0 END) open
                FROM unit_comments WHERE unit_id IN (`+live+`) AND kind IN (?,?,?,?)
                GROUP BY unit_id`, CommentUserKinds...); err != nil {
		return nil, err
	}
	for _, row := range rows {
		r.comments[row.Int("unit_id")] = [2]int64{row.Int("n"), row.Int("open")}
	}
	return &r, nil
}

func (s *Store) hydrateWith(ctx context.Context, q sqlitedb.Querier, rows []sqlitedb.Row, includeBody bool, rel *related) ([]*pyjson.Object, error) {
	idList := ids(rows)
	tags, outN, inN, par, media, assignees, comments := rel.tags, rel.outN, rel.inN, rel.parents, rel.media, rel.assignees, rel.comments
	var err error
	index, err := s.assigneeIndex(ctx, q)
	if err != nil {
		return nil, err
	}
	sourceSet := map[int64]bool{}
	var sources []any
	for _, id := range idList {
		if e, ok := index[id.(int64)]; ok && e.from != 0 && !sourceSet[e.from] {
			sourceSet[e.from] = true
			sources = append(sources, e.from)
		}
	}
	briefs := map[int64]*pyjson.Object{}
	if len(sources) > 0 {
		brows, err := groupRows(ctx, q, "SELECT id, uid, title, type FROM units WHERE id IN ({marks})", sources)
		if err != nil {
			return nil, err
		}
		for _, b := range brows {
			briefs[b.Int("id")] = pyjson.NewObject("id", b.Get("id"), "uid", b.Get("uid"), "title", b.Get("title"), "type", b.Get("type"))
		}
	}
	var topicIDs []int64
	for _, r := range rows {
		if r.Str("type") == "topic" {
			topicIDs = append(topicIDs, r.Int("id"))
		}
	}
	memberCounts, err := s.memberCounts(ctx, q, topicIDs)
	if err != nil {
		return nil, err
	}
	out := make([]*pyjson.Object, len(rows))
	for i, r := range rows {
		id := r.Int("id")
		e := index[id]
		var effFrom any
		if e.from != 0 {
			if b, ok := briefs[e.from]; ok {
				effFrom = b
			}
		}
		out[i] = serialize(r, tags[id], outN[id], inN[id], par[id], media[id], assignees[id],
			includeBody, e.nameOrNil(), effFrom, comments[id])
		if r.Str("type") == "topic" {
			out[i].Set("member_count", memberCounts[id])
		}
	}
	return out, nil
}

// ---------------------------------------------------------- assignee index

type assigneeEntry struct {
	name    string
	hasName bool
	from    int64 // 0 = none
}

func (e assigneeEntry) nameOrNil() any {
	if e.hasName {
		return e.name
	}
	return nil
}

type assigneeCache struct {
	key   string
	index map[int64]assigneeEntry
	order []int64 // units order (Python dict order)
}

// CacheKey changes whenever the database may have changed.
func (s *Store) CacheKey() string { return s.cacheKey() }

func (s *Store) cacheKey() string {
	// Python keys on total_changes + PRAGMA data_version.  The Go equivalent
	// is this process's write generation plus the WAL file identity, which
	// changes whenever any process commits.
	key := pyjson.Str(int64(s.DB.WriteGeneration()))
	if fi, err := os.Stat(s.DB.Path + "-wal"); err == nil {
		key += ":" + fi.ModTime().String() + ":" + pyjson.Str(fi.Size())
	}
	if fi, err := os.Stat(s.DB.Path); err == nil {
		key += ":" + fi.ModTime().String()
	}
	return key
}

// assigneeIndex is Store._assignee_index: the effective assignee of every
// unit via nearest assigned part_of ancestor (BFS, older link wins ties).
func (s *Store) assigneeIndex(ctx context.Context, q sqlitedb.Querier) (map[int64]assigneeEntry, error) {
	c, err := s.assigneeIndexCache(ctx, q)
	if err != nil {
		return nil, err
	}
	return c.index, nil
}

func (s *Store) assigneeIndexCache(ctx context.Context, q sqlitedb.Querier) (*assigneeCache, error) {
	key := s.cacheKey()
	if c := s.assignee.Load(); c != nil && c.key == key {
		return c, nil
	}
	units, err := sqlitedb.Query(ctx, q, "SELECT id, assignee FROM units")
	if err != nil {
		return nil, err
	}
	index := make(map[int64]assigneeEntry, len(units))
	order := make([]int64, len(units))
	own := map[int64]string{}
	var ownOrder []int64
	for i, r := range units {
		id := r.Int("id")
		order[i] = id
		index[id] = assigneeEntry{}
		if pyjson.Truthy(r.Get("assignee")) {
			own[id] = pyjson.Str(r.Get("assignee"))
			ownOrder = append(ownOrder, id)
		}
	}
	if len(own) > 0 {
		links, err := sqlitedb.Query(ctx, q, "SELECT source_id, target_id FROM unit_links WHERE relation='part_of' ORDER BY id")
		if err != nil {
			return nil, err
		}
		par := map[int64][]int64{}
		children := map[int64][]int64{}
		for _, l := range links {
			par[l.Int("source_id")] = append(par[l.Int("source_id")], l.Int("target_id"))
			children[l.Int("target_id")] = append(children[l.Int("target_id")], l.Int("source_id"))
		}
		distance := map[int64]int{}
		queue := append([]int64(nil), ownOrder...)
		for _, id := range ownOrder {
			distance[id] = 0
		}
		var ordered []int64
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			ordered = append(ordered, id)
			for _, child := range children[id] {
				if _, seen := distance[child]; !seen {
					distance[child] = distance[id] + 1
					queue = append(queue, child)
				}
			}
		}
		for _, id := range ordered {
			if name, ok := own[id]; ok {
				index[id] = assigneeEntry{name: name, hasName: true}
				continue
			}
			for _, p := range par[id] {
				if d, ok := distance[p]; ok && d == distance[id]-1 {
					e := index[p]
					from := e.from
					if from == 0 {
						from = p
					}
					index[id] = assigneeEntry{name: e.name, hasName: e.hasName, from: from}
					break
				}
			}
		}
	}
	c := &assigneeCache{key: key, index: index, order: order}
	s.assignee.Store(c)
	return c, nil
}
