package providers

// Named agent setups a session can start with (the launch dialog's
// "Agent" field, 2026-11-23):
//
//   claude  Claude Code sub-agent definitions, ~/.claude/agents/*.md and
//           <repository>/.claude/agents/*.md; ARK passes `--agent <name>`.
//   codex   Codex profiles, $CODEX_HOME/<name>.config.toml; ARK passes
//           `-p <name>`.
//
// Only names found here are passed on, so a stale or free-text value never
// reaches the command line.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pyos"
)

// AgentNameRe is what a name must look like to be passed to a CLI.
var AgentNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

const maxAgents = 40

// AgentOption is one named agent setup.
type AgentOption struct {
	ID, Label, Description, Scope string
}

// Public is the JSON shape of the presets endpoint.
func (a AgentOption) Public() *pyjson.Object {
	return pyjson.NewObject("id", a.ID, "label", a.Label, "description", a.Description, "scope", a.Scope)
}

var frontmatterKeyRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_-]*)\s*:\s*(.*)$`)

// frontmatter reads the simple `key: value` lines of a Markdown file's
// leading `---` block.
func frontmatter(text string) map[string]string {
	out := map[string]string{}
	text = strings.TrimPrefix(text, string(rune(0xfeff)))
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return out
	}
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			break
		}
		if m := frontmatterKeyRe.FindStringSubmatch(line); m != nil {
			v := strings.TrimSpace(m[2])
			if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
				v = v[1 : len(v)-1]
			}
			out[strings.ToLower(m[1])] = v
		}
	}
	return out
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, `\n`, " ")), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func claudeAgentsIn(dir, scope string, seen map[string]bool, out []AgentOption) []AgentOption {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if len(out) >= maxAgents {
			break
		}
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			continue
		}
		state, text := readTextBounded(filepath.Join(dir, e.Name()))
		if state != "present" {
			continue
		}
		fm := frontmatter(text)
		name := fm["name"]
		if name == "" {
			name = strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		}
		if !AgentNameRe.MatchString(name) || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, AgentOption{ID: name, Label: name, Description: oneLine(fm["description"], 160), Scope: scope})
	}
	return out
}

// projectAgentDirs are the .claude/agents folders of the project folder and
// of the Git repository it is in (the managed worktrees check out the same
// repository).
func projectAgentDirs(projectPath string) []string {
	if projectPath == "" {
		return nil
	}
	var dirs []string
	dir := filepath.Clean(projectPath)
	for i := 0; i < 6; i++ {
		if pyos.IsDir(filepath.Join(dir, ".claude", "agents")) {
			dirs = append(dirs, filepath.Join(dir, ".claude", "agents"))
		}
		if pyos.Exists(filepath.Join(dir, ".git")) {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return dirs
}

// ClaudeAgents lists Claude Code agent definitions: the user's own first,
// then the project's (scope "project").
func ClaudeAgents(home, projectPath string) []AgentOption {
	seen := map[string]bool{}
	var out []AgentOption
	if home != "" {
		out = claudeAgentsIn(filepath.Join(home, ".claude", "agents"), "user", seen, out)
	}
	for _, dir := range projectAgentDirs(projectPath) {
		out = claudeAgentsIn(dir, "project", seen, out)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Scope != out[j].Scope {
			return out[i].Scope == "user"
		}
		return strings.ToLower(out[i].ID) < strings.ToLower(out[j].ID)
	})
	return out
}

// CodexProfiles lists the Codex profiles ($CODEX_HOME/<name>.config.toml).
func CodexProfiles(home string) []AgentOption {
	dir := codexDir(home)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []AgentOption
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".config.toml") {
			continue
		}
		id := strings.TrimSuffix(name, ".config.toml")
		if !AgentNameRe.MatchString(id) {
			continue
		}
		desc := ""
		if state, text := readTextBounded(filepath.Join(dir, name)); state == "present" {
			top, _ := tomlModelKeys(text)
			if model := CleanModelID(top.Value("model")); model != "" {
				desc = model
			}
		}
		out = append(out, AgentOption{ID: id, Label: id, Description: desc, Scope: "user"})
		if len(out) >= maxAgents {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].ID) < strings.ToLower(out[j].ID) })
	return out
}

// AgentOptions is the named agent list of one platform (nil when it has
// none).
func AgentOptions(platform, home, projectPath string) []AgentOption {
	switch platform {
	case "claude":
		return ClaudeAgents(home, projectPath)
	case "codex":
		return CodexProfiles(home)
	}
	return nil
}

// AgentFlag is the command-line flag that selects `name` on `platform`, or
// nil when the platform has no such agent.
func AgentFlag(platform, name, home, projectPath string) []string {
	name = strings.TrimSpace(name)
	if name == "" || !AgentNameRe.MatchString(name) {
		return nil
	}
	for _, a := range AgentOptions(platform, home, projectPath) {
		if a.ID != name {
			continue
		}
		if platform == "claude" {
			return []string{"--agent", name}
		}
		return []string{"-p", name}
	}
	return nil
}
