package server

// What an agent ARK launches does first (Go only, 2026-11-24, T-0121).
// Without a pasted Home prompt the agent still knows whether to run setup,
// register this device, continue a task, resume, follow the first message
// or wait for it: sessions.Hooks.StartPoint gathers the facts here and
// internal/sessionstart writes the Start section of the session block.
//
//   GET /api/agent-sessions/start?task=<uid>   the Run dialog's default and
//        preview: {mode, summary, title, task, setup{...}, task_state{...}}
//
// The agent can re-read its start: GET /api/agent/v1/context answers it as
// "session_start" for a request of a session ARK launched.

import (
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/sessions"
	"arkstudio/go/internal/sessionstart"
	"arkstudio/go/internal/setup"
)

const sessionStartPath = "/api/agent-sessions/start"

var sessionStartGoOnly = []PortedRoute{{"goonly:exact:" + sessionStartPath, "GET,HEAD"}}

// sessionStartFacts are the setup readiness of the device sessions run on
// (this server) for actor, and the state of task taskRef ("" = none).
func (c *reqCtx) sessionStartFacts(p *project.Project, actor, taskRef string, folder *sessionstart.Folder) sessionstart.Facts {
	f := sessionstart.Facts{Setup: c.sessionSetupFacts(p, actor)}
	if taskRef != "" {
		f.Task = c.sessionTaskFacts(p, taskRef)
	}
	f.Folder = folder
	if f.Folder == nil {
		f.Folder = c.sessionFolder(p, actor, taskRef)
	}
	return f
}

// sessionFolder is the task folder of taskRef on the device sessions run on
// (this server) for actor; nil when setup cannot be read.
func (c *reqCtx) sessionFolder(p *project.Project, actor, taskRef string) *sessionstart.Folder {
	paths := c.sessionPaths(p, actor)
	if paths == nil {
		return nil
	}
	return c.taskFolderFacts(p, paths, taskRef, true)
}

// sessionPaths is GET setup/paths for actor on this server's device.
func (c *reqCtx) sessionPaths(p *project.Project, actor string) *pyjson.Object {
	if actor == "" {
		actor = c.actor()
	}
	perm := "admin"
	if c.app.Auth != nil {
		perm = "read"
		if pr, err := c.app.Org.ProjectPermission(c.ctx, actor, p.ID()); err == nil && pr != "" {
			perm = pr
		}
	}
	rank := org.PermissionRank[perm]
	v := setup.NewViewer(setup.Viewer{User: actor, Admin: rank >= org.PermissionRank["admin"], CanWrite: rank >= org.PermissionRank["write"],
		TokenKind: "personal", HostID: setup.MachineFingerprint()})
	setup.Lock()
	paths, err := c.setupEnv(p).Paths(v)
	setup.Unlock()
	if err != nil {
		return nil
	}
	return paths
}

func (c *reqCtx) sessionSetupFacts(p *project.Project, actor string) sessionstart.Setup {
	if actor == "" {
		actor = c.actor()
	}
	perm := "admin"
	if c.app.Auth != nil {
		perm = "read"
		if pr, err := c.app.Org.ProjectPermission(c.ctx, actor, p.ID()); err == nil && pr != "" {
			perm = pr
		}
	}
	rank := org.PermissionRank[perm]
	v := setup.NewViewer(setup.Viewer{User: actor, Admin: rank >= org.PermissionRank["admin"], CanWrite: rank >= org.PermissionRank["write"],
		TokenKind: "personal", HostID: setup.MachineFingerprint()})
	payload, err := c.setupPayloadFor(p, v, false)
	if err != nil || payload == nil {
		return sessionstart.Setup{}
	}
	r, ok := payload.Value("readiness").(*pyjson.Object)
	if !ok {
		return sessionstart.Setup{}
	}
	out := sessionstart.Setup{Known: true, ProjectState: strOrEmpty(r.Value("project_state")), UserState: strOrEmpty(r.Value("user_state")),
		HostID: v.HostID}
	if out.ProjectState == "" {
		out.ProjectState = strOrEmpty(r.Value("state"))
	}
	if host, ok := r.Value("host").(*pyjson.Object); ok {
		out.HostLabel = strOrEmpty(host.Value("label"))
	}
	if list, ok := r.Value("needs_location").([]any); ok {
		for _, k := range list {
			if s, ok := k.(string); ok && s != "" {
				out.NeedsLocation = append(out.NeedsLocation, s)
			}
		}
	}
	return out
}

