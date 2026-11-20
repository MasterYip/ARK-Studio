package providers

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
)

// Claude Code reads its configuration from several settings files. ARK
// mirrors the parts that decide whether a run can authenticate and which
// model it uses:
//
//   - managed settings (policy file, highest precedence among settings files)
//   - user settings: ~/.claude/settings.json ($CLAUDE_CONFIG_DIR/settings.json)
//
// Project settings (.claude/settings.json, then .claude/settings.local.json
// above it, in the run's working directory) depend on the session workspace
// and are not part of the host-wide catalog.
//
// Model precedence, highest first, verified with Claude Code 2.1.328 by
// reading the model of the stream-json init event under a throwaway HOME:
//
//  1. --model (ARK passes it when a model is picked in the run panel, so a
//     picked model beats ANTHROPIC_MODEL in a settings env block)
//  2. ANTHROPIC_MODEL; a settings "env" block overrides the process
//     environment (managed over user)
//  3. the settings "model" key (managed, local project, project, user)
//
// ARK lists only configured models (no built-in list): the sources above,
// the alias mappings ANTHROPIC_DEFAULT_*_MODEL and ANTHROPIC_SMALL_FAST_MODEL,
// and, when the configured endpoint answers GET /v1/models, its model list.

// claudeManagedSettings lists the managed-settings files per platform; a
// variable so tests can point it at a temporary file.
var claudeManagedSettings = func() []string {
	switch runtime.GOOS {
	case "windows":
		return []string{`C:\Program Files\ClaudeCode\managed-settings.json`, `C:\ProgramData\ClaudeCode\managed-settings.json`}
	case "darwin":
		return []string{"/Library/Application Support/ClaudeCode/managed-settings.json"}
	}
	return []string{"/etc/claude-code/managed-settings.json"}
}

// claudeAuthKeys are the settings/env credentials Claude Code accepts in
// place of an OAuth login.
var claudeAuthKeys = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"}

// claudeCloudKeys switch Claude Code to a cloud provider whose credentials
// come from that provider's own configuration.
var claudeCloudKeys = []string{"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"}

// claudeAliasEnv are the variables that map Claude Code's model aliases.
var claudeAliasEnv = []string{"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL", "ANTHROPIC_SMALL_FAST_MODEL"}

// claudeLayer is one parsed settings file.
type claudeLayer struct {
	scope string // "managed" or "user"
	label string // display path, e.g. ~/.claude/settings.json
	state fileState
	data  *pyjson.Object
	env   *pyjson.Object
}

func claudeConfigDir(home string) string {
	if dir := pytext.Strip(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		return dir
	}
	return filepath.Join(home, ".claude")
}

func claudeLabel(path, home string) string {
	_, rest, _ := strings.Cut(fileLabel(path, home), ":")
	if !strings.HasPrefix(rest, "~/") {
		return filepath.ToSlash(path)
	}
	return rest
}

// claudeLayers returns the settings files in precedence order (highest
// first). Missing files are omitted.
func claudeLayers(home string) []claudeLayer {
	var out []claudeLayer
	add := func(scope, path string) {
		state, raw := readJSONConfig(path)
		if state == "missing" {
			return
		}
		data, ok := asObject(raw)
		if !ok {
			data = pyjson.NewObject()
		}
		env, ok := asObject(data.Value("env"))
		if !ok {
			env = pyjson.NewObject()
		}
		out = append(out, claudeLayer{scope, claudeLabel(path, home), state, data, env})
	}
	for _, path := range claudeManagedSettings() {
		add("managed", path)
	}
	add("user", filepath.Join(claudeConfigDir(home), "settings.json"))
	return out
}

// claudeEnv resolves one environment variable as a Claude Code child sees
// it: the highest settings "env" block that sets it, else the process
// environment. It returns the value and where it came from.
func claudeEnv(layers []claudeLayer, name string) (string, string) {
	for _, l := range layers {
		if v := strStrip(l.env.Value(name)); v != "" {
			return v, name + " in " + l.label
		}
	}
	if v := pytext.Strip(os.Getenv(name)); v != "" {
		return v, name + " (environment)"
	}
	return "", ""
}

// claudeSettingsAuth reports credentials configured in Claude Code settings
// files: an API key or token in an "env" block, an apiKeyHelper, or a cloud
// provider switch. The second value is the auth method, the third the source
// labels.
func claudeSettingsAuth(layers []claudeLayer) (bool, string, []any, string) {
	for _, l := range layers {
		for _, name := range claudeAuthKeys {
			if strStrip(l.env.Value(name)) != "" {
				return true, "settings_env", []any{"settings:" + l.label + ":" + name},
					"Claude Code credential " + name + " found in " + l.label + "."
			}
		}
		if strStrip(l.data.Value("apiKeyHelper")) != "" {
			return true, "api_key_helper", []any{"settings:" + l.label + ":apiKeyHelper"},
				"Claude Code apiKeyHelper is configured in " + l.label + "."
		}
		for _, name := range claudeCloudKeys {
			if v := pytext.Lower(strStrip(l.env.Value(name))); v != "" && v != "0" && v != "false" {
				return true, "cloud_provider", []any{"settings:" + l.label + ":" + name},
					"Claude Code uses a cloud provider (" + name + " in " + l.label + "); its credentials are not checked."
			}
		}
	}
	return false, "", nil, ""
}

