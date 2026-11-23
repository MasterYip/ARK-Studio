package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"arkstudio/go/internal/platform/clock"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/settings"
	"arkstudio/go/internal/store"
)

// Project lifecycle: App.create_project, open_project, delete_project,
// _resolve_project_ref, switch and the per-user tutorial project.

// demoProject is App._demo_project.
func (c *reqCtx) demoProject(reset bool) (*project.Project, error) {
	username := c.actor()
	if username == "" {
		username = org.LocalUsername
	}
	if reset {
		c.app.Projects.Drop(project.DemoUserPath(c.app.Root, username))
	}
	path, err := project.EnsureDemo(c.ctx, c.app.Root, username, reset)
	if err != nil {
		return nil, err
	}
	return c.app.Projects.Open(path)
}

// switchProject is App.switch.
func (c *reqCtx) switchProject(p *project.Project) error {
	a := c.app
	// A paired phone is signed in as its user: the web app's project list
	// moves its session (the caller checked read access).
	if s := c.mobileSession; s != nil && !a.Mobile.IsDesktopLink(s) {
		if p.ID() == demoID {
			return apiErr(403, "The tutorial project is not available on a paired phone.", "PROJECT_NOT_ACCESSIBLE")
		}
		return c.moveMobile(p)
	}
	if a.Auth != nil {
		if c.forcedProject != "" {
			return apiErr(403, "A paired phone stays on its paired project.", "MOBILE_WRONG_PROJECT")
		}
		// The rest of this request (the bootstrap reply) follows the new
		// project; other tabs keep sending their own X-ARK-Project.
		c.tabProject, c.tabProjectSet = p.ID(), true
		if c.session != nil {
			a.mu.Lock()
			a.sessionProjects[c.session.SidHash] = p.Path
			a.mu.Unlock()
			a.Auth.SetSessionProject(c.session, p.ID())
		}
		userID, err := a.Org.UserIDFor(c.ctx, c.actor())
		if err != nil {
			return err
		}
		if userID != "" && p.ID() != demoID {
			if err := a.Org.SetLastProject(c.ctx, userID, p.ID()); err != nil {
				return err
			}
		}
		c.project, c.resolved = p, true
		return nil
	}
	a.mu.Lock()
	a.project = p
	a.mu.Unlock()
	c.project, c.resolved = p, true
	return nil
}

func (c *reqCtx) replyBootstrap() error {
	payload, err := c.bootstrap()
	if err != nil {
		return err
	}
	c.ok(payload)
	return nil
}

// createProject is App.create_project.
func (c *reqCtx) createProject(body *pyjson.Object) error {
	_, fallback := c.app.Projects.NewProjectFolder() // routes_storage.go
	p, err := c.createProjectRecord(body)
	if err != nil {
		return err
	}
	if err := c.switchProject(p); err != nil {
		return err
	}
	if !fallback {
		return c.replyBootstrap()
	}
	payload, err := c.bootstrap()
	if err != nil {
		return err
	}
	// The chosen folder for new projects is missing (an unplugged drive):
	// the project went to the server's own folder.
	payload.Set("storage_notice", pyjson.NewObject("code", "STORAGE_FALLBACK",
		"configured", c.app.Projects.Storage().Default, "used", filepath.Dir(p.Path)))
	c.ok(payload)
	return nil
}

