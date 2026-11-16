package gpu

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

// gpuvecOutcome is one CPython result: ok is json.dumps(value, ensure_ascii=False),
// otherwise type is the exception class and err its str().
type gpuvecOutcome struct {
	OK   *string `json:"ok"`
	Type string  `json:"type"`
	Err  string  `json:"err"`
}

type gpuvecFile struct {
	YAML []struct {
		Src      string         `json:"src"`
		Parse    gpuvecOutcome  `json:"parse"`
		Validate *gpuvecOutcome `json:"validate"`
	} `json:"yaml"`
	Recursion []struct {
		Kind   string        `json:"kind"`
		Levels int           `json:"levels"`
		Parse  gpuvecOutcome `json:"parse"`
	} `json:"recursion"`
	RecursionLimit []struct {
		Kind         string `json:"kind"`
		Stack        int    `json:"stack"`
		FirstFailing int    `json:"first_failing"`
	} `json:"recursion_limit"`
	Validate []struct {
		JSON string        `json:"json"`
		Out  gpuvecOutcome `json:"out"`
	} `json:"validate"`
	Public []struct {
		JSON string `json:"json"`
		Out  string `json:"out"`
	} `json:"public"`
	Load struct {
		Files map[string]string `json:"files"`
		Rows  []struct {
			Path string        `json:"path"`
			Out  gpuvecOutcome `json:"out"`
		} `json:"rows"`
	} `json:"load"`
	Dispatch struct {
		Servers map[string]string `json:"servers"`
		Rows    []struct {
			Body    string        `json:"body"`
			Servers string        `json:"servers"`
			Out     gpuvecOutcome `json:"out"`
		} `json:"rows"`
	} `json:"dispatch"`
	Helpers []struct {
		JSON    string        `json:"json"`
		Command gpuvecOutcome `json:"command"`
		WorkDir gpuvecOutcome `json:"work_dir"`
		Env     gpuvecOutcome `json:"env"`
		GPUs    gpuvecOutcome `json:"gpus"`
	} `json:"helpers"`
	Quote []struct{ S, Q string } `json:"quote"`
	Join  []struct {
		Args []string `json:"args"`
		Line string   `json:"line"`
	} `json:"join"`
	Job []struct {
		S string `json:"s"`
		M bool   `json:"m"`
	} `json:"job"`
}

var (
	gpuvecOnce sync.Once
	gpuvecData gpuvecFile
	gpuvecErr  error
)

func gpuvecLoad(t *testing.T) *gpuvecFile {
	t.Helper()
	gpuvecOnce.Do(func() {
		raw, err := os.ReadFile("testdata/vectors.json")
		if err != nil {
			gpuvecErr = err
			return
		}
		gpuvecErr = json.Unmarshal(raw, &gpuvecData)
	})
	if gpuvecErr != nil {
		t.Fatal(gpuvecErr)
	}
	return &gpuvecData
}

func gpuvecLoads(t *testing.T, text string) any {
	t.Helper()
	v, err := pyjson.Loads(text)
	if err != nil {
		t.Fatalf("pyjson.Loads(%q): %v", text, err)
	}
	return v
}

// gpuvecErrType names the Python exception class a Go error stands for.
func gpuvecErrType(err error) string {
	var ce *ConfigError
	var de *DispatchError
	var oe *OverflowError
	var re *RecursionError
	var pe *PythonError
	switch {
	case errors.As(err, &ce):
		if ce.NotFound {
			return "ConfigNotFound"
		}
		return "ConfigError"
	case errors.As(err, &de):
		return "DispatchError"
	case errors.As(err, &oe):
		return "OverflowError"
	case errors.As(err, &re):
		return "RecursionError"
	case errors.As(err, &pe):
		return pe.Type
	}
	return "ValueError"
}

// check compares a Go result with a CPython gpuvecOutcome.  CPython hangs on a few
// inputs; the port must report a ConfigError there instead.
func gpuvecCheck(t *testing.T, what string, got any, err error, want gpuvecOutcome) {
	t.Helper()
	if want.Type == "Hang" {
		if gpuvecErrType(err) != "ConfigError" {
			t.Errorf("%s: CPython hangs; want a ConfigError, got %v (%v)", what, err, got)
		}
		return
	}
	if want.OK != nil {
		if err != nil {
			t.Errorf("%s: got error %v; want %s", what, err, *want.OK)
			return
		}
		if gotText := string(pyjson.Dumps(got)); gotText != *want.OK {
			t.Errorf("%s:\n got %s\nwant %s", what, gotText, *want.OK)
		}
		return
	}
	if err == nil {
		t.Errorf("%s: got %s; want %s %q", what, pyjson.Dumps(got), want.Type, want.Err)
		return
	}
	if gpuvecErrType(err) != want.Type || (want.Type != "RecursionError" && err.Error() != want.Err) {
		t.Errorf("%s:\n got %s %q\nwant %s %q", what, gpuvecErrType(err), err.Error(), want.Type, want.Err)
	}
}

