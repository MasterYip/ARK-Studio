// Package archtest enforces the dependency rules of
// docs/development/go-port/architecture.md (section 4):
//
//	cmd/*      -> anything below
//	cli        -> domains, store, platform, compat
//	api        -> domains, platform, compat
//	domain     -> other domains, store, platform, compat
//	store      -> store, platform, compat
//	platform   -> platform, compat
//	compat     -> compat
//
// and no domain or store package imports net/http (outbound HTTP clients
// excepted, see outboundHTTP), api or cli.  Today's violations are listed
// in allowlist.txt; the list may only shrink: a listed edge that no longer
// exists fails the test too, so it is removed in the commit that fixes it.
package archtest

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const module = "arkstudio/go/"

// Layer of a package path relative to the module.
func Layer(pkg string) string {
	p := strings.TrimPrefix(pkg, "internal/")
	first := strings.SplitN(p, "/", 2)[0]
	switch {
	case strings.HasPrefix(pkg, "cmd/"):
		return "cmd"
	case first == "archtest":
		return "test"
	case first == "compat", compatLeaves[first]:
		return "compat"
	case first == "platform", platformLeaves[first]:
		return "platform"
	case first == "store":
		return "store"
	case first == "api", first == "server":
		return "api"
	case first == "cli":
		return "cli"
	}
	return "domain"
}

// Packages that belong to a layer but have not moved under its folder yet
// (they shrink as the restructuring moves them).
var compatLeaves = map[string]bool{
	"pyjson": true, "pyre": true, "pytext": true, "pyurl": true, "pyos": true,
	"pydiff": true, "pyargparse": true,
}

var platformLeaves = map[string]bool{
	"sqlitedb": true, "stream": true, "rnd": true, "blake2b": true, "qr": true,
	"webui": true,
}

// outboundHTTP may import net/http for HTTP clients (model endpoints,
// provider model lists); they never serve requests.
var outboundHTTP = map[string]bool{"internal/chat": true, "internal/providers": true}

var allowed = map[string]map[string]bool{
	"cmd":      {"cmd": true, "cli": true, "api": true, "domain": true, "store": true, "platform": true, "compat": true},
	"cli":      {"cli": true, "domain": true, "store": true, "platform": true, "compat": true},
	"api":      {"api": true, "domain": true, "store": true, "platform": true, "compat": true},
	"domain":   {"domain": true, "store": true, "platform": true, "compat": true},
	"store":    {"store": true, "platform": true, "compat": true},
	"platform": {"platform": true, "compat": true},
	"compat":   {"compat": true},
	"test":     {},
}

// Violations lists the forbidden edges "from -> to" of the module.
func violations(t *testing.T) []string {
	cmd := exec.Command("go", "list", "-f", "{{.ImportPath}}{{range .Imports}} {{.}}{{end}}", "./...")
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	var bad []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		from := strings.TrimPrefix(fields[0], module)
		lf := Layer(from)
		for _, imp := range fields[1:] {
			if imp == "net/http" && (lf == "domain" || lf == "store") && !outboundHTTP[from] {
				bad = append(bad, from+" -> net/http")
				continue
			}
			if !strings.HasPrefix(imp, module) {
				continue
			}
			to := strings.TrimPrefix(imp, module)
			// Handler packages (internal/api/<domain>) build on the api core,
			// never on the server package that registers them.
			if strings.HasPrefix(from, "internal/api/") && to == "internal/server" {
				bad = append(bad, from+" -> "+to)
				continue
			}
			if !allowed[lf][Layer(to)] {
				bad = append(bad, from+" -> "+to)
			}
		}
	}
	sort.Strings(bad)
	return bad
}

func readAllowlist(t *testing.T) map[string]bool {
	f, err := os.Open("allowlist.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			out[line] = true
		}
	}
	return out
}

func TestDependencyRules(t *testing.T) {
	bad := violations(t)
	allow := readAllowlist(t)
	if os.Getenv("ARCHTEST_WRITE") == "1" {
		body := "# Forbidden imports that still exist (architecture.md section 4).\n" +
			"# This list may only shrink.  Regenerate: ARCHTEST_WRITE=1 go test ./internal/archtest\n" +
			strings.Join(bad, "\n") + "\n"
		if err := os.WriteFile("allowlist.txt", []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	seen := map[string]bool{}
	for _, e := range bad {
		seen[e] = true
		if !allow[e] {
			t.Errorf("forbidden import (not in allowlist.txt): %s", e)
		}
	}
	for e := range allow {
		if !seen[e] {
			t.Errorf("allowlist.txt entry no longer violated, remove it: %s", e)
		}
	}
	t.Logf("allowlist: %d edges", len(allow))
}

func TestLayer(t *testing.T) {
	cases := map[string]string{
		"cmd/ark-server":               "cmd",
		"internal/pyjson":              "compat",
		"internal/compat/pyjson":       "compat",
		"internal/platform/clock":      "platform",
		"internal/sqlitedb":            "platform",
		"internal/store":               "store",
		"internal/store/core":          "store",
		"internal/server":              "api",
		"internal/api/units":           "api",
		"internal/cli":                 "cli",
		"internal/mobile":              "domain",
		"internal/sessions/transcript": "domain",
	}
	for pkg, want := range cases {
		if got := Layer(pkg); got != want {
			t.Errorf("Layer(%q) = %q, want %q", pkg, got, want)
		}
	}
}
