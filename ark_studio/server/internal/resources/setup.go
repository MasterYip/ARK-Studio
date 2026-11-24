package resources

import (
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"arkstudio/go/internal/platform/pyerr"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
)

// Read-only setup layer readers: setup_layers.read_layers and
// recent_history.  Records carry only what build_graph reads ("layer",
// "owner", "profile.resources", "locations", "hosts", "verification"),
// normalised exactly like setup_layers._finish does for those fields.  Never
// migrates or writes.

const (
	maxResources = 200
	maxLocations = 400
	// HistoryLimit is recent_history's default: the newest events read.
	HistoryLimit = 2000
)

// Setup kinds in base.KIND_FIELDS order, with their 1.0 list field.
var kindFields = []struct{ kind, field string }{
	{"repository", "repositories"}, {"task_space", "task_spaces"}, {"compute", "compute"},
	{"environment", "environments"}, {"storage", "storage"}, {"command", "commands"}, {"secret", "secrets"},
}

var metaKeys = map[string]bool{"name": true, "purpose": true, "owner": true, "visibility": true, "audience": true,
	"share": true, "locations": true, "path_or_ref": true, "host_id": true, "host_label": true}

var (
	repositoryRoles = []string{"primary", "code", "paper", "website", "data", "reference"}
	computeKinds    = []string{"local", "ssh", "slurm", "cloud"}
	computeUse      = []string{"any", "reserved", "ask"}
	storageKinds    = []string{"artifacts", "datasets", "checkpoints", "logs", "media", "other"}
	hostIDRe        = pytext.MustCompile(`\A[A-Za-z0-9][A-Za-z0-9._-]{0,47}\z`)
	userSlugRe      = regexp.MustCompile(`[^a-z0-9._-]+`)
	remoteUserRe    = regexp.MustCompile(`(://)[^/@` + pytext.SpaceClass + `]+@`)
)

// text is setup._text: str(value if not None else "").strip()[:limit].
func text(v any, limit int) string {
	if v == nil {
		return ""
	}
	return pytext.Prefix(pytext.Strip(pyjson.Str(v)), limit)
}

// list is setup._list.
func list(v any) []any {
	if v == nil || v == "" {
		return nil
	}
	if l, ok := v.([]any); ok {
		return l
	}
	return []any{v}
}

func choice(v any, choices []string, def string) string {
	t := pytext.Lower(text(v, 40))
	if t == "" {
		t = def
	}
	for _, c := range choices {
		if c == t {
			return t
		}
	}
	return def
}

func cleanHostID(v any) string {
	t := pytext.Prefix(pytext.Strip(pyjson.Str(or(v, ""))), 48)
	if hostIDRe.MatchString(t) {
		return t
	}
	return ""
}

func userSlug(username string) string {
	name := pytext.Lower(pytext.Strip(username))
	stem := strings.Trim(pytext.Prefix(userSlugRe.ReplaceAllString(name, "_"), 40), "._")
	if stem == "" {
		stem = "user"
	}
	sum := sha1.Sum([]byte(name))
	return stem + "-" + hex.EncodeToString(sum[:])[:8]
}

// readJSON is setup_layers._read_json: nil when the file is absent.
func readJSON(path string) *pyjson.Object {
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	stale := pyjson.NewObject("state", "stale", "stale_reason", filepath.Base(path)+" is unreadable")
	raw, err := os.ReadFile(path)
	if err != nil || !utf8.Valid(raw) {
		return stale
	}
	v, err := pyjson.Loads(string(raw))
	if err != nil {
		return stale
	}
	if o, ok := v.(*pyjson.Object); ok {
		return o
	}
	return pyjson.NewObject()
}

// itemFromEntry is setup_layers.item_from_entry.
func itemFromEntry(kind string, entry *pyjson.Object) *pyjson.Object {
	details := pyjson.NewObject()
	for _, k := range entry.Keys() {
		if !metaKeys[k] && k != "id" {
			details.Set(k, entry.Value(k))
		}
	}
	if _, ok := details.Value("access").([]any); ok {
		details.Delete("access")
	}
	item := pyjson.NewObject("kind", kind, "id", entry.Value("id"), "details", details)
	for _, k := range []string{"name", "purpose", "owner", "path_or_ref"} {
		if v := entry.Value(k); v != nil && v != "" {
			item.Set(k, v)
		}
	}
	return item
}

