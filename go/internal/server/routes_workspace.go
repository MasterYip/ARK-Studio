package server

import (
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/workspace"
)

// Handler._workspace_get / _workspace_post / _stream_terminal and the
// DELETE /api/workspace/terminals/{id} branch: the Editor's files, changes
// and terminal views.

var (
	reTermStream = pyRe(`^/api/workspace/terminals/([^/]+)/stream$`, false)
	reTermResize = pyRe(`^/api/workspace/terminals/([^/]+)/resize$`, false)
	reTermStdin  = pyRe(`^/api/workspace/terminals/([^/]+)/stdin$`, false)
	reTermCancel = pyRe(`^/api/workspace/terminals/([^/]+)/cancel$`, false)
)

func isWorkspacePath(path string) bool {
	return path == "/api/workspace" || strings.HasPrefix(path, "/api/workspace/")
}

// rejectTaskAgentReadToken is Handler._reject_task_agent_read_token.
func (c *reqCtx) rejectTaskAgentReadToken(message string) error {
	if token := c.readToken(); token != "" && c.app.Sessions != nil && c.app.Sessions.ForReadToken(token) != nil {
		return apiErr(403, message, "")
	}
	return nil
}

func (c *reqCtx) rejectWorkspaceReadToken() error {
	return c.rejectTaskAgentReadToken("task-agent read tokens cannot use the files workspace")
}

// workspaceGet is Handler._workspace_get.
func (c *reqCtx) workspaceGet(path string) error {
	p, err := c.require("read")
	if err != nil {
		return err
	}
	ws := c.app.Terminals
	root := p.Path
	reply := func(out *pyjson.Object, err error) error {
		if err != nil {
			return err
		}
		c.ok(out)
		return nil
	}
	switch path {
	case "/api/workspace/info":
		data := workspace.Info(root)
		data.Set("project", p.Name())
		c.ok(data)
		return nil
	case "/api/workspace/tree":
		return reply(workspace.Tree(root, c.query.First("path", ""), c.qInt("depth", 1, 1, 4)))
	case "/api/workspace/search":
		c.ok(workspace.Search(root, c.query.First("q", ""), c.qInt("limit", 100, 1, 200)))
		return nil
	case "/api/workspace/grep":
		return c.workspaceGrep(root)
	case "/api/workspace/file":
		target := c.query.First("path", "")
		if target == "" {
			return apiErr(400, "path is required", "")
		}
		return reply(workspace.ReadFile(root, target))
	case "/api/workspace/raw":
		target, err := workspace.ResolveFile(root, c.query.First("path", ""))
		if err != nil {
			return err
		}
		download := c.query.First("download", "")
		return c.sendFile(target, guessType(filepath.Base(target)), download == "1" || download == "true")
	case "/api/workspace/changes":
		return reply(workspace.Changes(root, c.query.First("base", "HEAD")))
	case "/api/workspace/diff":
		target := c.query.First("path", "")
		if target == "" {
			return apiErr(400, "path is required", "")
		}
		return reply(workspace.DiffFile(root, target, c.query.First("base", "HEAD")))
	case "/api/workspace/terminals":
		c.ok(ws.List(p.ID()))
		return nil
	}
	if m := reTermStream.FindStringSubmatch(path); m != nil {
		return c.streamTerminal(m[1], c.qInt("offset", 0, 0, 10_000_000), p.ID())
	}
	if m := reTerminal.FindStringSubmatch(path); m != nil {
		id := p.ID()
		if _, err := ws.Get(m[1], &id); err != nil {
			return err
		}
		snap, err := ws.Read(m[1], c.qInt("offset", 0, 0, 10_000_000), c.qInt("limit", 200, 1, 1000))
		if err != nil {
			return err
		}
		c.ok(pyjson.NewObject("terminal", snap))
		return nil
	}
	return apiErr(404, "Unknown workspace endpoint: "+path, "")
}

