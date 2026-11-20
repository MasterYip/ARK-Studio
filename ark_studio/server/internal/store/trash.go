package store

// Recycle bin (Go only).  A deleted node is a unit with archived=1: every
// view hides it and DELETE /api/units/{id} or POST .../archive puts it there.
// The bin lists those units with the time and actor of their last
// unit.archive activity row; RestoreUnit brings one back together with the
// links ArchiveUnit removed (still valid ones only); PurgeUnit and EmptyTrash
// delete for good.  The status "archived" is unrelated: an archived-status
// unit is live and shows in views.

import (
	"context"
	"sort"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/sqlitedb"
)

// trashLinkCap bounds the links kept in one unit.archive activity row.
const trashLinkCap = 1000

// trashLinks is the link list ArchiveUnit stores in its activity row (by uid,
// so it stays meaningful whatever happens to the link ids).
func trashLinks(rows []sqlitedb.Row) []any {
	out := []any{}
	for i, r := range rows {
		if i >= trashLinkCap {
			break
		}
		out = append(out, pyjson.NewObject("source", r.Get("suid"), "target", r.Get("tuid"),
			"relation", r.Get("relation"), "note", r.Get("note"), "created_at", r.Get("created_at"),
			"created_by", r.Get("created_by")))
	}
	return out
}

// restoreTrashLinks recreates the links the unit's unit.archive rows since
// its last restore recorded.  A link is skipped when its other end is gone or still in the bin,
// when it exists already, or when it is a part_of whose child has gained
// another parent since (a node keeps one parent).
func restoreTrashLinks(w *wtx, row sqlitedb.Row) (int64, error) {
	ctx := w.ctx
	acts, err := sqlitedb.Query(ctx, w.q, `SELECT detail_json FROM activity WHERE unit_id=? AND action='unit.archive'
                   AND id > COALESCE((SELECT MAX(id) FROM activity WHERE unit_id=? AND action='unit.restore'), 0)
                   ORDER BY id`, row.Int("id"), row.Int("id"))
	if err != nil {
		return 0, err
	}
	var list []any
	for _, act := range acts {
		links, _ := parseDetail(act.Get("detail_json")).Value("links").([]any)
		list = append(list, links...)
	}
	n := int64(0)
	for _, item := range list {
		link, ok := item.(*pyjson.Object)
		if !ok {
			continue
		}
		rel := pyjson.Str(link.Value("relation"))
		if !relationSet[rel] {
			continue
		}
		src, ok1, err := liveUnitID(w, link.Value("source"))
		if err != nil {
			return n, err
		}
		dst, ok2, err := liveUnitID(w, link.Value("target"))
		if err != nil {
			return n, err
		}
		if !ok1 || !ok2 || src == dst {
			continue
		}
		if rel == "part_of" {
			_, has, err := sqlitedb.QueryOne(ctx, w.q, "SELECT id FROM unit_links WHERE source_id=? AND relation='part_of' LIMIT 1", src)
			if err != nil {
				return n, err
			}
			if has {
				continue
			}
		}
		res, err := w.q.ExecContext(ctx, `INSERT OR IGNORE INTO unit_links(source_id,target_id,relation,note,created_at,created_by)
                   VALUES(?,?,?,?,?,?)`, src, dst, rel, pyjson.Str(orEmptyStr(link.Value("note"))),
			pyjson.Str(link.Value("created_at")), pyjson.Str(orEmptyStr(link.Value("created_by"))))
		if err != nil {
			return n, err
		}
		if k, _ := res.RowsAffected(); k > 0 {
			n++
		}
	}
	return n, nil
}

func orEmptyStr(v any) any {
	if v == nil {
		return ""
	}
	return v
}

// liveUnitID resolves a uid to the id of a unit that is not in the bin.
func liveUnitID(w *wtx, uid any) (int64, bool, error) {
	s, ok := uid.(string)
	if !ok || s == "" {
		return 0, false, nil
	}
	r, ok, err := sqlitedb.QueryOne(w.ctx, w.q, "SELECT id FROM units WHERE uid=? AND archived=0", s)
	if err != nil || !ok {
		return 0, false, err
	}
	return r.Int("id"), true, nil
}