// normalizeResource is the graph subset of setup_layers.normalize_resource:
// id, kind, name and the details build_graph reads.
func normalizeResource(item *pyjson.Object, index int) *pyjson.Object {
	kind := strings.ReplaceAll(pytext.Lower(text(or(item.Value("kind"), item.Value("type")), 40)), "-", "_")
	known := false
	for _, kf := range kindFields {
		if kf.field == kind {
			kind = kf.kind
		}
	}
	for _, kf := range kindFields {
		known = known || kf.kind == kind
	}
	if !known {
		kind = "storage"
	}
	if d, ok := item.Value("details").(*pyjson.Object); !ok || d == nil {
		flat := pyjson.NewObject()
		for _, k := range item.Keys() {
			if k != "kind" && k != "type" {
				flat.Set(k, item.Value(k))
			}
		}
		if flat.Has("subkind") {
			flat.Set("kind", flat.Delete("subkind"))
		}
		item = itemFromEntry(kind, flat)
	}
	in, _ := item.Value("details").(*pyjson.Object)
	rid := pytext.Lower(text(or(item.Value("id"), in.Value("id")), 48))
	if rid == "" {
		rid = strings.ReplaceAll(kind, "_", "-") + "-" + itoa(index+1)
	}
	details := pyjson.NewObject()
	switch kind {
	case "repository":
		details.Set("role", choice(in.Value("role"), repositoryRoles, "code"))
		details.Set("remote", remoteUserRe.ReplaceAllString(text(or(in.Value("remote"), ""), 400), "$1"))
	case "storage":
		details.Set("kind", choice(in.Value("kind"), storageKinds, "other"))
	case "compute":
		details.Set("kind", choice(in.Value("kind"), computeKinds, "local"))
		details.Set("gpu_server", text(in.Value("gpu_server"), 64))
		details.Set("gpus", text(in.Value("gpus"), 200))
		details.Set("use", choice(in.Value("use"), computeUse, "ask"))
		if h := cleanHostID(in.Value("device_id")); h != "" {
			details.Set("device_id", h)
		}
	}
	name := text(item.Value("name"), 120)
	if name == "" {
		name = rid
	}
	return pyjson.NewObject("id", rid, "kind", kind, "name", name, "details", details)
}

func itoa(n int) string { return intText(int64(n)) }

// layerResources is normalize_layer(...)["resources"] (duplicates dropped).
func layerResources(rawProfile any) ([]any, error) {
	raw, _ := rawProfile.(*pyjson.Object)
	var items []*pyjson.Object
	for _, v := range list(raw.Value("resources")) {
		if o, ok := v.(*pyjson.Object); ok {
			items = append(items, o)
		}
	}
	for _, kf := range kindFields {
		for _, v := range list(raw.Value(kf.field)) {
			if o, ok := v.(*pyjson.Object); ok {
				items = append(items, itemFromEntry(kf.kind, o))
			}
		}
	}
	if len(items) > maxResources {
		return nil, &pyerr.ValueError{Message: "too many setup resources; remove unused entries first"}
	}
	out := []any{}
	seen := map[string]bool{}
	for i, item := range items {
		r := normalizeResource(item, i)
		id := r.Value("id").(string)
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, r)
	}
	return out, nil
}

// normalizeLocation is setup_layers.normalize_location (nil when invalid).
func normalizeLocation(item *pyjson.Object) *pyjson.Object {
	host := cleanHostID(item.Value("host_id"))
	path := text(or(item.Value("path_or_ref"), item.Value("path")), 500)
	resource := text(item.Value("resource"), 200)
	if host == "" || path == "" || resource == "" {
		return nil
	}
	label := text(item.Value("host_label"), 60)
	if label == "" {
		label = "host-" + pytext.Prefix(host, 6)
	}
	return pyjson.NewObject("resource", resource, "host_id", host, "host_label", label, "path_or_ref", path,
		"verified_at", text(item.Value("verified_at"), 40), "verified_by", text(item.Value("verified_by"), 120),
		"added_by", text(item.Value("added_by"), 120), "added_at", text(item.Value("added_at"), 40))
}

