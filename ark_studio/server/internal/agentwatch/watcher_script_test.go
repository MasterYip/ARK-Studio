package agentwatch

// watcher.sh safety (T-0053, 2026-11-20): an inherited CODEX_THREAD_ID must
// never make the watcher queue into a Codex session that did not start it,
// the token never appears in a process's arguments, and the watcher stops
// when its agent is gone.  Linux only (sh, curl, setsid, /proc).

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "arka_WATCHERTESTTOKEN0123456789abcdef"

type fakeArk struct {
	*httptest.Server
	mu    sync.Mutex
	auths []string
	hold  time.Duration // how long an updates call with a cursor waits
	news  bool          // whether it then returns an update
}

func newFakeArk(t *testing.T, hold time.Duration, news bool) *fakeArk {
	f := &fakeArk{hold: hold, news: news}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/agent/v1/watcher.sh":
			w.Write(WatcherSh)
			return
		}
		f.mu.Lock()
		f.auths = append(f.auths, r.Header.Get("Authorization")+"|"+r.Header.Get("X-ARK-Project"))
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(401)
			w.Write([]byte(`{"ok": false}`))
			return
		}
		switch r.URL.Path {
		case "/api/agent/v1/context":
			w.Write([]byte(`{"ok": true, "project": {"id": "prj_t", "name": "Test Proj"}}`))
		case "/api/agent/v1/updates":
			if r.URL.Query().Get("since") == "" {
				w.Write([]byte(`{"ok": true, "cursor": "c1", "items": [], "count": 0, "more": false, "rules_version": "r-aa"}`))
				return
			}
			time.Sleep(f.hold)
			if f.news {
				w.Write([]byte(`{"ok": true, "cursor": "c2", "items": [{"kind": "comment", "summary": "Ann commented on T-0001: \"which seed?\""}], "count": 1, "more": false, "rules_version": "r-aa"}`))
			} else {
				w.Write([]byte(`{"ok": true, "cursor": "c1", "items": [], "count": 0, "more": false, "rules_version": "r-aa"}`))
			}
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeArk) authsSeen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.auths...)
}

type watcherRig struct {
	dir, script, queueLog, stop string
	env                         []string
}

// newRig writes the watcher, a fake `codex` (records `queue` calls; `codex
// run CMD` runs CMD as its child and stays until the stop file exists) and
// the token file.
func newRig(t *testing.T, f *fakeArk) *watcherRig {
	if runtime.GOOS != "linux" {
		t.Skip("watcher.sh tests need Linux (/proc)")
	}
	for _, tool := range []string{"sh", "curl", "setsid"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not found")
		}
	}
	dir := t.TempDir()
	rg := &watcherRig{dir: dir, script: filepath.Join(dir, "watcher.sh"),
		queueLog: filepath.Join(dir, "queue"), stop: filepath.Join(dir, "stop")}
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(rg.script, WatcherSh, 0o644))
	bin := filepath.Join(dir, "bin")
	must(os.MkdirAll(bin, 0o755))
	must(os.WriteFile(filepath.Join(bin, "codex"), []byte(`#!/bin/sh
if [ "$1" = queue ]; then
  [ "$2" = --help ] && exit 0
  printf '%s\n' "$@" > "`+rg.queueLog+`.$$"
  exit 0
fi
if [ "$1" = run ]; then
  sh -c "$2"
  while [ ! -f "`+rg.stop+`" ]; do sleep 0.2; done
  exit 0
fi
exit 1
`), 0o755))
	tokDir := filepath.Join(dir, "home", ".ark", "tokens")
	must(os.MkdirAll(tokDir, 0o700))
	must(os.WriteFile(filepath.Join(tokDir, "prj_t"), []byte(testToken+"\n"), 0o600))
	rg.env = []string{"PATH=" + bin + ":/usr/local/bin:/usr/bin:/bin", "HOME=" + filepath.Join(dir, "home"),
		"ARK_URL=" + f.URL, "ARK_PROJECT=prj_t", "ARK_WATCH_STATE=" + filepath.Join(dir, "state"),
		"ARK_WATCH_MAX=20", "ARK_AGENT_LABEL=tester"}
	t.Cleanup(func() { os.WriteFile(rg.stop, nil, 0o644) })
	return rg
}

