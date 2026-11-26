package server

// Task space and path relocation (Go only, 2026-11-24; internal/setup
// taskspace.go and pathtext.go).
//
//   GET  /api/agent/v1/setup/paths        the calling device's resolved task
//   GET  /api/setup/paths                 space, repositories, storage and
//                                         artifacts paths, plus missing: [...]
//                                         (?host= or X-ARK-Host)
//   POST /api/agent/v1/setup/task-space-decision
//        {options: [{path, free | free_bytes, note}], recommended: 0,
//         part_of?: uid, note?, lang?: "en"|"zh"}
//        opens the decision unit "Choose the task space on <device>" in
//        Needs you.  When the person answers it (comment kind "decided"),
//        ARK writes the chosen folder into their own setup layer for that
//        device; the agent is woken by the decision as usual.
//   POST /api/agent/v1/setup/relocate     (also /api/skills/setup/relocate)
//        {from, to, host_id?, all_devices?, apply?}       old -> new prefix
//        {logical: true, apply?}                         absolute -> task:
//        {undo: "rl_..."}
//        Without apply it is a dry run listing every unit body, attachment
//        link and setup location that would change.  Apply writes the unit
//        changes in one transaction and records them for undo.  Files are
//        never moved or deleted here: the person or the agent moves them.

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/sessionstart"
	"arkstudio/go/internal/setup"
	"arkstudio/go/internal/store"
)

const (
	agentSetupPathsPath = "/api/agent/v1/setup/paths"
	setupPathsPath      = "/api/setup/paths"
)

var taskSpaceGoOnly = []PortedRoute{
	{"goonly:exact:" + agentSetupPathsPath, "GET,HEAD"},
	{"goonly:exact:" + setupPathsPath, "GET,HEAD"},
}

// setupPathsGet answers both paths routes.
func (c *reqCtx) setupPathsGet() error {
	p, err := c.require("read")
	if err != nil {
		return err
	}
	v, err := c.setupViewer(p, nil, true)
	if err != nil {
		return err
	}
	env := c.setupEnv(p)
	setup.Lock()
	out, err := env.Paths(v)
	setup.Unlock()
	if err != nil {
		return setupAPIErr(err)
	}
	// The places this device may write to, and the exact task folder of the
	// unit asked about (?unit=) or of the ARK session the request comes from.
	out.Set("writable", setup.WritableRoots(out))
	unit := strings.TrimSpace(c.query.First("unit", ""))
	var f *sessionstart.Folder
	if unit != "" {
		f = c.taskFolderFacts(p, out, unit, v.HostID == setup.MachineFingerprint())
		// Asked for a unit on this machine: the folder is made now, so the
		// agent has its registered place from the first file.
		if f.Path != "" && !f.Exists && v.HostID == setup.MachineFingerprint() && f.TaskSpace != "" {
			if st, err := os.Stat(f.TaskSpace); err == nil && st.IsDir() && os.MkdirAll(f.Path, 0o755) == nil {
				f.Exists = true
			}
		}
	} else {
		f = c.sessionFolderOfRequest(p, out, v)
	}
	if f != nil {
		base, _ := out.Value("task_folder").(*pyjson.Object)
		out.Set("task_folder", taskFolderObject(base, f))
		if f.Path != "" {
			out.Set("next", "write this work's files only in task_folder.path ("+f.Path+"); write its absolute path and this device in ARK")
		}
	}
	c.ok(out)
	return nil
}

func taskText(lang, en, zh string) string {
	if lang == "zh" {
		return zh
	}
	return en
}

// humanBytes is "812 GB" (decimal units, as file managers show free space).
func humanBytes(n float64) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	i := 0
	for n >= 1000 && i < len(units)-1 {
		n /= 1000
		i++
	}
	if n >= 100 || i == 0 {
		return fmt.Sprintf("%.0f %s", n, units[i])
	}
	return fmt.Sprintf("%.1f %s", n, units[i])
}

