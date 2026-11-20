package sessions

import (
	"errors"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

func TestStartContextEntries(t *testing.T) {
	m := &Manager{}
	h := &Hooks{Unit: func(uid string) (*pyjson.Object, error) {
		if uid != "T-0042" {
			return nil, errors.New("missing")
		}
		return pyjson.NewObject("uid", "T-0042", "title", "Sidebar", "type", "task", "status", "active", "body", "Plan text"), nil
	}}
	if got, err := m.startContextEntries(h, nil); err != nil || got != nil {
		t.Fatalf("no context: %v %v", got, err)
	}
	raw := []any{pyjson.NewObject("kind", "unit", "uid", "T-0042"), pyjson.NewObject("kind", "text", "text", "note", "label", "Hint")}
	entries, err := m.startContextEntries(h, raw)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries %v err %v", entries, err)
	}
	msg := composeMessage("Do it", entries)
	for _, want := range []string{"Do it", "## Attached context", "### Project unit `T-0042`", "Plan text", "### Snippet `Hint`"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
	if _, err := m.startContextEntries(h, []any{pyjson.NewObject("kind", "file", "path", "a.txt")}); err == nil {
		t.Error("a file cannot be attached before the session has a workspace")
	}
	if _, err := m.startContextEntries(h, []any{pyjson.NewObject("kind", "unit", "uid", "T-0001")}); err == nil {
		t.Error("an unknown unit is refused")
	}
}
