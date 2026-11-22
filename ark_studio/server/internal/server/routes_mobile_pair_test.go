package server

// Creating a pairing code is not a project change: it must not bump the
// project revision (open tabs would refetch and repaint for it).

import (
	"context"
	"path/filepath"
	"testing"
)

func TestCreatePairingKeepsProjectRevision(t *testing.T) {
	root := t.TempDir()
	app, err := NewApp(Config{Root: filepath.Join(root, "projects"), NoDemo: true,
		SessionsRoot: filepath.Join(root, "sessions"), PublicURL: "https://ark.example.test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	app.Bind("127.0.0.1", 8765)
	r := gpuCall(t, app, "POST", "/api/projects", map[string]any{"name": "Pairing"}, nil)
	summary, _ := r.body["summary"].(map[string]any)
	projectID, _ := summary["id"].(string)
	if r.code != 200 || projectID == "" {
		t.Fatalf("create project: %d %s", r.code, r.raw)
	}
	project := map[string]string{"X-ARK-Project": projectID}
	revision := func() string {
		t.Helper()
		r := gpuCall(t, app, "GET", "/api/revision", nil, project)
		rev, _ := r.body["revision"].(string)
		if r.code != 200 || rev == "" {
			t.Fatalf("revision: %d %s", r.code, r.raw)
		}
		return rev
	}
	before := revision()
	for i := 0; i < 3; i++ {
		r = gpuCall(t, app, "POST", "/api/mobile/pair", map[string]any{"permission": "full", "expires_in": 600}, project)
		if r.code != 200 || r.body["code"] == nil {
			t.Fatalf("pair %d: %d %s", i, r.code, r.raw)
		}
	}
	if after := revision(); after != before {
		t.Fatalf("pairing codes changed the project revision: %s -> %s", before, after)
	}
	// Still audited (org log).
	audit, err := app.Org.ListAudit(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	created := 0
	for _, item := range audit {
		if obj, ok := item.(interface{ Value(string) any }); ok && obj.Value("action") == "mobile.pair.create" {
			created++
		}
	}
	if created != 3 {
		t.Fatalf("audit rows for mobile.pair.create: %d, want 3", created)
	}
}
