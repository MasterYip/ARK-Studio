package server

import (
	"strings"
	"testing"
)

func TestPortedRoutesExistInContract(t *testing.T) {
	known := map[string]bool{"exact:/api/nope": true} // harness id for "unknown endpoint"
	for _, r := range contractRoutes {
		known[r.ID] = true
	}
	for _, r := range Ported {
		if !known[r.ID] {
			t.Errorf("ported route %q is not in docs/development/go-port/contract.json", r.ID)
		}
	}
}

func TestContractMatch(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
	}{
		{"GET", "/api/units", true},
		{"PATCH", "/api/units/T-0001", true},
		{"GET", "/api/units/T-0001/history/abc", true},
		{"GET", "/api/workspace/tree", true},
		{"GET", "/api/skills/anything/at/all", true}, // dispatcher prefix
		{"GET", "/api/nope", false},
		{"DELETE", "/api/nope", false},
	}
	for _, c := range cases {
		if _, got := contractMatch(c.method, c.path); got != c.want {
			t.Errorf("contractMatch(%s %s) = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}

func TestPythonDollarMatchesBeforeFinalNewline(t *testing.T) {
	m := reUnit.FindStringSubmatch("/api/units/abc\n")
	if m == nil || m[1] != "abc\n" {
		t.Fatalf("got %q", m)
	}
	if m := reUnit.FindStringSubmatch("/api/units/a\nb"); m == nil || m[1] != "a\nb" {
		t.Fatalf("[^/]+ spans newlines like Python: %q", m)
	}
	if !reUnit.MatchString("/api/units/a\n\n") { // Python matches this too
		t.Fatal("must match")
	}
	if reUnit.MatchString("/api/units/a/b") {
		t.Fatal("must not match")
	}
	if m := reResolve.FindStringSubmatch("/api/units/x/comments/１２/resolve"); m == nil || m[2] != "１２" {
		t.Fatalf("Python \\d is Unicode: %q", m)
	}
}

func TestPosixNormpath(t *testing.T) {
	for in, want := range map[string]string{
		"/": "/", "": ".", "//a//b/": "//a/b", "///a": "/a", "/../x": "/x", "a/../../b": "../b",
		"/js/../index.html": "/index.html", "/./css/app.css": "/css/app.css",
	} {
		if got := posixNormpath(in); got != want {
			t.Errorf("posixNormpath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAcceptsGzip(t *testing.T) {
	for in, want := range map[string]bool{
		"gzip": true, "": false, "gzip;q=0": false, "br, *;q=0.5": true, "*;q=0": false,
		"GZIP;q=0.1": true, "gzip;q=2": false, "identity": false,
	} {
		if got := AcceptsGzip(in); got != want {
			t.Errorf("AcceptsGzip(%q) = %v", in, got)
		}
	}
}

func TestCleanAuthNextLookaheadPort(t *testing.T) {
	c := &reqCtx{app: &App{Cfg: Config{}}}
	for in, want := range map[string]string{
		"/x?mobile_token=abc":          "/x",
		"/x?a=1&mobile_token=abc#pair": "/x?a=1#pair",
		"/x?&&":                        "/x",
		"/x?a=1&&b=2":                  "/x?a=1&&b=2",
		"//evil":                       "/",
		"/ok#frag":                     "/ok#frag",
	} {
		if got := c.cleanAuthNext(in); got != want {
			t.Errorf("cleanAuthNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVersioning(t *testing.T) {
	html := VersionedHTML(`<meta charset="utf-8"><script src="./js/app.js"></script><link href="/css/a.css?x">`, "v1")
	want := "<meta charset=\"utf-8\">\n  <meta name=\"ark-build\" content=\"v1\"><script src=\"./js/app.js?v=v1\"></script><link href=\"/css/a.css?x\">"
	if html != want {
		t.Fatalf("got %q", html)
	}
	// Settings › About: the release version, when the build has one.
	page := VersionedHTML(`<meta charset="utf-8"><title>x</title>`, "v1")
	if got := ReleaseMeta(page, "2.1.1"); got != "<meta charset=\"utf-8\">\n  <meta name=\"ark-build\" content=\"v1\">\n  <meta name=\"ark-version\" content=\"2.1.1\"><title>x</title>" {
		t.Fatalf("release meta: got %q", got)
	}
	if got := ReleaseMeta(page, ""); got != page {
		t.Fatalf("no release: got %q", got)
	}
	if got := ReleaseMeta(page, `2.1"><script>`); !strings.Contains(got, `content="2.1script">`) {
		t.Fatalf("release is filtered: got %q", got)
	}
	if got := ReleaseMeta(`<p>no meta</p>`, "2.1.1"); got != `<p>no meta</p>` {
		t.Fatalf("no ark-build meta: got %q", got)
	}
	js := VersionedJS(`import { a } from "./a.js"; const m = import('../b.js'); import "./c.js";`, "v1")
	if js != `import { a } from "./a.js?v=v1"; const m = import('../b.js?v=v1'); import "./c.js?v=v1";` {
		t.Fatalf("got %q", js)
	}
}
