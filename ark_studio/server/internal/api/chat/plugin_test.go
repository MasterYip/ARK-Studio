package chat

import (
	"errors"
	"strings"
	"testing"
	"time"

	"arkstudio/go/internal/api"
	"arkstudio/go/internal/api/apitest"
	"arkstudio/go/internal/compat/pyjson"
)

func TestWindowLimiter(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newWindowLimiter(3, time.Minute)
	l.now = func() time.Time { return now }
	got := []bool{l.allow("a"), l.allow("a"), l.allow("a"), l.allow("a"), l.allow("b")}
	if want := []bool{true, true, true, false, true}; !equalBools(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	now = now.Add(59 * time.Second)
	if l.allow("a") {
		t.Error("still inside the window")
	}
	now = now.Add(2 * time.Second)
	if !l.allow("a") {
		t.Error("window passed")
	}
}

func equalBools(a, b []bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestPluginChatRequest(t *testing.T) {
	msg := func(role, content string) *pyjson.Object { return pyjson.NewObject("role", role, "content", content) }
	ok := pyjson.NewObject("plugin", "assistant-demo", "messages", []any{msg("user", " hi ")}, "system", " Be brief. ", "tools", false)
	req, err := pluginChatRequest(ok)
	if err != nil || req.Plugin != "assistant-demo" || req.System != "Be brief." || req.Tools ||
		pyjson.Str(req.Messages[0].(*pyjson.Object).Value("content")) != "hi" {
		t.Fatalf("%+v %v", req, err)
	}
	if req, _ := pluginChatRequest(pyjson.NewObject("plugin", "abc", "messages", []any{msg("user", "x")})); !req.Tools {
		t.Error("tools default on")
	}
	many := make([]any, 21)
	for i := range many {
		many[i] = msg("user", "x")
	}
	for i, body := range []*pyjson.Object{
		pyjson.NewObject("messages", []any{msg("user", "x")}),
		pyjson.NewObject("plugin", "u-x", "messages", []any{msg("user", "x")}),
		pyjson.NewObject("plugin", "abc", "messages", "hi"),
		pyjson.NewObject("plugin", "abc", "messages", many),
		pyjson.NewObject("plugin", "abc", "messages", []any{msg("user", "  ")}),
		pyjson.NewObject("plugin", "abc", "messages", []any{msg("assistant", "x")}),
		pyjson.NewObject("plugin", "abc", "messages", []any{"text"}),
		pyjson.NewObject("plugin", "abc", "messages", []any{msg("user", strings.Repeat("字", 8001))}),
		pyjson.NewObject("plugin", "abc", "messages", []any{msg("user", strings.Repeat("x", 8000)), msg("assistant", strings.Repeat("x", 8000)),
			msg("user", strings.Repeat("x", 8000)), msg("assistant", strings.Repeat("x", 8000)), msg("user", strings.Repeat("x", 8000)),
			msg("assistant", strings.Repeat("x", 8000)), msg("user", "x")}),
		pyjson.NewObject("plugin", "abc", "messages", []any{msg("user", "x")}, "system", strings.Repeat("s", 4001)),
	} {
		var ae *api.Error
		if _, err := pluginChatRequest(body); !errors.As(err, &ae) || ae.Status != 400 {
			t.Errorf("%d: %v", i, err)
		}
	}
}

func TestPluginChatNeedsWriteAccess(t *testing.T) {
	h := &Handlers{}
	c := &apitest.Ctx{RequireErr: api.Err(403, "Write access required", "")}
	var ae *api.Error
	if err := h.PluginChat(c, pyjson.NewObject(), "human:ui"); !errors.As(err, &ae) || ae.Status != 403 || c.Level != "write" {
		t.Errorf("%v level %q", err, c.Level)
	}
}
