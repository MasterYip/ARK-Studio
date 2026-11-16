package hostopen

import (
	"errors"
	"strings"
	"testing"

	"arkstudio/go/internal/api"
	"arkstudio/go/internal/api/apitest"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/store"
)

func TestOpenGuards(t *testing.T) {
	p := &project.Project{Path: t.TempDir(), Manifest: store.NewManifest(pyjson.NewObject("id", "p1"))}
	cases := []struct {
		name      string
		body      *pyjson.Object
		readToken bool
		status    int
		msg, code string
	}{
		{"read token", pyjson.NewObject("url", "https://x"), true, 403, "Task-agent read tokens cannot open resources on the host", ""},
		{"nothing", pyjson.NewObject(), false, 400, "open requires one of: unit, session+path, path, url", ""},
		{"blank fields", pyjson.NewObject("unit", "  ", "path", nil), false, 400, "open requires one of: unit, session+path, path, url", ""},
		{"session without path", pyjson.NewObject("session", "s_abcdefgh"), false, 400, "session open requests need a workspace-relative path", ""},
		{"session, sessions off", pyjson.NewObject("session", "s_abcdefgh", "path", "a"), false, 404, "session not found or not accessible", "SESSION_NOT_ACCESSIBLE"},
		{"path outside", pyjson.NewObject("path", "../outside.txt"), false, 0, "", ""},
	}
	for _, tc := range cases {
		c := &apitest.Ctx{Project: p, ReadToken: tc.readToken}
		err := (&Handlers{}).Open(c, tc.body)
		var ae *api.Error
		if tc.status == 0 {
			if err == nil {
				t.Errorf("%s: want an error", tc.name)
			}
			continue
		}
		if !errors.As(err, &ae) || ae.Status != tc.status || ae.Message != tc.msg || ae.Code != tc.code {
			t.Errorf("%s: %v", tc.name, err)
		}
		if tc.readToken && len(c.Levels) != 0 {
			t.Errorf("%s: read-token check must come before the project check", tc.name)
		}
	}
}

func headers(h [][2]string) string {
	var out []string
	for _, kv := range h {
		out = append(out, kv[0]+"="+kv[1])
	}
	return strings.Join(out, " ")
}

func TestLocalOpen(t *testing.T) {
	t.Setenv("ARK_LOCAL_OPEN_ORIGINS", "http://app.local/, http://other")
	t.Setenv("ARK_LOCAL_OPEN_ROOTS", "")
	cors := "Access-Control-Allow-Origin=http://app.local Access-Control-Allow-Methods=POST, OPTIONS " +
		"Access-Control-Allow-Headers=Content-Type Access-Control-Allow-Private-Network=true Vary=Origin"
	cases := []struct {
		name, peer, origin string
		body               *pyjson.Object
		status             int
		msg, headers       string
	}{
		{"remote peer", "10.0.0.2:5000", "http://app.local", nil, 403, "Local open is loopback-only", ""},
		{"mapped v4 peer", "[::ffff:127.0.0.1]:5000", "http://app.local", nil, 403, "Local open is loopback-only", ""},
		{"no origin", "127.0.0.1:5000", "", nil, 403, "Local open origin is not allowed", "Vary=Origin"},
		{"foreign origin", "[::1]:5000", "http://evil", nil, 403, "Local open origin is not allowed", "Vary=Origin"},
		{"no path", "127.0.0.1:5000", "http://app.local/", pyjson.NewObject("path", " "), 400, "path is required", cors},
		{"no roots", "127.0.0.1:5000", "http://app.local", pyjson.NewObject("path", "C:/x"), 403, "ARK_LOCAL_OPEN_ROOTS is not configured", cors},
	}
	for _, tc := range cases {
		c := &apitest.Ctx{Peer: tc.peer, Headers: map[string]string{"Origin": tc.origin}, Body: tc.body}
		if err := LocalOpen(c); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		r := c.Replies[0]
		obj, _ := r.Value.(*pyjson.Object)
		e, _ := obj.Value("error").(*pyjson.Object)
		if r.Status != tc.status || e == nil || e.Value("message") != tc.msg || headers(r.Headers) != tc.headers {
			t.Errorf("%s: %d %v %q", tc.name, r.Status, obj, headers(r.Headers))
		}
	}
}

func TestLocalOpenOptions(t *testing.T) {
	t.Setenv("ARK_LOCAL_OPEN_ORIGINS", "http://app.local")
	c := &apitest.Ctx{Headers: map[string]string{"Origin": "http://app.local"}}
	LocalOpenOptions(c)
	if r := c.Replies[0]; r.Status != 204 || r.ContentType != api.JSONType || len(r.Body) != 0 || len(r.Headers) != 5 {
		t.Errorf("allowed: %+v", r)
	}
	c = &apitest.Ctx{}
	LocalOpenOptions(c)
	if r := c.Replies[0]; r.Status != 403 || headers(r.Headers) != "Vary=Origin" {
		t.Errorf("refused: %+v", r)
	}
}
