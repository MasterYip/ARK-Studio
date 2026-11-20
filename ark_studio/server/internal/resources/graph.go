// Package resources ports arkstudio/resources_graph.py: the read-only
// resources graph (GET /api/resources/graph) and the personal-device
// preferences (POST /api/resources/devices/<id>).
//
// BuildGraph is pure, like build_graph: it works on the same "sources"
// mapping (decoded JSON values) that collect_sources produces, so the unit
// tests feed it the Python test fixtures and compare the bytes Python
// produces for them (testdata/golden.json).  Collect reads the sources;
// it never probes a GPU server, opens SSH or starts a process.
package resources

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"arkstudio/go/internal/platform/pyerr"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/setup"
)

// OnlineWindow is ONLINE_WINDOW: seconds a passive signal counts as online.
const OnlineWindow = 15 * 60

var resourceKinds = map[string]bool{"repository": true, "storage": true, "environment": true,
	"task_space": true, "compute": true}

var (
	gitHostRe = regexp.MustCompile(`^(?:[a-z+]+://)?(?:[^@/]+@)?([A-Za-z0-9.-]+\.[A-Za-z]{2,})[:/]`)
	slugRe    = regexp.MustCompile(`[^a-z0-9]+`)
)

func slug(text any) string {
	value := strings.Trim(slugRe.ReplaceAllString(pytext.Lower(pyjson.Str(or(text, ""))), "-"), "-")
	if value = pytext.Prefix(value, 48); value != "" {
		return value
	}
	sum := sha1.Sum([]byte(pyjson.Str(text)))
	return hex.EncodeToString(sum[:])[:8]
}

// GitHost is git_host: the hostname of a git remote URL or scp-style address.
func GitHost(remote any) string {
	m := gitHostRe.FindStringSubmatch(pytext.Strip(pyjson.Str(or(remote, ""))))
	if m == nil {
		return ""
	}
	return strings.ToLower(m[1])
}

// status is resources_graph.status; params are alternating key/value pairs.
func status(state, reason string, lastSeen *float64, source, code string, params ...any) *pyjson.Object {
	if code == "" {
		code = state
	}
	if source == "" {
		source = "none"
	}
	var seen any
	if lastSeen != nil {
		seen = iso(*lastSeen)
	}
	return pyjson.NewObject("state", state, "reason", reason, "code", code, "params", pyjson.NewObject(params...),
		"last_seen", seen, "source", source)
}

var signalNoun = map[string]string{"setup": "setup activity", "verification": "verification", "request": "request"}

func passiveStatus(lastSeen *float64, now float64, what, source string) *pyjson.Object {
	noun, ok := signalNoun[what]
	if !ok {
		noun = what
	}
	if lastSeen == nil {
		return status("unknown", "No "+noun+" recorded yet", nil, "none", "no_signal", "signal", what)
	}
	age := max0(now - *lastSeen)
	if age <= OnlineWindow {
		return status("online", capitalize(noun)+" "+ago(age), lastSeen, source, "recent_signal", "signal", what)
	}
	return status("stale", "Last "+noun+" "+ago(age), lastSeen, source, "old_signal", "signal", what)
}

// capitalize is str.capitalize for the ASCII nouns above.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + strings.ToLower(s[1:])
}

func fptr(v float64, ok bool) *float64 {
	if !ok {
		return nil
	}
	return &v
}

// ------------------------------------------------------------------ graph

type graph struct {
	devices     *pyjson.Object // id -> device, insertion order
	resources   []*pyjson.Object
	connections *pyjson.Object
}

func (g *graph) dev(id string) *pyjson.Object {
	d, _ := g.devices.Value(id).(*pyjson.Object)
	return d
}

// device is _Graph.device: fields are key/value pairs, None values skipped.
func (g *graph) device(id string, fields ...any) *pyjson.Object {
	item := g.dev(id)
	if item == nil {
		item = pyjson.NewObject("id", id, "label", id, "kind", "host", "scope", "shared", "owner", nil,
			"status", status("unknown", "No activity recorded yet", nil, "none", "no_signal", "signal", "setup"),
			"metrics", nil, "occupancy", nil, "probe", nil, "actions", []any{}, "sources", []any{})
		g.devices.Set(id, item)
	}
	for i := 0; i+1 < len(fields); i += 2 {
		if !isNone(fields[i+1]) {
			item.Set(fields[i].(string), fields[i+1])
		}
	}
	return item
}