func TestParseValidateAgainstCPython(t *testing.T) {
	v := gpuvecLoad(t)
	for _, c := range v.YAML {
		parsed, err := ParseYAML(c.Src)
		var got any
		if parsed != nil {
			got = parsed
		}
		gpuvecCheck(t, "ParseYAML("+pyjson.Repr(c.Src)+")", got, err, c.Parse)
		if c.Validate != nil && err == nil {
			out, err := Validate(parsed)
			var got any
			if out != nil {
				got = out
			}
			gpuvecCheck(t, "Validate(ParseYAML("+pyjson.Repr(c.Src)+"))", got, err, *c.Validate)
		}
	}
	for _, c := range v.Validate {
		out, err := Validate(gpuvecLoads(t, c.JSON))
		var got any
		if out != nil {
			got = out
		}
		gpuvecCheck(t, "Validate("+c.JSON+")", got, err, c.Out)
	}
	for _, c := range v.Public {
		got := string(pyjson.Dumps(PublicServer(gpuvecLoads(t, c.JSON).(*pyjson.Object))))
		if got != c.Out {
			t.Errorf("PublicServer(%s) = %s; want %s", c.JSON, got, c.Out)
		}
	}
	t.Logf("%d yaml, %d validate, %d public vectors", len(v.YAML), len(v.Validate), len(v.Public))
}

func TestRecursionCoarse(t *testing.T) {
	v := gpuvecLoad(t)
	for _, c := range v.Recursion {
		var src string
		if c.Kind == "mapping" {
			for i := 0; i < c.Levels; i++ {
				src += strings.Repeat(" ", i) + "k:\n"
			}
			src += strings.Repeat(" ", c.Levels) + "v: 1\n"
		} else {
			src = "a: " + strings.Repeat("[", c.Levels) + "x" + strings.Repeat("]", c.Levels) + "\n"
		}
		parsed, err := ParseYAML(src)
		var got any
		if parsed != nil {
			got = parsed
		}
		gpuvecCheck(t, fmt.Sprintf("%s nesting %d", c.Kind, c.Levels), got, err, c.Parse)
	}
}

func TestRecursionLimitAgainstCPython(t *testing.T) {
	v := gpuvecLoad(t)
	defer func(saved int) { callerFrames = saved }(callerFrames)
	for _, c := range v.RecursionLimit {
		callerFrames = c.Stack
		lo, hi := 1, 2000
		for lo < hi {
			mid := (lo + hi) / 2
			var err error
			if c.Kind == "mapping" {
				var b strings.Builder
				for i := 0; i < mid; i++ {
					b.WriteString(strings.Repeat(" ", i) + "k:\n")
				}
				b.WriteString(strings.Repeat(" ", mid) + "v: 1\n")
				_, err = ParseYAML(b.String())
			} else {
				_, err = ParseYAML("a: " + strings.Repeat("[", mid) + "x" + strings.Repeat("]", mid) + "\n")
			}
			if _, ok := err.(*RecursionError); ok {
				hi = mid
			} else {
				lo = mid + 1
			}
		}
		if lo != c.FirstFailing {
			t.Errorf("%s nesting at Python stack depth %d: first failing level %d; CPython %d", c.Kind, c.Stack, lo, c.FirstFailing)
		}
	}
}

func TestLoadConfigAgainstCPython(t *testing.T) {
	v := gpuvecLoad(t)
	dir := t.TempDir()
	for name, data := range v.Load.Files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		b := make([]byte, 0, len(data))
		for _, r := range data { // latin-1
			b = append(b, byte(r))
		}
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Too large to store: rebuilt exactly as gen_vectors.py writes them.
	exact := []byte(strings.Repeat("#", MaxConfigBytes-20) + "\nenabled: true\n")
	exact = append(exact, strings.Repeat(" ", MaxConfigBytes-len(exact))...)
	for name, data := range map[string][]byte{"big.yaml": []byte(strings.Repeat("#", MaxConfigBytes+1)), "exact.yaml": exact} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)
	dr := strings.ReplaceAll(dir, `\`, `\\`)
	for _, c := range v.Load.Rows {
		path := strings.ReplaceAll(c.Path, "{D}", dir)
		want := c.Out
		want.Err = strings.ReplaceAll(strings.ReplaceAll(want.Err, "{DR}", dr), "{D}", dir)
		out, err := LoadConfig(path)
		var got any
		if out != nil {
			got = out
		}
		gpuvecCheck(t, "LoadConfig("+c.Path+")", got, err, want)
	}
	t.Logf("%d load vectors", len(v.Load.Rows))
}

func TestPurePath(t *testing.T) {
	cases := map[string]string{"": ".", ".": ".", "./a": "a", "a//b/": "a/b", "/": "/", "//x": "//x", "///x": "/x"}
	if cfgWindows {
		cases = map[string]string{"": ".", "./a": "a", "a//b/": `a\b`, "C:/x/": `C:\x`, "C:": "C:", "C:/": `C:\`,
			"/a": `\a`, "//srv/share/x": `\\srv\share\x`, "a/../b": `a\..\b`}
	}
	for in, want := range cases {
		if got := cfgPurePath(in); got != want {
			t.Errorf("purePath(%q) = %q; want %q", in, got, want)
		}
	}
}
