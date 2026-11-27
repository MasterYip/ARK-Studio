package providers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

// geminiHome is fakeHome without Gemini variables.
func geminiHome(t *testing.T) string {
	t.Helper()
	home := fakeHome(t)
	for _, name := range []string{"GEMINI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_GENAI_USE_VERTEXAI", "GOOGLE_CLOUD_PROJECT",
		"GEMINI_CLI_HOME"} {
		t.Setenv(name, "")
	}
	return home
}

func geminiAuth(home string) *pyjson.Object { return (&Catalog{Home: home}).authFor("gemini") }

// geminiRunner answers the probes of a fake gemini binary.
type geminiRunner struct {
	version string
	list    RunResult
	calls   [][]string
}

func (r *geminiRunner) Version(string) RunResult {
	code := 0
	return RunResult{ReturnCode: &code, Stdout: r.version}
}

func (r *geminiRunner) List(argv []string) RunResult {
	r.calls = append(r.calls, argv)
	return r.list
}

func (r *geminiRunner) HTTPGet(string, [][2]string) HTTPResult {
	return HTTPResult{Error: "unreachable"}
}

const geminiListJSON = `Warning: 256-color support not detected.
[
  {"modelId": "auto", "id": "auto", "name": "Auto", "displayName": "Auto",
   "description": "Let Gemini CLI decide the best model for the task: gemini-2.5-pro, gemini-3.5-flash"},
  {"modelId": "gemini-2.5-pro", "id": "gemini-2.5-pro", "name": "gemini-2.5-pro", "displayName": "gemini-2.5-pro", "description": ""},
  {"modelId": "gemini-3.5-flash", "id": "gemini-3.5-flash", "name": "gemini-3.5-flash", "displayName": "gemini-3.5-flash", "description": ""}
]`

func TestGeminiAuth(t *testing.T) {
	home := geminiHome(t)
	if a := geminiAuth(home); a.Value("authenticated") != false || !strings.Contains(strOr(a.Value("detail")), "GEMINI_API_KEY") {
		t.Fatalf("nothing set: %v", string(pyjson.Dumps(a)))
	}
	writeFile(t, filepath.Join(home, ".gemini", "oauth_creds.json"), `{"refresh_token":"r"}`)
	if a := geminiAuth(home); a.Value("method") != "credentials_file" {
		t.Fatalf("login cache: %v", string(pyjson.Dumps(a)))
	}
	_ = os.Remove(filepath.Join(home, ".gemini", "oauth_creds.json"))
	writeFile(t, filepath.Join(home, ".gemini", ".env"), "# keys\nexport GEMINI_API_KEY=\"AIzaTestValue\"\n")
	a := geminiAuth(home)
	if a.Value("method") != "env_file" || strings.Contains(string(pyjson.Dumps(a)), "AIzaTestValue") {
		t.Fatalf(".env key: %v", string(pyjson.Dumps(a)))
	}
	writeFile(t, filepath.Join(home, ".gemini", ".env"), "GEMINI_API_KEY=\n")
	if a := geminiAuth(home); a.Value("authenticated") != false {
		t.Fatalf("empty .env key: %v", string(pyjson.Dumps(a)))
	}
	writeFile(t, filepath.Join(home, ".gemini", "settings.json"), `{"security":{"auth":{"selectedType":"vertex-ai"}}}`)
	if a := geminiAuth(home); a.Value("method") != "settings" || a.Value("detail") != "Gemini CLI signs in with vertex-ai (settings.json)." {
		t.Fatalf("settings: %v", string(pyjson.Dumps(a)))
	}
	t.Setenv("GOOGLE_GENAI_USE_VERTEXAI", "true")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "my-project")
	if a := geminiAuth(home); a.Value("method") != "vertex_ai" {
		t.Fatalf("vertex: %v", string(pyjson.Dumps(a)))
	}
	t.Setenv("GEMINI_API_KEY", "x")
	if a := geminiAuth(home); a.Value("method") != "environment" {
		t.Fatalf("env key: %v", string(pyjson.Dumps(a)))
	}
	// GEMINI_CLI_HOME moves the folder.
	other := t.TempDir()
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_GENAI_USE_VERTEXAI", "")
	t.Setenv("GEMINI_CLI_HOME", other)
	writeFile(t, filepath.Join(other, ".gemini", "oauth_creds.json"), `{"access_token":"a"}`)
	if a := geminiAuth(home); a.Value("method") != "credentials_file" {
		t.Fatalf("GEMINI_CLI_HOME: %v", string(pyjson.Dumps(a)))
	}
}

