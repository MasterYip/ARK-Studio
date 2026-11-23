package server

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"arkstudio/go/internal/mobile"
)

const testTunnelHost = "able-baker-charlie-delta.trycloudflare.com"

// tunnelTestApp is a local server without sign-in (like the desktop app)
// whose Cloudflare Tunnel is "up" at testTunnelHost: the tunnel listener's
// state is set directly, cloudflared is not run.
func tunnelTestApp(t *testing.T) *App {
	t.Helper()
	root := t.TempDir()
	t.Setenv("ARK_PRIVATE_DIR", filepath.Join(root, "private"))
	app, err := NewApp(Config{Root: filepath.Join(root, "projects"), NoDemo: true,
		SessionsRoot: filepath.Join(root, "sessions")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	app.Bind("127.0.0.1", 8765)
	fakeTunnelUp(t, app, testTunnelHost)
	return app
}

func fakeTunnelUp(t *testing.T, app *App, host string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	app.cfTunnel.mu.Lock()
	app.cfTunnel.listener, app.cfTunnel.host = ln, host
	app.setTunnelURLLocked("https://" + host + "/")
	app.cfTunnel.mu.Unlock()
}

// viaCF sends a request the way cloudflared delivers it: from 127.0.0.1 to
// the tunnel listener, with the public Host and Cloudflare's headers.
func viaCF(t *testing.T, app *App, method, target string, body any, headers map[string]string) gpuReply {
	t.Helper()
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, target, bytes.NewReader(data))
	r.Host = testTunnelHost
	r.RemoteAddr = "127.0.0.1:50123"
	r.Header.Set("Cf-Connecting-Ip", "203.0.113.7")
	r.Header.Set("Cf-Ray", "8f00000000000000-HKG")
	r.Header.Set("X-Forwarded-For", "203.0.113.7")
	r.Header.Set("X-Forwarded-Proto", "https")
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Content-Length", strconv.Itoa(len(data)))
	}
	for k, v := range headers {
		if v == "" {
			r.Header.Del(k)
		} else {
			r.Header.Set(k, v)
		}
	}
	w := httptest.NewRecorder()
	app.TunnelHandler().ServeHTTP(w, r)
	out := gpuReply{code: w.Code, raw: w.Body.String()}
	_ = json.Unmarshal(w.Body.Bytes(), &out.body)
	return out
}

func newTunnelPairing(t *testing.T, app *App) (code, pairID string) {
	t.Helper()
	r := gpuCall(t, app, "POST", "/api/mobile/pair", map[string]any{"permission": "full"}, nil)
	if r.code != 200 {
		t.Fatalf("create pairing: %d %s", r.code, r.raw)
	}
	if r.body["base_source"] != "cf_tunnel" || r.body["base_url"] != "https://"+testTunnelHost+"/" {
		t.Fatalf("pairing base: %v %v", r.body["base_source"], r.body["base_url"])
	}
	if !strings.HasPrefix(r.body["pairing_url"].(string), "https://"+testTunnelHost+"/#pair=arkp_") {
		t.Fatalf("pairing url: %v", r.body["pairing_url"])
	}
	pair := r.body["pair"].(map[string]any)
	return r.body["code"].(string), pair["id"].(string)
}

// claimConfirmed pairs a phone through the tunnel, the computer confirming.
func claimConfirmed(t *testing.T, app *App, code string) string {
	t.Helper()
	claim := viaCF(t, app, "POST", "/api/mobile/pair/claim", map[string]any{"code": code, "device": "Pixel"}, nil)
	if claim.code != 202 || claim.body["pending"] != true {
		t.Fatalf("claim: %d %s", claim.code, claim.raw)
	}
	confirm, _ := claim.body["confirm_code"].(string)
	if len(confirm) != 6 {
		t.Fatalf("confirm code %q", confirm)
	}
	poll := map[string]any{"code": code, "claim_id": claim.body["claim_id"], "claim_secret": claim.body["claim_secret"]}
	if r := viaCF(t, app, "POST", "/api/mobile/pair/claim/status", poll, nil); r.code != 202 || r.body["confirm_code"] != confirm {
		t.Fatalf("waiting: %d %s", r.code, r.raw)
	}
	pending := gpuCall(t, app, "GET", "/api/mobile/pairs/pending", nil, nil)
	list, _ := pending.body["pending"].([]any)
	if pending.code != 200 || len(list) != 1 {
		t.Fatalf("pending list: %d %s", pending.code, pending.raw)
	}
	item := list[0].(map[string]any)
	if item["confirm_code"] != confirm || item["client_ip"] != "203.0.113.7" || item["device"] != "Pixel" {
		t.Fatalf("pending item: %v", item)
	}
	if r := gpuCall(t, app, "POST", "/api/mobile/pairs/pending/"+item["id"].(string)+"/confirm",
		map[string]any{"approve": true}, nil); r.code != 200 {
		t.Fatalf("confirm: %d %s", r.code, r.raw)
	}
	done := viaCF(t, app, "POST", "/api/mobile/pair/claim/status", poll, nil)
	token, _ := done.body["mobile_token"].(string)
	if done.code != 200 || token == "" {
		t.Fatalf("claimed: %d %s", done.code, done.raw)
	}
	return token
}

