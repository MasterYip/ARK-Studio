package server

// Session context (session_context.py and the App/Handler methods of
// server.py): GET /api/session-contexts[/<id>[/transcript|raw|context]],
// PATCH/DELETE /api/session-contexts/<id>, POST .../continue, and the agent
// upload API GET /api/agent/v1/sessions/<id>, POST .../upload and
// GET /api/agent/v1/sessions/watcher.py.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/sessionctx"
	"arkstudio/go/internal/sessions"
	"arkstudio/go/internal/settings"
)

// sessionContextsPorted are the contract routes this file answers (router.go registers them).
var sessionContextsPorted = []PortedRoute{
	{"exact:/api/session-contexts", "GET,HEAD"},
	{"prefix:/api/session-contexts/", "GET,HEAD"},
	{"fullmatch:/api/session-contexts/([^/]+)", "DELETE"},
	{"fullmatch:/api/session-contexts/([^/]+)", "PATCH"},
	{"fullmatch:/api/session-contexts/([^/]+)/continue", "POST"},
	{"exact:/api/agent/v1/sessions/watcher.py", "GET,HEAD"},
	{"fullmatch:/api/agent/v1/sessions/([^/]+)", "GET,HEAD"},
	{"fullmatch:/api/agent/v1/sessions/([^/]+)/upload", "POST"},
}

var (
	reCtxGet      = pyRe(`/api/session-contexts/([^/]+)(?:/(transcript|raw|context))?`, true)
	reCtxOne      = pyRe(`/api/session-contexts/([^/]+)`, true)
	reCtxContinue = pyRe(`/api/session-contexts/([^/]+)/continue`, true)
	reAgentSess   = pyRe(`/api/agent/v1/sessions/([^/]+)`, true)
	reAgentUpload = pyRe(`/api/agent/v1/sessions/([^/]+)/upload`, true)
)

func (c *reqCtx) contextStore(p *project.Project) *sessionctx.Store {
	return sessionctx.NewStore(p.Path, []string{c.app.AgentToken()})
}

func sessionSettings(p *project.Project) (*pyjson.Object, error) {
	s, err := settings.ProjectSettings(p.Manifest.Snapshot())
	if err != nil {
		return nil, err
	}
	o, _ := s.Value("sessions").(*pyjson.Object)
	return o, nil
}

func sessionUploadsEnabled(p *project.Project) (bool, error) {
	s, err := sessionSettings(p)
	if err != nil {
		return false, err
	}
	return s != nil && pyjson.Truthy(s.Value("accept_uploads")), nil
}

func payloadProjectID(payload *pyjson.Object) string {
	ref, _ := payload.Value("project").(*pyjson.Object)
	if ref == nil {
		return ""
	}
	if !pyjson.Truthy(ref.Value("id")) {
		return ""
	}
	return pyjson.Str(ref.Value("id"))
}

func listedSessions(m *sessions.Manager) []*pyjson.Object {
	var out []*pyjson.Object
	list, _ := m.List().Value("sessions").([]any)
	for _, item := range list {
		if o, ok := item.(*pyjson.Object); ok {
			out = append(out, o)
		}
	}
	return out
}

// syncSessionMirrors is App.sync_session_mirrors.
func (c *reqCtx) syncSessionMirrors(p *project.Project) {
	m := c.app.Sessions
	for _, payload := range listedSessions(m) {
		if payloadProjectID(payload) != p.ID() {
			continue
		}
		s, err := m.Get(pyjson.Str(payload.Value("id")))
		if err != nil || m.IsActive(s) {
			continue
		}
		if stale, err := sessionctx.MirrorIsStale(p.Path, s.Folder); err == nil && stale {
			_, _ = sessionctx.MirrorInternal(p.Path, s.Folder, []string{c.app.AgentToken()})
		}
	}
}

// liveInternal is App._live_internal (ids in list order).
func (c *reqCtx) liveInternal(p *project.Project) ([]string, map[string]*pyjson.Object) {
	m := c.app.Sessions
	var order []string
	out := map[string]*pyjson.Object{}
	for _, payload := range listedSessions(m) {
		if payloadProjectID(payload) != p.ID() {
			continue
		}
		id := pyjson.Str(payload.Value("id"))
		folder := ""
		if s, err := m.Get(id); err == nil {
			folder = s.Folder
		}
		if _, seen := out[id]; !seen {
			order = append(order, id)
		}
		out[id] = sessionctx.InternalPublic(payload, folder)
	}
	return order, out
}

