package store

import (
	"context"
	"sort"
	"strings"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pytext"
	"arkstudio/go/internal/rnd"
	"arkstudio/go/internal/sqlitedb"
)

// Change log (schema v11, db.CHANGE_LOG_SCHEMA): the triggers record which
// rendered rows changed; GET /api/changes turns the rows after a client's
// cursor into a snapshot delta.  A cursor is "<change_log_id>.<seq>".

// Store.CHANGES_ROW_LIMIT / CHANGES_UNIT_LIMIT.
const (
	ChangesRowLimit  = 50_000
	ChangesUnitLimit = 2_000
	pruneEvery       = 500 // Store._after_write prunes every 500 writes
)

// newChangeLogID is db.new_change_log_id: secrets.token_hex(8) (stream
// "change_log" under the replay clock, see internal/rnd).
func newChangeLogID(ctx context.Context, q sqlitedb.Querier) (string, error) {
	value := rnd.Hex("change_log", 8)
	return value, MetaSet(ctx, q, ChangeLogID, value)
}

// changeCursor is Store.change_cursor within one read or write.
func changeCursor(ctx context.Context, q sqlitedb.Querier) (string, int64, error) {
	logID, _, err := MetaGet(ctx, q, ChangeLogID)
	if err != nil {
		return "", 0, err
	}
	r, _, err := sqlitedb.QueryOne(ctx, q, "SELECT COALESCE(MAX(seq),0) top FROM change_log")
	if err != nil {
		return "", 0, err
	}
	top := r.Int("top")
	return logID + "." + pyjson.Str(top), top, nil
}

// ChangeCursor is Store.change_cursor.
func (s *Store) ChangeCursor(ctx context.Context) (string, error) {
	var out string
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		var err error
		out, _, err = changeCursor(ctx, q)
		return err
	})
	return out, err
}

// ResetChangeLogID is Store.reset_change_log_id (after a snapshot restore).
func (s *Store) ResetChangeLogID(ctx context.Context) (string, error) {
	var out string
	err := s.DB.Write(ctx, func(q sqlitedb.Querier) error {
		var err error
		out, err = newChangeLogID(ctx, q)
		return err
	}, nil)
	return out, err
}

// PruneChangeLog is Store.prune_change_log: keep the newest keep rows.
func (s *Store) PruneChangeLog(ctx context.Context, keep int64) (int64, error) {
	var n int64
	err := s.DB.Write(ctx, func(q sqlitedb.Querier) error {
		r, _, err := sqlitedb.QueryOne(ctx, q, "SELECT COALESCE(MAX(seq),0) top FROM change_log")
		if err != nil {
			return err
		}
		res, err := q.ExecContext(ctx, "DELETE FROM change_log WHERE seq <= ?", r.Int("top")-max(1, keep))
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	}, nil)
	return n, err
}

// countWrite is the prune counter of Store._after_write; it runs after a
// commit while the writer is held, so the prune itself runs afterwards.
func (s *Store) countWrite() {
	if s.writesSincePrune.Add(1) >= pruneEvery {
		s.writesSincePrune.Store(0)
		go s.PruneChangeLog(context.Background(), ChangeLogKeep) //nolint:errcheck // Python ignores sqlite3.Error
	}
}

// GraphBodies is Store.graph_bodies.
func (s *Store) GraphBodies(ctx context.Context) (*pyjson.Object, error) {
	var out *pyjson.Object
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		cursor, _, err := changeCursor(ctx, q)
		if err != nil {
			return err
		}
		rows, err := sqlitedb.Query(ctx, q, "SELECT id, body FROM units WHERE archived=0 ORDER BY id")
		if err != nil {
			return err
		}
		bodies := make([]any, len(rows))
		for i, r := range rows {
			bodies[i] = []any{r.Get("id"), r.Get("body")}
		}
		out = pyjson.NewObject("cursor", cursor, "bodies", bodies)
		return nil
	})
	return out, err
}

// snapshotNodes is Store._snapshot_nodes.
func (s *Store) snapshotNodes(ctx context.Context, q sqlitedb.Querier, rows []sqlitedb.Row, bodies bool) ([]any, error) {
	out := make([]any, 0, len(rows))
	for off := 0; off < len(rows); off += 400 {
		batch := rows[off:min(off+400, len(rows))]
		nodes, err := s.hydrate(ctx, q, batch, false)
		if err != nil {
			return nil, err
		}
		for i, node := range nodes {
			if bodies {
				node.Set("search_body", batch[i].Get("body"))
			}
			node.Set("body_truncated", pytext.Len(batch[i].Str("body")) > 240)
			out = append(out, node)
		}
	}
	return out, nil
}

func chunks(items []any, size int) [][]any {
	var out [][]any
	for off := 0; off < len(items); off += size {
		out = append(out, items[off:min(off+size, len(items))])
	}
	return out
}