func (rg *watcherRig) queued() []string {
	m, _ := filepath.Glob(rg.queueLog + ".*")
	var out []string
	for _, f := range m {
		b, _ := os.ReadFile(f)
		out = append(out, string(b))
	}
	return out
}

func runWatcher(t *testing.T, rg *watcherRig, extra ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("sh", rg.script)
	cmd.Dir = rg.dir
	cmd.Env = append(append([]string{}, rg.env...), extra...)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

// The incident: a Claude Code session whose environment carried the thread
// id of an unrelated Codex session.  Nothing may be queued, whatever is set.
func TestWatcherInheritedCodexThreadNeverQueues(t *testing.T) {
	f := newFakeArk(t, 0, true)
	rg := newRig(t, f)
	cases := [][]string{
		{"CODEX_THREAD_ID=th-other", "CLAUDECODE=1"},
		{"CODEX_THREAD_ID=th-other"},
		{"CODEX_THREAD_ID=th-other", "CLAUDECODE=1", "ARK_WAKE=codex", "ARK_CODEX_THREAD=th-other"},
		{"CODEX_THREAD_ID=th-other", "ARK_WAKE=codex", "ARK_CODEX_THREAD=th-other"}, // no Codex above
	}
	for i, extra := range cases {
		os.Remove(rg.env[4][len("ARK_WATCH_STATE="):])
		out, code := runWatcher(t, rg, extra...)
		if q := rg.queued(); len(q) != 0 {
			t.Fatalf("case %d queued into a foreign Codex session: %q", i, q)
		}
		// Asked to wake Codex and cannot: say why and fail (T-0109).
		if strings.Contains(strings.Join(extra, " "), "ARK_WAKE") {
			if code != 2 || !strings.Contains(out, "ARK: cannot wake codex: ") {
				t.Fatalf("case %d: exit %d, does not say why it will not queue:\n%s", i, code, out)
			}
			continue
		}
		if code != 0 || !strings.Contains(out, "ARK UPDATE for tester, ARK project Test Proj (prj_t)") {
			t.Fatalf("case %d: exit %d\n%s", i, code, out)
		}
	}
}

// Under its own Codex session, with its own thread id, the watcher detaches
// and queues one message that names the project and the agent.
func TestWatcherQueuesOnlyIntoItsOwnCodex(t *testing.T) {
	f := newFakeArk(t, 200*time.Millisecond, true)
	rg := newRig(t, f)
	cmd := exec.Command(filepath.Join(rg.dir, "bin", "codex"), "run", "sh "+rg.script)
	cmd.Dir = rg.dir
	cmd.Env = append(append([]string{}, rg.env...), "CODEX_THREAD_ID=th-mine", "ARK_WAKE=codex", "ARK_CODEX_THREAD=th-mine")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { os.WriteFile(rg.stop, nil, 0o644); cmd.Wait() }()
	deadline := time.Now().Add(15 * time.Second)
	for len(rg.queued()) == 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	q := rg.queued()
	if len(q) != 1 {
		t.Fatalf("queued %d messages", len(q))
	}
	for _, want := range []string{"queue\n--thread\nth-mine\n--message\n", "ARK UPDATE for tester, ARK project Test Proj (prj_t)",
		"Ann commented on T-0001: 'which seed?'", "If you are not working on ARK project Test Proj (prj_t), ignore it"} {
		if !strings.Contains(q[0], want) {
			t.Fatalf("queued message lacks %q:\n%s", want, q[0])
		}
	}
	if strings.Contains(q[0], testToken) {
		t.Fatal("the token is in the queued message")
	}
	// A thread id that is not the one Codex gave the command: no queue.
	os.Remove(rg.env[4][len("ARK_WATCH_STATE="):])
	for _, m := range mustGlob(t, rg.queueLog+".*") {
		os.Remove(m)
	}
	out, code := runWatcher(t, rg, "CODEX_THREAD_ID=th-mine", "ARK_WAKE=codex", "ARK_CODEX_THREAD=th-forged")
	if len(rg.queued()) != 0 || code != 2 || !strings.Contains(out, "cannot wake codex") {
		t.Fatalf("forged thread id: %q\n%s", rg.queued(), out)
	}
}

func mustGlob(t *testing.T, pattern string) []string {
	m, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// While the watcher waits, no process on the machine has the token in its
// arguments; it reaches ARK in the header, from the file or the environment.
func TestWatcherTokenNeverInArguments(t *testing.T) {
	for _, source := range []string{"file", "env"} {
		t.Run(source, func(t *testing.T) {
			f := newFakeArk(t, 2*time.Second, true)
			rg := newRig(t, f)
			var extra []string
			if source == "env" {
				os.RemoveAll(filepath.Join(rg.dir, "home", ".ark"))
				extra = []string{"ARK_TOKEN=" + testToken}
			}
			done := make(chan string, 1)
			go func() { out, _ := runWatcher(t, rg, extra...); done <- out }()
			time.Sleep(1200 * time.Millisecond) // inside the held long poll
			if pid, args := processWithArg(testToken); pid != "" {
				t.Fatalf("token on the command line of pid %s: %s", pid, args)
			}
			out := <-done
			if !strings.Contains(out, "ARK UPDATE for tester") {
				t.Fatalf("no update:\n%s", out)
			}
			for _, a := range f.authsSeen() {
				if a != "Bearer "+testToken+"|prj_t" {
					t.Fatalf("ARK got %q", a)
				}
			}
		})
	}
}

func processWithArg(needle string) (string, string) {
	m, _ := filepath.Glob("/proc/[0-9]*/cmdline")
	for _, p := range m {
		b, err := os.ReadFile(p)
		if err == nil && strings.Contains(string(b), needle) {
			return filepath.Base(filepath.Dir(p)), strings.ReplaceAll(string(b), "\x00", " ")
		}
	}
	return "", ""
}

// The watcher stops soon after the agent process it belongs to ends.
func TestWatcherStopsWithItsAgent(t *testing.T) {
	f := newFakeArk(t, 300*time.Millisecond, false)
	rg := newRig(t, f)
	agent := exec.Command("sleep", "60")
	if err := agent.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	start := time.Now()
	go func() {
		out, _ := runWatcher(t, rg, "ARK_OWNER_PID="+strconv.Itoa(agent.Process.Pid), "ARK_WATCH_MAX=60")
		done <- out
	}()
	time.Sleep(time.Second)
	agent.Process.Kill()
	agent.Wait()
	select {
	case out := <-done:
		if !strings.Contains(out, "is gone; the watcher stops") {
			t.Fatalf("watcher output:\n%s", out)
		}
		if el := time.Since(start); el > 10*time.Second {
			t.Fatalf("stopped after %v", el)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the watcher outlived its agent")
	}
}

// In a session ARK launched and wakes itself, the watcher does not start.
func TestWatcherExitsWhereArkWakesTheSession(t *testing.T) {
	f := newFakeArk(t, 0, true)
	rg := newRig(t, f)
	out, code := runWatcher(t, rg, "ARK_SESSION_ID=ses_1", "ARK_WAKE=ark")
	if code != 0 || !strings.Contains(out, "ARK launched this session (ses_1) and wakes it itself") || strings.Contains(out, "ARK UPDATE") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if len(f.authsSeen()) != 0 {
		t.Fatal("it called ARK")
	}
	// ARK_SESSION_ID alone (reminders off): the watcher runs.
	if out, _ := runWatcher(t, rg, "ARK_SESSION_ID=ses_1"); !strings.Contains(out, "ARK UPDATE for tester") {
		t.Fatalf("reminders off:\n%s", out)
	}
}
