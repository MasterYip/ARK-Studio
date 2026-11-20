package setup

// Devices (T-0053, 2026-11-19): setup is repeatable per person and per
// device.  A device is a host id (X-ARK-Host; internal/hostid).  Locations
// already carry the host id; this file adds
//
//   host aliases   <setup>/host-aliases.json: an old id that means the same
//                  device as a new one (ids agents invented before the
//                  machine-id method, the server's pre-machine-id
//                  fingerprint), so their locations keep matching
//   last seen      <setup>/devices/<user>.json: when each of a person's
//                  devices last read setup (throttled)
//   devices        the payload's list of the viewer's own devices with the
//                  path each one has for every resource that needs one

import (
	"os"
	"sort"
	"strings"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/platform/textfile"
)

const (
	aliasFile    = "host-aliases.json"
	devicesDir   = "devices"
	seenInterval = 10 * time.Minute
	maxAliases   = 200
)

// hostAliases maps an old host id to the id it now means.
type hostAliases map[string]string

func (e *Env) aliasPath() string { return join(e.dir(), aliasFile) }

// aliases reads the alias file; the server's own pre-machine-id
// fingerprint is always an alias of its current id.
func (e *Env) aliases() hostAliases {
	out := hostAliases{}
	if old, now := LegacyMachineFingerprint(), MachineFingerprint(); old != "" && now != "" && old != now {
		out[old] = now
	}
	if raw := readJSON(e.aliasPath()); raw != nil {
		for _, x := range listOr(raw.Value("aliases")) {
			m := mapOf(x)
			from, to := CleanHostID(m.Value("from")), CleanHostID(m.Value("to"))
			if from != "" && to != "" && from != to {
				out[from] = to
			}
		}
	}
	return out
}

// canon follows aliases (at most a few hops, cycles stop).
func (a hostAliases) canon(id string) string {
	for i := 0; i < 5; i++ {
		next, ok := a[id]
		if !ok || next == id {
			break
		}
		id = next
	}
	return id
}

// same is whether two host ids name the same device.
func (a hostAliases) same(x, y any) bool {
	sx, sy := str(x), str(y)
	if sx == "" || sy == "" {
		return sx == sy
	}
	return a.canon(sx) == a.canon(sy)
}

// AddAlias records that host id `from` is the viewer's current device
// (v.HostID).  Allowed for a project admin, or when `from` is one of the
// viewer's own devices (a host in their layer or of a location they added).
func (e *Env) AddAlias(v *Viewer, from, actor string) (*obj, error) {
	from, to := CleanHostID(from), CleanHostID(v.HostID)
	if from == "" || to == "" {
		return nil, setupErr("from and the caller's X-ARK-Host must be host ids", 400, "SETUP_INVALID")
	}
	if from == to {
		return pyjson.NewObject("ok", true, "from", from, "to", to, "changed", false), nil
	}
	if !(v.Admin || v.Legacy) {
		own := false
		for _, d := range e.ownHostIDs(v) {
			if d == from {
				own = true
			}
		}
		if !own {
			return nil, setupErr("only your own devices (or a project admin) can be aliased", 403, "SETUP_ALIAS_FORBIDDEN")
		}
	}
	raw := readJSON(e.aliasPath())
	items := []any{}
	if raw != nil {
		for _, x := range listOr(raw.Value("aliases")) {
			if m := mapOf(x); m != nil && CleanHostID(m.Value("from")) != from {
				items = append(items, m)
			}
		}
	}
	al := e.aliases()
	if al.canon(to) == from { // would make a cycle
		return nil, setupErr("that alias would make a cycle", 409, "SETUP_ALIAS_CYCLE")
	}
	by := v.User
	if by == "" {
		by = actor
	}
	items = append(items, pyjson.NewObject("from", from, "to", to, "by", by, "at", utcNow()))
	if len(items) > maxAliases {
		items = items[len(items)-maxAliases:]
	}
	if err := writePlain(e.aliasPath(), pyjson.NewObject("aliases", items)); err != nil {
		return nil, err
	}
	return pyjson.NewObject("ok", true, "from", from, "to", to, "changed", true), nil
}

