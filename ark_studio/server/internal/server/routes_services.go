package server

// Services modules (branch feat/go-backend-services): assistant, chat, GPU,
// InfoHub import, local open.  Each module registers its routes in
// servicesPorted and answers from servicesGet / servicesPost, which
// routes.go calls at the place Python's dispatch reaches these paths.

import "arkstudio/go/internal/compat/pyjson"

var servicesPorted = []PortedRoute{
	{"exact:/api/assistant/dispatch", "GET,HEAD"},
	{"exact:/api/assistant/apply", "POST"},
	{"exact:/api/assistant/extract", "POST"},
	// InfoHub import (importer.py)
	{"exact:/api/infohub/candidates", "GET,HEAD"},
	{"exact:/api/infohub/browse", "GET,HEAD"},
	{"exact:/api/infohub/unit", "GET,HEAD"},
	{"exact:/api/infohub/graph", "GET,HEAD"},
	{"exact:/api/infohub/import", "POST"},
	{"exact:/api/import/infohub", "POST"},
	{"exact:/api/import/infohub/sources", "GET,HEAD"},
	{"exact:/api/import/infohub/preview", "GET,HEAD"},
	// Avatar writes: _handle_profile answers 405 for every write to
	// /api/users/<name>/avatar (profileWrite).
	{"regex:^/api/users/([^/]+)/avatar$", "DELETE,PATCH,POST"},
}

// servicesGet answers the services GET routes (Handler._api_get).
func (c *reqCtx) servicesGet(path string) (bool, error) {
	if handled, err := c.gpuHandlers().Get(c, path); handled {
		return true, err
	}
	return c.chatHandlers().Get(c, path)
}

func (c *reqCtx) servicesPost(path string, body *pyjson.Object, actor string) (bool, error) {
	if handled, err := c.gpuHandlers().Post(c, path, body); handled {
		return true, err
	}
	if path == "/api/open" {
		return true, c.openExternal(body)
	}
	return c.chatHandlers().Post(c, path, body, actor)
}
