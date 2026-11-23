package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

// fixtureRows reads a testdata file as log entries, one per line.
func fixtureRows(t *testing.T, name string) []*pyjson.Object {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var rows []*pyjson.Object
	for i, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		rows = append(rows, pyjson.NewObject("t", "2026-11-23T09:00:"+pad2(i)+"+00:00", "line", line))
	}
	return rows
}

func transcriptItems(t *testing.T, platform string, stdout, stderr []*pyjson.Object) []*pyjson.Object {
	t.Helper()
	got := BuildTranscript(TranscriptInput{SessionID: "s_codex000001", Status: "exited", Stdout: stdout, Stderr: stderr, Platform: platform})
	return objects(got.Value("items"))
}

// assertClean: no raw JSON, escape sequence, carriage return or Codex log
// line reaches the reader, and Chinese text arrives intact.
func assertClean(t *testing.T, items []*pyjson.Object) {
	t.Helper()
	for _, it := range items {
		for _, key := range []string{"text", "output"} {
			s, _ := it.Value(key).(string)
			if strings.Contains(s, "\x1b") || strings.Contains(s, "\r") || strings.Contains(s, "\ufffd") ||
				strings.Contains(s, `{"type"`) || codexTracingRe.MatchString(s) {
				t.Errorf("%s %s not clean: %q", it.Value("id"), key, s)
			}
		}
	}
}

func byType(items []*pyjson.Object, typ string) []*pyjson.Object {
	var out []*pyjson.Object
	for _, it := range items {
		if it.Value("type") == typ {
			out = append(out, it)
		}
	}
	return out
}

func toolInput(it *pyjson.Object) *pyjson.Object {
	o, _ := it.Value("input").(*pyjson.Object)
	if o == nil {
		return pyjson.NewObject()
	}
	return o
}

func TestCodexRecordedRead(t *testing.T) {
	items := transcriptItems(t, "codex", fixtureRows(t, "codex_exec_read.jsonl"), fixtureRows(t, "codex_exec_stderr.txt"))
	assertClean(t, items)
	var kinds []string
	for _, it := range items {
		kinds = append(kinds, strOr(it.Value("type"))+":"+strOr(or(it.Value("level"), it.Value("role"))))
	}
	// The config warning Codex prints twice shows once; stderr adds nothing.
	if got := strings.Join(kinds, " "); got != "notice:warning message:assistant tool: message:assistant" {
		t.Fatalf("blocks: %s", got)
	}
	tool := byType(items, "tool")[0]
	if strOr(toolInput(tool).Value("command")) != `Get-Content -LiteralPath .\hello.txt` {
		t.Errorf("command: %q", toolInput(tool).Value("command"))
	}
	if tool.Value("output") != "你好，世界 ARK" || tool.Value("status") != "ok" || tool.Value("name") != "command" {
		t.Errorf("tool: %v %v %v", tool.Value("name"), tool.Value("output"), tool.Value("status"))
	}
	last := items[len(items)-1]
	if last.Value("markdown") != true || !strings.Contains(strOr(last.Value("text")), "\n\n- 内容包含问候语“你好，世界”") {
		t.Errorf("final message: %q", last.Value("text"))
	}
}

func TestCodexRecordedWrite(t *testing.T) {
	items := transcriptItems(t, "codex", fixtureRows(t, "codex_exec_write.jsonl"), nil)
	assertClean(t, items)
	tools := byType(items, "tool")
	if len(tools) != 3 {
		t.Fatalf("tools: %d", len(tools))
	}
	if tools[0].Value("output") != "绿色 OK 进度 50%" {
		t.Errorf("ANSI output: %q", tools[0].Value("output"))
	}
	files, _ := toolInput(tools[1]).Value("files").([]any)
	if tools[1].Value("name") != "file_change" || len(files) != 1 || tools[1].Value("output") != `added D:\work\notes.md` {
		t.Errorf("file change: %v %v", toolInput(tools[1]), tools[1].Value("output"))
	}
	if strOr(toolInput(tools[2]).Value("command")) != "Get-Content -Raw notes.md" {
		t.Errorf("single-quoted command: %q", toolInput(tools[2]).Value("command"))
	}
	if n := len(byType(items, "message")); n != 3 {
		t.Errorf("messages: %d (each agent message is its own block)", n)
	}
}

