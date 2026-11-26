package server

// The exact task folder of a unit on a device (2026-11-25, T-0128).
//
// ARK names the folder instead of leaving it to the agent: for a task, its
// own folder in the device's task space; for any other unit (an idea, a
// question, an experiment, a result), the folder of the nearest task it
// belongs to, found by following part_of and derived_from links upwards.
// An existing folder of that task in the task space is reused; otherwise the
// name is <YYYYMMDD of the task's creation>_<task ID>-<slug of its title>.
// A unit that belongs to no task gets no folder: the agent creates the task
// first (C2) and uses its folder.  ARK never creates tasks itself.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/sessions"
	"arkstudio/go/internal/sessionstart"
	"arkstudio/go/internal/setup"
	"arkstudio/go/internal/store"
)

// maxTaskHops bounds the walk up the graph.
const maxTaskHops = 6

// taskOf is the nearest task the unit belongs to (the unit itself when it
// is a task): its uid, title and creation time; ok false when there is none.
func (c *reqCtx) taskOf(p *project.Project, unit *pyjson.Object) (uid, title, created string, ok bool) {
	if unit == nil {
		return "", "", "", false
	}
	if strOrEmpty(unit.Value("type")) == "task" {
		return strOrEmpty(unit.Value("uid")), strOrEmpty(unit.Value("title")), strOrEmpty(unit.Value("created_at")), true
	}
	seen := map[string]bool{strOrEmpty(unit.Value("uid")): true}
	level := []string{strOrEmpty(unit.Value("uid"))}
	for hop := 0; hop < maxTaskHops && len(level) > 0; hop++ {
		var next []string
		for _, ref := range level {
			_, links, err := p.Store.UnitLinks(c.ctx, ref)
			if err != nil {
				continue
			}
			for _, x := range links {
				l, _ := x.(*pyjson.Object)
				if l == nil || l.Value("direction") != "out" {
					continue
				}
				if rel := strOrEmpty(l.Value("relation")); rel != "part_of" && rel != "derived_from" {
					continue
				}
				other, _ := l.Value("other").(*pyjson.Object)
				target := strOrEmpty(l.Value("target"))
				if other == nil || seen[target] {
					continue
				}
				seen[target] = true
				if strOrEmpty(other.Value("type")) == "task" {
					t, err := p.Store.GetUnit(c.ctx, target)
					if err != nil || t == nil {
						continue
					}
					return target, strOrEmpty(t.Value("title")), strOrEmpty(t.Value("created_at")), true
				}
				next = append(next, target)
			}
		}
		level = next
	}
	return "", "", "", false
}

// taskFolderFacts are the folder facts of unitRef for the device of paths (a
// setup Paths result).  Folders follow the unit of work, not the session
// (the user, 2026-11-26): the nearest task's folder (the unit itself when it
// is a task); without a task above it, the unit's own folder; no unit, no
// folder (a free session has none until it picks or creates a node).
// thisMachine: the device is the one the server runs on, so an existing
// folder can be looked up on disk.
func (c *reqCtx) taskFolderFacts(p *project.Project, paths *pyjson.Object, unitRef string, thisMachine bool) *sessionstart.Folder {
	f := &sessionstart.Folder{}
	if host, ok := paths.Value("host").(*pyjson.Object); ok {
		f.HostID, f.HostLabel = strOrEmpty(host.Value("id")), strOrEmpty(host.Value("label"))
	}
	if ts, ok := paths.Value("task_space").(*pyjson.Object); ok {
		f.TaskSpace = strOrEmpty(ts.Value("path"))
	}
	for _, x := range setup.WritableRoots(paths) {
		if o, ok := x.(*pyjson.Object); ok {
			f.Writable = append(f.Writable, strOrEmpty(o.Value("path")))
		}
	}
	var unit *pyjson.Object
	if unitRef != "" {
		unit, _ = p.Store.GetUnit(c.ctx, unitRef)
	}
	if unit == nil {
		return f
	}
	f.Unit, f.UnitType = strOrEmpty(unit.Value("uid")), strOrEmpty(unit.Value("type"))
	if uid, title, created, ok := c.taskOf(p, unit); ok {
		f.Task, f.Kind = uid, sessionstart.FolderTask
		c.place(f, uid, title, created, thisMachine)
		return f
	}
	f.Kind = sessionstart.FolderUnit
	c.place(f, f.Unit, strOrEmpty(unit.Value("title")), strOrEmpty(unit.Value("created_at")), thisMachine)
	return f
}

