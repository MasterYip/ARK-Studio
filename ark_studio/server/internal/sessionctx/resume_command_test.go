package sessionctx

import "testing"

func TestResumeCommand(t *testing.T) {
	for agent, want := range map[string]string{
		"claude": "claude --resume 0b7c2f1e-5d4a-4c39",
		"codex":  "codex resume 0b7c2f1e-5d4a-4c39",
		"gemini": "gemini --resume 0b7c2f1e-5d4a-4c39",
		"dsh":    "",
	} {
		if got := ResumeCommand(agent, "0b7c2f1e-5d4a-4c39", ""); got != want {
			t.Errorf("%s: %q, want %q", agent, got, want)
		}
	}
}
