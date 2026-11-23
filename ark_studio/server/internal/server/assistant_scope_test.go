package server

// The Assistant reads the person's other projects at their own access
// there (assistant_scope.go, internal/chat/scope.go): the current project is
// the default, other readable projects are reachable by id, unreadable ones
// are refused, writes need write access in the target project.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type scriptedCall struct {
	name string
	args map[string]any
}

// scopeModel is a fake OpenAI-compatible endpoint: the first request of a
// conversation gets the scripted tool calls, the next one a text answer.
// It records every request body.
func scopeModel(t *testing.T, calls []scriptedCall) (*httptest.Server, func() []map[string]any) {
	var mu sync.Mutex
	var seen []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		seen = append(seen, body)
		mu.Unlock()
		msgs, _ := body["messages"].([]any)
		var msg map[string]any
		if last, _ := msgs[len(msgs)-1].(map[string]any); last["role"] == "tool" {
			msg = map[string]any{"content": "Done."}
		} else {
			list := []any{}
			for i, c := range calls {
				args, _ := json.Marshal(c.args)
				list = append(list, map[string]any{"id": "c" + string(rune('a'+i)), "type": "function",
					"function": map[string]any{"name": c.name, "arguments": string(args)}})
			}
			msg = map[string]any{"content": "", "tool_calls": list}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": msg}}})
	}))
	t.Cleanup(srv.Close)
	return srv, func() []map[string]any { mu.Lock(); defer mu.Unlock(); return append([]map[string]any(nil), seen...) }
}

