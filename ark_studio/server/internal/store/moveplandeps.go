package store

// What a move or import plan depends on (T-0068).  A plan is stale only when
// something it covers changed: a planned source unit (its row and tags), the
// set of planned units (deleted, moved, new children), the links among them
// or to other units, their attachments and comments, and for an import which
// units already have a copy here.  Edits elsewhere in either project do not
// count, and new uids are not part of it either: they are assigned when the
// plan is applied, inside the apply transaction (the numbers a plan shows
// are provisional).

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
)

type planDeps struct {
	head     string            // the request settings
	units    map[string]string // source uid -> fingerprint
	links    string            // links among the planned units
	cross    string            // links between planned units and others (move)
	media    string            // their attachments
	comments string            // their comments (move)
	skipped  []string          // import: planned units with a copy here already
}

func fingerprint(parts ...any) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%v\x1f", p)
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

func (d *planDeps) object() *pyjson.Object {
	uids := make([]string, 0, len(d.units))
	for uid := range d.units {
		uids = append(uids, uid)
	}
	sort.Strings(uids)
	units := pyjson.NewObject()
	for _, uid := range uids {
		units.Set(uid, d.units[uid])
	}
	skipped := append([]string{}, d.skipped...)
	sort.Strings(skipped)
	return pyjson.NewObject("head", d.head, "units", units, "links", d.links, "cross", d.cross,
		"media", d.media, "comments", d.comments, "skipped", stringsAny(skipped))
}

func depsFrom(o *pyjson.Object) *planDeps {
	if o == nil {
		return nil
	}
	d := &planDeps{head: str(o, "head"), units: map[string]string{}, links: str(o, "links"), cross: str(o, "cross"),
		media: str(o, "media"), comments: str(o, "comments"), skipped: strList(o.Value("skipped"))}
	if u, ok := o.Value("units").(*pyjson.Object); ok {
		for _, k := range u.Keys() {
			d.units[k] = pyjson.Str(u.Value(k))
		}
	}
	return d
}

// digest is the plan_hash.
func (d *planDeps) digest() string {
	return fingerprint(string(pyjson.Dumps(d.object())))
}

// StaleReason is one thing that changed since a plan was made.
type StaleReason struct {
	Code   string   // edited, gone, added, links, cross, media, comments, skipped, ids_taken, settings, unavailable, outdated
	Units  []string // the source uids concerned (when known)
	Detail string
}

var staleTexts = map[string]string{
	"edited":      "planned nodes were edited",
	"gone":        "planned nodes left the plan (deleted, moved, or no longer children)",
	"added":       "nodes joined the plan (new children)",
	"links":       "links among the planned nodes changed",
	"cross":       "links between the planned nodes and other nodes changed",
	"media":       "attachments of the planned nodes changed",
	"comments":    "comments on the planned nodes changed",
	"skipped":     "nodes copied here before changed",
	"ids_taken":   "new IDs were taken in the target while the plan was applied",
	"settings":    "the plan settings changed",
	"unavailable": "the plan cannot be made any more",
	"outdated":    "the plan was made by an earlier ARK version",
}

func (r StaleReason) message() string {
	m := staleTexts[r.Code]
	if r.Detail != "" {
		m += ": " + r.Detail
	} else if len(r.Units) > 0 {
		list := r.Units
		if len(list) > 10 {
			list = append(append([]string{}, list[:10]...), "…")
		}
		m += ": " + strings.Join(list, ", ")
	}
	return m
}

// Object is the reason as the API returns it.
func (r StaleReason) Object() *pyjson.Object {
	return pyjson.NewObject("code", r.Code, "units", stringsAny(r.Units), "message", r.message())
}

// diffDeps names what changed from old to now (nil: nothing).
func diffDeps(old, now *planDeps) []StaleReason {
	if old == nil {
		return []StaleReason{{Code: "outdated"}}
	}
	var out []StaleReason
	if old.head != now.head {
		out = append(out, StaleReason{Code: "settings"})
	}
	var edited, gone, added []string
	for uid, f := range old.units {
		g, ok := now.units[uid]
		switch {
		case !ok:
			gone = append(gone, uid)
		case g != f:
			edited = append(edited, uid)
		}
	}
	for uid := range now.units {
		if _, ok := old.units[uid]; !ok {
			added = append(added, uid)
		}
	}
	for _, x := range []struct {
		code string
		list []string
	}{{"edited", edited}, {"gone", gone}, {"added", added}} {
		if len(x.list) > 0 {
			sort.Strings(x.list)
			out = append(out, StaleReason{Code: x.code, Units: x.list})
		}
	}
	for _, x := range []struct {
		code      string
		old, curr string
	}{{"links", old.links, now.links}, {"cross", old.cross, now.cross}, {"media", old.media, now.media},
		{"comments", old.comments, now.comments}} {
		if x.old != x.curr {
			out = append(out, StaleReason{Code: x.code})
		}
	}
	a, b := map[string]bool{}, map[string]bool{}
	for _, u := range old.skipped {
		a[u] = true
	}
	for _, u := range now.skipped {
		b[u] = true
	}
	var changed []string
	for u := range a {
		if !b[u] {
			changed = append(changed, u)
		}
	}
	for u := range b {
		if !a[u] {
			changed = append(changed, u)
		}
	}
	if len(changed) > 0 {
		sort.Strings(changed)
		out = append(out, StaleReason{Code: "skipped", Units: changed})
	}
	return out
}

// deps of a split or merge: source side only.
func (p *movePlan) deps() *planDeps {
	d := &planDeps{head: fingerprint("move", p.req.SourceID, p.req.CrossLinks, p.req.All, p.req.WithChildren), units: map[string]string{}}
	for _, u := range p.units {
		d.units[u.oldUID] = fingerprint(string(pyjson.Dumps(u.row.Vals)), strings.Join(p.tags[u.id], ","))
	}
	var links, cross, media, comments []any
	for _, l := range p.links {
		links = append(links, string(pyjson.Dumps(l.Vals)))
	}
	for _, c := range p.cross {
		cross = append(cross, string(pyjson.Dumps(c.row.Vals)))
	}
	for _, m := range p.media {
		media = append(media, string(pyjson.Dumps(m.row.Vals)), m.borrowed)
	}
	for _, c := range p.comments {
		comments = append(comments, string(pyjson.Dumps(c.Vals)))
	}
	d.links, d.cross, d.media, d.comments = fingerprint(links...), fingerprint(cross...), fingerprint(media...), fingerprint(comments...)
	return d
}

// deps of an import: the source side and which units have a copy here.
func (p *importPlan) deps() *planDeps {
	d := &planDeps{head: fingerprint("import", p.req.SourceID, p.req.WithChildren), units: map[string]string{}}
	for _, u := range p.units {
		d.units[u.oldUID] = fingerprint(string(pyjson.Dumps(u.row.Vals)), strings.Join(p.tags[u.id], ","))
		if u.existing {
			d.skipped = append(d.skipped, u.oldUID)
		}
	}
	var links, media []any
	for _, l := range p.links {
		links = append(links, string(pyjson.Dumps(l.Vals)))
	}
	for _, m := range p.media {
		media = append(media, string(pyjson.Dumps(m.row.Vals)), m.owner)
	}
	d.links, d.media = fingerprint(links...), fingerprint(media...)
	return d
}
