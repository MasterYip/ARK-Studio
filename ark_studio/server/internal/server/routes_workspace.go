package server

import (
	"arkstudio/go/internal/api"
	wsapi "arkstudio/go/internal/api/workspace"
	"arkstudio/go/internal/compat/pyjson"
)

// workspacePorted are the contract routes this file answers (router.go registers them).
var workspacePorted = []PortedRoute{
	{"exact:/api/workspace", "GET,HEAD,POST"},
	{"exact:/api/workspace/changes", "GET,HEAD"},
	{"exact:/api/workspace/create", "POST"},
	{"exact:/api/workspace/delete", "POST"},
	{"exact:/api/workspace/diff", "GET,HEAD"},
	{"exact:/api/workspace/file", "GET,HEAD,POST"},
	{"exact:/api/workspace/grep", "GET,HEAD"},
	{"exact:/api/workspace/info", "GET,HEAD"},
	{"exact:/api/workspace/raw", "GET,HEAD"},
	{"exact:/api/workspace/rename", "POST"},
	{"exact:/api/workspace/search", "GET,HEAD"},
	{"exact:/api/workspace/terminals", "GET,HEAD,POST"},
	{"exact:/api/workspace/tree", "GET,HEAD"},
	{"prefix:/api/workspace/", "GET,HEAD,POST"},
	{"regex:^/api/workspace/terminals/([^/]+)$", "GET,HEAD"},
	{"regex:^/api/workspace/terminals/([^/]+)/cancel$", "POST"},
	{"regex:^/api/workspace/terminals/([^/]+)/resize$", "POST"},
	{"regex:^/api/workspace/terminals/([^/]+)/stdin$", "POST"},
	{"regex:^/api/workspace/terminals/([^/]+)/stream$", "GET,HEAD"},
}

// The workspace handlers live in internal/api/workspace.

func isWorkspacePath(path string) bool { return wsapi.IsPath(path) }

// rejectTaskAgentReadToken is Handler._reject_task_agent_read_token.
func (c *reqCtx) rejectTaskAgentReadToken(message string) error {
	if token := c.readToken(); token != "" && c.app.Sessions != nil && c.app.Sessions.ForReadToken(token) != nil {
		return apiErr(403, message, "")
	}
	return nil
}

func (c *reqCtx) workspaceHandlers() *wsapi.Handlers {
	return &wsapi.Handlers{Terminals: c.app.Terminals, Done: c.app.Streams.Done, GuessType: guessType}
}

func (c *reqCtx) workspaceGet(path string) error { return c.workspaceHandlers().Get(c, path) }

func (c *reqCtx) workspacePost(path string, body *pyjson.Object) error {
	return c.workspaceHandlers().Post(c, path, body)
}

func (c *reqCtx) terminalDelete(id string) error { return c.workspaceHandlers().Delete(c, id) }

// pyBoolArg is server._bool (api.BoolArg).
func pyBoolArg(v any) bool { return api.BoolArg(v) }
