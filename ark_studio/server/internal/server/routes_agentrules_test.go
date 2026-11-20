package server

import (
	"strings"
	"testing"
)

// T-0053: rules are composed from the catalog and the two scopes' settings;
// people change them, agents read them.
func TestAgentRulesSettingsAndAgentRead(t *testing.T) {
	t.Setenv("ARK_PRIVATE_DIR", t.TempDir()) // personal rule files
	app, _ := localGPUApp(t)
	agent := map[string]string{"Authorization": "Bearer " + app.AgentToken()}

	r := gpuCall(t, app, "GET", "/api/agent-rules", nil, nil)
	if r.code != 200 || !strings.Contains(r.raw, `"catalog"`) || !strings.Contains(r.raw, "P1. Plans need my approval") {
		t.Fatalf("GET defaults: %d %s", r.code, r.raw)
	}
	eff, _ := r.body["effective"].(map[string]any)
	v0, _ := eff["version"].(string)

	r = gpuCall(t, app, "POST", "/api/agent-rules", map[string]any{"scope": "personal", "settings": map[string]any{
		"rules": map[string]any{"plan-approval": map[string]any{"enabled": false}}}}, nil)
	if r.code != 200 || !strings.Contains(r.raw, "Plans do not need my approval") {
		t.Fatalf("POST personal: %d %s", r.code, r.raw)
	}
	r = gpuCall(t, app, "POST", "/api/agent-rules", map[string]any{"scope": "project", "settings": map[string]any{
		"rules":  map[string]any{"no-kill-by-name": map[string]any{"enabled": true}},
		"custom": []any{map[string]any{"id": "no-periods", "enabled": true, "text": map[string]any{"en": "Promo copy has no full stops."}}}}}, nil)
	if r.code != 200 {
		t.Fatalf("POST project: %d %s", r.code, r.raw)
	}

	r = gpuCall(t, app, "GET", "/api/agent/v1/rules", nil, agent)
	if r.code != 200 {
		t.Fatalf("agent GET: %d %s", r.code, r.raw)
	}
	v1, _ := r.body["version"].(string)
	for _, want := range []string{"Plans do not need my approval", "by PID", "Promo copy has no full stops."} {
		if !strings.Contains(r.raw, want) {
			t.Fatalf("agent rules miss %q: %s", want, r.raw)
		}
	}
	if v1 == "" || v1 == v0 {
		t.Fatalf("version %q did not change from %q", v1, v0)
	}
	if r = gpuCall(t, app, "GET", "/api/agent/v1/rules?lang=zh", nil, agent); !strings.Contains(r.raw, "核心规则") {
		t.Fatalf("zh lines: %s", r.raw)
	}

	// Required rules cannot be switched off, agents cannot change rules,
	// unknown scopes are refused.
	for _, bad := range []struct {
		body    map[string]any
		headers map[string]string
		code    int
	}{
		{map[string]any{"scope": "personal", "settings": map[string]any{"rules": map[string]any{"progress-body": map[string]any{"enabled": false}}}}, nil, 400},
		{map[string]any{"scope": "personal", "settings": map[string]any{}}, agent, 403},
		{map[string]any{"scope": "org", "settings": map[string]any{}}, nil, 400},
	} {
		if r = gpuCall(t, app, "POST", "/api/agent-rules", bad.body, bad.headers); r.code != bad.code {
			t.Fatalf("%v: want %d, got %d %s", bad.body, bad.code, r.code, r.raw)
		}
	}
}
