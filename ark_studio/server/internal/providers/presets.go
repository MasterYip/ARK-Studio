// Package providers ports arkstudio/providers.py (the read-only provider
// catalog: which agent CLIs are installed, their version, credential
// sources and model lists) and the task-agent preset table of
// arkstudio/sessions.py (default_presets, _normalize_preset,
// _preset_available) that the catalog and the session manager share.
package providers

import (
	"errors"
	"os"
	"regexp"
	"runtime"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pyos"
	"arkstudio/go/internal/compat/pytext"
)

// Permission modes of sessions.py.
var (
	PermissionModes = []string{"read_only", "plan", "ask", "accept_edits", "full_access"}
	ApprovalModes   = []string{"ask", "accept_edits"}
)

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// SplitCommand is sessions.split_command.
func SplitCommand(text string) ([]string, error) {
	text = pytext.Strip(text)
	if text == "" {
		return nil, nil
	}
	if runtime.GOOS == "windows" {
		raw, err := pyos.ShlexSplit(text, false)
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(raw))
		for _, tok := range raw {
			rs := []rune(tok)
			if len(rs) >= 2 && rs[0] == rs[len(rs)-1] && (rs[0] == '"' || rs[0] == '\'') {
				tok = string(rs[1 : len(rs)-1])
			}
			out = append(out, tok)
		}
		return out, nil
	}
	return pyos.ShlexSplit(text, true)
}

// stringList is sessions._string_list.
func stringList(value any, limit, maxLen int) []string {
	var items []any
	switch v := value.(type) {
	case string:
		for _, part := range strings.Split(v, ",") {
			items = append(items, pytext.Strip(part))
		}
	case []any:
		items = v
	case []string:
		for _, s := range v {
			items = append(items, s)
		}
	default:
		return []string{}
	}
	out := []string{}
	for _, item := range items {
		text := ""
		if pyjson.Truthy(item) {
			text = pyjson.Str(item)
		}
		text = pytext.Prefix(pytext.Strip(text), maxLen)
		if text != "" && !contains(out, text) {
			out = append(out, text)
		}
		if len(out) >= limit {
			break
		}
	}
	return out
}

// ModeToken is sessions._mode_token.
func ModeToken(value any) string {
	s := ""
	if pyjson.Truthy(value) {
		s = pyjson.Str(value)
	}
	s = pytext.Lower(pytext.Strip(s))
	return strings.ReplaceAll(strings.ReplaceAll(s, "-", "_"), " ", "_")
}

func asBool(value any, def bool) bool {
	switch v := value.(type) {
	case bool:
		return v
	case nil:
		return def
	}
	return set("1", "true", "yes", "on")[pytext.Lower(pytext.Strip(pyjson.Str(value)))]
}

func set(items ...string) map[string]bool {
	m := map[string]bool{}
	for _, s := range items {
		m[s] = true
	}
	return m
}

var presetIDJunk = regexp.MustCompile(`[^a-z0-9_-]+`)

