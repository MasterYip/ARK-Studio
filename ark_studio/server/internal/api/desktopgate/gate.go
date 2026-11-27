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
//   - requests with a token the server itself issued and would accept (agent
//     write token, task read tokens, valid personal arka_ tokens including
//     the per-run tokens of sessions ARK launches), so agent CLIs started by
//     ARK and agents given a copied prompt keep working; the request is
//     then authenticated and authorised as usual;
//   - GET/HEAD /api/health (readiness probe).
//
// Without ARK_DESKTOP_SECRET the server behaves exactly as before.
package desktopgate

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net"
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
	tokenOK    func(*http.Request, string) bool
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
// a bearer / X-ARK-Token / X-ARK-Read-Token value of the request is a token
// the server issued and would accept.
func New(next http.Handler, secret string, port int, tokenOK func(*http.Request, string) bool) *Gate {
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

type ownerKey struct{}

// Owner reports whether r carries this app's desktop cookie: the request
// comes from the app's own window, wherever the connection seems to come
// from (a local proxy the system routes 127.0.0.1 through still delivers
// the window's cookie). The server treats it like a loopback request, so
// the window never gets the phone pairing page.
func Owner(r *http.Request) bool {
	v, _ := r.Context().Value(ownerKey{}).(bool)
	return v
}

func (g *Gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == ClaimPath {
		g.claim(w, r)
		return
	}
	if g.owner(r) {
		g.next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ownerKey{}, true)))
		return
	}
	if g.allowed(r) {
		g.next.ServeHTTP(w, r)
		return
	}
	g.deny(w, r, http.StatusUnauthorized)
}

// Reasons for a refusal (Reason).
const (
	// ReasonNotLocal: the request reached the server through a proxy or
	// from another address; the app's window would carry its cookie.
	ReasonNotLocal = "not_local"
	// ReasonNotApp: a browser on this computer without the app's cookie.
	ReasonNotApp = "not_app"
)

// proxyHeaders are added by HTTP proxies and CDNs on the way.
var proxyHeaders = []string{"Via", "X-Forwarded-For", "Forwarded", "X-Real-Ip", "X-Forwarded-Host",
	"Cf-Connecting-Ip", "Cf-Ray"}

// Reason says why r would be refused: ReasonNotLocal when it came through a
// proxy or not from this computer, else ReasonNotApp.
func Reason(r *http.Request) string {
	for _, h := range proxyHeaders {
		if r.Header.Get(h) != "" {
			return ReasonNotLocal
		}
	}
	if !loopbackHost(remoteHost(r.RemoteAddr)) || !loopbackHost(hostName(r.Host)) {
		return ReasonNotLocal
	}
	return ReasonNotApp
}

func remoteHost(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

func hostName(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return strings.Trim(host, "[]")
}

func loopbackHost(h string) bool {
	h = strings.ToLower(strings.Trim(strings.TrimSpace(h), "[]"))
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Split(h, "%")[0])
	return ip != nil && ip.IsLoopback()
}

func (g *Gate) deny(w http.ResponseWriter, r *http.Request, status int) {
	reason := Reason(r)
	w.Header().Set("Cache-Control", "no-store")
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if reason == ReasonNotLocal {
			w.Write([]byte(`{"ok": false, "error": {"status": 401, "message": "This address is outside the service range: this ARK server only accepts connections from this computer. If a proxy is on, let localhost and 127.0.0.1 bypass it.", "code": "DESKTOP_NOT_LOCAL"}}`))
			return
		}
		w.Write([]byte(`{"ok": false, "error": {"status": 401, "message": "This ARK server belongs to the ARK Studio desktop app; open it from the app.", "code": "DESKTOP_ONLY"}}`))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	w.Write([]byte(deniedPage(reason, prefersChinese(r.Header.Get("Accept-Language")))))
}

// prefersChinese: the first language of Accept-Language is Chinese.
func prefersChinese(accept string) bool {
	first := strings.ToLower(strings.TrimSpace(strings.Split(accept, ",")[0]))
	return strings.HasPrefix(first, "zh")
}

func (g *Gate) owner(r *http.Request) bool {
	c, err := r.Cookie(g.cookieName)
	return err == nil && equal(c.Value, g.cookieVal)
}

func (g *Gate) allowed(r *http.Request) bool {
	if r.URL.Path == "/api/health" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
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
		if t != "" && g.tokenOK(r, t) {
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
		g.deny(w, r, http.StatusForbidden)
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

// deniedPage is the refusal page: it names the reason in plain words (the
// address is outside the service range, or the server belongs to the app).
func deniedPage(reason string, zh bool) string {
	lang, title, body, hint := "en", "", "", ""
	switch {
	case reason == ReasonNotLocal && zh:
		lang = "zh-CN"
		title = "此地址不在服务范围内"
		body = "这台 ARK 服务器只接受本机（或已配对设备）的连接。"
		hint = "如果你开了代理，请让本机地址（localhost、127.0.0.1）不经过代理，然后重新打开 ARK Studio。"
	case reason == ReasonNotLocal:
		title = "This address is outside the service range"
		body = "This ARK server only accepts connections from this computer (or from paired devices)."
		hint = "If a proxy is on, let local addresses (localhost, 127.0.0.1) bypass it, then reopen ARK Studio."
	case zh:
		lang = "zh-CN"
		title = "这台 ARK 服务器属于 ARK Studio 桌面端"
		body = "它只接受桌面端自己的窗口。要在浏览器中使用，请在 ARK Studio 托盘菜单中选择<b>在浏览器中打开</b>。"
		hint = "如果这就是 ARK Studio 的窗口，并且开着代理，请让本机地址（localhost、127.0.0.1）不经过代理。"
	default:
		title = "This ARK server belongs to the ARK Studio desktop app"
		body = "It only accepts the app's own window. To use it in a web browser, choose <b>Open in browser</b> in the ARK Studio tray menu."
		hint = "If this is the ARK Studio window and a proxy is on, let local addresses (localhost, 127.0.0.1) bypass it."
	}
	return `<!doctype html><html lang="` + lang + `"><head><meta charset="utf-8"><title>ARK Studio</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>body{font:15px/1.5 system-ui,sans-serif;margin:0;display:grid;place-items:center;min-height:100vh;background:#f6f6f4;color:#24262b}
main{max-width:30rem;padding:2rem}h1{font-size:1.2rem}.hint{font-size:.85rem;opacity:.75}
@media(prefers-color-scheme:dark){body{background:#0f141c;color:#e6e8ec}}</style></head>
<body><main data-reason="` + reason + `"><h1>` + title + `</h1>
<p>` + body + `</p><p class="hint">` + hint + `</p></main></body></html>`
}
