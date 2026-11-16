package sessionwatch

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// The differential test runs arkstudio/session_watch.py's run() against the
// same recording fake server and compares requests (raw head + body), log
// lines and exit codes.  Set ARK_PYTHON to a CPython 3.10 interpreter.
//
// zstandard is blocked in the driver: the Go port has no zstd decoder, so it
// mirrors Python's behaviour when no decoder is installed.
const pyDriver = `
import argparse, importlib.util, json, sys
sys.modules["zstandard"] = None
cfg = json.load(open(sys.argv[1], encoding="utf-8"))
spec = importlib.util.spec_from_file_location("session_watch", cfg["module"])
sw = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sw)
ns = argparse.Namespace(**cfg["args"])
logs = []
crash = ""
try:
    code = sw.run(ns, env=cfg["env"], home=cfg["home"] or None, log=logs.append)
except Exception as exc:
    code, crash = 1, type(exc).__name__
print(json.dumps({"code": code, "logs": logs, "crash": crash}))
`

func repoRoot(t *testing.T) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func pyInvoker(t *testing.T, python, work string) func(Options, map[string]string, string) outcome {
	driver := filepath.Join(work, "driver.py")
	if err := os.WriteFile(driver, []byte(pyDriver), 0o644); err != nil {
		t.Fatal(err)
	}
	module := filepath.Join(repoRoot(t), "arkstudio", "session_watch.py")
	return func(o Options, env map[string]string, home string) outcome {
		cfg := map[string]any{
			"module": module, "env": env, "home": home,
			"args": map[string]any{"id": o.ID, "file": o.File, "agent": o.Agent, "server": o.Server,
				"project": o.Project, "token_env": o.TokenEnv, "interval": o.Interval, "title": o.Title, "once": o.Once},
		}
		raw, _ := json.Marshal(cfg)
		cfgPath := filepath.Join(work, "cfg.json")
		if err := os.WriteFile(cfgPath, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(python, "-B", "-I", driver, cfgPath)
		cmd.Env = append(os.Environ(), "NO_PROXY=*", "PYTHONIOENCODING=utf-8")
		stdout, err := cmd.Output()
		if err != nil {
			t.Fatalf("python driver: %v\n%s", err, stdout)
		}
		var out outcome
		if err := json.Unmarshal(stdout, &out); err != nil {
			t.Fatalf("driver output %q: %v", stdout, err)
		}
		return out
	}
}

func TestDifferentialAgainstPython(t *testing.T) {
	python := os.Getenv("ARK_PYTHON")
	if python == "" {
		t.Skip("set ARK_PYTHON to compare with arkstudio/session_watch.py")
	}
	setupNoProxy(t)
	for _, sc := range scenarios() {
		t.Run(sc.name, func(t *testing.T) {
			base := t.TempDir()
			dir := filepath.Join(base, "w")
			srv := newFakeARK(t)
			py := play(t, sc, dir, srv, pyInvoker(t, python, base))
			gol := play(t, sc, dir, srv, goInvoke)
			for i := range py {
				p, g := py[i], gol[i]
				t.Logf("run %d: exit=%d crash=%q reqs=%d logs=%q", i, p.Code, p.Crash, len(p.Reqs), p.Logs)
				if p.Code != g.Code {
					t.Errorf("run %d: exit python=%d go=%d", i, p.Code, g.Code)
				}
				if p.Crash != g.Crash {
					t.Errorf("run %d: crash python=%q go=%q", i, p.Crash, g.Crash)
				}
				if len(p.Logs)+len(g.Logs) > 0 && !reflect.DeepEqual(p.Logs, g.Logs) {
					t.Errorf("run %d: logs\npython: %q\ngo:     %q", i, p.Logs, g.Logs)
				}
				if len(p.Reqs) != len(g.Reqs) {
					t.Errorf("run %d: %d python requests, %d go requests", i, len(p.Reqs), len(g.Reqs))
					for _, r := range p.Reqs {
						t.Logf("python: %q", r.Head)
					}
					for _, r := range g.Reqs {
						t.Logf("go:     %q", r.Head)
					}
					continue
				}
				for j := range p.Reqs {
					if p.Reqs[j] != g.Reqs[j] {
						t.Errorf("run %d request %d differs\npython: %q\n        %q\ngo:     %q\n        %q",
							i, j, p.Reqs[j].Head, p.Reqs[j].Body, g.Reqs[j].Head, g.Reqs[j].Body)
					}
				}
			}
		})
	}
}
