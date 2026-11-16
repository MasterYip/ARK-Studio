package resources

// T-0018: a project's GPU server scope is read from its setup layers.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

func writeLayer(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRegisteredGPUServers(t *testing.T) {
	cases := []struct {
		name    string
		project string            // setup/project.json ("" = absent)
		users   map[string]string // owner -> user layer
		want    string
	}{
		{"no setup", "", nil, ""},
		{"1.0 compute list", `{"state": "confirmed", "profile": {"compute": [
			{"id": "local", "kind": "local"},
			{"id": "gpu-a", "kind": "ssh", "gpu_server": " a100-box "},
			{"id": "slurm", "kind": "slurm", "gpu_server": ""}]}}`, nil, "a100-box"},
		{"resources form, any state", `{"state": "draft", "profile": {"resources": [
			{"id": "g", "kind": "compute", "details": {"kind": "ssh", "gpu_server": "h20"}},
			{"id": "d", "kind": "storage", "details": {"gpu_server": "not-compute"}}]}}`, nil, "h20"},
		{"user layers count, duplicates dropped", `{"profile": {"compute": [{"id": "x", "kind": "ssh", "gpu_server": "A"}]}}`,
			map[string]string{
				"ana": `{"owner": "ana", "state": "proposed", "profile": {"compute": [{"id": "y", "kind": "ssh", "gpu_server": "a"},
					{"id": "z", "kind": "ssh", "gpu_server": "b"}]}}`,
			}, "A,b"},
		{"misnamed user file ignored", "", map[string]string{"bob": `{"owner": "mallory", "profile": {"compute": [
			{"id": "y", "kind": "ssh", "gpu_server": "stolen"}]}}`}, ""},
		{"unreadable layer registers nothing", "{not json", nil, ""},
	}
	for _, tc := range cases {
		dir := t.TempDir()
		if tc.project != "" {
			writeLayer(t, filepath.Join(dir, "setup", "project.json"), tc.project)
		}
		for owner, text := range tc.users {
			writeLayer(t, filepath.Join(dir, "setup", "users", userSlug(owner)+".json"), text)
		}
		if got := strings.Join(RegisteredGPUServers(dir), ","); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// fakeGPU is a GPUSource with fixed replies.
type fakeGPU struct{ servers *pyjson.Object }

func (f fakeGPU) Servers() (*pyjson.Object, error) { return f.servers, nil }
func (f fakeGPU) LastProbe() *pyjson.Object        { return nil }
func (f fakeGPU) QueueJobs(string) ([]any, error)  { return nil, nil }

func TestGraphNotRegisteredUnknown(t *testing.T) {
	for _, tc := range []struct {
		name    string
		servers *pyjson.Object
		want    bool
	}{
		{"registered", pyjson.NewObject("enabled", true, "servers", []any{pyjson.NewObject("name", "a", "tags", []any{})},
			"registration", pyjson.NewObject("registered", []any{"a"})), false},
		{"none registered", pyjson.NewObject("enabled", true, "servers", []any{},
			"registration", pyjson.NewObject("registered", []any{}, "hint_code", "not_registered")), true},
		{"unscoped", pyjson.NewObject("enabled", true, "servers", []any{}), false},
	} {
		src, err := Collect(t.Context(), Deps{GPU: fakeGPU{tc.servers}}, Project{ID: "p", Name: "p", Path: t.TempDir()},
			Host{Hostname: "h", Fingerprint: "f"}, 1e9)
		if err != nil {
			t.Fatal(err)
		}
		graph, err := BuildGraph(src, "")
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Contains(string(pyjson.Dumps(graph.Value("unknowns"))), "gpu_not_registered")
		if got != tc.want {
			t.Errorf("%s: gpu_not_registered %v, want %v (%s)", tc.name, got, tc.want, pyjson.Dumps(graph.Value("unknowns")))
		}
		// A member's server (no host/user) still becomes a device.
		if tc.name == "registered" {
			if !strings.Contains(string(pyjson.Dumps(graph.Value("devices"))), `"gpu:a"`) {
				t.Errorf("member server device missing: %s", pyjson.Dumps(graph.Value("devices")))
			}
		}
	}
}