// taskSpaceDecision is the setup action "task-space-decision".
func (c *reqCtx) taskSpaceDecision(p *project.Project, env *setup.Env, v *setup.Viewer, body *pyjson.Object, actor string) error {
	if _, err := c.require("write"); err != nil {
		return err
	}
	if v.User == "" || v.Legacy {
		return apiErr(403, "use a personal agent token: the choice is saved in that person's own setup layer", "SETUP_NO_USER")
	}
	if v.HostSource == "server" {
		return apiErr(400, "send X-ARK-Host with this device's id (ark-server agent host-id)", "SETUP_NO_HOST")
	}
	lang := pytext.Lower(pytext.Strip(orStrV(body.Value("lang"))))
	var options []setup.TaskSpaceOption
	list, _ := body.Value("options").([]any)
	for _, x := range list {
		o, ok := x.(*pyjson.Object)
		if !ok {
			return apiErr(400, "options must be objects {path, free, note}", "SETUP_INVALID")
		}
		free := pytext.Strip(orStrV(o.Value("free")))
		if f, ok := o.Value("free_bytes").(float64); ok && f > 0 {
			free = humanBytes(f)
		} else if n, ok := o.Value("free_bytes").(int64); ok && n > 0 {
			free = humanBytes(float64(n))
		}
		options = append(options, setup.TaskSpaceOption{Path: pytext.Strip(orStrV(o.Value("path"))), Free: free,
			Note: pytext.Strip(orStrV(o.Value("note")))})
	}
	rec := 0
	if n, ok := body.Value("recommended").(int64); ok {
		rec = int(n)
	} else if f, ok := body.Value("recommended").(float64); ok {
		rec = int(f)
	}
	if err := setup.CheckTaskSpaceOptions(options, rec); err != nil {
		return setupAPIErr(err)
	}
	// A task space is kept: never the OS temp dir or an ARK session folder.
	sessRoot := ""
	if c.app.Sessions != nil {
		sessRoot = c.app.Sessions.Root
	}
	for i, o := range options {
		if why := setup.ScratchPlace(o.Path, sessRoot); why != "" {
			return apiErr(400, fmt.Sprintf("options[%d] (%s) cannot be a task space: %s; offer a kept folder", i, o.Path, why), "SETUP_INVALID")
		}
	}
	key, need, err := env.TaskSpaceDecisionTarget(v)
	if err != nil {
		return setupAPIErr(err)
	}
	paths, err := env.Paths(v)
	if err != nil {
		return setupAPIErr(err)
	}
	if !need {
		ts, _ := paths.Value("task_space").(*pyjson.Object)
		return apiErr(409, "this device already has a task space: "+pyjson.Str(ts.Value("path")), "SETUP_TASK_SPACE_SET")
	}
	host, _ := paths.Value("host").(*pyjson.Object)
	label := pytext.Strip(orStrV(body.Value("host_label")))
	if label == "" {
		label = pyjson.Str(host.Value("label"))
	}
	var lines []string
	lines = append(lines, taskText(lang,
		fmt.Sprintf("This device (%s, `%s`) has no task space in the ARK setup. The agent does not choose a folder itself: "+
			"pick one and ARK saves it in your setup for this device, then the agent continues. Task folders, intermediate "+
			"files, downloads and artifacts of this project go only under it.", label, v.HostID),
		fmt.Sprintf("本设备（%s，`%s`）在 ARK 配置中还没有任务空间。智能体不会自己选文件夹：请选一个，ARK 会把它保存到你在本设备上的配置中，"+
			"然后智能体继续。本项目的任务文件夹、中间文件、下载和产物都只放在它下面。", label, v.HostID)), "", taskText(lang, "## Options", "## 选项"))
	for i, o := range options {
		line := "- `" + o.Path + "`"
		if o.Free != "" {
			line += taskText(lang, " — "+o.Free+" free", " — 可用 "+o.Free)
		}
		if o.Note != "" {
			line += " — " + o.Note
		}
		if i == rec {
			line += taskText(lang, " (recommended)", "（推荐）")
		}
		lines = append(lines, line)
	}
	lines = append(lines, "", taskText(lang, "To use another folder, answer with its absolute path.",
		"要用别的文件夹，直接回答它的绝对路径。"))
	if note := pytext.Strip(orStrV(body.Value("note"))); note != "" {
		lines = append(lines, "", note)
	}
	data := pyjson.NewObject("type", "decision", "status", "open", "importance", "p1",
		"title", taskText(lang, "Choose the task space on "+label, "选择 "+label+" 上的任务空间"),
		"body", strings.Join(lines, "\n"), "assignee", v.User)
	if parent := pytext.Strip(orStrV(body.Value("part_of"))); parent != "" {
		data.Set("links", []any{pyjson.NewObject("target", parent, "relation", "part_of")})
	}
	unit, err := p.Store.CreateUnit(c.ctx, data, actor, c.who(p))
	if err != nil {
		return err
	}
	uid := pyjson.Str(unit.Value("uid"))
	if err := env.RegisterTaskSpaceDecision(v, uid, key, label, options, rec); err != nil {
		return setupAPIErr(err)
	}
	c.ok(pyjson.NewObject("ok", true, "unit", unit, "decision", pyjson.NewObject("uid", uid, "host_id", v.HostID,
		"resource", key, "state", "open"),
		"next", "wait for the person's answer; GET /api/agent/v1/setup/paths shows the task space once it is chosen"))
	return nil
}

