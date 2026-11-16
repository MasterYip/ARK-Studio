package server

// T-0018: GPU servers are scoped to the projects that register them; the
// connection details are for the server admin/owner or local mode.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"arkstudio/go/internal/auth"
	"arkstudio/go/internal/compat/pyjson"
)

const scopeServers = `enabled: true
defaults:
  user: alice
servers:
  - name: a
    host: a.example.internal
    tags: [a100]
  - name: b
    host: b.example.internal
`

type gpuReply struct {
	code int
	body map[string]any
	raw  string
}

func gpuCall(t *testing.T, app *App, method, target string, body any, headers map[string]string) gpuReply {
	t.Helper()
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, target, bytes.NewReader(data))
	r.Host = "127.0.0.1:8765"
	r.RemoteAddr = "127.0.0.1:50000"
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Content-Length", strconv.Itoa(len(data)))
		r.Header.Set("Origin", "http://127.0.0.1:8765")
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	out := gpuReply{code: w.Code, raw: w.Body.String()}
	_ = json.Unmarshal(w.Body.Bytes(), &out.body)
	return out
}

func (r gpuReply) errCode() string {
	e, _ := r.body["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

func serverNames(v any) string {
	var out []string
	list, _ := v.([]any)
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m["name"].(string))
		}
	}
	return strings.Join(out, ",")
}

