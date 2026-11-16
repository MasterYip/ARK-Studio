// Package assistcfg ports assistant_config.py: the private assistant/LLM
// configuration ($ARK_PRIVATE_DIR/assistant.json, environment fallbacks).
package assistcfg

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pyos"
	"arkstudio/go/internal/pytext"
)

const (
	DefaultBaseURL         = "https://tokenhub.tencentmaas.com/v1"
	DefaultModel           = "deepseek-v4.1-flash-1017"
	DefaultReasoningEffort = "high"
	DefaultTimeout         = int64(90)
)

func strip(s string) string { return pytext.Strip(s) }

// EnvValue is _env_value: os.environ, then (Windows) the registry.
func EnvValue(name string) string {
	if v := strip(os.Getenv(name)); v != "" {
		return v
	}
	return registryEnv(name)
}

// PrivateDir is assistant_config.private_dir().
func PrivateDir() string {
	if v := EnvValue("ARK_PRIVATE_DIR"); v != "" {
		return pyos.ExpandUser(v)
	}
	return filepath.Join(pyos.Home(), ".ark")
}

// ConfigPath is assistant_config.config_path().
func ConfigPath() string { return filepath.Join(PrivateDir(), "assistant.json") }

func fileValues() *pyjson.Object {
	raw, err := os.ReadFile(ConfigPath())
	if err != nil || !utf8.Valid(raw) {
		return pyjson.NewObject()
	}
	v, err := pyjson.Loads(string(raw))
	if o, ok := v.(*pyjson.Object); ok && err == nil {
		return o
	}
	return pyjson.NewObject()
}

// Error is a Python exception raised while reading the configuration
// (ValueError / TypeError / OverflowError of int()).
type Error struct {
	Type, Message string
}

func (e *Error) Error() string { return e.Message }

func orStr(v any, fallback string) string {
	if pyjson.Truthy(v) {
		return pyjson.Str(v)
	}
	return fallback
}

// PyInt is int(v) for a decoded JSON value or a str.
func PyInt(v any) (int64, error) {
	switch x := v.(type) {
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	case int64:
		return x, nil
	case float64:
		if math.IsInf(x, 0) {
			return 0, &Error{"OverflowError", "cannot convert float infinity to integer"}
		}
		if math.IsNaN(x) {
			return 0, &Error{"ValueError", "cannot convert float NaN to integer"}
		}
		return int64(x), nil
	case string:
		n, err := pytext.Int(x)
		if err != nil {
			return 0, &Error{"ValueError", err.Error()}
		}
		return n, nil
	}
	name := "NoneType"
	switch v.(type) {
	case []any:
		name = "list"
	case *pyjson.Object:
		name = "dict"
	}
	if v != nil && name == "NoneType" {
		name = "int"
	}
	return 0, &Error{"TypeError", "int() argument must be a string, a bytes-like object or a real number, not '" + name + "'"}
}

// Load is assistant_config.load().
func Load() (*pyjson.Object, error) {
	fv := fileValues()
	envKey := EnvValue("DEEPSEEK_API_KEY")
	if envKey == "" {
		envKey = EnvValue("ARK_DEEPSEEK_API_KEY")
	}
	apiKey := envKey
	if fv.Has("api_key") {
		apiKey = orStr(fv.Value("api_key"), "")
	}
	base := strings.TrimRight(orStr(fv.Value("base_url"), orStr(EnvValue("DEEPSEEK_BASE_URL"), DefaultBaseURL)), "/")
	model := orStr(fv.Value("model"), orStr(EnvValue("DEEPSEEK_MODEL"), DefaultModel))
	effort := pytext.Lower(orStr(fv.Value("reasoning_effort"), orStr(EnvValue("DEEPSEEK_REASONING_EFFORT"), DefaultReasoningEffort)))
	var timeoutV any = DefaultTimeout
	if t := fv.Value("timeout"); pyjson.Truthy(t) {
		timeoutV = t
	} else if e := EnvValue("DEEPSEEK_TIMEOUT"); e != "" {
		timeoutV = e
	}
	timeout, err := PyInt(timeoutV)
	if err != nil {
		return nil, err
	}
	return pyjson.NewObject("api_key", apiKey, "base_url", base, "model", model,
		"reasoning_effort", effort, "timeout", timeout), nil
}
