package setup

import (
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

func blockedReason(p *obj, capability string) string {
	for _, x := range listOr(mapOf(p.Value("readiness")).Value("blocked")) {
		if b := mapOf(x); str(b.Value("capability")) == capability {
			return str(b.Value("reason"))
		}
	}
	return ""
}

func deviceByID(p *obj, id string) *obj {
	for _, x := range listOr(p.Value("devices")) {
		if d := mapOf(x); str(d.Value("id")) == id {
			return d
		}
	}
	return nil
}

func mustPayload(t *testing.T, e *Env, v *Viewer) *obj {
	t.Helper()
	p, err := e.Payload(v)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Setup is repeatable per person and per device: a second person, and the
// same person on a second device, add their own paths without touching
// anyone's confirmation, and the agent sees which paths this device lacks.
func TestPerUserPerDeviceLocations(t *testing.T) {
	e := testEnv(t)
	ana := NewViewer(Viewer{User: "ana", Admin: true, CanWrite: true, HostID: "pc-1", HostSource: "header"})
	raw := pyjson.NewObject("resources", []any{
		pyjson.NewObject("id", "code", "kind", "repository", "name", "code",
			"details", pyjson.NewObject("role", "primary", "access", "read-write"), "path_or_ref", "/home/ana/code"),
		pyjson.NewObject("id", "tasks", "kind", "task_space", "name", "tasks",
			"details", pyjson.NewObject("access", "read-write"), "path_or_ref", "/home/ana/tasks"),
	})
	mustSave(t, e, ana, "project", raw, true)
	_, digest := layerState(t, e, "project", "")
	if _, err := e.ConfirmLayers(ana, []LayerDigest{{"project", digest}}, "human:ana", ""); err != nil {
		t.Fatal(err)
	}
	if r := blockedReason(mustPayload(t, e, ana), "files"); r != "" {
		t.Fatalf("ana's device has its paths: %s", r)
	}

	bob := NewViewer(Viewer{User: "bob", CanWrite: true, HostID: "laptop-b", HostSource: "header"})
	p := mustPayload(t, e, bob)
	if r := blockedReason(p, "files"); !strings.Contains(r, "laptop-b") || !strings.Contains(r, "code") || !strings.Contains(r, "tasks") {
		t.Fatalf("files not blocked for bob's device: %q", r)
	}
	if d := deviceByID(p, "laptop-b"); d == nil || d.Value("current") != true || len(listOr(d.Value("missing"))) != 2 {
		t.Fatalf("devices: %s", pyjson.Dumps(p.Value("devices")))
	}
	for _, key := range []string{"project/code", "project/tasks"} {
		if _, err := e.AddLocation(bob, key, "/Users/bob/"+key, "agent:bob", "", "bob-laptop", ""); err != nil {
			t.Fatal(err)
		}
	}
	if s, _ := layerState(t, e, "user", "bob"); s == "draft" || s == "proposed" {
		t.Fatalf("a location must not put bob's layer into %s", s)
	}
	if s, _ := layerState(t, e, "project", ""); s != "confirmed" {
		t.Fatalf("project confirmation lost: %s", s)
	}
	p = mustPayload(t, e, bob)
	if r := blockedReason(p, "files"); r != "" {
		t.Fatalf("files still blocked: %s", r)
	}
	if r := blockedReason(p, "tasks"); r != "" {
		t.Fatalf("tasks blocked after adding paths: %s", r)
	}
	if d := deviceByID(p, "laptop-b"); str(d.Value("label")) != "bob-laptop" || len(listOr(d.Value("missing"))) != 0 {
		t.Fatalf("device row: %s", pyjson.Dumps(d))
	}

	// Bob's second device: nothing registered there yet.
	desk := NewViewer(Viewer{User: "bob", CanWrite: true, HostID: "desk-b", HostSource: "header"})
	e.TouchHost(desk, "")
	p = mustPayload(t, e, desk)
	if r := blockedReason(p, "files"); !strings.Contains(r, "desk-b") {
		t.Fatalf("second device: %q", r)
	}
	if deviceByID(p, "laptop-b") == nil || deviceByID(p, "desk-b") == nil || deviceByID(p, "pc-1") != nil {
		t.Fatalf("bob's devices only: %s", pyjson.Dumps(p.Value("devices")))
	}
	if !pyjson.Truthy(deviceByID(p, "desk-b").Value("last_seen")) {
		t.Fatal("last seen not recorded")
	}
	if first := mapOf(listOr(p.Value("devices"))[0]); str(first.Value("id")) != "desk-b" {
		t.Fatal("the asking device comes first")
	}

	// laptop-b was an invented id; its machine id is now c59c4a65af13.
	machine := NewViewer(Viewer{User: "bob", CanWrite: true, HostID: "c59c4a65af13", HostSource: "header"})
	if _, err := e.AddAlias(machine, "pc-1", "agent:bob"); err == nil {
		t.Fatal("bob aliased ana's device")
	}
	if _, err := e.AddAlias(machine, "laptop-b", "agent:bob"); err != nil {
		t.Fatal(err)
	}
	p = mustPayload(t, e, machine)
	if r := blockedReason(p, "files"); r != "" {
		t.Fatalf("aliased device lost its paths: %s", r)
	}
	if d := deviceByID(p, "c59c4a65af13"); d == nil || d.Value("current") != true || deviceByID(p, "laptop-b") != nil {
		t.Fatalf("alias should fold the old id: %s", pyjson.Dumps(p.Value("devices")))
	}
	if _, err := e.AddAlias(NewViewer(Viewer{User: "bob", CanWrite: true, HostID: "laptop-b"}), "c59c4a65af13", "agent:bob"); err == nil {
		t.Fatal("cycle accepted")
	}
}

func TestServerLegacyFingerprintIsAnAlias(t *testing.T) {
	e := testEnv(t)
	al := e.aliases()
	if old, now := LegacyMachineFingerprint(), MachineFingerprint(); old != now && !al.same(old, now) {
		t.Fatal("old server fingerprint should match the new one")
	}
}