// sessionContexts is App.session_contexts.
func (c *reqCtx) sessionContexts(user string, admin bool) (*pyjson.Object, error) {
	p, err := c.require("read")
	if err != nil {
		return nil, err
	}
	st := c.contextStore(p)
	c.syncSessionMirrors(p)
	order, live := c.liveInternal(p)
	items := st.List()
	for _, id := range order {
		items = append(items, live[id])
	}
	for _, id := range sessionctx.MirrorIDs(p.Path) {
		if _, isLive := live[id]; !isLive {
			if rec, err := sessionctx.MirrorPublic(p.Path, id); err == nil {
				items = append(items, rec)
			}
		}
	}
	var visible []*pyjson.Object
	for _, item := range items {
		if sessionctx.CanView(item, user, admin) {
			visible = append(visible, sessionctx.WithAccess(item, user, admin))
		}
	}
	key := func(o *pyjson.Object) string {
		v := o.Value("updated_at")
		if !pyjson.Truthy(v) {
			return ""
		}
		return pyjson.Str(v)
	}
	sort.SliceStable(visible, func(i, j int) bool { return key(visible[i]) > key(visible[j]) })
	list := make([]any, len(visible))
	for i, v := range visible {
		list[i] = v
	}
	sess, err := sessionSettings(p)
	if err != nil {
		return nil, err
	}
	lim := sessionctx.CurrentLimits()
	return pyjson.NewObject("sessions", list, "settings", sess,
		"usage", pyjson.NewObject("bytes", st.UsageBytes(), "limit", lim.Project),
		"limits", lim.Object(), "viewer", pyjson.NewObject("user", user, "admin", admin)), nil
}

// contextKind is App._context_kind: external, live or copy.
func (c *reqCtx) contextKind(id string) (string, error) {
	if strings.HasPrefix(id, "ark-ses-") {
		if _, err := sessionctx.CheckID(id); err != nil {
			return "", err
		}
		return "external", nil
	}
	if s, err := c.app.Sessions.Get(id); err == nil && s != nil {
		p, err := c.require("read")
		if err != nil {
			return "", err
		}
		if c.app.Sessions.ProjectID(s) == p.ID() {
			return "live", nil
		}
		return "", errNotAccessible
	}
	p, err := c.require("read")
	if err != nil {
		return "", err
	}
	if _, err := sessionctx.ReadMirrorMeta(p.Path, id); err != nil {
		return "", err
	}
	return "copy", nil
}

func (c *reqCtx) contextRecord(id string) (*pyjson.Object, error) {
	kind, err := c.contextKind(id)
	if err != nil {
		return nil, err
	}
	p, err := c.require("read")
	if err != nil {
		return nil, err
	}
	switch kind {
	case "external":
		st := c.contextStore(p)
		meta, err := st.Get(id)
		if err != nil {
			return nil, err
		}
		return st.Public(meta), nil
	case "live":
		s, err := c.app.Sessions.Get(id)
		if err != nil {
			return nil, err
		}
		return sessionctx.InternalPublic(c.app.Sessions.Public(s, false), s.Folder), nil
	}
	return sessionctx.MirrorPublic(p.Path, id)
}

// sessionContext is App.session_context.
func (c *reqCtx) sessionContext(id, user string, admin, manage bool) (*pyjson.Object, error) {
	record, err := c.contextRecord(id)
	if err != nil {
		return nil, err
	}
	if !sessionctx.CanView(record, user, admin) {
		return nil, errNotAccessible
	}
	if manage && !sessionctx.CanManage(record, user, admin) {
		return nil, apiErr(403, "Only the session's creator or a project admin can change it.", "SESSION_NOT_OWNER")
	}
	return sessionctx.WithAccess(record, user, admin), nil
}

