package main

// ark-server agent watch as the preferred watcher (T-0053, 2026-11-21):
// token file, owner lifetime, the strict Codex wake-up conditions, one
// message format.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestHelperSleep is a long-running process for the owner tests.
func TestHelperSleep(t *testing.T) {
	if os.Getenv("ARK_WATCH_HELPER_SLEEP") == "" {
		t.Skip("helper")
	}
	time.Sleep(60 * time.Second)
}

func startSleeper(t *testing.T) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperSleep$")
	cmd.Env = append(os.Environ(), "ARK_WATCH_HELPER_SLEEP=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	return cmd
}

func clearWakeEnv(t *testing.T) {
	for _, k := range []string{"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CODEX_THREAD_ID", "ARK_WAKE", "ARK_CODEX_THREAD",
		"ARK_OWNER_PID", watchDetachedEnv, "ARK_SESSION_ID", "ARK_AGENT_LABEL", "ARK_TOKEN_FILE", "ARK_PROJECT"} {
		t.Setenv(k, "")
	}
}

func projectID(t *testing.T, srvURL string, ctx map[string]any) string {
	p, _ := ctx["project"].(map[string]any)
	id, _ := p["id"].(string)
	if id == "" {
		t.Fatalf("no project id from %s", srvURL)
	}
	return id
}

