package server

import (
	"strings"
	"testing"
)

// T-0025: the user plugin host page gets its own strict CSP (no network, an
// opaque origin even top-level); other pages keep theirs.
func TestPluginFrameCSP(t *testing.T) {
	app := docsApp(t)
	w := docsGet(app, "GET", "/plugin-frame.html", nil)
	if w.Code != 200 {
		t.Fatalf("plugin-frame.html: %d %s", w.Code, w.Body)
	}
	csp := w.Header().Get("Content-Security-Policy")
	if csp != PluginCSP {
		t.Fatalf("plugin-frame.html CSP = %q", csp)
	}
	for _, part := range []string{"sandbox allow-scripts", "connect-src 'none'", "frame-src 'none'", "form-action 'none'", "default-src 'none'"} {
		if !strings.Contains(csp, part) {
			t.Errorf("CSP lacks %q", part)
		}
	}
	if strings.Contains(csp, "unsafe-eval") || strings.Contains(csp, "allow-same-origin") {
		t.Errorf("CSP too loose: %q", csp)
	}
	if got := w.Header().Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Errorf("X-Frame-Options = %q", got)
	}
	if !strings.Contains(w.Body.String(), "ark-plugin-load") {
		t.Errorf("unexpected plugin-frame.html body")
	}
	// The viz frame and the app keep their own policies.
	if got := docsGet(app, "GET", "/viz-frame.html", nil).Header().Get("Content-Security-Policy"); got != VizCSP {
		t.Errorf("viz-frame.html CSP = %q", got)
	}
	// The example plugin is served as a plain page under the app's CSP (its
	// inline script cannot run in ARK's origin).
	ex := docsGet(app, "GET", "/plugins-examples/node-word-count.html", nil)
	if ex.Code != 200 || ex.Header().Get("Content-Security-Policy") != DefaultCSP {
		t.Errorf("example plugin: %d %q", ex.Code, ex.Header().Get("Content-Security-Policy"))
	}
}
