package chat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/store"
)

// fakeModel is an OpenAI-compatible /chat/completions endpoint answering
// the scripted assistant messages in order and recording every request
// (like the chat replay's FakeLLM, tests/python/contract/replay_services.py).
type fakeModel struct {
	*httptest.Server
	mu       sync.Mutex
	replies  []map[string]any
	requests []map[string]any
	auth     []string
}

func newFakeModel(t *testing.T, replies ...map[string]any) *fakeModel {
	f := &fakeModel{replies: replies}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.requests = append(f.requests, body)
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		n := len(f.requests) - 1
		f.mu.Unlock()
		if r.URL.Path != "/v1/chat/completions" || n >= len(f.replies) {
			http.Error(w, "unexpected", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": f.replies[n]}}})
	}))
	t.Cleanup(f.Close)
	return f
}

func toolCall(id, name string, args map[string]any) map[string]any {
	raw, _ := json.Marshal(args)
	return map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(raw)}}
}

func pluginEnv(t *testing.T, baseURL string) (*Env, string) {
	t.Helper()
	st, err := store.Open(t.TempDir(), store.NewManifest(pyjson.NewObject("id", "p1", "name", "Plugin project")), 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	unit, err := st.CreateUnit(ctx, pyjson.NewObject("type", "story", "title", "Base story", "body", "Long text."), "human:ui", store.Who{})
	if err != nil {
		t.Fatal(err)
	}
	config := pyjson.NewObject("api_key", "sk-secret", "base_url", baseURL+"/v1", "model", "fake-model",
		"reasoning_effort", "low", "timeout", int64(10))
	return &Env{Ctx: ctx, Store: st, ProjectName: "Plugin project", Config: config}, pyjson.Str(unit.Value("uid"))
}

func countUnits(t *testing.T, e *Env) int {
	list, err := e.list(listFilters("limit", int64(100)))
	if err != nil {
		t.Fatal(err)
	}
	return len(items(list))
}

// D-0003: read tools run, write tools become proposals (node:write ops),
// nothing is written, no history is used, the key stays server-side.
func TestPluginChatProposesWrites(t *testing.T) {
	var base string
	model := newFakeModel(t,
		map[string]any{"content": "", "tool_calls": []any{
			toolCall("c1", "get_unit", map[string]any{"ref": "S-0001"}),
			toolCall("c2", "create_unit", map[string]any{"type": "claim", "title": "Summary claim", "body": "One line.", "tags": []any{"summary", " "}}),
			toolCall("c3", "link_units", map[string]any{"source": "$n1", "target": "S-0001", "relation": "supports"}),
			toolCall("c4", "update_unit", map[string]any{"ref": "not a uid", "status": "done"}),
			toolCall("c5", "update_unit", map[string]any{"ref": "S-0001", "status": "done"}),
			toolCall("c6", "delete_project", map[string]any{}),
		}},
		map[string]any{"content": "I propose a claim that summarises S-0001."},
	)
	base = model.URL
	env, uid := pluginEnv(t, base)
	if uid != "S-0001" {
		t.Fatalf("uid %q", uid)
	}
	before := countUnits(t, env)
	out, err := env.PluginChat(PluginRequest{Plugin: "assistant-demo", System: "Summarise into one claim.", Tools: true,
		Messages: []any{pyjson.NewObject("role", "user", "content", "Summarise the selected node")}})
	if err != nil {
		t.Fatal(err)
	}
	if got := pyjson.Str(out.Value("message")); got != "I propose a claim that summarises S-0001." {
		t.Errorf("message %q", got)
	}
	if countUnits(t, env) != before {
		t.Error("a plugin conversation must not write")
	}
	raw := string(pyjson.Dumps(out.Value("proposals")))
	want := `[{"op": "create", "ref": "$n1", "title": "Summary claim", "type": "claim", "body": "One line.", "tags": ["summary"]}, ` +
		`{"op": "link", "source": "$n1", "target": "S-0001", "relation": "supports"}, ` +
		`{"op": "update", "uid": "S-0001", "status": "done"}]`
	if raw != want {
		t.Errorf("proposals\n got %s\nwant %s", raw, want)
	}
	if reads := string(pyjson.Dumps(out.Value("reads"))); reads != `[{"name": "get_unit", "args": {"ref": "S-0001"}, "ok": true}]` {
		t.Errorf("reads %s", reads)
	}
	if out.Value("model") != "fake-model" || strings.Contains(string(pyjson.Dumps(out)), "sk-secret") {
		t.Errorf("model / key leak: %s", pyjson.Dumps(out))
	}
	if len(model.requests) != 2 || model.auth[0] != "Bearer sk-secret" {
		t.Fatalf("requests %d auth %v", len(model.requests), model.auth)
	}
	first := model.requests[0]
	msgs := first["messages"].([]any)
	system := msgs[0].(map[string]any)["content"].(string)
	if !strings.Contains(system, `"assistant-demo"`) || !strings.Contains(system, "Summarise into one claim.") ||
		!strings.Contains(system, "do NOT change the project") {
		t.Errorf("system prompt %q", system)
	}
	if len(msgs) != 2 || msgs[1].(map[string]any)["content"] != "Summarise the selected node" {
		t.Errorf("no history, only the plugin's messages: %v", msgs)
	}
	var names []string
	for _, spec := range first["tools"].([]any) {
		names = append(names, spec.(map[string]any)["function"].(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "get_context,search_units,list_units,get_unit,create_unit,update_unit,link_units" {
		t.Errorf("tools %v", names)
	}
	// The tool answers the model saw: the read ran, the writes were proposals.
	second := model.requests[1]["messages"].([]any)
	answers := map[string]string{}
	for _, m := range second {
		mm := m.(map[string]any)
		if mm["role"] == "tool" {
			answers[mm["tool_call_id"].(string)] = mm["content"].(string)
		}
	}
	if !strings.Contains(answers["c1"], "Base story") || !strings.Contains(answers["c2"], `"ref": "$n1"`) ||
		!strings.Contains(answers["c4"], "error") || !strings.Contains(answers["c6"], "Unknown tool") {
		t.Errorf("tool answers %v", answers)
	}
}

func TestPluginChatWithoutToolsOrModel(t *testing.T) {
	model := newFakeModel(t, map[string]any{"content": "Plain answer", "tool_calls": []any{toolCall("x", "create_unit", map[string]any{"title": "x"})}})
	env, _ := pluginEnv(t, model.URL)
	out, err := env.PluginChat(PluginRequest{Plugin: "p-one", Messages: []any{pyjson.NewObject("role", "user", "content", "hi")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, sent := model.requests[0]["tools"]; sent || out.Value("message") != "Plain answer" ||
		string(pyjson.Dumps(out.Value("proposals"))) != "[]" {
		t.Errorf("tools off: %v %s", model.requests[0]["tools"], pyjson.Dumps(out))
	}
	env.Config.Set("api_key", "")
	if _, err := env.PluginChat(PluginRequest{Plugin: "p-one", Messages: []any{pyjson.NewObject("role", "user", "content", "hi")}}); !errors.Is(err, ErrNoModel) {
		t.Errorf("no key: %v", err)
	}
}

func TestPluginChatModelError(t *testing.T) {
	model := newFakeModel(t) // no scripted reply: HTTP 500
	env, _ := pluginEnv(t, model.URL)
	_, err := env.PluginChat(PluginRequest{Plugin: "p-one", Tools: true, Messages: []any{pyjson.NewObject("role", "user", "content", "hi")}})
	var pe *PyError
	if !errors.As(err, &pe) || pe.Type != "DeepSeekError" || !strings.Contains(pe.Message, "HTTP 500") {
		t.Errorf("model error: %v", err)
	}
}