// claudeAPIModels asks the configured endpoint (ANTHROPIC_BASE_URL, default
// api.anthropic.com) for its model list with the configured key or token.
// It runs server-side only; the credential never leaves the host. Errors
// are dropped: an endpoint without /v1/models simply adds nothing.
func claudeAPIModels(home string, get func(string, [][2]string) HTTPResult) discovery {
	method := "api:GET /v1/models"
	layers := claudeLayers(home)
	headers := [][2]string{{"Accept", "application/json"}, {"anthropic-version", "2023-06-01"}}
	if token, _ := claudeEnv(layers, "ANTHROPIC_AUTH_TOKEN"); token != "" {
		headers = append(headers, [2]string{"Authorization", "Bearer " + token})
	} else if key, _ := claudeEnv(layers, "ANTHROPIC_API_KEY"); key != "" {
		headers = append(headers, [2]string{"x-api-key", key})
	} else {
		return result(method, nil, "", "", "", false)
	}
	base, _ := claudeEnv(layers, "ANTHROPIC_BASE_URL")
	if base == "" {
		base = "https://api.anthropic.com"
	}
	d := openAICompatibleModels(strings.TrimRight(base, "/")+"/v1", "", func(u string, h [][2]string) HTTPResult {
		return get(u+"?limit=1000", append(h, headers[1:]...))
	})
	d.method, d.err, d.fullList = method, "", false
	for _, m := range d.models {
		m.Set("source", "available").Set("via", "api")
	}
	return d
}

// claudeConfigModels reads the configured Claude Code models in precedence
// order and marks the one a run uses when ARK passes no --model.
func claudeConfigModels(home string) discovery {
	layers := claudeLayers(home)
	method := "config:~/.claude/settings.json"
	if dir := pytext.Strip(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		method = "config:" + claudeLabel(filepath.Join(dir, "settings.json"), home)
	}
	var methods []string
	errText := ""
	for _, l := range layers {
		if l.scope == "managed" {
			methods = append(methods, "config:"+l.label)
		}
		if l.state == "unreadable" || l.state == "malformed" {
			errText = filepath.Base(l.label) + " is " + string(l.state)
			if l.scope == "managed" {
				errText = "managed " + errText
			}
		}
	}
	type candidate struct{ id, source, key, file string }
	var ranked []candidate
	addCandidate := func(value, key, file string) {
		source := key + " (environment)"
		if file != "" {
			source = key + " in " + file
		}
		if id := CleanModelID(value); id != "" {
			for _, c := range ranked {
				if c.source == source {
					return
				}
			}
			ranked = append(ranked, candidate{id, source, key, file})
		}
	}
	// ANTHROPIC_MODEL: the effective value first, then the values it shadows.
	for _, l := range layers {
		addCandidate(strStrip(l.env.Value("ANTHROPIC_MODEL")), "ANTHROPIC_MODEL", l.label)
	}
	addCandidate(pytext.Strip(os.Getenv("ANTHROPIC_MODEL")), "ANTHROPIC_MODEL", "")
	for _, l := range layers {
		addCandidate(strStrip(l.data.Value("model")), "model", l.label)
	}
	effectiveCount := len(ranked)
	for _, name := range claudeAliasEnv {
		found := false
		for _, l := range layers {
			if v := strStrip(l.env.Value(name)); v != "" && !found {
				found = true
				addCandidate(v, name, l.label)
			}
		}
		if v := pytext.Strip(os.Getenv(name)); v != "" && !found {
			addCandidate(v, name, "")
		}
	}
	def := ""
	var models []*pyjson.Object
	for i, c := range ranked {
		if hasModel(models, c.id) {
			continue
		}
		rank := "overridden"
		switch {
		case i >= effectiveCount:
			rank = "alias_target"
		case i == 0:
			rank, def = "effective", c.id
		}
		item := entry(c.id, "detected", "config", c.id+" ("+c.source+" · "+strings.ReplaceAll(rank, "_", " ")+")", nil, "")
		item.Set("origin_key", c.key).Set("origin_file", c.file).Set("rank", rank)
		models = append(models, item)
	}
	effort := ""
	for _, l := range layers {
		if e := cleanEfforts([]any{l.data.Value("effortLevel")}); len(e) > 0 {
			effort = e[0]
			break
		}
	}
	d := result(method, models, def, effort, errText, false)
	d.extraMethods = methods
	return d
}