// NormalizePreset is sessions._normalize_preset.
func NormalizePreset(raw *pyjson.Object) (*pyjson.Object, error) {
	rawID := ""
	if v := raw.Value("id"); pyjson.Truthy(v) {
		rawID = pyjson.Str(v)
	}
	id := strings.Trim(presetIDJunk.ReplaceAllString(pytext.Lower(rawID), "-"), "-")
	if id == "" {
		return nil, errors.New("task preset id is required")
	}
	var command []string
	switch c := raw.Value("command").(type) {
	case string:
		var err error
		if command, err = SplitCommand(c); err != nil {
			return nil, err
		}
	case []any:
		for _, part := range c {
			command = append(command, pyjson.Str(part))
		}
	case []string:
		command = append(command, c...)
	}
	if len(command) == 0 {
		return nil, errors.New("task preset " + id + " needs a command")
	}
	promptMode := ""
	if v := raw.Value("prompt_mode"); pyjson.Truthy(v) {
		promptMode = pytext.Lower(pytext.Strip(pyjson.Str(v)))
	}
	if promptMode != "arg" && promptMode != "stdin" {
		promptMode = "stdin"
		for _, part := range command {
			if strings.Contains(part, "{prompt}") {
				promptMode = "arg"
				break
			}
		}
	}
	followup := asBool(raw.Value("followup"), promptMode == "stdin")
	supportsApproval := asBool(raw.Value("supports_approval"), false) && followup
	declaredRaw := raw.Value("access_modes")
	if raw.Has("permission_modes") {
		declaredRaw = raw.Value("permission_modes")
	}
	var declared []string
	for _, mode := range stringList(declaredRaw, 5, 80) {
		if m := ModeToken(mode); contains(PermissionModes, m) {
			declared = append(declared, m)
		}
	}
	if len(declared) == 0 {
		if supportsApproval {
			declared = append([]string{}, PermissionModes...)
		} else {
			declared = []string{"read_only", "plan", "full_access"}
		}
	}
	var unique []string
	for _, m := range declared {
		if !contains(unique, m) {
			unique = append(unique, m)
		}
	}
	permissionModes := []string{}
	for _, m := range unique {
		if contains(ApprovalModes, m) && !supportsApproval {
			continue
		}
		permissionModes = append(permissionModes, m)
	}
	if len(permissionModes) == 0 {
		permissionModes = []string{"read_only"}
	}
	defRaw := raw.Value("default_permission_mode")
	if !pyjson.Truthy(defRaw) {
		defRaw = raw.Value("access_mode")
	}
	defaultMode := ModeToken(defRaw)
	if !contains(permissionModes, defaultMode) {
		if contains(permissionModes, "read_only") {
			defaultMode = "read_only"
		} else {
			defaultMode = permissionModes[0]
		}
	}
	unsupported := []string{}
	for _, m := range ApprovalModes {
		if !contains(permissionModes, m) {
			unsupported = append(unsupported, m)
		}
	}
	efforts := raw.Value("efforts")
	if !pyjson.Truthy(efforts) {
		efforts = raw.Value("reasoning")
	}
	label := id
	if v := raw.Value("label"); pyjson.Truthy(v) {
		label = pyjson.Str(v)
	}
	description := ""
	if v := raw.Value("description"); pyjson.Truthy(v) {
		description = pyjson.Str(v)
	}
	return pyjson.NewObject(
		"id", id,
		"label", label,
		"description", description,
		"command", command,
		"prompt_mode", promptMode,
		"followup", followup,
		"models", stringList(raw.Value("models"), 20, 80),
		"efforts", stringList(efforts, 8, 80),
		"permission_modes", permissionModes,
		"supports_approval", supportsApproval,
		"unsupported_permission_modes", unsupported,
		"default_permission_mode", defaultMode,
		"demo", pyjson.Truthy(raw.Value("demo")),
		"available", raw.Value("available"),
	), nil
}

// Command returns preset["command"] as strings.
func Command(preset *pyjson.Object) []string {
	switch c := preset.Value("command").(type) {
	case []string:
		return c
	case []any:
		out := make([]string, len(c))
		for i, x := range c {
			out[i] = pyjson.Str(x)
		}
		return out
	}
	return nil
}

// Available is sessions._preset_available.
func Available(preset *pyjson.Object) bool {
	if b, ok := preset.Value("available").(bool); ok {
		return b
	}
	if pyjson.Truthy(preset.Value("demo")) {
		return true
	}
	cmd := Command(preset)
	if len(cmd) == 0 {
		return false
	}
	program := cmd[0]
	if pyos.HasDirPart(program) {
		return pyos.IsFile(program)
	}
	return pyos.Which(program, "", false) != ""
}

// ShellCommand is platform_support.shell_command() (interactive mode).
func ShellCommand(env func(string) string) []string {
	if runtime.GOOS == "windows" {
		return []string{"powershell", "-NoLogo", "-NoProfile", "-Command", "-"}
	}
	shell := pytext.Strip(env("SHELL"))
	resolved := ""
	if shell != "" {
		if strings.HasPrefix(shell, "/") && pyos.IsFile(shell) {
			resolved = shell
		} else {
			base := shell[strings.LastIndex(shell, "/")+1:]
			resolved = pyos.Which(base, "", false)
		}
	}
	if resolved == "" {
		resolved = pyos.Which("sh", "", false)
		if resolved == "" {
			resolved = "/bin/sh"
		}
	}
	return []string{resolved, "-lc", "exec " + pyos.Quote(resolved) + " -l"}
}

// EchoCommand is the command of the local demo preset.  Python runs
// arkstudio/demo_agent.py with its own interpreter; the Go server runs its
// own binary.
func EchoCommand() []string {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	return []string{exe, "demo-agent", "--prompt", "{prompt}"}
}

