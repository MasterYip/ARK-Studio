package sessions

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/sessionstart"
)

// fakeGemini is a headless Gemini CLI for the session tests: it reports its
// argv, stdin and trust variable, and prints stream-json events.
func fakeGemini() {
	in, _ := io.ReadAll(os.Stdin)
	prompt := ""
	native := "0b7c2f1e-5d4a-4c39-9a51-000000000001"
	for i, a := range os.Args {
		if (a == "-p" || a == "--prompt") && i+1 < len(os.Args) {
			prompt = os.Args[i+1]
		}
		if a == "--resume" && i+1 < len(os.Args) {
			native = os.Args[i+1]
		}
	}
	if path := os.Getenv("ARK_FAKE_ARGS_FILE"); path != "" {
		b, _ := json.Marshal(map[string]any{"args": os.Args[1:], "stdin": string(in),
			"trust": os.Getenv("GEMINI_CLI_TRUST_WORKSPACE")})
		_ = os.WriteFile(path+".tmp", b, 0o644)
		_ = os.Rename(path+".tmp", path)
	}
	if strings.Contains(prompt, "no auth") {
		fmt.Fprintln(os.Stderr, "Warning: Basic terminal detected (TERM=dumb). Visual rendering will be limited.")
		fmt.Fprintln(os.Stderr, "Please set an Auth method in your settings.json or specify one of the following environment variables before running: GEMINI_API_KEY")
		os.Exit(41)
	}
	emit := func(v map[string]any) {
		v["timestamp"] = "2026-11-27T05:00:00.000Z"
		b, _ := json.Marshal(v)
		fmt.Println(string(b))
	}
	emit(map[string]any{"type": "init", "session_id": native, "model": "auto"})
	emit(map[string]any{"type": "message", "role": "user", "content": string(in) + prompt})
	if strings.Contains(fakeLastLine(prompt), "resume") && native != "0b7c2f1e-5d4a-4c39-9a51-000000000001" {
		fmt.Fprintln(os.Stderr, "Error resuming session: No previous sessions found for this project.")
		os.Exit(42)
	}
	if strings.Contains(fakeLastLine(prompt), "quota") {
		emit(map[string]any{"type": "result", "status": "error",
			"error": map[string]any{"type": "Error", "message": "[API Error: Quota exceeded for this model.]"},
			"stats": map[string]any{"total_tokens": 0}})
		os.Exit(1)
	}
	emit(map[string]any{"type": "message", "role": "assistant", "content": "gemini ", "delta": true})
	emit(map[string]any{"type": "message", "role": "assistant", "content": "echo: " + fakeLastLine(prompt), "delta": true})
	emit(map[string]any{"type": "result", "status": "success",
		"stats": map[string]any{"total_tokens": 12, "input_tokens": 10, "output_tokens": 2, "cached": 0}})
}

func fakeLastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

type geminiReport struct {
	Args  []string `json:"args"`
	Stdin string   `json:"stdin"`
	Trust string   `json:"trust"`
}

func geminiManager(t *testing.T) (*Manager, string) {
	t.Helper()
	m, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	m.Presets = []*pyjson.Object{pyjson.NewObject("id", "gemini", "label", "Gemini CLI", "available", true,
		"command", []any{exe, "-p", "{prompt}"}, "prompt_mode", "arg", "followup", false,
		"permission_modes", []any{"read_only", "plan", "full_access"})}
	report := filepath.Join(t.TempDir(), "report.json")
	t.Setenv("ARK_FAKE_GEMINI", "1")
	t.Setenv("ARK_FAKE_ARGS_FILE", report)
	t.Cleanup(func() { killAll(m) })
	return m, report
}

func readGeminiReport(t *testing.T, path string) geminiReport {
	t.Helper()
	var r geminiReport
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(path)
	return r
}

