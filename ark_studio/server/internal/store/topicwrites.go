package store

import (
	"context"
	"math"
	"math/big"
	"sort"
	"strings"

	"arkstudio/go/internal/platform/clock"

	"arkstudio/go/internal/platform/pyerr"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/sqlitedb"
)

// Port of the topic write path of store.py: _member_items, _coord,
// _clean_rule, _check_topic_design, design_topic, add_topic_members,
// remove_topic_member.

func sortStrings(a []string) { sort.Strings(a) }

// PyEqual is Python == for decoded JSON / SQLite values: dicts ignore key
// order, bool and numbers compare numerically.
func PyEqual(a, b any) bool {
	if x, ok := a.([]int64); ok {
		a = int64sAny(x)
	}
	if y, ok := b.([]int64); ok {
		b = int64sAny(y)
	}
	if na, ok := numeric(a); ok {
		if nb, ok := numeric(b); ok {
			return na.Cmp(nb) == 0
		}
		return false
	}
	switch x := a.(type) {
	case nil:
		return b == nil
	case string:
		y, ok := b.(string)
		return ok && x == y
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !PyEqual(x[i], y[i]) {
				return false
			}
		}
		return true
	case *pyjson.Object:
		y, ok := b.(*pyjson.Object)
		if !ok || x.Len() != y.Len() {
			return false
		}
		for _, k := range x.Keys() {
			v, has := y.Get(k)
			if !has || !PyEqual(x.Value(k), v) {
				return false
			}
		}
		return true
	}
	return false
}

func int64sAny(x []int64) []any {
	out := make([]any, len(x))
	for i, v := range x {
		out[i] = v
	}
	return out
}

func numeric(v any) (*big.Float, bool) {
	switch x := v.(type) {
	case bool:
		if x {
			return big.NewFloat(1), true
		}
		return big.NewFloat(0), true
	case int64:
		return new(big.Float).SetInt64(x), true
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, false
		}
		return big.NewFloat(x), true
	case *big.Int:
		return new(big.Float).SetInt(x), true
	}
	return nil, false
}

// memberItems is Store._member_items.
func memberItems(items any, key string) ([]*pyjson.Object, error) {
	if items == nil {
		return nil, nil
	}
	list, ok := items.([]any)
	if !ok {
		return nil, storeErr("%s must be a list", key)
	}
	out := make([]*pyjson.Object, 0, len(list))
	for _, item := range list {
		if o, ok := item.(*pyjson.Object); ok {
			unit, has := o.Get("unit")
			if !has {
				unit, has = o.Get("id")
				if !has {
					unit = o.Value("uid")
				}
			}
			out = append(out, o.Copy().Set("unit", unit))
		} else {
			out = append(out, pyjson.NewObject("unit", item))
		}
	}
	return out, nil
}

// coord is Store._coord (nil = None).
func coord(value any) (any, error) {
	if value == nil || value == "" {
		return nil, nil
	}
	n, ok, err := pyFloat(value)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, storeErr("member x/y must be numbers")
	}
	if math.IsNaN(n) || math.Abs(n) > 1e7 {
		return nil, storeErr("member x/y is out of range")
	}
	return pyRound(n, 2), nil
}

// Node-graph card width limits (MIN_CARD_W / MAX_CARD_W).
const (
	minCardW = 140
	maxCardW = 640
)

// cardWidth is Store._card_width (nil = automatic).
func cardWidth(value any) (any, error) {
	if value == nil || value == "" {
		return nil, nil
	}
	if _, ok := value.(bool); ok {
		return nil, storeErr("member w must be a number")
	}
	n, ok, err := pyFloat(value)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, storeErr("member w must be a number")
	}
	if math.IsNaN(n) || !(minCardW <= n && n <= maxCardW) {
		return nil, storeErr("member w must be between %d and %d", minCardW, maxCardW)
	}
	return pyRound(n, 1), nil
}

