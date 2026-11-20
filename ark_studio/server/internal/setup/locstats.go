package setup

// Location sizes (2026-11-20): how much each registered resource location
// holds on its device (bytes, files; for a git checkout the .git share).
//
//   <setup>/location-stats.json   {"stats": [stat, ...]}, one per location
//                                 (resource, host id, path), newest wins
//
// A path lives on one device, so only that device can measure it: the ARK
// server measures the locations on its own host (internal/server
// locsizes.go), the desktop app and agents report the locations of the
// device they run on (POST .../setup/location-stats).  The file is not a
// setup layer: writing it never changes a digest or a confirmation.

import (
	"sort"
	"strings"
	"sync"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/platform/pathx"
)

const (
	statsFile      = "location-stats.json"
	maxStats       = 2000
	maxStatReports = 100
	maxStatBytes   = int64(1) << 60
	// StatStaleAfter is when the UI marks a size as old.
	StatStaleAfter = 24 * time.Hour
)

// StatSources are who measured a size.
var StatSources = map[string]bool{"server": true, "desktop": true, "agent": true}

var statKinds = map[string]bool{"dir": true, "file": true, "missing": true, "network": true, "error": true}

var statsMu sync.Mutex

func statsPath(projectPath string) string { return join(join(projectPath, ProfileDir), statsFile) }

// StatKey identifies a location's size.
func StatKey(resource, hostID, path string) string { return resource + "\x00" + hostID + "\x00" + path }

// ReadLocationStats is every stored size of the project (nil when none).
func ReadLocationStats(projectPath string) []*obj {
	raw := readJSON(statsPath(projectPath))
	if raw == nil {
		return nil
	}
	var out []*obj
	for _, x := range listOr(raw.Value("stats")) {
		if m := mapOf(x); m != nil && str(m.Value("resource")) != "" && CleanHostID(m.Value("host_id")) != "" {
			out = append(out, m)
		}
	}
	return out
}

// StatLookup answers the stored size of a location (nil when unmeasured).
// Host ids follow the project's aliases.
func (e *Env) StatLookup() func(resource, hostID, path string) *obj {
	stats := ReadLocationStats(e.Project)
	if len(stats) == 0 {
		return func(string, string, string) *obj { return nil }
	}
	al := e.aliases()
	idx := map[string]*obj{}
	for _, s := range stats {
		k := StatKey(str(s.Value("resource")), al.canon(str(s.Value("host_id"))), str(s.Value("path")))
		if prev := idx[k]; prev == nil || str(s.Value("measured_at")) >= str(prev.Value("measured_at")) {
			idx[k] = s
		}
	}
	return func(resource, hostID, path string) *obj {
		return idx[StatKey(resource, al.canon(hostID), path)]
	}
}

// PublicStat is the stat as the UI and agents read it.
func PublicStat(s *obj) *obj {
	if s == nil {
		return nil
	}
	out := pyjson.NewObject()
	for _, k := range []string{"kind", "bytes", "files", "git", "git_bytes", "git_files", "capped", "skipped",
		"error", "measured_at", "source", "by"} {
		if v, ok := s.Get(k); ok {
			out.Set(k, v)
		}
	}
	return out
}

// PutLocationStat stores (replaces) the size of one location.
func (e *Env) PutLocationStat(stat *obj) error {
	statsMu.Lock()
	defer statsMu.Unlock()
	key := StatKey(str(stat.Value("resource")), str(stat.Value("host_id")), str(stat.Value("path")))
	items := []any{}
	for _, s := range ReadLocationStats(e.Project) {
		if StatKey(str(s.Value("resource")), str(s.Value("host_id")), str(s.Value("path"))) != key {
			items = append(items, s)
		}
	}
	items = append(items, stat)
	if len(items) > maxStats {
		sort.SliceStable(items, func(i, j int) bool {
			return str(items[i].(*obj).Value("measured_at")) < str(items[j].(*obj).Value("measured_at"))
		})
		items = items[len(items)-maxStats:]
	}
	return writePlain(statsPath(e.Project), pyjson.NewObject("stats", items))
}

// Location is one registered location (any layer).
type Location struct {
	Resource, HostID, Path string
}

// allLocations is every location of every layer.
func (e *Env) allLocations() []Location {
	records := []*obj{}
	if p, err := e.loadProject(); err == nil && p != nil {
		records = append(records, p)
	}
	if all, err := e.allUserLayers(); err == nil {
		records = append(records, all...)
	}
	var out []Location
	for _, rec := range records {
		for _, x := range listOr(rec.Value("locations")) {
			if loc := mapOf(x); loc != nil {
				out = append(out, Location{str(loc.Value("resource")), str(loc.Value("host_id")), str(loc.Value("path_or_ref"))})
			}
		}
	}
	return out
}