func argAfter(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestGeminiArgv(t *testing.T) {
	base := []string{"gemini", "-p", "do it"}
	got := geminiArgv(base, "gemini-2.5-pro", "read_only", []string{"/ws"})
	want := "gemini --output-format stream-json --model gemini-2.5-pro --approval-mode default --include-directories /ws -p do it"
	if strings.Join(got, " ") != want {
		t.Fatalf("argv:\n %q\nwant %q", strings.Join(got, " "), want)
	}
	for access, mode := range map[string]string{"read_only": "default", "plan": "plan", "full_access": "yolo", "": "default"} {
		if a := argAfter(geminiArgv(base, "", access, nil), "--approval-mode"); a != mode {
			t.Errorf("%s -> %s, want %s", access, a, mode)
		}
	}
	// No model: Gemini's default.  The command's own choices stay.
	if got := geminiArgv(base, "", "full_access", nil); argAfter(got, "--model") != "" {
		t.Errorf("model without a choice: %q", got)
	}
	own := []string{"gemini", "-y", "-o", "json", "-m", "flash", "-p", "x"}
	if got := geminiArgv(own, "pro", "read_only", nil); strings.Join(got, " ") != strings.Join(own, " ") {
		t.Errorf("own options changed: %q", got)
	}
	if got := geminiArgv([]string{"gemini", "--approval-mode=plan", "-p", "x"}, "", "full_access", nil); strings.Contains(strings.Join(got, " "), "yolo") {
		t.Errorf("own approval mode replaced: %q", got)
	}
	// Interactive commands are not headless runs: left alone.
	if got := geminiArgv([]string{"gemini", "x"}, "pro", "full_access", nil); len(got) != 2 {
		t.Errorf("interactive command changed: %q", got)
	}
	// Resume goes ahead of the prompt.
	r := resumeArgv([]string{"gemini", "-p", "next"}, "gemini", "0b7c2f1e-5d4a", "next")
	if strings.Join(r, " ") != "gemini --resume 0b7c2f1e-5d4a -p next" {
		t.Errorf("resume: %q", r)
	}
	if r := resumeArgv([]string{"gemini", "-r", "latest", "-p", "x"}, "gemini", "abc12345", "x"); strings.Contains(strings.Join(r, " "), "abc12345") {
		t.Errorf("own resume replaced: %q", r)
	}
	if _, ok := forkArgv([]string{"gemini", "-p", "x"}, "gemini", "abc12345", "x"); ok {
		t.Error("Gemini has no native fork")
	}
}

func TestGeminiDirs(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "s1", "workspace")
	wt := filepath.Join(root, "wt")
	task := filepath.Join(root, "tasks", "T-1")
	for _, d := range []string{filepath.Join(ws, "sub"), wt, task} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Working in the workspace: nothing to add; in a subfolder of it, the
	// workspace (ARK-TASK.md is at its top).
	if got := geminiDirs(ws, ws, nil); len(got) != 0 {
		t.Errorf("in the workspace: %q", got)
	}
	if got := geminiDirs(filepath.Join(ws, "sub"), ws, nil); len(got) != 1 || got[0] != ws {
		t.Errorf("in a subfolder: %q", got)
	}
	// Working in a worktree: the workspace and the task folder.
	got := geminiDirs(wt, ws, &sessionstart.Folder{Path: task, Writable: []string{task, filepath.Join(root, "missing")}})
	if len(got) != 2 || got[0] != ws || got[1] != task {
		t.Errorf("worktree: %q", got)
	}
}

func TestGeminiAtPath(t *testing.T) {
	if got := geminiAtPath(`C:\Users\user b\ws\img 1.png`, true); got != `@"C:/Users/user b/ws/img 1.png"` {
		t.Errorf("windows: %s", got)
	}
	if got := geminiAtPath("/home/user b/ws/x(1).png", false); got != `@/home/user\ b/ws/x\(1\).png` {
		t.Errorf("posix: %s", got)
	}
	if got := geminiMediaRefs(nil, false); got != "" {
		t.Errorf("no media: %q", got)
	}
	if got := geminiMediaRefs([]string{"/a.png", "/b.png"}, false); got != "\n\nAttached files: @/a.png @/b.png" {
		t.Errorf("refs: %q", got)
	}
	entry := pyjson.NewObject("kind", "media", "text", "### Media `a.png`")
	if got := composeMissing("look\n\n## Attached context\n\n### Media `a.png`", []*pyjson.Object{entry}); strings.Count(got, "### Media") != 1 {
		t.Errorf("context repeated: %q", got)
	}
	if got := composeMissing("look", []*pyjson.Object{entry}); !strings.Contains(got, "### Media `a.png`") {
		t.Errorf("context missing: %q", got)
	}
}

