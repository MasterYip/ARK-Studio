package sessions

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/sessionstart"
)

// fakeEcho is a one-shot agent: it writes {"args": argv[1:], "stdin": ...}
// to ARK_FAKE_ARGS_FILE and exits.
func fakeEcho() {
	if len(os.Args) == 2 && os.Args[1] == "--version" { // the codex version probe (codex_sandbox.go)
		os.Stdout.WriteString("codex-cli 0.200.0\n")
		return
	}
	in, _ := io.ReadAll(os.Stdin)
	b, _ := json.Marshal(map[string]any{"args": os.Args[1:], "stdin": string(in)})
	path := os.Getenv("ARK_FAKE_ARGS_FILE")
	_ = os.WriteFile(path+".tmp", b, 0o644)
	_ = os.Rename(path+".tmp", path)
}

type echoReport struct {
	Args  []string `json:"args"`
	Stdin string   `json:"stdin"`
}

// echoManager runs presets whose command is the test binary as fakeEcho.
func echoManager(t *testing.T, presets ...*pyjson.Object) (*Manager, string) {
	t.Helper()
	m, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.Presets = presets
	report := filepath.Join(t.TempDir(), "report.json")
	t.Setenv("ARK_FAKE_ECHO", "1")
	t.Setenv("ARK_FAKE_ARGS_FILE", report)
	t.Cleanup(func() { killAll(m) })
	return m, report
}

func echoPreset(id string, command ...string) *pyjson.Object {
	exe, _ := os.Executable()
	cmd := []any{exe}
	for _, c := range command {
		cmd = append(cmd, c)
	}
	return pyjson.NewObject("id", id, "label", id, "available", true, "command", cmd, "prompt_mode", "arg", "followup", false)
}

func readReport(t *testing.T, path string) echoReport {
	t.Helper()
	var r echoReport
	waitFor(t, "the agent's report", func() bool {
		b, err := os.ReadFile(path)
		return err == nil && json.Unmarshal(b, &r) == nil
	})
	return r
}

// longText is n characters of mixed ASCII and CJK text with line breaks and
// an end marker, so a cut anywhere shows.
func longText(n int) string {
	var b strings.Builder
	unit := []rune("ARK 长文本 line of guidance text. ")
	for i := 0; b.Len() < 4*n; i++ {
		b.WriteString(string(unit))
		if i%7 == 6 {
			b.WriteString("\n")
		}
	}
	r := []rune(b.String())[:n-len("<END>")]
	return string(r) + "<END>"
}

func assertShortArgs(t *testing.T, args []string) {
	t.Helper()
	for _, a := range args {
		if len(a) > argPromptMax+1000 {
			t.Fatalf("an argument of %d bytes reached the command line", len(a))
		}
	}
}

func TestLongPromptReachesCodexOnStdin(t *testing.T) {
	m, report := echoManager(t, echoPreset("codex", "exec", "--json", "{prompt}"))
	prompt := longText(50_000)
	if _, err := m.Start(nil, pyjson.NewObject("platform", "codex", "prompt", prompt), "http://127.0.0.1:1", "alice"); err != nil {
		t.Fatal(err)
	}
	r := readReport(t, report)
	assertShortArgs(t, r.Args)
	if !contains(r.Args, "-") {
		t.Fatalf("codex exec did not get '-' for stdin: %q", r.Args)
	}
	if r.Stdin != prompt {
		t.Fatalf("stdin has %d bytes, the prompt %d", len(r.Stdin), len(prompt))
	}
}

func TestShortPromptStaysAnArgument(t *testing.T) {
	m, report := echoManager(t, echoPreset("codex", "exec", "--json", "{prompt}"))
	if _, err := m.Start(nil, pyjson.NewObject("platform", "codex", "prompt", "short one"), "http://127.0.0.1:1", "alice"); err != nil {
		t.Fatal(err)
	}
	r := readReport(t, report)
	if !contains(r.Args, "short one") || r.Stdin != "" {
		t.Fatalf("a short prompt is an argument: %q stdin %q", r.Args, r.Stdin)
	}
}

