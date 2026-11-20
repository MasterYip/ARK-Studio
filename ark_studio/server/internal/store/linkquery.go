package store

// Link lookups for the agent API (Go only): list one unit's links and find
// the links between two units, so an agent can delete or retype them through
// DeleteLink / UpdateLink (routes_agentlinks.go).

import (
	"context"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/sqlitedb"
)

// LinkEnd is one end of a link: the unit's id, uid and title.
type LinkEnd struct {
	ID    int64
	UID   string
	Title string
}

func linkEnd(r sqlitedb.Row) LinkEnd {
	return LinkEnd{ID: r.Int("id"), UID: r.Str("uid"), Title: r.Str("title")}
}

// IsKnownRelation reports whether relation is one of Relations.
func IsKnownRelation(relation string) bool { return relationSet[relation] }

// UnitLinks lists the links of one unit, outgoing first, each with its
// direction ("out": the unit is the source, "in": it is the target) and the
// unit at the other end.
func (s *Store) UnitLinks(ctx context.Context, ref string) (LinkEnd, []any, error) {
	var unit LinkEnd
	links := []any{}
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		row, err := unitRow(ctx, q, ref)
		if err != nil {
			return err
		}
		unit = linkEnd(row)
		for _, outgoing := range []bool{true, false} {
			items, err := linksOf(ctx, q, unit.ID, outgoing)
			if err != nil {
				return err
			}
			direction, key := "out", "target"
			if !outgoing {
				direction, key = "in", "source"
			}
			for _, item := range items {
				link := item.(*pyjson.Object)
				other, _ := link.Value(key).(*pyjson.Object)
				source, target := unit.UID, pyjson.Str(other.Value("uid"))
				if !outgoing {
					source, target = target, source
				}
				links = append(links, pyjson.NewObject("id", link.Value("id"), "direction", direction,
					"relation", link.Value("relation"), "source", source, "target", target,
					"note", link.Value("note"), "created_at", link.Value("created_at"), "created_by", link.Value("created_by"),
					"other", other))
			}
		}
		return nil
	})
	return unit, links, err
}

// LinksBetween returns the two units and the links from source to target
// (forward) and from target to source (reverse), each as {id, relation,
// source, target}, oldest first.
func (s *Store) LinksBetween(ctx context.Context, source, target any) (src, dst LinkEnd, forward, reverse []*pyjson.Object, err error) {
	err = s.read(ctx, func(q sqlitedb.Querier) error {
		a, err := unitRowAny(ctx, q, source)
		if err != nil {
			return err
		}
		b, err := unitRowAny(ctx, q, target)
		if err != nil {
			return err
		}
		src, dst = linkEnd(a), linkEnd(b)
		pair := func(from, to LinkEnd) ([]*pyjson.Object, error) {
			rows, err := sqlitedb.Query(ctx, q, "SELECT id, relation FROM unit_links WHERE source_id=? AND target_id=? ORDER BY id",
				from.ID, to.ID)
			out := []*pyjson.Object{}
			for _, r := range rows {
				out = append(out, pyjson.NewObject("id", r.Get("id"), "relation", r.Get("relation"),
					"source", from.UID, "target", to.UID))
			}
			return out, err
		}
		if forward, err = pair(src, dst); err != nil {
			return err
		}
		reverse, err = pair(dst, src)
		return err
	})
	return
}

// Link returns one link as {id, relation, source, target, note} with the
// source and target uids; ok is false when there is no such link.
func (s *Store) Link(ctx context.Context, linkID int64) (*pyjson.Object, bool, error) {
	var out *pyjson.Object
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		row, ok, err := sqlitedb.QueryOne(ctx, q, `SELECT l.id, l.relation, l.note, s.uid suid, t.uid tuid
                   FROM unit_links l JOIN units s ON s.id=l.source_id JOIN units t ON t.id=l.target_id
                   WHERE l.id=?`, linkID)
		if err != nil || !ok {
			return err
		}
		out = pyjson.NewObject("id", row.Get("id"), "relation", row.Get("relation"), "source", row.Get("suid"),
			"target", row.Get("tuid"), "note", row.Get("note"))
		return nil
	})
	return out, out != nil, err
}