func TestGeminiStderrKind(t *testing.T) {
	for line, want := range map[string]string{
		"Warning: Windows 10 detected. Some UI features like smooth scrolling may be degraded.": "drop",
		"Warning: Basic terminal detected (TERM=dumb).":                                         "drop",
		"Ripgrep is not available. Falling back to GrepTool.":                                   "drop",
		"    at throwErrorIfNotOK (file:///x/chunk.js:1:2)":                                     "drop",
		"  status: 400": "drop",
		"}":             "drop",
		"Error when talking to Gemini API Full report available at: /tmp/r.json": "drop",
		"Please set an Auth method in your settings.json":                        "error",
		"[ERROR] Exiting due to an error processing the @ command.":              "error",
		"[WARNING] Agent execution blocked: policy":                              "warning",
		"[INFO] Using cached extensions":                                         "info",
		"Some new message":                                                       "warning",
	} {
		if got := geminiStderrKind(line); got != want {
			t.Errorf("%q: %s, want %s", line, got, want)
		}
	}
}

func TestGeminiTurnEnd(t *testing.T) {
	ok := pyjson.NewObject("type", "result", "status", "success")
	if o, r := geminiResultOutcome(ok); o != "done" || r != "" {
		t.Errorf("success: %s %q", o, r)
	}
	failed := pyjson.NewObject("type", "result", "status", "error",
		"error", pyjson.NewObject("type", "Error", "message", "[API Error: API key not valid.]"))
	if o, r := geminiResultOutcome(failed); o != "error" || r != "[API Error: API key not valid.]" {
		t.Errorf("error: %s %q", o, r)
	}
	cancelled := pyjson.NewObject("type", "result", "status", "error",
		"error", pyjson.NewObject("type", "FatalCancellationError", "message", "Operation cancelled."))
	if o, r := geminiResultOutcome(cancelled); o != "error" || r != "interrupted" {
		t.Errorf("cancelled: %s %q", o, r)
	}
	if o, r := geminiResultOutcome(pyjson.NewObject("type", "result", "status", "error")); o != "error" || r != "Gemini turn failed" {
		t.Errorf("bare error: %s %q", o, r)
	}
	code := func(n int) *int { return &n }
	for _, c := range []struct {
		turn geminiTurn
		code *int
		want string
	}{
		{geminiTurn{outcome: "done"}, code(0), ""},
		{geminiTurn{outcome: "error", reason: "quota"}, code(1), "quota"},
		{geminiTurn{outcome: "error", reason: "blocked"}, code(0), "blocked"},
		{geminiTurn{lastErr: "Please set an Auth method"}, code(41), "Please set an Auth method"},
		{geminiTurn{}, code(130), "interrupted (exit code 130)"},
		{geminiTurn{}, code(2), ""},
		{geminiTurn{lastErr: "x"}, nil, ""},
	} {
		if got := c.turn.failure(c.code); got != c.want {
			t.Errorf("%+v: %q, want %q", c.turn, got, c.want)
		}
	}
}

