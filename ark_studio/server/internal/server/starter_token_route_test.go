package server

import (
	"strings"
	"testing"
)

// The stored Home starter token is handed out only to the signed-in owner:
// an agent holding a personal token (even the starter token itself) and a
// caller without the session are refused.
func TestStarterTokenRouteOwnerOnly(t *testing.T) {
	t.Setenv("ARK_ORG_SECRET_KEY", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	app, _ := localGPUApp(t)
	h, cookie := gated(t, app)
	body := map[string]any{"purpose": "starter", "project": "prj_a", "scope": "write", "expires_days": 90}
	r := gateCall(t, h, "POST", "/api/personal/agent-tokens", body, nil, cookie)
	tok, _ := r.body["token"].(string)
	if r.code != 200 || !strings.HasPrefix(tok, "arka_") || r.body["reused"] != false {
		t.Fatalf("first copy: %d %s", r.code, r.raw)
	}
	if again := gateCall(t, h, "POST", "/api/personal/agent-tokens", body, nil, cookie); again.body["token"] != tok || again.body["reused"] != true {
		t.Fatalf("second copy did not reuse: %d", again.code)
	}
	if r := gateCall(t, h, "POST", "/api/personal/agent-tokens", body, bearer(tok), nil); r.code != 403 || strings.Contains(r.raw, tok) {
		t.Fatalf("agent token fetched the starter token: %d", r.code)
	}
	if r := gateCall(t, h, "POST", "/api/personal/agent-tokens", body, nil, nil); r.code == 200 || strings.Contains(r.raw, tok) {
		t.Fatalf("no session fetched the starter token: %d", r.code)
	}
}
