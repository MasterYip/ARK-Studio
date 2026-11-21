package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"arkstudio/go/internal/api/desktopgate"
)

const gateSecret = "desktop-gate-test-secret"

// gated wraps app in the desktop gate the way cmd/ark-server does and
// returns the desktop cookie from a signed claim.
func gated(t *testing.T, app *App) (http.Handler, *http.Cookie) {
	t.Helper()
	g := desktopgate.New(app, gateSecret, 8765, app.IssuedToken)
	claim := "/__ark_desktop/claim?t=" + url.QueryEscape(desktopgate.SignClaim(gateSecret, time.Now(), "n-"+t.Name()))
	w := httptest.NewRecorder()
	g.ServeHTTP(w, httptest.NewRequest("GET", claim, nil))
	cookies := w.Result().Cookies()
	if w.Code != 303 || len(cookies) != 1 {
		t.Fatalf("claim: %d %v", w.Code, cookies)
	}
	return g, cookies[0]
}

func gateCall(t *testing.T, h http.Handler, method, target string, body any, headers map[string]string, cookie *http.Cookie) gpuReply {
	t.Helper()
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, target, bytes.NewReader(data))
	r.Host = "127.0.0.1:8765"
	r.RemoteAddr = "127.0.0.1:50000"
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Content-Length", strconv.Itoa(len(data)))
		r.Header.Set("Origin", "http://127.0.0.1:8765")
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	out := gpuReply{code: w.Code, raw: w.Body.String()}
	_ = json.Unmarshal(w.Body.Bytes(), &out.body)
	return out
}

func bearer(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }

// Local desktop mode (no sign-in): personal tokens minted in the app pass
// the gate and work until revoked; read-scope tokens stay read-only; forged
// and unknown tokens are refused by the gate; the server token still
// passes; health stays open.
func TestDesktopGateLocalPersonalTokens(t *testing.T) {
	app, _ := localGPUApp(t)
	h, cookie := gated(t, app)
	if r := gateCall(t, h, "GET", "/api/health", nil, nil, nil); r.code != 200 {
		t.Fatalf("health: %d", r.code)
	}
	mint := func(scope string) (string, string) {
		r := gateCall(t, h, "POST", "/api/personal/agent-tokens", map[string]any{"label": "cli", "scope": scope}, nil, cookie)
		tok, _ := r.body["token"].(string)
		item, _ := r.body["item"].(map[string]any)
		id, _ := item["id"].(string)
		if r.code != 200 || !strings.HasPrefix(tok, "arka_") || id == "" {
			t.Fatalf("mint: %d %s", r.code, r.raw)
		}
		return tok, id
	}
	if r := gateCall(t, h, "POST", "/api/personal/agent-tokens", map[string]any{"label": "x"}, nil, nil); r.code != 401 || r.errCode() != "DESKTOP_ONLY" {
		t.Fatalf("minting without the desktop cookie: %d %s", r.code, r.raw)
	}
	tok, id := mint("write")
	if r := gateCall(t, h, "GET", "/api/agent/v1/context", nil, bearer(tok), nil); r.code != 200 {
		t.Fatalf("personal token refused: %d %s", r.code, r.raw)
	}
	if r := gateCall(t, h, "GET", "/api/agent/v1/context", nil, map[string]string{"X-ARK-Token": tok}, nil); r.code != 200 {
		t.Fatalf("X-ARK-Token: %d %s", r.code, r.raw)
	}
	if r := gateCall(t, h, "POST", "/api/agent/v1/units", map[string]any{"type": "task", "title": "via gate"}, bearer(tok), nil); r.code != 200 {
		t.Fatalf("write token cannot write: %d %s", r.code, r.raw)
	}
	// Passing the gate is not the desktop cookie: the token still cannot
	// manage tokens.
	if r := gateCall(t, h, "GET", "/api/personal/agent-tokens", nil, bearer(tok), nil); r.code != 403 {
		t.Fatalf("token managing tokens: %d %s", r.code, r.raw)
	}
	read, _ := mint("read")
	if r := gateCall(t, h, "GET", "/api/agent/v1/context", nil, bearer(read), nil); r.code != 200 {
		t.Fatalf("read token refused: %d %s", r.code, r.raw)
	}
	if r := gateCall(t, h, "POST", "/api/agent/v1/units", map[string]any{"type": "task", "title": "x"}, bearer(read), nil); r.code == 200 {
		t.Fatalf("read-scope token wrote through the gate: %s", r.raw)
	}
	// Revocation applies at once.
	if r := gateCall(t, h, "DELETE", "/api/personal/agent-tokens/"+id, nil, nil, cookie); r.code != 200 {
		t.Fatalf("revoke: %d %s", r.code, r.raw)
	}
	if r := gateCall(t, h, "GET", "/api/agent/v1/context", nil, bearer(tok), nil); r.code != 401 || r.errCode() != "DESKTOP_ONLY" {
		t.Fatalf("revoked token: %d %s", r.code, r.raw)
	}
	for _, forged := range []string{"arka_" + strings.Repeat("A", 40), tok + "x", "forged", app.AgentToken() + "x"} {
		if r := gateCall(t, h, "GET", "/api/agent/v1/context", nil, bearer(forged), nil); r.code != 401 || r.errCode() != "DESKTOP_ONLY" {
			t.Fatalf("forged %q: %d %s", forged, r.code, r.raw)
		}
	}
	if r := gateCall(t, h, "GET", "/api/agent/v1/context", nil, bearer(app.AgentToken()), nil); r.code != 200 {
		t.Fatalf("server agent token: %d %s", r.code, r.raw)
	}
}

