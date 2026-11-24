package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func standbyApp(t *testing.T, status string) *App {
	t.Helper()
	root := t.TempDir()
	statusPath := ""
	if status != "" {
		statusPath = filepath.Join(root, "standby-status.json")
		if err := os.WriteFile(statusPath, []byte(status), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	app, err := NewApp(Config{Root: filepath.Join(root, "projects"), NoDemo: true,
		SessionsRoot: filepath.Join(root, "sessions"), ReadOnly: true, StandbyStatus: statusPath}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	app.Bind("127.0.0.1", 8765)
	return app
}

func TestStandbyRefusesWrites(t *testing.T) {
	app := standbyApp(t, `{"last_sync": "2026-11-24T08:00:00Z", "primary": "gpu-lab", "databases": 12, "checksums": {"x": "y"}}`)
	before, _ := os.ReadDir(app.Root)
	for _, method := range []string{"POST", "PATCH", "DELETE"} {
		r := gpuCall(t, app, method, "/api/projects", map[string]any{"name": "Nope"}, nil)
		if r.code != 503 || r.errCode() != ReadOnlyCode {
			t.Fatalf("%s: want 503 %s, got %d %s", method, ReadOnlyCode, r.code, r.raw)
		}
	}
	r := gpuCall(t, app, "POST", "/api/agent/v1/units", map[string]any{"title": "x"}, nil)
	if r.code != 503 {
		t.Fatalf("agent write: want 503, got %d %s", r.code, r.raw)
	}
	if after, _ := os.ReadDir(app.Root); len(after) != len(before) {
		t.Fatalf("a refused write changed the project root: %d -> %d entries", len(before), len(after))
	}
	// Signing out is not a data change.
	if r := gpuCall(t, app, "POST", "/api/auth/logout", map[string]any{}, nil); r.code == 503 {
		t.Fatalf("logout refused on a standby: %s", r.raw)
	}
	h := gpuCall(t, app, "GET", "/api/health", nil, nil)
	if h.code != 200 || h.body["read_only"] != true {
		t.Fatalf("health: %d %s", h.code, h.raw)
	}
	st, _ := h.body["standby"].(map[string]any)
	if st["last_sync"] != "2026-11-24T08:00:00Z" || st["primary"] != "gpu-lab" || st["databases"] != float64(12) {
		t.Fatalf("standby status: %v", h.body["standby"])
	}
	if _, leaked := st["checksums"]; leaked {
		t.Fatal("health must only show the public part of the status file")
	}
}

func TestStandbyMeta(t *testing.T) {
	got := withStandbyMeta("<html><head><title>x</title></head><body></body></html>", "")
	if !strings.Contains(got, `<meta name="ark-standby" content="read-only" data-last-sync="">`+"\n</head>") {
		t.Fatalf("marker not before </head>: %s", got)
	}
	app := standbyApp(t, `{"last_sync": "2026-11-24T08:05:00Z"}`)
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "127.0.0.1:8765"
	r.RemoteAddr = "127.0.0.1:50000"
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `data-last-sync="2026-11-24T08:05:00Z"`) {
		t.Fatalf("index: %d, standby marker missing", w.Code)
	}
}

func TestPrimaryHasNoStandbyFields(t *testing.T) {
	app := homeApp(t)
	h := gpuCall(t, app, "GET", "/api/health", nil, nil)
	if _, ok := h.body["read_only"]; ok {
		t.Fatalf("primary health reports read_only: %s", h.raw)
	}
}
