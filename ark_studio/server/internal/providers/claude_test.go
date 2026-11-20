package providers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

// fakeHome isolates a test from the real Claude/Codex configuration: a
// temporary HOME, no managed settings and no provider variables.
func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	managed := filepath.Join(t.TempDir(), "managed-settings.json")
	saved := claudeManagedSettings
	claudeManagedSettings = func() []string { return []string{managed} }
	t.Cleanup(func() { claudeManagedSettings = saved })
	for _, name := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN",
		"ANTHROPIC_MODEL", "ANTHROPIC_BASE_URL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL", "ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CONFIG_DIR",
		"OPENAI_API_KEY", "CODEX_API_KEY", "CODEX_HOME", "AZURE_OPENAI_API_KEY"} {
		t.Setenv(name, "")
	}
	return home
}

func managedPath() string { return claudeManagedSettings()[0] }

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func claudeAuth(home string) *pyjson.Object { return (&Catalog{Home: home}).authFor("claude") }

func TestClaudeAuthTokenInSettingsEnv(t *testing.T) {
	home := fakeHome(t)
	writeFile(t, filepath.Join(home, ".claude", "settings.json"),
		`{"model":"claude-sonnet-5-5","env":{"ANTHROPIC_BASE_URL":"https://proxy.example","ANTHROPIC_AUTH_TOKEN":"tok"}}`)
	auth := claudeAuth(home)
	if auth.Value("authenticated") != true || auth.Value("method") != "settings_env" {
		t.Fatalf("token in settings env should count as signed in: %v", string(pyjson.Dumps(auth)))
	}
	if strings.Contains(string(string(pyjson.Dumps(auth))), "tok\"") {
		t.Fatalf("auth record leaks the token: %v", string(pyjson.Dumps(auth)))
	}
}

func TestClaudeAuthAPIKeyHelperAndManaged(t *testing.T) {
	home := fakeHome(t)
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), `{"apiKeyHelper":"/usr/local/bin/key"}`)
	if a := claudeAuth(home); a.Value("method") != "api_key_helper" {
		t.Fatalf("apiKeyHelper: %v", string(pyjson.Dumps(a)))
	}
	home = fakeHome(t)
	writeFile(t, managedPath(), `{"env":{"ANTHROPIC_API_KEY":"k"}}`)
	if a := claudeAuth(home); a.Value("authenticated") != true || a.Value("method") != "settings_env" {
		t.Fatalf("managed settings key: %v", string(pyjson.Dumps(a)))
	}
}

func TestClaudeAuthOAuthCredentials(t *testing.T) {
	home := fakeHome(t)
	writeFile(t, filepath.Join(home, ".claude", ".credentials.json"),
		`{"claudeAiOauth":{"accessToken":"a","refreshToken":"r"}}`)
	if a := claudeAuth(home); a.Value("method") != "credentials_file" {
		t.Fatalf("oauth: %v", string(pyjson.Dumps(a)))
	}
	// CLAUDE_CONFIG_DIR moves both files.
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if a := claudeAuth(home); a.Value("authenticated") != false {
		t.Fatalf("credentials outside CLAUDE_CONFIG_DIR must be ignored: %v", string(pyjson.Dumps(a)))
	}
	writeFile(t, filepath.Join(dir, "settings.json"), `{"env":{"ANTHROPIC_AUTH_TOKEN":"x"}}`)
	if a := claudeAuth(home); a.Value("method") != "settings_env" {
		t.Fatalf("CLAUDE_CONFIG_DIR settings: %v", string(pyjson.Dumps(a)))
	}
}

func TestClaudeAuthEnvAndMissing(t *testing.T) {
	home := fakeHome(t)
	if a := claudeAuth(home); a.Value("authenticated") != false || a.Value("state") != "unauthenticated" {
		t.Fatalf("no credentials: %v", string(pyjson.Dumps(a)))
	}
	// settings without a credential do not count
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), `{"model":"opus","env":{"ANTHROPIC_MODEL":"x"}}`)
	if a := claudeAuth(home); a.Value("authenticated") != false {
		t.Fatalf("model-only settings: %v", string(pyjson.Dumps(a)))
	}
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	if a := claudeAuth(home); a.Value("method") != "environment" {
		t.Fatalf("env key: %v", string(pyjson.Dumps(a)))
	}
}

func optionByID(t *testing.T, merged *pyjson.Object, id string) *pyjson.Object {
	t.Helper()
	for _, x := range merged.Value("options").([]any) {
		if o := x.(*pyjson.Object); o.Value("id") == id {
			return o
		}
	}
	t.Fatalf("option %q missing from %v", id, string(pyjson.Dumps(merged)))
	return nil
}

func claudeMerged(home string) *pyjson.Object {
	preset := pyjson.NewObject("id", "claude", "label", "Claude Code")
	return mergeModels(preset, []discovery{claudeConfigModels(home)}, "now", "", true)
}