// HostLocations are the distinct locations on device hostID (aliases
// followed) whose path is a local file system path.
func (e *Env) HostLocations(hostID string) []Location {
	al := e.aliases()
	seen := map[string]bool{}
	var out []Location
	for _, l := range e.allLocations() {
		if !al.same(l.HostID, hostID) || !IsLocalPath(l.Path) {
			continue
		}
		k := StatKey(l.Resource, l.HostID, l.Path)
		if !seen[k] {
			seen[k] = true
			out = append(out, l)
		}
	}
	return out
}

// FindLocations are the registered locations of resource with that path
// (any device; every path of the resource when path is empty).
func (e *Env) FindLocations(resource, path string) []Location {
	var out []Location
	for _, l := range e.allLocations() {
		if l.Resource == resource && (path == "" || l.Path == path) {
			out = append(out, l)
		}
	}
	return out
}

// SameHost is whether two host ids name the same device (aliases followed).
func (e *Env) SameHost(a, b string) bool { return e.aliases().same(a, b) }

// ExpandPath is the path a location names on its own device ("~" expanded).
func ExpandPath(p string) string { return pathx.ExpandUser(p) }

// StatAge is how old a stored size is (ok false when it has no valid time).
func StatAge(s *obj, now time.Time) (time.Duration, bool) {
	if s == nil {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339, str(s.Value("measured_at")))
	if err != nil {
		return 0, false
	}
	return now.Sub(t), true
}

// NeedsMeasure is whether a location's size should be measured again: never
// measured, older than maxAge, or with no valid time.
func NeedsMeasure(s *obj, now time.Time, maxAge time.Duration) bool {
	age, ok := StatAge(s, now)
	return !ok || age >= maxAge
}

func statInt(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, x >= 0 && x <= maxStatBytes
	case int:
		return int64(x), x >= 0 && int64(x) <= maxStatBytes
	case float64:
		if x != x || x < 0 || x > float64(maxStatBytes) || x != float64(int64(x)) {
			return 0, false
		}
		return int64(x), true
	case nil:
		return 0, true
	}
	return 0, false
}

// cleanReport validates one reported size; measured_at defaults to now.
func cleanReport(item *obj, now time.Time) (*obj, string) {
	if item == nil {
		return nil, "each report must be an object"
	}
	out := pyjson.NewObject()
	kind := strings.ToLower(text(item.Value("kind"), 20))
	if kind == "" {
		kind = "dir"
	}
	if !statKinds[kind] {
		return nil, "kind must be dir, file, missing, network or error"
	}
	out.Set("kind", kind)
	for _, k := range []string{"bytes", "files", "git_bytes", "git_files", "skipped"} {
		n, ok := statInt(item.Value(k))
		if !ok {
			return nil, k + " must be a non-negative integer"
		}
		if k == "bytes" || k == "files" || n > 0 {
			out.Set(k, n)
		}
	}
	if _, ok := item.Get("bytes"); !ok && (kind == "dir" || kind == "file") {
		return nil, "bytes is required"
	}
	gb, _ := statInt(item.Value("git_bytes"))
	b, _ := statInt(item.Value("bytes"))
	if gb > b {
		return nil, "git_bytes cannot exceed bytes"
	}
	if pyjson.Truthy(item.Value("git")) || gb > 0 {
		out.Set("git", true)
	}
	switch c := item.Value("capped").(type) {
	case bool:
		if c {
			out.Set("capped", "time")
		}
	case string:
		if c == "time" || c == "entries" {
			out.Set("capped", c)
		} else if c != "" {
			return nil, "capped must be true, time or entries"
		}
	}
	if e := text(item.Value("error"), 200); e != "" {
		out.Set("error", e)
	}
	at := now
	if raw := text(item.Value("measured_at"), 40); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, "measured_at must be an ISO 8601 time with a zone"
		}
		if t.After(now.Add(5 * time.Minute)) {
			return nil, "measured_at is in the future"
		}
		if now.Sub(t) > 30*24*time.Hour {
			return nil, "measured_at is older than 30 days"
		}
		at = t
	}
	out.Set("measured_at", at.UTC().Format("2006-01-02T15:04:05")+"+00:00")
	return out, ""
}