func TestGeminiTranscriptTools(t *testing.T) {
	items := transcriptItems(t, "gemini", fixtureRows(t, "gemini_stream_tools.jsonl"), nil)
	assertClean(t, items)
	var kinds []string
	for _, it := range items {
		kinds = append(kinds, strOr(it.Value("type"))+":"+strOr(or(it.Value("level"), or(it.Value("role"), it.Value("name")))))
	}
	want := "message:assistant tool:read_file tool:replace tool:run_shell_command tool:write_file notice:warning message:assistant"
	if got := strings.Join(kinds, " "); got != want {
		t.Fatalf("blocks:\n %s\nwant %s", got, want)
	}
	// The prompt echo (with the session block) is not shown; pieces join.
	if txt := strOr(items[0].Value("text")); txt != "我先读一下文件。\n" {
		t.Errorf("first message: %q", txt)
	}
	if !items[0].Value("markdown").(bool) {
		t.Error("assistant text is Markdown")
	}
	tools := byType(items, "tool")
	if strOr(toolInput(tools[1]).Value("new_string")) != "hello" || tools[1].Value("status") != "ok" {
		t.Errorf("replace: %v", tools[1])
	}
	if strOr(toolInput(tools[2]).Value("command")) != "git diff --stat" || !strings.Contains(strOr(tools[2].Value("output")), "1 file changed") {
		t.Errorf("shell: %v", tools[2])
	}
	if tools[3].Value("status") != "error" || !strings.Contains(strOr(tools[3].Value("output")), "within one of the workspace directories") {
		t.Errorf("failed tool: %v", tools[3])
	}
	got := BuildTranscript(TranscriptInput{SessionID: "s_g", Status: "stopped", Platform: "gemini",
		Stdout: fixtureRows(t, "gemini_stream_tools.jsonl")})
	usage := got.Value("usage").(*pyjson.Object)
	if usage.Value("source") != "reported" || usage.Value("total_tokens") != int64(5321) || usage.Value("cache_read_input_tokens") != int64(4096) {
		t.Errorf("usage: %v", usage)
	}
}

func TestGeminiTranscriptAPIError(t *testing.T) {
	stdout := fixtureRows(t, "gemini_api_error.jsonl")
	stderr := fixtureRows(t, "gemini_api_error_stderr.txt")
	got := BuildTranscript(TranscriptInput{SessionID: "s_g", Status: "errored", Platform: "gemini", Stdout: stdout, Stderr: stderr,
		Error: "[API Error: API key not valid. Please pass a valid API key.]"})
	items := objects(got.Value("items"))
	assertClean(t, items)
	// Recorded with Gemini CLI 0.71.1 and an invalid key: the terminal
	// warnings and the stack dump are dropped, the error shows once.
	if len(items) != 1 || items[0].Value("type") != "error" || !strings.Contains(strOr(items[0].Value("text")), "API key not valid") {
		var dump []string
		for _, it := range items {
			dump = append(dump, strOr(it.Value("type"))+": "+strOr(it.Value("text")))
		}
		t.Fatalf("items:\n%s", strings.Join(dump, "\n"))
	}
	if NativeSessionIDFromLine(strOr(stdout[0].Value("line"))) != "ac861928-2d1b-484d-ba44-fda26c35e438" {
		t.Error("native id from init")
	}
}

