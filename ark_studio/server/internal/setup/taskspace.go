package setup

// The task space of a device (Go only, 2026-11-24).
//
//   Paths            what an agent on a device may write to: the resolved
//                    task space, repositories, storage and artifacts paths
//                    of that device, and what is missing
//   task-space decisions
//                    when a device has no task space the agent does not pick
//                    one: it opens a decision unit with options; the person's
//                    answer is written to their own layer for that device
//                    (<setup>/task-space-decisions.json maps the unit to the
//                    device and the options)
//   relocations      setup locations under an old prefix move to a new one;
//                    every change is recorded (<setup>/relocations/<id>.json)
//                    so it can be undone
//
// Like the rest of the package, these functions expect the caller to hold
// Lock() (the server's setup actions do).

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
)

const (
	decisionsFile   = "task-space-decisions.json"
	relocationsDir  = "relocations"
	maxDecisions    = 200
	taskSpaceID     = "task-space"
	maxTaskOptions  = 3
	relocationLimit = 2000
)

// taskSpaceCandidates are the task_space resources that live on a file
// system (an issue tracker or another ARK project is not a folder).
func taskSpaceCandidates(vw *view) []*obj {
	var out []*obj
	for _, item := range vw.full {
		if item.Value("kind") != "task_space" {
			continue
		}
		if k := str(get(mapOf(item.Value("details")), "kind")); k == "issue-tracker" {
			continue
		}
		local := len(listOr(item.Value("locations"))) == 0
		for _, l := range listOr(item.Value("locations")) {
			if IsLocalPath(str(mapOf(l).Value("path_or_ref"))) {
				local = true
			}
		}
		if local {
			out = append(out, item)
		}
	}
	return out
}

// defaultTaskSpace is the task space "task:" paths name: a read-write one
// first, then the one with locations on the most devices, then setup order.
func defaultTaskSpace(vw *view) *obj {
	var best *obj
	bestScore := -1
	for _, item := range taskSpaceCandidates(vw) {
		score := 0
		if get(mapOf(item.Value("details")), "access") == "read-write" {
			score += 1000
		}
		hosts := map[string]bool{}
		for _, l := range listOr(item.Value("locations")) {
			loc := mapOf(l)
			if IsLocalPath(str(loc.Value("path_or_ref"))) {
				hosts[str(loc.Value("host_id"))] = true
			}
		}
		score += len(hosts)
		if score > bestScore {
			best, bestScore = item, score
		}
	}
	return best
}

// localPathOn is the item's newest local path on device host ("" if none).
func localPathOn(item *obj, host string, al hostAliases) string {
	path := ""
	for _, l := range listOr(item.Value("locations")) {
		loc := mapOf(l)
		if p := str(loc.Value("path_or_ref")); al.same(loc.Value("host_id"), host) && IsLocalPath(p) {
			path = p
		}
	}
	return path
}

// remoteRef is the item's first location usable from anywhere (ssh://, a URL).
func remoteRef(item *obj) string {
	for _, l := range listOr(item.Value("locations")) {
		if p := str(mapOf(l).Value("path_or_ref")); p != "" && !IsLocalPath(p) && p != "local" {
			return p
		}
	}
	return ""
}