func (g *graph) resource(device, kind string, label any, fields ...any) *pyjson.Object {
	item := pyjson.NewObject("id", fmt.Sprintf("%s/%s/%s/%d", device, kind, slug(label), len(g.resources)),
		"device", device, "kind", kind, "label", label, "path", nil, "redacted", false, "owner", nil,
		"setup_key", nil, "metrics", nil, "last_known", false)
	for i := 0; i+1 < len(fields); i += 2 {
		if !isNone(fields[i+1]) {
			item.Set(fields[i].(string), fields[i+1])
		}
	}
	g.resources = append(g.resources, item)
	return item
}

func (g *graph) connect(source, target, kind, label, state string) {
	key := source + "|" + target + "|" + kind
	if source == target || g.connections.Has(key) {
		return
	}
	g.connections.Set(key, pyjson.NewObject("id", key, "source", source, "target", target, "kind", kind,
		"label", label, "state", state))
}

func deviceState(d *pyjson.Object) string {
	return pyjson.Str(mustObj(d.Value("status")).Value("state"))
}

// BuildGraph is build_graph(sources, viewer).
func BuildGraph(sources *pyjson.Object, viewer string) (out *pyjson.Object, err error) {
	defer func() {
		if v := recover(); v != nil {
			ge, ok := v.(graphError)
			if !ok {
				panic(v)
			}
			out, err = nil, ge.err
		}
	}()
	return buildGraph(sources, viewer), nil
}