// cleanRule is Store._clean_rule (nil = None).
func cleanRule(rule any) (*pyjson.Object, error) {
	if rule == nil || PyEqual(rule, false) {
		return nil, nil
	}
	if o, ok := rule.(*pyjson.Object); ok && o.Len() == 0 {
		return nil, nil
	}
	r, ok := rule.(*pyjson.Object)
	if !ok {
		return nil, storeErr("rule must be an object")
	}
	words := func(key string) ([]any, error) {
		value := r.Value(key)
		if !pyjson.Truthy(value) {
			value = []any{}
		}
		if s, ok := value.(string); ok {
			parts := strings.Split(s, ",")
			value = make([]any, len(parts))
			for i, p := range parts {
				value.([]any)[i] = p
			}
		}
		list, ok := value.([]any)
		if !ok {
			return nil, storeErr("rule.%s must be a list", key)
		}
		out := []any{}
		for _, v := range list {
			text := pytext.Strip(pyjson.Str(v))
			if text != "" {
				out = append(out, pytext.Prefix(text, 120))
			}
		}
		if len(out) > 60 {
			out = out[:60]
		}
		return out, nil
	}
	clean := pyjson.NewObject()
	q := pytext.Prefix(pytext.Strip(strOrFalsy(r.Value("q"))), 200)
	if q != "" {
		clean.Set("q", q)
	}
	for _, key := range []string{"types", "statuses", "importance", "tags", "tags_unset", "origins", "assignees"} {
		values, err := words(key)
		if err != nil {
			return nil, err
		}
		if len(values) > 0 {
			clean.Set(key, values)
		}
	}
	if types, ok := clean.Value("types").([]any); ok {
		for _, t := range types {
			if !knownType(t.(string)) {
				return nil, storeErr("unknown type in rule: %s", t.(string))
			}
		}
	}
	if pyjson.Truthy(r.Value("roots")) {
		clean.Set("roots", true)
	}
	for _, key := range []string{"created_since", "updated_since", "start_from", "start_to"} {
		value := pytext.Prefix(pytext.Strip(strOrFalsy(r.Value(key))), 40)
		if value != "" {
			clean.Set(key, value)
		}
	}
	if clean.Len() == 0 {
		return nil, nil
	}
	return clean, nil
}

// strOrFalsy is str(value or "").
func strOrFalsy(v any) string {
	if !pyjson.Truthy(v) {
		return ""
	}
	return pyjson.Str(v)
}