// DefaultPresets is sessions.default_presets(os.environ), normalized as
// SessionManager stores them.
func DefaultPresets() ([]*pyjson.Object, error) {
	env := os.Getenv
	fromEnv := func(name, def string) ([]string, error) {
		v := env(name)
		if v == "" {
			v = def
		}
		return SplitCommand(v)
	}
	envList := func(name string, def []string) []string {
		raw := pytext.Strip(env(name))
		if raw == "" {
			return append([]string{}, def...)
		}
		out := []string{}
		for _, part := range strings.Split(raw, ",") {
			if p := pytext.Strip(part); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	var shellCommand []string
	if override := pytext.Strip(env("ARK_TASK_SHELL_COMMAND")); override != "" {
		var err error
		if shellCommand, err = SplitCommand(override); err != nil {
			return nil, err
		}
	} else {
		shellCommand = ShellCommand(env)
	}
	type spec struct {
		id, label, description, envName, def, promptMode string
	}
	cli := []spec{
		{"dsh", "DeepSeek Harness", "Headless DSH process. Reads the initial prompt from stdin.", "ARK_TASK_DSH_COMMAND", "dsh headless -", "stdin"},
		{"claude", "Claude Code", "Claude Code in a long-lived session: every message goes to the running process.", "ARK_TASK_CLAUDE_COMMAND", `claude -p "{prompt}"`, "arg"},
		{"codex", "Codex CLI", "Codex exec with its JSON event stream; each message continues the same thread.", "ARK_TASK_CODEX_COMMAND", `codex exec --json --skip-git-repo-check "{prompt}"`, "arg"},
		{"opencode", "OpenCode", "OpenCode non-interactive run mode.", "ARK_TASK_OPENCODE_COMMAND", `opencode run "{prompt}"`, "arg"},
		{"cursor", "Cursor Agent", "Cursor Agent CLI print mode.", "ARK_TASK_CURSOR_COMMAND", `cursor-agent -p "{prompt}"`, "arg"},
		{"gemini", "Gemini CLI", "Gemini CLI non-interactive prompt mode.", "ARK_TASK_GEMINI_COMMAND", `gemini -p "{prompt}"`, "arg"},
	}
	presets := []*pyjson.Object{pyjson.NewObject(
		"id", "echo",
		"label", "Echo (local demo)",
		"description", "A tiny local Python agent for trying the session lifecycle. No model call.",
		"command", EchoCommand(),
		"prompt_mode", "arg",
		"followup", true,
		"models", envList("ARK_TASK_ECHO_MODELS", []string{"none (demo)"}),
		"efforts", envList("ARK_TASK_ECHO_EFFORTS", nil),
		"permission_modes", append([]string{}, PermissionModes...),
		"supports_approval", true,
		"demo", true,
	)}
	for _, s := range cli {
		cmd, err := fromEnv(s.envName, s.def)
		if err != nil {
			return nil, err
		}
		upper := strings.ToUpper(s.id)
		presets = append(presets, pyjson.NewObject(
			"id", s.id, "label", s.label, "description", s.description, "command", cmd,
			"prompt_mode", s.promptMode, "followup", false,
			"models", envList("ARK_TASK_"+upper+"_MODELS", nil),
			"efforts", envList("ARK_TASK_"+upper+"_EFFORTS", nil),
		))
	}
	presets = append(presets, pyjson.NewObject(
		"id", "shell", "label", "Local shell",
		"description", "A plain local shell; the initial prompt is delivered on stdin.",
		"command", shellCommand, "prompt_mode", "stdin", "followup", true,
		"models", envList("ARK_TASK_SHELL_MODELS", nil),
		"efforts", envList("ARK_TASK_SHELL_EFFORTS", nil),
	))
	if custom := pytext.Strip(env("ARK_TASK_CUSTOM_COMMAND")); custom != "" {
		cmd, err := SplitCommand(custom)
		if err != nil {
			return nil, err
		}
		label := env("ARK_TASK_CUSTOM_LABEL")
		if label == "" {
			label = "Custom command"
		}
		promptMode := "arg"
		if !strings.Contains(custom, "{prompt}") {
			promptMode = "stdin"
		}
		followRaw, ok := os.LookupEnv("ARK_TASK_CUSTOM_FOLLOWUP")
		if !ok {
			followRaw = "1"
		}
		var permission any
		if modes := envList("ARK_TASK_CUSTOM_PERMISSION_MODES", nil); len(modes) > 0 {
			permission = modes
		}
		presets = append(presets, pyjson.NewObject(
			"id", "custom", "label", label,
			"description", "Operator-defined command from ARK_TASK_CUSTOM_COMMAND.",
			"command", cmd, "prompt_mode", promptMode,
			"followup", set("1", "true", "yes")[pytext.Lower(followRaw)],
			"models", envList("ARK_TASK_CUSTOM_MODELS", nil),
			"efforts", envList("ARK_TASK_CUSTOM_EFFORTS", nil),
			"supports_approval", set("1", "true", "yes", "on")[pytext.Lower(pytext.Strip(env("ARK_TASK_CUSTOM_APPROVALS")))],
			"permission_modes", permission,
		))
	}
	out := make([]*pyjson.Object, 0, len(presets))
	for _, p := range presets {
		p.Set("available", Available(p))
		n, err := NormalizePreset(p)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}
