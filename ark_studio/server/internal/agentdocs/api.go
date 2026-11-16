package agentdocs

import (
	"strings"

	"arkstudio/go/internal/compat/pyjson"
)

// Base is the API path of the docs index; a doc is Base + "/" + path.
const Base = "/api/agent/v1/docs"

// Format is the payload format name of the index.
const Format = "ark-agent-docs"

// Endpoints are the machine-readable agent endpoints the index points to.
var Endpoints = []struct{ Name, Path, Summary string }{
	{"interface", "/api/agent/v1/interface", "Capabilities, limits, topologies and the task manifest schema (read first)."},
	{"skills", "/api/agent/v1/skills", "Skill catalog; /api/agent/v1/skills/{id} returns one skill with its prompt."},
	{"setup", "/api/agent/v1/setup", "Your view of the project setup and its readiness; run the setup skill when not ready."},
	{"context", "/api/agent/v1/context", "Project summary, counts and current work."},
	{"whoami", "/api/agent/v1/whoami", "Who the token acts for: user, scope and reachable projects."},
}

func usage() *pyjson.Object {
	return pyjson.NewObject(
		"index", "GET "+Base,
		"read", "GET "+Base+"/{path}",
		"section", "GET "+Base+"/{path}?section={heading-slug}",
		"json", "GET "+Base+"/{path}?format=json",
		"notes", "Markdown is served as text/markdown with relative links as written; they resolve against the "+
			"doc's URL. format=json returns {path, title, headings, markdown} with links to other docs "+
			"rewritten to "+Base+"/... paths. Heading slugs are GitHub anchors (see headings[].slug).",
	)
}

// IndexObject is the GET /api/agent/v1/docs payload (without "ok").
func (s *Set) IndexObject() *pyjson.Object {
	start := []any{}
	for _, item := range StartHere {
		if d, ok := s.byPath[item.Path]; ok {
			start = append(start, pyjson.NewObject("path", d.Path, "title", d.Title, "why", item.Why,
				"url", Base+"/"+d.Path))
		}
	}
	endpoints := pyjson.NewObject()
	for _, e := range Endpoints {
		endpoints.Set(e.Name, pyjson.NewObject("method", "GET", "path", e.Path, "summary", e.Summary))
	}
	docs := make([]any, 0, len(s.Docs))
	sections := []any{}
	seen := map[string]bool{}
	total := 0
	for _, d := range s.Docs {
		docs = append(docs, pyjson.NewObject("path", d.Path, "title", d.Title, "summary", d.Summary,
			"section", d.Section, "bytes", int64(d.Bytes()), "updated", d.Updated, "url", Base+"/"+d.Path))
		total += d.Bytes()
		if !seen[d.Section] {
			seen[d.Section] = true
			sections = append(sections, d.Section)
		}
	}
	return pyjson.NewObject(
		"format", Format,
		"version", int64(1),
		"base", Base,
		"source", s.Source,
		"usage", usage(),
		"start_here", start,
		"endpoints", endpoints,
		"sections", sections,
		"count", int64(len(docs)),
		"total_bytes", int64(total),
		"docs", docs,
	)
}

// DocObject is the ?format=json payload (without "ok"); markdown is the
// whole doc or one section, with links to served docs rewritten.
func (s *Set) DocObject(d *Doc, markdown, section string) *pyjson.Object {
	headings := []any{}
	for _, h := range Headings(d.Text) {
		headings = append(headings, pyjson.NewObject("level", int64(h.Level), "text", h.Text, "slug", h.Slug))
	}
	o := pyjson.NewObject("path", d.Path, "title", d.Title, "section", d.Section, "updated", d.Updated,
		"url", Base+"/"+d.Path, "headings", headings)
	if section != "" {
		o.Set("heading", section)
	}
	o.Set("markdown", RewriteLinks(markdown, d.Path, Base, s.Has))
	return o
}

// InterfaceDocs is the "docs" entry added to /api/agent/v1/interface.
func InterfaceDocs() *pyjson.Object {
	start := make([]any, len(StartHere))
	for i, item := range StartHere {
		start[i] = item.Path
	}
	return pyjson.NewObject(
		"index", Base,
		"read", Base+"/{path}",
		"options", []any{"section={heading-slug}", "format=json"},
		"start_here", start,
		"summary", "ARK's user, operations and agent documentation as Markdown. Read the index, then only the docs (or sections) you need.",
	)
}

// Capabilities are the interface descriptor entries for the docs routes.
func Capabilities() []any {
	return []any{
		pyjson.NewObject("id", "docs.index", "kind", "read", "method", "GET", "path", Base,
			"summary", "Index of ARK's documentation: path, title, summary, section, bytes, updated, plus a start-here order."),
		pyjson.NewObject("id", "docs.read", "kind", "read", "method", "GET", "path", Base+"/{path}",
			"summary", "One doc as text/markdown; ?section={heading-slug} returns one heading's section, ?format=json returns {path, title, headings, markdown}."),
	}
}

// SplitPath returns the doc part of a request path under Base: ok is false
// for paths outside Base; rest is "" for the index.
func SplitPath(urlPath string) (rest string, ok bool) {
	if urlPath == Base {
		return "", true
	}
	if !strings.HasPrefix(urlPath, Base+"/") {
		return "", false
	}
	return urlPath[len(Base)+1:], true
}
