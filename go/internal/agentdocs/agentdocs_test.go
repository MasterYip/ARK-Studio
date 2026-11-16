package agentdocs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"unicode/utf8"

	"arkstudio/go/internal/pyjson"
)

func TestIncluded(t *testing.T) {
	for rel, want := range map[string]bool{
		"agents/agent-interface.md":             true,
		"guide/topics.md":                       true,
		"operations/gpu.md":                     true,
		"development/design.md":                 true,
		"development/development.md":            true,
		"development/release.md":                false,
		"development/performance.md":            false,
		"development/archive/implementation.md": false,
		"development/go-port/README.md":         false,
		"agents/skills-attribution.md":          false,
		"operations/public-export.md":           false,
		"README.md":                             false,
		"screenshots/readme-home.png":           false,
		"guide/sub/deeper.md":                   false,
		"guide/notes.txt":                       false,
	} {
		if got := Included(rel); got != want {
			t.Errorf("Included(%q) = %v, want %v", rel, got, want)
		}
	}
}

func TestNormalizeRejectsTraversal(t *testing.T) {
	ok := map[string]string{
		"guide/topics.md": "guide/topics.md", "guide/topics": "guide/topics.md",
		"/guide/topics.md": "guide/topics.md", "docs/agents/skills.md": "agents/skills.md",
	}
	for in, want := range ok {
		if got, good := Normalize(in); !good || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, good, want)
		}
	}
	for _, in := range []string{
		"", "../README.md", "guide/../../go/go.mod", "guide/./topics.md", "guide//topics.md",
		`guide\topics.md`, "..", "guide/..", "C:/Windows/win.ini", "guide/topics.md\x00", "guide/\ntopics.md",
		"//etc/passwd", "guide/topics.md/",
	} {
		if got, good := Normalize(in); good {
			t.Errorf("Normalize(%q) accepted as %q", in, got)
		}
	}
}

const sample = `---
title: ignored
description: "Front matter wins."
---
# Sample doc

**Status:** draft

First paragraph line one
continues here with ` + "`code`" + ` and a [link](other.md).

## Setup & install

Text with a [sibling](../guide/topics.md#create-a-topic) link,
an [external](https://example.com/x.md) one and [missing](nope.md).

` + "```sh" + `
# not a heading
see [code](../guide/topics.md)
` + "```" + `

### Details

Nested.

## Setup & install

Second section with the same title.

# Appendix
`

func TestHeadingsAndSlugs(t *testing.T) {
	hs := Headings(sample)
	var got []string
	for _, h := range hs {
		got = append(got, strings.Repeat("#", h.Level)+h.Slug)
	}
	want := "#sample-doc ##setup--install ###details ##setup--install-1 #appendix"
	if strings.Join(got, " ") != want {
		t.Fatalf("headings %q, want %q", strings.Join(got, " "), want)
	}
	if s := Slug("Use `GET /api/agent/v1/docs` (JSON)"); s != "use-get-apiagentv1docs-json" {
		t.Errorf("slug %q", s)
	}
	if s := Slug("节点 历史"); s != "节点-历史" {
		t.Errorf("unicode slug %q", s)
	}
}

func TestSection(t *testing.T) {
	sec, ok := Section(sample, "setup--install")
	if !ok {
		t.Fatal("section not found")
	}
	if !strings.HasPrefix(sec, "## Setup & install\n") || !strings.Contains(sec, "# not a heading") ||
		!strings.Contains(sec, "### Details") || strings.Contains(sec, "Second section") {
		t.Fatalf("section:\n%s", sec)
	}
	sec, _ = Section(sample, "setup--install-1")
	if !strings.Contains(sec, "Second section") || strings.Contains(sec, "Appendix") {
		t.Fatalf("duplicate section:\n%s", sec)
	}
	sec, _ = Section(sample, "details")
	if strings.TrimSpace(sec) != "### Details\n\nNested." {
		t.Fatalf("details: %q", sec)
	}
	if _, ok := Section(sample, "not-a-heading"); ok {
		t.Fatal("heading inside a code fence must not be a section")
	}
}

func TestSummary(t *testing.T) {
	if s := Summary(sample); s != "Front matter wins." {
		t.Errorf("front matter summary %q", s)
	}
	body := sample[strings.Index(sample, "# Sample"):]
	if s := Summary(body); s != "First paragraph line one continues here with code and a link." {
		t.Errorf("paragraph summary %q", s)
	}
	long := "# T\n\n" + strings.Repeat("word ", 200)
	s := Summary(long)
	if n := utf8.RuneCountInString(s); n > SummaryMax || !strings.HasSuffix(s, "...") {
		t.Errorf("long summary: %d chars %q", n, s)
	}
	if s := Summary("# Only\n\n```\ncode\n```\n"); s != "" {
		t.Errorf("no prose: %q", s)
	}
}