func (c *reqCtx) sessionContextTranscript(id, user string, admin bool) (*pyjson.Object, error) {
	record, err := c.sessionContext(id, user, admin, false)
	if err != nil {
		return nil, err
	}
	p, err := c.require("read")
	if err != nil {
		return nil, err
	}
	if record.Value("source") == "external" {
		return c.contextStore(p).Transcript(id)
	}
	if pyjson.Truthy(record.Value("copy")) {
		return sessionctx.MirrorTranscript(p.Path, id)
	}
	data, err := c.app.Sessions.Transcript(id)
	if err != nil {
		return nil, err
	}
	data.Set("source", "internal")
	return data, nil
}

func (c *reqCtx) sessionContextRaw(id, user string, admin bool) ([]byte, error) {
	record, err := c.sessionContext(id, user, admin, false)
	if err != nil {
		return nil, err
	}
	p, err := c.require("read")
	if err != nil {
		return nil, err
	}
	var source string
	switch {
	case record.Value("source") == "external":
		if source, err = c.contextStore(p).RawPath(id); err != nil {
			return nil, err
		}
	default:
		if !pyjson.Truthy(record.Value("copy")) {
			// The download is the redacted mirror, never the raw runtime log.
			s, err := c.app.Sessions.Get(id)
			if err != nil {
				return nil, err
			}
			if _, err := sessionctx.MirrorInternal(p.Path, s.Folder, []string{c.app.AgentToken()}); err != nil {
				return nil, err
			}
		}
		folder, err := sessionctx.MirrorFolder(p.Path, id)
		if err != nil {
			return nil, err
		}
		source = filepath.Join(folder, "stdout.jsonl")
	}
	if fi, err := os.Stat(source); err != nil || !fi.Mode().IsRegular() {
		return []byte{}, nil
	}
	return os.ReadFile(source)
}

func (c *reqCtx) sessionContextSummary(id, user string, admin bool) (string, error) {
	record, err := c.sessionContext(id, user, admin, false)
	if err != nil {
		return "", err
	}
	tr, err := c.sessionContextTranscript(id, user, admin)
	if err != nil {
		return "", err
	}
	items, _ := tr.Value("items").([]any)
	return sessionctx.ContextSummary(record, items, sessionctx.SummaryLimit), nil
}

// sessionContextsGet is Handler._session_contexts_get; handled=false for
// other paths.
func (c *reqCtx) sessionContextsGet(path string) (bool, error) {
	if path != "/api/session-contexts" && !strings.HasPrefix(path, "/api/session-contexts/") {
		return false, nil
	}
	user, admin, err := c.sessionViewer()
	if err != nil {
		return true, err
	}
	if path == "/api/session-contexts" {
		return true, c.okErr(c.sessionContexts(user, admin))
	}
	g := reCtxGet.FindStringSubmatch(path)
	if g == nil {
		return true, apiErr(404, "Unknown session context endpoint: "+path, "")
	}
	id, action := g[1], g[2]
	switch action {
	case "":
		rec, err := c.sessionContext(id, user, admin, false)
		if err != nil {
			return true, err
		}
		c.ok(pyjson.NewObject("session", rec))
		return true, nil
	case "transcript":
		return true, c.okErr(c.sessionContextTranscript(id, user, admin))
	case "context":
		summary, err := c.sessionContextSummary(id, user, admin)
		if err != nil {
			return true, err
		}
		c.ok(pyjson.NewObject("summary", summary))
		return true, nil
	}
	data, err := c.sessionContextRaw(id, user, admin)
	if err != nil {
		return true, err
	}
	c.send(200, data, "application/x-ndjson; charset=utf-8", headerList{
		{"Content-Disposition", `attachment; filename="` + id + `.jsonl"`},
		{"Cache-Control", "no-store"}}, true)
	return true, nil
}

// sessionContextsPatch is the session-context branch of do_PATCH.
func (c *reqCtx) sessionContextsPatch(path string, body *pyjson.Object) (bool, error) {
	g := reCtxOne.FindStringSubmatch(path)
	if g == nil {
		return false, nil
	}
	if err := c.rejectTaskReadToken("change stored sessions"); err != nil {
		return true, err
	}
	user, admin, err := c.sessionViewer()
	if err != nil {
		return true, err
	}
	id := g[1]
	record, err := c.sessionContext(id, user, admin, true)
	if err != nil {
		return true, err
	}
	p, err := c.require("read")
	if err != nil {
		return true, err
	}
	switch {
	case record.Value("source") == "external":
		_, err = c.contextStore(p).Update(id, body)
	case pyjson.Truthy(record.Value("copy")):
		_, err = sessionctx.UpdateMirror(p.Path, id, body)
	default:
		_, err = c.app.Sessions.UpdateContext(id, body)
	}
	if err != nil {
		return true, err
	}
	rec, err := c.sessionContext(id, user, admin, false)
	if err != nil {
		return true, err
	}
	c.ok(pyjson.NewObject("session", rec))
	return true, nil
}

