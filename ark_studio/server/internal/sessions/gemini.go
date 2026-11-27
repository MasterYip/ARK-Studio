package sessions

// Gemini CLI sessions (2026-11-27).  ARK runs Gemini CLI (npm
// @google/gemini-cli) in its headless mode, one process per turn, the way
// it runs `codex exec`:
//
//   gemini --output-format stream-json --model <m> --approval-mode <a>
//          [--include-directories <dir> ...] [--resume <id>] -p "<prompt>"
//
// stream-json prints one JSON event per stdout line (transcript_gemini.go):
// init {session_id, model}, message {role, content, delta}, tool_use
// {tool_name, tool_id, parameters}, tool_result {tool_id, status, output,
// error}, error {severity, message} and a closing result {status, error,
// stats}.  init's session_id is the native id (NativeSessionIDFromLine);
// later turns pass `--resume <id>`, so Gemini keeps the whole conversation
// and ARK sends only the new message.  Gemini has no native fork: forks
// start from a summary.
//
// Access modes map onto Gemini's approval modes.  In headless mode a tool
// that would ask for approval is refused, so:
//
//   read_only    --approval-mode default  read and search tools only
//   plan         --approval-mode plan     Gemini's read-only planning mode
//   full_access  --approval-mode yolo     every tool runs without asking
//
// A command that picks its own mode (ARK_TASK_GEMINI_COMMAND or the
// personal command with --approval-mode, --yolo or -y) is left alone.
// Gemini refuses non-default modes in folders it does not trust, and in
// headless mode it stops in an untrusted folder; ARK chose the folder, so
// the run gets GEMINI_CLI_TRUST_WORKSPACE=true (launch.go buildEnv).
//
// The session workspace and the task folder are passed as
// --include-directories when the agent works elsewhere (a worktree), so
// ARK-TASK.md, ark-context.json, artifacts and pasted images are inside
// Gemini's workspace.  Pasted images are referenced as @path in the
// message: Gemini reads them as inline image data.

import (
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/sessionstart"
)

// geminiMaxIncludeDirs is how many folders ARK adds with
// --include-directories (Gemini caps the workspace too).
const geminiMaxIncludeDirs = 5

// hasFlag reports an option in argv, as `name` or `name=value`.
func hasFlag(argv []string, names ...string) bool {
	for _, a := range argv {
		for _, n := range names {
			if a == n || strings.HasPrefix(a, n+"=") {
				return true
			}
		}
	}
	return false
}

// geminiHeadless: the command runs Gemini CLI's headless mode.
func geminiHeadless(argv []string) bool {
	return hasFlag(argv, "-p", "--prompt")
}

// geminiApprovalMode is the --approval-mode of an ARK access mode.
func geminiApprovalMode(access string) string {
	switch access {
	case "full_access":
		return "yolo"
	case "plan":
		return "plan"
	}
	return "default"
}

// geminiArgv adds ARK's options to a headless Gemini command (right after
// the program, so the prompt stays last).  Options the command sets itself
// are kept.
func geminiArgv(argv []string, model, access string, dirs []string) []string {
	if len(argv) < 2 || !geminiHeadless(argv) {
		return argv
	}
	var extra []string
	if !hasFlag(argv, "-o", "--output-format") {
		extra = append(extra, "--output-format", "stream-json")
	}
	if model != "" && !hasFlag(argv, "-m", "--model") {
		extra = append(extra, "--model", model)
	}
	if !hasFlag(argv, "--approval-mode", "--yolo", "-y") {
		extra = append(extra, "--approval-mode", geminiApprovalMode(access))
	}
	for _, d := range dirs {
		extra = append(extra, "--include-directories", d)
	}
	if len(extra) == 0 {
		return argv
	}
	return insertAt(argv, 1, extra...)
}