// Paths is GET /api/agent/v1/setup/paths: the viewer's device's paths.
func (e *Env) Paths(v *Viewer) (*obj, error) {
	vw, err := e.resolve(v)
	if err != nil {
		return nil, err
	}
	al := e.aliases()
	host := v.HostID
	mine := func(item *obj) bool { return item.Value("layer") == "project" || v.isUser(item.Value("owner")) }
	entry := func(item *obj) *obj {
		var path any
		if p := localPathOn(item, host, al); p != "" {
			path = p
		}
		out := pyjson.NewObject("id", item.Value("id"), "key", item.Value("key"), "name", item.Value("name"),
			"layer", item.Value("layer"), "path", path)
		if path == nil {
			if r := remoteRef(item); r != "" {
				out.Set("remote", r)
			}
		}
		return out
	}
	def := defaultTaskSpace(vw)
	missing := []any{}
	var taskSpace any
	spaces := []any{}
	for _, item := range taskSpaceCandidates(vw) {
		en := entry(item).Set("default", item == def)
		spaces = append(spaces, en)
		if item == def && en.Value("path") != nil {
			taskSpace = en
		}
	}
	if taskSpace == nil {
		missing = append(missing, "task_space")
	}
	repos, storage := []any{}, []any{}
	var artifacts any
	for _, item := range vw.full {
		kind := str(item.Value("kind"))
		if kind != "repository" && kind != "storage" {
			continue
		}
		en := entry(item)
		if kind == "repository" {
			repos = append(repos, en)
		} else {
			en.Set("kind", get(mapOf(item.Value("details")), "kind"))
			storage = append(storage, en)
			if artifacts == nil && en.Value("kind") == "artifacts" && en.Value("path") != nil {
				artifacts = en.Value("path")
			}
		}
		if en.Value("path") == nil && en.Value("remote") == nil && mine(item) {
			missing = append(missing, item.Value("key"))
		}
	}
	folder := "<task space>/<YYYYMMDD>_<task ID>-<name>/"
	if ts := mapOf(taskSpace); ts != nil {
		folder = JoinTaskPath(str(ts.Value("path")), "<YYYYMMDD>_<task ID>-<name>/")
	}
	out := pyjson.NewObject("host", vw.host.Copy(), "user", or(v.User, nil),
		"task_space", taskSpace, "task_spaces", spaces, "repositories", repos, "storage", storage,
		"artifacts", artifacts, "missing", missing,
		"task_folder", pyjson.NewObject("pattern", folder, "subfolders", []any{"artifacts/", "downloads/", "renders/", "logs/"},
			"logical", LogicalScheme+"<YYYYMMDD>_<task ID>-<name>/"),
		"scratch", "your own scratch and temporary downloads: your session workspace or the OS temp dir; anything a task needs later goes in its folder")
	if d := e.openDecision(v.User, host, al); d != nil {
		out.Set("decision", pyjson.NewObject("uid", d.Value("uid"), "state", d.Value("state")))
	}
	next := "write files only in the places listed in writable; task-related files go in the folder of the unit they belong to: ask with ?unit=<UID> (task_folder.path): its task's folder, else the unit's own folder; write absolute paths with this device in ARK"
	if taskSpace == nil {
		next = "this device has no task space: do not choose a folder yourself; POST /api/agent/v1/setup/task-space-decision " +
			"with 2-3 options (path, free space, why) and your recommendation, then wait for the person's answer"
		if d := mapOf(out.Value("decision")); d != nil {
			next = "the decision " + str(d.Value("uid")) + " asks the person to choose this device's task space: wait for the answer"
		}
	}
	return out.Set("next", next), nil
}

// LogicalRoots are every local location of every task space, for the
// absolute -> task: pass ("" space for the default task space).
func (e *Env) LogicalRoots(v *Viewer) ([]LogicalRoot, error) {
	vw, err := e.resolve(v)
	if err != nil {
		return nil, err
	}
	def := defaultTaskSpace(vw)
	var out []LogicalRoot
	for _, item := range taskSpaceCandidates(vw) {
		space := ""
		if item != def {
			space = str(item.Value("id"))
		}
		for _, l := range listOr(item.Value("locations")) {
			if p := str(mapOf(l).Value("path_or_ref")); IsLocalPath(p) && !strings.HasPrefix(p, "~") {
				out = append(out, LogicalRoot{Path: p, Space: space})
			}
		}
	}
	return out, nil
}

// ------------------------------------------------------------- decisions

// TaskSpaceOption is one choice of a task-space decision.
type TaskSpaceOption struct {
	Path string `json:"path"`
	Free string `json:"free,omitempty"`
	Note string `json:"note,omitempty"`
}

func (e *Env) decisionsPath() string { return join(e.dir(), decisionsFile) }

func (e *Env) decisions() []*obj {
	var out []*obj
	if raw := readJSON(e.decisionsPath()); raw != nil {
		for _, x := range listOr(raw.Value("decisions")) {
			if m := mapOf(x); m != nil {
				out = append(out, m)
			}
		}
	}
	return out
}

func (e *Env) saveDecisions(items []*obj) error {
	if len(items) > maxDecisions {
		items = items[len(items)-maxDecisions:]
	}
	list := make([]any, len(items))
	for i, x := range items {
		list[i] = x
	}
	return writePlain(e.decisionsPath(), pyjson.NewObject("decisions", list))
}

