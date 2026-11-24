package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"arkstudio/go/internal/server"
)

// watchServer is a local ark-server (no auth) with one project.
func watchServer(t *testing.T) (*httptest.Server, *server.App) {
	t.Helper()
	root := t.TempDir()
	app, err := server.NewApp(server.Config{Root: filepath.Join(root, "projects"), NoDemo: true,
		SessionsRoot: filepath.Join(root, "sessions")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	n, _ := strconv.Atoi(port)
	app.Bind("127.0.0.1", n)
	watchHTTP(t, srv, app, "POST", "/api/projects", `{"name":"Watch CLI"}`, false)
	return srv, app
}

func watchHTTP(t *testing.T, srv *httptest.Server, app *server.App, method, path, body string, agent bool) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", srv.URL)
	if agent {
		req.Header.Set("Authorization", "Bearer "+app.AgentToken())
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out) //nolint:errcheck
	if resp.StatusCode != 200 {
		t.Fatalf("%s %s: %d %v", method, path, resp.StatusCode, out)
	}
	return out
}

func TestAgentWatchCLI(t *testing.T) {
	srv, app := watchServer(t)
	state := filepath.Join(t.TempDir(), ".ark-watch-cursor")
	t.Setenv("ARK_TOKEN", app.AgentToken())
	run := func(extra ...string) (int, string, string) {
		var out, errw bytes.Buffer
		code := agentWatch(append([]string{"--url", srv.URL, "--state", state, "--once"}, extra...), &out, &errw)
		return code, out.String(), errw.String()
	}
	// First run: nothing, the cursor is saved.
	if code, out, errs := run(); code != 0 || out != "" {
		t.Fatalf("first run: %d %q %q", code, out, errs)
	}
	raw, err := os.ReadFile(state)
	if err != nil || !strings.Contains(string(raw), `"cursor": "u1.`) {
		t.Fatalf("state file: %v %s", err, raw)
	}
	unit := watchHTTP(t, srv, app, "POST", "/api/agent/v1/units", `{"type":"task","title":"Sweep","status":"review","review_note":"Check the sweep settings and the run list."}`, true)
	uid := unit["unit"].(map[string]any)["uid"].(string)
	watchHTTP(t, srv, app, "POST", "/api/units/"+uid+"/comments", `{"body":"Why lr 3e-4?"}`, false)
	code, out, _ := run()
	if code != 0 || !strings.Contains(out, "comment") || !strings.Contains(out, "Why lr 3e-4?") ||
		!strings.Contains(out, "/api/agent/v1/units/"+uid+"/comments") {
		t.Fatalf("second run: %d %q", code, out)
	}
	if code, out, _ := run(); code != 0 || out != "" {
		t.Fatalf("cursor did not advance: %q", out)
	}
	// --json prints one JSON object per line.
	watchHTTP(t, srv, app, "POST", "/api/units/"+uid+"/comments", `{"body":"Not good enough","kind":"sent_back"}`, false)
	watchHTTP(t, srv, app, "POST", "/api/agent-messages", `{"body":"Pause after this epoch"}`, false)
	code, out, _ = run("--json")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code != 0 || len(lines) != 2 {
		t.Fatalf("json run: %d %q", code, out)
	}
	var item map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &item); err != nil || item["kind"] != "sent_back" {
		t.Fatalf("json line %q: %v", lines[0], err)
	}
	// Messages print prominently.
	watchHTTP(t, srv, app, "POST", "/api/agent-messages", `{"body":"Check the GPU queue"}`, false)
	if _, out, _ := run(); !strings.Contains(out, ">>> MESSAGE") || !strings.Contains(out, "/reply") {
		t.Fatalf("message line %q", out)
	}
	// A refused token exits 3; no token at all too.
	if code, _, errs := run("--token", "nope"); code != watchExitAuth || !strings.Contains(errs, "401") {
		t.Fatalf("bad token: %d %q", code, errs)
	}
	t.Setenv("ARK_TOKEN", "")
	if code, _, _ := run(); code != watchExitAuth {
		t.Fatalf("no token: %d", code)
	}
}

func TestAgentWatchDispatch(t *testing.T) {
	// `agent watch` is taken before the Python parser snapshot; `agent
	// context` still goes to it.
	var errw bytes.Buffer
	if code := agentWatch([]string{"--bogus"}, &bytes.Buffer{}, &errw); code != 2 || !strings.Contains(errw.String(), "usage: ark-server agent watch") {
		t.Fatalf("usage: %d %q", code, errw.String())
	}
	if code := agentWatch([]string{"-h"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatalf("help: %d", code)
	}
}