// localGPUApp is a local-mode app with one open project and the server list above.
func localGPUApp(t *testing.T) (*App, string) {
	t.Helper()
	root := t.TempDir()
	cfg := filepath.Join(root, "gpu_servers.yaml")
	if err := os.WriteFile(cfg, []byte(scopeServers), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARK_GPU_SERVERS_CONFIG", cfg)
	t.Setenv("ARK_GPU_QUEUE_DB", filepath.Join(root, "queue.sqlite3"))
	t.Setenv("ARK_GPU_PROBE", "")
	app, err := NewApp(Config{Root: filepath.Join(root, "projects"), NoDemo: true,
		SessionsRoot: filepath.Join(root, "sessions")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	t.Cleanup(func() { app.gpu().Close() })
	app.Bind("127.0.0.1", 8765)
	if r := gpuCall(t, app, "POST", "/api/projects", map[string]any{"name": "GPU scope"}, nil); r.code != 200 {
		t.Fatalf("create project: %d %s", r.code, r.raw)
	}
	p, err := (&reqCtx{app: app, ctx: context.Background()}).currentProject()
	if err != nil || p == nil {
		t.Fatalf("no current project: %v", err)
	}
	return app, p.Path
}

func registerGPU(t *testing.T, projectPath string, names ...string) {
	t.Helper()
	var compute []any
	for _, n := range names {
		compute = append(compute, map[string]any{"id": "gpu-" + n, "kind": "ssh", "gpu_server": n})
	}
	data, _ := json.Marshal(map[string]any{"state": "draft", "profile": map[string]any{"compute": compute}})
	path := filepath.Join(projectPath, "setup", "project.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Local mode follows the same project rule (nothing registered: no
// servers, a hint, refusals); the local user is the admin.
func TestGPULocalModeScope(t *testing.T) {
	app, projectPath := localGPUApp(t)

	r := gpuCall(t, app, "GET", "/api/gpu/servers", nil, nil)
	reg, _ := r.body["registration"].(map[string]any)
	if r.code != 200 || serverNames(r.body["servers"]) != "" || reg["hint_code"] != "not_registered" ||
		len(reg["available"].([]any)) != 2 || r.body["config_path"] == nil {
		t.Fatalf("unregistered servers: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "GET", "/api/gpu/status", nil, nil); r.code != 200 || r.body["server_count"] != 0.0 {
		t.Errorf("unregistered status: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "GET", "/api/gpu/dashboard", nil, nil); r.code != 200 ||
		r.body["registration"].(map[string]any)["hint_code"] != "not_registered" {
		t.Errorf("unregistered dashboard: %d %s", r.code, r.raw)
	}
	if g := gpuCall(t, app, "GET", "/api/resources/graph", nil, nil); g.code != 200 ||
		strings.Contains(g.raw, `"gpu:a"`) || !strings.Contains(g.raw, "gpu_not_registered") {
		t.Errorf("unregistered graph: %d %s", g.code, g.raw)
	}
	refusals := []struct {
		path string
		body map[string]any
		code int
		err  string
	}{
		{"/api/gpu/probe", map[string]any{"server": "a"}, 404, "GPU_SERVER_NOT_REGISTERED"},
		{"/api/gpu/dispatch/prepare", map[string]any{"command": "x"}, 403, "GPU_NO_REGISTERED_SERVERS"},
		{"/api/gpu/dispatch/prepare", map[string]any{"command": "x", "server": "b"}, 404, "GPU_SERVER_NOT_REGISTERED"},
		{"/api/gpu/jobs", map[string]any{"command": "x", "server": "a"}, 404, "GPU_SERVER_NOT_REGISTERED"},
	}
	for _, tc := range refusals {
		if r := gpuCall(t, app, "POST", tc.path, tc.body, nil); r.code != tc.code || r.errCode() != tc.err {
			t.Errorf("%s %v: %d %s, want %d %s", tc.path, tc.body, r.code, r.raw, tc.code, tc.err)
		}
	}

	// Setup discovery still offers every configured name, and nothing else.
	agent := map[string]string{"Authorization": "Bearer " + app.AgentToken()}
	d := gpuCall(t, app, "POST", "/api/agent/v1/setup/discover", map[string]any{}, agent)
	if d.code != 200 || !strings.Contains(d.raw, `"gpu_server": "a"`) || !strings.Contains(d.raw, `"gpu_server": "b"`) ||
		strings.Contains(d.raw, "example.internal") || strings.Contains(d.raw, "a100") {
		t.Errorf("discover: %d %s", d.code, d.raw)
	}

	// Register "a": the project sees it, with the address (local = admin).
	registerGPU(t, projectPath, "a")
	r = gpuCall(t, app, "GET", "/api/gpu/servers", nil, nil)
	servers, _ := r.body["servers"].([]any)
	if r.code != 200 || serverNames(r.body["servers"]) != "a" || servers[0].(map[string]any)["host"] != "a.example.internal" {
		t.Fatalf("registered servers: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "POST", "/api/gpu/probe", map[string]any{"server": "b"}, nil); r.code != 404 {
		t.Errorf("probe unregistered b: %d %s", r.code, r.raw)
	}
	p := gpuCall(t, app, "POST", "/api/gpu/dispatch/prepare", map[string]any{"command": "x"}, nil)
	if p.code != 200 || !strings.Contains(pyjson.Str(p.body["ssh_preview"]), "alice@a.example.internal") {
		t.Errorf("local dispatch: %d %s", p.code, p.raw)
	}
	if g := gpuCall(t, app, "GET", "/api/resources/graph", nil, nil); !strings.Contains(g.raw, `"gpu:a"`) ||
		strings.Contains(g.raw, `"gpu:b"`) || strings.Contains(g.raw, "gpu_not_registered") {
		t.Errorf("registered graph: %s", g.raw)
	}

	// The web's one-click registration (api.gpuRegister): compute resources in
	// the viewer's own setup layer register "b" too.
	d = gpuCall(t, app, "POST", "/api/skills/setup/draft", map[string]any{"layer": "user", "resources": []any{
		map[string]any{"id": "gpu-b", "kind": "compute", "name": "b",
			"details": map[string]any{"kind": "ssh", "gpu_server": "b", "use": "ask"}}}}, nil)
	if d.code != 200 {
		t.Fatalf("one-click register: %d %s", d.code, d.raw)
	}
	if r := gpuCall(t, app, "GET", "/api/gpu/servers", nil, nil); serverNames(r.body["servers"]) != "a,b" {
		t.Errorf("after one-click register: %s", r.raw)
	}
}

// Org owners/admins see connection details; members and viewers do not;
// local mode (no auth) is the admin.
func TestGPUAdminView(t *testing.T) {
	app, _ := localGPUApp(t)
	ctx := context.Background()
	c := &reqCtx{app: app, ctx: ctx}
	if admin, err := c.gpuAdmin(); err != nil || !admin {
		t.Fatalf("local mode: %v %v", admin, err)
	}
	if err := app.Org.Bootstrap(ctx, "owner", "x", false, nil); err != nil {
		t.Fatal(err)
	}
	for name, role := range map[string]string{"boss": "admin", "ana": "member", "vic": "viewer"} {
		if _, err := app.Org.CreateUser(ctx, "owner", pyjson.NewObject("username", name,
			"password", "correct horse battery", "role", role)); err != nil {
			t.Fatal(err)
		}
	}
	app.Auth = &auth.Manager{} // auth mode (only its presence is consulted here)
	defer func() { app.Auth = nil }()
	for user, want := range map[string]bool{"owner": true, "boss": true, "ana": false, "vic": false, "": false,
		"stranger": false} {
		c := &reqCtx{app: app, ctx: ctx, user: user}
		if got, err := c.gpuAdmin(); err != nil || got != want {
			t.Errorf("%q: admin %v (%v), want %v", user, got, err, want)
		}
	}
}