// taskSpaceAnswered applies a person's answer (comment kind "decided") to a
// task-space decision.  Errors never fail the comment: the outcome is
// appended to the decision's body, where the person and the agent see it.
func (c *reqCtx) taskSpaceAnswered(p *project.Project, ref string, answer any) {
	if c.agentToken() != "" || c.token != nil {
		return // agents do not answer decisions for a person
	}
	env := c.setupEnv(p)
	if !env.HasTaskSpaceDecisions() {
		return
	}
	unit, err := p.Store.GetUnit(c.ctx, ref)
	if err != nil || unit == nil {
		return
	}
	uid := pyjson.Str(unit.Value("uid"))
	v, err := c.setupViewer(p, nil, false)
	if err != nil {
		return
	}
	user := v.User
	setup.Lock()
	r, err := env.ApplyTaskSpaceChoice(v, uid, orStrV(answer), "human:"+user)
	setup.Unlock()
	if r == nil && err == nil {
		return
	}
	stamp := clock.UTCNow()
	if len(stamp) > 16 {
		stamp = strings.Replace(stamp[:16], "T", " ", 1)
	}
	var line string
	if err != nil {
		msg := err.Error()
		if se, ok := err.(*setup.Error); ok {
			msg = se.Message
		}
		line = fmt.Sprintf("**%s** Not saved: %s. Answer again with one of the options or an absolute folder path.", stamp, msg)
	} else if !pyjson.Truthy(r.Value("changed")) {
		return
	} else {
		line = fmt.Sprintf("**%s** Saved: the task space on %s is `%s` (setup, %s's layer).", stamp,
			pyjson.Str(r.Value("host_label")), pyjson.Str(r.Value("path")), user)
		p.Store.LogActivity(c.ctx, "human:"+user, "setup.user.task_space", unit.Value("id"), //nolint:errcheck
			pyjson.NewObject("host", r.Value("host_id"), "path", r.Value("path"), "decision", uid))
	}
	body := strings.TrimRight(pyjson.Str(unit.Value("body")), "\n") + "\n\n" + line + "\n"
	p.Store.UpdateUnit(c.ctx, uid, pyjson.NewObject("body", body), "human:"+user, store.Who{}) //nolint:errcheck
}

// relocation is the record of an applied relocation (<setup>/relocations/<id>.json).
type relocation struct {
	ID        string                 `json:"id"`
	Mode      string                 `json:"mode"` // "prefix" | "logical"
	From      string                 `json:"from,omitempty"`
	To        string                 `json:"to,omitempty"`
	Host      string                 `json:"host,omitempty"`
	At        string                 `json:"at"`
	By        string                 `json:"by"`
	Edits     []store.TextEdit       `json:"edits"`
	Locations []setup.LocationChange `json:"locations"`
	UndoneAt  string                 `json:"undone_at,omitempty"`
}