// checkTopicDesign is Store._check_topic_design.
func (s *Store) checkTopicDesign(ctx context.Context, data *pyjson.Object) error {
	designRaw := data.Value("design")
	if designRaw != nil {
		if _, ok := designRaw.(*pyjson.Object); !ok {
			return storeErr("topic design must be an object")
		}
	}
	design, _ := designRaw.(*pyjson.Object)
	if !pyjson.Truthy(design) {
		design = pyjson.NewObject()
	}
	addRaw := data.Value("members")
	if !pyjson.Truthy(addRaw) {
		addRaw = design.Value("add")
	}
	refs, err := memberItems(addRaw, "add")
	if err != nil {
		return err
	}
	for _, key := range []string{"remove", "exclude", "restore", "items"} {
		more, err := memberItems(design.Value(key), key)
		if err != nil {
			return err
		}
		refs = append(refs, more...)
	}
	order := design.Value("order")
	if order != nil {
		if _, ok := order.([]any); !ok {
			return storeErr("order must be a list")
		}
	}
	if list, _ := order.([]any); len(list) > 0 {
		for _, item := range list {
			refs = append(refs, pyjson.NewObject("unit", item))
		}
	}
	items, err := memberItems(design.Value("items"), "items")
	if err != nil {
		return err
	}
	for _, item := range items {
		p := item.Value("parent")
		if !(p == nil || p == "" || p == "auto") {
			refs = append(refs, pyjson.NewObject("unit", p))
		}
	}
	err = s.read(ctx, func(q sqlitedb.Querier) error {
		for _, entry := range refs {
			u := entry.Value("unit")
			if u == nil || u == "" {
				return storeErr("member unit is required")
			}
			row, err := unitRowAny(ctx, q, u)
			if err != nil {
				return err
			}
			if row.Int("archived") != 0 {
				return storeErr("%s is archived", row.Str("uid"))
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if design.Has("rule") {
		_, err = cleanRule(design.Value("rule"))
	}
	return err
}

// hashable mirrors “x in dict“ raising TypeError for lists and dicts.
func hashable(v any) error {
	switch v.(type) {
	case []any:
		return &pyerr.InternalError{Message: "unhashable type: 'list'"}
	case *pyjson.Object:
		return &pyerr.InternalError{Message: "unhashable type: 'dict'"}
	}
	return nil
}

// digitInt is “int(v) if str(v).isdigit()“.
func digitInt(v any) (int64, bool, error) {
	text := pyjson.Str(v)
	if !pytext.IsDigit(text) {
		return 0, false, nil
	}
	n, err := PyInt(v)
	if err != nil {
		return 0, false, err
	}
	return n, true, nil
}

// DesignTopic is Store.design_topic.
func (s *Store) DesignTopic(ctx context.Context, ref any, body *pyjson.Object, actor string) (*pyjson.Object, error) {
	if body == nil {
		body = pyjson.NewObject()
	}
	add, err := memberItems(body.Value("add"), "add")
	if err != nil {
		return nil, err
	}
	remove, err := memberItems(body.Value("remove"), "remove")
	if err != nil {
		return nil, err
	}
	exclude, err := memberItems(body.Value("exclude"), "exclude")
	if err != nil {
		return nil, err
	}
	restore, err := memberItems(body.Value("restore"), "restore")
	if err != nil {
		return nil, err
	}
	items, err := memberItems(body.Value("items"), "items")
	if err != nil {
		return nil, err
	}
	orderRaw := body.Value("order")
	if orderRaw != nil {
		if _, ok := orderRaw.([]any); !ok {
			return nil, storeErr("order must be a list")
		}
	}
	report := pyjson.NewObject("added", []any{}, "skipped", []any{}, "removed", []any{}, "excluded", []any{},
		"restored", []any{}, "updated", int64(0), "reordered", false, "settings", false, "outline", false)
	appendTo := func(key string, v any) { report.Set(key, append(report.Value(key).([]any), v)) }
	changes := pyjson.NewObject()
	err = s.write(ctx, func(w *wtx) error {
		q := w.q
		w.full = true
		topic, err := unitRowAny(ctx, q, ref)
		if err != nil {
			return err
		}
		if topic.Str("type") != "topic" {
			return storeErr("%s is not a topic", topic.Str("uid"))
		}
		now := clock.UTCNow()
		sg, err := topicSubgraph(topic)
		if err != nil {
			return err
		}
		settings := sg.object()
		newSettings := settings.Copy()
		if body.Has("rule") {
			rule, err := cleanRule(body.Value("rule"))
			if err != nil {
				return err
			}
			if rule == nil {
				newSettings.Set("rule", nil)
			} else {
				newSettings.Set("rule", rule)
			}
		}
		if body.Has("relations") {
			relations := body.Value("relations")
			if relations != nil {
				list, ok := relations.([]any)
				if !ok {
					return storeErr("relations must be a list or null")
				}
				for _, r := range list {
					if err := hashable(r); err != nil {
						return err
					}
				}
				for _, r := range list {
					if s, ok := r.(string); !ok || !relationSet[s] {
						return storeErr("unknown relation: %s", pyjson.Repr(r))
					}
				}
				seen := map[string]bool{}
				dedup := []any{}
				for _, r := range list {
					if !seen[r.(string)] {
						seen[r.(string)] = true
						dedup = append(dedup, r)
					}
				}
				relations = dedup
			}
			newSettings.Set("relations", relations)
		}
		hidden := map[int64]bool{}
		for _, h := range sg.hidden {
			hidden[h] = true
		}
		if body.Has("hidden_links") {
			raw := body.Value("hidden_links")
			if !pyjson.Truthy(raw) {
				raw = []any{}
			}
			list, ok := raw.([]any)
			if !ok {
				return storeErr("hidden_links must be a list")
			}
			hidden = map[int64]bool{}
			for _, v := range list {
				n, ok, err := digitInt(v)
				if err != nil {
					return err
				}
				if ok {
					hidden[n] = true
				}
			}
		}
		for _, kv := range []struct {
			key  string
			show bool
		}{{"hide_links", false}, {"show_links", true}} {
			raw := body.Value(kv.key)
			if !pyjson.Truthy(raw) {
				raw = []any{}
			}
			list, ok := raw.([]any)
			if !ok {
				return storeErr("%s must be a list", kv.key)
			}
			for _, v := range list {
				n, ok, err := digitInt(v)
				if err != nil {
					return err
				}
				if ok {
					if kv.show {
						delete(hidden, n)
					} else {
						hidden[n] = true
					}
				}
			}
		}
		hiddenList := make([]int64, 0, len(hidden))
		for h := range hidden {
			hiddenList = append(hiddenList, h)
		}
		sort.Slice(hiddenList, func(i, j int) bool { return hiddenList[i] < hiddenList[j] })
		newSettings.Set("hidden_links", int64sAny(hiddenList))
		if !PyEqual(newSettings, settings) {
			meta := parseDetail(topic.Get("meta_json"))
			meta.Set("subgraph", newSettings)
			if _, err := q.ExecContext(ctx, "UPDATE units SET meta_json=?, updated_at=? WHERE id=?",
				string(pyjson.Dumps(meta)), now, topic.Int("id")); err != nil {
				return err
			}
			if topic, err = unitRowAny(ctx, q, topic.Int("id")); err != nil {
				return err
			}
			report.Set("settings", true)
			var keys []any
			for _, k := range newSettings.Keys() {
				if !PyEqual(newSettings.Value(k), settings.Value(k)) {
					keys = append(keys, k)
				}
			}
			if keys == nil {
				keys = []any{}
			}
			changes.Set("settings", keys)
		}
		topicID := topic.Int("id")
		rowList, err := sqlitedb.Query(ctx, q, "SELECT * FROM topic_members WHERE topic_id=?", topicID)
		if err != nil {
			return err
		}
		rows := map[int64]sqlitedb.Row{}
		position := int64(-1)
		for _, r := range rowList {
			rows[r.Int("unit_id")] = r
			if r.Int("position") > position {
				position = r.Int("position")
			}
		}
		unitOf := func(entry *pyjson.Object) (sqlitedb.Row, error) {
			unit, err := unitRowAny(ctx, q, entry.Value("unit"))
			if err != nil {
				return unit, err
			}
			if unit.Int("id") == topicID {
				return unit, storeErr("a topic cannot contain itself")
			}
			return unit, nil
		}
		put := func(unitID int64, mode string, entry *pyjson.Object) error {
			if entry == nil {
				entry = pyjson.NewObject()
			}
			existing, ok := rows[unitID]
			if !ok {
				position++
				x, err := coord(entry.Value("x"))
				if err != nil {
					return err
				}
				y, err := coord(entry.Value("y"))
				if err != nil {
					return err
				}
				caption, err := cleanTextField(entry.Value("caption"), "caption", maxCaption, false)
				if err != nil {
					return err
				}
				if _, err := q.ExecContext(ctx, `INSERT INTO topic_members(topic_id,unit_id,mode,position,x,y,caption,
                           added_at,added_by,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
					topicID, unitID, mode, position, x, y, caption, now, actor, now); err != nil {
					return err
				}
			} else if existing.Str("mode") != mode {
				if _, err := q.ExecContext(ctx, "UPDATE topic_members SET mode=?, updated_at=? WHERE topic_id=? AND unit_id=?",
					mode, now, topicID, unitID); err != nil {
					return err
				}
			}
			r, _, err := sqlitedb.QueryOne(ctx, q, "SELECT * FROM topic_members WHERE topic_id=? AND unit_id=?", topicID, unitID)
			if err != nil {
				return err
			}
			rows[unitID] = r
			return nil
		}
		included := 0
		for _, r := range rows {
			if r.Str("mode") == "include" {
				included++
			}
		}
		for _, entry := range add {
			unit, err := unitOf(entry)
			if err != nil {
				return err
			}
			if unit.Int("archived") != 0 {
				return storeErr("%s is archived", unit.Str("uid"))
			}
			if r, ok := rows[unit.Int("id")]; ok && r.Str("mode") == "include" {
				appendTo("skipped", unit.Get("uid"))
				continue
			}
			if included >= maxTopicMembers {
				return storeErr("a topic holds at most %d members", maxTopicMembers)
			}
			if err := put(unit.Int("id"), "include", entry); err != nil {
				return err
			}
			included++
			appendTo("added", unit.Get("uid"))
		}
		newRule, _ := newSettings.Value("rule").(*pyjson.Object)
		matchedIDs, err := s.ruleIDs(ctx, q, newRule, topicID)
		if err != nil {
			return err
		}
		matched := map[int64]bool{}
		for _, id := range matchedIDs {
			matched[id] = true
		}
		for _, entry := range remove {
			unit, err := unitOf(entry)
			if err != nil {
				return err
			}
			r, has := rows[unit.Int("id")]
			switch {
			case matched[unit.Int("id")]:
				if err := put(unit.Int("id"), "exclude", nil); err != nil {
					return err
				}
			case has && r.Str("mode") != "exclude":
				if _, err := q.ExecContext(ctx, "DELETE FROM topic_members WHERE topic_id=? AND unit_id=?", topicID, unit.Int("id")); err != nil {
					return err
				}
				delete(rows, unit.Int("id"))
			default:
				return storeErr("%s is not in %s", unit.Str("uid"), topic.Str("uid"))
			}
			appendTo("removed", unit.Get("uid"))
		}
		for _, entry := range exclude {
			unit, err := unitOf(entry)
			if err != nil {
				return err
			}
			if err := put(unit.Int("id"), "exclude", nil); err != nil {
				return err
			}
			appendTo("excluded", unit.Get("uid"))
		}
		for _, entry := range restore {
			unit, err := unitOf(entry)
			if err != nil {
				return err
			}
			if r, ok := rows[unit.Int("id")]; ok && r.Str("mode") == "exclude" {
				if _, err := q.ExecContext(ctx, "DELETE FROM topic_members WHERE topic_id=? AND unit_id=?", topicID, unit.Int("id")); err != nil {
					return err
				}
				delete(rows, unit.Int("id"))
				appendTo("restored", unit.Get("uid"))
			}
		}
		members, _, err := s.resolveMembers(ctx, q, topic)
		if err != nil {
			return err
		}
		visible := map[int64]bool{}
		for _, m := range members {
			visible[m.id] = true
		}
		var captions []any
		for _, entry := range items {
			unit, err := unitOf(entry)
			if err != nil {
				return err
			}
			uid := unit.Int("id")
			if !visible[uid] {
				return storeErr("%s is not in %s", unit.Str("uid"), topic.Str("uid"))
			}
			if _, ok := rows[uid]; !ok {
				if err := put(uid, "layout", nil); err != nil {
					return err
				}
			}
			var fields []string
			var args []any
			for _, key := range []string{"x", "y"} {
				if entry.Has(key) {
					c, err := coord(entry.Value(key))
					if err != nil {
						return err
					}
					fields = append(fields, key+"=?")
					args = append(args, c)
				}
			}
			if entry.Has("w") {
				w, err := cardWidth(entry.Value("w"))
				if err != nil {
					return err
				}
				fields = append(fields, "w=?")
				args = append(args, w)
			}
			if entry.Has("caption") {
				caption, err := cleanTextField(entry.Value("caption"), "caption", maxCaption, false)
				if err != nil {
					return err
				}
				if !PyEqual(caption, rows[uid].Get("caption")) {
					captions = append(captions, unit.Get("uid"))
				}
				fields = append(fields, "caption=?")
				args = append(args, caption)
			}
			if entry.Has("parent") {
				value := entry.Value("parent")
				var parentID any
				switch {
				case value == "auto":
					parentID = nil
				case value == nil || PyEqual(value, int64(0)) || value == "0" || value == "":
					parentID = int64(0)
				default:
					parent, err := unitRowAny(ctx, q, value)
					if err != nil {
						return err
					}
					if !visible[parent.Int("id")] || parent.Int("id") == uid {
						return storeErr("%s is not in %s", parent.Str("uid"), topic.Str("uid"))
					}
					parentID = parent.Int("id")
				}
				if !PyEqual(parentID, rows[uid].Get("parent_id")) {
					report.Set("outline", true)
				}
				fields = append(fields, "parent_id=?")
				args = append(args, parentID)
			}
			if len(fields) > 0 {
				args = append(args, now, topicID, uid)
				if _, err := q.ExecContext(ctx, "UPDATE topic_members SET "+strings.Join(fields, ", ")+", updated_at=? "+
					"WHERE topic_id=? AND unit_id=?", args...); err != nil {
					return err
				}
				report.Set("updated", report.Value("updated").(int64)+1)
			}
		}
		if order, ok := orderRaw.([]any); ok {
			members, _, err := s.resolveMembers(ctx, q, topic)
			if err != nil {
				return err
			}
			current := make([]int64, len(members))
			inCurrent := map[int64]bool{}
			for i, m := range members {
				current[i] = m.id
				inCurrent[m.id] = true
			}
			var wanted []int64
			inWanted := map[int64]bool{}
			for _, item := range order {
				unit, err := unitRowAny(ctx, q, item)
				if err != nil {
					return err
				}
				if inCurrent[unit.Int("id")] && !inWanted[unit.Int("id")] {
					wanted = append(wanted, unit.Int("id"))
					inWanted[unit.Int("id")] = true
				}
			}
			final := append([]int64(nil), wanted...)
			for _, id := range current {
				if !inWanted[id] {
					final = append(final, id)
				}
			}
			same := len(final) == len(current)
			for i := 0; same && i < len(final); i++ {
				same = final[i] == current[i]
			}
			if !same {
				report.Set("reordered", true)
				for index, unitID := range final {
					if _, ok := rows[unitID]; !ok {
						if err := put(unitID, "layout", nil); err != nil {
							return err
						}
					}
					if _, err := q.ExecContext(ctx, "UPDATE topic_members SET position=?, updated_at=? WHERE topic_id=? AND unit_id=?",
						int64(index), now, topicID, unitID); err != nil {
						return err
					}
				}
			}
		}
		for _, key := range []string{"added", "removed", "excluded", "restored"} {
			if list := report.Value(key).([]any); len(list) > 0 {
				changes.Set(key, list)
			}
		}
		if len(captions) > 0 {
			changes.Set("captions", captions)
		}
		if report.Value("reordered") == true {
			changes.Set("reordered", true)
		}
		if report.Value("outline") == true {
			changes.Set("outline", true)
		}
		if changes.Len() > 0 {
			detail := pyjson.NewObject("uid", topic.Get("uid"), "title", topic.Get("title"))
			for _, k := range changes.Keys() {
				detail.Set(k, changes.Value(k))
			}
			return w.log(actor, "topic.update", topicID, detail)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return report, nil
}

// RemoveTopicMember is Store.remove_topic_member.
func (s *Store) RemoveTopicMember(ctx context.Context, ref, unitRef any, actor string) (*pyjson.Object, error) {
	if _, err := s.DesignTopic(ctx, ref, pyjson.NewObject("remove", []any{unitRef}), actor); err != nil {
		return nil, err
	}
	var uid any
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		row, err := unitRowAny(ctx, q, unitRef)
		uid = row.Get("uid")
		return err
	})
	if err != nil {
		return nil, err
	}
	return pyjson.NewObject("removed", uid), nil
}