// Trash lists the units in the recycle bin, most recently deleted first.
// deleted_at/deleted_by come from the first unit.archive row since the unit's
// last restore (a second delete of a deleted unit changes nothing);
// "links" counts the links a restore can try to bring back.
func (s *Store) Trash(ctx context.Context) (*pyjson.Object, error) {
	var out *pyjson.Object
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		rows, err := sqlitedb.Query(ctx, q, `SELECT id, uid, type, title, status, importance, updated_at
                   FROM units WHERE archived=1`)
		if err != nil {
			return err
		}
		acts, err := sqlitedb.Query(ctx, q, `SELECT unit_id, action, at, actor, detail_json FROM activity
                   WHERE action IN ('unit.archive','unit.restore') AND unit_id IN (SELECT id FROM units WHERE archived=1)
                   ORDER BY id`)
		if err != nil {
			return err
		}
		type since struct {
			at, actor any
			links     int64
		}
		last := map[int64]*since{}
		for _, a := range acts {
			id := a.Int("unit_id")
			if a.Str("action") == "unit.restore" {
				delete(last, id)
				continue
			}
			entry := last[id]
			if entry == nil {
				entry = &since{at: a.Get("at"), actor: a.Get("actor")}
				last[id] = entry
			}
			links, _ := parseDetail(a.Get("detail_json")).Value("links").([]any)
			entry.links += int64(len(links))
		}
		items := make([]*pyjson.Object, 0, len(rows))
		for _, r := range rows {
			var at, by any = r.Get("updated_at"), nil
			links := int64(0)
			if entry := last[r.Int("id")]; entry != nil {
				at, by, links = entry.at, entry.actor, entry.links
			}
			items = append(items, pyjson.NewObject("id", r.Get("id"), "uid", r.Get("uid"), "type", r.Get("type"),
				"title", r.Get("title"), "status", r.Get("status"), "importance", r.Get("importance"),
				"deleted_at", at, "deleted_by", by, "links", links))
		}
		sort.SliceStable(items, func(i, j int) bool {
			a, b := pyjson.Str(items[i].Value("deleted_at")), pyjson.Str(items[j].Value("deleted_at"))
			if a != b {
				return a > b
			}
			return items[i].Value("id").(int64) > items[j].Value("id").(int64)
		})
		list := make([]any, len(items))
		for i, it := range items {
			list[i] = it
		}
		out = pyjson.NewObject("units", list, "total", int64(len(list)))
		return nil
	})
	return out, err
}

// PurgeUnit deletes a unit in the recycle bin for good: the row and, through
// ON DELETE CASCADE, its tags, links, media rows, comments, topic membership
// and text-history state.  Uploaded files stay on disk (other nodes or
// black-box snapshots may still point at them).  A live unit is refused.
func (s *Store) PurgeUnit(ctx context.Context, ref any, actor string) (*pyjson.Object, error) {
	var out *pyjson.Object
	err := s.write(ctx, func(w *wtx) error {
		w.full = true
		row, err := unitRowAny(ctx, w.q, ref)
		if err != nil {
			return err
		}
		if row.Int("archived") == 0 {
			return storeErr("%s is not in the recycle bin", row.Str("uid"))
		}
		if err := purgeRow(w, row, actor); err != nil {
			return err
		}
		out = pyjson.NewObject("id", row.Get("id"), "uid", row.Get("uid"), "deleted", true)
		return nil
	})
	return out, err
}

// EmptyTrash deletes every unit in the recycle bin for good.
func (s *Store) EmptyTrash(ctx context.Context, actor string) (*pyjson.Object, error) {
	var out *pyjson.Object
	err := s.write(ctx, func(w *wtx) error {
		w.full = true
		rows, err := sqlitedb.Query(ctx, w.q, "SELECT * FROM units WHERE archived=1 ORDER BY id")
		if err != nil {
			return err
		}
		uids := []any{}
		for _, row := range rows {
			if err := purgeRow(w, row, actor); err != nil {
				return err
			}
			uids = append(uids, row.Get("uid"))
		}
		out = pyjson.NewObject("deleted", int64(len(rows)), "uids", uids)
		return nil
	})
	return out, err
}

func purgeRow(w *wtx, row sqlitedb.Row, actor string) error {
	ctx := w.ctx
	// Outline parents in topics are plain ids (no foreign key): children of
	// the removed node go back to following part_of.
	if _, err := w.q.ExecContext(ctx, "UPDATE topic_members SET parent_id=NULL WHERE parent_id=?", row.Int("id")); err != nil {
		return err
	}
	if _, err := w.q.ExecContext(ctx, "DELETE FROM units WHERE id=? AND archived=1", row.Int("id")); err != nil {
		return err
	}
	return w.log(actor, "unit.purge", row.Int("id"), pyjson.NewObject("uid", row.Get("uid"),
		"title", row.Get("title"), "type", row.Get("type")))
}
