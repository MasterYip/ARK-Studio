package store

// Multi-unit operations (Go only, server/routes_batch.go):
//
//   UnitsBatch   the shared-attribute read of a selection (units + topics)
//   BatchUpdate  one attribute edit applied to many units in one transaction
//   GroupUnder   a new parent node for sibling units (mind map Ctrl+Enter)
//   Ungroup      its inverse: children move up, the group node goes to the bin
//
// Every activity entry a batch writes carries the same "batch" id
// (wtx.batch), one entry per unit as with single edits.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/sqlitedb"
)

// MaxBatchUnits bounds one batch read or write.
const MaxBatchUnits = 500

// batchFields are the attributes a batch may set; title and body are per
// node and never batch-edited.
var batchFields = map[string]bool{
	"type": true, "status": true, "importance": true, "assignee": true,
	"start_at": true, "end_at": true, "tags": true,
}

// NewBatchID returns a fresh "b_" id for one batch operation.
func NewBatchID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "b_" + hex.EncodeToString(b[:])
}

// CleanBatchID accepts a client batch id (so an undo can reuse a recognisable
// one) or makes a new one.
func CleanBatchID(v any) string {
	text := ""
	if s, ok := v.(string); ok {
		text = strings.TrimSpace(s)
	}
	if text == "" || len(text) > 40 {
		return NewBatchID()
	}
	for _, r := range text {
		if !(r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return NewBatchID()
		}
	}
	return text
}

// BatchItem is one unit of a BatchUpdate: Patch is an update_unit patch
// restricted to batchFields; TagsAdd / TagsRemove change the unit's own tags
// (type/status/importance tags are not touched through them).
type BatchItem struct {
	Ref        any
	Patch      *pyjson.Object
	TagsAdd    []string
	TagsRemove []string
}

// CheckBatchPatch rejects fields a batch cannot set.
func CheckBatchPatch(patch *pyjson.Object) error {
	if patch == nil {
		return nil
	}
	for _, k := range patch.Keys() {
		if !batchFields[k] {
			return storeErr("%s cannot be changed for several nodes at once", k)
		}
	}
	return nil
}

func uniqueRefs(refs []any) []any {
	seen := map[string]bool{}
	var out []any
	for _, r := range refs {
		key := pyjson.Str(r)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}

// UnitsBatch returns the hydrated units of refs (without body) with their
// topics, plus the refs that do not resolve to a live unit.
func (s *Store) UnitsBatch(ctx context.Context, refs []any) (*pyjson.Object, error) {
	refs = uniqueRefs(refs)
	if len(refs) > MaxBatchUnits {
		return nil, storeErr("at most %d units per batch", MaxBatchUnits)
	}
	var out *pyjson.Object
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		var rows []sqlitedb.Row
		missing := []any{}
		seen := map[int64]bool{}
		for _, ref := range refs {
			row, err := unitRowAny(ctx, q, ref)
			if err != nil {
				if isStoreErr(err) {
					missing = append(missing, ref)
					continue
				}
				return err
			}
			if row.Int("archived") != 0 {
				missing = append(missing, ref)
				continue
			}
			if !seen[row.Int("id")] {
				seen[row.Int("id")] = true
				rows = append(rows, row)
			}
		}
		units := []any{}
		if len(rows) > 0 {
			items, err := s.hydrate(ctx, q, rows, false)
			if err != nil {
				return err
			}
			topics, err := s.topicsOfMany(ctx, q, seen)
			if err != nil {
				return err
			}
			for _, item := range items {
				id, _ := item.Value("id").(int64)
				list := topics[id]
				if list == nil {
					list = []any{}
				}
				units = append(units, item.Set("topics", list))
			}
		}
		out = pyjson.NewObject("units", units, "missing", missing)
		return nil
	})
	return out, err
}

