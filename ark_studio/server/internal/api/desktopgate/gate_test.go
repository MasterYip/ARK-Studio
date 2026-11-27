package desktopgate

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func newGate(t *testing.T) (*Gate, *time.Time) {
	t.Helper()
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("app")) })
	now := time.Unix(1_800_000_000, 0)
	g := New(ok, "s3cret", 18890, func(_ *http.Request, tok string) bool { return tok == "agent-token" })
	g.now = func() time.Time { return now }
	return g, &now
}

func do(g *Gate, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	return rec
}

func TestNilWithoutSecret(t *testing.T) {
	if New(http.NotFoundHandler(), "", 1, nil) != nil {
		t.Fatal("gate must be off without a secret")
	}
}

// localReq is a request from a browser on this computer.
func localReq(method, target string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = "127.0.0.1:50000"
	req.Host = "127.0.0.1:18890"
	return req
}

func TestDeniesWithoutCookie(t *testing.T) {
	g, _ := newGate(t)
	if rec := do(g, localReq("GET", "/")); rec.Code != 401 || !strings.Contains(rec.Body.String(), "desktop app") {
		t.Fatalf("page: %d %q", rec.Code, rec.Body.String())
	}
	if rec := do(g, localReq("POST", "/api/units")); rec.Code != 401 || !strings.Contains(rec.Body.String(), "DESKTOP_ONLY") {
		t.Fatalf("api: %d", rec.Code)
	}
	if rec := do(g, httptest.NewRequest("POST", "/api/health", nil)); rec.Code != 401 {
		t.Fatalf("POST health must be gated: %d", rec.Code)
	}
}

func TestHealthAndIssuedTokens(t *testing.T) {
	g, _ := newGate(t)
	if rec := do(g, httptest.NewRequest("GET", "/api/health", nil)); rec.Code != 200 {
		t.Fatalf("health: %d", rec.Code)
	}
	for _, h := range [][2]string{{"Authorization", "Bearer agent-token"}, {"X-ARK-Token", "agent-token"}, {"X-ARK-Read-Token", "agent-token"}} {
		req := httptest.NewRequest("GET", "/api/project", nil)
		req.Header.Set(h[0], h[1])
		if rec := do(g, req); rec.Code != 200 {
			t.Fatalf("%s: %d", h[0], rec.Code)
		}
	}
	req := httptest.NewRequest("GET", "/api/project", nil)
	req.Header.Set("Authorization", "Bearer forged")
	if rec := do(g, req); rec.Code != 401 {
		t.Fatalf("forged token admitted: %d", rec.Code)
	}
}

func TestClaimIsOneTimeAndSetsCookie(t *testing.T) {
	g, now := newGate(t)
	claim := "/__ark_desktop/claim?t=" + url.QueryEscape(SignClaim("s3cret", *now, "n1")) + "&next=" + url.QueryEscape("/#/graph")
	rec := do(g, httptest.NewRequest("GET", claim, nil))
	if rec.Code != 303 || rec.Header().Get("Location") != "/#/graph" {
		t.Fatalf("claim: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "ark_desktop_18890" || !cookies[0].HttpOnly {
		t.Fatalf("cookie: %+v", cookies)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookies[0])
	if rec := do(g, req); rec.Code != 200 || rec.Body.String() != "app" {
		t.Fatalf("with cookie: %d", rec.Code)
	}
	if rec := do(g, httptest.NewRequest("GET", claim, nil)); rec.Code != 403 {
		t.Fatalf("replayed claim: %d", rec.Code)
	}
	bad := &http.Cookie{Name: "ark_desktop_18890", Value: "00"}
	req = httptest.NewRequest("GET", "/", nil)
	req.AddCookie(bad)
	if rec := do(g, req); rec.Code != 401 {
		t.Fatalf("bad cookie: %d", rec.Code)
	}
}

func TestClaimRejectsStaleForgedAndOpenRedirect(t *testing.T) {
	g, now := newGate(t)
	stale := SignClaim("s3cret", now.Add(-5*time.Minute), "n2")
	if rec := do(g, httptest.NewRequest("GET", ClaimPath+"?t="+url.QueryEscape(stale), nil)); rec.Code != 403 {
		t.Fatalf("stale: %d", rec.Code)
	}
	forged := SignClaim("other", *now, "n3")
	if rec := do(g, httptest.NewRequest("GET", ClaimPath+"?t="+url.QueryEscape(forged), nil)); rec.Code != 403 {
		t.Fatalf("forged: %d", rec.Code)
	}
	for i, next := range []string{"//evil.example", "https://evil.example", `/\evil`} {
		ok := SignClaim("s3cret", *now, "redirect"+string(rune('a'+i)))
		rec := do(g, httptest.NewRequest("GET", ClaimPath+"?t="+url.QueryEscape(ok)+"&next="+url.QueryEscape(next), nil))
		if rec.Code != 303 || rec.Header().Get("Location") != "/" {
			t.Fatalf("next %q -> %q", next, rec.Header().Get("Location"))
		}
	}
}

// The gate asks tokenOK on every request (no caching), so a token the
// server stops accepting (revoked, expired) is refused on the next request.
func TestTokenCheckedPerRequest(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("app")) })
	live := true
	var seen *http.Request
	g := New(ok, "s3cret", 18890, func(r *http.Request, tok string) bool {
		seen = r
		return live && tok == "arka_personal"
	})
	req := localReq("GET", "/api/agent/v1/context")
	req.Header.Set("Authorization", "Bearer arka_personal")
	if rec := do(g, req); rec.Code != 200 || seen != req {
		t.Fatalf("personal token: %d", rec.Code)
	}
	live = false
	if rec := do(g, req); rec.Code != 401 || !strings.Contains(rec.Body.String(), "DESKTOP_ONLY") {
		t.Fatalf("revoked token admitted: %d", rec.Code)
	}
}

