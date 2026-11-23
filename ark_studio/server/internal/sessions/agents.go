package sessions

import (
	"arkstudio/go/internal/compat/pyos"
	"arkstudio/go/internal/providers"
)

// AgentHome is the home folder whose agent definitions are listed (tests
// replace it).
var AgentHome = pyos.Home

func hooksProjectPath(h *Hooks) string {
	if h == nil || h.Project == nil {
		return ""
	}
	p, err := h.Project()
	if err != nil || p == nil {
		return ""
	}
	return strOr(p.Value("path"))
}

// agentOptionsPublic is the presets endpoint's agent_options of a platform.
func agentOptionsPublic(h *Hooks, platform string) []any {
	list := providers.AgentOptions(platform, AgentHome(), hooksProjectPath(h))
	out := make([]any, 0, len(list))
	for _, a := range list {
		out = append(out, a.Public())
	}
	return out
}

// agentFlag selects the session's named agent on the command line; nil when
// the name is not one the platform has (an old free-text label).
func (m *Manager) agentFlag(h *Hooks, platform, agent string) []string {
	return providers.AgentFlag(platform, agent, AgentHome(), hooksProjectPath(h))
}

// codexJSONArgv makes `codex exec` print its JSON event stream (one event
// per line on stdout), which the transcript maps to messages, commands and
// file changes.  Without it Codex prints a human log on stderr whose lines
// cannot be told apart reliably.
func codexJSONArgv(argv []string) []string {
	at := indexOf(argv, "exec")
	if at < 0 && len(argv) > 1 && argv[1] == "e" {
		at = 1
	}
	if at < 0 {
		return argv
	}
	for _, a := range argv {
		if a == "--json" || a == "--experimental-json" {
			return argv
		}
	}
	return insertAt(append([]string(nil), argv...), at+1, "--json")
}
