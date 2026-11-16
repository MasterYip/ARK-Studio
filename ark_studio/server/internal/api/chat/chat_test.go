package chat

import (
	"errors"
	"strings"
	"testing"

	"arkstudio/go/internal/api"
	"arkstudio/go/internal/api/apitest"
	"arkstudio/go/internal/assistcfg"
	"arkstudio/go/internal/chat"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/infohub"
	"arkstudio/go/internal/platform/pyerr"
	"arkstudio/go/internal/store"
)

func TestChatMessage(t *testing.T) {
	cases := []struct {
		body *pyjson.Object
		want string
		err  string
	}{
		{pyjson.NewObject("message", "  hi\n"), "hi", ""},
		{pyjson.NewObject("message", 12), "12", ""},
		{pyjson.NewObject(), "", "message is required"},
		{pyjson.NewObject("message", " \u3000 "), "", "message is required"},
		{pyjson.NewObject("message", strings.Repeat("字", 8000)), strings.Repeat("字", 8000), ""},
		{pyjson.NewObject("message", strings.Repeat("字", 8001)), "", "message is too long"},
	}
	for i, tc := range cases {
		got, err := chatMessage(tc.body)
		var ae *api.Error
		if tc.err != "" {
			if !errors.As(err, &ae) || ae.Status != 400 || ae.Message != tc.err {
				t.Errorf("%d: %v, want 400 %q", i, err, tc.err)
			}
		} else if err != nil || got != tc.want {
			t.Errorf("%d: %q %v", i, got, err)
		}
	}
}

func TestChatErr(t *testing.T) {
	var ve *pyerr.ValueError
	var ie *pyerr.InternalError
	if !errors.As(chatErr(&assistcfg.Error{Type: "ValueError", Message: "m"}), &ve) || ve.Message != "m" {
		t.Error("assistcfg ValueError")
	}
	if !errors.As(chatErr(&assistcfg.Error{Type: "OSError", Message: "m"}), &ie) {
		t.Error("assistcfg other")
	}
	if !errors.As(chatErr(&chat.PyError{Type: "DeepSeekError", Message: "d"}), &ve) {
		t.Error("DeepSeekError is a ValueError")
	}
	if !errors.As(chatErr(&chat.PyError{Type: "KeyError", Message: "k"}), &ie) {
		t.Error("other PyError is internal")
	}
	plain := errors.New("x")
	if chatErr(plain) != plain {
		t.Error("pass-through")
	}
}

func TestInfohubErr(t *testing.T) {
	sqlErr := &infohub.SQLError{Message: "no such table"}
	var ae *api.Error
	if !errors.As(infohubErr(sqlErr, true), &ae) || ae.Status != 500 || ae.Message != "Database error: no such table" {
		t.Error("POST maps to 500 Database error")
	}
	var ie *pyerr.InternalError
	if !errors.As(infohubErr(sqlErr, false), &ie) || ie.Message != "no such table" {
		t.Error("GET maps to an internal error")
	}
	if infohubErr(nil, true) != nil {
		t.Error("nil stays nil")
	}
}

func TestPyStr(t *testing.T) {
	for err, want := range map[error]string{
		api.Err(400, "a", "C"):                    "a",
		&store.Error{Message: "b"}:                "b",
		&pyerr.ValueError{Message: "c"}:           "c",
		&chat.PyError{Type: "X", Message: "d"}:    "d",
		&infohub.SQLError{Message: "e"}:           "e",
		&assistcfg.Error{Type: "T", Message: "f"}: "f",
		errors.New("g"):                           "g",
	} {
		if got := pyStr(err); got != want {
			t.Errorf("pyStr(%T) = %q, want %q", err, got, want)
		}
	}
}

func TestPathArg(t *testing.T) {
	if s, err := pathArg("a.db"); s != "a.db" || err != nil {
		t.Error("string")
	}
	var ie *pyerr.InternalError
	if _, err := pathArg(int64(3)); !errors.As(err, &ie) || ie.Message != "expected str, bytes or os.PathLike object, not int" {
		t.Errorf("int: %v", err)
	}
}

func TestRoutesNeedingArguments(t *testing.T) {
	h := &Handlers{}
	cases := []struct {
		path   string
		q      map[string][]string
		status int
		msg    string
	}{
		{"/api/assistant/dispatch", nil, 400, "task is required"},
		{"/api/import/infohub/preview", nil, 400, "path is required"},
	}
	for _, tc := range cases {
		c := &apitest.Ctx{Q: tc.q}
		handled, err := h.Get(c, tc.path)
		var ae *api.Error
		if !handled || !errors.As(err, &ae) || ae.Status != tc.status || ae.Message != tc.msg {
			t.Errorf("%s: %v %v", tc.path, handled, err)
		}
	}
	if handled, _ := h.Get(&apitest.Ctx{}, "/api/elsewhere"); handled {
		t.Error("unknown GET must not be handled")
	}
	if handled, _ := h.Post(&apitest.Ctx{}, "/api/open", pyjson.NewObject(), ""); handled {
		t.Error("/api/open belongs to hostopen")
	}
}

func TestStreamWritesErrorLine(t *testing.T) {
	h := &Handlers{}
	c := &apitest.Ctx{RequireErr: api.Err(403, "Write access required", "")}
	if err := h.Stream(c); err != nil {
		t.Fatal(err)
	}
	if got := c.StreamBody.String(); got != "{\"type\": \"error\", \"message\": \"Write access required\"}\n" || !c.Detached {
		t.Errorf("stream %q detached %v", got, c.Detached)
	}
	if len(c.StreamExtra) != 5 || c.StreamExtra[0][1] != "application/x-ndjson; charset=utf-8" {
		t.Errorf("headers %v", c.StreamExtra)
	}
}