// within reports whether path is dir or inside it.
func within(path, dir string) bool {
	if path == "" || dir == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// geminiDirs are the folders Gemini needs besides its working folder: the
// session workspace and the task folder's places.
func geminiDirs(cwd, workspace string, folder *sessionstart.Folder) []string {
	var out []string
	add := func(p string) {
		if p == "" || !isDir(p) || within(p, cwd) || len(out) >= geminiMaxIncludeDirs {
			return
		}
		for _, have := range out {
			if within(p, have) {
				return
			}
		}
		out = append(out, p)
	}
	add(workspace)
	for _, p := range codexWritableDirs(folder) {
		add(p)
	}
	return out
}

// geminiAtPath is an @ reference Gemini CLI resolves to the file: quoted
// with forward slashes on Windows (Gemini strips the quotes there), special
// characters escaped with a backslash elsewhere.
func geminiAtPath(p string, windows bool) string {
	if windows {
		return `@"` + strings.ReplaceAll(p, `\`, "/") + `"`
	}
	var b strings.Builder
	b.WriteString("@")
	for _, r := range p {
		if strings.ContainsRune(" \t,;!?()[]{}\"'`\\", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// geminiMediaRefs is the line that hands pasted images to Gemini.
func geminiMediaRefs(paths []string, windows bool) string {
	if len(paths) == 0 {
		return ""
	}
	refs := make([]string, len(paths))
	for i, p := range paths {
		refs[i] = geminiAtPath(p, windows)
	}
	return "\n\nAttached files: " + strings.Join(refs, " ")
}

// mediaPaths are the absolute paths of the media entries.
func (m *Manager) mediaPaths(s *Session, entries []*pyjson.Object) []string {
	var out []string
	for _, e := range entries {
		if e.Value("kind") != "media" {
			continue
		}
		if target, _, err := m.artifactTarget(s, strOr(e.Value("path"))); err == nil {
			out = append(out, target)
		}
	}
	return out
}

// composeMissing adds the entries whose text the prompt does not contain yet.
func composeMissing(prompt string, entries []*pyjson.Object) string {
	var missing []*pyjson.Object
	for _, e := range entries {
		if text := strOr(e.Value("text")); text == "" || !strings.Contains(prompt, text) {
			missing = append(missing, e)
		}
	}
	if len(missing) == 0 {
		return prompt
	}
	return composeMessage(prompt, missing)
}

// geminiPrompt is a one-shot Gemini message with its pasted media.
func (m *Manager) geminiPrompt(s *Session, prompt string, entries []*pyjson.Object) string {
	return composeMissing(prompt, entries) + geminiMediaRefs(m.mediaPaths(s, entries), runtime.GOOS == "windows")
}

// ------------------------------------------------------------ turn end

// geminiTurn is how the running turn ended, as its output says.
type geminiTurn struct {
	outcome string // "done" or "error" from the result event
	reason  string // the result's error message
	lastErr string // the last error line on stderr (start-up failures)
	native  string // the init event's session id
}

var (
	geminiEventRe        = regexp.MustCompile(`"type"\s*:\s*"(?:result|init)"`)
	geminiResumeFailedRe = regexp.MustCompile(`(?i)^(?:\[ERROR\]\s*)?Error resuming session`)
	// Terminal and platform warnings, the API error dump (the result event
	// carries its message), and indented lines and closing brackets: stack
	// traces and the fields Node prints after them.
	geminiNoiseRe = regexp.MustCompile(`^(?:Warning: (?:Windows \d+ detected|Basic terminal detected|256-color support|True ?color)|` +
		`Ripgrep is not available|Loaded cached credentials|YOLO mode is enabled|Error when talking to Gemini API|` +
		`\s|[}\]][)\],;]*$)`)
	geminiErrorRe = regexp.MustCompile(`(?i)^(?:\[ERROR\]|Error\b|Fatal|Please set an Auth method|Gemini CLI is not running in a trusted)`)
	geminiTagRe   = regexp.MustCompile(`^\[(ERROR|WARNING|WARN|INFO|DEBUG|LOG)\]\s*`)
)

// geminiStderrKind sorts a Gemini stderr line: "drop", "error" or a
// notice level ("warning", "info").
func geminiStderrKind(line string) string {
	text := pytext.Strip(line)
	switch {
	case text == "" || geminiNoiseRe.MatchString(line):
		return "drop"
	case geminiErrorRe.MatchString(text):
		return "error"
	}
	if m := geminiTagRe.FindStringSubmatch(text); m != nil && (m[1] == "INFO" || m[1] == "DEBUG" || m[1] == "LOG") {
		return "info"
	}
	return "warning"
}

// geminiResultOutcome reads a stream-json result event.
func geminiResultOutcome(obj *pyjson.Object) (string, string) {
	if pytext.Lower(strOr(obj.Value("status"))) != "error" {
		return "done", ""
	}
	e, _ := obj.Value("error").(*pyjson.Object)
	if e != nil && strOr(e.Value("type")) == "FatalCancellationError" {
		return "error", "interrupted"
	}
	reason := ""
	if e != nil {
		reason = strOr(e.Value("message"))
	}
	if reason == "" {
		reason = "Gemini turn failed"
	}
	return "error", reason
}

// observeGemini follows a Gemini turn: its session id, its result and its
// stderr errors.
//
// Gemini saves a conversation only once a turn got an answer: a first turn
// that failed (no sign-in, API error) leaves nothing to resume, and
// `--resume <id>` would then fail on every later message.  So the id from
// init is kept only after a successful result (observeNativeID skips
// Gemini), and a "cannot resume" error forgets it: the next message starts
// a new conversation with the earlier output as context (ContinueSession).
func (m *Manager) observeGemini(s *Session, stream, text string) {
	m.mu.Lock()
	gemini := strOr(s.Meta.Value("platform")) == "gemini"
	m.mu.Unlock()
	if !gemini {
		return
	}
	if stream == "stderr" {
		if geminiStderrKind(text) != "error" {
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		s.gemini.lastErr = limit(pytext.Strip(text), 2000)
		if geminiResumeFailedRe.MatchString(text) && s.Meta.Has("native_session_id") {
			s.Meta.Delete("native_session_id")
			m.saveLocked(s)
		}
		return
	}
	trimmed := pytext.Strip(text)
	if !strings.HasPrefix(trimmed, "{") || !geminiEventRe.MatchString(trimmed) {
		return
	}
	obj, ok := jsonLineObject(trimmed)
	if !ok {
		return
	}
	switch obj.Value("type") {
	case "init":
		if id := strOr(obj.Value("session_id")); nativeIDRe.MatchString(id) {
			m.mu.Lock()
			s.gemini.native = id
			m.mu.Unlock()
		}
	case "result":
		outcome, reason := geminiResultOutcome(obj)
		m.mu.Lock()
		s.gemini.outcome, s.gemini.reason = outcome, reason
		if outcome == "done" && s.gemini.native != "" && strOr(s.Meta.Value("native_session_id")) != s.gemini.native {
			s.Meta.Set("native_session_id", s.gemini.native)
			m.saveLocked(s)
		}
		m.mu.Unlock()
	}
}

// failure is why the turn failed ("" when it did not, or when nothing
// better than the exit code is known).
func (t geminiTurn) failure(code *int) string {
	if t.outcome == "error" {
		return t.reason
	}
	if code == nil || *code == 0 {
		return ""
	}
	if t.lastErr != "" {
		return t.lastErr
	}
	switch *code {
	case 130, 143, 129:
		return "interrupted (exit code " + strconv.Itoa(*code) + ")"
	}
	return ""
}
