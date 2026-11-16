package main

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"arkstudio/go/internal/pyjson"
)

// TestParseVectors replays the argv cases recorded from the Python parser
// (tests/python/contract/gen_cli.py) through the Go parser: exit code, help
// and error texts and the parsed namespace must match exactly.
func TestParseVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/cli_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Env   map[string]string `json:"env"`
		Cases []struct {
			Argv   []string       `json:"argv"`
			Exit   *int           `json:"exit"`
			NS     map[string]any `json:"ns"`
			Stdout string         `json:"stdout"`
			Stderr string         `json:"stderr"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "ARK_") {
			t.Setenv(name, "")
			os.Unsetenv(name)
		}
	}
	for k, v := range file.Env {
		t.Setenv(k, v)
	}
	for _, c := range file.Cases {
		var out, errw bytes.Buffer
		ns, err := parseCommandLine(newParser(), c.Argv, &out, &errw)
		name := strings.Join(c.Argv, " ")
		if strings.Contains(name, "--web-root") {
			// A Go-only flag: Python rejects it, Go accepts it (spelled in full).
			if err != nil || ns.Str("web_root") != "w" {
				t.Errorf("%q: Go-only flag not accepted: %v", name, err)
			}
			continue
		}
		if got, want := out.String(), c.Stdout; got != want {
			t.Errorf("%q stdout:\n%s\nwant:\n%s", name, got, want)
		}
		if got, want := errw.String(), c.Stderr; got != want {
			t.Errorf("%q stderr:\n%s\nwant:\n%s", name, got, want)
		}
		if c.Exit != nil {
			if err == nil || exitCode(err) != *c.Exit {
				t.Errorf("%q: exit %v, want %d", name, err, *c.Exit)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected %v", name, err)
			continue
		}
		got := map[string]any{}
		for k, v := range ns {
			if _, ok := c.NS[k]; !ok {
				switch k {
				case "web_root", "read_pool", "heavy_limit", "max_subprocesses", "max_in_flight", "request_timeout":
					continue // Go-only serve flags
				}
			}
			switch x := v.(type) {
			case int64:
				v = float64(x)
			case []string:
				l := make([]any, len(x))
				for i, s := range x {
					l[i] = s
				}
				v = l
			case float64:
				v = "f:" + pyjson.FloatRepr(x, false)
			case time.Duration:
				continue
			}
			got[k] = portableRoot(v, file.Env["ARK_ROOT"])
		}
		for k, v := range c.NS {
			if l, ok := v.([]any); ok && len(l) == 0 {
				c.NS[k] = []any{}
			}
		}
		if !reflect.DeepEqual(got, c.NS) {
			t.Errorf("%q namespace:\n got %v\nwant %v", name, got, c.NS)
		}
	}
}

// portableRoot maps the native rendering of the ARK_ROOT default
// (str(Path(ARK_ROOT)): rk-root on Windows, /ark-root elsewhere) back to the
// environment spelling the vectors store (gen_cli.py portable). Only exactly
// that value is mapped, so a wrong root default still fails.
func portableRoot(v any, envRoot string) any {
	native := pyPath(envRoot)
	switch x := v.(type) {
	case string:
		if x == native {
			return envRoot
		}
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = portableRoot(item, envRoot)
		}
		return out
	}
	return v
}