// topicsOfMany is topicsOf for a set of units (each topic's members are
// resolved once).
func (s *Store) topicsOfMany(ctx context.Context, q sqlitedb.Querier, units map[int64]bool) (map[int64][]any, error) {
	rows, err := sqlitedb.Query(ctx, q, "SELECT * FROM units WHERE archived=0 AND type='topic' ORDER BY title")
	if err != nil {
		return nil, err
	}
	out := map[int64][]any{}
	for _, r := range rows {
		members, _, err := s.resolveMembers(ctx, q, r)
		if err != nil {
			return nil, err
		}
		for _, m := range members {
			if units[m.id] && m.id != r.Int("id") {
				out[m.id] = append(out[m.id], pyjson.NewObject("id", r.Get("id"), "uid", r.Get("uid"),
					"title", r.Get("title"), "type", r.Get("type")))
			}
		}
	}
	return out, nil
}

// ownTags are a unit's tags without the type/status/importance tags, the
// list the inspector edits.
func ownTags(ctx context.Context, q sqlitedb.Querier, unitID int64) ([]any, error) {
	tags, err := tagsOfRow(ctx, q, unitID, true)
	if err != nil {
		return nil, err
	}
	out := []any{}
	for _, t := range tags {
		if !reserved[TagNamespace(t)] {
			out = append(out, t)
		}
	}
	return out, nil
}

// cleanTagList normalises tags to their stored "ns:value" form, without the
// namespaces a batch tag edit cannot touch.  Malformed tags are kept as given
// so update_unit reports them.
func cleanTagList(tags []string) []string {
	var out []string
	for _, t := range tags {
		key := tagKey(t)
		if key == "" || reserved[TagNamespace(key)] || TagNamespace(key) == needsNS {
			continue
		}
		out = append(out, key)
	}
	return out
}

func tagKey(t string) string {
	ns, value, err := splitTag(t)
	if err != nil {
		return strings.TrimSpace(t)
	}
	return ns + ":" + value
}

// BatchUpdate applies items in one transaction.  A unit that cannot take its
// patch (unknown, in the recycle bin, invalid value) is skipped and reported;
// the others are written.  Units the patch does not change are listed as
// unchanged and get no activity entry.
func (s *Store) BatchUpdate(ctx context.Context, items []BatchItem, batch, actor string, who Who) (*pyjson.Object, error) {
	if len(items) == 0 {
		return nil, storeErr("no units given")
	}
	if len(items) > MaxBatchUnits {
		return nil, storeErr("at most %d units per batch", MaxBatchUnits)
	}
	for _, item := range items {
		if err := CheckBatchPatch(item.Patch); err != nil {
			return nil, err
		}
	}
	if batch == "" {
		batch = NewBatchID()
	}
	var changedIDs []int64
	unchanged, skipped := []any{}, []any{}
	err := s.write(ctx, func(w *wtx) error {
		w.batch = batch
		changedIDs, unchanged, skipped = nil, []any{}, []any{}
		var touched []int64
		full := false
		done := map[int64]bool{}
		for i, item := range items {
			row, err := unitRowAny(ctx, w.q, item.Ref)
			if err != nil {
				if !isStoreErr(err) {
					return err
				}
				skipped = append(skipped, pyjson.NewObject("unit", item.Ref, "error", err.Error()))
				continue
			}
			uid := row.Get("uid")
			if row.Int("archived") != 0 {
				skipped = append(skipped, pyjson.NewObject("unit", item.Ref, "uid", uid, "error", "in the recycle bin"))
				continue
			}
			if done[row.Int("id")] {
				continue
			}
			done[row.Int("id")] = true
			patch := pyjson.NewObject()
			if item.Patch != nil {
				patch = item.Patch.Copy()
			}
			add, remove := cleanTagList(item.TagsAdd), cleanTagList(item.TagsRemove)
			if len(add) > 0 || len(remove) > 0 {
				base := []any{}
				if want, ok := patch.Get("tags"); ok && want != nil {
					if base, err = listOf(want); err != nil {
						return err
					}
				} else if base, err = ownTags(ctx, w.q, row.Int("id")); err != nil {
					return err
				}
				drop := map[string]bool{}
				for _, t := range remove {
					drop[t] = true
				}
				next, have := []any{}, map[string]bool{}
				for _, t := range base {
					text := tagKey(pyjson.Str(t))
					if !drop[text] && !have[text] {
						have[text] = true
						next = append(next, text)
					}
				}
				for _, t := range add {
					if !have[t] {
						have[t] = true
						next = append(next, t)
					}
				}
				patch.Set("tags", next)
			}
			mark := len(w.lines)
			point := "batch_" + pad4(int64(i))
			if _, err := w.q.ExecContext(ctx, "SAVEPOINT "+point); err != nil {
				return err
			}
			w.noTouch, w.full, w.units = false, false, nil
			_, err = s.updateUnitTx(w, row.Int("id"), patch, actor, who, "Save text")
			if err != nil {
				if !isStoreErr(err) {
					return err
				}
				if _, rerr := w.q.ExecContext(ctx, "ROLLBACK TO "+point); rerr != nil {
					return rerr
				}
				w.lines = w.lines[:mark]
				skipped = append(skipped, pyjson.NewObject("unit", item.Ref, "uid", uid, "error", err.Error()))
			} else if w.noTouch {
				unchanged = append(unchanged, uid)
			} else {
				touched = append(touched, row.Int("id"))
				changedIDs = append(changedIDs, row.Int("id"))
				full = full || w.full
			}
			if _, err := w.q.ExecContext(ctx, "RELEASE "+point); err != nil {
				return err
			}
		}
		w.units, w.full, w.noTouch = touched, full, len(touched) == 0
		return nil
	})
	if err != nil {
		return nil, err
	}
	updated := []any{}
	if len(changedIDs) > 0 {
		refs := make([]any, len(changedIDs))
		for i, id := range changedIDs {
			refs[i] = id
		}
		fresh, err := s.UnitsBatch(ctx, refs)
		if err != nil {
			return nil, err
		}
		updated = fresh.Value("units").([]any)
	}
	return pyjson.NewObject("batch", batch, "updated", updated, "unchanged", unchanged, "skipped", skipped), nil
}

