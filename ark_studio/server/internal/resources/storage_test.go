package resources

import (
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

// Go-only (go_only.py AUGMENTED "/api/resources/graph"): a GPU server's
// device carries the probe's "storage" list; without one there is no member.
func TestGPUDeviceStorage(t *testing.T) {
	storage := []any{pyjson.NewObject("mount", "/data", "used_bytes", int64(90), "total_bytes", int64(100),
		"free_bytes", int64(10), "percent", 90.0, "data", true)}
	for _, tc := range []struct {
		name   string
		result *pyjson.Object
		want   string
	}{
		{"with storage", pyjson.NewObject("gpus", []any{}, "storage", storage),
			`[{"mount": "/data", "used_bytes": 90, "total_bytes": 100, "free_bytes": 10, "percent": 90.0, "data": true}]`},
		{"empty list", pyjson.NewObject("gpus", []any{}, "storage", []any{}), ""},
		{"no storage", pyjson.NewObject("gpus", []any{}), ""},
	} {
		g := &graph{}
		device := pyjson.NewObject("id", "gpu:a")
		gpuResources(g, device, tc.result, "a", map[string]*pyjson.Object{}, false)
		got := ""
		if device.Has("storage") {
			got = string(pyjson.Dumps(device.Value("storage")))
		}
		if got != tc.want {
			t.Errorf("%s: storage %s, want %s", tc.name, got, tc.want)
		}
	}
}