func buildGraph(sources *pyjson.Object, viewer string) *pyjson.Object {
	now := pyFloat(sources.Value("now"))
	g := &graph{devices: pyjson.NewObject(), connections: pyjson.NewObject()}
	server := mustObj(sources.Value("server"))
	project := mustObj(sources.Value("project"))
	unknowns := []any{}

	// 1. The ARK server host: answering this request is its heartbeat.
	ark := g.device("ark", "label", or(server.Value("hostname"), "ARK server"), "kind", "ark", "scope", "shared",
		"status", status("online", "Serving this page", &now, "heartbeat", "heartbeat"))
	disk := server.Value("disk")
	var total, used, pct, load any
	if pyjson.Truthy(disk) {
		d := mustObj(disk)
		total = roundFloat(trueDivFloat(d.Value("total"), 1e9), 1)
		used = roundFloat(trueDivFloat(d.Value("used"), 1e9), 1)
		if pyjson.Truthy(d.Value("total")) {
			pct = roundFloat(trueDiv(mul100(d.Value("used")), d.Value("total")), 1)
		}
	}
	if !isNone(server.Value("load1")) {
		load = roundN(server.Value("load1"), 2)
	}
	metrics := pyjson.NewObject("disk_total_gb", total, "disk_used_gb", used, "disk_pct", pct, "load1", load,
		"platform", server.Value("platform"))
	ark.Set("metrics", metrics)
	ark.Set("sources", []any{"heartbeat"})
	g.resource("ark", "storage", "Project folder", "path", project.Value("path"),
		"metrics", pyjson.NewObject("disk_pct", pct))
	g.resource("ark", "database", "Project database", "path", project.Value("db"))
	var running []*pyjson.Object
	sessions := iterate(sources.Value("sessions"))
	for _, s := range sessions {
		so := mustObj(s)
		switch so.Value("status") {
		case "running", "starting", "waiting":
			running = append(running, so)
		}
	}
	if !isNone(sources.Value("sessions")) {
		mine := int64(0)
		for _, s := range running {
			if pyEq(s.Value("owner"), viewer) {
				mine++
			}
		}
		g.resource("ark", "sessions", "Agent sessions", "metrics", pyjson.NewObject(
			"running", int64(len(running)), "mine", mine, "total", int64(len(sessions))))
	}
	seenPath := map[string]bool{}
	var paths []string
	for _, s := range running {
		w := s.Value("worktree")
		if !pyjson.Truthy(w) {
			continue
		}
		text, ok := w.(string)
		if !ok {
			hashKey(w) // unhashable -> TypeError
			internal("'<' not supported between instances of '" + pyerr.TypeName(w) + "' and 'str'")
		}
		if !seenPath[text] {
			seenPath[text] = true
			paths = append(paths, text)
		}
	}
	sort.Strings(paths)
	for _, path := range paths {
		g.resource("ark", "worktree", or(pathName(path), path), "path", path)
	}

	// 2. GPU servers: config + the last explicit probe.  Never probes here.
	gpu := orObj(sources.Value("gpu"))
	lastProbe := orObj(gpu.Value("last_probe"))
	probedAt, probedOK := parseTime(lastProbe.Value("probed_at"))
	results := map[string]*pyjson.Object{}
	for _, item := range iterate(lastProbe.Value("servers")) {
		o := mustObj(item)
		results[hashKey(o.Value("server"))] = o
	}
	ours := map[string]*pyjson.Object{}
	for _, item := range iterate(gpu.Value("jobs")) {
		job := mustObj(item)
		for _, index := range iterate(job.Value("gpu_indices")) {
			ours[hashKey(job.Value("server"))+"|"+fmt.Sprint(pyInt(index))] = job
		}
	}
	gpuNames := map[string]string{}
	for _, item := range iterate(gpu.Value("servers")) {
		cfg := mustObj(item)
		name := or(cfg.Value("name"), cfg.Value("host"))
		deviceID := "gpu:" + slug(name)
		gpuNames[pytext.Lower(pyjson.Str(name))] = deviceID
		if pyjson.Truthy(cfg.Value("host")) {
			gpuNames[pytext.Lower(pyjson.Str(cfg.Value("host")))] = deviceID
		}
		device := g.device(deviceID, "label", name, "kind", "gpu-server", "scope", "shared")
		device.Set("sources", []any{"gpu-config"})
		if pyjson.Truthy(gpu.Value("enabled")) {
			device.Set("actions", []any{"check"})
		} else {
			device.Set("actions", []any{})
		}
		device.Set("detail", pyjson.NewObject("host", cfg.Value("host"), "user", cfg.Value("user"),
			"tags", or(cfg.Value("tags"), []any{})))
		result := results[hashKey(name)]
		if result == nil {
			device.Set("status", status("unknown", "Not probed since the ARK server started; use Check now",
				nil, "probe", "probe_never"))
			g.connect("ark", deviceID, "ssh", "SSH", "unknown")
			continue
		}
		message := orObj(result.Value("error")).Value("message")
		device.Set("probe", pyjson.NewObject("probed_at", isoOpt(probedAt, probedOK),
			"ok", pyjson.Truthy(result.Value("ok")), "error", message))
		if !pyjson.Truthy(result.Value("ok")) {
			msg := or(message, "unreachable")
			device.Set("status", status("offline", "Last probe failed: "+pyjson.Str(msg), fptr(probedAt, probedOK),
				"probe", "probe_failed", "error", msg))
			g.connect("ark", deviceID, "ssh", "SSH", "failed")
		} else {
			at := now
			if probedOK && probedAt != 0 {
				at = probedAt
			}
			age := now - at
			if age <= OnlineWindow {
				device.Set("status", status("online", "Last probe ok "+ago(age), fptr(probedAt, probedOK), "probe", "probe_ok"))
			} else {
				device.Set("status", status("stale", "Last probe ok "+ago(age)+"; metrics may be out of date",
					fptr(probedAt, probedOK), "probe", "probe_old"))
			}
			g.connect("ark", deviceID, "ssh", "SSH", "ok")
		}
		stale := deviceState(device) != "online"
		gpuResources(g, device, result, name, ours, stale)
	}

	// 3. Setup layers: hosts, resource locations, compute.
	layers := orObj(sources.Value("layers"))
	if !pyjson.Truthy(sources.Value("layers")) {
		layers = pyjson.NewObject("project", pyjson.NewObject(), "users", []any{})
	}
	projectLayer := orObj(layers.Value("project"))
	users := orObj(sources.Value("users"))
	ownerObj := func(owner string) any {
		if v, ok := users.Get(owner); ok {
			return v
		}
		return pyjson.NewObject("username", owner, "display_name", owner)
	}
	hostOwner := map[string]string{}
	hostLabel := map[string]any{}
	var userRecords []*pyjson.Object
	for _, r := range iterate(layers.Value("users")) {
		userRecords = append(userRecords, mustObj(r))
	}
	for _, record := range userRecords {
		for _, h := range iterate(record.Value("hosts")) {
			host := mustObj(h)
			id := pyjson.Str(host.Value("id"))
			if _, ok := hostOwner[id]; !ok {
				hostOwner[id] = pyjson.Str(or(record.Value("owner"), ""))
			}
			if _, ok := hostLabel[id]; !ok {
				hostLabel[id] = or(host.Value("label"), host.Value("id"))
			}
		}
	}
	sharedHosts := map[string]bool{}
	for _, l := range iterate(projectLayer.Value("locations")) {
		sharedHosts[pyjson.Str(mustObj(l).Value("host_id"))] = true
	}
	type activity struct {
		ts   float64
		what string
	}
	lastActivity := map[string]activity{}
	seen := func(host any, when any, what string) {
		ts, ok := parseTime(when)
		if !ok {
			return
		}
		key := hashKey(host)
		prev, had := lastActivity[key]
		if !had {
			prev = activity{0, ""}
		}
		if ts > prev.ts {
			lastActivity[key] = activity{ts, what}
		}
	}
	for _, e := range iterate(sources.Value("history")) {
		event := mustObj(e)
		if pyjson.Truthy(event.Value("host")) {
			seen(event.Value("host"), event.Value("at"), "setup")
		}
	}
	allRecords := append([]*pyjson.Object{projectLayer}, userRecords...)
	for _, record := range allRecords {
		verification := orObj(record.Value("verification"))
		if pyjson.Truthy(verification.Value("host")) {
			seen(verification.Value("host"), verification.Value("at"), "verification")
		}
		for _, l := range iterate(record.Value("locations")) {
			loc := mustObj(l)
			seen(loc.Value("host_id"), loc.Value("verified_at"), "verification")
		}
	}
	fingerprint := server.Value("fingerprint")
	legacyFingerprint := server.Value("legacy_fingerprint")
	hostDevice := func(hostID string, label any) string {
		if pyEq(hostID, fingerprint) || (legacyFingerprint != nil && pyEq(hostID, legacyFingerprint)) {
			return "ark"
		}
		if m, ok := gpuNames[pytext.Lower(hostID)]; ok && m != "" {
			return m
		}
		if m, ok := gpuNames[pytext.Lower(pyjson.Str(or(label, "")))]; ok && m != "" {
			return m
		}
		deviceID := "host:" + hostID
		owner, hasOwner := hostOwner[hostID]
		personal := hasOwner && owner != "" && !sharedHosts[hostID]
		scope := "shared"
		var ownerVal any
		if personal {
			scope, ownerVal = "personal", ownerObj(owner)
		}
		device := g.device(deviceID, "label", or(or(hostLabel[hostID], label), hostID), "kind", "host",
			"scope", scope, "owner", ownerVal)
		srcs, _ := device.Value("sources").([]any)
		has := false
		for _, s := range srcs {
			has = has || s == "setup"
		}
		if !has {
			device.Set("sources", append(srcs, "setup"))
			act, ok := lastActivity[hashKey(hostID)]
			what := act.what
			if what == "" {
				what = "setup"
			}
			device.Set("status", passiveStatus(fptr(act.ts, ok), now, what, "setup"))
			if ok {
				state := "unknown"
				if now-act.ts <= OnlineWindow {
					state = "ok"
				}
				g.connect(deviceID, "ark", "agent-api", "ARK API", state)
			}
		}
		return deviceID
	}

	type setupResource struct {
		key, layer, owner string
		item              *pyjson.Object
	}
	var resources []setupResource
	for i, record := range allRecords {
		layer := pyjson.Str(record.Value("layer"))
		if !pyjson.Truthy(record.Value("layer")) {
			layer = "user"
			if i == 0 {
				layer = "project"
			}
		}
		owner := pyjson.Str(or(record.Value("owner"), ""))
		prefix := "u:" + owner
		if layer == "project" {
			prefix = "project"
		}
		for _, it := range iterate(orObj(record.Value("profile")).Value("resources")) {
			item := mustObj(it)
			if k, ok := item.Value("kind").(string); !ok || !resourceKinds[k] {
				continue
			}
			resources = append(resources, setupResource{prefix + "/" + pyjson.Str(item.Value("id")), layer, owner, item})
		}
	}
	// Go only (2026-11-20): sizes measured on each location's device.
	sizes := map[string]*pyjson.Object{}
	for _, x := range iterate(sources.Value("location_stats")) {
		if s, ok := x.(*pyjson.Object); ok {
			k := setup.StatKey(pyjson.Str(s.Value("resource")), pyjson.Str(s.Value("host_id")), pyjson.Str(s.Value("path")))
			if prev := sizes[k]; prev == nil || pyjson.Str(s.Value("measured_at")) >= pyjson.Str(prev.Value("measured_at")) {
				sizes[k] = s
			}
		}
	}
	locations := map[string][]*pyjson.Object{}
	for _, record := range allRecords {
		for _, l := range iterate(record.Value("locations")) {
			loc := mustObj(l)
			k := hashKey(loc.Value("resource"))
			locations[k] = append(locations[k], loc)
		}
	}
	unplacedMade := false
	unplacedStatus := func() *pyjson.Object {
		return status("unknown", "Resources without a registered location", nil, "none", "unplaced")
	}
	for _, r := range resources {
		details := orObj(r.item.Value("details"))
		kind := pyjson.Str(r.item.Value("kind"))
		name := or(r.item.Value("name"), r.item.Value("id"))
		var resOwner any
		if r.layer != "project" {
			resOwner = r.owner
		}
		if kind == "compute" {
			deviceID := gpuNames[pytext.Lower(pyjson.Str(or(details.Value("gpu_server"), "")))]
			if deviceID == "" {
				deviceID = "compute:" + slug(name)
				personal := r.layer == "user"
				scope := "shared"
				var ownerVal any
				if personal {
					scope, ownerVal = "personal", ownerObj(r.owner)
				}
				d := g.device(deviceID, "label", name, "kind", "compute", "scope", scope, "owner", ownerVal)
				d.Set("sources", []any{"setup"})
				ck := or(details.Value("kind"), "compute")
				d.Set("status", status("unknown", pyjson.Str(ck)+" resource; ARK does not monitor it", nil, "none",
					"unmonitored", "kind", ck))
			}
			g.resource(deviceID, "compute", name, "setup_key", r.key, "owner", resOwner,
				"metrics", pyjson.NewObject("gpus", details.Value("gpus"), "use", details.Value("use")))
			continue
		}
		resKind := kind
		if kind == "storage" && strings.Contains(pytext.Lower(pyjson.Str(or(details.Value("kind"), ""))), "database") {
			resKind = "database"
		}
		placed := false
		for _, loc := range locations[hashKey(r.key)] {
			deviceID := hostDevice(pyjson.Str(loc.Value("host_id")), or(loc.Value("host_label"), ""))
			var m any
			if pyjson.Truthy(details.Value("role")) {
				m = pyjson.NewObject("role", details.Value("role"))
			}
			var verified any
			if pyjson.Truthy(loc.Value("verified_at")) {
				verified = loc.Value("verified_at")
			}
			res := g.resource(deviceID, resKind, name, "path", loc.Value("path_or_ref"), "setup_key", r.key,
				"owner", resOwner, "verified_at", verified, "metrics", m, "layer", r.layer)
			if s := sizes[setup.StatKey(r.key, pyjson.Str(loc.Value("host_id")), pyjson.Str(loc.Value("path_or_ref")))]; s != nil {
				res.Set("size", setup.PublicStat(s))
			}
			placed = true
			if kind == "repository" {
				if remote := GitHost(or(details.Value("remote"), "")); remote != "" {
					g.connect(deviceID, "git:"+remote, "git-remote", "git remote", "unknown")
				}
			}
		}
		if kind == "repository" && pyjson.Truthy(details.Value("remote")) {
			if remote := GitHost(details.Value("remote")); remote != "" {
				remoteID := "git:" + remote
				g.device(remoteID, "label", remote, "kind", "git-host", "scope", "shared",
					"status", status("unknown", "External service; ARK does not monitor it", nil, "none", "external"))
				g.resource(remoteID, "repository", name, "path", details.Value("remote"), "setup_key", r.key,
					"owner", resOwner, "layer", r.layer)
				placed = true
			}
		}
		if !placed {
			if !unplacedMade {
				unplacedMade = true
				g.device("unplaced", "label", "Not located yet", "kind", "group", "scope", "shared",
					"status", unplacedStatus())
			}
			g.resource("unplaced", resKind, name, "setup_key", r.key, "owner", resOwner, "layer", r.layer)
		}
	}
	for _, record := range userRecords {
		for _, h := range iterate(record.Value("hosts")) {
			host := mustObj(h)
			hostDevice(pyjson.Str(host.Value("id")), or(host.Value("label"), ""))
		}
	}

	// 4. Paired phones.
	for _, p := range iterate(sources.Value("phones")) {
		phone := mustObj(p)
		deviceID := "phone:" + pyjson.Str(phone.Value("id"))
		owner := pyjson.Str(or(phone.Value("user"), ""))
		seenAt, ok := parseTime(phone.Value("last_seen_at"))
		d := g.device(deviceID, "label", or(phone.Value("label"), "Phone"), "kind", "phone", "scope", "personal",
			"owner", ownerObj(owner), "status", passiveStatus(fptr(seenAt, ok), now, "request", "phone"))
		d.Set("sources", []any{"mobile"})
		d.Set("detail", pyjson.NewObject("permission", phone.Value("permission")))
		state := "unknown"
		if deviceState(d) == "online" {
			state = "ok"
		}
		g.connect(deviceID, "ark", "paired", "paired phone", state)
	}

	// 5. Privacy: personal devices of other members.
	prefs := orObj(sources.Value("prefs"))
	hidden := map[string]bool{}
	private := map[string]bool{}
	for _, id := range g.devices.Keys() {
		device := g.dev(id)
		if device.Value("scope") != "personal" {
			continue
		}
		ownerRec := orObj(device.Value("owner"))
		owner := ownerRec.Value("username")
		if !pyjson.Truthy(owner) {
			continue
		}
		pref := orObj(orObj(prefs.Value(pyjson.Str(owner))).Value(id))
		device.Set("mine", pyEq(owner, viewer))
		if pyEq(owner, viewer) {
			device.Set("prefs", pyjson.NewObject("hidden", pyjson.Truthy(pref.Value("hidden")),
				"share_details", pyjson.Truthy(pref.Value("share_details"))))
			actions, _ := device.Value("actions").([]any)
			device.Set("actions", append(append([]any{}, actions...), "privacy"))
			continue
		}
		if pyjson.Truthy(pref.Value("hidden")) {
			hidden[id] = true
			continue
		}
		if pyjson.Truthy(pref.Value("share_details")) {
			device.Set("details_shared", true)
			continue
		}
		who := or(ownerRec.Value("display_name"), owner)
		device.Set("label", pyjson.Str(who)+"'s "+kindNoun(pyjson.Str(device.Value("kind"))))
		device.Set("status", status("unknown", "Private device; the owner has not shared its details", nil, "none", "private"))
		device.Set("metrics", nil)
		device.Set("occupancy", nil)
		device.Set("detail", nil)
		private[id] = true
	}
	outResources := []any{}
	var kept []*pyjson.Object
	for _, res := range g.resources {
		devID := pyjson.Str(res.Value("device"))
		if hidden[devID] {
			// Shared project resources stay visible, without their location.
			if res.Value("layer") == "project" {
				if !g.devices.Has("unplaced") {
					g.device("unplaced", "label", "Not located yet", "kind", "group", "scope", "shared",
						"status", unplacedStatus())
				}
				c := res.Copy()
				c.Set("device", "unplaced").Set("path", nil).Set("redacted", true)
				c.Delete("size")
				kept = append(kept, c)
			}
			continue
		}
		if private[devID] {
			if res.Value("layer") != "project" {
				continue // the owner's own resources stay private
			}
			res = res.Copy()
			res.Set("path", nil).Set("redacted", true).Set("metrics", nil).Set("verified_at", nil)
			res.Delete("size")
		}
		kept = append(kept, res)
	}
	for _, res := range kept {
		res.Delete("layer")
		devID := pyjson.Str(res.Value("device"))
		if s := deviceState(g.dev(devID)); (s == "offline" || s == "stale") && devID != "ark" {
			res.Set("last_known", true)
		}
		outResources = append(outResources, res)
	}
	devices := []any{}
	for _, id := range g.devices.Keys() {
		if !hidden[id] {
			devices = append(devices, g.dev(id))
		}
	}
	connections := []any{}
	for _, key := range g.connections.Keys() {
		c, _ := g.connections.Value(key).(*pyjson.Object)
		if !hidden[pyjson.Str(c.Value("source"))] && !hidden[pyjson.Str(c.Value("target"))] {
			connections = append(connections, c)
		}
	}
	// Codes; the UI translates them (English text in docs/operations).
	unknowns = append(unknowns, "sshfs_tunnels", "browser_host", "host_metrics")
	if !pyjson.Truthy(gpu.Value("enabled")) {
		unknowns = append(unknowns, "gpu_disabled")
	} else if orObj(gpu.Value("registration")).Value("hint_code") == "not_registered" {
		// Go only (T-0018): the list has servers, the project registers none.
		unknowns = append(unknowns, "gpu_not_registered")
	}
	return pyjson.NewObject(
		"generated_at", iso(now),
		"viewer", viewer,
		"project", pyjson.NewObject("id", project.Value("id"), "name", project.Value("name")),
		"devices", devices,
		"resources", outResources,
		"connections", connections,
		"probe", pyjson.NewObject("enabled", pyjson.Truthy(gpu.Value("enabled")), "last_probed_at", isoOpt(probedAt, probedOK)),
		"online_window_seconds", int64(OnlineWindow),
		"unknowns", unknowns,
	)
}