// changedLines are up to three changed lines of a text, before and after.
func changedLines(before, after string) []any {
	b, a := strings.Split(before, "\n"), strings.Split(after, "\n")
	out := []any{}
	for i := 0; i < len(b) && i < len(a) && len(out) < 3; i++ {
		if b[i] != a[i] {
			out = append(out, pyjson.NewObject("before", clip(b[i], 240), "after", clip(a[i], 240)))
		}
	}
	return out
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// relocatableURL: attachment links that name a file on disk (uploaded
// assets are project-relative and move with the project).
func relocatableURL(u string) bool {
	return strings.HasPrefix(strings.ToLower(u), "file:") || setup.IsLocalPath(u)
}

// relocate is the setup action "relocate".
func (c *reqCtx) relocate(p *project.Project, env *setup.Env, v *setup.Viewer, body *pyjson.Object, actor string) error {
	if _, err := c.require("write"); err != nil {
		return err
	}
	if id := pytext.Strip(orStrV(body.Value("undo"))); id != "" {
		return c.relocateUndo(p, env, v, id, actor)
	}
	logical := pyjson.Truthy(body.Value("logical"))
	from := pytext.Strip(orStrV(body.Value("from")))
	to := pytext.Strip(orStrV(body.Value("to")))
	host := ""
	var roots []setup.LogicalRoot
	var rewrite func(string) (string, int)
	mode := "prefix"
	if logical {
		mode = "logical"
		var err error
		if roots, err = env.LogicalRoots(v); err != nil {
			return setupAPIErr(err)
		}
		if len(roots) == 0 {
			return apiErr(409, "setup has no task space with a path: nothing to convert to task: paths", "SETUP_NO_TASK_SPACE")
		}
		rewrite = func(s string) (string, int) { return setup.ToLogical(s, roots) }
	} else {
		if !setup.ValidPrefix(from) || !setup.ValidPrefix(to) {
			return apiErr(400, "from and to must be absolute folder paths below a drive or / (not a whole drive)", "SETUP_INVALID")
		}
		if !pyjson.Truthy(body.Value("all_devices")) {
			host = setup.CleanHostID(body.Value("host_id"))
			if host == "" {
				host = v.HostID
			}
		}
		rewrite = func(s string) (string, int) { return setup.RewritePrefix(s, from, to) }
	}
	texts, err := p.Store.UnitTexts(c.ctx)
	if err != nil {
		return err
	}
	var edits []store.TextEdit
	units, media := []any{}, []any{}
	paths := 0
	for _, u := range texts {
		if nb, n := rewrite(u.Body); n > 0 && nb != u.Body {
			edits = append(edits, store.TextEdit{UID: u.UID, Before: u.Body, After: nb})
			units = append(units, pyjson.NewObject("uid", u.UID, "title", u.Title, "paths", int64(n), "lines", changedLines(u.Body, nb)))
			paths += n
		}
		for _, m := range u.Media {
			if !relocatableURL(m.URL) {
				continue
			}
			if nu, n := rewrite(m.URL); n > 0 && nu != m.URL {
				edits = append(edits, store.TextEdit{UID: u.UID, MediaID: m.ID, Before: m.URL, After: nu})
				media = append(media, pyjson.NewObject("uid", u.UID, "media_id", m.ID, "before", m.URL, "after", nu))
				paths += n
			}
		}
	}
	if len(edits) > setup.RelocationLimit {
		return apiErr(400, fmt.Sprintf("%d changes are more than one relocation may make (%d); use a narrower prefix",
			len(edits), setup.RelocationLimit), "SETUP_TOO_MANY")
	}
	var locs []setup.LocationChange
	if !logical {
		if locs, err = env.PlanLocationMoves(v, host, from, to); err != nil {
			return setupAPIErr(err)
		}
	}
	locOut := []any{}
	for _, l := range locs {
		locOut = append(locOut, pyjson.NewObject("layer", l.Layer, "owner", l.Owner, "resource", l.Resource,
			"host_id", l.HostID, "before", l.Before, "after", l.After))
	}
	out := pyjson.NewObject("ok", true, "mode", mode, "from", from, "to", to, "host", host,
		"units", units, "media", media, "locations", locOut,
		"totals", pyjson.NewObject("units", int64(len(units)), "media", int64(len(media)), "paths", int64(paths),
			"locations", int64(len(locs))),
		"note", "comments and session records are history and are not rewritten; files are not moved",
		"applied", false)
	if !pyjson.Truthy(body.Value("apply")) {
		c.ok(out)
		return nil
	}
	id := setup.NewRelocationID()
	applied, unchanged, conflicts, err := p.Store.ApplyTextEdits(c.ctx, edits, id, actor)
	if err != nil {
		return err
	}
	doneLocs, err := env.ApplyLocationMoves(v, locs, actor, false)
	if err != nil {
		return setupAPIErr(err)
	}
	rec := relocation{ID: id, Mode: mode, From: from, To: to, Host: host, At: clock.UTCNow(), By: actor,
		Edits: applied, Locations: doneLocs}
	if rec.Edits == nil {
		rec.Edits = []store.TextEdit{}
	}
	if rec.Locations == nil {
		rec.Locations = []setup.LocationChange{}
	}
	if err := env.SaveRelocation(id, rec); err != nil {
		return setupAPIErr(err)
	}
	p.Store.LogActivity(c.ctx, actor, "setup.relocate", nil, pyjson.NewObject("relocation", id, "mode", mode, //nolint:errcheck
		"from", from, "to", to, "units", int64(len(applied)), "locations", int64(len(doneLocs))))
	conflictUIDs := []any{}
	for _, e := range conflicts {
		conflictUIDs = append(conflictUIDs, e.UID)
	}
	out.Set("applied", true).Set("relocation", id).Set("written", int64(len(applied))).
		Set("unchanged", int64(len(unchanged))).Set("conflicts", conflictUIDs).
		Set("locations_written", int64(len(doneLocs))).
		Set("undo", pyjson.NewObject("action", "relocate", "body", pyjson.NewObject("undo", id)))
	c.ok(out)
	return nil
}

func (c *reqCtx) relocateUndo(p *project.Project, env *setup.Env, v *setup.Viewer, id, actor string) error {
	var rec relocation
	if err := env.LoadRelocation(id, &rec); err != nil {
		return setupAPIErr(err)
	}
	if rec.UndoneAt != "" {
		c.ok(pyjson.NewObject("ok", true, "relocation", id, "undone", false, "undone_at", rec.UndoneAt))
		return nil
	}
	reverse := make([]store.TextEdit, len(rec.Edits))
	for i, e := range rec.Edits {
		reverse[i] = store.TextEdit{UID: e.UID, MediaID: e.MediaID, Before: e.After, After: e.Before}
	}
	applied, _, conflicts, err := p.Store.ApplyTextEdits(c.ctx, reverse, id+"_undo", actor)
	if err != nil {
		return err
	}
	locs, err := env.ApplyLocationMoves(v, rec.Locations, actor, true)
	if err != nil {
		return setupAPIErr(err)
	}
	rec.UndoneAt = clock.UTCNow()
	if err := env.SaveRelocation(id, rec); err != nil {
		return setupAPIErr(err)
	}
	p.Store.LogActivity(c.ctx, actor, "setup.relocate.undo", nil, pyjson.NewObject("relocation", id)) //nolint:errcheck
	conflictUIDs := []any{}
	for _, e := range conflicts {
		conflictUIDs = append(conflictUIDs, e.UID)
	}
	c.ok(pyjson.NewObject("ok", true, "relocation", id, "undone", true, "written", int64(len(applied)),
		"conflicts", conflictUIDs, "locations_written", int64(len(locs))))
	return nil
}