func (e *Env) openDecision(user, host string, al hostAliases) *obj {
	if user == "" {
		return nil
	}
	for _, d := range e.decisions() {
		if d.Value("state") == "open" && lower(str(d.Value("user"))) == lower(user) && al.same(d.Value("host_id"), host) {
			return d
		}
	}
	return nil
}

// HasTaskSpaceDecisions is whether the project has any (cheap check before
// looking a unit up on every decided comment).
func (e *Env) HasTaskSpaceDecisions() bool {
	_, err := os.Stat(e.decisionsPath())
	return err == nil
}

// CheckTaskSpaceOptions validates the options of a new decision.
func CheckTaskSpaceOptions(options []TaskSpaceOption, recommended int) error {
	if len(options) < 1 || len(options) > maxTaskOptions {
		return setupErr("give 2-3 options (path, free space, why)", 400, "SETUP_INVALID")
	}
	seen := map[string]bool{}
	for i, o := range options {
		p, ok := cleanPrefix(o.Path)
		if !ok {
			return setupErr(fmt.Sprintf("options[%d].path must be an absolute folder path on the device", i), 400, "SETUP_INVALID")
		}
		if seen[lower(p)] {
			return setupErr("two options name the same folder", 400, "SETUP_INVALID")
		}
		seen[lower(p)] = true
		if len(FindSecretValues(pyjson.NewObject("path_or_ref", o.Path), "")) > 0 {
			return setupErr("refusing to store secret values", 400, "SETUP_SECRET")
		}
	}
	if recommended < 0 || recommended >= len(options) {
		return setupErr("recommended must be the index of one of the options", 400, "SETUP_INVALID")
	}
	return nil
}

// TaskSpaceDecisionTarget is the resource a decision fills for the viewer's
// device: the default task space, else "" (the answer creates one in the
// person's layer).  ok is false when this device already has a task space.
func (e *Env) TaskSpaceDecisionTarget(v *Viewer) (key string, ok bool, err error) {
	vw, err := e.resolve(v)
	if err != nil {
		return "", false, err
	}
	def := defaultTaskSpace(vw)
	if def == nil {
		return "", true, nil
	}
	if localPathOn(def, v.HostID, e.aliases()) != "" {
		return str(def.Value("key")), false, nil
	}
	return str(def.Value("key")), true, nil
}

// RegisterTaskSpaceDecision remembers which device and options unit uid asks about.
func (e *Env) RegisterTaskSpaceDecision(v *Viewer, uid, resource, hostLabel string, options []TaskSpaceOption, recommended int) error {
	items := []*obj{}
	al := e.aliases()
	for _, d := range e.decisions() {
		// An older open question for the same device is replaced.
		if d.Value("state") == "open" && lower(str(d.Value("user"))) == lower(v.User) && al.same(d.Value("host_id"), v.HostID) {
			d.Set("state", "replaced").Set("replaced_by", uid)
		}
		items = append(items, d)
	}
	opts := []any{}
	for _, o := range options {
		p, _ := cleanPrefix(o.Path)
		opts = append(opts, pyjson.NewObject("path", p, "free", text(o.Free, 40), "note", text(o.Note, 200)))
	}
	items = append(items, pyjson.NewObject("uid", uid, "user", v.User, "host_id", v.HostID, "host_label", text(hostLabel, 60),
		"resource", resource, "options", opts, "recommended", int64(recommended), "state", "open", "created_at", utcNow()))
	return e.saveDecisions(items)
}

var answerPathRe = regexp.MustCompile("(?:[A-Za-z]:[\\\\/]|/|~[\\\\/])[^\\s`'\"，。；、)）]*")

// chooseOption maps a decision answer to a folder: an option's path
// written in it, an option number (1-3), or else an absolute path typed in.
func chooseOption(answer string, options []any) (string, bool) {
	a := strings.TrimSpace(answer)
	norm := func(s string) string { return lower(strings.TrimRight(strings.ReplaceAll(s, "\\", "/"), "/")) }
	na := strings.ReplaceAll(lower(a), "\\", "/")
	bestLen, best := 0, ""
	for _, x := range options {
		p := str(mapOf(x).Value("path"))
		if n := norm(p); n != "" && strings.Contains(na, n) && len(n) > bestLen {
			best, bestLen = p, len(n)
		}
	}
	if best != "" {
		return best, true
	}
	if n, err := strconv.Atoi(strings.Trim(a, " .#)")); err == nil && n >= 1 && n <= len(options) {
		return str(mapOf(options[n-1]).Value("path")), true
	}
	if m := answerPathRe.FindString(a); m != "" {
		if p, ok := cleanPrefix(m); ok {
			return p, true
		}
	}
	return "", false
}