func kindNoun(kind string) string {
	switch kind {
	case "phone":
		return "phone"
	case "compute":
		return "compute resource"
	}
	return "device"
}

// trueDivFloat is v / f for a float divisor.
func trueDivFloat(v any, f float64) float64 { return pyFloat(v) / f }

// pathName is Path(path).name on this OS.
func pathName(path string) string {
	p := path
	if runtime.GOOS == "windows" {
		p = strings.ReplaceAll(p, `\`, "/")
		if len(p) >= 2 && p[1] == ':' {
			p = p[2:]
		}
		if strings.HasPrefix(p, "//") { // UNC anchor //server/share
			parts := strings.SplitN(strings.TrimPrefix(p, "//"), "/", 3)
			if len(parts) < 3 {
				return ""
			}
			p = parts[2]
		}
	}
	name := ""
	for _, part := range strings.Split(p, "/") {
		if part != "" && part != "." {
			name = part
		}
	}
	return name
}

func gpuResources(g *graph, device *pyjson.Object, result *pyjson.Object, name any, ours map[string]*pyjson.Object, lastKnown bool) {
	gpus := iterate(result.Value("gpus"))
	system := orObj(result.Value("system"))
	processes := iterate(system.Value("processes"))
	oursCount, busyOthers := int64(0), int64(0)
	memOurs, memOthers := 0.0, 0.0
	var utils []float64
	memTotal := 0.0
	for _, it := range gpus {
		gpu := mustObj(it)
		index := pyInt(or(gpu.Value("index"), int64(0)))
		usedMB := pyFloat(or(gpu.Value("memory_used_mb"), int64(0)))
		totalMB := pyFloat(or(gpu.Value("memory_total_mb"), int64(0)))
		job := ours[hashKey(name)+"|"+fmt.Sprint(index)]
		if job != nil && job.Len() == 0 {
			job = nil // an empty dict is falsy
		}
		mine := 0.0
		for _, pit := range processes {
			p := mustObj(pit)
			if pyEq(p.Value("gpu_index"), index) && pyjson.Truthy(p.Value("job_id")) && job != nil &&
				pyEq(p.Value("job_id"), job.Value("id")) {
				mine += pyFloat(or(p.Value("memory_mb"), int64(0)))
			}
		}
		if job != nil && mine == 0 {
			mine = usedMB // our job holds the GPU but per-process memory is unavailable
		}
		others := max0(usedMB - mine)
		memOurs += mine
		memOthers += others
		if job != nil {
			oursCount++
		} else if pyFloat(or(gpu.Value("utilization_pct"), int64(0))) >= 10 || others >= 1024 {
			busyOthers++
		}
		var jobName any
		if job != nil {
			jobName = job.Value("job")
		}
		g.resource(pyjson.Str(device.Value("id")), "gpu",
			fmt.Sprintf("GPU %d · %s", index, pyjson.Str(or(gpu.Value("name"), "GPU"))), "last_known", lastKnown,
			"metrics", pyjson.NewObject("index", index, "util_pct", gpu.Value("utilization_pct"),
				"mem_used_mb", finiteMB(usedMB), "mem_total_mb", finiteMB(totalMB),
				"mem_ours_mb", roundMB(mine), "mem_others_mb", roundMB(others),
				"temperature_c", gpu.Value("temperature_c"), "job", jobName, "ours", job != nil))
		utils = append(utils, pyFloat(or(gpu.Value("utilization_pct"), int64(0))))
		memTotal += totalMB
	}
	if system.Len() > 0 {
		cpu := system.Value("cpu_percent")
		if isNone(cpu) {
			cpu = system.Value("cpu_load_pct")
		}
		g.resource(pyjson.Str(device.Value("id")), "cpu", "CPU & memory", "last_known", lastKnown,
			"metrics", pyjson.NewObject("cpu_pct", cpu, "cores", system.Value("cores"), "load1", system.Value("load1"),
				"mem_used_mb", system.Value("memory_used_mb"), "mem_total_mb", system.Value("memory_total_mb")))
	}
	var avg any
	if len(utils) > 0 {
		sum := 0.0
		for _, u := range utils {
			sum += u
		}
		avg = roundFloat(sum/float64(len(utils)), 1)
	}
	device.Set("occupancy", pyjson.NewObject(
		"gpus_total", int64(len(gpus)), "gpus_ours", oursCount, "gpus_busy_others", busyOthers,
		"gpus_free", max(0, int64(len(gpus))-oursCount-busyOthers),
		"mem_ours_mb", roundMB(memOurs), "mem_others_mb", roundMB(memOthers),
		"mem_total_mb", roundMB(memTotal), "util_avg_pct", avg))
	device.Set("metrics", pyjson.NewObject("cpu_pct", system.Value("cpu_percent"), "load1", system.Value("load1"),
		"mem_used_mb", system.Value("memory_used_mb"), "mem_total_mb", system.Value("memory_total_mb")))
	// Go-only (tests/python/contract/go_only.py AUGMENTED): the server's
	// storage from the same probe (gpu/storage.go), only when it reported any.
	if storage, ok := result.Value("storage").([]any); ok && len(storage) > 0 {
		device.Set("storage", storage)
	}
}

// ErrGPUNotPorted: a GPU server list is configured but the GPU service is
// not ported on this build (the server answers 501).
var ErrGPUNotPorted = errors.New("GPU server list present; the GPU service is not ported")

// Go-only fix (tests/python/contract/go_only.py FIXES
// "resources-graph-nonfinite-gpu-memory"): a GPU memory figure that is
// infinite or NaN (a broken probe reply) is reported as null.  Python's
// round() raises on it and the whole graph answers 500; finite figures are
// unchanged (round half to even, floats as reported).
func roundMB(x float64) any {
	if math.IsInf(x, 0) || math.IsNaN(x) {
		return nil
	}
	return round0(x)
}

func finiteMB(x float64) any {
	if math.IsInf(x, 0) || math.IsNaN(x) {
		return nil
	}
	return x
}
