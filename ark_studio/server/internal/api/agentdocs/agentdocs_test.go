package agentdocs

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	dom "arkstudio/go/internal/agentdocs"
	"arkstudio/go/internal/api"
	"arkstudio/go/internal/api/apitest"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pyurl"
)

// The repository's web/ folder, so Locate serves the docs beside it.
var webDir = filepath.Join("..", "..", "..", "..", "web")

func TestGet(t *testing.T) {
	doc := dom.StartHere[0].Path
	cases := []struct {
		name    string
		path    string
		q       pyurl.Values
		handled bool
		status  int    // expected error status, 0 for a reply
		code    string // expected error code
		md      bool   // a Markdown reply
		json    bool   // an OK JSON reply
	}{
		{"not docs", "/api/agent/v1/units", nil, false, 0, "", false, false},
		{"index", "/api/agent/v1/docs", nil, true, 0, "", false, true},
		{"doc markdown", "/api/agent/v1/docs/" + doc, nil, true, 0, "", true, false},
		{"doc md alias", "/api/agent/v1/docs/" + doc, pyurl.Values{"format": {"md"}}, true, 0, "", true, false},
		{"doc json", "/api/agent/v1/docs/" + doc, pyurl.Values{"format": {"json"}}, true, 0, "", false, true},
		{"bad format", "/api/agent/v1/docs/" + doc, pyurl.Values{"format": {"html"}}, true, 400, "BAD_FORMAT", false, false},
		{"unknown doc", "/api/agent/v1/docs/nope.md", nil, true, 404, "DOC_NOT_FOUND", false, false},
		{"unknown section", "/api/agent/v1/docs/" + doc, pyurl.Values{"section": {"no-such-heading"}}, true, 404, "SECTION_NOT_FOUND", false, false},
	}
	for _, tc := range cases {
		c := &apitest.Ctx{Q: tc.q}
		handled, err := Get(c, tc.path, webDir)
		if handled != tc.handled {
			t.Errorf("%s: handled=%v", tc.name, handled)
			continue
		}
		if tc.status != 0 {
			var ae *api.Error
			if !errors.As(err, &ae) || ae.Status != tc.status || ae.Code != tc.code {
				t.Errorf("%s: error %v, want %d %s", tc.name, err, tc.status, tc.code)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if tc.json && c.Sent == nil {
			t.Errorf("%s: no JSON reply", tc.name)
		}
		if tc.md {
			if len(c.Replies) != 1 || c.Replies[0].ContentType != MarkdownType || c.Replies[0].Status != 200 ||
				len(c.Replies[0].Headers) != 1 || c.Replies[0].Headers[0] != [2]string{"X-ARK-Doc-Path", doc} {
				t.Errorf("%s: replies %+v", tc.name, c.Replies)
			}
		}
	}
}

func TestInterfaceWithDocs(t *testing.T) {
	out := string(InterfaceWithDocs(`{"capabilities": ["a"], "x": 1}`))
	obj, ok := pyjson.LoadsObject(out)
	if !ok {
		t.Fatalf("not an object: %s", out)
	}
	caps, _ := obj.Value("capabilities").([]any)
	if len(caps) < 2 || caps[0] != "a" || obj.Value("docs") == nil || !strings.HasPrefix(out, `{"capabilities": ["a"`) {
		t.Errorf("descriptor: %s", out)
	}
	if got := string(InterfaceWithDocs("[1]")); got != "[1]" {
		t.Errorf("non-object changed: %s", got)
	}
}
