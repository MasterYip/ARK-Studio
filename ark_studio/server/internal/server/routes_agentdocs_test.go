package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

// docsApp is a local-mode app serving the repository's web/ and docs/ from disk.
func docsApp(t *testing.T) *App {
	t.Helper()
	root := t.TempDir()
	app, err := NewApp(Config{Root: filepath.Join(root, "projects"), NoDemo: true,
		WebRoot: filepath.Join("..", "..", "..", "web"), SessionsRoot: filepath.Join(root, "sessions")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	app.Bind("127.0.0.1", 8765)
	return app
}

func docsGet(app *App, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	r.Host = "127.0.0.1:8765"
	r.RemoteAddr = "127.0.0.1:50000"
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	return w
}

func TestAgentDocsRoutes(t *testing.T) {
	app := docsApp(t)
	auth := map[string]string{"Authorization": "Bearer " + app.AgentToken()}

	// Index.
	w := docsGet(app, "GET", "/api/agent/v1/docs", auth)
	if w.Code != 200 || w.Header().Get("Content-Type") != jsonType {
		t.Fatalf("index: %d %s %s", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
	var index struct {
		OK        bool   `json:"ok"`
		Format    string `json:"format"`
		Source    string `json:"source"`
		StartHere []struct {
			Path string `json:"path"`
		} `json:"start_here"`
		Endpoints map[string]struct{ Path string } `json:"endpoints"`
		Docs      []struct {
			Path, Title, Summary, Section, Updated string
			Bytes                                  int
		} `json:"docs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &index); err != nil {
		t.Fatal(err)
	}
	if !index.OK || index.Format != "ark-agent-docs" || index.Source != "disk" || len(index.Docs) < 20 {
		t.Fatalf("index header: %+v", index)
	}
	if index.StartHere[0].Path != "agents/agent-interface.md" || index.Endpoints["interface"].Path != "/api/agent/v1/interface" ||
		index.Endpoints["setup"].Path != "/api/agent/v1/setup" || index.Endpoints["skills"].Path != "/api/agent/v1/skills" ||
		index.Endpoints["context"].Path != "/api/agent/v1/context" {
		t.Fatalf("start_here/endpoints: %+v %+v", index.StartHere, index.Endpoints)
	}
	seen := map[string]bool{}
	for _, d := range index.Docs {
		seen[d.Path] = true
		if d.Title == "" || d.Summary == "" || d.Bytes == 0 || d.Updated == "" || !strings.HasPrefix(d.Path, d.Section+"/") {
			t.Errorf("doc entry %+v", d)
		}
	}
	for _, p := range []string{"agents/external-agent.md", "guide/topics.md", "operations/gpu.md", "development/design.md"} {
		if !seen[p] {
			t.Errorf("index lacks %s", p)
		}
	}
	for _, p := range []string{"development/release.md", "agents/skills-attribution.md", "operations/public-export.md"} {
		if seen[p] {
			t.Errorf("index serves excluded %s", p)
		}
	}

	// Markdown, HEAD, section, JSON.
	w = docsGet(app, "GET", "/api/agent/v1/docs/guide/topics.md", auth)
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/markdown; charset=utf-8" ||
		!strings.HasPrefix(w.Body.String(), "# Topics") || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("markdown: %d %v %.80s", w.Code, w.Header(), w.Body)
	}
	full := w.Body.String()
	if w := docsGet(app, "GET", "/api/agent/v1/docs/docs/guide/topics", auth); w.Code != 200 || w.Body.String() != full {
		t.Fatalf("normalised path: %d", w.Code)
	}
	if w := docsGet(app, "HEAD", "/api/agent/v1/docs/guide/topics.md", auth); w.Code != 200 || w.Body.Len() != 0 {
		t.Fatalf("HEAD: %d %d", w.Code, w.Body.Len())
	}
	w = docsGet(app, "GET", "/api/agent/v1/docs/agents/agent-interface.md?format=json", auth)
	var doc struct {
		Path, Title, Markdown string
		Headings              []struct {
			Level      int
			Text, Slug string
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil || w.Code != 200 {
		t.Fatalf("json: %d %v", w.Code, err)
	}
	if doc.Path != "agents/agent-interface.md" || doc.Title != "ARK Agent Interface v2" || len(doc.Headings) < 5 ||
		!strings.HasPrefix(doc.Markdown, "# ARK Agent Interface v2") {
		t.Fatalf("json doc: %+v", doc.Headings)
	}
	slug := doc.Headings[1].Slug
	w = docsGet(app, "GET", "/api/agent/v1/docs/agents/agent-interface.md?section="+slug, auth)
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), "#") || !strings.Contains(w.Body.String(), doc.Headings[1].Text) ||
		len(w.Body.String()) >= len(doc.Markdown) {
		t.Fatalf("section %s: %d %.200s", slug, w.Code, w.Body)
	}
	w = docsGet(app, "GET", "/api/agent/v1/docs/agents/external-agent.md?format=json", auth)
	if !strings.Contains(w.Body.String(), "](/api/agent/v1/docs/guide/topics.md?section=agent-workflow)") {
		t.Errorf("json mode did not rewrite relative doc links: %.300s", w.Body)
	}

	// Errors: unknown docs, traversal, excluded files, bad section/format.
	for target, status := range map[string]int{
		"/api/agent/v1/docs/guide/nope.md":                               404,
		"/api/agent/v1/docs/../../go/go.mod":                             404,
		"/api/agent/v1/docs/guide/..%2f..%2fREADME.md":                   404,
		"/api/agent/v1/docs/%2e%2e/README.md":                            404,
		"/api/agent/v1/docs/guide%5ctopics.md":                           404,
		"/api/agent/v1/docs/development/release.md":                      404,
		"/api/agent/v1/docs/development/archive/implementation-notes.md": 404,
		"/api/agent/v1/docs/agents/skills-attribution.md":                404,
		"/api/agent/v1/docs/screenshots/readme-home.png":                 404,
		"/api/agent/v1/docs/guide/topics.md?section=zz-nope":             404,
		"/api/agent/v1/docs/guide/topics.md?format=html":                 400,
	} {
		w := docsGet(app, "GET", target, auth)
		if w.Code != status || !strings.Contains(w.Body.String(), `"ok": false`) {
			t.Errorf("%s: %d %s, want %d", target, w.Code, w.Body, status)
		}
	}

	// Auth: same gate as every /api/agent/v1 route.
	for name, h := range map[string]map[string]string{
		"none":   nil,
		"wrong":  {"Authorization": "Bearer wrong"},
		"mobile": {"X-ARK-Mobile-Token": "not-an-agent-token"},
	} {
		for _, target := range []string{"/api/agent/v1/docs", "/api/agent/v1/docs/guide/topics.md"} {
			if w := docsGet(app, "GET", target, h); w.Code != 401 {
				t.Errorf("%s %s: %d", name, target, w.Code)
			}
		}
	}
	if w := docsGet(app, "GET", "/api/agent/v1/docs", map[string]string{"X-ARK-Token": app.AgentToken()}); w.Code != 200 {
		t.Errorf("X-ARK-Token: %d", w.Code)
	}
	tok, err := app.Org.CreateAgentToken(context.Background(), "local", pyjson.NewObject("scope", "read"))
	if err != nil {
		t.Fatal(err)
	}
	personal := map[string]string{"Authorization": "Bearer " + pyjson.Str(tok.Value("token"))}
	if w := docsGet(app, "GET", "/api/agent/v1/docs/guide/topics.md", personal); w.Code != 200 {
		t.Errorf("personal read token: %d %s", w.Code, w.Body)
	}
}

func TestAgentInterfaceListsDocs(t *testing.T) {
	app := docsApp(t)
	w := docsGet(app, "GET", "/api/agent/v1/interface", map[string]string{"Authorization": "Bearer " + app.AgentToken()})
	var desc struct {
		Capabilities []struct{ ID, Path string }
		Docs         struct {
			Index, Read string
			StartHere   []string `json:"start_here"`
		}
		Documentation []string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &desc); err != nil || w.Code != 200 {
		t.Fatalf("%d %v", w.Code, err)
	}
	ids := map[string]string{}
	for _, c := range desc.Capabilities {
		ids[c.ID] = c.Path
	}
	if ids["docs.index"] != "/api/agent/v1/docs" || ids["docs.read"] != "/api/agent/v1/docs/{path}" || ids["setup.read"] == "" {
		t.Fatalf("capabilities %v", ids)
	}
	if desc.Docs.Index != "/api/agent/v1/docs" || len(desc.Docs.StartHere) == 0 || len(desc.Documentation) == 0 {
		t.Fatalf("docs entry %+v", desc.Docs)
	}
	// Everything else is Python's descriptor unchanged (diffreplay relies on it).
	var mine, python map[string]any
	json.Unmarshal(agentInterfaceEncoded(), &mine)                  //nolint:errcheck
	json.Unmarshal([]byte(staticJSON["skills_interface"]), &python) //nolint:errcheck
	delete(mine, "docs")
	// The Go-only capabilities (go_only.py GO_ONLY_CAPABILITIES) come last.
	caps := mine["capabilities"].([]any)
	kept := []any{}
	for _, c := range caps {
		id, _ := c.(map[string]any)["id"].(string)
		if !strings.HasPrefix(id, "docs.") && !strings.HasPrefix(id, "watch.") && !strings.HasPrefix(id, "messages.") &&
			!goOnlyLinkCaps[id] {
			kept = append(kept, c)
		}
	}
	if len(caps)-len(kept) != 9 || ids["watch.updates"] != "/api/agent/v1/updates" {
		t.Fatalf("Go-only capabilities: %d, %v", len(caps)-len(kept), ids)
	}
	mine["capabilities"] = kept
	a, _ := json.Marshal(mine)
	b, _ := json.Marshal(python)
	if string(a) != string(b) {
		t.Fatal("interface descriptor changed beyond the docs additions")
	}
	if !strings.HasPrefix(string(agentInterfaceEncoded()), staticJSON["skills_interface"][:40]) {
		t.Fatal("key order changed")
	}
	if docsGet(app, "GET", "/api/skills/interface", nil).Body.String() != `{"ok": true, `+staticJSON["skills_interface"][1:] {
		t.Fatal("/api/skills/interface must stay Python's payload")
	}
}

// goOnlyLinkCaps are the Go-only link capabilities (routes_agentlinks.go);
// Python's links.write stays.
var goOnlyLinkCaps = map[string]bool{"links.list": true, "links.delete": true, "links.update": true}