// The Linux desktop with a proxy on (2026-11-27): the app window's
// requests reached its own sidecar through a local HTTP proxy. With the
// window's cookie such a request is the app (Owner), whatever the proxy
// added; without it the page names the real reason, not the tray hint.
func TestOwnerThroughLocalProxy(t *testing.T) {
	var owner bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { owner = Owner(r); w.Write([]byte("app")) })
	g := New(next, "s3cret", 18890, nil)
	proxied := func(target string) *http.Request {
		req := localReq("GET", target)
		req.Header.Set("Via", "1.1 clash")
		req.Header.Set("X-Forwarded-For", "127.0.0.1")
		req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15")
		return req
	}
	for _, target := range []string{"/", "/css/mobile.css", "/api/mobile/session"} {
		req := proxied(target)
		req.AddCookie(&http.Cookie{Name: "ark_desktop_18890", Value: g.cookieVal})
		owner = false
		if rec := do(g, req); rec.Code != 200 || !owner {
			t.Fatalf("%s with the window cookie: %d owner=%v", target, rec.Code, owner)
		}
	}
	// Plain loopback with the cookie is the owner too; issued tokens are not.
	req := localReq("GET", "/api/project")
	req.AddCookie(&http.Cookie{Name: "ark_desktop_18890", Value: g.cookieVal})
	if do(g, req); !owner {
		t.Fatal("cookie on loopback must mark the owner")
	}
	// A wrong cookie through the proxy: the not-local page, in Chinese for a
	// Chinese browser, without the tray hint.
	req = proxied("/")
	req.AddCookie(&http.Cookie{Name: "ark_desktop_18890", Value: "forged"})
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	rec := do(g, req)
	body := rec.Body.String()
	if rec.Code != 401 || !strings.Contains(body, "此地址不在服务范围内") || !strings.Contains(body, "127.0.0.1") ||
		!strings.Contains(body, `data-reason="not_local"`) || strings.Contains(body, "托盘") {
		t.Fatalf("not-local page: %d %q", rec.Code, body)
	}
	if rec := do(g, proxied("/api/units")); rec.Code != 401 || !strings.Contains(rec.Body.String(), "DESKTOP_NOT_LOCAL") {
		t.Fatalf("not-local api: %d %q", rec.Code, rec.Body.String())
	}
}

func TestRefusalReasons(t *testing.T) {
	g, _ := newGate(t)
	cases := []struct {
		name   string
		mutate func(*http.Request)
		reason string
		text   string
	}{
		{"browser on this computer", func(*http.Request) {}, ReasonNotApp, "Open in browser"},
		{"localhost name", func(r *http.Request) { r.Host = "localhost:18890" }, ReasonNotApp, "Open in browser"},
		{"ipv6 loopback", func(r *http.Request) { r.RemoteAddr = "[::1]:5000"; r.Host = "[::1]:18890" }, ReasonNotApp, "Open in browser"},
		{"via header", func(r *http.Request) { r.Header.Set("Via", "1.1 proxy") }, ReasonNotLocal, "outside the service range"},
		{"forwarded", func(r *http.Request) { r.Header.Set("Forwarded", "for=127.0.0.1") }, ReasonNotLocal, "outside the service range"},
		{"other address", func(r *http.Request) { r.RemoteAddr = "192.0.2.7:4000" }, ReasonNotLocal, "outside the service range"},
		{"other host name", func(r *http.Request) { r.Host = "ark.example.com" }, ReasonNotLocal, "outside the service range"},
	}
	for _, c := range cases {
		req := localReq("GET", "/")
		c.mutate(req)
		if got := Reason(req); got != c.reason {
			t.Fatalf("%s: reason %q, want %q", c.name, got, c.reason)
		}
		rec := do(g, req)
		if rec.Code != 401 || !strings.Contains(rec.Body.String(), c.text) {
			t.Fatalf("%s: %d %q", c.name, rec.Code, rec.Body.String())
		}
	}
	// A refused claim names the reason as well.
	req := localReq("GET", "/__ark_desktop/claim?t=1.n.bad")
	req.Header.Set("Accept-Language", "zh-CN")
	if rec := do(g, req); rec.Code != 403 || !strings.Contains(rec.Body.String(), "在浏览器中打开") {
		t.Fatalf("claim page: %d %q", rec.Code, rec.Body.String())
	}
}
