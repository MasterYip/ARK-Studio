package server

import (
	"sync"

	"arkstudio/go/internal/agentdocs"
	"arkstudio/go/internal/pyjson"
)

// GoOnlyRoutes are routes the frozen Python server does not have (added
// after the 2026-11-15 switch to ark-server).  They are not contract ids:
// `ark-server routes` lists them with a "goonly:" prefix, the contract
// harness skips cases on them against Python, and diffreplay strips their
// additions to /api/agent/v1/interface before comparing with Python
// (tests/python/contract: GO_ONLY in harness.py and diffreplay.py).
var GoOnlyRoutes = []PortedRoute{
	{"goonly:exact:/api/agent/v1/docs", "GET,HEAD"},
	{"goonly:prefix:/api/agent/v1/docs/", "GET,HEAD"},
}

const markdownType = "text/markdown; charset=utf-8"

// agentDocsGet answers GET/HEAD /api/agent/v1/docs[/{path}] (agent auth was
// checked by the caller; the docs are not project data, so no project scope).
func (c *reqCtx) agentDocsGet(path string) (bool, error) {
	rest, ok := agentdocs.SplitPath(path)
	if !ok {
		return false, nil
	}
	webDir := ""
	if c.app.Assets != nil {
		webDir = c.app.Assets.Dir
	}
	set := agentdocs.Locate(webDir)
	if rest == "" {
		c.ok(set.IndexObject())
		return true, nil
	}
	doc, ok := set.Get(rest)
	if !ok {
		return true, apiErr(404, "Unknown doc: "+rest+" (GET "+agentdocs.Base+" lists the docs)", "DOC_NOT_FOUND")
	}
	text := doc.Text
	slug := c.query.First("section", "")
	if slug != "" {
		if text, ok = agentdocs.Section(doc.Text, slug); !ok {
			return true, apiErr(404, "Unknown section "+slug+" in "+doc.Path+" (format=json lists headings[].slug)", "SECTION_NOT_FOUND")
		}
	}
	switch c.query.First("format", "") {
	case "", "markdown", "md":
		c.send(200, []byte(text), markdownType, headerList{{"X-ARK-Doc-Path", doc.Path}}, true)
	case "json":
		c.ok(set.DocObject(doc, text, slug))
	default:
		return true, apiErr(400, "format must be markdown or json", "BAD_FORMAT")
	}
	return true, nil
}

var (
	agentInterfaceOnce sync.Once
	agentInterfaceBody []byte
)

// agentInterfaceEncoded is Python's interface descriptor plus the Go-only
// docs capabilities and a "docs" entry (diffreplay strips both).
func agentInterfaceEncoded() []byte {
	agentInterfaceOnce.Do(func() {
		raw := staticJSON["skills_interface"]
		obj, ok := pyjson.LoadsObject(raw)
		if !ok {
			agentInterfaceBody = []byte(raw)
			return
		}
		caps, _ := obj.Value("capabilities").([]any)
		obj.Set("capabilities", append(append([]any(nil), caps...), agentdocs.Capabilities()...))
		obj.Set("docs", agentdocs.InterfaceDocs())
		agentInterfaceBody = pyjson.Dumps(obj)
	})
	return agentInterfaceBody
}
