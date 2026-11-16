package store

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pytext"
	"arkstudio/go/internal/sqlitedb"
)

const maxTopicMembers = 400

// GetUnit is Store.get_unit(ref) with activity.
func (s *Store) GetUnit(ctx context.Context, ref string) (*pyjson.Object, error) {
	var out *pyjson.Object
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		row, err := unitRow(ctx, q, ref)
		if err != nil {
			return err
		}
		items, err := s.hydrate(ctx, q, []sqlitedb.Row{row}, true)
		if err != nil {
			return err
		}
		out = items[0]
		id := row.Int("id")
		linksOut, err := linksOf(ctx, q, id, true)
		if err != nil {
			return err
		}
		linksIn, err := linksOf(ctx, q, id, false)
		if err != nil {
			return err
		}
		comments, err := commentsOf(ctx, q, id)
		if err != nil {
			return err
		}
		topics, err := s.topicsOf(ctx, q, id)
		if err != nil {
			return err
		}
		activity, err := activityRows(ctx, q, 40, &id)
		if err != nil {
			return err
		}
		out.Set("links_out", linksOut).Set("links_in", linksIn).Set("comments", comments).
			Set("topics", topics).Set("activity", activity).Set("raw_url", "/api/units/"+pyjson.Str(id))
		return nil
	})
	return out, err
}

// linksOf is Store._links_of.
func linksOf(ctx context.Context, q sqlitedb.Querier, unitID int64, outgoing bool) ([]any, error) {
	query := `SELECT l.*, u.uid, u.title, u.type, u.status, u.importance
                   FROM unit_links l JOIN units u ON u.id=l.target_id
                   WHERE l.source_id=? ORDER BY l.id`
	key, other := "target", "target_id"
	if !outgoing {
		query = `SELECT l.*, u.uid, u.title, u.type, u.status, u.importance
                   FROM unit_links l JOIN units u ON u.id=l.source_id
                   WHERE l.target_id=? ORDER BY l.id`
		key, other = "source", "source_id"
	}
	rows, err := sqlitedb.Query(ctx, q, query, unitID)
	out := []any{}
	for _, r := range rows {
		out = append(out, pyjson.NewObject("id", r.Get("id"), "relation", r.Get("relation"), "note", r.Get("note"),
			"created_at", r.Get("created_at"), "created_by", r.Get("created_by"),
			key, pyjson.NewObject("id", r.Get(other), "uid", r.Get("uid"), "title", r.Get("title"),
				"type", r.Get("type"), "status", r.Get("status"), "importance", r.Get("importance"))))
	}
	return out, err
}

// commentDict is Store._comment_dict.
func commentDict(r sqlitedb.Row) *pyjson.Object {
	authorKind := "user"
	if r.Str("kind") == "agent_reply" {
		authorKind = "agent"
	}
	return pyjson.NewObject("id", r.Get("id"), "unit_id", r.Get("unit_id"), "author", r.Get("author"),
		"author_kind", authorKind, "kind", r.Get("kind"), "body", r.Get("body"),
		"created_at", r.Get("created_at"), "reply_to", r.Get("reply_to"),
		"resolved", pyjson.Truthy(r.Get("resolved_at")), "resolved_at", r.Get("resolved_at"),
		"resolved_by", r.Get("resolved_by"))
}

func commentsOf(ctx context.Context, q sqlitedb.Querier, unitID int64) ([]any, error) {
	rows, err := sqlitedb.Query(ctx, q, "SELECT * FROM unit_comments WHERE unit_id=? ORDER BY created_at, id", unitID)
	out := []any{}
	for _, r := range rows {
		out = append(out, commentDict(r))
	}
	return out, err
}

// Comments is Store.comments(ref).
func (s *Store) Comments(ctx context.Context, ref string) ([]any, error) {
	var out []any
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		row, err := unitRow(ctx, q, ref)
		if err != nil {
			return err
		}
		out, err = commentsOf(ctx, q, row.Int("id"))
		return err
	})
	return out, err
}

// ------------------------------------------------------------ activity

func activityRows(ctx context.Context, q sqlitedb.Querier, limit int64, unitID *int64) ([]any, error) {
	limit = clamp(limit, 1, 400)
	var rows []sqlitedb.Row
	var err error
	if unitID == nil {
		rows, err = sqlitedb.Query(ctx, q, `SELECT a.*, u.uid, u.title, u.type FROM activity a
                       LEFT JOIN units u ON u.id=a.unit_id ORDER BY a.id DESC LIMIT ?`, limit)
	} else {
		rows, err = sqlitedb.Query(ctx, q, `SELECT a.*, u.uid, u.title, u.type FROM activity a
                       LEFT JOIN units u ON u.id=a.unit_id WHERE a.unit_id=?
                       ORDER BY a.id DESC LIMIT ?`, *unitID, limit)
	}
	out := []any{}
	for _, r := range rows {
		out = append(out, pyjson.NewObject("id", r.Get("id"), "at", r.Get("at"), "actor", r.Get("actor"),
			"action", r.Get("action"), "unit_id", r.Get("unit_id"), "unit_uid", r.Get("uid"),
			"unit_title", r.Get("title"), "unit_type", r.Get("type"), "detail", parseDetail(r.Get("detail_json"))))
	}
	return out, err
}

