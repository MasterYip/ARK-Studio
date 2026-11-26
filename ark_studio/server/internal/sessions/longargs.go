package sessions

// Long prompts (2026-11-25).  A message to a session has no working length
// limit any more: maxPrompt (1,000,000 characters) is only a sanity guard,
// and text over it is refused with 413, never cut.  The HTTP body cap
// (24 MiB) still applies.
//
// A prompt can be far longer than a command line allows (Windows: about
// 32,767 UTF-16 units for the whole line; Linux: 128 KiB per argument), so
// a prompt longer than argPromptMax bytes never travels as an argument:
//
//   - Claude Code in stream mode takes every message on stdin anyway; in
//     one-shot print mode the prompt argument is dropped and the prompt is
//     written to stdin (`claude -p` reads it from there).
//   - Codex exec gets "-" in place of the prompt and reads it from stdin
//     (also `codex exec resume <id> -`).
//   - Gemini CLI reads stdin and appends its -p value to it: -p gets a short
//     pointer, the prompt goes to stdin.
//   - Every other agent (and a prompt embedded inside a larger argument) gets
//     the prompt in ARK-PROMPT.md in the session workspace and a short
//     argument that points at the file.
//
// The Claude session block (--append-system-prompt) follows the same rule:
// a long one goes to ARK-SESSION-BLOCK.md and --append-system-prompt-file.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
)

// argPromptMax is the longest prompt, in bytes, passed as an argument.  A
// UTF-8 byte count is never below the UTF-16 length Windows counts, so a
// prompt and a session block under it stay well inside 32,767.
const argPromptMax = 8000

const (
	promptFileName       = "ARK-PROMPT.md"
	sessionBlockFileName = "ARK-SESSION-BLOCK.md"
)

// tooLong is the 413 for text over the safety cap.
func tooLong(what string, n int) *Error {
	return serr(what+" is "+strconv.Itoa(n)+" characters long; ARK accepts at most "+
		strconv.Itoa(maxPrompt)+" characters per message", 413, "PROMPT_TOO_LONG")
}

// checkPrompt refuses text over maxPrompt characters.
func checkPrompt(what, text string) error {
	if len(text) <= maxPrompt { // bytes >= characters: most texts stop here
		return nil
	}
	if n := utf8.RuneCountInString(text); n > maxPrompt {
		return tooLong(what, n)
	}
	return nil
}

// cutMarked shortens an excerpt ARK composes itself (fork context) and says
// so in the text: nothing is cut without a marker.
func cutMarked(s string, n int) string {
	total := pytext.Len(s)
	if total <= n {
		return s
	}
	return pytext.Prefix(s, n) + "\n[… " + strconv.Itoa(total-n) + " characters cut]"
}

// longPromptArgv moves a long prompt out of argv (see the top of the file).
// It returns the new argv and the text to write to the agent's stdin ("" when
// the prompt stays in argv or went to a file).
func longPromptArgv(presetID string, preset *pyjson.Object, argv []string, prompt, workspace string) ([]string, string, error) {
	if len(prompt) <= argPromptMax {
		return argv, "", nil
	}
	at := indexOf(argv, prompt)
	inside := -1
	if at < 0 {
		for i, part := range argv {
			if strings.Contains(part, prompt) {
				inside = i
				break
			}
		}
		if inside < 0 {
			return argv, "", nil // the prompt is not on the command line
		}
	}
	out := append([]string(nil), argv...)
	// stdin is free for the prompt only when nothing else is written to it
	// later (followup agents take their input there).
	stdinFree := !pyjson.Truthy(preset.Value("followup"))
	if at > 0 && stdinFree {
		switch {
		case presetID == "codex" && contains(out, "exec"):
			out[at] = "-"
			return out, prompt, nil
		case presetID == "claude" && (contains(out, "-p") || contains(out, "--print")):
			return append(out[:at], out[at+1:]...), prompt, nil
		case presetID == "gemini" && (out[at-1] == "-p" || out[at-1] == "--prompt"):
			out[at] = "(ARK sent your full message on stdin, above. Follow it.)"
			return out, prompt, nil
		}
	}
	path := filepath.Join(workspace, promptFileName)
	if err := os.WriteFile(path, []byte(prompt), 0o644); err != nil {
		return nil, "", serr("could not save the long prompt for the agent: "+err.Error(), 500, "")
	}
	pointer := "ARK saved your full message (" + strconv.Itoa(pytext.Len(prompt)) + " characters) in the file " + path +
		" because it is too long for a command line. Read the whole file first and follow it as this message."
	if at >= 0 {
		out[at] = pointer
	} else {
		out[inside] = strings.Replace(out[inside], prompt, pointer, 1)
	}
	return out, "", nil
}

// systemPromptArgs passes the Claude session block as an argument, or in a
// file when it is long.
func systemPromptArgs(block, workspace string) []string {
	if len(block) > argPromptMax && workspace != "" {
		path := filepath.Join(workspace, sessionBlockFileName)
		if err := os.WriteFile(path, []byte(block), 0o644); err == nil {
			return []string{"--append-system-prompt-file", path}
		}
	}
	return []string{"--append-system-prompt", block}
}
