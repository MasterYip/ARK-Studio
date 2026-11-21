package server

// Project storage location (Go only; project/storage.go).
//
//   GET  /api/org/project-storage   where new projects go, the listed folders
//   POST /api/org/project-storage   {check: path}   validate only (free space)
//                                   {default: path} use path for new projects
//                                                   ("" = the server's own folder)
//                                   {remove: path}  stop listing a folder
//                                                   (its files stay)
//   POST /api/projects/reveal       {id} or {path}: show a project or a listed
//                                   folder in the file manager (local mode)
//
// Local mode (no accounts: the desktop app, `ark-server serve`) may change it;
// on a server with sign-in only organization admins and owners. Agent,
// task and device tokens never can. Existing projects are never moved: the
// folder chosen before stays listed.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/hostopen"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/platform/diskfree"
	"arkstudio/go/internal/platform/pathx"
	"arkstudio/go/internal/project"
)

const (
	projectStoragePath   = "/api/org/project-storage"
	projectRevealPath    = "/api/projects/reveal"
	projectStorageOption = "project_storage"
	projectStorageAction = "org.project_storage"
)

// storageRoutesGoOnly are the Go-only ids of this file (GoOnlyRoutes).
var storageRoutesGoOnly = []PortedRoute{
	{"goonly:exact:" + projectStoragePath, "GET,HEAD,POST"},
	{"goonly:exact:" + projectRevealPath, "POST"},
}

// revealFolder opens a folder in the file manager (tests replace it).
var revealFolder = func(path string) error {
	_, err := hostopen.OpenExternal(path)
	return err
}

// loadProjectStorage applies the stored option at start. A broken value is
// ignored (new projects go to the server's own folder).
func (a *App) loadProjectStorage(ctx context.Context) {
	raw, ok, err := a.Org.Option(ctx, projectStorageOption)
	if err != nil || !ok {
		return
	}
	var cfg project.StorageConfig
	if json.Unmarshal([]byte(raw), &cfg) == nil {
		a.Projects.SetStorage(cfg)
	}
}

func (a *App) saveProjectStorage(ctx context.Context, actor string, cfg project.StorageConfig, detail *pyjson.Object) error {
	a.Projects.SetStorage(cfg)
	applied := a.Projects.Storage()
	if applied.Folders == nil {
		applied.Folders = []string{}
	}
	raw, err := json.Marshal(applied)
	if err != nil {
		return err
	}
	return a.Org.SetOption(ctx, actor, projectStorageOption, string(raw), projectStorageAction, detail)
}

// storageGuard is who may see and change the project storage location.
func (c *reqCtx) storageGuard() (string, error) {
	if c.token != nil || c.agentToken() != "" || c.header("X-ARK-Read-Token") != "" {
		return "", apiErr(403, "Agent and task tokens cannot change where projects are stored.", "AGENT_TOKEN_FORBIDDEN")
	}
	if c.mobileSession != nil || c.mobileToken() != "" {
		return "", apiErr(403, "A paired device cannot change where projects are stored.", "MOBILE_STORAGE_BLOCKED")
	}
	if c.app.Auth == nil {
		return org.LocalUsername, nil
	}
	if c.session == nil || c.actor() == "" {
		return "", apiErr(401, "Sign in with the ARK account to continue.", "AUTH_REQUIRED")
	}
	if err := c.app.Org.RequireOrgAdmin(c.ctx, c.actor()); err != nil {
		return "", orgErr(err)
	}
	return c.actor(), nil
}

func freeBytes(path string) any {
	for cur := path; ; {
		if n, ok := diskfree.Available(cur); ok {
			return n
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return nil
		}
		cur = parent
	}
}

func (c *reqCtx) storagePayload() *pyjson.Object {
	m := c.app.Projects
	cfg := m.Storage()
	dir, fallback := m.NewProjectFolder()
	folders := []any{}
	for _, f := range m.FolderInfos() {
		folders = append(folders, pyjson.NewObject("path", f.Path, "builtin", f.Builtin, "default", f.Default,
			"exists", f.Exists, "projects", int64(f.Projects)))
	}
	out := pyjson.NewObject("default", dir, "configured", cfg.Default, "builtin", m.Root,
		"fallback", fallback, "free_bytes", freeBytes(dir), "folders", folders,
		"can_change", true, "host_local", c.app.Auth == nil, "platform", runtime.GOOS)
	return out
}

func storageErr(err error) error {
	if ce, ok := err.(*project.CheckError); ok {
		return apiErr(400, ce.Message, ce.Code)
	}
	return err
}

// projectStorageGet is GET /api/org/project-storage.
func (c *reqCtx) projectStorageGet() error {
	if _, err := c.storageGuard(); err != nil {
		return err
	}
	c.ok(pyjson.NewObject("storage", c.storagePayload()))
	return nil
}

