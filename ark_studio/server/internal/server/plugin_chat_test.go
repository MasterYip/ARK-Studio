package server

// D-0003: POST /api/plugins/ai/chat (the ai:chat permission of user view
// plugins) and the X-ARK-Plugin write actor.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// pluginModel is a fake OpenAI-compatible endpoint: it proposes a claim and
// a link on the first request of each conversation and answers in text on
// the second.
func pluginModel(t *testing.T) (*httptest.Server, func() int) {
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls++
		mu.Unlock()
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		_ = json.Unmarshal(raw, &body)
		var msg map[string]any
		if last := body.Messages[len(body.Messages)-1]; last["role"] == "tool" {
			msg = map[string]any{"content": "Proposed a summary claim."}
		} else {
			args, _ := json.Marshal(map[string]any{"type": "claim", "title": "Summary"})
			link, _ := json.Marshal(map[string]any{"source": "$n1", "target": "S-0001", "relation": "supports"})
			msg = map[string]any{"content": "", "tool_calls": []any{
				map[string]any{"id": "a", "type": "function", "function": map[string]any{"name": "create_unit", "arguments": string(args)}},
				map[string]any{"id": "b", "type": "function", "function": map[string]any{"name": "link_units", "arguments": string(link)}},
			}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": msg}}})
	}))
	t.Cleanup(srv.Close)
	return srv, func() int { mu.Lock(); defer mu.Unlock(); return calls }
}

func pluginChatBody(plugin string) map[string]any {
	return map[string]any{"plugin": plugin, "messages": []any{map[string]any{"role": "user", "content": "Summarise S-0001"}},
		"system": "Propose one claim."}
}

func TestPluginAIChatRoute(t *testing.T) {
	t.Setenv("ARK_PRIVATE_DIR", t.TempDir())
	t.Setenv("ARK_CHAT_MODE", "")
	t.Setenv("ARK_DSH_COMMAND", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("ARK_DEEPSEEK_API_KEY", "")
	app, _ := chatScopeApp(t)
	hdr := map[string]string{"X-ARK-Plugin": "assistant-demo"}

	// No model configured.
	r := gpuCall(t, app, "POST", "/api/plugins/ai/chat", pluginChatBody("assistant-demo"), hdr)
	if r.code != 400 || r.errCode() != "AI_NOT_CONFIGURED" {
		t.Fatalf("no model: %d %s", r.code, r.raw)
	}

	model, calls := pluginModel(t)
	t.Setenv("DEEPSEEK_API_KEY", "sk-plugin-secret")
	t.Setenv("DEEPSEEK_BASE_URL", model.URL+"/v1")
	t.Setenv("DEEPSEEK_MODEL", "fake-model")

	r = gpuCall(t, app, "POST", "/api/plugins/ai/chat", pluginChatBody("assistant-demo"), hdr)
	if r.code != 200 || r.body["message"] != "Proposed a summary claim." || r.body["model"] != "fake-model" {
		t.Fatalf("chat: %d %s", r.code, r.raw)
	}
	if strings.Contains(r.raw, "sk-plugin-secret") {
		t.Fatal("the API key must never reach the plugin")
	}
	proposals, _ := r.body["proposals"].([]any)
	if len(proposals) != 2 || proposals[0].(map[string]any)["ref"] != "$n1" || proposals[1].(map[string]any)["source"] != "$n1" {
		t.Fatalf("proposals: %s", r.raw)
	}
	// Proposals are not applied; the call is in the activity, named after the plugin.
	units := gpuCall(t, app, "GET", "/api/units?q=Summary", nil, nil)
	if list, _ := units.body["items"].([]any); len(list) != 0 {
		t.Errorf("ai:chat wrote units: %s", units.raw)
	}
	act := gpuCall(t, app, "GET", "/api/activity?limit=5", nil, nil)
	if !strings.Contains(act.raw, `"actor": "human:plugin:assistant-demo", "action": "plugin.ai.chat"`) ||
		!strings.Contains(act.raw, `"proposals": 2`) {
		t.Errorf("activity: %s", act.raw)
	}
	// No chat history is stored.
	if hist := gpuCall(t, app, "GET", "/api/chat", nil, nil); strings.Contains(hist.raw, "Summarise S-0001") {
		t.Errorf("history persisted: %s", hist.raw)
	}

	// Validation.
	for _, body := range []map[string]any{
		{"plugin": "Bad Id", "messages": []any{map[string]any{"role": "user", "content": "x"}}},
		{"plugin": "assistant-demo", "messages": []any{}},
		{"plugin": "assistant-demo", "messages": []any{map[string]any{"role": "system", "content": "x"}}},
		{"plugin": "assistant-demo", "messages": []any{map[string]any{"role": "user", "content": "x"}, map[string]any{"role": "assistant", "content": "y"}}},
		{"plugin": "assistant-demo", "messages": []any{map[string]any{"role": "user", "content": strings.Repeat("x", 8001)}}},
		{"plugin": "assistant-demo", "messages": []any{map[string]any{"role": "user", "content": "x"}}, "system": 7},
	} {
		if r := gpuCall(t, app, "POST", "/api/plugins/ai/chat", body, hdr); r.code != 400 {
			t.Errorf("%v: %d %s", body, r.code, r.raw)
		}
	}

	// Rate limit: 20 calls per minute per user and plugin; other plugins are separate.
	limited := false
	for i := 0; i < 25; i++ {
		r := gpuCall(t, app, "POST", "/api/plugins/ai/chat", pluginChatBody("assistant-demo"), hdr)
		if r.code == 429 {
			limited = r.errCode() == "RATE_LIMITED"
			if i != 19 {
				t.Errorf("limited after %d more calls, want 19", i)
			}
			break
		}
	}
	if !limited {
		t.Error("no rate limit")
	}
	before := calls()
	if r := gpuCall(t, app, "POST", "/api/plugins/ai/chat", pluginChatBody("other-plugin"), nil); r.code != 200 {
		t.Errorf("other plugin: %d %s", r.code, r.raw)
	}
	if calls() != before+2 {
		t.Errorf("model calls %d -> %d", before, calls())
	}
}

func TestPluginWriteActor(t *testing.T) {
	app, _ := chatScopeApp(t)
	create := func(title string, headers map[string]string) {
		if r := gpuCall(t, app, "POST", "/api/units", map[string]any{"type": "idea", "title": title}, headers); r.code != 200 {
			t.Fatalf("create %s: %d %s", title, r.code, r.raw)
		}
	}
	create("by plugin", map[string]string{"X-ARK-Plugin": "assistant-demo"})
	create("bad header", map[string]string{"X-ARK-Plugin": "Not An Id"})
	create("agent wins", map[string]string{"X-ARK-Plugin": "assistant-demo", "X-Agent-Name": "codex"})
	act := gpuCall(t, app, "GET", "/api/activity?limit=10", nil, nil)
	for title, actor := range map[string]string{"by plugin": "human:plugin:assistant-demo", "bad header": "human:ui", "agent wins": "agent:codex"} {
		if !strings.Contains(act.raw, `"actor": "`+actor+`", "action": "unit.create"`) || !strings.Contains(act.raw, title) {
			t.Errorf("%s: want actor %s in %s", title, actor, act.raw)
		}
	}
}