// createProjectRecord creates and registers a project for the actor (the
// part of POST /api/projects shared with POST /api/desktop/projects).
func (c *reqCtx) createProjectRecord(body *pyjson.Object) (*project.Project, error) {
	name := pytext.Strip(strOrEmpty(body.Value("name")))
	if name == "" {
		return nil, apiErr(400, "Project name is required", "")
	}
	if c.app.Auth != nil {
		role, _, err := c.app.Org.RoleFor(c.ctx, c.actor())
		if err != nil {
			return nil, err
		}
		if role != "owner" && role != "admin" && role != "member" {
			return nil, apiErr(403, "Only organization members can create projects.", "ORG_FORBIDDEN")
		}
	}
	template := "blank"
	if v := body.Value("template"); pyjson.Truthy(v) {
		template = pyjson.Str(v)
	}
	description := pytext.Prefix(pytext.Strip(strOrEmpty(body.Value("description"))), 600)
	p, err := c.app.Projects.Create(c.ctx, name, description, template, false, "")
	if err != nil {
		return nil, err
	}
	providers := pyjson.NewObject()
	for _, pid := range settings.PresetIDs() {
		if pid != "echo" && pid != "shell" {
			providers.Set(pid, pyjson.NewObject("enabled", false))
		}
	}
	encoded, err := p.Manifest.Mutate(func(m *pyjson.Object) error {
		s, ok := m.Value("settings").(*pyjson.Object)
		if !ok {
			s = pyjson.NewObject()
			m.Set("settings", s)
		}
		s.Set("agents", pyjson.NewObject("providers", providers))
		m.Set("updated", clock.UTCNow())
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := store.WriteManifest(p.Path, encoded); err != nil {
		return nil, err
	}
	summary, err := p.Summary()
	if err != nil {
		return nil, err
	}
	actor := c.actor()
	if err := c.app.Org.RegisterProject(c.ctx, summary, actor, true); err != nil {
		return nil, err
	}
	c.app.noteWriter(p.ID(), actor, c.writeActor()) // project_people.go
	actorID, err := c.app.Org.UserIDFor(c.ctx, actor)
	if err != nil {
		return nil, err
	}
	owners, err := c.app.Org.Owners(c.ctx)
	if err != nil {
		return nil, err
	}
	for _, owner := range owners {
		granted, err := c.app.Org.ProjectGrantUsers(c.ctx, p.ID())
		if err != nil {
			return nil, err
		}
		if !granted[owner.Str("id")] {
			if err := c.app.Org.SetProjectGrant(c.ctx, p.ID(), owner.Str("id"), "admin", actorID); err != nil {
				return nil, err
			}
		}
	}
	if err := c.app.Org.Log(c.ctx, actorID, "project.create", p.ID(), pyjson.NewObject("name", name)); err != nil {
		return nil, err
	}
	return p, nil
}

func strOrEmpty(v any) string {
	if !pyjson.Truthy(v) {
		return ""
	}
	return pyjson.Str(v)
}

// resolveProjectRef is App._resolve_project_ref.
func (c *reqCtx) resolveProjectRef(ref string) (*project.Project, error) {
	text := pytext.Strip(ref)
	if text == "" {
		return nil, apiErr(400, "Project id or path is required", "")
	}
	if text == project.DemoID {
		p, err := c.demoProject(true)
		if err != nil {
			return nil, apiErr(500, fmt.Sprintf("Cannot open the demo project: %v", err), "DEMO_UNAVAILABLE")
		}
		return p, nil
	}
	if !filepath.IsAbs(text) && !strings.HasSuffix(text, ".ark") {
		p, err := c.projectByID(text)
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, apiErr(404, "Project not found or not accessible.", "PROJECT_NOT_ACCESSIBLE")
		}
		return p, nil
	}
	root := project.Resolve(c.app.Root)
	target := text
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, text)
	}
	target = project.Resolve(target)
	// Inside a listed project folder (routes_storage.go); in local mode an
	// ARK project anywhere else lists its folder.
	if !c.app.Projects.InsideFolders(target) && !c.adoptProjectFolder(target) {
		return nil, apiErr(403, "Project folders must stay inside the configured root.", "PROJECT_PATH_DENIED")
	}
	if fi, err := os.Stat(target); err != nil || !fi.IsDir() {
		return nil, apiErr(404, "Project folder not found.", "PROJECT_NOT_ACCESSIBLE")
	}
	found := false
	for _, item := range c.app.Projects.ListProjects() {
		if project.Resolve(pyjson.Str(item.Value("path"))) == target {
			found = true
			break
		}
	}
	if !found {
		return nil, apiErr(404, "Not an ARK project folder.", "PROJECT_NOT_ACCESSIBLE")
	}
	p, err := c.app.Projects.Open(target)
	if err != nil {
		return nil, err
	}
	if _, ok, err := c.app.Org.ProjectByPath(c.ctx, target); err != nil {
		return nil, err
	} else if !ok {
		if err := c.app.Org.SyncProjects(c.ctx, c.app.Projects.ListProjects(), c.ownerBootstrapName()); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// openProject is App.open_project.
func (c *reqCtx) openProject(body *pyjson.Object) error {
	ref := body.Value("id")
	if !pyjson.Truthy(ref) {
		ref = body.Value("path")
	}
	text := pytext.Strip(strOrEmpty(ref))
	if text == "" {
		return apiErr(400, "Project id or path is required", "")
	}
	p, err := c.resolveProjectRef(text)
	if err != nil {
		return err
	}
	if c.app.Auth != nil && p.ID() != project.DemoID {
		if err := c.requireProjectPermission(p.ID(), "read"); err != nil {
			return err
		}
	}
	if err := c.switchProject(p); err != nil {
		return err
	}
	return c.replyBootstrap()
}

// requireProjectPermission is OrgService.require_project_permission.
func (c *reqCtx) requireProjectPermission(projectID, level string) error {
	perm, err := c.app.Org.ProjectPermission(c.ctx, c.actor(), projectID)
	if err != nil {
		return err
	}
	if perm == "" {
		return &org.Error{Status: 404, Message: "project not found or your account is not authorized for it", Code: "PROJECT_NOT_ACCESSIBLE"}
	}
	if org.PermissionRank[perm] < org.PermissionRank[level] {
		return &org.Error{Status: 403, Message: "this action needs " + level + " access to the project (your access is " + perm + ")",
			Code: "PROJECT_FORBIDDEN"}
	}
	return nil
}

// deleteProject is App.delete_project.
func (c *reqCtx) deleteProject(body *pyjson.Object) error {
	p, err := c.require("admin")
	if err != nil {
		return err
	}
	if p.ID() == project.DemoID {
		return apiErr(403, "The tutorial project cannot be deleted.", "DEMO_PROJECT_PROTECTED")
	}
	if pytext.Strip(strOrEmpty(body.Value("name"))) != p.Name() {
		return apiErr(400, "Type the current project name exactly to confirm deletion.", "PROJECT_CONFIRM_REQUIRED")
	}
	projectID, name := p.ID(), p.Name()
	for _, item := range c.app.Sessions.Brief() {
		ref, _ := item.Value("project").(*pyjson.Object)
		status := pyjson.Str(item.Value("status"))
		if ref != nil && pyjson.Str(ref.Value("id")) == projectID &&
			(status == "starting" || status == "running" || status == "idle" || status == "stopping") {
			return apiErr(409, "Stop this project's task sessions before deleting it.", "PROJECT_SESSIONS_ACTIVE")
		}
	}
	if err := c.app.Projects.Delete(projectID); err != nil {
		if pe, ok := err.(*project.Error); ok {
			return apiErr(404, pe.Message, "PROJECT_NOT_FOUND")
		}
		return err
	}
	if err := c.app.Org.UnregisterProject(c.ctx, projectID); err != nil {
		return err
	}
	actorID, err := c.app.Org.UserIDFor(c.ctx, c.actor())
	if err != nil {
		return err
	}
	if err := c.app.Org.Log(c.ctx, actorID, "project.delete", projectID, pyjson.NewObject("name", name)); err != nil {
		return err
	}
	a := c.app
	a.mu.Lock()
	if a.Auth == nil {
		a.project = nil
	}
	for sid, path := range a.sessionProjects {
		if path == p.Path {
			delete(a.sessionProjects, sid)
		}
	}
	a.mu.Unlock()
	c.tabProject, c.tabProjectSet = "", true
	c.project, c.resolved = nil, false
	next, err := c.defaultProjectFor(c.actor())
	if err != nil {
		return err
	}
	if next != nil {
		if err := c.switchProject(next); err != nil {
			return err
		}
	} else {
		c.project, c.resolved = nil, true
	}
	return c.replyBootstrap()
}
