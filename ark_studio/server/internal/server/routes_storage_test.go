package server

// Project storage location (Go only, routes_storage.go / project/storage.go).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/project"
)

func localStorageApp(t *testing.T) (*App, string) {
	t.Helper()
	base := t.TempDir()
	app, err := NewApp(Config{Root: filepath.Join(base, "data", "projects"), NoDemo: true,
		SessionsRoot: filepath.Join(base, "sessions")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	app.Bind("127.0.0.1", 8765)
	return app, base
}

func storageOf(t *testing.T, r gpuReply) map[string]any {
	t.Helper()
	s, _ := r.body["storage"].(map[string]any)
	if r.code != 200 || s == nil {
		t.Fatalf("storage reply: %d %s", r.code, r.raw)
	}
	return s
}

func TestProjectStorageLocalMode(t *testing.T) {
	app, base := localStorageApp(t)
	s := storageOf(t, gpuCall(t, app, "GET", projectStoragePath, nil, nil))
	if s["default"] != app.Root || s["configured"] != "" || s["fallback"] != false || s["host_local"] != true ||
		len(s["folders"].([]any)) != 1 {
		t.Fatalf("initial: %v", s)
	}
	// Check only: nothing is created.
	target := filepath.Join(base, "My Projects")
	r := gpuCall(t, app, "POST", projectStoragePath, map[string]any{"check": target}, nil)
	check, _ := r.body["check"].(map[string]any)
	if r.code != 200 || check["will_create"] != true || check["free_bytes"] == nil {
		t.Fatalf("check: %d %s", r.code, r.raw)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("check created the folder")
	}
	if r := gpuCall(t, app, "POST", projectStoragePath, map[string]any{"check": "relative"}, nil); r.code != 400 || r.errCode() != "STORAGE_PATH_RELATIVE" {
		t.Fatalf("relative: %d %s", r.code, r.raw)
	}
	// A project made before the change stays where it is.
	r = gpuCall(t, app, "POST", "/api/projects", map[string]any{"name": "Before"}, nil)
	before, _ := r.body["summary"].(map[string]any)
	if r.code != 200 || filepath.Dir(before["path"].(string)) != app.Root {
		t.Fatalf("before: %d %s", r.code, r.raw)
	}
	s = storageOf(t, gpuCall(t, app, "POST", projectStoragePath, map[string]any{"default": target}, nil))
	resolved := project.Resolve(target)
	if s["default"] != resolved || s["configured"] != resolved {
		t.Fatalf("saved: %v", s)
	}
	r = gpuCall(t, app, "POST", "/api/projects", map[string]any{"name": "After"}, nil)
	after, _ := r.body["summary"].(map[string]any)
	if r.code != 200 || filepath.Dir(after["path"].(string)) != resolved || r.body["storage_notice"] != nil {
		t.Fatalf("after: %d %s", r.code, r.raw)
	}
	if _, err := os.Stat(before["path"].(string)); err != nil {
		t.Fatal("the earlier project folder is gone")
	}
	// Both are listed, and the option survives a restart.
	r = gpuCall(t, app, "GET", "/api/projects", nil, nil)
	if !strings.Contains(r.raw, `"Before"`) || !strings.Contains(r.raw, `"After"`) {
		t.Fatalf("list: %s", r.raw)
	}
	fresh, err := NewApp(app.Cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if dir, _ := fresh.Projects.NewProjectFolder(); dir != resolved {
		t.Errorf("after restart: %s", dir)
	}
	fresh.Close()
	// Reset: new projects go to the built-in folder, the chosen one stays listed.
	s = storageOf(t, gpuCall(t, app, "POST", projectStoragePath, map[string]any{"default": ""}, nil))
	if s["default"] != app.Root || len(s["folders"].([]any)) != 2 {
		t.Fatalf("reset: %v", s)
	}
	// Show in folder (the file manager is not opened in tests).
	var shown []string
	revealFolder = func(p string) error { shown = append(shown, p); return nil }
	t.Cleanup(func() { revealFolder = func(p string) error { return nil } })
	if r := gpuCall(t, app, "POST", projectRevealPath, map[string]any{"id": after["id"]}, nil); r.code != 200 || len(shown) != 1 || shown[0] != after["path"] {
		t.Fatalf("reveal: %d %s %v", r.code, r.raw, shown)
	}
	if r := gpuCall(t, app, "POST", projectRevealPath, map[string]any{"path": base}, nil); r.code != 403 {
		t.Fatalf("reveal unlisted: %d %s", r.code, r.raw)
	}
	// Unlist the chosen folder: its files stay.
	s = storageOf(t, gpuCall(t, app, "POST", projectStoragePath, map[string]any{"remove": target}, nil))
	if len(s["folders"].([]any)) != 1 {
		t.Fatalf("remove: %v", s)
	}
	if _, err := os.Stat(after["path"].(string)); err != nil {
		t.Fatal("unlisting deleted the project")
	}
	if r := gpuCall(t, app, "POST", projectStoragePath, map[string]any{"remove": app.Root}, nil); r.errCode() != "STORAGE_FOLDER_BUILTIN" {
		t.Fatalf("remove root: %d %s", r.code, r.raw)
	}
	// "Open another project…" by path lists the folder again (local mode).
	r = gpuCall(t, app, "POST", "/api/projects/open", map[string]any{"path": after["path"]}, nil)
	if r.code != 200 || !app.Projects.InFolders(resolved) {
		t.Fatalf("open by path: %d %s", r.code, r.raw)
	}
	// A folder that is not a project still cannot be opened.
	if r := gpuCall(t, app, "POST", "/api/projects/open", map[string]any{"path": base}, nil); r.code != 403 {
		t.Fatalf("open non-project: %d %s", r.code, r.raw)
	}
}

func TestProjectStorageFallbackNotice(t *testing.T) {
	app, base := localStorageApp(t)
	usb := filepath.Join(base, "usb")
	storageOf(t, gpuCall(t, app, "POST", projectStoragePath, map[string]any{"default": usb}, nil))
	if err := os.Remove(usb); err != nil {
		t.Fatal(err)
	}
	s := storageOf(t, gpuCall(t, app, "GET", projectStoragePath, nil, nil))
	if s["fallback"] != true || s["default"] != app.Root {
		t.Fatalf("storage: %v", s)
	}
	r := gpuCall(t, app, "POST", "/api/projects", map[string]any{"name": "Fallback"}, nil)
	notice, _ := r.body["storage_notice"].(map[string]any)
	summary, _ := r.body["summary"].(map[string]any)
	if r.code != 200 || notice == nil || notice["code"] != "STORAGE_FALLBACK" || filepath.Dir(summary["path"].(string)) != app.Root {
		t.Fatalf("create: %d %s", r.code, r.raw)
	}
	if _, err := os.Stat(usb); !os.IsNotExist(err) {
		t.Fatal("the missing folder was recreated")
	}
}

func TestProjectStoragePermissions(t *testing.T) {
	app, _ := authDesktopApp(t)
	base := t.TempDir()
	target := filepath.Join(base, "team-projects")
	for _, user := range []string{"ana", "vic"} {
		if r := gpuCall(t, app, "GET", projectStoragePath, nil, sessionCookie(app, user)); r.code != 403 {
			t.Errorf("%s reads: %d %s", user, r.code, r.raw)
		}
		if r := gpuCall(t, app, "POST", projectStoragePath, map[string]any{"default": target}, sessionCookie(app, user)); r.code != 403 {
			t.Errorf("%s changes: %d %s", user, r.code, r.raw)
		}
	}
	if r := gpuCall(t, app, "GET", projectStoragePath, nil, nil); r.code != 401 {
		t.Errorf("anonymous: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "GET", projectStoragePath, nil, map[string]string{"X-ARK-Token": app.Projects.AgentToken}); r.code != 401 && r.code != 403 {
		t.Errorf("agent token: %d %s", r.code, r.raw)
	}
	s := storageOf(t, gpuCall(t, app, "POST", projectStoragePath, map[string]any{"default": target}, sessionCookie(app, "owner")))
	if s["host_local"] != false || s["default"] != project.Resolve(target) {
		t.Fatalf("owner: %v", s)
	}
	// Members create projects in the chosen folder.
	r := gpuCall(t, app, "POST", "/api/projects", map[string]any{"name": "Ana's"}, sessionCookie(app, "ana"))
	summary, _ := r.body["summary"].(map[string]any)
	if r.code != 200 || filepath.Dir(summary["path"].(string)) != project.Resolve(target) {
		t.Fatalf("member create: %d %s", r.code, r.raw)
	}
	// Show in folder is for local mode only.
	if r := gpuCall(t, app, "POST", projectRevealPath, map[string]any{"id": summary["id"]}, sessionCookie(app, "owner")); r.code != 403 || r.errCode() != "REVEAL_LOCAL_ONLY" {
		t.Errorf("reveal on a server: %d %s", r.code, r.raw)
	}
	// On a server, open by path stays inside the listed folders.
	other := filepath.Join(base, "other")
	os.MkdirAll(other, 0o755)
	if r := gpuCall(t, app, "POST", "/api/projects/open", map[string]any{"path": other}, sessionCookie(app, "owner")); r.code != 403 {
		t.Errorf("open outside: %d %s", r.code, r.raw)
	}
	// The change is logged.
	rows, err := app.Org.ListAudit(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pyjson.Dumps(rows)), projectStorageAction) {
		t.Error("no audit row")
	}
}