// finish is the graph subset of setup_layers._finish.
func finish(data *pyjson.Object, layer, owner string) (*pyjson.Object, error) {
	resources, err := layerResources(or(data.Value("profile"), nil))
	if err != nil {
		return nil, err
	}
	if s := data.Value("state"); s == "confirmed" || s == "verified" {
		if c := data.Value("confirmation"); pyjson.Truthy(c) {
			if _, ok := c.(*pyjson.Object); !ok {
				return nil, &pyerr.InternalError{Message: "'" + pyerr.TypeName(c) + "' object has no attribute 'get'"}
			}
		}
	}
	locs := []any{}
	all := list(data.Value("locations"))
	if len(all) > maxLocations {
		all = all[:maxLocations]
	}
	for _, v := range all {
		if o, ok := v.(*pyjson.Object); ok {
			if loc := normalizeLocation(o); loc != nil {
				locs = append(locs, loc)
			}
		}
	}
	hosts := []any{}
	rawHosts := list(data.Value("hosts"))
	if len(rawHosts) > 50 {
		rawHosts = rawHosts[:50]
	}
	for _, v := range rawHosts {
		o, ok := v.(*pyjson.Object)
		if !ok || cleanHostID(o.Value("id")) == "" {
			continue
		}
		label := text(o.Value("label"), 60)
		if label == "" {
			label = "host-" + pytext.Prefix(pyjson.Str(o.Value("id")), 6)
		}
		hosts = append(hosts, pyjson.NewObject("id", cleanHostID(o.Value("id")), "label", label))
	}
	return pyjson.NewObject("layer", layer, "owner", owner, "profile", pyjson.NewObject("resources", resources),
		"locations", locs, "hosts", hosts, "verification", data.Value("verification")), nil
}

func setupDir(projectPath string) string { return filepath.Join(projectPath, "setup") }

// ReadLayers is setup_layers.read_layers: {"project": record, "users": [...]}.
func ReadLayers(projectPath string) (*pyjson.Object, error) {
	var project *pyjson.Object
	if data := readJSON(filepath.Join(setupDir(projectPath), "project.json")); data != nil {
		var err error
		if project, err = finish(data, "project", ""); err != nil {
			return nil, err
		}
	} else {
		project = pyjson.NewObject("layer", "project", "owner", "", "profile", pyjson.NewObject("resources", []any{}),
			"locations", []any{}, "hosts", []any{}, "verification", nil)
	}
	users := []any{}
	folder := filepath.Join(setupDir(projectPath), "users")
	entries, err := os.ReadDir(folder)
	if err == nil {
		var names []string
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".json") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, name := range names {
			data := readJSON(filepath.Join(folder, name))
			if data == nil {
				continue
			}
			owner := pyjson.Str(or(data.Value("owner"), ""))
			if owner == "" || name != userSlug(owner)+".json" {
				continue
			}
			record, err := finish(data, "user", owner)
			if err != nil {
				return nil, err
			}
			users = append(users, record)
		}
	}
	return pyjson.NewObject("project", project, "users", users), nil
}

// RecentHistory is setup_layers.recent_history(project_path, limit).
func RecentHistory(projectPath string, limit int) ([]any, error) {
	path := filepath.Join(setupDir(projectPath), "history.jsonl")
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return []any{}, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return []any{}, nil
	}
	if !utf8.Valid(raw) {
		return nil, &pyerr.InternalError{Message: "'utf-8' codec can't decode setup/history.jsonl"}
	}
	content := strings.ReplaceAll(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\r", "\n")
	lines := pytext.SplitLines(content, false)
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	out := []any{}
	for _, line := range lines {
		v, err := pyjson.Loads(line)
		if err != nil {
			continue
		}
		if o, ok := v.(*pyjson.Object); ok {
			out = append(out, o)
		}
	}
	return out, nil
}

// RegisteredGPUServers are the gpu_server names of the compute resources in
// the project's setup layers (project layer and every user layer, any
// state), deduplicated case-insensitively in layer order.  This is the
// project's GPU server scope (T-0018, gpu.Scope); an unreadable setup
// registers nothing.
func RegisteredGPUServers(projectPath string) []string {
	layers, err := ReadLayers(projectPath)
	if err != nil {
		return nil
	}
	records := []any{layers.Value("project")}
	if users, ok := layers.Value("users").([]any); ok {
		records = append(records, users...)
	}
	var out []string
	seen := map[string]bool{}
	for _, rec := range records {
		r, _ := rec.(*pyjson.Object)
		profile, _ := r.Value("profile").(*pyjson.Object)
		items, _ := profile.Value("resources").([]any)
		for _, item := range items {
			res, _ := item.(*pyjson.Object)
			if res == nil || res.Value("kind") != "compute" {
				continue
			}
			details, _ := res.Value("details").(*pyjson.Object)
			name := pytext.Strip(pyjson.Str(or(details.Value("gpu_server"), "")))
			if key := pytext.Lower(name); name != "" && !seen[key] {
				seen[key] = true
				out = append(out, name)
			}
		}
	}
	return out
}