// sessionContextsDelete is the session-context branch of do_DELETE.
func (c *reqCtx) sessionContextsDelete(path string) (bool, error) {
	g := reCtxOne.FindStringSubmatch(path)
	if g == nil {
		return false, nil
	}
	if err := c.rejectTaskReadToken("delete stored sessions"); err != nil {
		return true, err
	}
	user, admin, err := c.sessionViewer()
	if err != nil {
		return true, err
	}
	id := g[1]
	record, err := c.sessionContext(id, user, admin, true)
	if err != nil {
		return true, err
	}
	p, err := c.require("read")
	if err != nil {
		return true, err
	}
	switch {
	case record.Value("source") == "external":
		return true, c.okErr(c.contextStore(p).Delete(id))
	case pyjson.Truthy(record.Value("copy")):
		_, _ = sessionctx.RemoveMirror(p.Path, id)
		c.ok(pyjson.NewObject("deleted", id))
		return true, nil
	}
	return true, c.okErr(c.app.Sessions.Delete(id))
}

// sessionContextsPost is POST /api/session-contexts/<id>/continue
// (App.continue_session_context).
func (c *reqCtx) sessionContextsPost(path string, body *pyjson.Object) (bool, error) {
	g := reCtxContinue.FindStringSubmatch(path)
	if g == nil {
		return false, nil
	}
	if err := c.rejectTaskReadToken("continue stored sessions"); err != nil {
		return true, err
	}
	user, admin, err := c.sessionViewer()
	if err != nil {
		return true, err
	}
	id := g[1]
	record, err := c.sessionContext(id, user, admin, false)
	if err != nil {
		return true, err
	}
	m := c.app.Sessions
	mode := pytext.Lower(pytext.Strip(pyjson.Str(orAny(body.Value("mode"), "auto"))))
	if mode != "auto" && mode != "resume" && mode != "seed" {
		return true, apiErr(400, "mode must be auto, resume or seed", "SESSION_CONTINUE_MODE_INVALID")
	}
	text := pytext.Strip(strOrEmpty(orAny(body.Value("text"), body.Value("message"))))
	resume, _ := record.Value("resume").(*pyjson.Object)
	resumable := record.Value("source") == "internal" && !pyjson.Truthy(record.Value("copy")) &&
		resume != nil && pyjson.Truthy(resume.Value("native"))
	canManage := pyjson.Truthy(record.Value("can_manage"))
	if mode == "resume" && !resumable {
		return true, apiErr(409, "this session has no provider session id to resume", "SESSION_NOT_RESUMABLE")
	}
	if mode == "resume" && !canManage {
		return true, apiErr(403, "Only the session's creator or a project admin can change it.", "SESSION_NOT_OWNER")
	}
	h := c.sessionHooks()
	if resumable && canManage && (mode == "auto" || mode == "resume") && !pyjson.Truthy(body.Value("platform")) {
		if text == "" {
			return true, apiErr(400, "a message is required to continue", "SESSION_TEXT_REQUIRED")
		}
		var model any
		if pyjson.Truthy(body.Value("model")) {
			model = body.Value("model")
		}
		payload, err := m.ContinueSession(h, id, text, sessions.ContinueOptions{Model: model, Actor: c.actor(), Resume: true})
		if err != nil {
			return true, err
		}
		c.ok(pyjson.NewObject("mode", "resume", "session", payload))
		return true, nil
	}
	p, err := c.require("read")
	if err != nil {
		return true, err
	}
	if p.ID() == demoID {
		return true, apiErr(403, "Coding agents are disabled in the demo project. Chat and Assistant are still available.", "DEMO_AGENTS_DISABLED")
	}
	platform := pytext.Strip(strOrEmpty(body.Value("platform")))
	if platform == "" {
		agent := strOrEmpty(record.Value("agent"))
		for _, preset := range m.Presets {
			if pyjson.Str(preset.Value("id")) == agent {
				platform = agent
				break
			}
		}
	}
	if platform == "" {
		return true, apiErr(400, "choose a platform for the new session", "SESSION_PLATFORM_REQUIRED")
	}
	summary, err := c.sessionContextSummary(id, user, admin)
	if err != nil {
		return true, err
	}
	lead := text
	if lead == "" {
		lead = "Continue the work from the previous session."
	}
	prompt := lead + "\n\n" + summary
	links := pyList(record.Value("linked_units"))
	var task string
	if pyjson.Truthy(record.Value("task_ref")) {
		task = pyjson.Str(record.Value("task_ref"))
	} else if len(links) > 0 {
		task = pyjson.Str(links[0])
	}
	title := strOrEmpty(orAny(record.Value("title"), record.Value("id")))
	if !strings.HasPrefix(title, "Continue · ") {
		title = "Continue · " + title
	}
	model := body.Value("model")
	if !pyjson.Truthy(model) {
		if agent, ok := record.Value("agent").(string); ok && platform == agent {
			model = record.Value("model")
		} else {
			model = ""
		}
	}
	s, err := m.Start(h, pyjson.NewObject("platform", platform, "title", pytext.Prefix(title, 120),
		"prompt", prompt, "model", model, "task", task), c.hostBaseURL(), c.actor())
	if err != nil {
		return true, err
	}
	m.SetMeta(s, "continued_from", record.Value("id"), "linked_units", links)
	c.ok(pyjson.NewObject("mode", "seed", "session", m.Public(s, true)))
	return true, nil
}