// Nothing but the web shell, health, session status, identity and claims
// reaches a local server through the tunnel without a device token, whatever
// the request claims about itself.
func TestTunnelRefusesUnpairedRequests(t *testing.T) {
	app := tunnelTestApp(t)
	if r := viaCF(t, app, "GET", "/api/health", nil, nil); r.code != 200 {
		t.Fatalf("health: %d %s", r.code, r.raw)
	}
	if r := viaCF(t, app, "GET", "/", nil, nil); r.code != 200 {
		t.Fatalf("web shell: %d", r.code)
	}
	spoof := map[string]string{"X-Forwarded-For": "127.0.0.1", "X-Real-Ip": "127.0.0.1", "Forwarded": "for=127.0.0.1"}
	for _, path := range []string{"/api/projects", "/api/bootstrap", "/api/units", "/api/mobile/pairs",
		"/api/mobile/pairs/pending", "/api/agent/token", "/api/mobile/tunnel", "/api/mobile/connection",
		"/api/personal/agent-tokens", "/api/agent/v1/units"} {
		for _, h := range []map[string]string{nil, spoof} {
			if r := viaCF(t, app, "GET", path, nil, h); r.code != 401 {
				t.Errorf("unpaired GET %s: %d %s", path, r.code, r.raw)
			}
		}
	}
	for _, path := range []string{"/api/mobile/pair", "/api/projects", "/api/mobile/tunnel", "/api/units",
		"/api/mobile/pairs/pending/claim_x/confirm"} {
		if r := viaCF(t, app, "POST", path, map[string]any{}, nil); r.code != 401 {
			t.Errorf("unpaired POST %s: %d %s", path, r.code, r.raw)
		}
	}
	// A bogus device token is no token.
	if r := viaCF(t, app, "GET", "/api/bootstrap", nil, map[string]string{"X-ARK-Mobile-Token": "arkm_forged"}); r.code != 401 {
		t.Errorf("forged token: %d %s", r.code, r.raw)
	}
	// The same server answers the computer itself.
	if r := gpuCall(t, app, "GET", "/api/projects", nil, nil); r.code != 200 {
		t.Fatalf("loopback owner: %d %s", r.code, r.raw)
	}
}

// The tunnel listener answers only the tunnel's hostname, and the app port
// never answers it (nor trusts Cloudflare-forwarded requests).
func TestTunnelHostBinding(t *testing.T) {
	app := tunnelTestApp(t)
	for _, host := range []string{"127.0.0.1:8765", "localhost", "evil-host.trycloudflare.com", "example.com"} {
		req := httptest.NewRequest("GET", "/api/health", nil)
		req.Host, req.RemoteAddr = host, "127.0.0.1:1"
		w := httptest.NewRecorder()
		app.TunnelHandler().ServeHTTP(w, req)
		if w.Code != 403 {
			t.Errorf("tunnel listener with Host %s: %d", host, w.Code)
		}
	}
	newTunnelPairing(t, app) // advertises the tunnel URL
	req := httptest.NewRequest("GET", "/api/health", nil)
	req.Host, req.RemoteAddr = testTunnelHost, "127.0.0.1:1"
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Errorf("app port answered the tunnel host: %d %s", w.Code, w.Body.String())
	}
	// cloudflared pointed at the app port by mistake: loopback peer and Host,
	// but Cloudflare's headers say it came from the internet.
	for _, h := range []map[string]string{{"Cf-Connecting-Ip": "203.0.113.9"}, {"Cf-Ray": "x"}, {"Cdn-Loop": "cloudflare"}} {
		if r := gpuCall(t, app, "GET", "/api/projects", nil, h); r.code != 401 {
			t.Errorf("app port trusted a Cloudflare request %v: %d %s", h, r.code, r.raw)
		}
	}
}