// Expired personal tokens are refused by the gate.
func TestDesktopGateExpiredToken(t *testing.T) {
	app, _ := authDesktopApp(t)
	h, _ := gated(t, app)
	expired, _, err := app.Org.CreateSessionToken(context.Background(), "owner", "s_exp", "write", -time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if r := gateCall(t, h, "GET", "/api/agent/v1/context", nil, bearer(expired), nil); r.code != 401 || r.errCode() != "DESKTOP_ONLY" {
		t.Fatalf("expired: %d %s", r.code, r.raw)
	}
}

// Account mode behind the gate (a desktop server with sign-in): the per-run
// personal token of an ARK-launched session passes the gate and keeps its
// scope; once the run's token is revoked the gate refuses it.
func TestDesktopGateSessionRunTokens(t *testing.T) {
	app, projectID := authDesktopApp(t)
	h, _ := gated(t, app)
	hdr := func(tok string) map[string]string {
		return map[string]string{"Authorization": "Bearer " + tok, "X-ARK-Project": projectID}
	}
	secret, id, err := app.sessionAgentToken("owner", "s_gate1", "full_access")
	if err != nil || !strings.HasPrefix(secret, "arka_") {
		t.Fatalf("%q %v", secret, err)
	}
	if r := gateCall(t, h, "GET", "/api/agent/v1/context", nil, hdr(secret), nil); r.code != 200 {
		t.Fatalf("run token refused: %d %s", r.code, r.raw)
	}
	if r := gateCall(t, h, "POST", "/api/agent/v1/units", map[string]any{"type": "task", "title": "run"}, hdr(secret), nil); r.code != 200 {
		t.Fatalf("run token cannot write: %d %s", r.code, r.raw)
	}
	read, _, err := app.sessionAgentToken("owner", "s_gate2", "read_only")
	if err != nil {
		t.Fatal(err)
	}
	if r := gateCall(t, h, "GET", "/api/agent/v1/context", nil, hdr(read), nil); r.code != 200 {
		t.Fatalf("read run token refused: %d %s", r.code, r.raw)
	}
	if r := gateCall(t, h, "POST", "/api/agent/v1/units", map[string]any{"type": "task", "title": "x"}, hdr(read), nil); r.code == 200 {
		t.Fatal("read-only run wrote through the gate")
	}
	if err := app.Org.RevokeTokenID(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if r := gateCall(t, h, "GET", "/api/agent/v1/context", nil, hdr(secret), nil); r.code != 401 || r.errCode() != "DESKTOP_ONLY" {
		t.Fatalf("revoked run token: %d %s", r.code, r.raw)
	}
}

// Repeated forged personal tokens count against the shared failure limiter.
func TestDesktopGateForgedTokensLimited(t *testing.T) {
	app, _ := localGPUApp(t)
	r := httptest.NewRequest("GET", "/api/agent/v1/context", nil)
	r.RemoteAddr = "127.0.0.1:50000"
	for i := 0; i < failLimit; i++ {
		if app.IssuedToken(r, "arka_forged"+strconv.Itoa(i)+strings.Repeat("x", 30)) {
			t.Fatal("forged token admitted")
		}
	}
	if err := app.failures.check("127.0.0.1"); err == nil {
		t.Fatal("failures not recorded")
	}
}
