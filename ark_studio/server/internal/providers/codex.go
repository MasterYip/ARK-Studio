package providers

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"arkstudio/go/internal/compat/pytext"
)

// codexDir is $CODEX_HOME or ~/.codex.
func codexDir(home string) string {
	if dir := pytext.Strip(os.Getenv("CODEX_HOME")); dir != "" {
		return dir
	}
	return filepath.Join(home, ".codex")
}

// codexBuiltinKeyless are Codex's built-in non-OpenAI providers that need no
// API key (local model servers).
var codexBuiltinKeyless = set("oss", "ollama", "lmstudio")

// codexProviderAuth reports a non-OpenAI model_provider selected in Codex's
// config.toml whose credentials are available: its env_key is set in the
// environment, or it needs no key at all.
func codexProviderAuth(home string) (bool, []any, string) {
	path := filepath.Join(codexDir(home), "config.toml")
	state, text := readTextBounded(path)
	if state != "present" {
		return false, nil, ""
	}
	tables := tomlStrings(text)
	name := pytext.Strip(tables[""]["model_provider"])
	if name == "" || name == "openai" {
		return false, nil, ""
	}
	source := fileLabel(path, home)
	spec, defined := tables["model_providers."+name]
	if !defined {
		if codexBuiltinKeyless[name] {
			return true, []any{source}, "Codex uses the local provider \"" + name + "\", which needs no API key."
		}
		return false, nil, ""
	}
	if tomlTrue(text, "model_providers."+name, "requires_openai_auth") {
		// The provider uses the OpenAI login or key from auth.json.
		return false, nil, ""
	}
	key := pytext.Strip(spec["env_key"])
	if key == "" {
		return true, []any{source}, "Codex uses the provider \"" + name + "\" from config.toml, which declares no API key."
	}
	if pytext.Strip(os.Getenv(key)) != "" {
		return true, []any{source, "env:" + key}, "Codex uses the provider \"" + name + "\"; its key " + key + " is set in the environment."
	}
	return false, nil, ""
}

var tomlTrueLine = regexp.MustCompile(`^([A-Za-z0-9_-]+)\s*=\s*true\s*(#.*)?$`)

// tomlTrue reports whether key = true is set in the given table.
func tomlTrue(text, table, key string) bool {
	current := ""
	for _, raw := range pytext.SplitLines(text, false) {
		line := pytext.Strip(raw)
		if header := reMatch(tomlTable, line, 2); header != nil {
			current = strings.NewReplacer("\"", "", "'", "").Replace(pytext.Strip(*header[1]))
			continue
		}
		if m := tomlTrueLine.FindStringSubmatch(line); m != nil && current == table && m[1] == key {
			return true
		}
	}
	return false
}