func TestGeminiCLIModels(t *testing.T) {
	code := 0
	r := &geminiRunner{list: RunResult{ReturnCode: &code, Stdout: geminiListJSON}}
	d := geminiCLIModels("gemini", r.List)
	if d.err != "" || !d.fullList || d.def != "auto" || len(d.models) != 3 {
		t.Fatalf("list: %+v", d)
	}
	if got := strings.Join(r.calls[0][1:], " "); got != "--list-models --output-format json" {
		t.Fatalf("argv %q", got)
	}
	if d.models[0].Value("label") == "auto" || d.models[1].Value("id") != "gemini-2.5-pro" {
		t.Fatalf("models: %v", d.models)
	}
	// An older Gemini CLI without the list: an error, then the aliases.
	bad := 1
	old := geminiCLIModels("gemini", (&geminiRunner{list: RunResult{ReturnCode: &bad, Stderr: "Unknown argument: list-models"}}).List)
	if old.err == "" || len(old.models) != 0 {
		t.Fatalf("old version: %+v", old)
	}
	if junk := geminiCLIModels("gemini", (&geminiRunner{list: RunResult{ReturnCode: &code, Stdout: "not json"}}).List); junk.err == "" {
		t.Fatalf("junk: %+v", junk)
	}
}

func TestGeminiProbe(t *testing.T) {
	home := geminiHome(t)
	exe, _ := os.Executable()
	code := 0
	r := &geminiRunner{version: "0.71.1\n", list: RunResult{ReturnCode: &code, Stdout: geminiListJSON}}
	c := &Catalog{Home: home, Runner: r, remote: map[string]discovery{}}
	// Installed, signed in with Google: ready, version, the CLI's models.
	writeFile(t, filepath.Join(home, ".gemini", "oauth_creds.json"), `{"refresh_token":"r"}`)
	writeFile(t, filepath.Join(home, ".gemini", "settings.json"), `{"model":{"name":"gemini-3.5-flash"}}`)
	got := c.probeOne(pyjson.NewObject("id", "gemini", "label", "Gemini CLI", "command", []any{exe, "-p", "{prompt}"}))
	if got.Value("status") != "ready" || got.Value("version") != "0.71.1" || got.Value("installed") != true {
		t.Fatalf("installed: %v", string(pyjson.Dumps(got)))
	}
	caps := got.Value("capabilities").(*pyjson.Object)
	if caps.Value("default_model") != "gemini-3.5-flash" || len(stringsOf(caps.Value("efforts"))) != 0 {
		t.Fatalf("capabilities: %v", string(pyjson.Dumps(caps)))
	}
	if !strings.Contains(string(pyjson.Dumps(caps)), "gemini-2.5-pro") {
		t.Fatalf("CLI models missing: %v", string(pyjson.Dumps(caps)))
	}
	// The list is cached until an explicit refresh.
	c.probeOne(pyjson.NewObject("id", "gemini", "command", []any{exe, "-p", "{prompt}"}))
	if len(r.calls) != 1 {
		t.Fatalf("list ran %d times", len(r.calls))
	}
	// No list (older CLI): Gemini's aliases.
	bad := 2
	c2 := &Catalog{Home: home, Runner: &geminiRunner{version: "0.9.0", list: RunResult{ReturnCode: &bad}}, remote: map[string]discovery{}}
	caps = c2.probeOne(pyjson.NewObject("id", "gemini", "command", []any{exe, "-p", "{prompt}"})).Value("capabilities").(*pyjson.Object)
	for _, alias := range []string{`"pro"`, `"flash"`, `"flash-lite"`, `"auto"`} {
		if !strings.Contains(string(pyjson.Dumps(caps)), alias) {
			t.Fatalf("alias %s missing: %v", alias, string(pyjson.Dumps(caps)))
		}
	}
	// Not installed.
	missing := c.probeOne(pyjson.NewObject("id", "gemini", "command", []any{"gemini-cli-not-installed-x", "-p", "{prompt}"}))
	if missing.Value("status") != "missing" || missing.Value("installed") != false {
		t.Fatalf("missing: %v", string(pyjson.Dumps(missing)))
	}
}

func strOr(v any) string {
	s, _ := v.(string)
	return s
}
