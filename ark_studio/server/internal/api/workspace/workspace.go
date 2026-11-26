// Package workspace is the HTTP side of the Editor workspace: files,
// changes and terminals (Handler._workspace_get / _workspace_post /
// _stream_terminal and the DELETE /api/workspace/terminals/{id} branch of
// arkstudio/server.py over internal/workspace).  internal/server registers
// it (router.go) after the read-token and project checks of do_GET/do_POST.
package workspace

import (
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"arkstudio/go/internal/api"
	searchapi "arkstudio/go/internal/api/search"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/workspace"
)

// Handlers answers the workspace routes for one server.
type Handlers struct {
	Terminals *workspace.Manager // the Editor's terminal registry
	// Done is closed on server shutdown (ends terminal streams).
	Done func() <-chan struct{}
	// GuessType is the host's mimetypes.guess_type (raw file replies).
	GuessType func(name string) string
}

var (
	// ReTerminal is ^/api/workspace/terminals/([^/]+)$ (GET and DELETE).
	ReTerminal   = api.PyRe(`^/api/workspace/terminals/([^/]+)$`, false)
	reTermStream = api.PyRe(`^/api/workspace/terminals/([^/]+)/stream$`, false)
	reTermResize = api.PyRe(`^/api/workspace/terminals/([^/]+)/resize$`, false)
	reTermStdin  = api.PyRe(`^/api/workspace/terminals/([^/]+)/stdin$`, false)
	reTermCancel = api.PyRe(`^/api/workspace/terminals/([^/]+)/cancel$`, false)
)

// IsPath reports a /api/workspace or /api/workspace/... path.
func IsPath(path string) bool {
	return path == "/api/workspace" || strings.HasPrefix(path, "/api/workspace/")
}

func rejectReadToken(c api.Ctx) error {
	return c.RejectTaskAgentReadToken("task-agent read tokens cannot use the files workspace")
}

// Get is Handler._workspace_get.
func (h *Handlers) Get(c api.Ctx, path string) error {
	p, err := c.Require("read")
	if err != nil {
		return err
	}
	ws := h.Terminals
	root := p.Path
	reply := func(out *pyjson.Object, err error) error {
		if err != nil {
			return err
		}
		c.OK(out)
		return nil
	}
	switch path {
	case "/api/workspace/info":
		data := workspace.Info(root)
		data.Set("project", p.Name())
		c.OK(data)
		return nil
	case "/api/workspace/tree":
		return reply(workspace.Tree(root, c.Query().First("path", ""), api.QInt(c.Query(), "depth", 1, 1, 4)))
	case "/api/workspace/search":
		c.OK(workspace.Search(root, c.Query().First("q", ""), api.QInt(c.Query(), "limit", 100, 1, 200)))
		return nil
	case "/api/workspace/grep":
		return searchapi.Grep(c, root)
	case "/api/workspace/file":
		target := c.Query().First("path", "")
		if target == "" {
			return api.Err(400, "path is required", "")
		}
		return reply(workspace.ReadFile(root, target))
	case "/api/workspace/raw":
		target, err := workspace.ResolveFile(root, c.Query().First("path", ""))
		if err != nil {
			return err
		}
		download := c.Query().First("download", "")
		return c.SendFile(target, h.GuessType(filepath.Base(target)), download == "1" || download == "true")
	case "/api/workspace/changes":
		return reply(workspace.Changes(root, c.Query().First("base", "HEAD")))
	case "/api/workspace/diff":
		target := c.Query().First("path", "")
		if target == "" {
			return api.Err(400, "path is required", "")
		}
		return reply(workspace.DiffFile(root, target, c.Query().First("base", "HEAD")))
	case "/api/workspace/timeline":
		return reply(workspace.Timeline(root, c.Query().First("path", ""), api.QInt(c.Query(), "limit", workspace.TimelineDefault, 1, workspace.TimelineMax)))
	case "/api/workspace/preview-token":
		return h.previewToken(c, p.Path, p.ID())
	case "/api/workspace/show":
		return reply(workspace.ShowFile(root, c.Query().First("path", ""), c.Query().First("rev", "")))
	case "/api/workspace/terminals":
		c.OK(ws.List(p.ID()))
		return nil
	}
	if m := reTermStream.FindStringSubmatch(path); m != nil {
		return h.streamTerminal(c, m[1], api.QInt(c.Query(), "offset", 0, 0, 10_000_000), p.ID())
	}
	if m := ReTerminal.FindStringSubmatch(path); m != nil {
		id := p.ID()
		if _, err := ws.Get(m[1], &id); err != nil {
			return err
		}
		snap, err := ws.Read(m[1], api.QInt(c.Query(), "offset", 0, 0, 10_000_000), api.QInt(c.Query(), "limit", 200, 1, 1000))
		if err != nil {
			return err
		}
		c.OK(pyjson.NewObject("terminal", snap))
		return nil
	}
	return api.Err(404, "Unknown workspace endpoint: "+path, "")
}