// linkTx inserts a link inside w (CreateLink's row and activity entry).
func linkTx(w *wtx, src, dst sqlitedb.Row, rel, note, actor string) (*pyjson.Object, error) {
	res, err := w.q.ExecContext(w.ctx, "INSERT INTO unit_links(source_id,target_id,relation,note,created_at,created_by) VALUES(?,?,?,?,?,?)",
		src.Int("id"), dst.Int("id"), rel, note, clock.UTCNow(), actor)
	if err != nil {
		return nil, err
	}
	linkID, _ := res.LastInsertId()
	if err := w.log(actor, "link.create", src.Int("id"), pyjson.NewObject("from", src.Get("uid"), "to", dst.Get("uid"),
		"relation", rel, "link_id", linkID)); err != nil {
		return nil, err
	}
	return pyjson.NewObject("id", linkID, "source", src.Get("id"), "target", dst.Get("id"), "relation", rel), nil
}

// unlinkTx deletes one link row inside w (DeleteLink's activity entry).
func unlinkTx(w *wtx, link, src, dst sqlitedb.Row, actor string) error {
	if _, err := w.q.ExecContext(w.ctx, "DELETE FROM unit_links WHERE id=?", link.Get("id")); err != nil {
		return err
	}
	return w.log(actor, "link.delete", src.Int("id"), pyjson.NewObject("from", src.Get("uid"),
		"to", dst.Get("uid"), "relation", link.Get("relation")))
}

func cleanRelation(relation any) (string, error) {
	rel := relationArg(relation)
	if rel == "" {
		rel = "part_of"
	}
	if !relationSet[rel] {
		return "", storeErr("unknown relation: %s", pyjson.Repr(rel))
	}
	return rel, nil
}

func idsOf(rows []sqlitedb.Row) []any {
	out := make([]any, len(rows))
	for i, r := range rows {
		out[i] = r.Get("uid")
	}
	return out
}

