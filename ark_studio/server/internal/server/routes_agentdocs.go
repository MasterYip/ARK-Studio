package server

import (
	"sync"

	apidocs "arkstudio/go/internal/api/agentdocs"
)

// The agent docs API lives in internal/api/agentdocs.

// GoOnlyRoutes are routes the frozen Python server does not have (added
// after the 2026-11-15 switch to ark-server).  They are not contract ids:
// `ark-server routes` lists them with a "goonly:" prefix, the contract
// harness skips cases on them against Python, and diffreplay strips their
// additions to /api/agent/v1/interface before comparing with Python
// (tests/python/contract/go_only.py).
var GoOnlyRoutes = []PortedRoute{
	{"goonly:exact:/api/agent/v1/docs", "GET,HEAD"},
	{"goonly:prefix:/api/agent/v1/docs/", "GET,HEAD"},
}

const markdownType = apidocs.MarkdownType

// agentDocsGet answers GET/HEAD /api/agent/v1/docs[/{path}] (agent auth was
// checked by the caller).
func (c *reqCtx) agentDocsGet(path string) (bool, error) {
	webDir := ""
	if c.app.Assets != nil {
		webDir = c.app.Assets.Dir
	}
	return apidocs.Get(c, path, webDir)
}

var (
	agentInterfaceOnce sync.Once
	agentInterfaceBody []byte
)

func agentInterfaceEncoded() []byte {
	agentInterfaceOnce.Do(func() { agentInterfaceBody = apidocs.InterfaceWithDocs(staticJSON["skills_interface"]) })
	return agentInterfaceBody
}