// Pairing through the tunnel: the phone waits for the computer to confirm the
// 6-digit code, then works with its device token only and inside the paired
// phone's scope; revoking it on the computer cuts it off.
func TestTunnelPairingWithConfirmation(t *testing.T) {
	app := tunnelTestApp(t)
	code, pairID := newTunnelPairing(t, app)
	token := claimConfirmed(t, app, code)
	phone := map[string]string{"X-ARK-Mobile-Token": token}

	if r := viaCF(t, app, "POST", "/api/mobile/pair/claim", map[string]any{"code": code}, nil); r.code != 401 {
		t.Errorf("code reused: %d %s", r.code, r.raw)
	}
	for _, path := range []string{"/api/bootstrap", "/api/mobile/session", "/api/mobile/session/projects"} {
		if r := viaCF(t, app, "GET", path, nil, phone); r.code != 200 {
			t.Fatalf("paired GET %s: %d %.300s", path, r.code, r.raw)
		}
	}
	s := viaCF(t, app, "GET", "/api/mobile/session", nil, phone)
	if s.body["authenticated"] != true {
		t.Fatalf("session: %s", s.raw)
	}
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/mobile/pair"}, {"GET", "/api/mobile/pairs/pending"}, {"GET", "/api/mobile/tunnel"},
		{"POST", "/api/mobile/tunnel"}, {"GET", "/api/agent/token"}, {"POST", "/api/mobile/pairs/pending/x/confirm"},
	} {
		var body any
		if c.method == "POST" {
			body = map[string]any{"enabled": false}
		}
		if r := viaCF(t, app, c.method, c.path, body, phone); r.code != 403 {
			t.Errorf("phone %s %s: %d %s", c.method, c.path, r.code, r.raw)
		}
	}
	if r := gpuCall(t, app, "POST", "/api/mobile/pairs/"+pairID+"/revoke", map[string]any{}, nil); r.code != 200 {
		t.Fatalf("revoke: %d %s", r.code, r.raw)
	}
	if r := viaCF(t, app, "GET", "/api/bootstrap", nil, phone); r.code != 401 {
		t.Errorf("after revoke: %d %s", r.code, r.raw)
	}
}

// A photographed QR: two phones claim one code; the computer confirms the one
// whose code matches, the other is declined.
func TestTunnelSecondClaimantDeclined(t *testing.T) {
	app := tunnelTestApp(t)
	code, _ := newTunnelPairing(t, app)
	thief := viaCF(t, app, "POST", "/api/mobile/pair/claim", map[string]any{"code": code}, map[string]string{"Cf-Connecting-Ip": "198.51.100.66"})
	if thief.code != 202 {
		t.Fatalf("thief claim: %d %s", thief.code, thief.raw)
	}
	claimConfirmed2 := func() string { // the owner's phone, confirmed while the thief waits
		claim := viaCF(t, app, "POST", "/api/mobile/pair/claim", map[string]any{"code": code}, nil)
		pending := gpuCall(t, app, "GET", "/api/mobile/pairs/pending", nil, nil)
		list := pending.body["pending"].([]any)
		if len(list) != 2 {
			t.Fatalf("pending: %s", pending.raw)
		}
		for _, item := range list {
			m := item.(map[string]any)
			if m["confirm_code"] == claim.body["confirm_code"] && m["client_ip"] == "203.0.113.7" {
				gpuCall(t, app, "POST", "/api/mobile/pairs/pending/"+m["id"].(string)+"/confirm", map[string]any{"approve": true}, nil)
			}
		}
		r := viaCF(t, app, "POST", "/api/mobile/pair/claim/status", map[string]any{"code": code,
			"claim_id": claim.body["claim_id"], "claim_secret": claim.body["claim_secret"]}, nil)
		tok, _ := r.body["mobile_token"].(string)
		return tok
	}
	if claimConfirmed2() == "" {
		t.Fatal("owner was not paired")
	}
	r := viaCF(t, app, "POST", "/api/mobile/pair/claim/status", map[string]any{"code": code,
		"claim_id": thief.body["claim_id"], "claim_secret": thief.body["claim_secret"]}, nil)
	if r.code != 403 || r.errCode() != "PAIRING_REJECTED" {
		t.Fatalf("thief: %d %s", r.code, r.raw)
	}
	// A wrong claim secret learns nothing.
	if r := viaCF(t, app, "POST", "/api/mobile/pair/claim/status", map[string]any{"claim_id": thief.body["claim_id"],
		"claim_secret": "guess"}, nil); r.code != 401 {
		t.Fatalf("wrong secret: %d %s", r.code, r.raw)
	}
}

