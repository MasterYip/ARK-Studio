package setup

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"arkstudio/go/internal/platform/textfile"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pyos"
	"arkstudio/go/internal/compat/pyre"
	"arkstudio/go/internal/compat/pytext"
)

const (
	LayerFormat  = "ark-setup-layer"
	LayerVersion = "2.0"
	projectFile  = "project.json"
	usersDir     = "users"
	legacyFile   = "environment.json"
	legacyBackup = "environment.json.v1.bak"
	maxResources = 200
	maxLocations = 400
)

var (
	locationField = map[string]string{"repository": "path", "task_space": "location", "storage": "location"}
	projectFields = []string{"objective", "non_goals", "agents", "policies", "cadence", "task_ids", "unknowns", "skipped", "notes"}
	userFields    = []string{"resources", "policies", "notes", "unknowns", "hosts", "locations"}
	policyRank    = map[string]int{"allow": 0, "ask": 1, "deny": 2}
	hostRe        = pyre.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,47}$`, 0)
	metaKeys      = []string{"name", "purpose", "owner", "visibility", "audience", "share", "locations", "path_or_ref", "host_id", "host_label"}
	slugRe        = regexp.MustCompile(`[^a-z0-9._-]+`)

	// mu is _WRITE_LOCK; every exported entry point holds it for its whole run.
	mu sync.Mutex
)

// Lock / Unlock serialize a sequence of calls (the server's setup actions).
func Lock()   { mu.Lock() }
func Unlock() { mu.Unlock() }

// ------------------------------------------------------------- viewer

// Viewer is setup_layers.Viewer.
type Viewer struct {
	User       string
	Admin      bool
	Legacy     bool
	CanWrite   bool
	TokenKind  string
	HostID     string
	HostSource string
}

// NewViewer applies __post_init__ (host id cleaned, else this machine).
func NewViewer(v Viewer) *Viewer {
	if h := CleanHostID(v.HostID); h != "" {
		v.HostID = h
	} else {
		v.HostID = MachineFingerprint()
	}
	if v.TokenKind == "" {
		v.TokenKind = "session"
	}
	if v.HostSource == "" {
		v.HostSource = "server"
	}
	return &v
}

// LegacyViewer is legacy_viewer(host_id).
func LegacyViewer(hostID string) *Viewer {
	return NewViewer(Viewer{Legacy: true, CanWrite: true, TokenKind: "server", HostID: hostID})
}

func (v *Viewer) isUser(name any) bool {
	return v.User != "" && lower(v.User) == lower(orStr(name))
}

func (v *Viewer) public(label string) *obj {
	var user any
	if v.User != "" {
		user = v.User
	}
	if label == "" {
		label = v.HostID
	}
	return pyjson.NewObject("user", user, "token_kind", v.TokenKind, "admin", v.Admin, "legacy", v.Legacy,
		"teams", []any{}, "host", pyjson.NewObject("id", v.HostID, "label", label, "source", v.HostSource,
			"server", v.HostID == MachineFingerprint()))
}

// CleanHostID is clean_host_id.
func CleanHostID(v any) string {
	t := pytext.Prefix(pytext.Strip(orStr(v)), 48)
	if search(hostRe, t) {
		return t
	}
	return ""
}

func defaultHostLabel(hostID any) string { return "host-" + pytext.Prefix(str(hostID), 6) }

// ------------------------------------------------------------- paths

// Env is one project's setup folder plus the server's migration hooks.
type Env struct {
	Project      string
	DefaultOwner string
	KnownUser    func(string) string
}

func (e *Env) dir() string { return join(e.Project, ProfileDir) }

func userSlug(username string) string {
	name := lower(pytext.Strip(username))
	stem := strings.Trim(pytext.Prefix(slugRe.ReplaceAllString(name, "_"), 40), "._")
	if stem == "" {
		stem = "user"
	}
	sum := sha1.Sum([]byte(name))
	return stem + "-" + hex.EncodeToString(sum[:])[:8]
}

func (e *Env) projectFile() string { return join(e.dir(), projectFile) }
func (e *Env) userFile(username string) string {
	return join(join(e.dir(), usersDir), userSlug(username)+".json")
}

func resourceKey(layer, owner, rid string) string {
	if layer == "project" {
		return "project/" + rid
	}
	return "u:" + owner + "/" + rid
}

// ------------------------------------------------------------- normalisation

func normalizeLocation(item any, errs *[]string, where, actor string) *obj {
	m := mapOf(item)
	if m == nil {
		*errs = append(*errs, where+" must be an object")
		return nil
	}
	hostID := CleanHostID(get(m, "host_id"))
	path := text(or(get(m, "path_or_ref"), get(m, "path")), 500)
	resource := text(get(m, "resource"), 200)
	if hostID == "" {
		*errs = append(*errs, where+".host_id must be letters, digits, '.', '_' or '-'")
	}
	if path == "" {
		*errs = append(*errs, where+".path_or_ref is required")
	}
	if resource == "" {
		*errs = append(*errs, where+".resource is required")
	}
	if hostID == "" || path == "" || resource == "" {
		return nil
	}
	label := text(get(m, "host_label"), 60)
	if label == "" {
		label = defaultHostLabel(hostID)
	}
	addedBy := text(get(m, "added_by"), 120)
	if addedBy == "" {
		addedBy = actor
	}
	return pyjson.NewObject("resource", resource, "host_id", hostID, "host_label", label, "path_or_ref", path,
		"verified_at", text(get(m, "verified_at"), 40), "verified_by", text(get(m, "verified_by"), 120),
		"added_by", addedBy, "added_at", text(get(m, "added_at"), 40))
}

func notNoneOrEmpty(v any, ok bool) bool {
	if !ok || v == nil {
		return false
	}
	s, isStr := v.(string)
	return !isStr || s != ""
}

// itemFromEntry is item_from_entry.
func itemFromEntry(kind string, entry *obj) *obj {
	details := pyjson.NewObject()
	for _, k := range entry.Keys() {
		if !in(k, metaKeys) && k != "id" {
			details.Set(k, entry.Value(k))
		}
	}
	if _, isList := details.Value("access").([]any); isList {
		details.Delete("access")
	}
	item := pyjson.NewObject("kind", kind, "id", entry.Value("id"), "details", details)
	for _, k := range []string{"name", "purpose", "owner", "path_or_ref"} {
		if v, ok := entry.Get(k); notNoneOrEmpty(v, ok) {
			item.Set(k, v)
		}
	}
	return item
}

func normalizeResource(item *obj, index int, layer, owner string, errs, warns *[]string) (*obj, string) {
	kind := strings.ReplaceAll(lower(text(or(get(item, "kind"), get(item, "type")), 40)), "-", "_")
	for _, k := range Kinds {
		if KindFields[k] == kind {
			kind = k
			break
		}
	}
	if !in(kind, Kinds) {
		*errs = append(*errs, fmt.Sprintf("resources[%d].kind: '%s' is not one of %s", index, kind, strings.Join(Kinds, ", ")))
		kind = "storage"
	}
	if mapOf(get(item, "details")) == nil {
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
	detailsIn := mapOf(item.Value("details")).Copy()
	rid := ident(or(get(item, "id"), detailsIn.Value("id")), fmt.Sprintf("%s-%d", strings.ReplaceAll(kind, "_", "-"), index+1),
		fmt.Sprintf("resources[%d]", index), errs)
	locField := locationField[kind]
	var loc any = get(item, "path_or_ref")
	if !pyjson.Truthy(loc) {
		popped, had := detailsIn.Get("path_or_ref")
		detailsIn.Delete("path_or_ref")
		if !had {
			popped = ""
		}
		loc = popped
		if !pyjson.Truthy(loc) {
			loc = ""
			if locField != "" {
				loc = detailsIn.Value(locField)
			}
		}
	}
	location := text(loc, 500)
	probe := detailsIn.Copy().Set("id", rid)
	if kind == "repository" {
		probe.Set("path", or(probe.Value("path"), "~"))
	}
	entry, e, w := NormalizeEntry(kind, probe, index)
	for _, msg := range e {
		*errs = append(*errs, "resources."+rid+": "+msg)
	}
	for _, msg := range w {
		*warns = append(*warns, "resources."+rid+": "+msg)
	}
	entry.Delete("id")
	if locField != "" {
		entry.Delete(locField)
	}
	resOwner := owner
	if layer != "user" {
		if t := text(get(item, "owner"), 64); t != "" {
			resOwner = t
		}
	}
	name := text(get(item, "name"), 120)
	if name == "" {
		name = rid
	}
	purpose := text(or(get(item, "purpose"), or(detailsIn.Value("purpose"), detailsIn.Value("note"))), 300)
	return pyjson.NewObject("id", rid, "kind", kind, "name", name, "purpose", purpose, "owner", resOwner, "details", entry), location
}

// LegacyResources is _legacy_resources(raw).
func LegacyResources(raw *obj) []any { return legacyResources(raw) }

func legacyResources(raw *obj) []any {
	out := []any{}
	for _, kind := range Kinds {
		for _, entry := range list(get(raw, KindFields[kind])) {
			if m := mapOf(entry); m != nil {
				out = append(out, itemFromEntry(kind, m))
			}
		}
	}
	return out
}

type locPair struct{ id, path string }

func dedupe(l []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range l {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// normalizeLayer is normalize_layer.
func normalizeLayer(raw *obj, layer, owner string, projectPolicies *obj) (*obj, []locPair, []string, []string, error) {
	if raw == nil {
		raw = pyjson.NewObject()
	} else {
		raw = raw.Copy()
	}
	errs, warns := []string{}, []string{}
	items := []any{}
	for _, x := range list(raw.Value("resources")) {
		if mapOf(x) != nil {
			items = append(items, x)
		}
	}
	items = append(items, legacyResources(raw)...)
	if len(items) > maxResources {
		return nil, nil, nil, nil, setupErr("too many setup resources; remove unused entries first", 400, "")
	}
	resources, seen, locs := []any{}, map[string]bool{}, []locPair{}
	for i, x := range items {
		res, location := normalizeResource(x.(*obj), i, layer, owner, &errs, &warns)
		id := res.Value("id").(string)
		if seen[id] {
			errs = append(errs, "resources: duplicate id '"+id+"'")
			continue
		}
		seen[id] = true
		resources = append(resources, res)
		if location != "" {
			locs = append(locs, locPair{id, location})
		}
	}
	var profile *obj
	if layer == "project" {
		flat := pyjson.NewObject()
		for _, k := range projectFields {
			if v, ok := raw.Get(k); ok {
				flat.Set(k, v)
			}
		}
		if pol := mapOf(flat.Value("policies")); pol != nil {
			policiesIn := pol.Copy()
			for _, k := range listOr(or(raw.Value("policies_defaulted"), nil)) {
				ks, ok := k.(string)
				if !ok {
					continue
				}
				// policies_in.get(key) == POLICY_DEFAULTS.get(key)
				def, known := PolicyDefaults[ks]
				v := policiesIn.Value(ks)
				s, isStr := v.(string)
				if (known && isStr && s == def) || (!known && v == nil) {
					policiesIn.Delete(ks)
				}
			}
			flat.Set("policies", policiesIn)
		}
		normalized, e, w := NormalizeProfile(flat)
		errs = append(errs, e...)
		warns = append(warns, w...)
		profile = pyjson.NewObject()
		for _, k := range projectFields {
			profile.Set(k, normalized.Value(k))
		}
		profile.Set("policies_defaulted", normalized.Value("policies_defaulted"))
	} else {
		for _, k := range projectFields {
			if !in(k, userFields) && pyjson.Truthy(raw.Value(k)) {
				warns = append(warns, k+": project-level field ignored in a user layer; draft it with layer=project")
			}
		}
		policies := pyjson.NewObject()
		rawPolicies := orMap(mapOf(raw.Value("policies")))
		ceiling := projectPolicies
		if ceiling.Len() == 0 {
			ceiling = pyjson.NewObject()
			for _, k := range PolicyKeys {
				ceiling.Set(k, PolicyDefaults[k])
			}
		}
		for _, k := range rawPolicies.Keys() {
			if _, known := PolicyDefaults[k]; !known {
				warns = append(warns, "policies."+k+" is not a known policy and is ignored")
				continue
			}
			value := lower(text(rawPolicies.Value(k), 10))
			rank, ok := policyRank[value]
			if !ok {
				errs = append(errs, "policies."+k+": '"+value+"' is not one of allow, ask, deny")
				continue
			}
			ceil, has := ceiling.Get(k)
			if !has {
				ceil = "ask"
			}
			cr := 1
			if s, ok := ceil.(string); ok {
				if r, ok := policyRank[s]; ok {
					cr = r
				}
			}
			if rank < cr {
				errs = append(errs, "policies."+k+": a user layer may only tighten the project policy (project: "+str(ceiling.Value(k))+")")
				continue
			}
			policies.Set(k, value)
		}
		unk, _, _ := NormalizeProfile(pyjson.NewObject("unknowns", raw.Value("unknowns")))
		profile = pyjson.NewObject("policies", policies, "notes", text(raw.Value("notes"), 4000), "unknowns", unk.Value("unknowns"))
	}
	for _, x := range resources {
		r := x.(*obj)
		d := r.Value("details").(*obj)
		if r.Value("kind") == "repository" && d.Value("role") == "primary" && d.Value("access") == "read-only" {
			warns = append(warns, "resources."+str(r.Value("id"))+": this primary repository is read-only")
		}
	}
	profile.Set("resources", resources)
	for _, hit := range FindSecretValues(raw, "") {
		errs = append(errs, hit+": looks like a secret value; store a reference (env:NAME) instead")
	}
	return profile, locs, dedupe(errs), dedupe(warns), nil
}

// ------------------------------------------------------------- records

func empty(layer, owner string) *obj {
	profile, _, _, _, _ := normalizeLayer(nil, layer, owner, nil)
	return pyjson.NewObject("format", LayerFormat, "setup_version", LayerVersion, "layer", layer,
		"owner", owner, "state", "unset", "profile", profile, "digest", "", "confirmation", nil,
		"verification", nil, "locations", []any{}, "hosts", []any{}, "updated_at", "", "updated_by", "")
}

func finish(record *obj, layer, owner string, projectPolicies *obj) (*obj, error) {
	var raw *obj
	if p := record.Value("profile"); pyjson.Truthy(p) {
		raw = mapOf(p)
	}
	profile, _, errs, _, err := normalizeLayer(raw, layer, owner, projectPolicies)
	if err != nil {
		return nil, err
	}
	record.Set("profile", profile)
	record.Set("layer", layer).Set("owner", owner)
	state := "draft"
	if s, ok := record.Value("state").(string); ok && in(s, States) {
		state = s
	}
	current := Digest(profile)
	if in(state, ReadyStates) {
		if get(orMap(record.Value("confirmation")), "digest") != current {
			state = "stale"
			record.Set("stale_reason", "profile changed after confirmation")
		}
	}
	record.Set("state", state)
	record.Set("digest", current)
	record.Set("valid", len(errs) == 0)
	locs := []any{}
	for _, item := range head(list(record.Value("locations")), maxLocations) {
		if mapOf(item) != nil {
			var none []string
			if loc := normalizeLocation(item, &none, "locations", ""); loc != nil {
				locs = append(locs, loc)
			}
		}
	}
	record.Set("locations", locs)
	hosts := []any{}
	for _, item := range head(list(record.Value("hosts")), 50) {
		m := mapOf(item)
		if m != nil && CleanHostID(m.Value("id")) != "" {
			label := text(m.Value("label"), 60)
			if label == "" {
				label = defaultHostLabel(m.Value("id"))
			}
			hosts = append(hosts, pyjson.NewObject("id", CleanHostID(m.Value("id")), "label", label))
		}
	}
	record.Set("hosts", hosts)
	return record, nil
}

// readJSON is _read_json: nil when not a file.
func readJSON(path string) *obj {
	if !pyos.IsFile(path) {
		return nil
	}
	unreadable := pyjson.NewObject("state", "stale", "stale_reason", pathName(path)+" is unreadable")
	raw, err := os.ReadFile(path)
	if err != nil || !utf8.Valid(raw) {
		return unreadable
	}
	text := strings.ReplaceAll(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\r", "\n")
	if strings.HasPrefix(text, "\ufeff") {
		return unreadable // json.loads: "Unexpected UTF-8 BOM"
	}
	v, err := pyjson.Loads(text)
	if err != nil {
		return unreadable
	}
	if m := mapOf(v); m != nil {
		return m
	}
	return pyjson.NewObject()
}

func (e *Env) loadProject() (*obj, error) {
	if err := e.migrate(); err != nil {
		return nil, err
	}
	data := readJSON(e.projectFile())
	if data == nil {
		return empty("project", ""), nil
	}
	return finish(data, "project", "", nil)
}

func (e *Env) loadUser(username string, projectPolicies *obj) (*obj, error) {
	if username == "" {
		return empty("user", ""), nil
	}
	data := readJSON(e.userFile(username))
	if data == nil {
		return empty("user", username), nil
	}
	owner := orStr(data.Value("owner"))
	if owner == "" {
		owner = username
	}
	if lower(owner) != lower(username) {
		return empty("user", username), nil
	}
	return finish(data, "user", owner, projectPolicies)
}

func (e *Env) allUserLayers() ([]*obj, error) {
	folder := join(e.dir(), usersDir)
	out := []*obj{}
	if !pyos.IsDir(folder) {
		return out, nil
	}
	ents, err := os.ReadDir(folder)
	if err != nil {
		return out, nil
	}
	names := []string{}
	for _, ent := range ents {
		n := ent.Name()
		if strings.HasSuffix(n, ".json") || (windows && strings.HasSuffix(strings.ToLower(n), ".json")) {
			names = append(names, n)
		}
	}
	sort.SliceStable(names, func(i, j int) bool {
		if windows {
			return strings.ToLower(names[i]) < strings.ToLower(names[j])
		}
		return names[i] < names[j]
	})
	for _, n := range names {
		data := readJSON(join(folder, n))
		if data == nil {
			data = pyjson.NewObject()
		}
		owner := orStr(data.Value("owner"))
		if owner != "" && n == userSlug(owner)+".json" {
			rec, err := finish(data, "user", owner, nil)
			if err != nil {
				return nil, err
			}
			out = append(out, rec)
		}
	}
	return out, nil
}

// write is _write: the record (without valid/stale_reason) then one history line.
func (e *Env) write(target string, record *obj, event *obj) error {
	if err := os.MkdirAll(pathParent(target), 0o755); err != nil {
		return err
	}
	keep := pyjson.NewObject()
	for _, k := range record.Keys() {
		if k != "valid" && k != "stale_reason" {
			keep.Set(k, record.Value(k))
		}
	}
	tmp := strings.TrimSuffix(target, ".json") + ".tmp"
	if err := os.WriteFile(tmp, textfile.Encode(string(pyjson.DumpsIndent(keep, 2))+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		return err
	}
	f, err := os.OpenFile(join(e.dir(), HistoryFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(textfile.Encode(string(pyjson.Dumps(event)) + "\n"))
	return err
}

func (e *Env) layerTarget(layer, owner string) string {
	if layer == "project" {
		return e.projectFile()
	}
	return e.userFile(owner)
}

func (e *Env) loadLayer(layer, owner string) (*obj, error) {
	if layer == "project" {
		return e.loadProject()
	}
	project, err := e.loadProject()
	if err != nil {
		return nil, err
	}
	return e.loadUser(owner, mapOf(get(mapOf(project.Value("profile")), "policies")))
}

func layerName(layer, owner string) string {
	if layer == "project" {
		return "project"
	}
	return "user:" + owner
}

// ------------------------------------------------------------- migration

func (e *Env) migrate() error {
	folder := e.dir()
	legacy := join(folder, legacyFile)
	if pyos.Exists(e.projectFile()) || !pyos.IsFile(legacy) {
		return nil
	}
	old := pyjson.NewObject()
	if raw, err := os.ReadFile(legacy); err == nil && utf8.Valid(raw) && !strings.HasPrefix(string(raw), "\ufeff") {
		if v, err := pyjson.Loads(string(raw)); err == nil {
			if m := mapOf(v); m != nil {
				old = m
			}
		}
	}
	var oldIn *obj
	if p := old.Value("profile"); pyjson.Truthy(p) {
		oldIn = mapOf(p)
	}
	oldProfile, _, _ := NormalizeProfile(oldIn)
	confirmation := orMap(mapOf(old.Value("confirmation")))
	carried := inAny(old.Value("state"), ReadyStates) && get(confirmation, "digest") == Digest(oldProfile)
	owner := ""
	for _, cand := range []any{confirmation.Value("by"), old.Value("updated_by")} {
		t := orStr(cand)
		if strings.HasPrefix(t, "human:") {
			name := strings.SplitN(t, ":", 2)[1]
			resolved := ""
			if e.KnownUser != nil {
				resolved = e.KnownUser(name)
			}
			if resolved != "" {
				owner = resolved
				break
			}
		}
	}
	if owner == "" {
		owner = e.DefaultOwner
	}
	if owner == "" {
		owner = "local"
	}
	machine := CleanHostID(old.Value("machine"))
	if machine == "" {
		machine = MachineFingerprint()
	}
	label := defaultHostLabel(machine)
	now := utcNow()
	projectItems, locations := []any{}, []any{}
	for _, kind := range Kinds {
		for _, x := range listOr(oldProfile.Value(KindFields[kind])) {
			entry := x.(*obj)
			item := itemFromEntry(kind, entry)
			item.Set("owner", owner)
			projectItems = append(projectItems, item)
			var path any = ""
			if f := locationField[kind]; f != "" {
				path = entry.Value(f)
			} else if kind == "compute" && entry.Value("kind") == "local" {
				path = "local"
			}
			if pyjson.Truthy(path) {
				locations = append(locations, pyjson.NewObject("resource", resourceKey("project", "", str(entry.Value("id"))),
					"host_id", machine, "host_label", label, "path_or_ref", path, "added_by", "migration", "added_at", now))
			}
		}
	}
	projectRaw := pyjson.NewObject()
	for _, k := range projectFields {
		projectRaw.Set(k, oldProfile.Value(k))
	}
	pol := pyjson.NewObject()
	defaulted := map[string]bool{}
	for _, k := range listOr(oldProfile.Value("policies_defaulted")) {
		defaulted[str(k)] = true
	}
	op := orMap(oldProfile.Value("policies"))
	for _, k := range op.Keys() {
		if !defaulted[k] {
			pol.Set(k, op.Value(k))
		}
	}
	projectRaw.Set("policies", pol)
	projectRaw.Set("resources", projectItems)
	pProfile, _, pErrs, _, err := normalizeLayer(projectRaw, "project", owner, nil)
	if err != nil {
		return err
	}
	oldState := old.Value("state")
	note := pyjson.NewObject("from", legacyFile, "at", now, "old_digest", Digest(oldProfile),
		"old_state", or(oldState, ""), "carried_confirmation", carried)
	digest := Digest(pProfile)
	var state any
	var conf any
	if carried {
		state = "confirmed"
		if inAny(oldState, ReadyStates) {
			state = oldState
		}
		conf = pyjson.NewObject("by", confirmation.Value("by"), "at", confirmation.Value("at"), "digest", digest,
			"note", or(confirmation.Value("note"), ""), "migrated_from_digest", confirmation.Value("digest"))
	} else {
		state = "draft"
		if s, isStr := oldState.(string); oldState != nil && !(isStr && (s == "" || s == "unset")) {
			state = "proposed"
		}
	}
	var verification any
	if carried {
		verification = old.Value("verification")
	}
	rec := pyjson.NewObject("format", LayerFormat, "setup_version", LayerVersion, "layer", "project",
		"owner", "", "state", state, "profile", pProfile, "digest", digest, "confirmation", conf,
		"verification", verification, "locations", locations, "hosts", []any{},
		"updated_at", now, "updated_by", "migration", "migration", note)
	if !carried && state == "proposed" {
		rec.Set("needs_reconfirmation", true)
	}
	if len(pErrs) > 0 && rec.Value("state") == "proposed" {
		rec.Set("state", "draft")
	}
	if err := e.write(e.projectFile(), rec, pyjson.NewObject("at", now, "actor", "migration", "layer", "project",
		"event", "migrated", "state", rec.Value("state"), "digest", rec.Value("digest"))); err != nil {
		return err
	}
	backup := join(folder, legacyBackup)
	if pyos.Exists(backup) {
		stamp := strings.ReplaceAll(strings.ReplaceAll(now, ":", ""), "-", "")
		backup = join(folder, legacyBackup+"."+pytext.Prefix(stamp, 15))
	}
	return os.Rename(legacy, backup)
}

// ------------------------------------------------------------- writes

func checkLayerWriter(v *Viewer, layer, owner string) error {
	if !v.CanWrite {
		return setupErr("this action needs write access to the project", 403, "SETUP_FORBIDDEN")
	}
	if layer == "project" {
		return nil
	}
	if v.Legacy || v.User == "" {
		return setupErr("the server token has no user layer; use a personal agent token or layer=project", 403, "SETUP_NO_USER")
	}
	if !v.isUser(owner) {
		return setupErr("you can only change your own setup layer", 403, "SETUP_NOT_OWNER")
	}
	return nil
}

func locKey(l *obj) string {
	return pyjson.Repr([]any{l.Value("resource"), l.Value("host_id"), l.Value("path_or_ref"), l.Value("added_by")})
}

func mergeLocations(existing, new []any, keep map[string]bool, prefix string) ([]any, error) {
	replaced := map[string]bool{}
	for _, x := range new {
		replaced[locKey(x.(*obj))] = true
	}
	out := []any{}
	for _, x := range existing {
		l := x.(*obj)
		if replaced[locKey(l)] {
			continue
		}
		r := str(l.Value("resource"))
		if keep != nil && strings.HasPrefix(r, prefix) && !keep[r] {
			continue
		}
		out = append(out, l)
	}
	if len(out)+len(new) > maxLocations {
		return nil, setupErr("too many setup locations; remove unused entries first", 400, "")
	}
	return append(out, new...), nil
}

func upsertHost(hosts []any, hostID, label string) []any {
	out := []any{}
	for _, h := range hosts {
		out = append(out, h.(*obj).Copy())
	}
	for _, x := range out {
		item := x.(*obj)
		if item.Value("id") == hostID {
			if label != "" {
				item.Set("label", label)
			}
			return out
		}
	}
	if label == "" {
		label = defaultHostLabel(hostID)
	}
	out = append(out, pyjson.NewObject("id", hostID, "label", label))
	if len(out) > 50 {
		out = out[len(out)-50:]
	}
	return out
}

func hostLabelFor(record *obj, hostID string) string {
	for _, x := range listOr(record.Value("hosts")) {
		item := mapOf(x)
		if get(item, "id") == hostID {
			if l := get(item, "label"); pyjson.Truthy(l) {
				return str(l)
			}
			return defaultHostLabel(hostID)
		}
	}
	return defaultHostLabel(hostID)
}

func profileOf(record *obj) *obj { return mapOf(record.Value("profile")) }

// SaveLayer is save_layer (merge_resources=True).
func (e *Env) SaveLayer(raw *obj, v *Viewer, layer, actor string, propose bool, hostLabel string) (*obj, error) {
	owner := ""
	if layer != "project" {
		owner = v.User
	}
	if err := checkLayerWriter(v, layer, owner); err != nil {
		return nil, err
	}
	if raw == nil {
		raw = pyjson.NewObject()
	}
	previous, err := e.loadLayer(layer, owner)
	if err != nil {
		return nil, err
	}
	prevProfile := profileOf(previous)
	merged := pyjson.NewObject()
	for _, x := range listOr(prevProfile.Value("resources")) {
		r := x.(*obj)
		merged.Set(str(r.Value("id")), r)
	}
	fields := pyjson.NewObject()
	for _, k := range prevProfile.Keys() {
		if k != "resources" {
			fields.Set(k, prevProfile.Value(k))
		}
	}
	for _, x := range append(list(raw.Value("resources")), legacyResources(raw)...) {
		if m := mapOf(x); m != nil {
			key := lower(text(m.Value("id"), 48))
			if key == "" {
				key = fmt.Sprintf("r%d", merged.Len()+1)
			}
			merged.Set(key, m)
		}
	}
	for _, rid := range list(raw.Value("delete")) {
		merged.Delete(lower(text(rid, 48)))
	}
	for _, k := range raw.Keys() {
		if k == "resources" || k == "delete" {
			continue
		}
		skip := false
		for _, f := range KindFields {
			if f == k {
				skip = true
			}
		}
		if !skip {
			fields.Set(k, raw.Value(k))
		}
	}
	res := []any{}
	for _, k := range merged.Keys() {
		res = append(res, merged.Value(k))
	}
	raw = fields.Copy().Set("resources", res)
	var projectPolicies *obj
	if layer == "user" {
		project, err := e.loadProject()
		if err != nil {
			return nil, err
		}
		projectPolicies = mapOf(get(profileOf(project), "policies"))
	}
	lo := owner
	if lo == "" {
		lo = v.User
	}
	profile, locs, errs, warns, err := normalizeLayer(raw, layer, lo, projectPolicies)
	if err != nil {
		return nil, err
	}
	secret := []string{}
	for _, msg := range errs {
		if strings.Contains(msg, "looks like a secret value") {
			secret = append(secret, msg)
		}
	}
	if len(secret) > 0 {
		return nil, setupErr("refusing to store secret values: "+strings.Join(secret, "; "), 400, "SETUP_SECRET")
	}
	now := utcNow()
	label := text(hostLabel, 60)
	if label == "" {
		label = hostLabelFor(previous, v.HostID)
	}
	addedBy := v.User
	if addedBy == "" {
		addedBy = actor
	}
	newLocs := []any{}
	for _, l := range locs {
		newLocs = append(newLocs, pyjson.NewObject("resource", resourceKey(layer, owner, l.id), "host_id", v.HostID,
			"host_label", label, "path_or_ref", l.path, "verified_at", "", "verified_by", "",
			"added_by", addedBy, "added_at", now))
	}
	for _, item := range list(raw.Value("locations")) {
		if loc := normalizeLocation(item, &errs, "locations", addedBy); loc != nil {
			newLocs = append(newLocs, loc)
		}
	}
	prefix := resourceKey(layer, owner, "")
	keep := map[string]bool{}
	for _, x := range listOr(profile.Value("resources")) {
		keep[resourceKey(layer, owner, str(x.(*obj).Value("id")))] = true
	}
	locations, err := mergeLocations(listOr(previous.Value("locations")), newLocs, keep, prefix)
	if err != nil {
		return nil, err
	}
	hosts := listOr(previous.Value("hosts"))
	if layer == "user" {
		hosts = upsertHost(hosts, v.HostID, label)
		for _, x := range list(raw.Value("hosts")) {
			m := mapOf(x)
			if m != nil && CleanHostID(m.Value("id")) != "" {
				hosts = upsertHost(hosts, CleanHostID(m.Value("id")), text(m.Value("label"), 60))
			}
		}
	}
	digest := Digest(profile)
	prevState := str(previous.Value("state"))
	unchanged := digest == previous.Value("digest") && in(prevState, ReadyStates)
	state := "draft"
	if unchanged {
		state = prevState
	} else if propose && len(errs) == 0 {
		state = "proposed"
	}
	var conf, verification any
	if unchanged {
		conf, verification = previous.Value("confirmation"), previous.Value("verification")
	}
	record := pyjson.NewObject("format", LayerFormat, "setup_version", LayerVersion, "layer", layer,
		"owner", owner, "state", state, "profile", profile, "digest", digest, "confirmation", conf,
		"verification", verification, "locations", locations, "hosts", hosts, "updated_at", now, "updated_by", actor)
	if err := e.write(e.layerTarget(layer, owner), record, pyjson.NewObject("at", now, "actor", actor,
		"layer", layerName(layer, owner), "event", state, "digest", digest, "previous_state", previous.Value("state"))); err != nil {
		return nil, err
	}
	return pyjson.NewObject("ok", len(errs) == 0, "errors", strList(errs), "warnings", strList(warns), "layer", layer,
		"state", state, "digest", digest), nil
}

// SetHost is set_host.
func (e *Env) SetHost(v *Viewer, label, actor, hostID string) (*obj, error) {
	if err := checkLayerWriter(v, "user", v.User); err != nil {
		return nil, err
	}
	host := CleanHostID(hostID)
	if host == "" {
		host = v.HostID
	}
	record, err := e.loadLayer("user", v.User)
	if err != nil {
		return nil, err
	}
	if record.Value("state") == "unset" {
		record.Set("state", "draft").Set("updated_at", utcNow()).Set("updated_by", actor)
	}
	record.Set("hosts", upsertHost(listOr(record.Value("hosts")), host, text(label, 60)))
	for _, x := range listOr(record.Value("locations")) {
		loc := x.(*obj)
		if loc.Value("host_id") == host && label != "" {
			loc.Set("host_label", text(label, 60))
		}
	}
	if err := e.write(e.userFile(v.User), record, pyjson.NewObject("at", utcNow(), "actor", actor,
		"layer", "user:"+v.User, "event", "host", "host", host)); err != nil {
		return nil, err
	}
	return pyjson.NewObject("ok", true, "host", pyjson.NewObject("id", host, "label", hostLabelFor(record, host))), nil
}

// AddLocation is add_location.
func (e *Env) AddLocation(v *Viewer, key, pathOrRef, actor, hostID, hostLabel, scope string) (*obj, error) {
	wl := "user"
	if v.Legacy {
		wl = "project"
	}
	if err := checkLayerWriter(v, wl, v.User); err != nil {
		return nil, err
	}
	view, err := (&Env{Project: e.Project, DefaultOwner: "local"}).resolve(v)
	if err != nil {
		return nil, err
	}
	found := false
	for _, x := range view.full {
		if x.Value("key") == key {
			found = true
		}
	}
	if !found {
		return nil, setupErr("resource not found or not accessible to you", 404, "SETUP_RESOURCE_NOT_FOUND")
	}
	errs := []string{}
	host := CleanHostID(hostID)
	if host == "" {
		host = v.HostID
	}
	projectScope := scope == "project" || v.Legacy
	if projectScope && !(v.Admin || v.Legacy) {
		return nil, setupErr("only a project admin can add project-wide locations", 403, "SETUP_ADMIN_LOCATION")
	}
	layer, owner := "user", v.User
	if projectScope {
		layer, owner = "project", ""
	}
	record, err := e.loadLayer(layer, owner)
	if err != nil {
		return nil, err
	}
	label := text(hostLabel, 60)
	if label == "" {
		label = hostLabelFor(record, host)
	}
	addedBy := v.User
	if addedBy == "" {
		addedBy = actor
	}
	loc := normalizeLocation(pyjson.NewObject("resource", key, "host_id", host, "host_label", label,
		"path_or_ref", pathOrRef, "added_at", utcNow()), &errs, "locations", addedBy)
	if len(errs) > 0 || loc == nil {
		msg := strings.Join(errs, "; ")
		if msg == "" {
			msg = "invalid location"
		}
		return nil, setupErr(msg, 400, "SETUP_INVALID")
	}
	if len(FindSecretValues(pyjson.NewObject("path_or_ref", loc.Value("path_or_ref")), "")) > 0 {
		return nil, setupErr("refusing to store secret values: path_or_ref looks like a secret", 400, "SETUP_SECRET")
	}
	locations, err := mergeLocations(listOr(record.Value("locations")), []any{loc}, nil, "")
	if err != nil {
		return nil, err
	}
	record.Set("locations", locations)
	if layer == "user" {
		record.Set("hosts", upsertHost(listOr(record.Value("hosts")), host, text(hostLabel, 60)))
		if record.Value("state") == "unset" {
			record.Set("state", "draft")
		}
	}
	now := utcNow()
	record.Set("updated_at", now).Set("updated_by", actor)
	if err := e.write(e.layerTarget(layer, v.User), record, pyjson.NewObject("at", now, "actor", actor,
		"layer", layerName(layer, v.User), "event", "location", "resource", key, "host", host)); err != nil {
		return nil, err
	}
	return pyjson.NewObject("ok", true, "location", loc, "scope", layer), nil
}

func (e *Env) invalidLayer(record *obj, layer, owner string) error {
	_, _, errs, _, err := normalizeLayer(profileOf(record), layer, owner, nil)
	if err != nil {
		return err
	}
	if len(errs) > 5 {
		errs = errs[:5]
	}
	return setupErr("the profile has validation errors: "+strings.Join(errs, "; "), 409, "SETUP_INVALID")
}

// Propose is propose.
func (e *Env) Propose(v *Viewer, layer, actor string) (*obj, error) {
	owner := ""
	if layer != "project" {
		owner = v.User
	}
	if err := checkLayerWriter(v, layer, owner); err != nil {
		return nil, err
	}
	record, err := e.loadLayer(layer, owner)
	if err != nil {
		return nil, err
	}
	if record.Value("state") == "unset" {
		return nil, setupErr("there is no profile to confirm", 409, "SETUP_UNSET")
	}
	if valid, ok := record.Get("valid"); ok && !pyjson.Truthy(valid) {
		return nil, e.invalidLayer(record, layer, owner)
	}
	if s := record.Value("state"); s == "draft" || s == "stale" {
		now := utcNow()
		record.Set("state", "proposed").Set("confirmation", nil).Set("updated_at", now).Set("updated_by", actor)
		if err := e.write(e.layerTarget(layer, owner), record, pyjson.NewObject("at", now, "actor", actor,
			"layer", layerName(layer, owner), "event", "proposed", "digest", record.Value("digest"))); err != nil {
			return nil, err
		}
	}
	return pyjson.NewObject("ok", true, "layer", layer, "state", record.Value("state"), "digest", record.Value("digest")), nil
}

// IsHumanActor is is_human_actor.
func IsHumanActor(actor string) bool { return strings.HasPrefix(actor, "human:") }

// ConfirmLayer is confirm_layer.
func (e *Env) ConfirmLayer(v *Viewer, layer, actor, digest, note string) (*obj, error) {
	if err := checkLayerWriter(v, layer, v.User); err != nil {
		return nil, err
	}
	if !IsHumanActor(actor) || v.Legacy {
		return nil, setupErr("only a human can confirm the environment profile; agents may draft and propose", 403, "SETUP_HUMAN_REQUIRED")
	}
	owner := ""
	if layer == "project" {
		if !v.Admin {
			return nil, setupErr("only a project admin can confirm the project layer", 403, "SETUP_ADMIN_REQUIRED")
		}
	} else {
		owner = v.User
		if owner == "" {
			return nil, setupErr("you can only change your own setup layer", 403, "SETUP_NOT_OWNER")
		}
	}
	record, err := e.loadLayer(layer, owner)
	if err != nil {
		return nil, err
	}
	if record.Value("state") == "unset" {
		return nil, setupErr("there is no profile to confirm", 409, "SETUP_UNSET")
	}
	if valid, ok := record.Get("valid"); ok && !pyjson.Truthy(valid) {
		return nil, e.invalidLayer(record, layer, owner)
	}
	if digest != record.Value("digest") {
		return nil, setupErr("digest mismatch: the profile changed since it was reviewed", 409, "SETUP_DIGEST")
	}
	now := utcNow()
	record.Set("state", "confirmed").Set("confirmation", pyjson.NewObject("by", actor, "at", now,
		"digest", record.Value("digest"), "note", text(note, 1000))).Set("verification", nil)
	record.Delete("needs_reconfirmation")
	if err := e.write(e.layerTarget(layer, owner), record, pyjson.NewObject("at", now, "actor", actor,
		"layer", layerName(layer, owner), "event", "confirmed", "digest", record.Value("digest"))); err != nil {
		return nil, err
	}
	return pyjson.NewObject("ok", true, "layer", layer, "state", "confirmed", "digest", record.Value("digest")), nil
}

// LoadUserDigest is load_user(path, user)["digest"] (the 1.0 confirm default).
func (e *Env) LoadUserDigest(user string) (string, error) {
	rec, err := e.loadUser(user, nil)
	if err != nil {
		return "", err
	}
	return orStr(rec.Value("digest")), nil
}
