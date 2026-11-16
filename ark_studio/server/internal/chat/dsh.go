package chat

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"arkstudio/go/internal/platform/procx"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pyos"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/compat/pyurl"
)

// textOut is subprocess text-mode output: decoded with errors="replace",
// universal newlines.
func textOut(b []byte) string {
	s := pyurl.DecodeReplace(b)
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// runDSH is _run_dsh: nil when the harness failed (the caller falls back).
func (e *Env) runDSH(text string, status *pyjson.Object) (*pyjson.Object, error) {
	command := pyjson.Str(status.Value("command"))
	parts, err := pyos.ShlexSplit(command, false)
	if err != nil {
		return nil, valueErr(err.Error())
	}
	if len(parts) == 0 {
		return nil, nil
	}
	base := e.BaseURL
	if base == "" {
		base = "http://127.0.0.1:8890"
	}
	token := e.AgentToken
	if token == "" {
		token = os.Getenv("ARK_AGENT_TOKEN")
	}
	prompt := "You are the ARK project agent, working inside the local `.ark` project folder.\n" +
		"You may edit files and use the ARK HTTP API to read or write nodes, links, and settings.\n" +
		"Project: " + e.ProjectName + " (" + e.ProjectPath + ")\n" +
		"API base: " + base + "\n" +
		"API token: " + token + "\n"
	if e.Infohub != nil && e.Infohub.Len() > 0 {
		path, err := infohubPath(e.Infohub)
		if err != nil {
			return nil, err
		}
		prompt += "InfoHub database: " + path + "\n" +
			"Current InfoHub filters: " + string(pyjson.Dumps(infohubFilters(e.Infohub))) + "\n"
	}
	prompt += "Prefer small, explicit changes; report what you changed.\n\n" + "User request:\n" + text
	timeoutText, ok := os.LookupEnv("ARK_DSH_TIMEOUT")
	if !ok {
		timeoutText = "180"
	}
	seconds, err := pytext.Int(timeoutText)
	if err != nil {
		return nil, valueErr(err.Error())
	}
	input := prompt
	if runtime.GOOS == "windows" {
		input = strings.ReplaceAll(prompt, "\n", "\r\n") // text-mode stdin
	}
	ctx, cancel := context.WithTimeout(e.Ctx, time.Duration(seconds)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
	procx.SetArgvCmdline(cmd, parts)
	cmd.Dir = e.ProjectPath
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, nil // OSError, TimeoutExpired or a non-zero exit
	}
	output := pytext.Strip(textOut(stdout.Bytes()))
	if output == "" {
		output = pytext.Strip(textOut(stderr.Bytes()))
	}
	if output == "" {
		output = "DeepSeek Harness finished without a message."
	}
	return pyjson.NewObject("message", output, "tools", []any{}, "refresh", true, "suggestions", suggestions(),
		"harness", status.Value("label")), nil
}
