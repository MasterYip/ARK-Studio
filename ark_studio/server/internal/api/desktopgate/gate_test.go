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

func TestDeniesWithoutCookie(t *testing.T) {
	g, _ := newGate(t)
	if rec := do(g, httptest.NewRequest("GET", "/", nil)); rec.Code != 401 || !strings.Contains(rec.Body.String(), "desktop app") {
		t.Fatalf("page: %d %q", rec.Code, rec.Body.String())
	}
	if rec := do(g, httptest.NewRequest("POST", "/api/units", nil)); rec.Code != 401 || !strings.Contains(rec.Body.String(), "DESKTOP_ONLY") {
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
	req := httptest.NewRequest("GET", "/api/agent/v1/context", nil)
	req.Header.Set("Authorization", "Bearer arka_personal")
	if rec := do(g, req); rec.Code != 200 || seen != req {
		t.Fatalf("personal token: %d", rec.Code)
	}
	live = false
	if rec := do(g, req); rec.Code != 401 || !strings.Contains(rec.Body.String(), "DESKTOP_ONLY") {
		t.Fatalf("revoked token admitted: %d", rec.Code)
	}
}
