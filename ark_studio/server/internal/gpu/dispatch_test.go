package gpu

import (
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

func TestBuildRequestAgainstCPython(t *testing.T) {
	v := gpuvecLoad(t)
	servers := map[string][]any{}
	for name, text := range v.Dispatch.Servers {
		list, _ := gpuvecLoads(t, text).([]any)
		servers[name] = list
	}
	for _, c := range v.Dispatch.Rows {
		out, err := BuildRequest(gpuvecLoads(t, c.Body), servers[c.Servers], "NOW")
		var got any
		if out != nil {
			got = out
		}
		gpuvecCheck(t, "BuildRequest("+c.Body+", "+c.Servers+")", got, err, c.Out)
	}
	t.Logf("%d build_request vectors", len(v.Dispatch.Rows))
}

func TestDispatchHelpersAgainstCPython(t *testing.T) {
	v := gpuvecLoad(t)
	for _, c := range v.Helpers {
		raw := gpuvecLoads(t, c.JSON)
		cmd, err := DispatchCommand(raw)
		gpuvecCheck(t, "DispatchCommand("+c.JSON+")", cmd, err, c.Command)
		wd, err := DispatchWorkDir(raw)
		gpuvecCheck(t, "DispatchWorkDir("+c.JSON+")", wd, err, c.WorkDir)
		env, err := DispatchEnv(raw)
		var got any
		if env != nil {
			got = env
		}
		gpuvecCheck(t, "DispatchEnv("+c.JSON+")", got, err, c.Env)
		gpus, err := DispatchGPUs(raw)
		got = nil
		if err == nil {
			got = gpus
		}
		gpuvecCheck(t, "DispatchGPUs("+c.JSON+")", got, err, c.GPUs)
	}
	for _, c := range v.Quote {
		if got := ShlexQuote(c.S); got != c.Q {
			t.Errorf("ShlexQuote(%q) = %q; want %q", c.S, got, c.Q)
		}
	}
	for _, c := range v.Join {
		if got := ShlexJoin(c.Args); got != c.Line {
			t.Errorf("ShlexJoin(%q) = %q; want %q", c.Args, got, c.Line)
		}
	}
	for _, c := range v.Job {
		if got := JobRE.MatchString(c.S); got != c.M {
			t.Errorf("JobRE.MatchString(%q) = %v; want %v", c.S, got, c.M)
		}
	}
	t.Logf("%d helper, %d quote, %d join, %d job vectors", len(v.Helpers), len(v.Quote), len(v.Join), len(v.Job))
	_ = pyjson.Dumps
}
