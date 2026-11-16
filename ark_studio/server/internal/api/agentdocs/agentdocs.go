// Package agentdocs is the HTTP side of the agent documentation API
// (Go only, added after the switch to ark-server): GET/HEAD
// /api/agent/v1/docs[/{path}] over internal/agentdocs, plus the docs
// additions to the agent interface descriptor.  internal/server registers it
// after the agent token check; the docs are not project data, so no project
// scope applies.
package agentdocs

import (
	"arkstudio/go/internal/agentdocs"
	"arkstudio/go/internal/api"
	"arkstudio/go/internal/compat/pyjson"
)

// MarkdownType is the Content-Type of a Markdown doc reply.
const MarkdownType = "text/markdown; charset=utf-8"

// Get answers GET/HEAD /api/agent/v1/docs[/{path}]; handled is false when
// path is not under the docs prefix.  webDir is the served web/ folder ("" for
// the embedded build), which locates the docs on disk when present.
func Get(c api.Ctx, path, webDir string) (handled bool, err error) {
	rest, ok := agentdocs.SplitPath(path)
	if !ok {
		return false, nil
	}
	set := agentdocs.Locate(webDir)
	if rest == "" {
		c.OK(set.IndexObject())
		return true, nil
	}
	doc, ok := set.Get(rest)
	if !ok {
		return true, api.Err(404, "Unknown doc: "+rest+" (GET "+agentdocs.Base+" lists the docs)", "DOC_NOT_FOUND")
	}
	text := doc.Text
	q := c.Query()
	slug := q.First("section", "")
	if slug != "" {
		if text, ok = agentdocs.Section(doc.Text, slug); !ok {
			return true, api.Err(404, "Unknown section "+slug+" in "+doc.Path+" (format=json lists headings[].slug)", "SECTION_NOT_FOUND")
		}
	}
	switch q.First("format", "") {
	case "", "markdown", "md":
		c.Send(200, []byte(text), MarkdownType, [][2]string{{"X-ARK-Doc-Path", doc.Path}})
	case "json":
		c.OK(set.DocObject(doc, text, slug))
	default:
		return true, api.Err(400, "format must be markdown or json", "BAD_FORMAT")
	}
	return true, nil
}

// InterfaceWithDocs is the Python interface descriptor (raw JSON) plus the
// Go-only docs capabilities and a "docs" entry (diffreplay strips both).
// raw is returned unchanged when it is not a JSON object.
func InterfaceWithDocs(raw string) []byte {
	obj, ok := pyjson.LoadsObject(raw)
	if !ok {
		return []byte(raw)
	}
	caps, _ := obj.Value("capabilities").([]any)
	obj.Set("capabilities", append(append([]any(nil), caps...), agentdocs.Capabilities()...))
	obj.Set("docs", agentdocs.InterfaceDocs())
	return pyjson.Dumps(obj)
}