// GroupUnder creates one new unit (data: title, type, status, importance,
// tags) between the sibling units refs and their common parent: each
// "child -relation-> parent" link becomes "child -> new" and the new unit
// gets "new -> parent".  parentRef nil groups top-level units (none of them
// may have a parent through relation) under a new top-level unit.  All or
// nothing, in one transaction.
func (s *Store) GroupUnder(ctx context.Context, refs []any, parentRef any, relation any, data *pyjson.Object, batch, actor string, who Who) (*pyjson.Object, error) {
	refs = uniqueRefs(refs)
	if len(refs) == 0 {
		return nil, storeErr("no units given")
	}
	if len(refs) > MaxBatchUnits {
		return nil, storeErr("at most %d units per batch", MaxBatchUnits)
	}
	rel, err := cleanRelation(relation)
	if err != nil {
		return nil, err
	}
	if data == nil {
		data = pyjson.NewObject()
	}
	data = data.Copy()
	for _, k := range data.Keys() {
		switch k {
		case "title", "type", "status", "importance", "tags", "origin":
		default:
			data.Delete(k)
		}
	}
	if !pyjson.Truthy(data.Value("title")) {
		data.Set("title", "New node")
	}
	if batch == "" {
		batch = NewBatchID()
	}
	var out *pyjson.Object
	err = s.write(ctx, func(w *wtx) error {
		w.full, w.batch = true, batch
		var children []sqlitedb.Row
		seen := map[int64]bool{}
		for _, ref := range refs {
			row, err := unitRowAny(ctx, w.q, ref)
			if err != nil {
				return err
			}
			if row.Int("archived") != 0 {
				return storeErr("%s is in the recycle bin", row.Str("uid"))
			}
			if !seen[row.Int("id")] {
				seen[row.Int("id")] = true
				children = append(children, row)
			}
		}
		var parent sqlitedb.Row
		hasParent := parentRef != nil && parentRef != ""
		if hasParent {
			if parent, err = unitRowAny(ctx, w.q, parentRef); err != nil {
				return err
			}
			if seen[parent.Int("id")] {
				return storeErr("the parent cannot be one of the grouped nodes")
			}
		}
		var oldLinks []sqlitedb.Row
		for _, child := range children {
			if hasParent {
				link, ok, err := sqlitedb.QueryOne(ctx, w.q, "SELECT * FROM unit_links WHERE source_id=? AND target_id=? AND relation=? ORDER BY id LIMIT 1",
					child.Int("id"), parent.Int("id"), rel)
				if err != nil {
					return err
				}
				if !ok {
					return storeErr("%s is not a child of %s: only nodes with the same parent can be grouped", child.Str("uid"), parent.Str("uid"))
				}
				oldLinks = append(oldLinks, link)
				continue
			}
			_, ok, err := sqlitedb.QueryOne(ctx, w.q, "SELECT id FROM unit_links WHERE source_id=? AND relation=? LIMIT 1", child.Int("id"), rel)
			if err != nil {
				return err
			}
			if ok {
				return storeErr("%s has a parent: only nodes with the same parent can be grouped", child.Str("uid"))
			}
		}
		newID, err := s.createUnitIn(ctx, w, data, actor, who)
		if err != nil {
			return err
		}
		group, err := unitRowAny(ctx, w.q, newID)
		if err != nil {
			return err
		}
		links := []any{}
		removed := []any{}
		var parentOut any
		if hasParent {
			link, err := linkTx(w, group, parent, rel, "Grouped nodes", actor)
			if err != nil {
				return err
			}
			links = append(links, link)
			parentOut = pyjson.NewObject("id", parent.Get("id"), "uid", parent.Get("uid"), "title", parent.Get("title"))
		}
		for i, child := range children {
			if hasParent {
				if err := unlinkTx(w, oldLinks[i], child, parent, actor); err != nil {
					return err
				}
				removed = append(removed, oldLinks[i].Get("id"))
			}
			link, err := linkTx(w, child, group, rel, "Grouped nodes", actor)
			if err != nil {
				return err
			}
			links = append(links, link)
		}
		var parentUID any
		if hasParent {
			parentUID = parent.Get("uid")
		}
		if err := w.log(actor, "unit.group", newID, pyjson.NewObject("uid", group.Get("uid"), "title", group.Get("title"),
			"parent", parentUID, "children", idsOf(children), "relation", rel)); err != nil {
			return err
		}
		out = pyjson.NewObject("batch", batch, "parent", parentOut, "children", idsOf(children),
			"removed_links", removed, "links", links, "relation", rel)
		w.units = nil
		out.Set("unit_id", newID)
		return nil
	})
	if err != nil {
		return nil, err
	}
	unit, err := s.GetUnit(ctx, pyjson.Str(out.Value("unit_id")))
	if err != nil {
		return nil, err
	}
	out.Delete("unit_id")
	out.Set("unit", unit)
	return out, nil
}

