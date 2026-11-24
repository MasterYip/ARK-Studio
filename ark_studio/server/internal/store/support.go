package store

import (
	"context"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/sqlitedb"
)

// Reads of the Board and Home support panel (GET /api/project/support, Go
// only, 2026-11-24): the agents that wrote through the agent API recently
// and the review rounds that have waited long.  Both are bounded index
// reads (activity_at, review_rounds by unit).

// SupportAgents lists the agent actors ("agent:...") that wrote since
// `since` (ISO), latest first, at most `limit`: writes in the window, the
// last write, the unit of the agent's last write that names one, and the
// ARK session of its last write that carried one ("session", "session_via";
// WithActivitySession).
func (s *Store) SupportAgents(ctx context.Context, since string, limit int) ([]*pyjson.Object, error) {
	out := []*pyjson.Object{}
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		rows, err := sqlitedb.Query(ctx, q, `SELECT a.actor, COUNT(*) n, MAX(a.at) last,
              (SELECT b.unit_id FROM activity b WHERE b.actor = a.actor AND b.at >= ? AND b.unit_id IS NOT NULL
                ORDER BY b.id DESC LIMIT 1) unit,
              (SELECT json_extract(c.detail_json, '$.session') || ' ' || COALESCE(json_extract(c.detail_json, '$.session_via'), '')
                 FROM activity c WHERE c.actor = a.actor AND c.at >= ? AND c.detail_json LIKE '%"session_via"%'
                ORDER BY c.id DESC LIMIT 1) session
            FROM activity a WHERE a.actor LIKE 'agent:%' AND a.at >= ?
            GROUP BY a.actor ORDER BY last DESC, a.actor LIMIT ?`, since, since, since, limit)
		if err != nil {
			return err
		}
		for _, r := range rows {
			item := pyjson.NewObject("actor", r.Get("actor"), "writes", r.Get("n"), "last_at", r.Get("last"),
				"unit_uid", nil, "unit_title", nil, "session_id", nil, "session_via", nil)
			if sv, ok := r.Get("session").(string); ok {
				if id, via, _ := strings.Cut(strings.TrimSpace(sv), " "); id != "" {
					item.Set("session_id", id).Set("session_via", orNone(via))
				}
			}
			if id, ok := r.Get("unit").(int64); ok {
				u, found, err := sqlitedb.QueryOne(ctx, q, "SELECT uid, title FROM units WHERE id=? AND archived=0", id)
				if err != nil {
					return err
				}
				if found {
					item.Set("unit_uid", u.Get("uid")).Set("unit_title", u.Get("title"))
				}
			}
			out = append(out, item)
		}
		return nil
	})
	return out, err
}

// SupportReviewWaits lists the open review rounds opened before `before`
// (ISO) on units still in review, oldest first, at most `limit`.  A project
// without review tables has none.
func (s *Store) SupportReviewWaits(ctx context.Context, before string, limit int) ([]*pyjson.Object, error) {
	out := []*pyjson.Object{}
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		ok, err := hasReviewTables(ctx, q)
		if err != nil || !ok {
			return err
		}
		rows, err := sqlitedb.Query(ctx, q, `SELECT u.uid, u.title, u.type, r.round, r.opened_at, r.opened_by
            FROM review_rounds r JOIN units u ON u.id = r.unit_id
            WHERE r.closed_at IS NULL AND u.status = 'review' AND u.archived = 0 AND r.opened_at < ?
            ORDER BY r.opened_at, r.id LIMIT ?`, before, limit)
		if err != nil {
			return err
		}
		for _, r := range rows {
			out = append(out, pyjson.NewObject("uid", r.Get("uid"), "title", r.Get("title"), "type", r.Get("type"),
				"round", r.Get("round"), "since", r.Get("opened_at"), "by", r.Get("opened_by")))
		}
		return nil
	})
	return out, err
}

func orNone(s string) any {
	if s == "" {
		return nil
	}
	return s
}
