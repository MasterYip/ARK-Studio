// Package demoagent ports arkstudio/demo_agent.py, the line-oriented local
// agent of the built-in "Echo (local demo)" platform (`ark-server
// demo-agent --prompt ...`).  It is not a model and makes no network calls.
//
// Output matches the Python script byte for byte, including the text-mode
// "\r\n" line ends of sys.stdout and Path.write_text on Windows.
package demoagent

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pytext"
	"arkstudio/go/internal/store"
)

const eventPrefix = "[[ark-event]]"

var newline = func() string {
	if runtime.GOOS == "windows" {
		return "\r\n"
	}
	return "\n"
}()

type agent struct{ out io.Writer }

func (a *agent) print(text string) {
	fmt.Fprint(a.out, strings.ReplaceAll(text, "\n", newline)+newline)
}

func (a *agent) emit(kv ...any) {
	a.print(eventPrefix + " " + string(pyjson.Dumps(pyjson.NewObject(kv...))))
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// pathName is pathlib.Path(name).name.
func pathName(name string) string {
	seps := "/"
	if runtime.GOOS == "windows" {
		seps = `/\`
	}
	var parts []string
	for _, p := range strings.FieldsFunc(name, func(r rune) bool { return strings.ContainsRune(seps, r) }) {
		if p != "." {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

func writeText(path, text string) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, err
	}
	data := store.TextFile(text)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return 0, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

func runeSlice(s string, from int) string {
	r := []rune(s)
	if from > len(r) {
		return ""
	}
	return string(r[from:])
}

// Main is demo_agent.main(argv).
func Main(argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	prompt := ""
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case arg == "--prompt":
			if i+1 >= len(argv) {
				fmt.Fprintln(stderr, "usage: arkstudio.demo_agent [-h] [--prompt PROMPT]\narkstudio.demo_agent: error: argument --prompt: expected one argument")
				return 2
			}
			prompt = argv[i+1]
			i++
		case strings.HasPrefix(arg, "--prompt="):
			prompt = arg[len("--prompt="):]
		default:
			fmt.Fprintf(stderr, "usage: arkstudio.demo_agent [-h] [--prompt PROMPT]\narkstudio.demo_agent: error: unrecognized arguments: %s\n", arg)
			return 2
		}
	}
	a := &agent{out: stdout}
	project := os.Getenv("ARK_PROJECT_PATH")
	a.emit("type", "assistant", "text", "# ARK echo agent\n\n"+
		"This is a **local demo agent**, not a model. It reads your prompt, "+
		"can write files into `artifacts/`, and keeps the session lifecycle testable "+
		"without an API key.\n\n"+
		"| Input | Behaviour |\n|---|---|\n"+
		"| `write <name> <text>` | write an artifact in the private workspace |\n"+
		"| `quit` | end this session |\n"+
		"| anything else | echoed back as assistant output |")
	a.emit("type", "tool_call", "id", "demo-context", "name", "read_context",
		"input", pyjson.NewObject("path", "ark-context.json", "mode", "read-only"))
	units, bytesRead := int64(0), int64(0)
	if raw, err := os.ReadFile("ark-context.json"); err == nil {
		bytesRead = int64(len(raw))
		if parsed, ok := pyjson.LoadsObject(string(raw)); ok {
			if list, ok := parsed.Value("units").([]any); ok {
				units = int64(len(list))
			}
		}
	}
	shown := project
	if shown == "" {
		shown = "(none)"
	}
	a.emit("type", "tool_result", "id", "demo-context", "status", "ok",
		"output", pyjson.NewObject("project", shown, "units", units, "bytes", bytesRead))
	cwd, _ := os.Getwd()
	a.print("ARK echo agent ready — local demo, no model call.")
	a.print("project (read-only): " + shown)
	a.print("workspace: " + cwd)
	a.print("read-only token present: " + yesNo(os.Getenv("ARK_READ_TOKEN") != ""))
	if prompt != "" {
		a.print("instruction: " + prompt)
	}
	a.print("Commands: write <name> <text> · request command · request edit · quit")

	counter := 0
	pending := map[string]*pyjson.Object{}
	handleResponse := func(frame *pyjson.Object) {
		requestID := pyjson.Str(orAny(frame.Value("request_id"), orAny(frame.Value("id"), "")))
		action := pending[requestID]
		if action == nil {
			a.print("approval response for unknown request " + requestID)
			return
		}
		delete(pending, requestID)
		allowed := pytext.Lower(pyjson.Str(orAny(frame.Value("decision"), ""))) == "allow"
		scope := pyjson.Str(orAny(frame.Value("scope"), "once"))
		tool := action.Value("tool")
		if !allowed {
			a.emit("type", "tool_result", "id", requestID, "name", tool, "status", "error",
				"output", pyjson.NewObject("decision", "deny", "message", orAny(frame.Value("message"), "")))
			a.print("denied " + pyjson.Str(action.Value("label")) + " (" + scope + ")")
			return
		}
		if action.Value("kind") == "file_edit" {
			name := pyjson.Str(action.Value("name"))
			text := pyjson.Str(orAny(action.Value("text"), "approved demo content\n"))
			_, _ = writeText(filepath.Join("artifacts", name), text)
			a.emit("type", "tool_call", "id", requestID, "name", tool,
				"input", pyjson.NewObject("path", "artifacts/"+name, "approval", "allow"))
			a.emit("type", "tool_result", "id", requestID, "name", tool, "status", "ok",
				"output", pyjson.NewObject("path", "artifacts/"+name, "scope", scope))
			a.print("approved file edit -> artifacts/" + name + " (" + scope + ")")
			return
		}
		command := pyjson.Str(orAny(action.Value("command"), ""))
		a.emit("type", "tool_call", "id", requestID, "name", tool,
			"input", pyjson.NewObject("command", command, "approval", "allow"))
		a.emit("type", "tool_result", "id", requestID, "name", tool, "status", "ok",
			"output", pyjson.NewObject("exit_code", int64(0), "scope", scope))
		a.print("approved command: " + command + " (" + scope + ")")
	}

	const demoCommand = `python -c "print('approved by ARK')"`
	r := bufio.NewReader(stdin)
	for {
		raw, err := r.ReadString('\n')
		if raw == "" && err != nil {
			return 0
		}
		line := strings.TrimRight(strings.ReplaceAll(raw, "\r\n", "\n"), "\r\n")
		stripped := pytext.Strip(line)
		if stripped == "" {
			continue
		}
		if strings.HasPrefix(stripped, "{") {
			if frame, ok := pyjson.LoadsObject(stripped); ok && frame.Value("type") == "permission_response" {
				handleResponse(frame)
				continue
			}
		}
		low := pytext.Lower(stripped)
		switch {
		case low == "quit" || low == "exit":
			a.print("bye")
			return 0
		case low == "request command" || low == "request make command":
			counter++
			requestID := fmt.Sprintf("demo-approval-%d", counter)
			pending[requestID] = pyjson.NewObject("kind", "command", "tool", "bash", "label", "demo command", "command", demoCommand)
			a.emit("type", "permission_request", "id", requestID, "kind", "command", "tool", "bash",
				"command", demoCommand, "description", "Run a harmless demo command")
			a.print("waiting for approval on " + requestID)
			continue
		case strings.HasPrefix(low, "request edit") || strings.HasPrefix(low, "request write"):
			counter++
			requestID := fmt.Sprintf("demo-approval-%d", counter)
			var rest string
			if strings.HasPrefix(low, "request edit") {
				rest = runeSlice(stripped, len("request edit"))
			} else {
				rest = runeSlice(stripped, len("request write"))
			}
			rest = pytext.Strip(rest)
			text := rest
			if text == "" {
				text = "approved demo content\n"
			}
			pending[requestID] = pyjson.NewObject("kind", "file_edit", "tool", "write_file", "label", "demo file edit",
				"name", "approved-demo.txt", "text", text)
			a.emit("type", "permission_request", "id", requestID, "kind", "file_edit", "tool", "write_file",
				"path", "artifacts/approved-demo.txt", "description", "Write an approved demo artifact")
			a.print("waiting for approval on " + requestID)
			continue
		case strings.HasPrefix(low, "write "):
			rest := pytext.Strip(runeSlice(stripped, 6))
			if rest == "" {
				a.print("usage: write <name> <text>")
				continue
			}
			name, text, _ := strings.Cut(rest, " ")
			name = pathName(name)
			if name == "" {
				name = "note.txt"
			}
			content := text
			if content == "" {
				content = "(empty)\n"
			}
			size, _ := writeText(filepath.Join("artifacts", name), content)
			a.emit("type", "tool_call", "id", "write-"+name, "name", "write_file",
				"input", pyjson.NewObject("path", "artifacts/"+name, "chars", int64(len([]rune(text)))))
			a.emit("type", "tool_result", "id", "write-"+name, "status", "ok",
				"output", pyjson.NewObject("path", "artifacts/"+name, "size", size))
			a.print("wrote artifacts/" + name)
			continue
		}
		a.emit("type", "assistant", "text", "Echo from the local demo agent:\n\n> "+line)
		if err != nil {
			return 0
		}
	}
}

func orAny(v, def any) any {
	if pyjson.Truthy(v) {
		return v
	}
	return def
}
