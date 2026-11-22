package server

import (
	"context"
	"strings"
	"testing"
	"time"
)

func promptIDs(r gpuReply, key string) []string {
	var out []string
	list, _ := r.body[key].([]any)
	for _, x := range list {
		out = append(out, x.(map[string]any)["id"].(string))
	}
	return out
}

// Local mode: one person, admin of every project.
func TestPromptsCRUDPinsImport(t *testing.T) {
	t.Setenv("ARK_PRIVATE_DIR", t.TempDir())
	app, _ := localGPUApp(t)
	r := gpuCall(t, app, "GET", "/api/prompts", nil, nil)
	if r.code != 200 || r.body["pins"] != nil || r.body["can_edit_project"] != true ||
		!strings.HasPrefix(r.body["rules_version"].(string), "r-") || len(promptIDs(r, "personal")) != 0 {
		t.Fatalf("GET empty: %d %s", r.code, r.raw)
	}
	r = gpuCall(t, app, "POST", "/api/prompts", map[string]any{"action": "save", "scope": "personal",
		"prompt": map[string]any{"title": "Digest", "description": "Weekly", "body": "Summarise {project} for {user}, {when}."}}, nil)
	ids := promptIDs(r, "personal")
	if r.code != 200 || len(ids) != 1 || !strings.Contains(r.raw, "{when} is not a placeholder") {
		t.Fatalf("save: %d %s", r.code, r.raw)
	}
	r = gpuCall(t, app, "POST", "/api/prompts", map[string]any{"action": "save", "scope": "project",
		"prompt": map[string]any{"title": "Team digest", "body": "For {unit}"}}, nil)
	proj := promptIDs(r, "project")
	if r.code != 200 || len(proj) != 1 {
		t.Fatalf("save project: %d %s", r.code, r.raw)
	}
	// Edit keeps the id and changes the version.
	before := r.body["version"]
	r = gpuCall(t, app, "POST", "/api/prompts", map[string]any{"action": "save", "scope": "personal",
		"prompt": map[string]any{"id": ids[0], "title": "Digest", "body": "Summarise {project}."}}, nil)
	if r.code != 200 || promptIDs(r, "personal")[0] != ids[0] || r.body["version"] == before {
		t.Fatalf("edit: %d %s", r.code, r.raw)
	}
	// Pins: an order, then back to the defaults.
	r = gpuCall(t, app, "POST", "/api/prompts", map[string]any{"action": "pins", "pins": []any{"team", ids[0], "setup"}}, nil)
	if pins, _ := r.body["pins"].([]any); r.code != 200 || len(pins) != 3 || pins[1] != ids[0] {
		t.Fatalf("pins: %d %s", r.code, r.raw)
	}
	if r = gpuCall(t, app, "POST", "/api/prompts", map[string]any{"action": "pins", "pins": []any{}}, nil); r.body["pins"] == nil {
		t.Fatalf("an empty pin list is kept (nothing pinned): %s", r.raw)
	}
	if r = gpuCall(t, app, "POST", "/api/prompts", map[string]any{"action": "pins", "pins": nil}, nil); r.body["pins"] != nil {
		t.Fatalf("reset: %s", r.raw)
	}
	// Import skips what is already there.
	r = gpuCall(t, app, "POST", "/api/prompts", map[string]any{"action": "import", "scope": "personal",
		"prompts": []any{map[string]any{"title": "Digest", "body": "Summarise {project}."}, map[string]any{"title": "New", "body": "b"}}}, nil)
	if r.code != 200 || len(promptIDs(r, "personal")) != 2 || !strings.Contains(r.raw, "1 added, 1 already there") {
		t.Fatalf("import: %d %s", r.code, r.raw)
	}
	if r = gpuCall(t, app, "POST", "/api/prompts", map[string]any{"action": "delete", "scope": "personal", "id": ids[0]}, nil); r.code != 200 || len(promptIDs(r, "personal")) != 1 {
		t.Fatalf("delete: %d %s", r.code, r.raw)
	}
	for _, bad := range []struct {
		body map[string]any
		code int
	}{
		{map[string]any{"action": "save", "scope": "personal", "prompt": map[string]any{"title": "", "body": "b"}}, 400},
		{map[string]any{"action": "save", "scope": "team", "prompt": map[string]any{"title": "t", "body": "b"}}, 400},
		{map[string]any{"action": "delete", "scope": "personal", "id": "cp-00000000"}, 404},
		{map[string]any{"action": "pins", "pins": []any{"No Such"}}, 400},
		{map[string]any{"action": "rename"}, 400},
	} {
		if r = gpuCall(t, app, "POST", "/api/prompts", bad.body, nil); r.code != bad.code {
			t.Fatalf("%v: %d %s", bad.body, r.code, r.raw)
		}
	}
	// Agents use their own endpoint.
	agent := map[string]string{"Authorization": "Bearer " + app.AgentToken()}
	if r = gpuCall(t, app, "POST", "/api/prompts", map[string]any{"action": "pins", "pins": []any{}}, agent); r.code != 403 {
		t.Fatalf("agent on the people's route: %d", r.code)
	}
	r = gpuCall(t, app, "POST", "/api/agent/v1/prompts", map[string]any{"title": "From my agent", "description": "d", "body": "Check {unit}."}, agent)
	if r.code != 200 || r.body["prompt"].(map[string]any)["scope"] != "personal" || !strings.HasPrefix(r.body["rules_version"].(string), "r-") {
		t.Fatalf("agent POST: %d %s", r.code, r.raw)
	}
	r = gpuCall(t, app, "GET", "/api/agent/v1/prompts", nil, agent)
	if r.code != 200 || len(promptIDs(r, "prompts")) != 3 {
		t.Fatalf("agent GET: %d %s", r.code, r.raw)
	}
	// The interface lists the Go-only routes.
	found := 0
	for _, route := range GoOnlyRoutes {
		if strings.Contains(route.ID, "/prompts") {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("GoOnlyRoutes has %d prompt routes", found)
	}
}

// Account mode: the project's prompts are for admins to change; a member's
// agent writes only into the scopes the member may change.
func TestPromptsPermissions(t *testing.T) {
	t.Setenv("ARK_PRIVATE_DIR", t.TempDir())
	app, projectID := authDesktopApp(t)
	owner := sessionCookie(app, "owner")
	owner["X-ARK-Project"] = projectID
	ana := sessionCookie(app, "ana") // read grant
	ana["X-ARK-Project"] = projectID
	r := gpuCall(t, app, "POST", "/api/prompts", map[string]any{"action": "save", "scope": "project",
		"prompt": map[string]any{"title": "Team", "body": "b"}}, owner)
	if r.code != 200 {
		t.Fatalf("owner project save: %d %s", r.code, r.raw)
	}
	if r = gpuCall(t, app, "GET", "/api/prompts", nil, ana); r.code != 200 || len(promptIDs(r, "project")) != 1 || r.body["can_edit_project"] != false {
		t.Fatalf("ana GET: %d %s", r.code, r.raw)
	}
	if r = gpuCall(t, app, "POST", "/api/prompts", map[string]any{"action": "save", "scope": "project",
		"prompt": map[string]any{"title": "Mine", "body": "b"}}, ana); r.code != 403 || r.errCode() != "PROMPT_PROJECT_ADMIN" {
		t.Fatalf("ana project save: %d %s", r.code, r.raw)
	}
	// Her own prompts and pins need only read access.
	if r = gpuCall(t, app, "POST", "/api/prompts", map[string]any{"action": "save", "scope": "personal",
		"prompt": map[string]any{"title": "Mine", "body": "b"}}, ana); r.code != 200 || len(promptIDs(r, "personal")) != 1 {
		t.Fatalf("ana personal save: %d %s", r.code, r.raw)
	}
	if r = gpuCall(t, app, "GET", "/api/prompts", nil, owner); len(promptIDs(r, "personal")) != 0 {
		t.Fatalf("personal prompts leaked to another person: %s", r.raw)
	}
	ctx := context.Background()
	ownerTok, _, err := app.Org.CreateSessionToken(ctx, "owner", "s_p1", "write", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	anaTok, _, err := app.Org.CreateSessionToken(ctx, "ana", "s_p2", "write", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	h := func(tok string) map[string]string {
		return map[string]string{"Authorization": "Bearer " + tok, "X-ARK-Project": projectID}
	}
	if r = gpuCall(t, app, "POST", "/api/agent/v1/prompts", map[string]any{"scope": "project", "title": "By agent", "body": "b"}, h(ownerTok)); r.code != 200 {
		t.Fatalf("owner agent project: %d %s", r.code, r.raw)
	}
	if r = gpuCall(t, app, "POST", "/api/agent/v1/prompts", map[string]any{"scope": "project", "title": "By agent", "body": "b"}, h(anaTok)); r.code != 403 {
		t.Fatalf("ana agent project: %d %s", r.code, r.raw)
	}
	if r = gpuCall(t, app, "GET", "/api/agent/v1/prompts", nil, h(ownerTok)); r.body["can_edit_project"] != true {
		t.Fatalf("an admin's agent: can_edit_project %v", r.body["can_edit_project"])
	}
	if r = gpuCall(t, app, "GET", "/api/agent/v1/prompts", nil, h(anaTok)); r.code != 200 || len(promptIDs(r, "prompts")) != 3 || r.body["can_edit_project"] != false {
		t.Fatalf("ana agent GET: %d %s", r.code, r.raw)
	}
}
