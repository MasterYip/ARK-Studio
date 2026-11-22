package server

// prefs.seen_version: the server version a person last saw, for the web
// app's "This server was updated to ..." notice (once per version).

import "testing"

func TestSeenVersionPref(t *testing.T) {
	app, _ := chatScopeApp(t)
	save := func(v any) gpuReply {
		t.Helper()
		return gpuCall(t, app, "PATCH", "/api/me", map[string]any{"prefs": map[string]any{"seen_version": v}}, nil)
	}
	r := save("2.1.2")
	if r.code != 200 {
		t.Fatalf("save: %d %s", r.code, r.raw)
	}
	if got := r.body["profile"].(map[string]any)["prefs"].(map[string]any)["seen_version"]; got != "2.1.2" {
		t.Fatalf("echoed: %v", got)
	}
	for _, bad := range []any{"latest", "2.1", 3, "2.1.2<script>"} {
		if r := save(bad); r.code != 400 {
			t.Fatalf("%v: %d %s", bad, r.code, r.raw)
		}
	}
	r = save(nil)
	if _, has := r.body["profile"].(map[string]any)["prefs"].(map[string]any)["seen_version"]; r.code != 200 || has {
		t.Fatalf("cleared: %d %s", r.code, r.raw)
	}
}