// Activity is Store.activity(limit).
func (s *Store) Activity(ctx context.Context, limit int64) ([]any, error) {
	var out []any
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		var err error
		out, err = activityRows(ctx, q, limit, nil)
		return err
	})
	return out, err
}

// -------------------------------------------------------------- graph

// GraphSnapshot is Store.graph_snapshot (the uncached reference build).
func (s *Store) GraphSnapshot(ctx context.Context, bodies bool) (*pyjson.Object, error) {
	var out *pyjson.Object
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		cursor, _, err := changeCursor(ctx, q)
		if err != nil {
			return err
		}
		rows, err := sqlitedb.Query(ctx, q, "SELECT * FROM units WHERE archived=0 ORDER BY updated_at DESC, id DESC")
		if err != nil {
			return err
		}
		nodes := make([]*pyjson.Object, 0, len(rows))
		rel, err := relatedAllLive(ctx, q)
		if err != nil {
			return err
		}
		for off := 0; off < len(rows); off += 400 {
			batch := rows[off:min(off+400, len(rows))]
			hydrated, err := s.hydrateWith(ctx, q, batch, false, rel)
			if err != nil {
				return err
			}
			for i, node := range hydrated {
				if bodies {
					node.Set("search_body", batch[i].Get("body"))
				}
				node.Set("body_truncated", pytext.Len(batch[i].Str("body")) > 240)
			}
			nodes = append(nodes, hydrated...)
		}
		edgeRows, err := sqlitedb.Query(ctx, q, "SELECT l.id, l.source_id AS source, l.target_id AS target, l.relation, l.note "+
			"FROM unit_links l JOIN units s ON s.id=l.source_id JOIN units t ON t.id=l.target_id "+
			"WHERE s.archived=0 AND t.archived=0 ORDER BY l.id")
		if err != nil {
			return err
		}
		out = pyjson.NewObject("nodes", nodes, "edges", rowObjects(edgeRows), "total", int64(len(nodes)),
			"cursor", cursor, "bodies", bodies)
		return nil
	})
	return out, err
}

func rowObjects(rows []sqlitedb.Row) []any {
	out := make([]any, len(rows))
	for i, r := range rows {
		o := pyjson.NewObject()
		for j, c := range r.Cols {
			o.Set(c, r.Vals[j])
		}
		out[i] = o
	}
	return out
}

// Graph is Store.graph(filters, context=..., limit=..., slim=True).
func (s *Store) Graph(ctx context.Context, f Filters, withContext bool, limit int64) (*pyjson.Object, error) {
	var out *pyjson.Object
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		var err error
		out, err = s.graph(ctx, q, f, withContext, limit, true)
		return err
	})
	return out, err
}