func (c *reqCtx) sessionTaskFacts(p *project.Project, ref string) *sessionstart.Task {
	u, err := p.Store.GetUnit(c.ctx, ref)
	if err != nil || u == nil {
		return nil
	}
	t := &sessionstart.Task{UID: strOrEmpty(u.Value("uid")), Type: strOrEmpty(u.Value("type")), Title: strOrEmpty(u.Value("title")), Status: strOrEmpty(u.Value("status")),
		LastProgress: sessionstart.LastProgress(strOrEmpty(u.Value("body")))}
	if comments, err := p.Store.Comments(c.ctx, t.UID); err == nil {
		for _, x := range comments {
			o, ok := x.(*pyjson.Object)
			if !ok || o.Value("author_kind") != "user" || o.Value("resolved") != false {
				continue
			}
			id, _ := o.Value("id").(int64)
			t.Open = append(t.Open, sessionstart.Comment{ID: id, Kind: strOrEmpty(o.Value("kind")), Author: strOrEmpty(o.Value("author")),
				Body: strOrEmpty(o.Value("body"))})
		}
	}
	if info, err := p.Store.ReviewInfo(c.ctx, t.UID); err == nil && info != nil {
		t.InReview = info.Value("in_review") == true
		if rounds, ok := info.Value("rounds").([]any); ok && len(rounds) > 0 {
			if last, ok := rounds[len(rounds)-1].(*pyjson.Object); ok {
				t.LastOutcome = strOrEmpty(last.Value("outcome"))
			}
		}
	}
	return t
}

// sessionStartPoint is sessions.Hooks.StartPoint.
func (c *reqCtx) sessionStartPoint(in sessions.StartInput) *sessionstart.Start {
	p, err := c.require("read")
	if err != nil || p == nil {
		return nil
	}
	f := c.sessionStartFacts(p, in.Actor, in.TaskRef, in.Folder)
	f.Prompt, f.Chosen, f.Resume = in.Prompt, in.Chosen, in.Resume
	st := sessionstart.Compose(f)
	return &st
}

// sessionStartGet answers GET /api/agent-sessions/start: what a session
// launched now would do first without a choice, for the Run dialog.
func (c *reqCtx) sessionStartGet() error {
	p, err := c.require("read")
	if err != nil {
		return err
	}
	task := strings.TrimSpace(c.query.First("task", ""))
	f := c.sessionStartFacts(p, c.actor(), task, nil)
	mode := sessionstart.DefaultMode(f)
	st := sessionstart.Compose(f)
	setupOut := pyjson.NewObject("known", f.Setup.Known, "needs_setup", f.Setup.NeedsSetup(), "needs_device", f.Setup.NeedsDevice(),
		"project_state", f.Setup.ProjectState, "user_state", f.Setup.UserState, "needs_location", strList(f.Setup.NeedsLocation),
		"host_id", f.Setup.HostID, "host_label", f.Setup.HostLabel)
	var taskOut any
	if f.Task != nil {
		taskOut = pyjson.NewObject("uid", f.Task.UID, "title", f.Task.Title, "status", f.Task.Status, "in_review", f.Task.InReview,
			"last_outcome", f.Task.LastOutcome, "open_comments", int64(len(f.Task.Open)), "send_backs", int64(f.Task.SendBacks()))
	}
	c.ok(pyjson.NewObject("mode", mode, "summary", st.Summary, "setup", setupOut, "task", taskOut))
	return nil
}

func strList(list []string) []any {
	out := make([]any, 0, len(list))
	for _, s := range list {
		out = append(out, s)
	}
	return out
}

// sessionStartOf is the start of the ARK session the request comes from
// (agent_session.go), for GET /api/agent/v1/context; nil when none.
func (c *reqCtx) sessionStartOf() any {
	id, _ := c.agentSessionLink()
	if id == "" || c.app.Sessions == nil {
		return nil
	}
	s, err := c.app.Sessions.Get(id)
	if err != nil {
		return nil
	}
	if st, ok := c.app.Sessions.MetaValue(s, "start").(*pyjson.Object); ok {
		return st.Set("session", id)
	}
	return nil
}
