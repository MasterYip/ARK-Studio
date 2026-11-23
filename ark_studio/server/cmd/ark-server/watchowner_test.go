package main

// The watcher finds its agent by walking up its parent chain when the
// given --owner-pid is gone or only the shell (T-0109), and refuses a
// wake-up it cannot give with exit status 2.

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Windows: Codex (npm: cmd -> node -> codex.exe) runs each command in a
// PowerShell; the agent passed that shell's $PID, which exits after the
// command.
func TestResolveOwnerWindowsTable(t *testing.T) {
	tab := procTable{
		4:    {0, "System"},
		900:  {4, "explorer.exe"},
		950:  {4, "services.exe"},
		1000: {900, "WindowsTerminal.exe"},
		1100: {1000, "cmd.exe"},
		1200: {1100, "node.exe"},
		1300: {1200, "codex.exe"},
		1400: {1300, "powershell.exe"},
		1500: {1400, "ark-server.exe"},
	}
	for _, c := range []struct {
		given, want int
		kind        string
		note        bool
	}{
		{0, 1300, "codex", false},    // no --owner-pid
		{1400, 1300, "codex", true},  // $PID: the shell, still alive
		{4242, 1300, "codex", true},  // $PID of a shell that has exited
		{1300, 1300, "codex", false}, // the right one
		{950, 950, "", false},        // a live process the agent named on purpose (not above the watcher)
	} {
		got, kind, note := resolveOwner(tab, 1500, c.given)
		if got != c.want || kind != c.kind || (note != "") != c.note {
			t.Fatalf("given %d: got %d %q %q", c.given, got, kind, note)
		}
	}
	// Claude Code installed through npm: the node process is the owner.
	tab[1300] = procInfo{1200, "bash.exe"}
	if got, kind, _ := resolveOwner(tab, 1500, 0); got != 1200 || kind != "node" {
		t.Fatalf("node wrapper: %d %q", got, kind)
	}
	// Nothing agent-like above, nothing given: no owner.
	tab[1200] = procInfo{1100, "python.exe"}
	if got, _, _ := resolveOwner(tab, 1500, 0); got != 0 {
		t.Fatalf("no agent: %d", got)
	}
	// A parent-id loop (stale ids on Windows) ends.
	loop := procTable{10: {11, "ark-server.exe"}, 11: {12, "a.exe"}, 12: {11, "b.exe"}}
	if got, _, _ := resolveOwner(loop, 10, 0); got != 0 {
		t.Fatalf("loop: %d", got)
	}
}

// Linux: names from /proc/<pid>/stat (15 characters at most); Claude Code
// sets its process title to claude; in Codex's sandbox namespace the
// sandbox helper is pid 1.  macOS: ps.
func TestResolveOwnerLinuxTable(t *testing.T) {
	stat := func(s string) procInfo {
		info, ok := parseProcStat(s)
		if !ok {
			t.Fatalf("parse %q", s)
		}
		return info
	}
	tab := procTable{
		1:   stat("1 (systemd) S 0 1 1 0"),
		500: stat("500 (sshd) S 1 500 500 0"),
		600: stat("600 (claude) S 500 600 600 0"),
		700: stat("700 (bash) S 600 700 700 0"),
		800: stat("800 (ark-server) S 700 700 700 0"),
	}
	if got, kind, _ := resolveOwner(tab, 800, 0); got != 600 || kind != "claude" {
		t.Fatalf("claude: %d %q", got, kind)
	}
	if got, kind, note := resolveOwner(tab, 800, 700); got != 600 || kind != "claude" || note == "" {
		t.Fatalf("$$ instead of $PPID: %d %q %q", got, kind, note)
	}
	sandbox := procTable{
		1: stat("1 (codex-linux-san) S 0 1 1 0"),
		2: stat("2 (bash) S 1 1 1 0"),
		3: stat("3 (ark-server) S 2 1 1 0"),
	}
	if got, kind, _ := resolveOwner(sandbox, 3, 0); got != 1 || kind != "codex" {
		t.Fatalf("sandbox: %d %q", got, kind)
	}
	if info := stat("42 (tmux: server) S 7 42 42 0"); info.PPID != 7 || info.Name != "tmux: server" {
		t.Fatalf("spaces in comm: %+v", info)
	}
	ps := parsePS("  1     0 /sbin/launchd\n 300   1 /Applications/Codex App.app/Contents/MacOS/codex\n 301 300 -zsh\n")
	mac := procTable{302: {301, "ark-server"}, 301: ps[301], 300: ps[300], 1: ps[1]}
	if got, kind, _ := resolveOwner(mac, 302, 0); got != 300 || kind != "codex" {
		t.Fatalf("ps: %d %q %+v", got, kind, ps)
	}
	for name, want := range map[string]string{"codex.exe": "codex", "Claude.EXE": "claude", "node": "node", "bun": "node",
		"/usr/local/bin/codex": "codex", "codexbar": "", "pwsh.exe": "", "claude-helper": ""} {
		if agentKind(name) != want {
			t.Fatalf("agentKind(%q) = %q", name, agentKind(name))
		}
	}
}