// With confirmation off (Settings) a tunnel claim gets its token at once.
func TestTunnelConfirmationSetting(t *testing.T) {
	app := tunnelTestApp(t)
	if r := gpuCall(t, app, "POST", "/api/mobile/tunnel", map[string]any{"require_confirm": false}, nil); r.code != 200 || r.body["require_confirm"] != false {
		t.Fatalf("setting: %d %s", r.code, r.raw)
	}
	code, _ := newTunnelPairing(t, app)
	claim := viaCF(t, app, "POST", "/api/mobile/pair/claim", map[string]any{"code": code}, nil)
	if claim.code != 200 || claim.body["mobile_token"] == nil {
		t.Fatalf("claim: %d %s", claim.code, claim.raw)
	}
	if r := gpuCall(t, app, "GET", "/api/mobile/tunnel", nil, nil); r.body["require_confirm"] != false || r.body["available"] != true {
		t.Fatalf("status: %s", r.raw)
	}
}

// Claims and identity requests are rate-limited per phone address.
func TestTunnelClaimRateLimit(t *testing.T) {
	app := tunnelTestApp(t)
	limited := false
	for i := 0; i < tunnelPerClient+2; i++ {
		r := viaCF(t, app, "POST", "/api/mobile/pair/claim", map[string]any{"code": "arkp_wrong" + strconv.Itoa(i)}, nil)
		if r.code == 429 {
			limited = true
			break
		}
		if r.code != 401 {
			t.Fatalf("bad code: %d %s", r.code, r.raw)
		}
	}
	if !limited {
		t.Fatal("claims were not rate-limited")
	}
	// Another address is not blocked by the first one.
	if r := viaCF(t, app, "POST", "/api/mobile/pair/claim", map[string]any{"code": "arkp_x"},
		map[string]string{"Cf-Connecting-Ip": "198.51.100.1"}); r.code != 401 {
		t.Fatalf("other address: %d %s", r.code, r.raw)
	}
}

// The identity signature binds the server key to the host and the phone's
// nonce; the key survives a restart (so phones keep trusting the server).
func TestServerIdentity(t *testing.T) {
	app := tunnelTestApp(t)
	nonce := "n0nce-0123456789abcdef"
	r := viaCF(t, app, "GET", "/api/mobile/identity?nonce="+nonce, nil, nil)
	if r.code != 200 {
		t.Fatalf("identity: %d %s", r.code, r.raw)
	}
	id, key, sig := r.body["server_id"].(string), r.body["public_key"].(string), r.body["signature"].(string)
	if r.body["host"] != testTunnelHost || !mobile.VerifyIdentity(key, id, testTunnelHost, nonce, sig) {
		t.Fatalf("signature does not verify: %s", r.raw)
	}
	if mobile.VerifyIdentity(key, id, "other.trycloudflare.com", nonce, sig) || mobile.VerifyIdentity(key, id, testTunnelHost, nonce+"x", sig) {
		t.Fatal("signature verifies for another host or nonce")
	}
	if r := viaCF(t, app, "GET", "/api/mobile/identity?nonce=short", nil, nil); r.code != 400 {
		t.Fatalf("short nonce: %d", r.code)
	}
	again, err := mobile.LoadIdentity(filepath.Join(app.Root, ".ark-server-identity.json"))
	if err != nil || again.ID != id || again.PublicKey != key {
		t.Fatalf("identity changed on reload: %v", err)
	}
	st := gpuCall(t, app, "GET", "/api/mobile/tunnel", nil, nil)
	if st.body["server_id"] != id || st.body["relink_code"] != "able-baker-charlie-delta" ||
		st.body["relink_url"] != "https://"+testTunnelHost+"/#relink="+id || st.body["relink_qr_svg"] == "" {
		t.Fatalf("relink: %s", st.raw)
	}
}

