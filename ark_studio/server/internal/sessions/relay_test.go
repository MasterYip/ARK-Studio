//go:build !windows

package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"arkstudio/go/internal/compat/pyjson"
)

// relayManager is a manager on root that starts relayed processes; it is
// not cleaned up (the test hands its sessions to a second manager).
func relayManager(t *testing.T, root string) *Manager {
	t.Helper()
	m, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	m.Presets = []*pyjson.Object{pyjson.NewObject("id", "claude", "label", "Claude Code", "available", true,
		"command", []any{exe, "-p", "{prompt}"}, "prompt_mode", "arg", "followup", false)}
	m.Relay = true
	t.Cleanup(func() { killAll(m) }) // ends what is still running
	return m
}

func alive(pid int) bool { return pid > 0 && syscall.Kill(pid, 0) == nil }

func lineCount(m *Manager, s *Session, line string) int {
	n := 0
	for _, e := range m.allLogEntries(s, "stdout") {
		if strings.Contains(strOr(e.Value("line")), line) {
			n++
		}
	}
	return n
}

// A server restart: the old manager detaches, the agent keeps running (and
// writes while no server reads), a new manager on the same root re-attaches,
// takes in the output it missed exactly once, and talks to the agent.
func TestRelaySessionSurvivesRestart(t *testing.T) {
	t.Setenv("ARK_FAKE_CLAUDE", "1")
	root := t.TempDir()
	old := relayManager(t, root)
	s, err := old.Start(nil, pyjson.NewObject("platform", "claude", "prompt", "hello"), "http://127.0.0.1:1", "alice")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first turn", func() bool { return metaOf(old, s, "turn_state") == "waiting" })
	if _, err := old.Steer(nil, s.ID, "slow work", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "slow turn", func() bool { return strings.Contains(logText(old, s), "echo: slow work") })
	pid := int(intOr(metaOf(old, s, "pid"), 0))

	old.Detach() // the server shuts down
	if !alive(pid) {
		t.Fatal("shutdown must not end the agent")
	}
	if strings.Contains(logText(old, s), "slow turn done") {
		t.Fatal("the slow turn should still run")
	}
	time.Sleep(300 * time.Millisecond)

	m := relayManager(t, root)
	again, err := m.Get(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st := metaOf(m, again, "status"); st != "running" {
		t.Fatalf("a relayed session stays running across Open: %v", st)
	}
	if attached, ended := m.Resume(); attached != 1 || ended != 0 {
		t.Fatalf("resume: attached=%d ended=%d", attached, ended)
	}
	// The turn that ran through the restart ends; its lines arrive once.
	waitFor(t, "slow turn done", func() bool { return strings.Contains(logText(m, again), "slow turn done") })
	waitFor(t, "waiting", func() bool { return metaOf(m, again, "turn_state") == "waiting" })
	if !alive(pid) || int(intOr(metaOf(m, again, "pid"), 0)) != pid {
		t.Fatal("the same agent process continues")
	}
	if _, err := m.Steer(nil, again.ID, "after restart", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "message after the restart", func() bool { return strings.Contains(logText(m, again), "echo: after restart") })
	for _, line := range []string{"echo: hello", "echo: slow work", "slow turn done", "echo: after restart", `"subtype":"init"`} {
		if n := lineCount(m, again, line); n != 1 {
			t.Errorf("%q recorded %d times:\n%s", line, n, logText(m, again))
		}
	}
	// Stop ends the process cleanly; the relay files go away.
	waitFor(t, "waiting again", func() bool { return metaOf(m, again, "turn_state") == "waiting" })
	if _, err := m.RequestStop(again.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "stopped", func() bool { return !m.IsActive(again) })
	if st := metaOf(m, again, "status"); st != "stopped" {
		t.Fatalf("after stop: %v", st)
	}
	for _, name := range []string{relayInfoFile, relayExitFile, "stdout.raw", "stdout.pos", "relay.sock"} {
		if _, err := os.Stat(filepath.Join(again.Folder, name)); err == nil {
			t.Errorf("%s is left after the session ended", name)
		}
	}
	// The next message starts a new relayed process in the same folder.
	if _, err := m.ContinueSession(nil, again.ID, "welcome back", ContinueOptions{Actor: "alice"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "resumed turn", func() bool { return strings.Contains(logText(m, again), "echo: welcome back") })
}

// Sessions whose process ended while no server ran are finished on Resume:
// with the exit code when the relay saw it, else stopped with a notice.
func TestRelayDeadSessionsEnded(t *testing.T) {
	t.Setenv("ARK_FAKE_CLAUDE", "1")
	root := t.TempDir()
	old := relayManager(t, root)
	start := func(actor string) *Session {
		s, err := old.Start(nil, pyjson.NewObject("platform", "claude", "prompt", "hi "+actor), "http://127.0.0.1:1", actor)
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, "first turn", func() bool { return metaOf(old, s, "turn_state") == "waiting" })
		return s
	}
	crashed, vanished := start("ann"), start("ben")
	old.Detach()

	// One agent crashes while the server is down: its relay records the code.
	killGroup(int(intOr(metaOf(old, crashed, "pid"), 0)))
	waitFor(t, "exit recorded", func() bool { _, ok := readRelayExit(crashed.Folder); return ok })
	// The other loses its relay and itself (as after a machine restart).
	info, ok := readRelayInfo(vanished.Folder)
	if !ok {
		t.Fatal("relay.json")
	}
	_ = syscall.Kill(info.RelayPID, syscall.SIGKILL)
	killGroup(info.ChildPID)
	waitFor(t, "agent gone", func() bool { return !alive(info.ChildPID) })
	waitFor(t, "relay gone", func() bool { return !alive(info.RelayPID) })

	m := relayManager(t, root)
	if attached, ended := m.Resume(); attached != 0 || ended != 2 {
		t.Fatalf("resume: attached=%d ended=%d", attached, ended)
	}
	a, _ := m.Get(crashed.ID)
	if metaOf(m, a, "status") != "errored" || intOr(metaOf(m, a, "exit_code"), 0) != -9 {
		t.Errorf("crashed: %v %v", metaOf(m, a, "status"), metaOf(m, a, "exit_code"))
	}
	if lineCount(m, a, "echo: hi ann") != 1 {
		t.Errorf("crashed session log:\n%s", logText(m, a))
	}
	b, _ := m.Get(vanished.ID)
	if metaOf(m, b, "status") != "stopped" || !strings.Contains(strOr(metaOf(m, b, "notice")), "not running") {
		t.Errorf("vanished: %v %v", metaOf(m, b, "status"), metaOf(m, b, "notice"))
	}
	for _, s := range []*Session{a, b} {
		if hasRelay(s.Folder) {
			t.Errorf("%s: relay.json is left", s.ID)
		}
	}
}

// A relay answers for the agent's stdin: a closed stdin is reported, and an
// agent that cannot start reports the same error text as without a relay.
func TestRelayStartErrorAndClosedStdin(t *testing.T) {
	dir := t.TempDir()
	if _, err := startRelay(dir, "s_relaytest1", []string{filepath.Join(dir, "missing-agent")}, dir, os.Environ()); err == nil ||
		!strings.Contains(osErrorText(err, "missing-agent"), "No such file") {
		t.Fatalf("missing program: %v", err)
	}
	t.Setenv("ARK_FAKE_CLAUDE", "1")
	exe, _ := os.Executable()
	p, err := startRelay(dir, "s_relaytest1", []string{exe, "--input-format", "stream-json"}, dir, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	p.closeStdin()
	code, known := p.relay.wait(dir)
	if !known || code == nil || *code != 0 {
		t.Fatalf("EOF on stdin ends the fake agent with 0: %v %v", code, known)
	}
	if err := p.writeStdin([]byte("late\n")); err == nil {
		t.Fatal("a write after the agent ended fails")
	}
}