// pyList is list(v or []) for a decoded JSON value.
func pyList(v any) []any {
	switch x := v.(type) {
	case []any:
		return append([]any{}, x...)
	case string:
		out := []any{}
		for _, r := range x {
			out = append(out, string(r))
		}
		return out
	}
	return []any{}
}

// agentSessionsGet answers watcher.py and GET /api/agent/v1/sessions/<id>
// (inside Handler._agent_get, after require()).
func (c *reqCtx) agentSessionsGet(path string) (bool, error) {
	if path == "/api/agent/v1/sessions/watcher.py" {
		c.send(200, sessionctx.WatcherScript, "text/x-python; charset=utf-8", headerList{
			{"Content-Disposition", `attachment; filename="ark-session-watch.py"`},
			{"Cache-Control", "no-store"}}, true)
		return true, nil
	}
	g := reAgentSess.FindStringSubmatch(path)
	if g == nil {
		return false, nil
	}
	p, err := c.require("read")
	if err != nil {
		return true, err
	}
	st := c.contextStore(p)
	enabled, err := sessionUploadsEnabled(p)
	if err != nil {
		return true, err
	}
	state, err := st.Status(g[1], enabled)
	if err != nil {
		return true, err
	}
	if state.Value("exists") == true {
		user, admin, err := c.sessionViewer()
		if err != nil {
			return true, err
		}
		meta, err := st.Get(g[1])
		if err != nil {
			return true, err
		}
		if !sessionctx.CanManage(meta, user, admin) {
			return true, errNotAccessible
		}
	}
	c.ok(state)
	return true, nil
}

// agentSessionUpload is POST /api/agent/v1/sessions/<id>/upload (first
// branch of Handler._agent_write).
func (c *reqCtx) agentSessionUpload(path string, body *pyjson.Object, actor string) (bool, error) {
	g := reAgentUpload.FindStringSubmatch(path)
	if g == nil {
		return false, nil
	}
	p, err := c.require("write")
	if err != nil {
		return true, err
	}
	enabled, err := sessionUploadsEnabled(p)
	if err != nil {
		return true, err
	}
	if !enabled {
		return true, apiErr(403, "This project does not accept session uploads. A project admin can turn on "+
			"session uploads in Execute > Runs > Session context.", "SESSION_UPLOADS_DISABLED")
	}
	user, admin, err := c.sessionViewer()
	if err != nil {
		return true, err
	}
	return true, c.okErr(c.contextStore(p).Upload(g[1], body, actor, user, admin))
}