// The token comes from ~/.ark/tokens/<project>; nothing on the command line.
func TestAgentWatchTokenFile(t *testing.T) {
	clearWakeEnv(t)
	srv, app := watchServer(t)
	pid := projectID(t, srv.URL, watchHTTP(t, srv, app, "GET", "/api/agent/v1/context", "", true))
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("ARK_TOKEN", "")
	if err := os.MkdirAll(filepath.Join(home, ".ark", "tokens"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ark", "tokens", pid), []byte(app.AgentToken()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "cursor")
	var out, errw bytes.Buffer
	if code := agentWatch([]string{"--url", srv.URL, "--project", pid, "--state", state, "--once"}, &out, &errw); code != 0 {
		t.Fatalf("%d %s", code, errw.String())
	}
	errw.Reset()
	if code := agentWatch([]string{"--url", srv.URL, "--project", pid, "--state", state, "--once", "--token", app.AgentToken()}, &out, &errw); code != 0 ||
		!strings.Contains(errw.String(), "--token puts the token on a command line") {
		t.Fatalf("no warning for --token: %d %q", code, errw.String())
	}
}

// The watcher stops when the agent process it belongs to is gone.
func TestAgentWatchStopsWithItsOwner(t *testing.T) {
	clearWakeEnv(t)
	srv, app := watchServer(t)
	t.Setenv("ARK_TOKEN", app.AgentToken())
	state := filepath.Join(t.TempDir(), "cursor")
	var o0, e0 bytes.Buffer
	agentWatch([]string{"--url", srv.URL, "--state", state, "--once"}, &o0, &e0)
	owner := startSleeper(t)
	done := make(chan string, 1)
	go func() {
		var out, errw bytes.Buffer
		agentWatch([]string{"--url", srv.URL, "--state", state, "--until-update", "--wait", "1",
			"--owner-pid", strconv.Itoa(owner.Process.Pid)}, &out, &errw)
		done <- out.String()
	}()
	time.Sleep(1500 * time.Millisecond)
	owner.Process.Kill()
	owner.Wait()
	select {
	case out := <-done:
		if !strings.Contains(out, "is gone; the watcher stops") {
			t.Fatalf("%q", out)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the watcher outlived its owner")
	}
}

// codex queue only under its own Codex session with its own thread id.
func TestAgentWatchCodexWakeConditions(t *testing.T) {
	clearWakeEnv(t)
	me := os.Getpid()
	o := &watchOptions{CodexThread: "th-1"}
	t.Setenv("CODEX_THREAD_ID", "th-1")
	if why := codexWakeBlocker(o, 0); !strings.Contains(why, "owner-pid") {
		t.Fatalf("no owner: %q", why)
	}
	if why := codexWakeBlocker(o, me); !strings.Contains(why, "not Codex") {
		t.Fatalf("owner is this test, not Codex: %q", why)
	}
	t.Setenv("CLAUDECODE", "1")
	if why := codexWakeBlocker(o, me); !strings.Contains(why, "Claude Code") {
		t.Fatalf("Claude Code: %q", why)
	}
	for name, want := range map[string]bool{"codex": true, "codex.exe": true, "codex-linux-san": true, "Codex.EXE": true,
		"claude": false, "node": false, "codexbar": false} {
		if isCodexProcess(name) != want {
			t.Fatalf("isCodexProcess(%q)", name)
		}
	}
	// End to end: an inherited CODEX_THREAD_ID under a non-Codex owner says
	// why it will not queue and exits on news like any watcher.
	t.Setenv("CLAUDECODE", "")
	srv, app := watchServer(t)
	t.Setenv("ARK_TOKEN", app.AgentToken())
	var out, errw bytes.Buffer
	agentWatch([]string{"--url", srv.URL, "--state", filepath.Join(t.TempDir(), "c"), "--until-update", "--max", "1", "--wait", "1",
		"--wake", "codex", "--codex-thread", "th-1", "--owner-pid", strconv.Itoa(me)}, &out, &errw)
	if !strings.Contains(out.String(), "not waking through codex queue (the owner process is not Codex)") {
		t.Fatalf("%q %q", out.String(), errw.String())
	}
}

// Under its own Codex (owner named codex, thread ids match) the detached
// watcher queues one message in the shared format.  Unix: a shell script
// named codex is the owner and the queue.
func TestAgentWatchQueuesIntoItsOwnCodex(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a script named codex as a process")
	}
	clearWakeEnv(t)
	dir := t.TempDir()
	log := filepath.Join(dir, "queue.log")
	codex := filepath.Join(dir, "codex")
	script := "#!/bin/sh\nif [ \"$1\" = queue ]; then [ \"$2\" = --help ] && exit 0; printf '%s\\n' \"$@\" > " + log + "; exit 0; fi\nsleep 60\n"
	if err := os.WriteFile(codex, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	owner := exec.Command(codex)
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { owner.Process.Kill(); owner.Wait() })
	time.Sleep(200 * time.Millisecond)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CODEX_THREAD_ID", "th-mine")
	t.Setenv(watchDetachedEnv, strconv.Itoa(owner.Process.Pid)) // as the detached copy
	srv, app := watchServer(t)
	t.Setenv("ARK_TOKEN", app.AgentToken())
	unit := watchHTTP(t, srv, app, "POST", "/api/agent/v1/units", `{"type":"task","title":"Sweep","status":"review"}`, true)
	uid := unit["unit"].(map[string]any)["uid"].(string)
	pid := projectID(t, srv.URL, watchHTTP(t, srv, app, "GET", "/api/agent/v1/context", "", true))
	state := filepath.Join(dir, "cursor")
	var o0, e0 bytes.Buffer
	agentWatch([]string{"--url", srv.URL, "--project", pid, "--state", state, "--once"}, &o0, &e0)
	done := make(chan string, 1)
	go func() {
		var out, errw bytes.Buffer
		agentWatch([]string{"--url", srv.URL, "--project", pid, "--state", state, "--until-update", "--wait", "5",
			"--wake", "codex", "--codex-thread", "th-mine", "--name", "tester"}, &out, &errw)
		done <- out.String() + errw.String()
	}()
	time.Sleep(500 * time.Millisecond)
	watchHTTP(t, srv, app, "POST", "/api/units/"+uid+"/comments", `{"body":"which seed?"}`, false)
	select {
	case out := <-done:
		b, _ := os.ReadFile(log)
		q := string(b)
		for _, want := range []string{"queue\n--thread\nth-mine\n--message\n", "ARK UPDATE for tester, ARK project Watch CLI (" + pid + "): 1 new update(s)",
			"If you are not working on ARK project Watch CLI (" + pid + "), ignore it"} {
			if !strings.Contains(q, want) {
				t.Fatalf("queued message lacks %q:\n%s\nwatcher: %s", want, q, out)
			}
		}
		if strings.Contains(q, app.AgentToken()) {
			t.Fatal("token in the queued message")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no wake-up")
	}
}