// place sets the folder of owner (a unit uid) in the task
// space: its existing folder, else <date>_<owner>-<slug>.
func (c *reqCtx) place(f *sessionstart.Folder, owner, title, created string, thisMachine bool) {
	f.Owner = owner
	if f.TaskSpace == "" {
		return
	}
	if thisMachine {
		if found := setup.FindTaskFolder(f.TaskSpace, owner); found != "" {
			f.Path, f.Exists = found, true
			return
		}
	}
	f.Path = setup.JoinTaskPath(f.TaskSpace, setup.TaskFolderName(created, owner, title))
	if thisMachine {
		if st, err := os.Stat(f.Path); err == nil && st.IsDir() {
			f.Exists = true
		}
	}
}

// taskFolderObject is the task_folder of GET setup/paths with a unit.
func taskFolderObject(base *pyjson.Object, f *sessionstart.Folder) *pyjson.Object {
	out := base
	if out == nil {
		out = pyjson.NewObject()
	}
	out.Set("unit", orNil(f.Unit)).Set("unit_type", orNil(f.UnitType)).Set("task", orNil(f.Task)).
		Set("kind", orNil(f.Kind)).Set("owner", orNil(f.Owner)).Set("path", orNil(f.Path)).Set("exists", f.Exists)
	out.Set("note", sessionstart.FolderNote(f))
	return out
}

// sessionFolderOfRequest is the folder of the starting unit of the ARK
// session the request comes from (nil when none), for convenience: work on
// another unit asks with ?unit=.
func (c *reqCtx) sessionFolderOfRequest(p *project.Project, paths *pyjson.Object, v *setup.Viewer) *sessionstart.Folder {
	id, _ := c.agentSessionLink()
	if id == "" || c.app.Sessions == nil {
		return nil
	}
	s, err := c.app.Sessions.Get(id)
	if err != nil {
		return nil
	}
	if f := sessions.FolderFromMeta(c.app.Sessions.MetaValue(s, "task_folder")); f != nil && f.Path != "" &&
		(f.HostID == "" || f.HostID == v.HostID) {
		return f
	}
	ref := strings.TrimSpace(strOrEmpty(c.app.Sessions.MetaValue(s, "task_ref")))
	return c.taskFolderFacts(p, paths, ref, v.HostID == setup.MachineFingerprint())
}

// referencedFiles is sessions.Manager.ReferencedFiles: the paths that units
// of the project updated since name in their body (at most 200 units).
func (a *App) referencedFiles(projectID string, since time.Time, paths []string) []string {
	ctx := context.Background()
	p := a.nudgeProject(ctx, projectID)
	if p == nil || len(paths) == 0 {
		return nil
	}
	f, err := store.FiltersFromObject(pyjson.NewObject("updated_since", since.UTC().Format(time.RFC3339)))
	if err != nil {
		return nil
	}
	list, err := p.Store.ListUnits(ctx, f, false)
	if err != nil || list == nil {
		return nil
	}
	items, _ := list.Value("items").([]any)
	var text strings.Builder
	for i, x := range items {
		if i >= 200 {
			break
		}
		o, _ := x.(*pyjson.Object)
		if o == nil {
			continue
		}
		body := strOrEmpty(o.Value("body"))
		if body == "" || o.Has("excerpt") {
			if u, err := p.Store.GetUnit(ctx, strOrEmpty(o.Value("uid"))); err == nil && u != nil {
				body = strOrEmpty(u.Value("body"))
			}
		}
		text.WriteString(body)
		text.WriteByte('\n')
	}
	all := text.String()
	var out []string
	for _, path := range paths {
		if strings.Contains(all, path) || strings.Contains(all, filepath.ToSlash(path)) {
			out = append(out, path)
		}
	}
	return out
}
