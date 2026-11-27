package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"arkstudio/go/internal/api/desktopgate"
)

// webKitGTKUA is the Linux desktop window's user agent (WebKitGTK).
const webKitGTKUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"

const androidUA = "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Mobile Safari/537.36"

func plainTestApp(t *testing.T, bind string) *App {
	t.Helper()
	root := t.TempDir()
	t.Setenv("ARK_PRIVATE_DIR", filepath.Join(root, "private"))
	app, err := NewApp(Config{Root: filepath.Join(root, "projects"), NoDemo: true,
		SessionsRoot: filepath.Join(root, "sessions")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	app.Bind(bind, 8765)
	return app
}

type sessionReply struct {
	code   int
	raw    string
	body   map[string]any
	header http.Header
}

func callVia(h http.Handler, target, remote, host string, headers map[string]string, cookies ...*http.Cookie) sessionReply {
	r := httptest.NewRequest("GET", target, nil)
	r.RemoteAddr, r.Host = remote, host
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	out := sessionReply{code: w.Code, raw: w.Body.String(), header: w.Header()}
	_ = json.Unmarshal(w.Body.Bytes(), &out.body)
	return out
}

// desktopCookie signs the gate in the way the app's claim URL does.
func desktopCookie(t *testing.T, gate http.Handler) *http.Cookie {
	t.Helper()
	r := httptest.NewRequest("GET", desktopgate.ClaimPath+"?t="+desktopgate.SignClaim("s3cret", time.Now(), "n1")+"&next=/", nil)
	r.RemoteAddr, r.Host = "127.0.0.1:50000", "127.0.0.1:8765"
	w := httptest.NewRecorder()
	gate.ServeHTTP(w, r)
	for _, c := range w.Result().Cookies() {
		if c.Name == "ark_desktop_8765" {
			return c
		}
	}
	t.Fatalf("claim gave no cookie: %d %s", w.Code, w.Body.String())
	return nil
}

// 2026-11-27, Linux desktop with a proxy on: the window's requests to its
// own sidecar went through the proxy, the server saw a non-local client and
// the window showed the phone pairing page (or the "belongs to the desktop
// app" page). The window's cookie makes it the app wherever it seems to
// come from; without it the reason is named; a phone still pairs.
func TestDesktopWindowThroughProxyIsNotAPhone(t *testing.T) {
	app := plainTestApp(t, "127.0.0.1")
	gate := desktopgate.New(app, "s3cret", 8765, app.IssuedToken)
	cookie := desktopCookie(t, gate)
	proxy := map[string]string{"User-Agent": webKitGTKUA, "Via": "1.1 clash", "X-Forwarded-For": "127.0.0.1",
		"Accept-Language": "zh-CN,zh;q=0.9"}
	// The proxy's own address (TUN mode, another proxy host): not loopback.
	const proxyAddr = "198.51.100.4:40000"

	for _, remote := range []string{"127.0.0.1:50000", proxyAddr} {
		r := callVia(gate, "/api/mobile/session", remote, "127.0.0.1:8765", proxy, cookie)
		if r.code != 200 || r.body["required"] != false {
			t.Fatalf("desktop window via %s: %d %.300s", remote, r.code, r.raw)
		}
		if r := callVia(gate, "/api/bootstrap", remote, "127.0.0.1:8765", proxy, cookie); r.code != 200 {
			t.Fatalf("bootstrap via %s: %d %.300s", remote, r.code, r.raw)
		}
		// The first load and a reload: the page and its stylesheet.
		for _, path := range []string{"/", "/css/mobile.css"} {
			if r := callVia(gate, path, remote, "127.0.0.1:8765", proxy, cookie); r.code != 200 {
				t.Fatalf("%s via %s: %d", path, remote, r.code)
			}
		}
	}
	// Without the cookie: the gate names the reason (not the tray hint).
	r := callVia(gate, "/", proxyAddr, "127.0.0.1:8765", proxy)
	if r.code != 401 || !strings.Contains(r.raw, "此地址不在服务范围内") || strings.Contains(r.raw, "手机配对") {
		t.Fatalf("no cookie through a proxy: %d %.400s", r.code, r.raw)
	}
	// A browser on this computer without the app: the tray page.
	r = callVia(gate, "/", "127.0.0.1:50000", "127.0.0.1:8765", map[string]string{"User-Agent": webKitGTKUA})
	if r.code != 401 || !strings.Contains(r.raw, "Open in browser") {
		t.Fatalf("local browser: %d %.400s", r.code, r.raw)
	}
}

// Without the desktop gate (a plain loopback server), a desktop browser
// that seems non-local gets "not_local", never a request for a phone code.
func TestSessionReasonNotLocalForDesktopClients(t *testing.T) {
	app := plainTestApp(t, "127.0.0.1")
	r := callVia(app, "/api/mobile/session", "198.51.100.4:40000", "127.0.0.1:8765",
		map[string]string{"User-Agent": webKitGTKUA, "Via": "1.1 proxy"})
	if r.code != 200 || r.body["required"] != true || r.body["reason"] != "not_local" {
		t.Fatalf("proxied desktop: %d %.300s", r.code, r.raw)
	}
	// Even a phone UA cannot be a phone on a loopback-only server.
	r = callVia(app, "/api/mobile/session", "198.51.100.4:40000", "127.0.0.1:8765", map[string]string{"User-Agent": androidUA})
	if r.body["reason"] != "not_local" {
		t.Fatalf("loopback-only server: %.300s", r.raw)
	}
	// The local window itself is not asked for anything.
	r = callVia(app, "/api/mobile/session", "127.0.0.1:50000", "127.0.0.1:8765", map[string]string{"User-Agent": webKitGTKUA})
	if r.body["required"] != false {
		t.Fatalf("loopback desktop: %.300s", r.raw)
	}
}

// Phones keep the pairing page: through the tunnel, and from a phone
// browser on the LAN.
func TestPhonesStillPair(t *testing.T) {
	app := tunnelTestApp(t)
	r := viaCF(t, app, "GET", "/api/mobile/session", nil, map[string]string{"User-Agent": androidUA})
	if r.code != 200 || r.body["required"] != true || r.body["reason"] != "" {
		t.Fatalf("tunnel phone: %d %.300s", r.code, r.raw)
	}
	if r := viaCF(t, app, "GET", "/api/bootstrap", nil, nil); r.code != 401 {
		t.Fatalf("unpaired phone read data: %d", r.code)
	}

	lan := plainTestApp(t, "0.0.0.0")
	phone := callVia(lan, "/api/mobile/session", "192.168.1.20:40000", "192.168.1.5:8765", map[string]string{"User-Agent": androidUA})
	if phone.code != 200 || phone.body["required"] != true || phone.body["reason"] != "" {
		t.Fatalf("LAN phone: %d %.300s", phone.code, phone.raw)
	}
	laptop := callVia(lan, "/api/mobile/session", "192.168.1.21:40000", "192.168.1.5:8765", map[string]string{"User-Agent": webKitGTKUA})
	if laptop.code != 200 || laptop.body["required"] != true || laptop.body["reason"] != "not_local" {
		t.Fatalf("LAN desktop browser: %d %.300s", laptop.code, laptop.raw)
	}
	if r := callVia(lan, "/api/bootstrap", "192.168.1.21:40000", "192.168.1.5:8765", map[string]string{"User-Agent": webKitGTKUA}); r.code != 401 {
		t.Fatalf("LAN desktop browser read data without pairing: %d", r.code)
	}
}
