// Package desktopgate restricts a loopback ark-server to the ARK Studio
// desktop app that started it (Go-only; there is no Python counterpart).
//
// A loopback ARK without sign-in treats every local connection as the owner,
// which on a shared computer lets other local users (or any local process)
// drive the server, including agent sessions and terminals. When the desktop
// app starts ark-server as its sidecar it passes a random secret in
// ARK_DESKTOP_SECRET (environment, not argv: other users cannot read it). The
// gate then admits only:
//
//   - requests carrying the desktop cookie, which a browser obtains by opening
//     a one-time claim URL the app signs with the secret
//     (/__ark_desktop/claim?t=<unix>.<nonce>.<hex hmac>&next=/path);
//   - requests with a token the server itself issued (agent write token,
//     task read tokens), so agent CLIs started by ARK keep working;
//   - GET/HEAD /api/health (readiness probe).
//
// Without ARK_DESKTOP_SECRET the server behaves exactly as before.
package desktopgate

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// EnvSecret names the environment variable carrying the shared secret.
const EnvSecret = "ARK_DESKTOP_SECRET"

// ClaimPath is the one-time sign-in URL path.
const ClaimPath = "/__ark_desktop/claim"

// claimMaxAge bounds how old (or how far in the future) a claim may be.
const claimMaxAge = 120 * time.Second

// Gate wraps an http.Handler.
type Gate struct {
	next       http.Handler
	key        []byte
	cookieName string
	cookieVal  string
	tokenOK    func(string) bool
	now        func() time.Time

	mu   sync.Mutex
	used map[string]time.Time // claim nonce -> expiry
}

// SecretFromEnv reads and removes ARK_DESKTOP_SECRET from the process
// environment so child processes (agent CLIs, terminals) never inherit it.
func SecretFromEnv() string {
	secret := strings.TrimSpace(os.Getenv(EnvSecret))
	os.Unsetenv(EnvSecret)
	return secret
}

// New returns the gate, or nil when secret is empty. tokenOK reports whether
// a bearer / X-ARK-Token / X-ARK-Read-Token value is a server-issued token.
func New(next http.Handler, secret string, port int, tokenOK func(string) bool) *Gate {
	if secret == "" {
		return nil
	}
	g := &Gate{next: next, key: []byte(secret), tokenOK: tokenOK, now: time.Now,
		used: map[string]time.Time{}}
	// Cookies are scoped per host, not per port: keep the port in the name so
	// two desktop servers on one machine do not overwrite each other.
	g.cookieName = "ark_desktop_" + strconv.Itoa(port)
	g.cookieVal = g.mac("cookie")
	return g
}

func (g *Gate) mac(msg string) string {
	m := hmac.New(sha256.New, g.key)
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// SignClaim returns the t= value for a claim made at the given time (the
// desktop app computes the same in Rust; exported for tests).
func SignClaim(secret string, at time.Time, nonce string) string {
	payload := strconv.FormatInt(at.Unix(), 10) + "." + nonce
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte("claim:" + payload))
	return payload + "." + hex.EncodeToString(m.Sum(nil))
}

func (g *Gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == ClaimPath {
		g.claim(w, r)
		return
	}
	if g.allowed(r) {
		g.next.ServeHTTP(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"ok": false, "error": {"status": 401, "message": "This ARK server belongs to the ARK Studio desktop app; open it from the app.", "code": "DESKTOP_ONLY"}}`))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	w.Write([]byte(deniedPage))
}

func (g *Gate) allowed(r *http.Request) bool {
	if r.URL.Path == "/api/health" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		return true
	}
	if c, err := r.Cookie(g.cookieName); err == nil && equal(c.Value, g.cookieVal) {
		return true
	}
	if g.tokenOK == nil {
		return false
	}
	var tokens []string
	if h := r.Header.Get("Authorization"); len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		tokens = append(tokens, strings.TrimSpace(h[7:]))
	}
	tokens = append(tokens, r.Header.Get("X-ARK-Token"), r.Header.Get("X-ARK-Read-Token"))
	for _, t := range tokens {
		if t != "" && g.tokenOK(t) {
			return true
		}
	}
	return false
}

func (g *Gate) claim(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.URL.Query().Get("t"), ".")
	ok := len(parts) == 3 && parts[1] != "" && len(parts[1]) <= 64
	var issued time.Time
	if ok {
		secs, err := strconv.ParseInt(parts[0], 10, 64)
		issued = time.Unix(secs, 0)
		age := g.now().Sub(issued)
		ok = err == nil && age < claimMaxAge && age > -claimMaxAge &&
			equal(parts[2], g.mac("claim:"+parts[0]+"."+parts[1]))
	}
	if ok {
		ok = g.useNonce(parts[1])
	}
	if !ok {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(deniedPage))
		return
	}
	next := r.URL.Query().Get("next")
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		next = "/"
	}
	http.SetCookie(w, &http.Cookie{Name: g.cookieName, Value: g.cookieVal, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// useNonce records a claim nonce; false if it was used before.
func (g *Gate) useNonce(nonce string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	for n, exp := range g.used {
		if now.After(exp) {
			delete(g.used, n)
		}
	}
	if _, seen := g.used[nonce]; seen {
		return false
	}
	g.used[nonce] = now.Add(2 * claimMaxAge)
	return true
}

const deniedPage = `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>ARK Studio</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>body{font:15px/1.5 system-ui,sans-serif;margin:0;display:grid;place-items:center;min-height:100vh;background:#f6f6f4;color:#24262b}
main{max-width:30rem;padding:2rem}h1{font-size:1.2rem}@media(prefers-color-scheme:dark){body{background:#0f141c;color:#e6e8ec}}</style></head>
<body><main><h1>This ARK server belongs to the ARK Studio desktop app</h1>
<p>It only accepts the app's own window. To use it in a web browser, choose <b>Open in browser</b> in the ARK Studio tray menu.</p></main></body></html>`