func TestLongPromptGoesToAFileForOtherAgents(t *testing.T) {
	m, report := echoManager(t, echoPreset("opencode", "run", "{prompt}"))
	prompt := longText(50_000)
	s, err := m.Start(nil, pyjson.NewObject("platform", "opencode", "prompt", prompt), "http://127.0.0.1:1", "alice")
	if err != nil {
		t.Fatal(err)
	}
	r := readReport(t, report)
	assertShortArgs(t, r.Args)
	path := filepath.Join(s.Folder, "workspace", promptFileName)
	last := r.Args[len(r.Args)-1]
	if !strings.Contains(last, path) || !strings.Contains(last, "50000 characters") {
		t.Fatalf("the argument does not point at the prompt file: %q", last)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != prompt {
		t.Fatalf("prompt file has %d bytes (err %v), the prompt %d", len(b), err, len(prompt))
	}
}

func TestLongPromptClaudeOneShotUsesStdin(t *testing.T) {
	t.Setenv("ARK_CLAUDE_STREAM", "0")
	m, report := echoManager(t, echoPreset("claude", "-p", "{prompt}"))
	prompt := longText(50_000)
	if _, err := m.Start(nil, pyjson.NewObject("platform", "claude", "prompt", prompt), "http://127.0.0.1:1", "alice"); err != nil {
		t.Fatal(err)
	}
	r := readReport(t, report)
	assertShortArgs(t, r.Args)
	if r.Stdin != prompt || !contains(r.Args, "-p") {
		t.Fatalf("claude -p: args %q, stdin %d bytes of %d", r.Args, len(r.Stdin), len(prompt))
	}
}

func TestLongPromptReachesClaudeStream(t *testing.T) {
	m := streamManager(t)
	got := filepath.Join(t.TempDir(), "stdin.txt")
	t.Setenv("ARK_FAKE_STDIN_FILE", got)
	prompt := longText(50_000)
	s, err := m.Start(nil, pyjson.NewObject("platform", "claude", "prompt", prompt), "http://127.0.0.1:1", "alice")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the first message", func() bool {
		b, _ := os.ReadFile(got)
		return strings.HasSuffix(string(b), "<END>")
	})
	if b, _ := os.ReadFile(got); string(b) != prompt {
		t.Fatalf("claude got %d bytes, the prompt has %d", len(b), len(prompt))
	}
	// A long follow-up message: delivered and recorded whole.
	_ = os.Remove(got)
	waitFor(t, "the first turn to end", func() bool { return metaOf(m, s, "turn_state") == "waiting" })
	if _, err := m.SendInput(nil, s.ID, prompt, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the follow-up", func() bool {
		b, _ := os.ReadFile(got)
		return strings.HasSuffix(string(b), "<END>")
	})
	if b, _ := os.ReadFile(got); string(b) != prompt {
		t.Fatalf("the follow-up arrived with %d bytes of %d", len(b), len(prompt))
	}
	if !strings.Contains(logText(m, s), "<END>") || strings.Contains(logText(m, s), "…[truncated]") {
		t.Error("the session log cut the message")
	}
}

// startHooks give a session a task with a long send-back comment.
func startHooks(comment string) *Hooks {
	return &Hooks{
		CoreRules: func(string, string) string { return "C1. Read the context first." },
		StartPoint: func(in StartInput) *sessionstart.Start {
			st := sessionstart.Compose(sessionstart.Facts{Prompt: in.Prompt, Chosen: in.Chosen, Task: &sessionstart.Task{
				UID: "T-0042", Title: "Sidebar", Status: "active",
				Open: []sessionstart.Comment{{ID: 7, Kind: "sent_back", Author: "alice", Body: comment}}}})
			return &st
		},
		Unit: func(uid string) (*pyjson.Object, error) {
			if uid != "T-0042" {
				return nil, errors.New("missing")
			}
			return pyjson.NewObject("uid", "T-0042", "title", "Sidebar", "type", "task", "status", "active",
				"body", longText(30_000)), nil
		},
	}
}

func TestComposedStartIsNotCut(t *testing.T) {
	comment := longText(20_000)
	m, report := echoManager(t, echoPreset("codex", "exec", "{prompt}"))
	body := pyjson.NewObject("platform", "codex", "prompt", longText(15_000), "task", "T-0042",
		"context", []any{pyjson.NewObject("kind", "unit", "uid", "T-0042"),
			pyjson.NewObject("kind", "text", "text", longText(12_000), "label", "notes")})
	if _, err := m.Start(startHooks(comment), body, "http://127.0.0.1:1", "alice"); err != nil {
		t.Fatal(err)
	}
	r := readReport(t, report)
	assertShortArgs(t, r.Args)
	// The session block (with the send-back), the prompt, the task unit and
	// the snippet each arrive whole.
	if !strings.Contains(r.Stdin, strings.ReplaceAll(comment, "\n", "\n  ")) {
		t.Error("the send-back comment was cut")
	}
	if strings.Count(r.Stdin, "<END>") != 4 {
		t.Fatalf("want 4 whole texts (send-back, prompt, unit, snippet), got %d in %d bytes", strings.Count(r.Stdin, "<END>"), len(r.Stdin))
	}
}

