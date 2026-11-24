package sessions

import (
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/sessionstart"
)

// A launched agent is told to use its own session token ($ARKT) and to
// ignore other tokens; pasted personal tokens are taken out of messages.
func TestSessionPreambleAndPastedTokens(t *testing.T) {
	m := &Manager{}
	s := &Session{ID: "s_abcdef1234", Meta: pyjson.NewObject("access_mode", "full_access")}
	ref := pyjson.NewObject("id", "prj_1", "name", "Demo")
	h := &Hooks{CoreRules: func(pid, actor string) string { return "CORE RULES (rules v1) for " + pid + "/" + actor }}
	env := []string{"ARKT=arka_secretsecretsecret", "ARK_WAKE=ark"}
	out := m.sessionPreamble(h, s, pyjson.NewObject("id", "claude"), ref, env, "ann", nil)
	for _, want := range []string{"s_abcdef1234", "prj_1", "In an ARK-launched session use $ARKT", "write scope",
		"ignore other tokens", "X-ARK-Session: $ARK_SESSION_ID", "~/.ark/tokens", "start no watcher", "CORE RULES (rules v1) for prj_1/ann"} {
		if !strings.Contains(out, want) {
			t.Fatalf("preamble lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "arka_secret") {
		t.Fatal("the token value is in the preamble")
	}
	s.Meta.Set("access_mode", "read_only")
	if out := m.sessionPreamble(h, s, pyjson.NewObject("id", "claude"), ref, nil, "ann", nil); strings.Contains(out, "$ARKT") ||
		!strings.Contains(out, "ARK_READ_TOKEN") {
		t.Fatalf("no token: %s", out)
	}
	if m.sessionPreamble(h, s, pyjson.NewObject("id", "echo", "demo", true), ref, env, "ann", nil) != "" {
		t.Fatal("demo preset")
	}
	if m.sessionPreamble(nil, s, pyjson.NewObject("id", "claude"), ref, env, "ann", nil) != "" {
		t.Fatal("no hooks")
	}
	msg := WithoutPastedTokens("token: arka_AbCdEfGhIjKlMnOpQrSt12 and more")
	if strings.Contains(msg, "arka_AbC") || !strings.Contains(msg, PastedTokenNote) {
		t.Fatalf("pasted token kept: %s", msg)
	}
	if WithoutPastedTokens("no token here") != "no token here" {
		t.Fatal("changed a plain message")
	}
}

// The start goes last in the block, and the block says so.
func TestSessionPreambleStart(t *testing.T) {
	m := &Manager{}
	s := &Session{ID: "s_abcdef1234", Meta: pyjson.NewObject("access_mode", "full_access")}
	h := &Hooks{CoreRules: func(string, string) string { return "CORE RULES" }}
	st := &sessionstart.Start{Mode: sessionstart.ModeContinue, Text: "Start: continue T-0042 \"x\" (status active)."}
	out := m.sessionPreamble(h, s, pyjson.NewObject("id", "claude"), pyjson.NewObject("id", "prj_1"), []string{"ARKT=x"}, "ann", st)
	if !strings.HasSuffix(out, st.Text) || !strings.Contains(out, "do not ask for a pasted prompt") ||
		strings.Index(out, "CORE RULES") > strings.Index(out, "Start: continue") {
		t.Fatalf("start section:\n%s", out)
	}
	c := startChoice(pyjson.NewObject("id", "plan-idea", "title", "Plan", "text", "TASK: plan arka_AbCdEfGhIjKlMnOpQrSt12"))
	if c == nil || c.ID != "plan-idea" || strings.Contains(c.Text, "arka_AbC") {
		t.Fatalf("choice %+v", c)
	}
	if startChoice(pyjson.NewObject("title", "x")) != nil || startChoice("x") != nil {
		t.Fatal("a choice without an id")
	}
}