func TestGeminiSessionTurns(t *testing.T) {
	m, report := geminiManager(t)
	s, err := m.Start(nil, pyjson.NewObject("platform", "gemini", "prompt", "hello there", "model", "flash",
		"access_mode", "full_access"), "http://127.0.0.1:1", "alice")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first turn", func() bool { return !m.IsActive(s) })
	r := readGeminiReport(t, report)
	if argAfter(r.Args, "--output-format") != "stream-json" || argAfter(r.Args, "--approval-mode") != "yolo" ||
		argAfter(r.Args, "--model") != "flash" || argAfter(r.Args, "--resume") != "" || r.Trust != "true" {
		t.Fatalf("first run: %q trust=%q", r.Args, r.Trust)
	}
	if native := strOr(metaOf(m, s, "native_session_id")); native != "0b7c2f1e-5d4a-4c39-9a51-000000000001" {
		t.Fatalf("native id %q", native)
	}
	if f := finishByID(m, s.ID); f.Outcome != "done" {
		t.Fatalf("first finish: %+v", f)
	}
	// The next message resumes the conversation: only the new text goes.
	if _, err := m.ContinueSession(nil, s.ID, "and now this", ContinueOptions{Actor: "alice"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second turn", func() bool { return !m.IsActive(s) })
	r = readGeminiReport(t, report)
	if argAfter(r.Args, "--resume") != "0b7c2f1e-5d4a-4c39-9a51-000000000001" || argAfter(r.Args, "-p") != "and now this" {
		t.Fatalf("second run: %q", r.Args)
	}
	tr, err := m.Transcript(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, it := range objects(tr.Value("items")) {
		texts = append(texts, strOr(it.Value("role"))+": "+strOr(it.Value("text")))
	}
	if got := strings.Join(texts, " | "); got != "user: hello there | assistant: gemini echo: hello there | user: and now this | assistant: gemini echo: and now this" {
		t.Fatalf("transcript: %s", got)
	}
	// A failed turn: the result's message is the reason.
	if _, err := m.ContinueSession(nil, s.ID, "use up the quota", ContinueOptions{Actor: "alice"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "failed turn", func() bool { return !m.IsActive(s) })
	if f := finishByID(m, s.ID); f.Outcome != "error" || f.Reason != "[API Error: Quota exceeded for this model.]" {
		t.Fatalf("failed finish: %+v", f)
	}
	if e := strOr(metaOf(m, s, "error")); e != "[API Error: Quota exceeded for this model.]" {
		t.Fatalf("error %q", e)
	}
	tr, _ = m.Transcript(s.ID)
	errs := byType(objects(tr.Value("items")), "error")
	if len(errs) != 1 {
		t.Fatalf("errors shown: %d", len(errs))
	}
}

func TestGeminiStartupFailure(t *testing.T) {
	m, report := geminiManager(t)
	s, err := m.Start(nil, pyjson.NewObject("platform", "gemini", "prompt", "no auth here"), "http://127.0.0.1:1", "alice")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "run", func() bool { return !m.IsActive(s) })
	if r := readGeminiReport(t, report); argAfter(r.Args, "--approval-mode") != "default" {
		t.Fatalf("read-only run: %q", r.Args)
	}
	f := finishByID(m, s.ID)
	if f.Outcome != "error" || !strings.HasPrefix(f.Reason, "Please set an Auth method") {
		t.Fatalf("finish: %+v", f)
	}
	tr, _ := m.Transcript(s.ID)
	items := objects(tr.Value("items"))
	errs := byType(items, "error")
	if len(errs) != 1 || len(byType(items, "notice")) != 0 {
		t.Fatalf("items: %v", items)
	}
}

func TestGeminiNativeIDAfterSuccess(t *testing.T) {
	m, report := geminiManager(t)
	// A first turn that fails leaves no Gemini conversation: no id is kept.
	s, err := m.Start(nil, pyjson.NewObject("platform", "gemini", "prompt", "check the quota"), "http://127.0.0.1:1", "alice")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "failed turn", func() bool { return !m.IsActive(s) })
	readGeminiReport(t, report)
	if native := metaOf(m, s, "native_session_id"); native != nil {
		t.Fatalf("id kept after a failed turn: %v", native)
	}
	// The next message starts a new conversation with the earlier output.
	if _, err := m.ContinueSession(nil, s.ID, "try again", ContinueOptions{Actor: "alice"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second turn", func() bool { return !m.IsActive(s) })
	r := readGeminiReport(t, report)
	if argAfter(r.Args, "--resume") != "" || !strings.Contains(argAfter(r.Args, "-p"), "## User correction\ntry again") {
		t.Fatalf("second run: %q", r.Args)
	}
	if native := strOr(metaOf(m, s, "native_session_id")); native == "" {
		t.Fatal("no id after a successful turn")
	}
	// Gemini lost the conversation: the id is dropped, the turn fails once.
	m.mu.Lock()
	s.Meta.Set("native_session_id", "0b7c2f1e-5d4a-4c39-9a51-0000000000ff")
	m.mu.Unlock()
	if _, err := m.ContinueSession(nil, s.ID, "resume please", ContinueOptions{Actor: "alice"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "lost conversation", func() bool { return !m.IsActive(s) })
	readGeminiReport(t, report)
	if f := finishByID(m, s.ID); f.Outcome != "error" || !strings.HasPrefix(f.Reason, "Error resuming session") {
		t.Fatalf("finish: %+v", f)
	}
	if native := metaOf(m, s, "native_session_id"); native != nil {
		t.Fatalf("lost id kept: %v", native)
	}
}