func TestRewriteLinks(t *testing.T) {
	served := func(p string) bool { return p == "guide/topics.md" || p == "agents/other.md" }
	out := RewriteLinks(sample, "agents/sample.md", Base, served)
	for _, want := range []string{
		"[link](/api/agent/v1/docs/agents/other.md)",
		"[sibling](/api/agent/v1/docs/guide/topics.md?section=create-a-topic)",
		"[external](https://example.com/x.md)",
		"[missing](nope.md)",
		"see [code](../guide/topics.md)", // inside a fence: untouched
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}

func TestLoadAndIndex(t *testing.T) {
	fsys := fstest.MapFS{
		"guide/topics.md":              {Data: []byte("# Topics\r\n\r\nTopic nodes.\r\n")},
		"agents/agent-interface.md":    {Data: []byte("\uFEFF# ARK Agent Interface v2\n\nThe contract.\n")},
		"agents/skills-attribution.md": {Data: []byte("# Private\n")},
		"development/archive/x.md":     {Data: []byte("# Old\n")},
		"development/design.md":        {Data: []byte("# Design\n\nArchitecture.\n")},
		"screenshots/a.png":            {Data: []byte("png")},
	}
	s, err := Load(fsys, "test", map[string]string{"guide/topics.md": "2026-11-15T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, d := range s.Docs {
		paths = append(paths, d.Path)
	}
	if got := strings.Join(paths, " "); got != "agents/agent-interface.md guide/topics.md development/design.md" {
		t.Fatalf("docs %q", got)
	}
	d, ok := s.Get("guide/topics")
	if !ok || d.Text != "# Topics\n\nTopic nodes.\n" || d.Updated != "2026-11-15T00:00:00Z" || d.Section != "guide" {
		t.Fatalf("topics doc %+v", d)
	}
	if a, _ := s.Get("agents/agent-interface.md"); a.Title != "ARK Agent Interface v2" || a.Summary != "The contract." {
		t.Fatalf("BOM not stripped: %+v", a)
	}
	if _, ok := s.Get("agents/skills-attribution.md"); ok {
		t.Fatal("excluded doc served")
	}
	idx := string(pyjson.Dumps(s.IndexObject()))
	for _, want := range []string{
		`"format": "ark-agent-docs"`, `"count": 3`,
		`{"path": "guide/topics.md", "title": "Topics", "summary": "Topic nodes.", "section": "guide", "bytes": 23, "updated": "2026-11-15T00:00:00Z", "url": "/api/agent/v1/docs/guide/topics.md"}`,
		`"start_here": [{"path": "agents/agent-interface.md"`,
		`"interface": {"method": "GET", "path": "/api/agent/v1/interface"`,
		`"sections": ["agents", "guide", "development"]`,
	} {
		if !strings.Contains(idx, want) {
			t.Errorf("index lacks %s\n%s", want, idx)
		}
	}
	doc := string(pyjson.Dumps(s.DocObject(d, d.Text, "")))
	if !strings.HasPrefix(doc, `{"path": "guide/topics.md", "title": "Topics", "section": "guide"`) ||
		!strings.Contains(doc, `"headings": [{"level": 1, "text": "Topics", "slug": "topics"}]`) ||
		!strings.Contains(doc, `"markdown": "# Topics\n\nTopic nodes.\n"`) {
		t.Errorf("doc object %s", doc)
	}
}

// TestRepositoryDocs loads the real docs/ folder: every start-here entry and
// every doc the interface descriptor names must exist and have a summary.
func TestRepositoryDocs(t *testing.T) {
	s, err := FromDir(filepath.Join("..", "..", "..", "docs"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Docs) < 20 {
		t.Fatalf("only %d docs", len(s.Docs))
	}
	for _, item := range StartHere {
		if !s.Has(item.Path) {
			t.Errorf("start-here doc %s is not served", item.Path)
		}
	}
	for _, d := range s.Docs {
		if d.Title == "" || d.Summary == "" || utf8.RuneCountInString(d.Summary) > SummaryMax || d.Updated == "" {
			t.Errorf("%s: title %q summary %q updated %q", d.Path, d.Title, d.Summary, d.Updated)
		}
		if strings.Contains(d.Text, "\r\n") {
			t.Errorf("%s keeps CRLF", d.Path)
		}
	}
}

func TestSplitPath(t *testing.T) {
	for in, want := range map[string]string{Base: "", Base + "/": "", Base + "/guide/topics.md": "guide/topics.md"} {
		if got, ok := SplitPath(in); !ok || got != want {
			t.Errorf("SplitPath(%q) = %q, %v", in, got, ok)
		}
	}
	for _, in := range []string{"/api/agent/v1/docsx", "/api/agent/v1/doc", "/api/agent/v1/interface"} {
		if _, ok := SplitPath(in); ok {
			t.Errorf("SplitPath(%q) matched", in)
		}
	}
}

// TestEmbeddedDocs checks the go:embed copy against docs/.  A plain checkout
// embeds only dist/.keep, so the test skips unless the build generated the
// docs; release builds set ARK_REQUIRE_EMBEDDED_DOCS=1 so a binary without
// (or with stale) docs cannot ship.
func TestEmbeddedDocs(t *testing.T) {
	_, ok := Embedded()
	if !ok && os.Getenv("ARK_REQUIRE_EMBEDDED_DOCS") != "1" {
		t.Skip("no embedded docs (run go generate ./internal/agentdocs)")
	}
	summary, err := CheckEmbedded(filepath.Join("..", "..", "..", "docs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Log(summary)
	if s, _ := Embedded(); !s.Has("agents/agent-interface.md") || s.Source != "embedded" {
		t.Fatal("embedded set lacks agents/agent-interface.md")
	}
}
