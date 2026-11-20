package sessions

// Codex sandbox for sessions ARK launches (T-0053, 2026-11-20).  `codex
// exec` defaults to a read-only sandbox without network, so the agent could
// not reach the ARK API it is told to use.  ARK now passes a sandbox that
// matches the session's access mode and always allows network:
//
//   read_only, plan                 files read-only, network on
//   ask, accept_edits, full_access  workspace writable, network on
//
// The filesystem is never widened beyond Codex's workspace sandbox (no
// danger-full-access).  Codex 0.166 and later take a permission profile
// (`permissions.<name>.extends` + `network.enabled`); older versions get
// `-s workspace-write -c sandbox_workspace_write.network_access=true` for
// the writing modes and `-s read-only` (no network) for the read modes.
// A command that already chooses a sandbox (ARK_TASK_CODEX_COMMAND with
// -s, --sandbox, default_permissions or the bypass flag) is left alone.

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// codexProfileSince is the first Codex version checked to support
// permission profiles with network access.
var codexProfileSince = [3]int{0, 166, 0}

var (
	codexVersionMu    sync.Mutex
	codexVersionCache = map[string][3]int{}
	reCodexVersion    = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)
)

// CodexVersion reports the version of a codex binary (cached); a zero
// version when it cannot be read.  Tests replace it.
var CodexVersion = func(bin string) [3]int {
	codexVersionMu.Lock()
	defer codexVersionMu.Unlock()
	if v, ok := codexVersionCache[bin]; ok {
		return v
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, bin, "--version").Output()
	v := parseCodexVersion(string(out))
	codexVersionCache[bin] = v
	return v
}

func parseCodexVersion(s string) [3]int {
	var v [3]int
	if m := reCodexVersion.FindStringSubmatch(s); m != nil {
		for i := 0; i < 3; i++ {
			v[i], _ = strconv.Atoi(m[i+1])
		}
	}
	return v
}

func versionAtLeast(v, min [3]int) bool {
	for i := 0; i < 3; i++ {
		if v[i] != min[i] {
			return v[i] > min[i]
		}
	}
	return true
}

// codexChoosesSandbox: the command already picks a sandbox.
func codexChoosesSandbox(argv []string) bool {
	for _, a := range argv {
		if a == "-s" || a == "--sandbox" || strings.HasPrefix(a, "--sandbox=") ||
			a == "--dangerously-bypass-approvals-and-sandbox" || a == "--full-auto" ||
			strings.Contains(a, "default_permissions") || strings.Contains(a, "sandbox_mode") {
			return true
		}
	}
	return false
}

// codexSandboxArgs are the arguments for one access mode and Codex version.
func codexSandboxArgs(access string, version [3]int) []string {
	writes := access == "full_access" || access == "accept_edits" || access == "ask"
	if versionAtLeast(version, codexProfileSince) {
		base := ":read-only"
		if writes {
			base = ":workspace"
		}
		return []string{"-c", `permissions.ark_session.extends="` + base + `"`,
			"-c", "permissions.ark_session.network.enabled=true", "-c", `default_permissions="ark_session"`}
	}
	if writes {
		return []string{"-s", "workspace-write", "-c", "sandbox_workspace_write.network_access=true"}
	}
	return []string{"-s", "read-only"}
}

// withCodexSandbox inserts the sandbox arguments right after `exec`.
func withCodexSandbox(argv []string, access string) []string {
	i := indexOf(argv, "exec")
	if i < 0 || codexChoosesSandbox(argv) {
		return argv
	}
	return insertAt(argv, i+1, codexSandboxArgs(access, CodexVersion(argv[0]))...)
}