func TestClaudeModelPrecedence(t *testing.T) {
	home := fakeHome(t)
	writeFile(t, filepath.Join(home, ".claude", "settings.json"),
		`{"model":"claude-haiku-5-5","env":{"ANTHROPIC_MODEL":"claude-sonnet-5-5","ANTHROPIC_DEFAULT_OPUS_MODEL":"proxy-opus"}}`)
	t.Setenv("ANTHROPIC_MODEL", "claude-fable-5-2")
	merged := claudeMerged(home)
	if merged.Value("default") != "claude-sonnet-5-5" {
		t.Fatalf("settings env ANTHROPIC_MODEL beats process env and settings model: %v", string(pyjson.Dumps(merged)))
	}
	for id, rank := range map[string]string{"claude-sonnet-5-5": "effective", "claude-fable-5-2": "overridden",
		"claude-haiku-5-5": "overridden", "proxy-opus": "alias_target"} {
		o := optionByID(t, merged, id)
		if o.Value("rank") != rank || o.Value("source") != "detected" {
			t.Fatalf("%s: want rank %s, got %v", id, rank, string(pyjson.Dumps(o)))
		}
	}
	if o := optionByID(t, merged, "claude-sonnet-5-5"); o.Value("default") != true || o.Value("origin_key") != "ANTHROPIC_MODEL" ||
		o.Value("origin_file") != "~/.claude/settings.json" {
		t.Fatalf("effective entry: %v", string(pyjson.Dumps(o)))
	}
	assertNoBuiltins(t, merged)
}

func assertNoBuiltins(t *testing.T, merged *pyjson.Object) {
	t.Helper()
	for _, x := range merged.Value("options").([]any) {
		o := x.(*pyjson.Object)
		if o.Value("source") == "builtin" || o.Value("id") == "opus" {
			t.Fatalf("no built-in models expected: %v", string(pyjson.Dumps(merged)))
		}
	}
	if merged.Value("builtin") != false {
		t.Fatalf("builtin flag: %v", string(pyjson.Dumps(merged)))
	}
}

func TestClaudeManagedSettingsWin(t *testing.T) {
	home := fakeHome(t)
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), `{"model":"claude-haiku-5-5","env":{"ANTHROPIC_MODEL":"user-env"}}`)
	writeFile(t, managedPath(), `{"env":{"ANTHROPIC_MODEL":"managed-env"}}`)
	merged := claudeMerged(home)
	if merged.Value("default") != "managed-env" {
		t.Fatalf("managed env first: %v", string(pyjson.Dumps(merged)))
	}
	if o := optionByID(t, merged, "user-env"); o.Value("rank") != "overridden" {
		t.Fatalf("user env shadowed: %v", string(pyjson.Dumps(o)))
	}
	// Settings model only: managed model beats the user model.
	home = fakeHome(t)
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), `{"model":"claude-haiku-5-5"}`)
	writeFile(t, managedPath(), `{"model":"claude-opus-5-5"}`)
	if merged := claudeMerged(home); merged.Value("default") != "claude-opus-5-5" {
		t.Fatalf("managed model first: %v", string(pyjson.Dumps(merged)))
	}
}

func TestClaudeNothingConfiguredListsNothing(t *testing.T) {
	home := fakeHome(t)
	writeFile(t, filepath.Join(home, ".claude", "settings.json"),
		`{"env":{"ANTHROPIC_BASE_URL":"https://api-claude.example.com","ANTHROPIC_AUTH_TOKEN":"t","ANTHROPIC_SMALL_FAST_MODEL":"claude-haiku-5-5"}}`)
	merged := claudeMerged(home)
	if merged.Value("default") != "" {
		t.Fatalf("nothing configured: no default expected: %v", string(pyjson.Dumps(merged)))
	}
	if opts := merged.Value("options").([]any); len(opts) != 1 {
		t.Fatalf("only the alias mapping expected: %v", string(pyjson.Dumps(merged)))
	}
	if o := optionByID(t, merged, "claude-haiku-5-5"); o.Value("rank") != "alias_target" {
		t.Fatalf("small fast model: %v", string(pyjson.Dumps(o)))
	}
	assertNoBuiltins(t, merged)
}

type fakeGet struct {
	urls    []string
	headers [][][2]string
	result  HTTPResult
}

func (f *fakeGet) get(u string, h [][2]string) HTTPResult {
	f.urls, f.headers = append(f.urls, u), append(f.headers, h)
	return f.result
}

func header(h [][2]string, name string) string {
	for _, kv := range h {
		if strings.EqualFold(kv[0], name) {
			return kv[1]
		}
	}
	return ""
}