// streamTerminal is Handler._stream_terminal: SSE output/state/done events.
// The stream leaves the admission pool and the per-request deadline: it is
// bounded by its own 600 s deadline, the client and server shutdown.
func (h *Handlers) streamTerminal(c api.Ctx, id string, offset int64, projectID string) error {
	ws := h.Terminals
	var pid *string
	if projectID != "" {
		pid = &projectID
	}
	if _, err := ws.Get(id, pid); err != nil {
		return err
	}
	c.DetachFromRequestLimits()
	st := c.StartRawStream([][2]string{
		{"Content-Type", "text/event-stream; charset=utf-8"},
		{"Cache-Control", "no-store"},
		{"X-Accel-Buffering", "no"},
		{"X-Content-Type-Options", "nosniff"},
		{"Connection", "close"},
	})
	write := func(b []byte) bool {
		st.SetWriteDeadline(time.Now().Add(30 * time.Second)) //nolint:errcheck
		if _, err := st.Write(b); err != nil {
			return false
		}
		return true
	}
	flush := func() bool { return st.Flush() == nil }
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
		case <-c.RequestContext().Done():
			return nil
		case <-h.Done():
			return nil
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// Post is Handler._workspace_post (after the read-token and
// terminal admin checks of do_POST).
func (h *Handlers) Post(c api.Ctx, path string, body *pyjson.Object) error {
	if err := rejectReadToken(c); err != nil {
		return err
	}
	if strings.Contains(path, "/terminals") {
		if _, err := c.Require("admin"); err != nil {
			return err
		}
	}
	p, err := c.Require("read")
	if err != nil {
		return err
	}
	ws := h.Terminals
	root := p.Path
	reply := func(out *pyjson.Object, err error) error {
		if err != nil {
			return err
		}
		c.OK(out)
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
	case "/api/workspace/replace":
		return searchapi.Replace(c, root, body)
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
			Command: body.Value("command"), Cwd: cwd, Timeout: timeout, PTY: api.BoolArg(body.Value("pty")),
			Cols: cols, Rows: rows, ProjectID: p.ID(), ProjectPath: p.Path, Creator: c.Actor()})
		if err != nil {
			return err
		}
		c.OK(pyjson.NewObject("terminal", t.Public(), "pty_available", pty.Available,
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
		c.OK(pyjson.NewObject("terminal", snap))
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
		c.OK(pyjson.NewObject("terminal", snap))
		return nil
	}
	return api.Err(404, "Unknown workspace endpoint: "+path, "")
}

// Delete is the DELETE /api/workspace/terminals/{id} branch.
func (h *Handlers) Delete(c api.Ctx, id string) error {
	if err := rejectReadToken(c); err != nil {
		return err
	}
	p, err := c.Require("admin")
	if err != nil {
		return err
	}
	pid := p.ID()
	if _, err := h.Terminals.Get(id, &pid); err != nil {
		return err
	}
	out, err := h.Terminals.Close(id)
	if err != nil {
		return err
	}
	c.OK(out)
	return nil
}

// previewToken answers GET /api/workspace/preview-token: ?path=<file.html>
// issues a token for the file's folder (the interactive HTML preview,
// workspace/preview.go), ?renew=<token> extends one of the same person.
func (h *Handlers) previewToken(c api.Ctx, root, projectID string) error {
	q := c.Query()
	expires := int64(workspace.PreviewTTL / time.Second)
	if token := q.First("renew", ""); token != "" {
		if _, ok := workspace.RenewPreview(token, projectID, c.Actor()); !ok {
			return api.Err(404, "The preview has expired. Reload it.", "PREVIEW_EXPIRED")
		}
		c.OK(pyjson.NewObject("token", token, "expires_in", expires))
		return nil
	}
	p, name, err := workspace.IssuePreview(root, projectID, c.Actor(), q.First("path", ""))
	if err != nil {
		return err
	}
	parts := strings.Split(name, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	c.OK(pyjson.NewObject("token", p.Token, "dir", p.Dir, "expires_in", expires,
		"url", PreviewPrefix+p.Token+"/"+strings.Join(parts, "/")))
	return nil
}

// PreviewPrefix is the token-authorised file route of HTML previews
// (internal/server answers it before the session checks).
const PreviewPrefix = "/api/workspace/preview/"