// After the quick tunnel restarts with a new address the phone's token works
// at the new address (it re-links without pairing again), and the old name
// is refused.
func TestTunnelNewAddressKeepsToken(t *testing.T) {
	app := tunnelTestApp(t)
	code, _ := newTunnelPairing(t, app)
	token := claimConfirmed(t, app, code)
	const next = "echo-foxtrot-golf-hotel.trycloudflare.com"
	fakeTunnelUp(t, app, next)
	st := gpuCall(t, app, "GET", "/api/mobile/tunnel", nil, nil)
	if st.body["url"] != "https://"+next+"/" || st.body["previous_url"] != "https://"+testTunnelHost+"/" {
		t.Fatalf("status after change: %s", st.raw)
	}
	req := httptest.NewRequest("GET", "/api/bootstrap", nil)
	req.Host, req.RemoteAddr = next, "127.0.0.1:1"
	req.Header.Set("X-ARK-Mobile-Token", token)
	w := httptest.NewRecorder()
	app.TunnelHandler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("token at the new address: %d %.200s", w.Code, w.Body.String())
	}
	if r := viaCF(t, app, "GET", "/api/bootstrap", nil, map[string]string{"X-ARK-Mobile-Token": token}); r.code != 403 {
		t.Fatalf("old address still answered: %d", r.code)
	}
}

// Only the computer manages the tunnel; a server with a public address or a
// non-loopback bind has none.
func TestTunnelSettingsAccess(t *testing.T) {
	app := tunnelTestApp(t)
	if r := gpuCall(t, app, "GET", "/api/mobile/tunnel", nil, map[string]string{"Cf-Connecting-Ip": "203.0.113.1"}); r.code == 200 && (r.body["available"] != false || r.body["url"] != nil) {
		t.Fatalf("Cloudflare-forwarded request read the tunnel: %s", r.raw)
	}
	if r := gpuCall(t, app, "POST", "/api/mobile/tunnel", map[string]any{"enabled": false}, map[string]string{"Cf-Connecting-Ip": "203.0.113.1"}); r.code != 401 && r.code != 403 {
		t.Fatalf("Cloudflare-forwarded request changed the tunnel: %d %s", r.code, r.raw)
	}
	app.publicURL = "https://ark.example.com/"
	defer func() { app.publicURL = "" }()
	if r := gpuCall(t, app, "POST", "/api/mobile/tunnel", map[string]any{"enabled": true}, nil); r.code != 409 {
		t.Fatalf("public server enabling a tunnel: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "GET", "/api/mobile/tunnel", nil, nil); r.body["available"] != false {
		t.Fatalf("available on a public server: %s", r.raw)
	}
}

// Requests through the tunnel are https (Secure cookies) and never local.
func TestTunnelRequestIsRemoteHTTPS(t *testing.T) {
	app := tunnelTestApp(t)
	app.cfTunnel.mu.Lock()
	app.cfTunnel.settings.RequireConfirm = new(bool) // off: direct claim
	app.cfTunnel.loaded = true
	app.cfTunnel.mu.Unlock()
	code, _ := newTunnelPairing(t, app)
	var data []byte
	data, _ = json.Marshal(map[string]any{"code": code})
	r := httptest.NewRequest("POST", "/api/mobile/pair/claim", bytes.NewReader(data))
	r.Host, r.RemoteAddr = testTunnelHost, "127.0.0.1:9"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Content-Length", strconv.Itoa(len(data)))
	w := httptest.NewRecorder()
	app.TunnelHandler().ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Header().Get("Set-Cookie"), "Secure") {
		t.Fatalf("claim cookie: %d %q %s", w.Code, w.Header().Get("Set-Cookie"), w.Body.String())
	}
}

// A phone that cancels drops its request from the computer's list.
func TestTunnelClaimCancel(t *testing.T) {
	app := tunnelTestApp(t)
	code, _ := newTunnelPairing(t, app)
	claim := viaCF(t, app, "POST", "/api/mobile/pair/claim", map[string]any{"code": code}, nil)
	poll := map[string]any{"claim_id": claim.body["claim_id"], "claim_secret": claim.body["claim_secret"], "cancel": true}
	if r := viaCF(t, app, "POST", "/api/mobile/pair/claim/status", poll, nil); r.code != 200 || r.body["cancelled"] != true {
		t.Fatalf("cancel: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "GET", "/api/mobile/pairs/pending", nil, nil); len(r.body["pending"].([]any)) != 0 {
		t.Fatalf("still pending: %s", r.raw)
	}
}
