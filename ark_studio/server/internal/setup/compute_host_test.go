package setup

import (
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

// A compute resource names the setup host (device) it is; only a clean id
// is kept, never an address, and entries without it are unchanged.
func TestComputeHostID(t *testing.T) {
	item, _ := pyjson.LoadsObject(`{"id": "gpu-lab", "kind": "ssh", "gpu_server": "gpu-lab", "device_id": " 709270ec09fc "}`)
	got, errs, _ := NormalizeEntry("compute", item, 0)
	if len(errs) > 0 || got.Value("device_id") != "709270ec09fc" {
		t.Fatalf("%s %v", pyjson.Dumps(got), errs)
	}
	for _, bad := range []string{`"198.51.100.23:22"`, `"a b"`, `""`} {
		item, _ = pyjson.LoadsObject(`{"id": "x", "kind": "ssh", "gpu_server": "g", "device_id": ` + bad + `}`)
		got, _, _ = NormalizeEntry("compute", item, 0)
		if got.Has("device_id") {
			t.Errorf("%s kept: %s", bad, pyjson.Dumps(got))
		}
	}
	item, _ = pyjson.LoadsObject(`{"id": "x", "kind": "ssh", "gpu_server": "g"}`)
	if got, _, _ = NormalizeEntry("compute", item, 0); got.Has("device_id") {
		t.Error("device_id added")
	}
}
