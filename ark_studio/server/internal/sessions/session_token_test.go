package sessions

import (
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

// A run gets its agent token from Hooks.AgentToken; the id is kept for
// revocation, the secret only in memory, and output lines that echo it are
// redacted before they are written.
func TestSessionAgentTokenEnvAndRedaction(t *testing.T) {
	m := streamManager(t)
	m.WriteToken = "arka_SHAREDSERVERTOKEN0000000000"
	const secret = "arka_SESSIONSECRETxxxxxxxxxxxxxxxx"
	issued := 0
	h := &Hooks{AgentToken: func(actor, sessionID, access string) (string, string, error) {
		issued++
		if actor != "carol" || access != "read_only" || sessionID == "" {
			t.Errorf("hook got %q %q %q", actor, sessionID, access)
		}
		return secret, "tok_session1", nil
	}}
	s, err := m.Start(h, pyjson.NewObject("platform", "claude", "prompt", "hi", "access_mode", "read_only"),
		"http://127.0.0.1:1", "carol")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { killSession(m, s); waitFor(t, "ended", func() bool { return !m.IsActive(s) }) }()
	if issued == 0 {
		t.Fatal("no token issued")
	}
	env := strings.Join(m.buildEnv(h, s, pyjson.NewObject(), "http://127.0.0.1:1", "", s.Workspace(), "", "carol"), "\n")
	if !strings.Contains(env, "ARK_AGENT_TOKEN="+secret) || strings.Contains(env, m.WriteToken) {
		t.Fatal("the run's own token, not the shared one, goes to ARK_AGENT_TOKEN")
	}
	m.appendEvent(s, "stdout", "curl -H 'Authorization: Bearer "+secret+"'", "")
	if text := logText(m, s); strings.Contains(text, secret) {
		t.Fatalf("token in the log: %s", text)
	}
	ids := m.TakeAgentTokenIDs(s)
	if len(ids) == 0 || ids[0] != "tok_session1" {
		t.Fatalf("ids %v", ids)
	}
	if again := m.TakeAgentTokenIDs(s); len(again) != 0 {
		t.Fatal("ids are handed out once")
	}
	// Without a hook a read-only run gets no agent token at all.
	env = strings.Join(m.buildEnv(nil, s, pyjson.NewObject(), "", "", s.Workspace(), "", "carol"), "\n")
	if strings.Contains(env, "ARK_AGENT_TOKEN=") {
		t.Fatal("a read-only run without a hook got a token")
	}
}

func TestSessionEnvHasServerRootAndProject(t *testing.T) {
	m := streamManager(t)
	s, err := m.Start(nil, pyjson.NewObject("platform", "claude", "prompt", "hi"), "http://127.0.0.1:1", "carol")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { killSession(m, s); waitFor(t, "ended", func() bool { return !m.IsActive(s) }) }()
	env := strings.Join(m.buildEnv(nil, s, pyjson.NewObject("id", "prj_x"), "https://ark.example/api/task-agent/v1", "",
		s.Workspace(), "", "carol"), "\n")
	if !strings.Contains(env, "ARK_URL=https://ark.example\n") || !strings.Contains(env, "ARK_PROJECT=prj_x") {
		t.Fatalf("root or project missing")
	}
}