// streamTerminal is Handler._stream_terminal: SSE output/state/done events.
// The stream leaves the admission pool and the per-request deadline: it is
// bounded by its own 600 s deadline, the client and server shutdown.
func (c *reqCtx) streamTerminal(id string, offset int64, projectID string) error {
	ws := c.app.Terminals
	var pid *string
	if projectID != "" {
		pid = &projectID
	}
	if _, err := ws.Get(id, pid); err != nil {
		return err
	}
	c.detachFromRequestLimits()
	h := c.w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Connection", "close")
	c.w.WriteHeader(200)
	rc := http.NewResponseController(c.w)
	write := func(b []byte) bool {
		rc.SetWriteDeadline(time.Now().Add(30 * time.Second)) //nolint:errcheck
		if _, err := c.w.Write(b); err != nil {
			return false
		}
		return true
	}
	flush := func() bool { return rc.Flush() == nil }
	emit := func(event string, payload any) bool {
		b := append([]byte("event: "+event+"\ndata: "), pyjson.Dumps(payload)...)
		return write(append(b, "\n\n"...))
	}
	terminal := map[string]bool{"exited": true, "cancelled": true, "timeout": true, "output_limit": true, "failed": true}
	var lastStatus any
	lastBeat := time.Now()
	deadline := lastBeat.Add(600 * time.Second)
	if !flush() {
		return nil
	}
	for {
		data, err := ws.Read(id, offset, 200)
		if err != nil {
			return nil
		}
		entries, _ := data.Value("entries").([]any)
		for _, e := range entries {
			if !emit("output", e) {
				return nil
			}
		}
		offset, _ = data.Value("offset").(int64)
		state := pyjson.NewObject()
		for _, k := range data.Keys() {
			if k != "entries" {
				state.Set(k, data.Value(k))
			}
		}
		status := data.Value("status")
		if len(entries) > 0 || status != lastStatus {
			if !emit("state", state) || !flush() {
				return nil
			}
			lastStatus = status
		}
		if s, _ := status.(string); terminal[s] {
			emit("done", state)
			flush()
			return nil
		}
		if time.Since(lastBeat) >= 10*time.Second {
			if !write([]byte(": keep-alive\n\n")) || !flush() {
				return nil
			}
			lastBeat = time.Now()
		}
		if time.Now().After(deadline) {
			return nil
		}
		select {
		case <-c.r.Context().Done():
			return nil
		case <-c.app.Streams.Done():
			return nil
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// workspacePost is Handler._workspace_post (after the read-token and
// terminal admin checks of do_POST).
func (c *reqCtx) workspacePost(path string, body *pyjson.Object) error {
	if err := c.rejectWorkspaceReadToken(); err != nil {
		return err
	}
	if strings.Contains(path, "/terminals") {
		if _, err := c.require("admin"); err != nil {
			return err
		}
	}
	p, err := c.require("read")
	if err != nil {
		return err
	}
	ws := c.app.Terminals
	root := p.Path
	reply := func(out *pyjson.Object, err error) error {
		if err != nil {
			return err
		}
		c.ok(out)
		return nil
	}
	switch path {
	case "/api/workspace/file":
		return reply(workspace.WriteFile(root, body))
	case "/api/workspace/create":
		return reply(workspace.CreatePath(root, body))
	case "/api/workspace/rename":
		return reply(workspace.RenamePath(root, body))
	case "/api/workspace/delete":
		return reply(workspace.DeletePath(root, body))
	case "/api/workspace/terminals":
		timeout := body.Value("timeout")
		if !pyjson.Truthy(timeout) {
			timeout = int64(workspace.TerminalDefaultTimeout)
		}
		cwd := body.Value("cwd")
		if !pyjson.Truthy(cwd) {
			cwd = ""
		}
		cols, ok := body.Get("cols")
		if !ok {
			cols = int64(workspace.TerminalDefaultCols)
		}
		rows, ok := body.Get("rows")
		if !ok {
			rows = int64(workspace.TerminalDefaultRows)
		}
		t, pty, err := ws.Start(root, workspace.StartOptions{
			Command: body.Value("command"), Cwd: cwd, Timeout: timeout, PTY: pyBoolArg(body.Value("pty")),
			Cols: cols, Rows: rows, ProjectID: p.ID(), ProjectPath: p.Path, Creator: c.actor()})
		if err != nil {
			return err
		}
		c.ok(pyjson.NewObject("terminal", t.Public(), "pty_available", pty.Available,
			"stream", "/api/workspace/terminals/"+t.ID+"/stream"))
		return nil
	}
	id := p.ID()
	if m := reTermResize.FindStringSubmatch(path); m != nil {
		if _, err := ws.Get(m[1], &id); err != nil {
			return err
		}
		cols, ok := body.Get("cols")
		if !ok {
			cols = int64(0)
		}
		rows, ok := body.Get("rows")
		if !ok {
			rows = int64(0)
		}
		return reply(ws.Resize(m[1], cols, rows))
	}
	if m := reTermStdin.FindStringSubmatch(path); m != nil {
		if _, err := ws.Get(m[1], &id); err != nil {
			return err
		}
		text := ""
		if v := body.Value("text"); pyjson.Truthy(v) {
			text = pyjson.Str(v)
		}
		snap, err := ws.Write(m[1], text)
		if err != nil {
			return err
		}
		c.ok(pyjson.NewObject("terminal", snap))
		return nil
	}
	if m := reTermCancel.FindStringSubmatch(path); m != nil {
		if _, err := ws.Get(m[1], &id); err != nil {
			return err
		}
		snap, err := ws.Cancel(m[1])
		if err != nil {
			return err
		}
		c.ok(pyjson.NewObject("terminal", snap))
		return nil
	}
	return apiErr(404, "Unknown workspace endpoint: "+path, "")
}

// terminalDelete is the DELETE /api/workspace/terminals/{id} branch.
func (c *reqCtx) terminalDelete(id string) error {
	if err := c.rejectWorkspaceReadToken(); err != nil {
		return err
	}
	p, err := c.require("admin")
	if err != nil {
		return err
	}
	pid := p.ID()
	if _, err := c.app.Terminals.Get(id, &pid); err != nil {
		return err
	}
	out, err := c.app.Terminals.Close(id)
	if err != nil {
		return err
	}
	c.ok(out)
	return nil
}

// pyBoolArg is server._bool: str(value).lower() in {"1", "true", "yes", "on"}.
func pyBoolArg(v any) bool {
	switch pyLowerStr(v) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func pyLowerStr(v any) string {
	if v == nil {
		return "none"
	}
	return strings.ToLower(pyjson.Str(v))
}
