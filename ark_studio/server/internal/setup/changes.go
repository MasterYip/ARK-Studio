package setup

import (
	"bufio"
	"os"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
)

// SnapshotKey holds a layer's last confirmed version inside its record:
// {at, digest, profile, locations}.  Confirming writes it; later writes
// carry it along, so the review can show what changed since then.
const SnapshotKey = "last_confirmed"

// snapshotOf is the record as confirmed at `at`.
func snapshotOf(record *obj, at string) *obj {
	locs := []any{}
	for _, x := range listOr(record.Value("locations")) {
		if m := mapOf(x); m != nil {
			locs = append(locs, m.Copy())
		}
	}
	return pyjson.NewObject("at", at, "digest", record.Value("digest"),
		"profile", deepCopy(profileOf(record)), "locations", locs)
}

// deepCopy is a JSON round trip (records hold only JSON values).
func deepCopy(o *obj) any {
	if o == nil {
		return pyjson.NewObject()
	}
	v, err := pyjson.Loads(string(pyjson.Dumps(o)))
	if err != nil {
		return o.Copy()
	}
	return v
}

// carriedSnapshot is the last confirmed version a write keeps: the stored
// one, or (for records confirmed before snapshots existed) the previous
// record itself while it was still confirmed, without the locations added
// after the confirmation.
func carriedSnapshot(previous *obj) *obj {
	if snap := mapOf(previous.Value(SnapshotKey)); snap != nil {
		return snap
	}
	if !inAny(previous.Value("state"), ReadyStates) {
		return nil
	}
	at := orStr(get(orMap(previous.Value("confirmation")), "at"))
	snap := snapshotOf(previous, at)
	kept := []any{}
	for _, x := range listOr(snap.Value("locations")) {
		if added := orStr(x.(*obj).Value("added_at")); at == "" || added == "" || added <= at {
			kept = append(kept, x)
		}
	}
	return snap.Set("locations", kept)
}

// lastConfirmedAt is the time of the layer's last "confirmed" event in
// history.jsonl ("" when there is none).
func (e *Env) lastConfirmedAt(name string) string {
	f, err := os.Open(join(e.dir(), HistoryFile))
	if err != nil {
		return ""
	}
	defer f.Close()
	last := ""
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scan.Scan() {
		line := scan.Text()
		if !strings.Contains(line, `"confirmed"`) {
			continue
		}
		v, err := pyjson.Loads(line)
		if err != nil {
			continue
		}
		if m := mapOf(v); m != nil && m.Value("event") == "confirmed" && m.Value("layer") == name {
			last = orStr(m.Value("at"))
		}
	}
	return last
}

// display is a field value as one line of text for the review.
func display(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []any:
		parts := []string{}
		for _, item := range x {
			if s := display(item); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ", ")
	case *obj:
		for _, k := range []string{"name", "id", "question", "text", "title"} {
			if s, ok := x.Value(k).(string); ok && s != "" {
				return s
			}
		}
		return string(pyjson.Dumps(x))
	}
	return string(pyjson.Dumps(v))
}

func same(a, b any) bool { return string(sortedJSON(nil, a)) == string(sortedJSON(nil, b)) }

func fieldChange(field string, old, new any) *obj {
	return pyjson.NewObject("field", field, "old", display(old), "new", display(new))
}

// resourceFields compares two versions of a resource: name, purpose, owner
// and every details field.
func resourceFields(old, new *obj) []any {
	out := []any{}
	for _, k := range []string{"name", "purpose", "owner"} {
		if !same(old.Value(k), new.Value(k)) {
			out = append(out, fieldChange(k, old.Value(k), new.Value(k)))
		}
	}
	od, nd := orMap(mapOf(old.Value("details"))), orMap(mapOf(new.Value("details")))
	keys := append([]string{}, nd.Keys()...)
	for _, k := range od.Keys() {
		if !nd.Has(k) {
			keys = append(keys, k)
		}
	}
	for _, k := range keys {
		if !same(od.Value(k), nd.Value(k)) {
			out = append(out, fieldChange(k, od.Value(k), nd.Value(k)))
		}
	}
	return out
}

func resourceEntry(layer, owner string, r *obj, change string) *obj {
	return pyjson.NewObject("key", resourceKey(layer, owner, str(r.Value("id"))), "id", r.Value("id"),
		"kind", r.Value("kind"), "name", or(r.Value("name"), r.Value("id")), "change", change)
}

func locationEntry(l *obj, change string) *obj {
	return pyjson.NewObject("resource", l.Value("resource"), "host_id", l.Value("host_id"),
		"host_label", l.Value("host_label"), "path_or_ref", l.Value("path_or_ref"), "change", change)
}

func locIdentity(l *obj) string {
	return pyjson.Repr([]any{l.Value("resource"), l.Value("host_id"), l.Value("path_or_ref")})
}