func TestLongSessionBlockGoesToAFileForClaude(t *testing.T) {
	m := streamManager(t)
	args := filepath.Join(t.TempDir(), "args.json")
	t.Setenv("ARK_FAKE_ARGS_FILE", args)
	comment := longText(20_000)
	s, err := m.Start(startHooks(comment), pyjson.NewObject("platform", "claude", "prompt", "go", "task", "T-0042"),
		"http://127.0.0.1:1", "alice")
	if err != nil {
		t.Fatal(err)
	}
	var argv []string
	waitFor(t, "the argv", func() bool {
		b, err := os.ReadFile(args)
		return err == nil && json.Unmarshal(b, &argv) == nil
	})
	assertShortArgs(t, argv)
	i := indexOf(argv, "--append-system-prompt-file")
	if i < 0 || contains(argv, "--append-system-prompt") {
		t.Fatalf("a long session block goes in a file: %q", argv)
	}
	want := filepath.Join(s.Folder, "workspace", sessionBlockFileName)
	if argv[i+1] != want {
		t.Fatalf("file %q, want %q", argv[i+1], want)
	}
	b, _ := os.ReadFile(want)
	if !strings.Contains(string(b), "ARK SESSION") || !strings.Contains(string(b), strings.ReplaceAll(comment, "\n", "\n  ")) {
		t.Fatal("the session block file lacks the block or the whole send-back")
	}
}

func TestOverCapPromptIsRefused(t *testing.T) {
	m, _ := echoManager(t, echoPreset("codex", "exec", "{prompt}"))
	_, err := m.Start(nil, pyjson.NewObject("platform", "codex", "prompt", strings.Repeat("x", maxPrompt+1)), "http://127.0.0.1:1", "alice")
	var e *Error
	if !errors.As(err, &e) || e.Status != 413 || e.Code != "PROMPT_TOO_LONG" || !strings.Contains(e.Message, "1000000") {
		t.Fatalf("want a 413 naming the limit, got %v", err)
	}
	// Characters count, not bytes: a cap-length CJK prompt passes the check.
	if err := checkPrompt("x", strings.Repeat("长", maxPrompt)); err != nil {
		t.Fatalf("a %d-character prompt is allowed: %v", maxPrompt, err)
	}
	if got := cutMarked("abcdef", 4); got != "abcd\n[… 2 characters cut]" {
		t.Fatalf("cutMarked: %q", got)
	}
}

// On Windows the whole command line is at most 32,767 UTF-16 units: argv
// with a long prompt never carries it (stdin or a file instead).
func TestLongPromptArgvSelection(t *testing.T) {
	dir := t.TempDir()
	long := longText(40_000)
	cases := []struct {
		id      string
		argv    []string
		stdin   bool
		file    bool
		follows bool
	}{
		{"codex", []string{"codex", "exec", "--json", long}, true, false, false},
		{"codex", []string{"codex", "exec", "resume", "abc", long}, true, false, false},
		{"claude", []string{"claude", "-p", long}, true, false, false},
		{"gemini", []string{"gemini", "-p", long}, true, false, false},
		{"cursor", []string{"cursor-agent", "-p", long}, false, true, false},
		{"custom", []string{"sh", "-c", "run --prompt='" + long + "'"}, false, true, false},
		{"codex", []string{"codex", "exec", long}, false, true, true}, // stdin is busy for a followup agent
	}
	for _, c := range cases {
		preset := pyjson.NewObject("id", c.id, "followup", c.follows)
		out, stdin, err := longPromptArgv(c.id, preset, c.argv, long, dir)
		if err != nil {
			t.Fatal(err)
		}
		total := 0
		for _, a := range out {
			total += len(a) + 3
		}
		if total > 32_767/2 {
			t.Errorf("%s %q: command line of %d bytes", c.id, c.argv[:2], total)
		}
		if (stdin == long) != c.stdin {
			t.Errorf("%s %q: stdin %v, want %v", c.id, c.argv[:2], stdin == long, c.stdin)
		}
		if c.file {
			b, _ := os.ReadFile(filepath.Join(dir, promptFileName))
			if string(b) != long || !strings.Contains(strings.Join(out, " "), promptFileName) {
				t.Errorf("%s: the prompt file is missing or not named", c.id)
			}
			_ = os.Remove(filepath.Join(dir, promptFileName))
		}
	}
}