// projectStoragePost is POST /api/org/project-storage.
func (c *reqCtx) projectStoragePost() error {
	actor, err := c.storageGuard()
	if err != nil {
		return err
	}
	m := c.app.Projects
	field := func(name string) (string, bool) {
		v, ok := c.body.Get(name)
		if !ok {
			return "", false
		}
		s, _ := v.(string)
		return pytext.Strip(s), true
	}
	if path, ok := field("check"); ok {
		res, err := m.CheckFolder(path)
		if err != nil {
			return storageErr(err)
		}
		c.ok(pyjson.NewObject("check", pyjson.NewObject("ok", true, "path", res.Path, "exists", res.Exists,
			"will_create", res.WillCreate, "free_bytes", nullIfNegative(res.Free),
			"current", pathx.SamePath(res.Path, mustNewFolder(m)))))
		return nil
	}
	cfg := m.Storage()
	if path, ok := field("remove"); ok {
		target := project.Resolve(path)
		if path == "" || pathx.SamePath(target, m.Root) {
			return apiErr(400, "The server's own project folder is always listed.", "STORAGE_FOLDER_BUILTIN")
		}
		if cfg.Default != "" && pathx.SamePath(target, cfg.Default) {
			return apiErr(400, "New projects are created in this folder. Choose another folder first.", "STORAGE_FOLDER_DEFAULT")
		}
		kept := []string{}
		found := false
		for _, f := range cfg.Folders {
			if pathx.SamePath(f, target) {
				found = true
				continue
			}
			kept = append(kept, f)
		}
		if !found {
			return apiErr(404, "This folder is not listed.", "STORAGE_FOLDER_UNKNOWN")
		}
		cfg.Folders = kept
		if err := c.app.saveProjectStorage(c.ctx, actor, cfg, pyjson.NewObject("removed", target)); err != nil {
			return err
		}
		c.ok(pyjson.NewObject("storage", c.storagePayload()))
		return nil
	}
	path, ok := field("default")
	if !ok {
		return apiErr(400, "Send check, default or remove.", "STORAGE_REQUEST_INVALID")
	}
	previous := cfg.Default
	if path == "" || pathx.SamePath(project.Resolve(path), m.Root) {
		cfg.Default = ""
	} else {
		res, err := m.CheckFolder(path)
		if err != nil {
			return storageErr(err)
		}
		if err := project.PrepareFolder(res); err != nil {
			return storageErr(err)
		}
		cfg.Default = res.Path
	}
	// The folder used so far stays listed: its projects stay where they are.
	if previous != "" {
		cfg.Folders = append(cfg.Folders, previous)
	}
	shown := cfg.Default
	if shown == "" {
		shown = m.Root
	}
	if err := c.app.saveProjectStorage(c.ctx, actor, cfg, pyjson.NewObject("default", shown)); err != nil {
		return err
	}
	c.ok(pyjson.NewObject("storage", c.storagePayload()))
	return nil
}

func mustNewFolder(m *project.Manager) string {
	dir, _ := m.NewProjectFolder()
	return dir
}

func nullIfNegative(n int64) any {
	if n < 0 {
		return nil
	}
	return n
}

// projectReveal is POST /api/projects/reveal: the file manager on the
// computer running ARK, so only without accounts (desktop, local server).
func (c *reqCtx) projectReveal() error {
	if _, err := c.storageGuard(); err != nil {
		return err
	}
	if c.app.Auth != nil {
		return apiErr(403, "Folders can be shown only when ARK runs on this computer.", "REVEAL_LOCAL_ONLY")
	}
	target := ""
	if id := pytext.Strip(strOrEmpty(c.body.Value("id"))); id != "" {
		for _, s := range c.app.Projects.ListProjects() {
			if pyjson.Str(s.Value("id")) == id {
				target = pyjson.Str(s.Value("path"))
				break
			}
		}
		if target == "" {
			return apiErr(404, "Project not found.", "PROJECT_NOT_FOUND")
		}
	} else if path := pytext.Strip(strOrEmpty(c.body.Value("path"))); path != "" {
		if !c.app.Projects.InFolders(path) {
			return apiErr(403, "Only listed project folders can be shown.", "REVEAL_DENIED")
		}
		target = project.Resolve(path)
	} else {
		return apiErr(400, "Project id or folder is required.", "")
	}
	if fi, err := os.Stat(target); err != nil || !fi.IsDir() {
		return apiErr(404, "The folder does not exist (any more).", "REVEAL_MISSING")
	}
	if err := revealFolder(target); err != nil {
		return err
	}
	c.ok(pyjson.NewObject("ok", true, "path", target))
	return nil
}

// adoptProjectFolder lists the folder holding an ARK project opened by path
// outside the listed folders (local mode: "Open another project…").
func (c *reqCtx) adoptProjectFolder(target string) bool {
	if c.app.Auth != nil || strings.HasPrefix(filepath.Base(target), ".") {
		return false
	}
	if _, ok := project.ReadManifest(target); !ok {
		return false
	}
	parent := filepath.Dir(target)
	if _, err := c.app.Projects.CheckFolder(parent); err != nil {
		return false
	}
	cfg := c.app.Projects.Storage()
	cfg.Folders = append(cfg.Folders, parent)
	if len(cfg.Folders) > project.MaxStorageFolders {
		return false
	}
	if err := c.app.saveProjectStorage(c.ctx, org.LocalUsername, cfg, pyjson.NewObject("listed", parent)); err != nil {
		return false
	}
	return c.app.Projects.InFolders(parent)
}
