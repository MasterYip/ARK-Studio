package chat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

// Suggest mode changes nothing: write tools (built-in and extra) become
// proposals; read tools run.
func TestScheduledRunSuggestProposes(t *testing.T) {
	f := newFakeModel(t,
		map[string]any{"content": "", "tool_calls": []any{toolCall("c1", "list_units", map[string]any{"limit": 5})}},
		map[string]any{"content": "", "tool_calls": []any{
			toolCall("c2", "add_tags", map[string]any{"ref": "S-0001", "tags": []any{"branch:pilot"}}),
			toolCall("c3", "create_unit", map[string]any{"type": "note", "title": "Summary"}),
			toolCall("c4", "link_units", map[string]any{"source": "$n1", "target": "S-0001", "relation": "part_of"}),
		}},
		map[string]any{"content": "Proposed a tag and a note."},
	)
	e, uid := pluginEnv(t, f.URL)
	ran := false
	before := countUnits(t, e)
	res, err := e.ScheduledRun(ScheduledRequest{Name: "Tags", Prompt: "Tag things", Suggest: true,
		Extra: []ExtraTool{{Name: "add_tags", Write: true, Spec: fnSpec("add_tags", "add", params(pyjson.NewObject())),
			Run: func(*pyjson.Object) (*pyjson.Object, error) { ran = true; return pyjson.NewObject("uid", uid), nil }}}})
	if err != nil {
		t.Fatal(err)
	}
	if ran || countUnits(t, e) != before {
		t.Fatal("suggest mode wrote")
	}
	if len(res.Proposals) != 3 || res.Reads != 1 || res.Steps != 3 || res.Message != "Proposed a tag and a note." {
		t.Fatalf("result: %+v", res)
	}
	if len(res.Touched) != 1 || res.Touched[0] != uid {
		t.Fatalf("touched: %v", res.Touched)
	}
}

// Apply mode writes as the run's actor, up to the write cap.
func TestScheduledRunApplyAndWriteCap(t *testing.T) {
	f := newFakeModel(t,
		map[string]any{"content": "", "tool_calls": []any{
			toolCall("c1", "create_unit", map[string]any{"type": "note", "title": "One"}),
			toolCall("c2", "create_unit", map[string]any{"type": "note", "title": "Two"}),
		}},
		map[string]any{"content": "Done."},
	)
	e, _ := pluginEnv(t, f.URL)
	e.Actor = "assistant:schedule:st_x"
	before := countUnits(t, e)
	res, err := e.ScheduledRun(ScheduledRequest{Name: "Notes", Prompt: "Make notes", MaxWrites: 1})
	if err != nil {
		t.Fatal(err)
	}
	if countUnits(t, e) != before+1 || len(res.Writes) != 1 || len(res.Touched) != 1 {
		t.Fatalf("write cap: %d units, %+v", countUnits(t, e)-before, res)
	}
	acts, err := e.Store.Activity(e.Ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range acts {
		if o, ok := a.(*pyjson.Object); ok && o.Value("actor") == "assistant:schedule:st_x" {
			found = true
		}
	}
	if !found {
		t.Fatal("the write is not logged as the task")
	}
}

// The step and token caps end a run that keeps calling tools.
func TestScheduledRunCaps(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": "", "tool_calls": []any{toolCall("c", "get_context", map[string]any{})}}}},
			"usage":   map[string]any{"total_tokens": 1000},
		})
	}))
	t.Cleanup(srv.Close)
	e, _ := pluginEnv(t, srv.URL)
	res, err := e.ScheduledRun(ScheduledRequest{Name: "Loop", Prompt: "Loop", MaxSteps: 3})
	if err != nil {
		t.Fatal(err)
	}
	if res.Steps != 3 || calls != 3 {
		t.Fatalf("steps: %+v calls %d", res, calls)
	}
	calls = 0
	res, err = e.ScheduledRun(ScheduledRequest{Name: "Loop", Prompt: "Loop", MaxSteps: 20, MaxTokens: 2500})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stopped != "tokens" || calls != 3 || res.Tokens != 3000 {
		t.Fatalf("tokens: %+v calls %d", res, calls)
	}
	if _, err := (&Env{Ctx: e.Ctx, Store: e.Store, Config: pyjson.NewObject()}).ScheduledRun(ScheduledRequest{Prompt: "x"}); err != ErrNoModel {
		t.Fatalf("no model: %v", err)
	}
}
