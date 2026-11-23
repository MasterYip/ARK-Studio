package main

// `ark-server agent watch --until-update`: whose watcher it is and how it
// wakes its agent (T-0053, 2026-11-21).  The built-in binary is the
// preferred watcher (no script download, nothing for antivirus heuristics
// to flag); it does what the watcher scripts do:
//   - the token comes from ~/.ark/tokens/<project> or the environment;
//   - --owner-pid (ARK_OWNER_PID) ties it to the agent: it stops when that
//     process is gone; without it, or when it is gone or only the shell,
//     the nearest Codex or Claude Code process above the watcher (T-0109);
//   - --wake codex with --codex-thread (ARK_WAKE, ARK_CODEX_THREAD) wakes a
//     Codex session through `codex queue`, only when the owner process is
//     Codex, the thread id is the one Codex gave this command
//     (CODEX_THREAD_ID) and no Claude Code environment is present; it then
//     detaches and keeps watching; when it cannot, it says why and exits 2;
//   - messages start "ARK UPDATE for <agent>, ARK project <name> (<id>)".

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// watchDetachedEnv carries the owner pid into the detached copy.
const watchDetachedEnv = "ARK_WATCH_DETACHED_OWNER"

// watchSelf is how the watcher starts its detached copy (tests replace it).
var watchSelf = func() (string, []string, error) {
	exe, err := os.Executable()
	return exe, os.Args[1:], err
}

// isCodexProcess reports whether a process name is Codex's (the binary,
// or its Linux sandbox, whose name the kernel cuts to 15 characters).
func isCodexProcess(name string) bool {
	name = strings.ToLower(filepath.Base(strings.TrimSpace(name)))
	name = strings.TrimSuffix(name, ".exe")
	return name == "codex" || strings.HasPrefix(name, "codex-linux-san")
}

// codexWakeBlocker says why the watcher must not queue into a Codex
// session ("" = it may).  owner is the agent's process id and name ("" =
// look it up).
func codexWakeBlocker(o *watchOptions, owner int, name string) string {
	if name == "" && owner > 1 {
		name = watchProcessName(owner)
	}
	switch {
	case os.Getenv("CLAUDECODE") != "" || os.Getenv("CLAUDE_CODE_ENTRYPOINT") != "":
		return "this is a Claude Code session"
	case owner <= 1:
		return "owner process not found"
	case !isCodexProcess(name):
		return fmt.Sprintf("the owner process (pid %d) is not Codex", owner)
	case o.CodexThread == "" || o.CodexThread != os.Getenv("CODEX_THREAD_ID"):
		return "--codex-thread (ARK_CODEX_THREAD) is not this Codex session's thread"
	}
	return ""
}

// wakeHint says what to do about a codexWakeBlocker reason.
func wakeHint(why string) string {
	switch {
	case strings.Contains(why, "Claude Code"):
		return "Claude Code is woken when the watcher exits: drop --wake codex"
	case strings.Contains(why, "thread"):
		return "pass --codex-thread with your own CODEX_THREAD_ID (PowerShell: $env:CODEX_THREAD_ID)"
	}
	return "pass --owner-pid <codex pid>"
}

// findCodexQueue returns a codex that has `queue`: the one running this
// session first (another `codex` on the PATH may be older).
func findCodexQueue() string {
	var candidates []string
	if root := os.Getenv("CODEX_MANAGED_PACKAGE_ROOT"); root != "" {
		candidates = append(candidates, filepath.Join(root, "bin", "codex.js"))
	}
	if p, err := exec.LookPath("codex"); err == nil {
		candidates = append(candidates, p)
	}
	for _, c := range candidates {
		cmd := exec.Command(c, "queue", "--help")
		if cmd.Run() == nil {
			return c
		}
	}
	return ""
}

// startDetached starts the watcher again on its own, tied to owner, and
// returns at once (Codex ends a command's process group when it returns).
func startDetached(owner int) error {
	exe, args, err := watchSelf()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), watchDetachedEnv+"="+strconv.Itoa(owner))
	detachProcess(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// projectLabel is "<name> (<id>)" from GET /api/agent/v1/context, or the
// id alone.
func projectLabel(ctx context.Context, o *watchOptions) string {
	if o.Project == "" {
		return "at " + o.URL
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", o.URL+"/api/agent/v1/context", nil)
	if err != nil {
		return o.Project
	}
	setWatchHeaders(req, o)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return o.Project
	}
	defer resp.Body.Close()
	var reply struct {
		Project struct {
			Name string `json:"name"`
		} `json:"project"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != 200 || json.Unmarshal(body, &reply) != nil || reply.Project.Name == "" {
		return o.Project
	}
	return reply.Project.Name + " (" + o.Project + ")"
}

func watchLabel(o *watchOptions) string {
	switch {
	case os.Getenv("ARK_AGENT_LABEL") != "":
		return os.Getenv("ARK_AGENT_LABEL")
	case o.Name != "":
		return o.Name
	case o.CodexThread != "" && o.codex != "":
		return "Codex thread " + o.CodexThread
	}
	return "the agent that started this watcher"
}

// queueCodex wakes the Codex session with the news.
func queueCodex(o *watchOptions, head string, items []json.RawMessage, project string) error {
	var lines []string
	for i, raw := range items {
		if i == 10 {
			break
		}
		var it watchItem
		if json.Unmarshal(raw, &it) == nil && it.Summary != "" {
			lines = append(lines, "- "+oneLine(it.Summary))
		}
	}
	msg := head + "\n" + strings.Join(lines, "\n") + "\nThis message comes from the ARK watcher you started for this project. " +
		"If you are not working on ARK project " + project + ", ignore it. Otherwise act on the updates under the core rules " +
		"(GET /api/agent/v1/units/<ID>/comments for the full comments, reply with reply_to, resolve), then start the ARK watcher again."
	return exec.Command(o.codex, "queue", "--thread", o.CodexThread, "--message", msg).Run()
}

func watchEnvInt(name string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	return n
}