// Ungroup undoes GroupUnder for ref: every "child -relation-> ref" link
// becomes "child -> ref's parent" (or is dropped when ref is top level), and
// ref goes to the recycle bin with its remaining links, like a delete.
func (s *Store) Ungroup(ctx context.Context, ref any, relation any, batch, actor string) (*pyjson.Object, error) {
	rel, err := cleanRelation(relation)
	if err != nil {
		return nil, err
	}
	if batch == "" {
		batch = NewBatchID()
	}
	var out *pyjson.Object
	err = s.write(ctx, func(w *wtx) error {
		w.full, w.batch = true, batch
		group, err := unitRowAny(ctx, w.q, ref)
		if err != nil {
			return err
		}
		if group.Int("archived") != 0 {
			return storeErr("%s is in the recycle bin", group.Str("uid"))
		}
		var parent sqlitedb.Row
		parentLink, hasParent, err := sqlitedb.QueryOne(ctx, w.q, "SELECT * FROM unit_links WHERE source_id=? AND relation=? ORDER BY id LIMIT 1", group.Int("id"), rel)
		if err != nil {
			return err
		}
		if hasParent {
			if parent, err = unitRowAny(ctx, w.q, parentLink.Int("target_id")); err != nil {
				return err
			}
		}
		childLinks, err := sqlitedb.Query(ctx, w.q, "SELECT * FROM unit_links WHERE target_id=? AND relation=? ORDER BY id", group.Int("id"), rel)
		if err != nil {
			return err
		}
		children := []any{}
		links := []any{}
		for _, link := range childLinks {
			child, err := unitRowAny(ctx, w.q, link.Int("source_id"))
			if err != nil {
				return err
			}
			if err := unlinkTx(w, link, child, group, actor); err != nil {
				return err
			}
			children = append(children, child.Get("uid"))
			if !hasParent || child.Int("id") == parent.Int("id") {
				continue
			}
			_, exists, err := sqlitedb.QueryOne(ctx, w.q, "SELECT id FROM unit_links WHERE source_id=? AND target_id=? AND relation=?",
				child.Int("id"), parent.Int("id"), rel)
			if err != nil {
				return err
			}
			if exists {
				continue
			}
			made, err := linkTx(w, child, parent, rel, "Ungrouped nodes", actor)
			if err != nil {
				return err
			}
			links = append(links, made)
		}
		// The group node itself goes to the bin like ArchiveUnit.
		if _, err := w.q.ExecContext(ctx, "UPDATE units SET archived=1, updated_at=? WHERE id=?", clock.UTCNow(), group.Int("id")); err != nil {
			return err
		}
		if err := syncNeeds(w, group, true, actor); err != nil {
			return err
		}
		rest, err := sqlitedb.Query(ctx, w.q, `SELECT l.id, l.source_id, l.target_id, l.relation, l.note, l.created_at,
                   l.created_by, s.uid suid, t.uid tuid FROM unit_links l JOIN units s ON s.id=l.source_id
                   JOIN units t ON t.id=l.target_id WHERE l.source_id=? OR l.target_id=? ORDER BY l.id`,
			group.Int("id"), group.Int("id"))
		if err != nil {
			return err
		}
		if _, err := w.q.ExecContext(ctx, "DELETE FROM unit_links WHERE source_id=? OR target_id=?", group.Int("id"), group.Int("id")); err != nil {
			return err
		}
		if err := w.log(actor, "unit.archive", group.Int("id"), pyjson.NewObject("uid", group.Get("uid"),
			"title", group.Get("title"), "links_deleted", int64(len(rest)), "links", trashLinks(rest))); err != nil {
			return err
		}
		var parentOut any
		if hasParent {
			parentOut = pyjson.NewObject("id", parent.Get("id"), "uid", parent.Get("uid"), "title", parent.Get("title"))
		}
		out = pyjson.NewObject("batch", batch, "id", group.Get("id"), "uid", group.Get("uid"), "archived", true,
			"parent", parentOut, "children", children, "links", links, "relation", rel)
		return nil
	})
	return out, err
}
