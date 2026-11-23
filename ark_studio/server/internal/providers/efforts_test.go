package providers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

// The launch dialog reads reasoning levels from provider_capabilities: a
// preset without ARK_TASK_*_EFFORTS still gets them from the catalog.
func TestEffortCapabilities(t *testing.T) {
	for _, c := range []struct {
		id      string
		results []discovery
		want    string
		def     string
		source  string
	}{
		{"claude", nil, "low medium high xhigh max", "", "builtin"},
		{"codex", nil, "low medium high", "", "builtin"},
		{"codex", []discovery{
			result("config:~/.codex/config.toml", []*pyjson.Object{entry("gpt-6", "detected", "config", "", nil, "")}, "gpt-6", "high", "", false),
			result("cli:codex debug models", []*pyjson.Object{entry("gpt-6", "detected", "cli", "", []string{"low", "medium", "high", "xhigh"}, "low")}, "", "", "", true),
		}, "low medium high xhigh", "high", "detected"},
		{"dsh", nil, "", "", "unsupported"},
	} {
		preset := pyjson.NewObject("id", c.id, "efforts", []string{})
		caps := capabilities(preset, mergeModels(preset, c.results, nil, "", true))
		if got := strings.Join(stringsOf(caps.Value("efforts")), " "); got != c.want {
			t.Errorf("%s efforts %q, want %q", c.id, got, c.want)
		}
		if caps.Value("efforts_source") != c.source {
			t.Errorf("%s source %v, want %s", c.id, caps.Value("efforts_source"), c.source)
		}
		if pyjson.Str(caps.Value("default_effort")) != c.def {
			t.Errorf("%s default effort %v, want %q", c.id, caps.Value("default_effort"), c.def)
		}
	}
}

func TestPresetDefaults(t *testing.T) {
	t.Setenv("ARK_TASK_CODEX_COMMAND", "")
	t.Setenv("ARK_TASK_CLAUDE_COMMAND", "")
	presets, err := DefaultPresets()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range presets {
		desc := pyjson.Str(p.Value("description"))
		switch p.Value("id") {
		case "codex":
			if cmd := strings.Join(Command(p), " "); !strings.Contains(cmd, "exec --json") {
				t.Errorf("codex command %q", cmd)
			}
		case "claude":
			if strings.Contains(desc, "print mode") {
				t.Errorf("stale claude description %q", desc)
			}
		}
	}
}

func TestAgentFlagOnlyKnownNames(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", "")
	project := filepath.Join(home, "repo", "proj")
	_ = os.MkdirAll(filepath.Join(home, "repo", ".claude", "agents"), 0o755)
	_ = os.MkdirAll(filepath.Join(home, "repo", ".git"), 0o755)
	_ = os.MkdirAll(project, 0o755)
	_ = os.WriteFile(filepath.Join(home, "repo", ".claude", "agents", "tester.md"), []byte("---\nname: tester\ndescription: \"Runs tests\"\n---\n"), 0o644)
	if got := AgentOptions("claude", filepath.Join(home, "nobody"), project); len(got) != 1 || got[0].Scope != "project" || got[0].Description != "Runs tests" {
		t.Fatalf("project agents: %+v", got)
	}
	if f := AgentFlag("claude", "tester", filepath.Join(home, "nobody"), project); strings.Join(f, " ") != "--agent tester" {
		t.Errorf("flag %v", f)
	}
	for _, name := range []string{"", "Claude-1", "tester; rm -rf /", "../x"} {
		if f := AgentFlag("claude", name, filepath.Join(home, "nobody"), project); f != nil {
			t.Errorf("%q: %v", name, f)
		}
	}
}