// ReportLocationStats stores sizes measured on the caller's device
// (v.HostID, given explicitly as X-ARK-Host or host_id).  Each report names
// a resource key and, when the resource has several paths on the device,
// the path; it must match a location registered on that device.  body is
// {"items": [report, ...]} or one report.
func (e *Env) ReportLocationStats(v *Viewer, body *obj, actor, source string) (*obj, error) {
	if !v.CanWrite && !v.Legacy {
		return nil, setupErr("reporting sizes needs write access to the project", 403, "SETUP_FORBIDDEN")
	}
	if v.HostSource == "server" || CleanHostID(v.HostID) == "" {
		return nil, setupErr("send this device's id as X-ARK-Host (or host_id): sizes are reported by the device that holds the path", 400, "SETUP_HOST_REQUIRED")
	}
	if !StatSources[source] {
		source = "agent"
	}
	var items []any
	if l, ok := body.Value("items").([]any); ok {
		items = l
	} else {
		items = []any{body}
	}
	if len(items) == 0 {
		return nil, setupErr("items is empty", 400, "SETUP_INVALID")
	}
	if len(items) > maxStatReports {
		return nil, setupErr("at most 100 sizes per request", 400, "SETUP_INVALID")
	}
	al := e.aliases()
	locs := e.allLocations()
	now := time.Now().UTC()
	stored, errs := []any{}, []any{}
	for i, x := range items {
		item := mapOf(x)
		resource := text(item.Value("resource"), 200)
		path := text(or(item.Value("path_or_ref"), item.Value("path")), 500)
		fail := func(msg string) {
			errs = append(errs, pyjson.NewObject("index", int64(i), "resource", resource, "error", msg))
		}
		if resource == "" {
			fail("resource is required")
			continue
		}
		if h := CleanHostID(item.Value("host_id")); h != "" && !al.same(h, v.HostID) {
			fail("host_id is not this device; report sizes from the device that holds the path")
			continue
		}
		match, msg := findLocation(locs, al, resource, v.HostID, path)
		if msg != "" {
			fail(msg)
			continue
		}
		stat, msg := cleanReport(item, now)
		if msg != "" {
			fail(msg)
			continue
		}
		stat.Set("resource", match.Resource).Set("host_id", match.HostID).Set("path", match.Path).
			Set("source", source).Set("by", text(actor, 120))
		if err := e.PutLocationStat(rekey(stat)); err != nil {
			return nil, err
		}
		stored = append(stored, pyjson.NewObject("resource", match.Resource, "path_or_ref", match.Path,
			"stats", PublicStat(stat)))
	}
	if len(stored) == 0 {
		first := mapOf(errs[0])
		return nil, setupErr(str(first.Value("error")), 404, "SETUP_NO_LOCATION")
	}
	return pyjson.NewObject("ok", true, "stored", stored, "errors", errs), nil
}

// findLocation is the one location of resource on device host (with path
// when given).
func findLocation(locs []Location, al hostAliases, resource, host, path string) (Location, string) {
	var match *Location
	for j := range locs {
		l := &locs[j]
		if l.Resource != resource || !al.same(l.HostID, host) || (path != "" && l.Path != path) {
			continue
		}
		if match != nil && match.Path != l.Path {
			return Location{}, "the resource has several paths on this device; give path_or_ref"
		}
		match = l
	}
	if match == nil {
		return Location{}, "no location of this resource is registered on this device"
	}
	return *match, ""
}

// rekey puts the identifying fields first (readable file).
func rekey(s *obj) *obj {
	out := pyjson.NewObject("resource", s.Value("resource"), "host_id", s.Value("host_id"), "path", s.Value("path"))
	for _, k := range s.Keys() {
		if !out.Has(k) {
			out.Set(k, s.Value(k))
		}
	}
	return out
}

// decorateStats adds "stats" to every location of the view's resources and
// to every path of the viewer's devices that has a stored size.
func (e *Env) decorateStats(full []*obj, devices []any) {
	lookup := e.StatLookup()
	for _, item := range full {
		key := str(item.Value("key"))
		for _, x := range listOr(item.Value("locations")) {
			loc := mapOf(x)
			if s := lookup(key, str(loc.Value("host_id")), str(loc.Value("path_or_ref"))); s != nil {
				loc.Set("stats", PublicStat(s))
			}
		}
	}
	for _, d := range devices {
		dev := mapOf(d)
		for _, x := range listOr(dev.Value("paths")) {
			p := mapOf(x)
			if p.Value("path_or_ref") == nil {
				continue
			}
			if s := lookup(str(p.Value("resource")), str(dev.Value("id")), str(p.Value("path_or_ref"))); s != nil {
				p.Set("stats", PublicStat(s))
			}
		}
	}
}