func TestClaudeEndpointModels(t *testing.T) {
	home := fakeHome(t)
	writeFile(t, filepath.Join(home, ".claude", "settings.json"),
		`{"model":"claude-opus-5-5","env":{"ANTHROPIC_BASE_URL":"https://proxy.example/","ANTHROPIC_AUTH_TOKEN":"tok"}}`)
	body := `{"data":[{"id":"claude-opus-5-5","display_name":"Opus"},{"id":"claude-sonnet-5-5"},{"id":"gemini-4-argon"}]}`
	f := &fakeGet{result: HTTPResult{Status: intp(200), Body: body}}
	api := claudeAPIModels(home, f.get)
	if len(f.urls) != 1 || f.urls[0] != "https://proxy.example/v1/models?limit=1000" {
		t.Fatalf("url: %v", f.urls)
	}
	if header(f.headers[0], "Authorization") != "Bearer tok" || header(f.headers[0], "anthropic-version") == "" {
		t.Fatalf("headers: %v", f.headers[0])
	}
	preset := pyjson.NewObject("id", "claude", "label", "Claude Code")
	merged := mergeModels(preset, []discovery{claudeConfigModels(home), api}, "now", "", true)
	if o := optionByID(t, merged, "claude-opus-5-5"); o.Value("source") != "detected" || o.Value("default") != true {
		t.Fatalf("configured model stays detected and default: %v", string(pyjson.Dumps(o)))
	}
	for _, id := range []string{"claude-sonnet-5-5", "gemini-4-argon"} {
		if o := optionByID(t, merged, id); o.Value("source") != "available" {
			t.Fatalf("%s should be available: %v", id, string(pyjson.Dumps(o)))
		}
	}
	if strings.Contains(string(pyjson.Dumps(merged)), "tok") {
		t.Fatalf("token leaked into the model record")
	}
	// An endpoint without /v1/models adds nothing and reports no error.
	f = &fakeGet{result: HTTPResult{Status: intp(404), Error: "HTTP 404"}}
	merged = mergeModels(preset, []discovery{claudeConfigModels(home), claudeAPIModels(home, f.get)}, "now", "", true)
	if merged.Value("error") != "" || len(merged.Value("options").([]any)) != 1 {
		t.Fatalf("missing endpoint: %v", string(pyjson.Dumps(merged)))
	}
	// API key instead of a token uses x-api-key; no credential means no request.
	home = fakeHome(t)
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), `{"env":{"ANTHROPIC_API_KEY":"k"}}`)
	f = &fakeGet{result: HTTPResult{Status: intp(200), Body: `{"data":[]}`}}
	claudeAPIModels(home, f.get)
	if len(f.urls) != 1 || f.urls[0] != "https://api.anthropic.com/v1/models?limit=1000" || header(f.headers[0], "x-api-key") != "k" {
		t.Fatalf("api key request: %v %v", f.urls, f.headers)
	}
	home = fakeHome(t)
	f = &fakeGet{}
	claudeAPIModels(home, f.get)
	if len(f.urls) != 0 {
		t.Fatalf("no credential: no request expected")
	}
}

func TestCodexProviderAuth(t *testing.T) {
	home := fakeHome(t)
	writeFile(t, filepath.Join(home, ".codex", "config.toml"), "model = \"gpt-5\"\nmodel_provider = \"azure\"\n\n"+
		"[model_providers.azure]\nname = \"Azure\"\nenv_key = \"AZURE_OPENAI_API_KEY\"\n")
	c := &Catalog{Home: home}
	if a := c.authFor("codex"); a.Value("authenticated") != false {
		t.Fatalf("env_key unset: %v", string(pyjson.Dumps(a)))
	}
	t.Setenv("AZURE_OPENAI_API_KEY", "k")
	if a := c.authFor("codex"); a.Value("method") != "config_provider" {
		t.Fatalf("env_key set: %v", string(pyjson.Dumps(a)))
	}
	writeFile(t, filepath.Join(home, ".codex", "config.toml"), "model_provider = \"ollama\"\n")
	if a := c.authFor("codex"); a.Value("authenticated") != true {
		t.Fatalf("local provider: %v", string(pyjson.Dumps(a)))
	}
	// A custom provider with requires_openai_auth uses the OpenAI login in auth.json.
	writeFile(t, filepath.Join(home, ".codex", "config.toml"), "model_provider = \"OpenAI\"\n"+
		"[model_providers.OpenAI]\nbase_url = \"https://proxy.example/v1\"\nrequires_openai_auth = true\n")
	if a := c.authFor("codex"); a.Value("authenticated") != false {
		t.Fatalf("requires_openai_auth without auth.json: %v", string(pyjson.Dumps(a)))
	}
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), `{"OPENAI_API_KEY":"sk-x"}`)
	if a := c.authFor("codex"); a.Value("method") != "credentials_file" {
		t.Fatalf("auth.json key with requires_openai_auth: %v", string(pyjson.Dumps(a)))
	}
	writeFile(t, filepath.Join(home, ".codex", "config.toml"), "model = \"gpt-5\"\n")
	if a := c.authFor("codex"); a.Value("method") != "credentials_file" {
		t.Fatalf("auth.json key: %v", string(pyjson.Dumps(a)))
	}
}