func writePlain(path string, o *obj) error {
	if err := os.MkdirAll(pathParent(path), 0o755); err != nil {
		return err
	}
	tmp := strings.TrimSuffix(path, ".json") + ".tmp"
	if err := os.WriteFile(tmp, textfile.Encode(string(pyjson.DumpsIndent(o, 2))+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (e *Env) seenPath(user string) string {
	return join(join(e.dir(), devicesDir), userSlug(user)+".json")
}

// TouchHost records that the viewer's device read setup now (at most every
// ten minutes per device).  Only for a person with an explicit host id.
func (e *Env) TouchHost(v *Viewer, label string) {
	if v.User == "" || v.Legacy || v.HostID == "" || v.HostSource == "" {
		return
	}
	path := e.seenPath(v.User)
	raw := readJSON(path)
	if raw == nil {
		raw = pyjson.NewObject()
	}
	hosts := mapOf(raw.Value("hosts"))
	if hosts == nil {
		hosts = pyjson.NewObject()
	}
	now := time.Now().UTC()
	prev := mapOf(hosts.Value(v.HostID))
	if prev != nil {
		if t, err := time.Parse(time.RFC3339, str(prev.Value("last_seen"))); err == nil && now.Sub(t) < seenInterval {
			return
		}
	}
	entry := pyjson.NewObject("last_seen", now.Format(time.RFC3339))
	if label = text(label, 60); label != "" {
		entry.Set("label", label)
	} else if prev != nil && pyjson.Truthy(prev.Value("label")) {
		entry.Set("label", prev.Value("label"))
	}
	hosts.Set(v.HostID, entry)
	_ = writePlain(path, raw.Set("hosts", hosts))
}

// ownHostIDs is every host id that is the viewer's: hosts of their layer,
// locations they added in any layer, devices that read setup as them.
func (e *Env) ownHostIDs(v *Viewer) []string {
	seen := map[string]bool{}
	var out []string
	add := func(id any) {
		if h := CleanHostID(id); h != "" && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	if v.User == "" {
		return out
	}
	if rec, err := e.loadUser(v.User, nil); err == nil && rec != nil {
		for _, x := range listOr(rec.Value("hosts")) {
			add(mapOf(x).Value("id"))
		}
	}
	records := []*obj{}
	if p, err := e.loadProject(); err == nil {
		records = append(records, p)
	}
	if all, err := e.allUserLayers(); err == nil {
		records = append(records, all...)
	}
	for _, rec := range records {
		for _, x := range listOr(rec.Value("locations")) {
			if loc := mapOf(x); loc != nil && v.isUser(loc.Value("added_by")) {
				add(loc.Value("host_id"))
			}
		}
	}
	if raw := readJSON(e.seenPath(v.User)); raw != nil {
		if hosts := mapOf(raw.Value("hosts")); hosts != nil {
			for _, k := range hosts.Keys() {
				add(k)
			}
		}
	}
	return out
}

// devices is the viewer's own devices: label, last seen, whether it is the
// device asking, and per resource that needs a path, the path on that
// device (or nil).  Aliased ids fold into the id they point to.
func (e *Env) devices(vw *view, v *Viewer, al hostAliases) []any {
	if v.User == "" || v.Legacy {
		return []any{}
	}
	type dev struct {
		id, label, lastSeen string
	}
	byID := map[string]*dev{}
	order := []string{}
	dv := func(id string) *dev {
		id = al.canon(id)
		d, ok := byID[id]
		if !ok {
			d = &dev{id: id}
			byID[id] = d
			order = append(order, id)
		}
		return d
	}
	ids := e.ownHostIDs(v)
	if v.HostID != "" && v.HostSource != "" {
		ids = append(ids, v.HostID)
	}
	for _, id := range ids {
		dv(id)
	}
	if vw.mine != nil {
		for _, x := range listOr(vw.mine.Value("hosts")) {
			m := mapOf(x)
			if id := CleanHostID(m.Value("id")); id != "" {
				if d := dv(id); d.label == "" {
					d.label = str(m.Value("label"))
				}
			}
		}
	}
	if raw := readJSON(e.seenPath(v.User)); raw != nil {
		if hosts := mapOf(raw.Value("hosts")); hosts != nil {
			for _, k := range hosts.Keys() {
				m := mapOf(hosts.Value(k))
				d := dv(k)
				if ls := str(m.Value("last_seen")); ls > d.lastSeen {
					d.lastSeen = ls
				}
				if d.label == "" && pyjson.Truthy(m.Value("label")) {
					d.label = str(m.Value("label"))
				}
			}
		}
	}
	out := []any{}
	for _, id := range order {
		d := byID[id]
		if d.label == "" {
			d.label = defaultHostLabel(id)
		}
		paths := []any{}
		missing := []any{}
		for _, item := range vw.full {
			if !needsLocation(item) {
				continue
			}
			mineOrProject := item.Value("layer") == "project" || v.isUser(item.Value("owner"))
			var here *obj
			remote := false
			for _, l := range listOr(item.Value("locations")) {
				loc := mapOf(l)
				if al.same(loc.Value("host_id"), id) {
					here = loc
				} else if p := str(loc.Value("path_or_ref")); !IsLocalPath(p) && p != "local" {
					remote = true
				}
			}
			var path any
			if here != nil {
				path = here.Value("path_or_ref")
			}
			paths = append(paths, pyjson.NewObject("resource", item.Value("key"), "id", item.Value("id"),
				"name", or(item.Value("name"), item.Value("id")), "kind", item.Value("kind"), "path_or_ref", path))
			if here == nil && !remote && mineOrProject {
				missing = append(missing, item.Value("key"))
			}
		}
		var lastSeen any
		if d.lastSeen != "" {
			lastSeen = d.lastSeen
		}
		out = append(out, pyjson.NewObject("id", id, "label", d.label, "last_seen", lastSeen,
			"current", v.HostID != "" && al.same(id, v.HostID), "paths", paths, "missing", missing))
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].(*obj), out[j].(*obj)
		if a.Value("current") != b.Value("current") {
			return a.Value("current") == true
		}
		return str(a.Value("last_seen")) > str(b.Value("last_seen"))
	})
	return out
}