func TestCodexSyntheticEvents(t *testing.T) {
	got := BuildTranscript(TranscriptInput{SessionID: "s_codex000002", Status: "exited",
		Stdout: fixtureRows(t, "codex_exec_synthetic.jsonl"), Platform: "codex"})
	items := objects(got.Value("items"))
	assertClean(t, items)
	notices := byType(items, "notice")
	if len(notices) != 2 || notices[0].Value("level") != "reasoning" || notices[0].Value("text") != "**检查** 测试目录和失败用例" {
		t.Fatalf("reasoning updated in place: %v", notices)
	}
	if notices[1].Value("level") != "warning" || notices[1].Value("text") != "Reconnecting... 1/5" {
		t.Errorf("reconnect: %v", notices[1])
	}
	tools := byType(items, "tool")
	if len(tools) != 4 {
		t.Fatalf("tools: %d", len(tools))
	}
	plan, cmd, change, mcp := tools[0], tools[1], tools[2], tools[3]
	if plan.Value("name") != "update_plan" || plan.Value("output") != "- [x] 运行测试\n- [ ] 修复失败" {
		t.Errorf("plan: %v", plan.Value("output"))
	}
	if strOr(toolInput(cmd).Value("command")) != "pytest -q" || cmd.Value("status") != "error" {
		t.Errorf("command: %v %v", toolInput(cmd), cmd.Value("status"))
	}
	if want := "进度 100%\nFAILED tests/test_中文.py::test_x - 断言失败\n1 failed in 0.12s"; cmd.Value("output") != want {
		t.Errorf("output:\n%q\nwant\n%q", cmd.Value("output"), want)
	}
	if change.Value("output") != "updated tests/test_中文.py\nadded src/new.py" || change.Value("status") != "ok" {
		t.Errorf("change: %v", change.Value("output"))
	}
	if mcp.Value("name") != "ark.get_unit" || mcp.Value("output") != "任务：整理日志" {
		t.Errorf("mcp: %v %v", mcp.Value("name"), mcp.Value("output"))
	}
	msgs := byType(items, "message")
	if len(msgs) != 1 || msgs[0].Value("text") != "已修复 `test_x`：\n\n- 断言改为比较 **中文** 字符串\n- 新增 `src/new.py`" {
		t.Errorf("streamed message merged: %v", msgs)
	}
	errs := byType(items, "error")
	if len(errs) != 1 || !strings.Contains(strOr(errs[0].Value("text")), "连接被重置") {
		t.Errorf("failed turn: %v", errs)
	}
	usage, _ := got.Value("usage").(*pyjson.Object)
	if usage == nil || usage.Value("source") != "reported" {
		t.Errorf("usage: %v", usage)
	}
}

// TestCodexMatchesClaudeBlocks: the same kind of work renders with the same
// blocks for Codex and Claude Code (Markdown messages, commands with
// input.command and their output, file changes with paths).
func TestCodexMatchesClaudeBlocks(t *testing.T) {
	codex := transcriptItems(t, "codex", fixtureRows(t, "codex_exec_write.jsonl"), nil)
	claude := transcriptItems(t, "claude", fixtureRows(t, "claude_stream_equivalent.jsonl"), nil)
	assertClean(t, claude)
	shape := func(items []*pyjson.Object) map[string]bool {
		out := map[string]bool{}
		for _, it := range items {
			switch it.Value("type") {
			case "message":
				out["message:markdown="+pyjson.Str(it.Value("markdown"))] = true
			case "tool":
				in := toolInput(it)
				switch {
				case in.Has("command"):
					out["tool:command:"+pyjson.Str(it.Value("status"))] = true
				case in.Has("file_path") || in.Has("files"):
					out["tool:edit:"+pyjson.Str(it.Value("status"))] = true
				default:
					out["tool:other"] = true
				}
			case "notice":
				// Codex's config warning; Claude has none here.
			default:
				out[strOr(it.Value("type"))] = true
			}
		}
		return out
	}
	a, b := shape(codex), shape(claude)
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Fatalf("codex blocks %s\nclaude blocks %s", ja, jb)
	}
}

func TestCodexLegacyTextLogDropsTracing(t *testing.T) {
	stderr := []*pyjson.Object{
		pyjson.NewObject("t", "2026-11-23T09:00:00+00:00", "line", "codex"),
		pyjson.NewObject("t", "2026-11-23T09:00:01+00:00", "line", "2026-11-23T09:02:49.649846Z  WARN codex_otel::auth_storage: noise"),
		pyjson.NewObject("t", "2026-11-23T09:00:02+00:00", "line", "我先读取文件。"),
	}
	items := transcriptItems(t, "codex", nil, stderr)
	if len(items) != 1 || items[0].Value("text") != "我先读取文件。" {
		t.Fatalf("text log: %v", items)
	}
}

