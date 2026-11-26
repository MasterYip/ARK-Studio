package server

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// previewGet requests a preview file like the sandboxed frame does: no
// cookie, no token header, the token only in the path.
func previewGet(app *App, target string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", target, nil)
	r.Host = "127.0.0.1:8765"
	r.RemoteAddr = "127.0.0.1:50000"
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	return w
}

func TestHTMLPreviewIsTokenScopedAndSandboxed(t *testing.T) {
	app, team := authDesktopApp(t) // ana and vic: read on Team
	row, ok, err := app.Org.ProjectRow(context.Background(), team)
	if err != nil || !ok {
		t.Fatalf("project row: %v %v", ok, err)
	}
	root := row.Str("path")
	for rel, text := range map[string]string{
		"site/index.html": `<script src="js/app.js"></script>`,
		"site/js/app.js":  "document.body.dataset.ran = '1';",
		"private.txt":     "not for the page",
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ana := sessionCookie(app, "ana")
	ana["X-ARK-Project"] = team

	// A read-only member may open a preview; without a session nobody may.
	r := gpuCall(t, app, "GET", "/api/workspace/preview-token?path=site/index.html", nil, ana)
	token, _ := r.body["token"].(string)
	url, _ := r.body["url"].(string)
	if r.code != 200 || token == "" || url != "/api/workspace/preview/"+token+"/index.html" {
		t.Fatalf("mint: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "GET", "/api/workspace/preview-token?path=site/index.html", nil, map[string]string{"X-ARK-Project": team}); r.code != 401 {
		t.Fatalf("mint without a session: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "GET", "/api/workspace/preview-token?path=private.txt", nil, ana); r.code != 400 {
		t.Fatalf("mint for a non-HTML file: %d %s", r.code, r.raw)
	}

	// The page and its relative assets load with the token alone.
	page := previewGet(app, url, nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "js/app.js") || !strings.HasPrefix(page.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("page: %d %q %s", page.Code, page.Header().Get("Content-Type"), page.Body.String())
	}
	asset := previewGet(app, "/api/workspace/preview/"+token+"/js/app.js", nil)
	if asset.Code != 200 || !strings.Contains(asset.Body.String(), "dataset.ran") {
		t.Fatalf("asset: %d %s", asset.Code, asset.Body.String())
	}
	// Sandboxed even when opened as its own tab; requests limited to the folder.
	csp := page.Header().Get("Content-Security-Policy")
	own := "http://127.0.0.1:*/api/workspace/preview/" + token + "/"
	for _, want := range []string{"sandbox allow-scripts ", "connect-src https://127.0.0.1:*/api/workspace/preview/" + token + "/ " + own + " data: blob:;",
		"form-action https://127.0.0.1:*/api/workspace/preview/" + token + "/ " + own + ";", "frame-ancestors 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP lacks %q: %s", want, csp)
		}
	}
	if strings.Contains(csp, "allow-same-origin") || strings.Contains(csp, "connect-src 'self'") {
		t.Errorf("CSP too open: %s", csp)
	}
	// Readable by the page's own fetch() (opaque origin), never with credentials.
	if page.Header().Get("Access-Control-Allow-Origin") != "*" || page.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Errorf("CORS headers: %v", page.Header())
	}
	if page.Header().Get("Set-Cookie") != "" || page.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("headers: %v", page.Header())
	}

	// Outside the folder, unknown tokens and other routes: refused.
	for _, target := range []string{
		"/api/workspace/preview/" + token + "/../private.txt",
		"/api/workspace/preview/" + token + "/%2e%2e/private.txt",
		"/api/workspace/preview/" + token + "/js/../../private.txt",
		"/api/workspace/preview/not-a-token/index.html",
		"/api/workspace/preview/" + token,
	} {
		if w := previewGet(app, target, nil); w.Code == 200 {
			t.Errorf("%s served: %s", target, w.Body.String())
		}
	}
	// The token is not a session: the API still wants one.
	for _, headers := range []map[string]string{
		{"X-ARK-Project": team},
		{"X-ARK-Project": team, "Authorization": "Bearer " + token},
		{"X-ARK-Project": team, "X-ARK-Read-Token": token},
	} {
		if w := previewGet(app, "/api/workspace/file?path=private.txt", headers); w.Code != 401 {
			t.Errorf("API with %v: %d %s", headers, w.Code, w.Body.String())
		}
	}
	// A session cookie does not widen a preview: still the folder only.
	if w := previewGet(app, "/api/workspace/preview/"+token+"/../private.txt", ana); w.Code == 200 {
		t.Error("cookie widened the preview")
	}

	// Renewal: the same person only.
	if r := gpuCall(t, app, "GET", "/api/workspace/preview-token?renew="+token, nil, ana); r.code != 200 || r.body["token"] != token {
		t.Fatalf("renew: %d %s", r.code, r.raw)
	}
	vic := sessionCookie(app, "vic")
	vic["X-ARK-Project"] = team
	if r := gpuCall(t, app, "GET", "/api/workspace/preview-token?renew="+token, nil, vic); r.code != 404 {
		t.Fatalf("renew by another member: %d %s", r.code, r.raw)
	}
}
