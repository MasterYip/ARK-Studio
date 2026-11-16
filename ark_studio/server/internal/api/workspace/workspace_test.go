package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkstudio/go/internal/api"
	"arkstudio/go/internal/api/apitest"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pyurl"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/store"
	"arkstudio/go/internal/workspace"
)

func handlers() *Handlers {
	return &Handlers{Terminals: workspace.NewManager(), Done: func() <-chan struct{} { return nil },
		GuessType: func(name string) string { return "type/" + filepath.Ext(name) }}
}

func apiStatus(err error) (int, string) {
	var ae *api.Error
	if errors.As(err, &ae) {
		return ae.Status, ae.Message
	}
	return 0, ""
}

func TestIsPath(t *testing.T) {
	for path, want := range map[string]bool{
		"/api/workspace": true, "/api/workspace/": true, "/api/workspace/tree": true,
		"/api/workspaces": false, "/api/workspac": false, "/api/work/space": false,
	} {
		if IsPath(path) != want {
			t.Errorf("IsPath(%q) != %v", path, want)
		}
	}
}

func TestGet(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := newProject(root)
	cases := []struct {
		name, path string
		q          pyurl.Values
		reqErr     error
		status     int
		msg        string
	}{
		{"no project", "/api/workspace/info", nil, api.Err(403, "no", ""), 403, "no"},
		{"file without path", "/api/workspace/file", nil, nil, 400, "path is required"},
		{"diff without path", "/api/workspace/diff", nil, nil, 400, "path is required"},
		{"unknown", "/api/workspace/nope", nil, nil, 404, "Unknown workspace endpoint: /api/workspace/nope"},
		{"tree", "/api/workspace/tree", nil, nil, 200, ""},
		{"file", "/api/workspace/file", pyurl.Values{"path": {"a.txt"}}, nil, 200, ""},
		{"search", "/api/workspace/search", pyurl.Values{"q": {"a"}}, nil, 200, ""},
	}
	for _, tc := range cases {
		c := &apitest.Ctx{Q: tc.q, Project: p, RequireErr: tc.reqErr}
		err := handlers().Get(c, tc.path)
		if tc.status == 200 {
			if err != nil || c.Sent == nil {
				t.Errorf("%s: %v (sent %v)", tc.name, err, c.Sent)
			}
		} else if st, msg := apiStatus(err); st != tc.status || msg != tc.msg {
			t.Errorf("%s: %d %q, want %d %q", tc.name, st, msg, tc.status, tc.msg)
		}
		if strings.Join(c.Levels, ",") != "read" {
			t.Errorf("%s: levels %v", tc.name, c.Levels)
		}
	}
}

func TestGetRawUsesGuessType(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "x.csv"), []byte("a,b\n"), 0o644) //nolint:errcheck
	for _, tc := range []struct {
		download string
		want     string
	}{{"", ""}, {"1", "download"}, {"true", "download"}, {"yes", ""}} {
		c := &apitest.Ctx{Q: pyurl.Values{"path": {"x.csv"}, "download": {tc.download}}, Project: newProject(root)}
		if err := handlers().Get(c, "/api/workspace/raw"); err != nil {
			t.Fatal(err)
		}
		r := c.Replies[0]
		if r.ContentType != "type/.csv" || string(r.Body) != tc.want || !strings.HasSuffix(r.Value.(string), "x.csv") {
			t.Errorf("download=%q: %+v", tc.download, r)
		}
	}
}

func TestPostGuards(t *testing.T) {
	p := newProject(t.TempDir())
	cases := []struct {
		name, path string
		readToken  bool
		levels     string
		status     int
		msg        string
	}{
		{"read token", "/api/workspace/file", true, "", 403, "task-agent read tokens cannot use the files workspace"},
		{"terminal needs admin first", "/api/workspace/terminals/t1/cancel", false, "admin,read", 0, ""},
		{"files need read", "/api/workspace/nope", false, "read", 404, "Unknown workspace endpoint: /api/workspace/nope"},
	}
	for _, tc := range cases {
		c := &apitest.Ctx{Project: p, ReadToken: tc.readToken}
		err := handlers().Post(c, tc.path, pyjson.NewObject())
		if strings.Join(c.Levels, ",") != tc.levels {
			t.Errorf("%s: levels %v, want %s", tc.name, c.Levels, tc.levels)
		}
		if tc.status != 0 {
			if st, msg := apiStatus(err); st != tc.status || msg != tc.msg {
				t.Errorf("%s: %d %q", tc.name, st, msg)
			}
		}
	}
}

func TestDeleteGuards(t *testing.T) {
	c := &apitest.Ctx{ReadToken: true}
	if st, _ := apiStatus(handlers().Delete(c, "t1")); st != 403 || len(c.Levels) != 0 {
		t.Errorf("read token: %d %v", st, c.Levels)
	}
	c = &apitest.Ctx{RequireErr: api.Err(403, "admin", "")}
	if st, msg := apiStatus(handlers().Delete(c, "t1")); st != 403 || msg != "admin" || c.Levels[0] != "admin" {
		t.Errorf("admin: %d %q %v", st, msg, c.Levels)
	}
}

func newProject(root string) *project.Project {
	return &project.Project{Path: root, Manifest: store.NewManifest(pyjson.NewObject("id", "p1", "name", "P"))}
}

func TestTerminalsListAndUnknownTerminal(t *testing.T) {
	c := &apitest.Ctx{Project: newProject(t.TempDir())}
	if err := handlers().Get(c, "/api/workspace/terminals"); err != nil || c.Sent == nil {
		t.Fatalf("list: %v", err)
	}
	if err := handlers().Get(c, "/api/workspace/terminals/t-missing"); err == nil {
		t.Fatal("unknown terminal must fail")
	}
}
