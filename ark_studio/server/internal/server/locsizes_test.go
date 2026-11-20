package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/platform/dirsize"
	"arkstudio/go/internal/setup"
)

func sizesProject(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	code := filepath.Join(dir, "code")
	if err := os.MkdirAll(filepath.Join(code, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, n := range map[string]int{"a.txt": 10, ".git/HEAD": 4} {
		if err := os.WriteFile(filepath.Join(code, name), make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := &setup.Env{Project: dir, DefaultOwner: "ana"}
	if err := os.MkdirAll(filepath.Join(dir, setup.ProfileDir), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := setup.NewViewer(setup.Viewer{User: "ana", Admin: true, CanWrite: true, HostID: "srv-1", HostSource: "header"})
	raw := pyjson.NewObject("resources", []any{
		pyjson.NewObject("id", "code", "kind", "repository", "name", "code", "path_or_ref", code),
	})
	if _, err := env.SaveLayer(raw, srv, "project", "agent:ana", false, ""); err != nil {
		t.Fatal(err)
	}
	other := setup.NewViewer(setup.Viewer{User: "ana", CanWrite: true, HostID: "laptop", HostSource: "header"})
	if _, err := env.AddLocation(other, "project/code", "/home/ana/code", "agent:ana", "", "", ""); err != nil {
		t.Fatal(err)
	}
	return dir, code
}

func testSizes(measure func(context.Context, string, dirsize.Options) dirsize.Result) *sizeService {
	s := newSizeService()
	s.off, s.gap, s.host = false, 0, func() string { return "srv-1" }
	if measure != nil {
		s.measure = measure
	}
	return s
}

func waitIdle(t *testing.T, s *sizeService) {
	t.Helper()
	for i := 0; i < 500; i++ {
		s.mu.Lock()
		idle := !s.running && len(s.queue) == 0
		s.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("size worker did not finish")
}

// Only this host's locations are measured; the laptop's path is left to
// the laptop.
func TestSizeScanMeasuresOwnHostOnly(t *testing.T) {
	dir, code := sizesProject(t)
	var mu sync.Mutex
	var paths []string
	s := testSizes(func(ctx context.Context, p string, o dirsize.Options) dirsize.Result {
		mu.Lock()
		paths = append(paths, p)
		mu.Unlock()
		return dirsize.Measure(ctx, p, o)
	})
	s.scanNow(dir, time.Now())
	waitIdle(t, s)
	if len(paths) != 1 || paths[0] != code {
		t.Fatalf("measured %v", paths)
	}
	stats := setup.ReadLocationStats(dir)
	if len(stats) != 1 {
		t.Fatalf("stats: %d", len(stats))
	}
	st := stats[0]
	if st.Value("bytes") != int64(14) || st.Value("git_bytes") != int64(4) || st.Value("source") != "server" ||
		st.Value("host_id") != "srv-1" {
		t.Fatalf("stat: %s", pyjson.Dumps(st))
	}
	// Fresh sizes are not measured again; the per-project scan is throttled.
	s.scanNow(dir, time.Now())
	waitIdle(t, s)
	if len(paths) != 1 {
		t.Fatalf("fresh size measured again: %v", paths)
	}
	s.scan(dir)
	s.scan(dir)
	s.mu.Lock()
	_, scanned := s.lastScan[dir]
	s.mu.Unlock()
	if !scanned {
		t.Fatal("scan not recorded")
	}
}

func TestSizeRefreshRateLimited(t *testing.T) {
	dir, code := sizesProject(t)
	s := testSizes(nil)
	job := sizeJob{dir, "project/code", "srv-1", code}
	if ok, wait := s.refresh(job); !ok || wait != 0 {
		t.Fatalf("first refresh: %v %v", ok, wait)
	}
	if ok, wait := s.refresh(job); ok || wait <= 0 {
		t.Fatalf("second refresh within a minute: %v %v", ok, wait)
	}
	waitIdle(t, s)
	off := testSizes(nil)
	off.off = true
	if ok, _ := off.refresh(job); ok {
		t.Fatal("refresh while off")
	}
}

func TestStatFromCappedResult(t *testing.T) {
	r := dirsize.Result{Kind: dirsize.KindDir, Bytes: 5, Files: 2, Capped: dirsize.CappedTime, Skipped: 3}
	st := statFromResult(sizeJob{"p", "project/x", "h", "/x"}, r, time.Date(2026, 11, 20, 1, 2, 3, 0, time.UTC))
	if st.Value("capped") != "time" || st.Value("skipped") != int64(3) || st.Value("measured_at") != "2026-11-20T01:02:03+00:00" {
		t.Fatalf("%s", pyjson.Dumps(st))
	}
}

// Agents and the desktop app report sizes of their own device only; the
// graph and setup state show them; "measure" refuses another device's path.
func TestLocationStatsRoutes(t *testing.T) {
	app, _ := localGPUApp(t)
	agent := map[string]string{"Authorization": "Bearer " + app.AgentToken(), "X-ARK-Host": "dev-9"}
	r := gpuCall(t, app, "POST", "/api/agent/v1/setup/resource", map[string]any{"layer": "project",
		"resource": map[string]any{"id": "data", "kind": "storage", "name": "data", "details": map[string]any{"kind": "datasets"}}}, agent)
	if r.code != 200 {
		t.Fatalf("resource: %d %s", r.code, r.raw)
	}
	r = gpuCall(t, app, "POST", "/api/agent/v1/setup/location", map[string]any{"resource": "project/data",
		"path_or_ref": "/mnt/data", "host_label": "dev box"}, agent)
	if r.code != 200 {
		t.Fatalf("location: %d %s", r.code, r.raw)
	}
	r = gpuCall(t, app, "POST", "/api/agent/v1/setup/location-stats", map[string]any{"items": []any{
		map[string]any{"resource": "project/data", "bytes": 2469606195, "files": 12340}}}, agent)
	if r.code != 200 {
		t.Fatalf("stats: %d %s", r.code, r.raw)
	}
	other := map[string]string{"Authorization": "Bearer " + app.AgentToken(), "X-ARK-Host": "dev-7"}
	r = gpuCall(t, app, "POST", "/api/agent/v1/setup/location-stats", map[string]any{"resource": "project/data", "bytes": 1}, other)
	if r.code != 404 || r.errCode() != "SETUP_NO_LOCATION" {
		t.Fatalf("other device: %d %s", r.code, r.raw)
	}
	r = gpuCall(t, app, "GET", "/api/resources/graph", nil, nil)
	if r.code != 200 || !strings.Contains(r.raw, `"bytes": 2469606195`) {
		t.Fatalf("graph without size: %d %s", r.code, r.raw)
	}
	r = gpuCall(t, app, "GET", "/api/skills/setup/state", nil, nil)
	if !strings.Contains(r.raw, `"files": 12340`) {
		t.Fatalf("state without size: %s", r.raw)
	}
	r = gpuCall(t, app, "POST", "/api/skills/setup/measure", map[string]any{"resource": "project/data"}, nil)
	if r.code != 409 || r.errCode() != "SETUP_SIZE_ON_DEVICE" {
		t.Fatalf("measure other device: %d %s", r.code, r.raw)
	}
}