// ApplyTaskSpaceChoice writes the person's answer to decision uid into
// their own layer for the decision's device.  It returns nil, nil when uid
// is not a task-space decision.  Only the person the decision asks may
// answer it; an answer already applied is not applied again.
func (e *Env) ApplyTaskSpaceChoice(v *Viewer, uid, answer, actor string) (*obj, error) {
	items := e.decisions()
	var d *obj
	for _, x := range items {
		if x.Value("uid") == uid {
			d = x
		}
	}
	if d == nil {
		return nil, nil
	}
	if d.Value("state") == "applied" {
		return pyjson.NewObject("ok", true, "changed", false, "path", get(mapOf(d.Value("applied")), "path"),
			"host_id", d.Value("host_id")), nil
	}
	if !v.isUser(d.Value("user")) || v.Legacy {
		return nil, setupErr("only "+str(d.Value("user"))+" can choose the task space of their device", 403, "SETUP_NOT_OWNER")
	}
	path, ok := chooseOption(answer, listOr(d.Value("options")))
	if !ok {
		return nil, setupErr("the answer names no option and no absolute folder path", 400, "SETUP_INVALID")
	}
	dv := *v
	dv.HostID = str(d.Value("host_id"))
	label := str(d.Value("host_label"))
	key := str(d.Value("resource"))
	vw, err := e.resolve(&dv)
	if err != nil {
		return nil, err
	}
	found := false
	for _, item := range vw.full {
		if key != "" && item.Value("key") == key {
			found = true
		}
	}
	if !found {
		key = ""
		if def := defaultTaskSpace(vw); def != nil {
			key = str(def.Value("key"))
		}
	}
	confirmed := false
	if key == "" {
		// No task space in setup yet: one in the person's own layer.
		prev, err := e.loadLayer("user", v.User)
		if err != nil {
			return nil, err
		}
		prevState := str(prev.Value("state"))
		raw := pyjson.NewObject("resources", []any{pyjson.NewObject("id", taskSpaceID, "kind", "task_space",
			"name", "Task space", "purpose", "task folders, intermediate files and artifacts of this project",
			"details", pyjson.NewObject("kind", "ark", "access", "read-write", "relation", "task folders"))})
		res, err := e.SaveLayer(raw, &dv, "user", actor, false, label)
		if err != nil {
			return nil, err
		}
		key = resourceKey("user", v.User, taskSpaceID)
		// The answer is the person's confirmation of this one addition; a
		// layer with other unconfirmed changes stays for them to review.
		if prevState == "unset" || in(prevState, ReadyStates) {
			if _, err := e.ConfirmLayer(&dv, "user", "human:"+v.User, str(res.Value("digest")), "task space chosen in "+uid); err != nil {
				return nil, err
			}
			confirmed = true
		}
	}
	if _, err := e.AddLocation(&dv, key, path, actor, dv.HostID, label, ""); err != nil {
		return nil, err
	}
	d.Set("state", "applied").Set("applied", pyjson.NewObject("path", path, "resource", key, "at", utcNow(), "by", v.User))
	if err := e.saveDecisions(items); err != nil {
		return nil, err
	}
	return pyjson.NewObject("ok", true, "changed", true, "path", path, "resource", key, "host_id", dv.HostID,
		"host_label", label, "confirmed", confirmed), nil
}

// ------------------------------------------------------------- relocations

// LocationChange is one setup location moved by a relocation.
type LocationChange struct {
	Layer    string `json:"layer"`
	Owner    string `json:"owner,omitempty"`
	Resource string `json:"resource"`
	HostID   string `json:"host_id"`
	Before   string `json:"before"`
	After    string `json:"after"`
}

// relocTargets are the layers the viewer may move locations in: their own
// layer, and the project layer for a project admin.
func relocTargets(v *Viewer) [][2]string {
	var out [][2]string
	if v.User != "" && !v.Legacy {
		out = append(out, [2]string{"user", v.User})
	}
	if v.Admin || v.Legacy {
		out = append(out, [2]string{"project", ""})
	}
	return out
}