// Changes is Store.changes: what changed in the graph snapshot since cursor.
func (s *Store) Changes(ctx context.Context, cursor string, bodies bool, unitLimit int) (*pyjson.Object, error) {
	var out *pyjson.Object
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		current, top, err := changeCursor(ctx, q)
		if err != nil {
			return err
		}
		logID, seqText, _ := strings.Cut(cursor, ".")
		out = pyjson.NewObject("cursor", current, "reset", false, "units", []any{}, "deleted", []any{},
			"edges", []any{}, "touched", []any{}, "bodies", bodies)
		if current == cursor {
			return nil
		}
		f, _, err := sqlitedb.QueryOne(ctx, q, "SELECT MIN(seq) floor FROM change_log")
		if err != nil {
			return err
		}
		since, perr := pytext.Int(seqText)
		if perr != nil {
			since = -1
		}
		currentID, _, _ := strings.Cut(current, ".")
		floor, hasFloor := f.Get("floor").(int64)
		if logID != currentID || since < 0 || since > top || (hasFloor && since < floor-1) {
			out.Set("reset", true).Set("reason", "cursor")
			return nil
		}
		rows, err := sqlitedb.Query(ctx, q, "SELECT kind, ref, tree FROM change_log WHERE seq > ? LIMIT ?",
			since, ChangesRowLimit+1)
		if err != nil {
			return err
		}
		if len(rows) > ChangesRowLimit {
			out.Set("reset", true).Set("reason", "size")
			return nil
		}
		changed := map[int64]bool{}
		var tree []int64
		inTree := map[int64]bool{}
		for _, r := range rows {
			if r.Str("kind") != "unit" {
				continue
			}
			ref := r.Int("ref")
			changed[ref] = true
			if pyjson.Truthy(r.Get("tree")) && !inTree[ref] {
				inTree[ref] = true
				tree = append(tree, ref)
			}
		}
		if len(changed) == 0 {
			return nil
		}
		touched := map[int64]bool{}
		sortedChanged := make([]any, 0, len(changed))
		for id := range changed {
			touched[id] = true
			sortedChanged = append(sortedChanged, id)
		}
		sort.Slice(sortedChanged, func(i, j int) bool { return sortedChanged[i].(int64) < sortedChanged[j].(int64) })
		// Neighbours show a changed unit's title/type/status; inherited
		// owners show their source.
		for _, chunk := range chunks(sortedChanged, 400) {
			marks := sqlitedb.Marks(len(chunk))
			links, err := sqlitedb.Query(ctx, q, "SELECT source_id, target_id FROM unit_links WHERE source_id IN ("+
				marks+") OR target_id IN ("+marks+")", append(append([]any{}, chunk...), chunk...)...)
			if err != nil {
				return err
			}
			for _, l := range links {
				touched[l.Int("source_id")] = true
				touched[l.Int("target_id")] = true
			}
		}
		index, err := s.assigneeIndex(ctx, q)
		if err != nil {
			return err
		}
		for id, e := range index {
			if e.from != 0 && changed[e.from] {
				touched[id] = true
			}
		}
		if len(tree) > 0 {
			children := map[int64][]int64{}
			parts, err := sqlitedb.Query(ctx, q, "SELECT source_id, target_id FROM unit_links WHERE relation='part_of'")
			if err != nil {
				return err
			}
			for _, l := range parts {
				children[l.Int("target_id")] = append(children[l.Int("target_id")], l.Int("source_id"))
			}
			queue := append([]int64(nil), tree...)
			seen := map[int64]bool{}
			for _, id := range tree {
				seen[id] = true
			}
			for len(queue) > 0 {
				id := queue[0]
				queue = queue[1:]
				for _, child := range children[id] {
					if !seen[child] {
						seen[child] = true
						queue = append(queue, child)
					}
				}
			}
			for id := range seen {
				touched[id] = true
			}
		}
		// A topic's member count follows its rule over every unit.
		topics, err := sqlitedb.Query(ctx, q, "SELECT id FROM units WHERE type='topic'")
		if err != nil {
			return err
		}
		for _, r := range topics {
			touched[r.Int("id")] = true
		}
		if len(touched) > unitLimit {
			out.Set("reset", true).Set("reason", "size")
			return nil
		}
		ids := make([]any, 0, len(touched))
		for id := range touched {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i].(int64) < ids[j].(int64) })
		var liveRows []sqlitedb.Row
		for _, chunk := range chunks(ids, 400) {
			part, err := sqlitedb.Query(ctx, q, "SELECT * FROM units WHERE archived=0 AND id IN ("+
				sqlitedb.Marks(len(chunk))+") ORDER BY updated_at DESC, id DESC", chunk...)
			if err != nil {
				return err
			}
			liveRows = append(liveRows, part...)
		}
		live := map[int64]bool{}
		for _, r := range liveRows {
			live[r.Int("id")] = true
		}
		units, err := s.snapshotNodes(ctx, q, liveRows, bodies)
		if err != nil {
			return err
		}
		deleted := []any{}
		for _, id := range ids {
			if !live[id.(int64)] {
				deleted = append(deleted, id)
			}
		}
		edges := map[int64]*pyjson.Object{}
		for _, chunk := range chunks(ids, 400) {
			marks := sqlitedb.Marks(len(chunk))
			part, err := sqlitedb.Query(ctx, q, "SELECT l.id, l.source_id AS source, l.target_id AS target, l.relation, l.note "+
				"FROM unit_links l JOIN units s ON s.id=l.source_id "+
				"JOIN units t ON t.id=l.target_id WHERE s.archived=0 AND t.archived=0 "+
				"AND (l.source_id IN ("+marks+") OR l.target_id IN ("+marks+"))",
				append(append([]any{}, chunk...), chunk...)...)
			if err != nil {
				return err
			}
			for i, o := range rowObjects(part) {
				edges[part[i].Int("id")] = o.(*pyjson.Object)
			}
		}
		keys := make([]int64, 0, len(edges))
		for id := range edges {
			keys = append(keys, id)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		edgeList := make([]any, len(keys))
		for i, id := range keys {
			edgeList[i] = edges[id]
		}
		out.Set("units", units).Set("deleted", deleted).Set("edges", edgeList).Set("touched", ids)
		return nil
	})
	return out, err
}