// graph is Store.graph(filters, context=..., limit=..., slim=...).
func (s *Store) graph(ctx context.Context, q sqlitedb.Querier, f Filters, withContext bool, limit int64, slim bool) (*pyjson.Object, error) {
	var out *pyjson.Object
	err := func() error {
		f.Limit, f.Offset = limit, 0
		base, err := s.listUnits(ctx, q, f, false)
		if err != nil {
			return err
		}
		nodes := base.Value("items").([]*pyjson.Object)
		var selected []any
		seen := map[int64]bool{}
		for _, n := range nodes {
			id := n.Value("id").(int64)
			if !seen[id] {
				seen[id] = true
				selected = append(selected, id)
			}
		}
		var extra []*pyjson.Object
		if withContext && len(selected) > 0 {
			marks := sqlitedb.Marks(len(selected))
			rows, err := sqlitedb.Query(ctx, q, `SELECT DISTINCT u.* FROM units u
                    WHERE u.archived=0 AND u.id NOT IN (`+marks+`)
                    AND (u.id IN (SELECT target_id FROM unit_links WHERE source_id IN (`+marks+`))
                      OR u.id IN (SELECT source_id FROM unit_links WHERE target_id IN (`+marks+`)))
                    LIMIT 120`, append(append(append([]any{}, selected...), selected...), selected...)...)
			if err != nil {
				return err
			}
			if extra, err = s.hydrate(ctx, q, rows, true); err != nil {
				return err
			}
			for _, n := range extra {
				n.Set("context", true)
			}
			nodes = append(append([]*pyjson.Object{}, nodes...), extra...)
		}
		var all []any
		allSet := map[int64]bool{}
		for _, n := range nodes {
			id := n.Value("id").(int64)
			if !allSet[id] {
				allSet[id] = true
				all = append(all, id)
			}
		}
		edges := []any{}
		degree := map[int64]int64{}
		if len(all) > 0 {
			marks := sqlitedb.Marks(len(all))
			rows, err := sqlitedb.Query(ctx, q, `SELECT l.*, s.uid suid, t.uid tuid
                    FROM unit_links l JOIN units s ON s.id=l.source_id JOIN units t ON t.id=l.target_id
                    WHERE l.source_id IN (`+marks+`) AND l.target_id IN (`+marks+`)
                    ORDER BY l.id`, append(append([]any{}, all...), all...)...)
			if err != nil {
				return err
			}
			for _, r := range rows {
				edges = append(edges, pyjson.NewObject("id", r.Get("id"), "source", r.Get("source_id"),
					"target", r.Get("target_id"), "relation", r.Get("relation"), "note", r.Get("note")))
				degree[r.Int("source_id")]++
				degree[r.Int("target_id")]++
			}
		}
		if slim {
			for _, n := range nodes {
				body := strOrEmpty(n.Delete("body"))
				n.Set("excerpt", pytext.Excerpt(body, 240))
				n.Set("body_truncated", pytext.Len(body) > 240)
			}
		}
		for _, n := range nodes {
			n.Set("degree", degree[n.Value("id").(int64)])
		}
		out = pyjson.NewObject("nodes", nodes, "edges", edges, "context_count", int64(len(extra)), "total", base.Value("total"))
		return nil
	}()
	return out, err
}

// Timeline is Store.timeline.
func (s *Store) Timeline(ctx context.Context, f Filters) (*pyjson.Object, error) {
	f.HasDates = true
	f.Limit = 1000
	data, err := s.ListUnits(ctx, f, false)
	if err != nil {
		return nil, err
	}
	var items []*pyjson.Object
	for _, u := range data.Value("items").([]*pyjson.Object) {
		if pyjson.Truthy(u.Value("start_at")) || pyjson.Truthy(u.Value("end_at")) {
			items = append(items, u)
		}
	}
	key := func(u *pyjson.Object) string {
		for _, k := range []string{"start_at", "end_at"} {
			if v := u.Value(k); pyjson.Truthy(v) {
				return pyjson.Str(v)
			}
		}
		return ""
	}
	sort.SliceStable(items, func(i, j int) bool {
		ki, kj := key(items[i]), key(items[j])
		if ki != kj {
			return ki < kj
		}
		return pyjson.Str(items[i].Value("title")) < pyjson.Str(items[j].Value("title"))
	})
	if items == nil {
		items = []*pyjson.Object{}
	}
	return pyjson.NewObject("items", items, "total", int64(len(items))), nil
}

// Revision is Store.revision.
func (s *Store) Revision(ctx context.Context) (string, error) {
	var out string
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		r, _, err := sqlitedb.QueryOne(ctx, q, "SELECT (SELECT COUNT(*) FROM units) units, "+
			"(SELECT COALESCE(MAX(updated_at),'') FROM units) unit_time, "+
			"(SELECT COUNT(*) FROM unit_links) links, "+
			"(SELECT COALESCE(MAX(id),0) FROM unit_links) link_id, "+
			"(SELECT COUNT(*) FROM unit_media) media, "+
			"(SELECT COUNT(*) FROM unit_comments WHERE resolved_at IS NULL) comments_open, "+
			"(SELECT COALESCE(MAX(id),0) FROM activity) activity, "+
			"(SELECT COUNT(*) FROM topic_members) members, "+
			"(SELECT COALESCE(MAX(updated_at),'') FROM topic_members) member_time, "+
			"(SELECT COALESCE(MAX(n),0) FROM change_counter) changes")
		if err != nil {
			return err
		}
		settingsTime := int64(0)
		if fi, err := os.Stat(filepath.Join(s.Path, "project.json")); err == nil {
			settingsTime = fi.ModTime().UnixNano()
		}
		parts := []string{pyjson.Str(r.Get("units")), pyjson.Str(r.Get("unit_time")), pyjson.Str(r.Get("links")),
			pyjson.Str(r.Get("link_id")), pyjson.Str(r.Get("media")), pyjson.Str(r.Get("comments_open")),
			pyjson.Str(r.Get("activity")), pyjson.Str(settingsTime), pyjson.Str(r.Get("members")), pyjson.Str(r.Get("member_time")),
			pyjson.Str(r.Get("changes"))}
		out = strings.Join(parts, ":")
		return nil
	})
	return out, err
}
