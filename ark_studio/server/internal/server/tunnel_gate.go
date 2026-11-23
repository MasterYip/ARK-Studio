package server

// The Cloudflare Tunnel gate (Go only, mobile_tunnel.go starts it).
//
// cloudflared forwards public HTTPS requests to a second loopback listener
// that only it uses, and that listener serves TunnelHandler. A request that
// arrives there is a remote phone even though its TCP peer is 127.0.0.1, so:
//
//   - it never counts as trusted local (the owner of a server without
//     sign-in), whatever its peer, Host or forwarding headers say;
//   - the desktop gate's cookie does not apply (the phone has none);
//   - before it holds a paired-device token or a signed-in session it may
//     only load the web shell, check health, read its session status, ask
//     for the server identity and claim a pairing code (rate-limited);
//   - its Host must be the tunnel's own hostname (DNS-rebinding guard);
//   - after that the usual paired-phone scope (guardMobile) applies.
//
// The app port itself never accepts the tunnel's hostname (allowAdvertisedURL
// skips it), and requests carrying Cloudflare headers are never trusted
// local there either, in case cloudflared is pointed at the wrong port.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"arkstudio/go/internal/auth"
)

type tunnelKey struct{}

// viaTunnel reports whether TunnelHandler admitted r (a context value, so
// no header can claim it).
func viaTunnel(r *http.Request) bool {
	v, _ := r.Context().Value(tunnelKey{}).(bool)
	return v
}

// cloudflareForwarded reports whether r carries headers that Cloudflare's
// edge or cloudflared add; such a request came from the internet.
func cloudflareForwarded(r *http.Request) bool {
	return r.Header.Get("Cf-Connecting-Ip") != "" || r.Header.Get("Cf-Ray") != "" ||
		strings.Contains(strings.ToLower(r.Header.Get("Cdn-Loop")), "cloudflare")
}

// tunnelForwardHeaders are dropped: only Cf-Connecting-Ip is read, and only
// to rate-limit per phone.
var tunnelForwardHeaders = []string{"X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Ssl",
	"X-Forwarded-Host", "X-Forwarded-Prefix", "Forwarded", "X-Real-Ip"}

// TunnelHandler serves the tunnel listener.
func (a *App) TunnelHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client := "tunnel"
		if ip := net.ParseIP(strings.TrimSpace(r.Header.Get("Cf-Connecting-Ip"))); ip != nil {
			client = ip.String()
		}
		for _, h := range tunnelForwardHeaders {
			r.Header.Del(h)
		}
		r.RemoteAddr = net.JoinHostPort(client, "0")
		host := urlHostname(strings.ToLower(r.Host))
		if want := a.tunnelHost(); want == "" || host != want {
			tunnelRefuse(w, http.StatusForbidden, "HOST_REJECTED", "Unexpected Host header")
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), tunnelKey{}, true))
		if limited := a.tunnelLimited(r, client); limited {
			w.Header().Set("Retry-After", "60")
			tunnelRefuse(w, http.StatusTooManyRequests, "RATE_LIMITED", "Too many pairing attempts. Wait a minute and try again.")
			return
		}
		if !a.tunnelAllowed(r) {
			tunnelRefuse(w, http.StatusUnauthorized, "MOBILE_TOKEN_REQUIRED",
				"Pair this phone from ARK Settings → Mobile on the computer first.")
			return
		}
		a.ServeHTTP(w, r)
	})
}

func tunnelRefuse(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Server", ServerHeader)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": map[string]any{ //nolint:errcheck
		"status": status, "code": code, "message": message}})
}

// tunnelPublic are the API paths a phone may use before it is paired.
func tunnelPublic(path, method string, accounts bool) bool {
	read := method == http.MethodGet || method == http.MethodHead
	switch path {
	case "/api/health", "/api/mobile/session", mobileIdentityPath:
		return read
	case "/api/mobile/pair/claim", mobileClaimStatusPath:
		return method == http.MethodPost
	}
	if accounts {
		switch path {
		case "/api/mobile/login", "/login", "/logout", "/api/auth/logout":
			return method == http.MethodPost || read
		case "/api/auth/session":
			return read
		}
	}
	return false
}

func (a *App) tunnelAllowed(r *http.Request) bool {
	path := r.URL.Path
	read := r.Method == http.MethodGet || r.Method == http.MethodHead
	if !strings.HasPrefix(path, "/api/") && path != "/login" && path != "/logout" {
		return read // the web shell, its assets and the app download
	}
	if tunnelPublic(path, r.Method, a.Auth != nil) {
		return true
	}
	token := strings.TrimSpace(r.Header.Get("X-ARK-Mobile-Token"))
	if token == "" && read {
		token = strings.TrimSpace(r.URL.Query().Get("mobile_token"))
	}
	if token == "" {
		token = auth.ParseCookie(r.Header.Get("Cookie"), "ark_mobile")
	}
	if token != "" && a.Mobile.SessionForToken(token) != nil {
		return true
	}
	if a.Auth != nil {
		if sid := auth.ParseCookie(r.Header.Get("Cookie"), auth.CookieName); sid != "" {
			if s, err := a.Auth.Authenticate(r.Context(), sid); err == nil && s != nil {
				return true
			}
		}
	}
	return false
}

// tunnelLimiter caps the unauthenticated endpoints reachable through the
// tunnel: per phone address and for the whole tunnel.
type tunnelLimiter struct {
	mu     sync.Mutex
	recent map[string][]time.Time
}

const (
	tunnelPerClient = 12 // claims, identity and sign-in per address and minute
	tunnelOverall   = 60 // the same for every address together
)

func (a *App) tunnelLimited(r *http.Request, client string) bool {
	switch r.URL.Path {
	case "/api/mobile/pair/claim", mobileIdentityPath, "/api/mobile/login", "/login":
	default:
		return false
	}
	if r.URL.Path == "/login" && r.Method != http.MethodPost {
		return false
	}
	l := &a.cfTunnel.limits
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.recent == nil {
		l.recent = map[string][]time.Time{}
	}
	now := time.Now()
	hit := func(key string, limit int) bool {
		keep := l.recent[key][:0]
		for _, t := range l.recent[key] {
			if now.Sub(t) < time.Minute {
				keep = append(keep, t)
			}
		}
		l.recent[key] = keep
		return len(keep) >= limit
	}
	if hit("*", tunnelOverall) || hit(client, tunnelPerClient) {
		return true
	}
	l.recent["*"] = append(l.recent["*"], now)
	l.recent[client] = append(l.recent[client], now)
	if len(l.recent) > 4096 { // forget idle addresses
		for k, v := range l.recent {
			if k != "*" && (len(v) == 0 || now.Sub(v[len(v)-1]) >= time.Minute) {
				delete(l.recent, k)
			}
		}
	}
	return false
}