// Changes is what changed in a layer since its last confirmed version:
//
//	{baseline, since, count, fields, policies, resources, locations}
//
// baseline is "confirmed" (an exact snapshot), "history" (no snapshot: the
// time of the last confirmation marks the resources and locations added
// since; changed fields cannot be told apart) or "none" (never confirmed).
// fields and policies hold {field, old, new}; resources {key, id, kind,
// name, change, fields?} with change added / changed / removed; locations
// {resource, host_id, host_label, path_or_ref, change}.
func (e *Env) Changes(record *obj) *obj {
	layer, owner := str(record.Value("layer")), orStr(record.Value("owner"))
	fields, policies, resources, locations := []any{}, []any{}, []any{}, []any{}
	out := pyjson.NewObject("baseline", "none", "since", "")
	finishOut := func() *obj {
		out.Set("fields", fields).Set("policies", policies).Set("resources", resources).Set("locations", locations)
		return out.Set("count", int64(len(fields)+len(policies)+len(resources)+len(locations)))
	}
	profile := orMap(profileOf(record))
	current := listOr(profile.Value("resources"))
	snap := mapOf(record.Value(SnapshotKey))
	if snap == nil && inAny(record.Value("state"), ReadyStates) {
		snap = carriedSnapshot(record)
	}
	if snap == nil {
		since := e.lastConfirmedAt(layerName(layer, owner))
		if since == "" {
			return finishOut()
		}
		out.Set("baseline", "history").Set("since", since)
		for _, x := range listOr(record.Value("locations")) {
			l := x.(*obj)
			if added := orStr(l.Value("added_at")); added > since {
				locations = append(locations, locationEntry(l, "added"))
			}
		}
		for _, x := range current {
			r := x.(*obj)
			key := resourceKey(layer, owner, str(r.Value("id")))
			mine, fresh := 0, 0
			for _, y := range listOr(record.Value("locations")) {
				l := y.(*obj)
				if l.Value("resource") == key {
					mine++
					if orStr(l.Value("added_at")) > since {
						fresh++
					}
				}
			}
			if mine > 0 && fresh == mine {
				resources = append(resources, resourceEntry(layer, owner, r, "added"))
			}
		}
		return finishOut()
	}
	out.Set("baseline", "confirmed").Set("since", orStr(snap.Value("at")))
	before := orMap(mapOf(snap.Value("profile")))
	for _, k := range before.Keys() {
		if !profile.Has(k) && k != "resources" && k != "policies" && k != "policies_defaulted" {
			fields = append(fields, fieldChange(k, before.Value(k), nil))
		}
	}
	for _, k := range profile.Keys() {
		if k == "resources" || k == "policies" || k == "policies_defaulted" {
			continue
		}
		if !same(before.Value(k), profile.Value(k)) {
			fields = append(fields, fieldChange(k, before.Value(k), profile.Value(k)))
		}
	}
	op, np := orMap(mapOf(before.Value("policies"))), orMap(mapOf(profile.Value("policies")))
	keys := append([]string{}, PolicyKeys...)
	for _, k := range append(np.Keys(), op.Keys()...) {
		if !in(k, keys) {
			keys = append(keys, k)
		}
	}
	for _, k := range keys {
		if !same(op.Value(k), np.Value(k)) {
			policies = append(policies, fieldChange(k, op.Value(k), np.Value(k)))
		}
	}
	old := map[string]*obj{}
	for _, x := range listOr(before.Value("resources")) {
		if r := mapOf(x); r != nil {
			old[str(r.Value("id"))] = r
		}
	}
	seen := map[string]bool{}
	for _, x := range current {
		r := x.(*obj)
		id := str(r.Value("id"))
		seen[id] = true
		prev, had := old[id]
		switch {
		case !had:
			resources = append(resources, resourceEntry(layer, owner, r, "added"))
		case !same(prev, r):
			resources = append(resources, resourceEntry(layer, owner, r, "changed").Set("fields", resourceFields(prev, r)))
		}
	}
	for _, x := range listOr(before.Value("resources")) {
		if r := mapOf(x); r != nil && !seen[str(r.Value("id"))] {
			resources = append(resources, resourceEntry(layer, owner, r, "removed"))
		}
	}
	was := map[string]bool{}
	for _, x := range listOr(snap.Value("locations")) {
		if l := mapOf(x); l != nil {
			was[locIdentity(l)] = true
		}
	}
	now := map[string]bool{}
	for _, x := range listOr(record.Value("locations")) {
		l := x.(*obj)
		now[locIdentity(l)] = true
		if !was[locIdentity(l)] {
			locations = append(locations, locationEntry(l, "added"))
		}
	}
	for _, x := range listOr(snap.Value("locations")) {
		if l := mapOf(x); l != nil && !now[locIdentity(l)] {
			locations = append(locations, locationEntry(l, "removed"))
		}
	}
	return finishOut()
}
