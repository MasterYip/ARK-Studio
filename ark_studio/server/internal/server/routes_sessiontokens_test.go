package server

import (
	"context"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

// Account mode: a session run gets a personal token of the person who
// launched it (write for full access, read otherwise) that the agent API
// accepts, and it stops working once the run's tokens are revoked.
// Local mode: the shared server token, for full access only.
func TestSessionAgentTokens(t *testing.T) {
	app, projectID := authDesktopApp(t)
	h := map[string]string{"X-ARK-Project": projectID}
	call := func(secret string) int {
		h["Authorization"] = "Bearer " + secret
		return gpuCall(t, app, "GET", "/api/agent/v1/setup", nil, h).code
	}
	if code := call(app.AgentToken()); code != 401 {
		t.Fatalf("the shared server token should be refused in account mode: %d", code)
	}
	secret, id, err := app.sessionAgentToken("owner", "s_test1", "full_access")
	if err != nil || !strings.HasPrefix(secret, "arka_") || id == "" {
		t.Fatalf("%q %q %v", secret, id, err)
	}
	if code := call(secret); code != 200 {
		t.Fatalf("session token refused: %d", code)
	}
	h["Authorization"] = "Bearer " + secret
	if r := gpuCall(t, app, "POST", "/api/agent/v1/units", map[string]any{"type": "task", "title": "from a session"}, h); r.code != 200 {
		t.Fatalf("full access cannot write: %d %s", r.code, r.raw)
	}
	read, _, err := app.sessionAgentToken("owner", "s_test2", "read_only")
	if err != nil {
		t.Fatal(err)
	}
	h["Authorization"] = "Bearer " + read
	if r := gpuCall(t, app, "POST", "/api/agent/v1/units", map[string]any{"type": "task", "title": "x"}, h); r.code == 200 {
		t.Fatal("a read-only run wrote a unit")
	}
	// Not counted against the person's limit, listed with the session id.
	list, err := app.Org.ListAgentTokens(context.Background(), "owner")
	if err != nil || !strings.Contains(string(pyjson.Dumps(list)), "ARK session s_test1") {
		t.Fatalf("%v %v", err, list)
	}
	if err := app.Org.RevokeTokenID(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if code := call(secret); code != 401 {
		t.Fatalf("revoked token still works: %d", code)
	}
	// Local mode keeps the shared token for full access only.
	local, _ := localGPUApp(t)
	if s, _, _ := local.sessionAgentToken("local", "s_x", "full_access"); s != local.AgentToken() {
		t.Fatal("local full access")
	}
	if s, _, _ := local.sessionAgentToken("local", "s_x", "read_only"); s != "" {
		t.Fatal("local read-only gets no token")
	}
}