func scopeModelEnv(t *testing.T, url string) {
	t.Setenv("ARK_PRIVATE_DIR", t.TempDir())
	t.Setenv("ARK_CHAT_MODE", "")
	t.Setenv("ARK_DSH_COMMAND", "")
	t.Setenv("ARK_DEEPSEEK_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "sk-scope-test")
	t.Setenv("DEEPSEEK_BASE_URL", url+"/v1")
	t.Setenv("DEEPSEEK_MODEL", "fake-model")
}

// chatTools posts one message and returns the tool steps by name order.
func chatTools(t *testing.T, app *App, hdr map[string]string) []map[string]any {
	t.Helper()
	r := gpuCall(t, app, "POST", "/api/chat", map[string]any{"message": "Look around"}, hdr)
	if r.code != 200 {
		t.Fatalf("chat: %d %s", r.code, r.raw)
	}
	var out []map[string]any
	for _, tool := range r.body["assistant"].(map[string]any)["tools"].([]any) {
		out = append(out, tool.(map[string]any))
	}
	return out
}

func toolResult(tool map[string]any) map[string]any {
	m, _ := tool["result"].(map[string]any)
	return m
}

func TestAssistantScope(t *testing.T) {
	app, teamID, anaID := peopleApp(t) // ana: read on Team, admin of AnaProj
	owner := asIn(app, "owner", teamID)
	if r := gpuCall(t, app, "POST", "/api/units", map[string]any{"type": "idea", "title": "Team idea"}, owner); r.code != 200 {
		t.Fatalf("team unit: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "POST", "/api/units", map[string]any{"type": "idea", "title": "Team follow-up"}, owner); r.code != 200 {
		t.Fatalf("team unit: %d %s", r.code, r.raw)
	}
	gpuCall(t, app, "POST", "/api/links", map[string]any{"source": "I-0002", "target": "I-0001", "relation": "derived_from"}, owner)
	ana := asIn(app, "ana", anaID)
	if r := gpuCall(t, app, "POST", "/api/units", map[string]any{"type": "idea", "title": "Ana idea"}, ana); r.code != 200 {
		t.Fatalf("ana unit: %d %s", r.code, r.raw)
	}
	pr := gpuCall(t, app, "POST", "/api/projects", map[string]any{"name": "Private"}, sessionCookie(app, "owner"))
	privateID, _ := pr.body["summary"].(map[string]any)["id"].(string)
	if privateID == "" {
		t.Fatalf("private project: %s", pr.raw)
	}

	model, requests := scopeModel(t, []scriptedCall{
		{"list_projects", map[string]any{}},
		{"get_unit", map[string]any{"ref": "I-0001"}},
		{"search_units", map[string]any{"query": "Team", "project": teamID}},
		{"get_unit", map[string]any{"ref": "I-0001", "project": "Team"}},
		{"get_neighbors", map[string]any{"ref": "I-0001", "project": teamID}},
		{"get_unit", map[string]any{"ref": "I-0001", "project": privateID}},
		{"create_unit", map[string]any{"type": "idea", "title": "Sneaky", "project": teamID}},
		{"create_unit", map[string]any{"type": "idea", "title": "Ana note"}},
	})
	scopeModelEnv(t, model.URL)
	tools := chatTools(t, app, ana)
	if len(tools) != 8 {
		t.Fatalf("tools: %v", tools)
	}

	// The project list has only the projects ana can read, with her access.
	listed := map[string]string{}
	for _, p := range toolResult(tools[0])["projects"].([]any) {
		m := p.(map[string]any)
		listed[m["id"].(string)] = m["role"].(string)
	}
	if len(listed) != 2 || listed[anaID] != "admin" || listed[teamID] != "read" || toolResult(tools[0])["current"] != anaID {
		t.Errorf("list_projects: %v", toolResult(tools[0]))
	}
	// The default is the current project: I-0001 there is Ana's own idea.
	if u := toolResult(tools[1])["unit"].(map[string]any); u["title"] != "Ana idea" || toolResult(tools[1])["project"] != nil {
		t.Errorf("default project: %v", toolResult(tools[1]))
	}
	// Another readable project, by id and by name, names the project and the reference to cite.
	search := toolResult(tools[2])
	items := search["items"].([]any)
	if len(items) != 2 || search["project"].(map[string]any)["id"] != teamID ||
		!strings.HasPrefix(items[0].(map[string]any)["cite"].(string), "[["+teamID+"/I-000") {
		t.Errorf("search other project: %v", search)
	}
	if u := toolResult(tools[3]); u["unit"].(map[string]any)["title"] != "Team idea" || u["unit"].(map[string]any)["cite"] != "[["+teamID+"/I-0001]]" {
		t.Errorf("get_unit other project: %v", u)
	}
	nb := toolResult(tools[4])
	if nodes := nb["nodes"].([]any); len(nodes) != 2 || len(nb["edges"].([]any)) != 1 {
		t.Errorf("neighbours: %v", nb)
	}
	// A project ana cannot read is refused.
	if r := toolResult(tools[5]); r["error"] == nil || tools[5]["ok"] != false || r["unit"] != nil {
		t.Errorf("unreadable project: %v", tools[5])
	}
	// Read access is not write access.
	if r := toolResult(tools[6]); r["error"] == nil || !strings.Contains(r["error"].(string), "write access") {
		t.Errorf("write to a read-only project: %v", tools[6])
	}
	if r := gpuCall(t, app, "GET", "/api/units?q=Sneaky", nil, owner); len(r.body["items"].([]any)) != 0 {
		t.Errorf("the read-only project changed: %s", r.raw)
	}
	// Writes in the current project work as before.
	if r := toolResult(tools[7]); r["uid"] == nil || r["project"] != nil {
		t.Errorf("write in the current project: %v", tools[7])
	}

	// The model is told the scope: Team is named, Private is not, and how to cite.
	reqs := requests()
	system := reqs[0]["messages"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.Contains(system, teamID) || strings.Contains(system, privateID) || !strings.Contains(system, "[[<project id>/<UID>]]") {
		t.Errorf("system prompt:\n%s", system)
	}
	specs, _ := json.Marshal(reqs[0]["tools"])
	if !strings.Contains(string(specs), `"list_projects"`) || !strings.Contains(string(specs), `"get_neighbors"`) {
		t.Errorf("tool specs: %s", specs)
	}
}

// An admin of the other project may change it through the explicit
// project argument; the read-only grant above may not.
func TestAssistantScopeWritesFollowAccess(t *testing.T) {
	app, teamID, anaID := peopleApp(t) // the owner is admin of both
	model, _ := scopeModel(t, []scriptedCall{
		{"create_unit", map[string]any{"type": "idea", "title": "From Team", "project": anaID}},
	})
	scopeModelEnv(t, model.URL)
	tools := chatTools(t, app, asIn(app, "owner", teamID))
	r := toolResult(tools[0])
	if r["error"] != nil || r["project"].(map[string]any)["id"] != anaID || !strings.HasPrefix(r["cite"].(string), "[["+anaID+"/") {
		t.Fatalf("write in another writable project: %v", tools[0])
	}
	if got := gpuCall(t, app, "GET", "/api/units?q=From+Team", nil, asIn(app, "owner", anaID)); len(got.body["items"].([]any)) != 1 {
		t.Errorf("unit not in AnaProj: %s", got.raw)
	}
	if got := gpuCall(t, app, "GET", "/api/units?q=From+Team", nil, asIn(app, "owner", teamID)); len(got.body["items"].([]any)) != 0 {
		t.Errorf("unit leaked into Team: %s", got.raw)
	}
}