func TestCleanLine(t *testing.T) {
	for in, want := range map[string]string{
		"\x1b[31mred\x1b[0m 中文":           "red 中文",
		"\x1b]0;title\x07body":            "body",
		"10%\r50%\r\x1b[2K100% done\r\n":  "100% done",
		"plain\r\n":                       "plain",
		"\x1b[?25lhidden cursor\x1b[?25h": "hidden cursor",
		"\x1b(Bcharset":                   "charset",
	} {
		if got := cleanLine(in); got != want {
			t.Errorf("cleanLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCodexCommandUnwrap(t *testing.T) {
	for in, want := range map[string]string{
		`"C:\\Users\\me\\pwsh.exe" -Command "Get-Content -LiteralPath .\\hello.txt"`: `Get-Content -LiteralPath .\hello.txt`,
		`"C:\\pwsh.exe" -Command 'Get-Content -Raw notes.md'`:                         `Get-Content -Raw notes.md`,
		`/bin/bash -lc 'pytest -q'`:                                                   `pytest -q`,
		`bash -lc "echo \"hi\""`:                                                      `echo "hi"`,
		`git status`:                                                                  `git status`,
	} {
		if got := codexCommand(in); got != want {
			t.Errorf("codexCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCodexJSONArgv(t *testing.T) {
	join := func(a []string) string { return strings.Join(a, " ") }
	for in, want := range map[string]string{
		"codex exec --skip-git-repo-check go": "codex exec --json --skip-git-repo-check go",
		"codex exec --json go":                "codex exec --json go",
		"codex e go":                          "codex e --json go",
		"codex exec resume N1 go":             "codex exec --json resume N1 go",
		"codex --experimental-json exec go":   "codex --experimental-json exec go",
		"my-agent run go":                     "my-agent run go",
	} {
		if got := join(codexJSONArgv(strings.Fields(in))); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}

func agentHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude", "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(home, ".claude", "agents", "reviewer.md"),
		[]byte("---\nname: reviewer\ndescription: Reviews diffs for bugs\n---\nYou review.\n"), 0o644)
	_ = os.WriteFile(filepath.Join(home, ".claude", "agents", "bad name.md"), []byte("no front matter"), 0o644)
	_ = os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	_ = os.WriteFile(filepath.Join(home, ".codex", "fast.config.toml"), []byte("model = \"gpt-5.5\"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("model = \"gpt-6\"\n"), 0o644)
	old := AgentHome
	AgentHome = func() string { return home }
	t.Cleanup(func() { AgentHome = old })
	t.Setenv("CODEX_HOME", "")
	return home
}

func TestAgentOptionsPublic(t *testing.T) {
	agentHome(t)
	claude := agentOptionsPublic(nil, "claude")
	if len(claude) != 1 || claude[0].(*pyjson.Object).Value("id") != "reviewer" ||
		claude[0].(*pyjson.Object).Value("description") != "Reviews diffs for bugs" {
		t.Fatalf("claude agents: %v", claude)
	}
	codex := agentOptionsPublic(nil, "codex")
	if len(codex) != 1 || codex[0].(*pyjson.Object).Value("id") != "fast" || codex[0].(*pyjson.Object).Value("description") != "gpt-5.5" {
		t.Fatalf("codex profiles: %v", codex)
	}
	if len(agentOptionsPublic(nil, "dsh")) != 0 {
		t.Fatal("dsh has no named agents")
	}
}

// TestLaunchFlags: effort, model and a named agent reach the command line;
// a free-text agent label does not.
func TestLaunchFlags(t *testing.T) {
	agentHome(t)
	old := CodexVersion
	CodexVersion = func(string) [3]int { return [3]int{0, 169, 0} }
	defer func() { CodexVersion = old }()
	m := streamManager(t)
	t.Setenv("ARK_CLAUDE_STREAM", "0")
	exe, _ := os.Executable()
	m.Presets = []*pyjson.Object{
		pyjson.NewObject("id", "claude", "label", "Claude Code", "available", true, "efforts", []any{"low", "high", "max"},
			"command", []any{exe, "-p", "{prompt}"}, "prompt_mode", "arg", "followup", false),
		pyjson.NewObject("id", "codex", "label", "Codex", "available", true, "efforts", []any{"low", "medium", "high"},
			"command", []any{exe, "exec", "--skip-git-repo-check", "{prompt}"}, "prompt_mode", "arg", "followup", false),
	}
	args := func(body *pyjson.Object) string {
		t.Helper()
		file := filepath.Join(t.TempDir(), "args.json")
		t.Setenv("ARK_FAKE_ARGS_FILE", file)
		s, err := m.Start(nil, body, "http://127.0.0.1:1", "bob")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		waitFor(t, "args", func() bool {
			raw, err := os.ReadFile(file)
			return err == nil && json.Unmarshal(raw, &got) == nil
		})
		killSession(m, s)
		return strings.Join(got, " ")
	}
	got := args(pyjson.NewObject("platform", "claude", "prompt", "hi", "model", "opus", "effort", "max", "agent", "reviewer"))
	for _, want := range []string{"--model opus", "--effort max", "--agent reviewer"} {
		if !strings.Contains(got, want) {
			t.Errorf("claude argv lacks %q: %s", want, got)
		}
	}
	if got := args(pyjson.NewObject("platform", "claude", "prompt", "hi", "agent", "Claude-1")); strings.Contains(got, "--agent") {
		t.Errorf("a label is not an agent: %s", got)
	}
	got = args(pyjson.NewObject("platform", "codex", "prompt", "你好", "model", "gpt-5.5", "effort", "high", "agent", "fast"))
	for _, want := range []string{"exec ", "--json", "-p fast", "-m gpt-5.5", "model_reasoning_effort=high", "你好"} {
		if !strings.Contains(got, want) {
			t.Errorf("codex argv lacks %q: %s", want, got)
		}
	}
	if _, err := m.Start(nil, pyjson.NewObject("platform", "codex", "prompt", "x", "effort", "max"), "http://127.0.0.1:1", "bob"); err == nil {
		t.Error("an effort the provider does not list is rejected")
	}
}