// With the real watcher: the owner comes from the parent chain (the agent
// passed the shell's pid, or none), and a wake-up it cannot give exits 2.
func TestAgentWatchFindsOwnerAndRefusesLoudly(t *testing.T) {
	clearWakeEnv(t)
	codex := startSleeper(t) // stands in for codex.exe: alive, named codex in the table
	me := os.Getpid()
	const shell = 7_000_001
	table := procTable{me: {shell, "ark-server.exe"}, shell: {codex.Process.Pid, "powershell.exe"},
		codex.Process.Pid: {1, "codex.exe"}}
	old := watchProcTable
	watchProcTable = func() (procTable, error) { return table, nil }
	t.Cleanup(func() { watchProcTable = old })
	t.Setenv("CODEX_THREAD_ID", "th-1")
	t.Setenv("CODEX_MANAGED_PACKAGE_ROOT", "")
	t.Setenv("PATH", t.TempDir()) // no codex with queue here: the last check fails
	srv, app := watchServer(t)
	t.Setenv("ARK_TOKEN", app.AgentToken())
	run := func(args ...string) (int, string, string) {
		var out, errw bytes.Buffer
		base := []string{"--url", srv.URL, "--state", filepath.Join(t.TempDir(), "c"), "--until-update", "--max", "1", "--wait", "1"}
		code := agentWatch(append(base, args...), &out, &errw)
		return code, out.String(), errw.String()
	}
	// No --owner-pid: the owner check passes (Codex found above), the
	// queue check is the one that fails.
	code, out, errs := run("--wake", "codex", "--codex-thread", "th-1")
	if code != 2 || !strings.Contains(errs, "cannot wake codex: no codex with `queue` found") {
		t.Fatalf("no owner given: %d %q %q", code, out, errs)
	}
	// The shell's pid: replaced by the Codex process, and said so.
	code, out, errs = run("--wake", "codex", "--codex-thread", "th-1", "--owner-pid", strconv.Itoa(shell))
	if code != 2 || !strings.Contains(out, "the watcher belongs to codex.exe pid "+strconv.Itoa(codex.Process.Pid)) ||
		!strings.Contains(errs, "no codex with `queue` found") {
		t.Fatalf("shell pid: %d %q %q", code, out, errs)
	}
	// Nothing agent-like above: the owner is not found.
	table[shell] = procInfo{1, "powershell.exe"}
	code, _, errs = run("--wake", "codex", "--codex-thread", "th-1")
	if code != 2 || !strings.Contains(errs, "cannot wake codex: owner process not found; pass --owner-pid <codex pid>") {
		t.Fatalf("no owner: %d %q", code, errs)
	}
	// --wake claude outside Claude Code, and an unknown value, fail too.
	if code, _, errs = run("--wake", "claude"); code != 2 || !strings.Contains(errs, "cannot wake claude") {
		t.Fatalf("claude: %d %q", code, errs)
	}
	if code, _, errs = run("--wake", "pager"); code != 2 || !strings.Contains(errs, "unknown --wake") {
		t.Fatalf("unknown: %d %q", code, errs)
	}
	// Under Claude Code --wake claude is the plain exit-on-news watcher.
	t.Setenv("CLAUDECODE", "1")
	if code, out, errs = run("--wake", "claude"); code != 0 || !strings.Contains(out, "no updates") {
		t.Fatalf("claude ok: %d %q %q", code, out, errs)
	}
	// ARK_WAKE=ark outside an ARK session wakes nothing and fails nothing.
	t.Setenv("CLAUDECODE", "")
	if code, _, errs = run("--wake", "ark"); code != 0 {
		t.Fatalf("ark: %d %q", code, errs)
	}
}

// The real process table holds this test with its parent.
func TestListProcessesHasSelf(t *testing.T) {
	tab, err := listProcesses()
	if err != nil {
		t.Fatal(err)
	}
	if info, ok := tab[os.Getpid()]; !ok || info.PPID != os.Getppid() || info.Name == "" {
		t.Fatalf("self: %+v %v", info, ok)
	}
}