// PlanLocationMoves lists the locations of device host (all devices when
// host is "") under prefix from that would move to to.
func (e *Env) PlanLocationMoves(v *Viewer, host, from, to string) ([]LocationChange, error) {
	al := e.aliases()
	var out []LocationChange
	for _, t := range relocTargets(v) {
		rec, err := e.loadLayer(t[0], t[1])
		if err != nil {
			return nil, err
		}
		for _, x := range listOr(rec.Value("locations")) {
			loc := mapOf(x)
			if host != "" && !al.same(loc.Value("host_id"), host) {
				continue
			}
			p := str(loc.Value("path_or_ref"))
			if !IsLocalPath(p) {
				continue
			}
			np, n := RewritePrefix(p, from, to)
			if n == 0 || np == p {
				continue
			}
			out = append(out, LocationChange{Layer: t[0], Owner: t[1], Resource: str(loc.Value("resource")),
				HostID: str(loc.Value("host_id")), Before: p, After: np})
		}
	}
	return out, nil
}

// ApplyLocationMoves writes changes (undo: the same changes reversed).  A
// location no longer at Before is skipped.  Returns the changes written.
func (e *Env) ApplyLocationMoves(v *Viewer, changes []LocationChange, actor string, undo bool) ([]LocationChange, error) {
	allowed := map[string]bool{}
	for _, t := range relocTargets(v) {
		allowed[t[0]+"\x00"+t[1]] = true
	}
	byLayer := map[string][]LocationChange{}
	order := []string{}
	for _, c := range changes {
		k := c.Layer + "\x00" + c.Owner
		if !allowed[k] {
			return nil, setupErr("you can only move locations in your own layer (and the project layer as a project admin)", 403, "SETUP_NOT_OWNER")
		}
		if _, seen := byLayer[k]; !seen {
			order = append(order, k)
		}
		byLayer[k] = append(byLayer[k], c)
	}
	var done []LocationChange
	for _, k := range order {
		list := byLayer[k]
		layer, owner := list[0].Layer, list[0].Owner
		rec, err := e.loadLayer(layer, owner)
		if err != nil {
			return nil, err
		}
		changed := false
		for _, c := range list {
			from, to := c.Before, c.After
			if undo {
				from, to = to, from
			}
			for _, x := range listOr(rec.Value("locations")) {
				loc := mapOf(x)
				if loc.Value("resource") == c.Resource && loc.Value("host_id") == c.HostID && str(loc.Value("path_or_ref")) == from {
					loc.Set("path_or_ref", to).Set("verified_at", "").Set("verified_by", "")
					done = append(done, c)
					changed = true
					break
				}
			}
		}
		if !changed {
			continue
		}
		now := utcNow()
		rec.Set("updated_at", now).Set("updated_by", actor)
		event := "relocate"
		if undo {
			event = "relocate-undo"
		}
		if err := e.write(e.layerTarget(layer, owner), rec, pyjson.NewObject("at", now, "actor", actor,
			"layer", layerName(layer, owner), "event", event)); err != nil {
			return nil, err
		}
	}
	return done, nil
}

// NewRelocationID is a fresh "rl_" id.
func NewRelocationID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "rl_" + hex.EncodeToString(b[:])
}

var relocIDRe = regexp.MustCompile(`^rl_[0-9a-f]{12}$`)

func (e *Env) relocationPath(id string) string {
	return join(join(e.dir(), relocationsDir), id+".json")
}

// SaveRelocation stores a relocation record (any JSON value with an "id").
func (e *Env) SaveRelocation(id string, record any) error {
	if !relocIDRe.MatchString(id) {
		return setupErr("bad relocation id", 400, "SETUP_INVALID")
	}
	b, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	path := e.relocationPath(id)
	if err := os.MkdirAll(pathParent(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadRelocation reads a relocation record into out.
func (e *Env) LoadRelocation(id string, out any) error {
	if !relocIDRe.MatchString(id) {
		return setupErr("bad relocation id", 400, "SETUP_INVALID")
	}
	b, err := os.ReadFile(e.relocationPath(id))
	if err != nil {
		return setupErr("no such relocation", 404, "SETUP_NO_RELOCATION")
	}
	return json.Unmarshal(b, out)
}

// ValidPrefix reports whether p can be a relocation prefix (an absolute
// folder below a drive or "/").
func ValidPrefix(p string) bool {
	_, ok := cleanPrefix(p)
	return ok
}

// RelocationLimit bounds the edits of one relocation.
const RelocationLimit = relocationLimit
