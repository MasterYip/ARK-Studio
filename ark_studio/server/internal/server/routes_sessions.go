package server

// /api/agent-sessions/* and GET /api/task-agent/v1/session (Handler.
// _sessions_get, the agent-session branches of do_POST / do_PATCH /
// do_DELETE, _guard_session_access) plus the SessionManager providers of
// App.__init__ (agent_config_for, assistant_config_for, linux_account_for,
// _on_session_change).

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"

	"arkstudio/go/internal/assistcfg"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/sessionctx"
	"arkstudio/go/internal/sessions"
	"arkstudio/go/internal/sessionstart"
)

// sessionsPorted are the contract routes this file answers (router.go registers them).
var sessionsPorted = []PortedRoute{
	{"exact:/api/agent-sessions", "GET,HEAD"},
	{"exact:/api/agent-sessions", "POST"},
	{"exact:/api/agent-sessions/groups", "GET,HEAD"},
	{"exact:/api/agent-sessions/presets", "GET,HEAD"},
	{`regex:^/api/agent-sessions/([^/]+)$`, "GET,HEAD"},
	{`regex:^/api/agent-sessions/([^/]+)$`, "DELETE"},
	{`regex:^/api/agent-sessions/([^/]+)$`, "PATCH"},
	{`regex:^/api/agent-sessions/([^/]+)/approvals$`, "GET,HEAD"},
	{`regex:^/api/agent-sessions/([^/]+)/approvals/([^/]+)$`, "POST"},
	{`regex:^/api/agent-sessions/([^/]+)/archive$`, "POST"},
	{`regex:^/api/agent-sessions/([^/]+)/artifacts$`, "GET,HEAD"},
	{`regex:^/api/agent-sessions/([^/]+)/artifacts/download$`, "GET,HEAD"},
	{`regex:^/api/agent-sessions/([^/]+)/artifacts/read$`, "GET,HEAD"},
	{`regex:^/api/agent-sessions/([^/]+)/context/validate$`, "POST"},
	{`regex:^/api/agent-sessions/([^/]+)/continue$`, "POST"},
	{`regex:^/api/agent-sessions/([^/]+)/fork$`, "POST"},
	{`regex:^/api/agent-sessions/([^/]+)/input$`, "POST"},
	{`regex:^/api/agent-sessions/([^/]+)/logs$`, "GET,HEAD"},
	{`regex:^/api/agent-sessions/([^/]+)/media$`, "GET,HEAD"},
	{`regex:^/api/agent-sessions/([^/]+)/relaunch$`, "POST"},
	{`regex:^/api/agent-sessions/([^/]+)/steer$`, "POST"},
	{`regex:^/api/agent-sessions/([^/]+)/steer/reorder$`, "POST"},
	{`regex:^/api/agent-sessions/([^/]+)/steer/([^/]+)$`, "DELETE"},
	{`regex:^/api/agent-sessions/([^/]+)/steer/([^/]+)$`, "PATCH"},
	{`regex:^/api/agent-sessions/([^/]+)/steer/([^/]+)$`, "POST"},
	{`regex:^/api/agent-sessions/([^/]+)/stop$`, "POST"},
	{`regex:^/api/agent-sessions/([^/]+)/transcript$`, "GET,HEAD"},
	{"exact:/api/task-agent/v1/session", "GET,HEAD"},
}

var (
	reSessLogs       = pyRe(`^/api/agent-sessions/([^/]+)/logs$`, false)
	reSessTranscript = pyRe(`^/api/agent-sessions/([^/]+)/transcript$`, false)
	reSessApprovals  = pyRe(`^/api/agent-sessions/([^/]+)/approvals$`, false)
	reSessMedia      = pyRe(`^/api/agent-sessions/([^/]+)/media$`, false)
	reSessRead       = pyRe(`^/api/agent-sessions/([^/]+)/artifacts/read$`, false)
	reSessDownload   = pyRe(`^/api/agent-sessions/([^/]+)/artifacts/download$`, false)
	reSessArtifacts  = pyRe(`^/api/agent-sessions/([^/]+)/artifacts$`, false)
	reSessInput      = pyRe(`^/api/agent-sessions/([^/]+)/input$`, false)
	reSessContinue   = pyRe(`^/api/agent-sessions/([^/]+)/continue$`, false)
	reSessSteerPost  = pyRe(`^/api/agent-sessions/([^/]+)/steer$`, false)
	reSessReorder    = pyRe(`^/api/agent-sessions/([^/]+)/steer/reorder$`, false)
	reSessDecide     = pyRe(`^/api/agent-sessions/([^/]+)/approvals/([^/]+)$`, false)
	reSessFork       = pyRe(`^/api/agent-sessions/([^/]+)/fork$`, false)
	reSessValidate   = pyRe(`^/api/agent-sessions/([^/]+)/context/validate$`, false)
	reSessStop       = pyRe(`^/api/agent-sessions/([^/]+)/stop$`, false)
	reSessArchive    = pyRe(`^/api/agent-sessions/([^/]+)/archive$`, false)
	reSessRelaunch   = pyRe(`^/api/agent-sessions/([^/]+)/relaunch$`, false)
	reDispositionBad = regexp.MustCompile(`["\r\n]`)
)

func asSessionError(err error) (*sessions.Error, bool) {
	var se *sessions.Error
	ok := errors.As(err, &se)
	return se, ok
}

// configureSessions wires the SessionManager options of App.__init__.
func (a *App) configureSessions() {
	m := a.Sessions
	m.Presets = a.Providers.Presets
	m.StripEnvValues = []string{a.AgentToken()}
	m.WriteToken = a.AgentToken()
	m.Catalog = a.Providers
	m.GuessType = func(name string) string { t, _ := guessMime(name); return t }
	m.OnChange = a.onSessionChange
	m.ReferencedFiles = a.referencedFiles
}

// onSessionChange is App._on_session_change: keep the redacted mirror in step.
func (a *App) onSessionChange(s *sessions.Session, event string) {
	if event == "finish" || event == "delete" {
		a.revokeSessionTokens(s)
	}
	c := &reqCtx{app: a, ctx: context.Background()}
	p, err := c.projectByID(a.Sessions.ProjectID(s))
	if err != nil || p == nil {
		return
	}
	if event == "delete" {
		if a.Org != nil {
			_ = a.Org.ForgetSession(context.Background(), s.ID) // session_notices.go
		}
		_, _ = sessionctx.RemoveMirror(p.Path, s.ID)
	} else if !a.Sessions.IsActive(s) || event == "update" {
		_, _ = sessionctx.MirrorInternal(p.Path, s.Folder, []string{a.AgentToken()})
	}
}

func (c *reqCtx) projectSummaryForSettings() *pyjson.Object {
	p, err := c.currentProject()
	if err != nil || p == nil {
		return pyjson.NewObject()
	}
	summary, err := p.Summary()
	if err != nil {
		return pyjson.NewObject()
	}
	return summary
}

// personalAgents is OrgManager.personal_settings(username, "agents") without
// "updated_at": the stored data and the decrypted secrets.  It goes through
// org's reader so plaintext left by the old save bug is re-sealed on read
// (main 6988ce9).
func (c *reqCtx) personalAgents(username string) (*pyjson.Object, *pyjson.Object, error) {
	out, err := c.app.Org.PersonalSettings(c.ctx, username, "agents")
	if err != nil {
		return nil, nil, err
	}
	secrets, _ := out.Value("secrets").(*pyjson.Object)
	if secrets == nil {
		secrets = pyjson.NewObject()
	}
	out.Delete("secrets")
	out.Delete("updated_at")
	return out, secrets, nil
}

// assistantKeyFor is App.assistant_config_for(username)["api_key"]: the
// per-user effective key; with sign-in a key that only comes from the global
// config is not handed to a task subprocess.
func (c *reqCtx) assistantKeyFor(actor string) (string, error) {
	global, err := assistcfg.Load()
	if err != nil {
		return "", err
	}
	resolved, err := c.app.Org.ResolveAssistant(c.ctx, c.projectSummaryForSettings(), actor, global)
	if err != nil {
		return "", err
	}
	if c.app.Auth != nil {
		if src, ok := resolved.Value("sources").(*pyjson.Object); ok && pyjson.Str(src.Value("api_key")) == "global" {
			return "", nil
		}
	}
	return pyjson.Str(resolved.Value("api_key")), nil
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func objectOf(v any) *pyjson.Object {
	if o, ok := v.(*pyjson.Object); ok {
		return o
	}
	return pyjson.NewObject()
}

// resolveAgents is OrgManager.resolve_agents.
func (c *reqCtx) resolveAgents(summary *pyjson.Object, username string) (*pyjson.Object, error) {
	projectSettings := objectOf(summary.Value("settings"))
	projectAgents := objectOf(projectSettings.Value("agents"))
	projectProviders := objectOf(projectAgents.Value("providers"))
	personal, secrets, err := c.personalAgents(username)
	if err != nil {
		return nil, err
	}
	personalProviders := objectOf(personal.Value("providers"))
	secretEnv := map[string]*pyjson.Object{}
	for _, key := range secrets.Keys() {
		parts := strings.SplitN(key, ":", 3)
		if len(parts) != 3 || parts[0] != "env" {
			continue
		}
		provider := pytext.Lower(parts[1])
		if secretEnv[provider] == nil {
			secretEnv[provider] = pyjson.NewObject()
		}
		secretEnv[provider].Set(parts[2], pyjson.Str(secrets.Value(key)))
	}
	presetsOut := pyjson.NewObject()
	providersPublic := []any{}
	asList := func(v any) []any {
		if l, ok := v.([]any); ok {
			return l
		}
		if l, ok := v.([]string); ok {
			out := make([]any, len(l))
			for i, s := range l {
				out[i] = s
			}
			return out
		}
		return []any{v}
	}
	for _, base := range c.app.Sessions.Presets {
		id := pyjson.Str(base.Value("id"))
		if id == "" {
			continue
		}
		personalItem := objectOf(personalProviders.Value(id))
		projectItem := objectOf(projectProviders.Value(id))
		command := asList(base.Value("command"))
		source := "global"
		if v := personalItem.Value("command"); pyjson.Truthy(v) {
			command, source = asList(v), "personal"
		} else if v := projectItem.Value("command"); pyjson.Truthy(v) {
			command, source = asList(v), "project"
		}
		env := pyjson.NewObject()
		envNames := []any{}
		has := func(name string) bool {
			for _, x := range envNames {
				if x == name {
					return true
				}
			}
			return false
		}
		names, _ := projectItem.Value("env_names").([]any)
		for _, n := range names {
			text := pytext.Strip(strOrEmpty(n))
			if text == "" || has(text) {
				continue
			}
			envNames = append(envNames, text)
			if value := pytext.Strip(os.Getenv(text)); value != "" {
				env.Set(text, value)
			}
		}
		if se := secretEnv[id]; se != nil {
			for _, name := range se.Keys() {
				if !has(name) {
					envNames = append(envNames, name)
				}
				env.Set(name, se.Value(name))
			}
		}
		enabled, disabledBy := true, ""
		if v, ok := projectItem.Get("enabled"); ok && v == false {
			enabled, disabledBy = false, "project"
		} else if v, ok := personalItem.Get("enabled"); ok && v != nil {
			enabled, disabledBy = pyjson.Truthy(v), "personal"
		} else if v, ok := projectItem.Get("enabled"); ok && v != nil {
			enabled, disabledBy = pyjson.Truthy(v), "project"
		}
		if enabled {
			disabledBy = ""
		}
		presetsOut.Set(id, pyjson.NewObject("command", command, "env", env, "enabled", enabled, "command_source", source,
			"disabled_by", disabledBy))
		credential := "none"
		if secretEnv[id] != nil && secretEnv[id].Len() > 0 {
			credential = "personal"
		} else if env.Len() > 0 {
			credential = "project"
		}
		label := base.Value("label")
		if !pyjson.Truthy(label) {
			label = id
		}
		providersPublic = append(providersPublic, pyjson.NewObject("id", id, "label", label, "command", command,
			"command_source", source, "env_names", envNames, "credential_source", credential,
			"overridden", personalItem.Len() > 0, "project_configured", projectItem.Len() > 0, "enabled", enabled))
	}
	projectCount := 0
	for _, k := range projectProviders.Keys() {
		if pyjson.Truthy(projectProviders.Value(k)) {
			projectCount++
		}
	}
	return pyjson.NewObject("presets", presetsOut, "providers", providersPublic,
		"personal_configured", personalProviders.Len() > 0, "project_configured", projectCount > 0), nil
}

// agentConfigFor is App.agent_config_for.
func (c *reqCtx) agentConfigFor(username string) (*pyjson.Object, error) {
	if username == "" {
		username = c.actor()
	}
	return c.resolveAgents(c.projectSummaryForSettings(), username)
}

// effectiveAgentsPublic is App.effective_agents_public.
func (c *reqCtx) effectiveAgentsPublic() (*pyjson.Object, error) {
	resolved, err := c.agentConfigFor("")
	if err != nil {
		return nil, err
	}
	byID := map[string]*pyjson.Object{}
	func() {
		defer func() { _ = recover() }()
		for _, x := range objectList(c.app.Providers.Snapshot(false).Value("providers")) {
			byID[pyjson.Str(x.Value("id"))] = x
		}
	}()
	out := []any{}
	for _, item := range objectList(resolved.Value("providers")) {
		provider := item.Copy()
		cat := byID[pyjson.Str(provider.Value("id"))]
		if cat == nil {
			cat = pyjson.NewObject()
		}
		credential := objectOf(cat.Value("auth"))
		state := credential.Value("state")
		provider.Set("credential_state", orAny(state, "unknown"))
		provider.Set("credential_sources", orAny(credential.Value("sources"), []any{}))
		provider.Set("installed", cat.Value("installed"))
		provider.Set("hint", orAny(cat.Value("detail"), ""))
		needs := state == "missing" || state == "unauthenticated"
		provider.Set("personal_config_required", needs && !pyjson.Truthy(provider.Value("overridden")) &&
			!pyjson.Truthy(provider.Value("project_configured")) && !pyjson.Truthy(provider.Value("env_names")))
		out = append(out, provider)
	}
	return pyjson.NewObject("providers", out,
		"personal_configured", orAny(resolved.Value("personal_configured"), false),
		"project_configured", orAny(resolved.Value("project_configured"), false),
		"personal_config_required", len(out) > 0 && !pyjson.Truthy(resolved.Value("personal_configured")) &&
			!pyjson.Truthy(resolved.Value("project_configured"))), nil
}

func orAny(v, def any) any {
	if pyjson.Truthy(v) {
		return v
	}
	return def
}

func objectList(v any) []*pyjson.Object {
	list, _ := v.([]any)
	var out []*pyjson.Object
	for _, x := range list {
		if o, ok := x.(*pyjson.Object); ok {
			out = append(out, o)
		}
	}
	return out
}

// sessionHooks are the request-bound SessionManager providers.
func (c *reqCtx) sessionHooks() *sessions.Hooks {
	return &sessions.Hooks{
		AgentToken: c.app.sessionAgentToken,
		WakesIdle: func(projectID string) bool {
			// The nudger wakes every session ARK launched (T-0109).
			return c.app.nudgeProject(c.ctx, projectID) != nil
		},
		Project: func() (*pyjson.Object, error) {
			p, err := c.require("read")
			if err != nil {
				return nil, err
			}
			return p.Summary()
		},
		Context: func() (*pyjson.Object, error) {
			p, err := c.require("read")
			if err != nil {
				return nil, err
			}
			return c.taskContext(p, nil)
		},
		Unit: func(uid string) (*pyjson.Object, error) {
			p, err := c.require("read")
			if err != nil {
				return nil, err
			}
			return p.Store.GetUnit(c.ctx, uid)
		},
		Worktree: func(ref string) (*pyjson.Object, error) {
			target, err := c.worktreeManager().ResolveSessionTarget(ref)
			if err != nil {
				if we, ok := asWorktreeError(err); ok {
					return nil, &sessions.Error{Message: we.Message, Status: we.Status, Code: we.Code}
				}
				var api *ApiError
				if errors.As(err, &api) {
					return nil, &sessions.Error{Message: api.Message, Status: api.Status, Code: api.Code}
				}
			}
			return target, err
		},
		StartPoint: c.sessionStartPoint,
		TaskFolder: func(in sessions.FolderInput) *sessionstart.Folder {
			p, err := c.require("read")
			if err != nil || p == nil {
				return nil
			}
			return c.sessionFolder(p, in.Actor, in.TaskRef)
		},
		CoreRules: func(projectID, actor string) string {
			p, err := c.require("read")
			if err != nil || p.ID() != projectID {
				return ""
			}
			return c.sessionCoreRules(p, actor)
		},
		AgentConfig:  func(actor string) (*pyjson.Object, error) { return c.agentConfigFor(actor) },
		AssistantKey: func(actor string) (string, error) { return c.assistantKeyFor(actor) },
		LinuxAccount: func(actor string) string {
			if actor == "" {
				actor = c.actor()
			}
			if actor == "" {
				return ""
			}
			name, err := c.app.Org.SelectedLinuxAccount(c.ctx, actor)
			if err != nil {
				return ""
			}
			return name
		},
	}
}

// sessionProject is Handler._session_project.
func (c *reqCtx) sessionProject(id string) (*project.Project, error) {
	s, err := c.app.Sessions.Get(id)
	if err != nil {
		return nil, nil
	}
	pid := c.app.Sessions.ProjectID(s)
	if pid == "" {
		return nil, nil
	}
	return c.projectByID(pid)
}

var errNotAccessible = apiErr(404, "Session not found or not accessible.", "SESSION_NOT_ACCESSIBLE")

// guardSessionAccess is Handler._guard_session_access.
func (c *reqCtx) guardSessionAccess(id, level string) error {
	p, err := c.sessionProject(id)
	if err != nil {
		return err
	}
	current, err := c.currentProject()
	if err != nil {
		return err
	}
	if p == nil || current == nil || p.ID() != current.ID() {
		return errNotAccessible
	}
	if c.app.Auth != nil {
		if err := c.checkProjectAccess(p, level); err != nil {
			return err
		}
	}
	s, err := c.app.Sessions.Get(id)
	if err != nil {
		return err
	}
	owner, shared := c.app.Sessions.Owner(s)
	user, admin, err := c.sessionViewer()
	if err != nil {
		return err
	}
	if !sessions.CanView(owner, shared, user, admin) {
		return errNotAccessible
	}
	if level != "read" && !sessions.CanManage(owner, user, admin) {
		return apiErr(403, "Only the session's creator or a project admin can change it.", "SESSION_NOT_OWNER")
	}
	return nil
}

// filterSessionListing is Handler._filter_session_listing.
func (c *reqCtx) filterSessionListing(data *pyjson.Object) (*pyjson.Object, error) {
	allowed, err := c.accessibleSessionIDs()
	if err != nil {
		return nil, err
	}
	payload := data.Copy()
	items := []any{}
	counts := pyjson.NewObject()
	for _, item := range objectList(data.Value("sessions")) {
		if !allowed[strOrEmpty(item.Value("id"))] {
			continue
		}
		items = append(items, item)
		key := strOrEmpty(item.Value("status"))
		n, _ := counts.Value(key).(int64)
		counts.Set(key, n+1)
	}
	return payload.Set("sessions", items).Set("counts", counts).Set("total", int64(len(items))), nil
}

// filterSessionGroupsFull is Handler._filter_session_groups (whole reply).
func (c *reqCtx) filterSessionGroupsFull(data *pyjson.Object) (*pyjson.Object, error) {
	groups, err := c.filterSessionGroups(data)
	if err != nil {
		return nil, err
	}
	counts := pyjson.NewObject()
	for _, g := range groups {
		key := strOrEmpty(g.(*pyjson.Object).Value("status"))
		n, _ := counts.Value(key).(int64)
		counts.Set(key, n+1)
	}
	return pyjson.NewObject("groups", groups, "counts", counts, "active", int64(len(groups))), nil
}

func (c *reqCtx) okErr(o *pyjson.Object, err error) error {
	if err != nil {
		return err
	}
	c.ok(o)
	return nil
}

// sessionsGet is Handler._sessions_get.
func (c *reqCtx) sessionsGet(path string) error {
	m := c.app.Sessions
	switch path {
	case "/api/agent-sessions/presets":
		presets := m.PresetsPublic(c.sessionHooks(), c.actor())
		eff, err := c.effectiveAgentsPublic()
		if err != nil {
			return uncaughtOrg(err) // OrgError uncaught in Python: 500 "Internal error: ..."
		}
		list := make([]any, len(presets))
		for i, p := range presets {
			list[i] = p
		}
		c.ok(pyjson.NewObject("presets", list, "personal_config_required", pyjson.Truthy(eff.Value("personal_config_required"))))
		return nil
	case "/api/agent-sessions/groups":
		return c.okErr(c.filterSessionGroupsFull(m.Groups()))
	case "/api/agent-sessions":
		return c.okErr(c.filterSessionListing(m.List()))
	}
	guard := func(id string) error { return c.guardSessionAccess(id, "read") }
	if g := reAgentSession.FindStringSubmatch(path); g != nil {
		if err := guard(g[1]); err != nil {
			return err
		}
		s, err := m.Get(g[1])
		if err != nil {
			return err
		}
		c.ok(pyjson.NewObject("session", m.Public(s, true)))
		return nil
	}
	if g := reSessLogs.FindStringSubmatch(path); g != nil {
		if err := guard(g[1]); err != nil {
			return err
		}
		stream := c.query.First("stream", "stdout")
		return c.okErr(m.Logs(g[1], stream, int(c.qInt("offset", 0, 0, 10_000_000)), int(c.qInt("limit", 200, 1, 500))))
	}
	if g := reSessTranscript.FindStringSubmatch(path); g != nil {
		if err := guard(g[1]); err != nil {
			return err
		}
		tail, _ := strconv.Atoi(c.query.First("tail", "0"))
		image, _ := strconv.Atoi(c.query.First("image", "0"))
		opts := sessions.TranscriptOptions{Tail: tail, Known: c.query.First("known", ""), Item: c.query.First("item", ""), Image: image}
		if agent := c.query.First("agent", ""); agent != "" {
			return c.okErr(m.AgentTranscript(g[1], agent, opts))
		}
		return c.okErr(m.TranscriptPage(g[1], opts))
	}
	if g := reSessApprovals.FindStringSubmatch(path); g != nil {
		if err := guard(g[1]); err != nil {
			return err
		}
		return c.okErr(m.Approvals(g[1]))
	}
	if g := reSessMedia.FindStringSubmatch(path); g != nil {
		if err := guard(g[1]); err != nil {
			return err
		}
		return c.okErr(m.WorkspaceIndex(g[1]))
	}
	if g := reSessRead.FindStringSubmatch(path); g != nil {
		if err := guard(g[1]); err != nil {
			return err
		}
		return c.okErr(m.ReadArtifact(g[1], c.query.First("path", "")))
	}
	if g := reSessDownload.FindStringSubmatch(path); g != nil {
		if err := guard(g[1]); err != nil {
			return err
		}
		target, ctype, name, err := m.ArtifactDownload(g[1], c.query.First("path", ""))
		if err != nil {
			return err
		}
		data, err := os.ReadFile(target)
		if err != nil {
			return err
		}
		safe := reDispositionBad.ReplaceAllString(name, "")
		c.send(200, data, ctype, headerList{{"Content-Disposition", `attachment; filename="` + safe + `"`}}, true)
		return nil
	}
	if g := reSessArtifacts.FindStringSubmatch(path); g != nil {
		if err := guard(g[1]); err != nil {
			return err
		}
		return c.okErr(m.ListArtifacts(g[1], c.query.First("path", "")))
	}
	return apiErr(404, "Unknown endpoint: "+path, "")
}

func (c *reqCtx) hostBaseURL() string {
	if host := c.r.Host; host != "" {
		return "http://" + host
	}
	return ""
}

func bodyText(body *pyjson.Object, keys ...string) string {
	for _, k := range keys {
		if v := body.Value(k); pyjson.Truthy(v) {
			return pyjson.Str(v)
		}
	}
	return ""
}

// sessionsPost answers the agent-session branches of do_POST (Python order).
func (c *reqCtx) sessionsPost(path string, body *pyjson.Object, actor string) (bool, error) {
	m := c.app.Sessions
	h := c.sessionHooks()
	if path == "/api/agent-sessions" {
		p, err := c.require("read")
		if err != nil {
			return true, err
		}
		if p.ID() == demoID {
			return true, apiErr(403, "Coding agents are disabled in the demo project. Chat and Assistant are still available.", "DEMO_AGENTS_DISABLED")
		}
		if strOrEmpty(body.Value("access_mode")) == "full_access" && c.app.Blackbox != nil {
			func() {
				defer func() { _ = recover() }()
				_, _ = c.app.Blackbox.Snapshot(c.ctx, bbProject{c.app, p}, "pre-agent", false)
			}()
		}
		s, err := m.Start(h, body, c.hostBaseURL(), c.actor())
		if err != nil {
			return true, err
		}
		c.ok(pyjson.NewObject("session", m.Public(s, true)))
		return true, nil
	}
	write := func(id string) error { return c.guardSessionAccess(id, "write") }
	if g := reSessInput.FindStringSubmatch(path); g != nil {
		if err := write(g[1]); err != nil {
			return true, err
		}
		s, err := m.SendInput(h, g[1], bodyText(body, "text", "message"), body.Value("context"))
		if err != nil {
			return true, err
		}
		c.ok(pyjson.NewObject("session", s, "accepted", true))
		return true, nil
	}
	if g := reSessContinue.FindStringSubmatch(path); g != nil {
		if err := write(g[1]); err != nil {
			return true, err
		}
		s, err := m.ContinueSession(h, g[1], bodyText(body, "text", "message"), sessions.ContinueOptions{
			Platform: body.Value("platform"), Model: body.Value("model"), Effort: body.Value("effort"),
			AccessMode: body.Value("access_mode"), Context: body.Value("context"), Actor: c.actor()})
		if err != nil {
			return true, err
		}
		c.ok(pyjson.NewObject("session", s, "continued", true))
		return true, nil
	}
	if g := reSessSteerPost.FindStringSubmatch(path); g != nil {
		if err := write(g[1]); err != nil {
			return true, err
		}
		res, err := m.Steer(h, g[1], bodyText(body, "text", "message"), body.Value("context"))
		if err != nil || !pyjson.Truthy(body.Value("now")) {
			return true, c.okErr(res, err)
		}
		// Ctrl+Enter: the new message skips the queue.
		steer, _ := res.Value("steer").(*pyjson.Object)
		if steer == nil || steer.Value("status") != "queued" {
			return true, c.okErr(res, nil)
		}
		return true, c.okErr(m.SendSteerNow(h, g[1], pyjson.Str(steer.Value("id")), c.actor()))
	}
	if g := reSessReorder.FindStringSubmatch(path); g != nil {
		if err := write(g[1]); err != nil {
			return true, err
		}
		s, err := m.ReorderSteer(g[1], body.Value("order"))
		if err != nil {
			return true, err
		}
		c.ok(pyjson.NewObject("session", s))
		return true, nil
	}
	if g := reSessDecide.FindStringSubmatch(path); g != nil {
		if err := c.rejectTaskReadToken("decide approvals"); err != nil {
			return true, err
		}
		if err := write(g[1]); err != nil {
			return true, err
		}
		return true, c.okErr(m.DecideApproval(g[1], g[2], bodyText(body, "decision", "action"), bodyText(body, "message"), actor))
	}
	if g := reSessFork.FindStringSubmatch(path); g != nil {
		if err := write(g[1]); err != nil {
			return true, err
		}
		s, err := m.ForkSession(h, g[1], body, c.hostBaseURL(), c.actor())
		if err != nil {
			return true, err
		}
		relation := m.MetaValue(s, "relation")
		if !pyjson.Truthy(relation) {
			relation = "side_chat"
		}
		c.ok(pyjson.NewObject("session", m.Public(s, true), "forked_from", g[1], "relation", relation))
		return true, nil
	}
	if g := reSessValidate.FindStringSubmatch(path); g != nil {
		if err := c.guardSessionAccess(g[1], "read"); err != nil {
			return true, err
		}
		attachments := body.Value("context")
		if !pyjson.Truthy(attachments) {
			attachments = body.Value("attachments")
		}
		if !pyjson.Truthy(attachments) {
			attachments = []any{}
		}
		return true, c.okErr(m.ValidateContext(h, g[1], attachments))
	}
	if g := reSteer.FindStringSubmatch(path); g != nil {
		if err := write(g[1]); err != nil {
			return true, err
		}
		if strOrEmpty(body.Value("action")) == "send_now" {
			return true, c.okErr(m.SendSteerNow(h, g[1], g[2], c.actor()))
		}
		s, err := m.RemoveSteer(g[1], g[2])
		if err != nil {
			return true, err
		}
		c.ok(pyjson.NewObject("session", s, "removed", true))
		return true, nil
	}
	if g := reSessStop.FindStringSubmatch(path); g != nil {
		if err := write(g[1]); err != nil {
			return true, err
		}
		s, err := m.StopByUser(g[1])
		if err != nil {
			return true, err
		}
		c.ok(pyjson.NewObject("session", s))
		return true, nil
	}
	if g := reSessArchive.FindStringSubmatch(path); g != nil {
		if err := write(g[1]); err != nil {
			return true, err
		}
		archive := m.Archive
		if pyjson.Truthy(body.Value("end")) {
			archive = m.EndAndArchive // remove from the list, keep the transcript: end it first
		}
		s, err := archive(g[1])
		if err != nil {
			return true, err
		}
		c.ok(pyjson.NewObject("session", s, "archived", true))
		return true, nil
	}
	if g := reSessRelaunch.FindStringSubmatch(path); g != nil {
		if err := write(g[1]); err != nil {
			return true, err
		}
		s, err := m.Relaunch(h, g[1], c.hostBaseURL(), c.actor())
		if err != nil {
			return true, err
		}
		c.ok(pyjson.NewObject("session", m.Public(s, true), "relaunched", true))
		return true, nil
	}
	return false, nil
}

// sessionsPatch is the PATCH steer branch of do_PATCH, and the session's
// title (rename: the session's creator or a project admin).
func (c *reqCtx) sessionsPatch(path string, body *pyjson.Object) (bool, error) {
	if g := reAgentSession.FindStringSubmatch(path); g != nil {
		if err := c.guardSessionAccess(g[1], "write"); err != nil {
			return true, err
		}
		if closed, ok := body.Get("closed"); ok {
			// 关闭 / End: stop the process; ARK leaves the session alone.
			if !pyjson.Truthy(closed) {
				s, err := c.app.Sessions.Get(g[1])
				if err != nil {
					return true, err
				}
				c.app.Sessions.Reopen(s)
				c.ok(pyjson.NewObject("session", c.app.Sessions.Public(s, true), "closed", false))
				return true, nil
			}
			s, err := c.app.Sessions.Close(g[1])
			if err != nil {
				return true, err
			}
			c.ok(pyjson.NewObject("session", s, "closed", true))
			return true, nil
		}
		title, ok := body.Get("title")
		if !ok {
			return true, apiErr(400, "title is required", "")
		}
		s, err := c.app.Sessions.Rename(g[1], strOrEmpty(title))
		if err != nil {
			return true, err
		}
		c.ok(pyjson.NewObject("session", s, "renamed", true))
		return true, nil
	}
	g := reSteer.FindStringSubmatch(path)
	if g == nil {
		return false, nil
	}
	if err := c.guardSessionAccess(g[1], "write"); err != nil {
		return true, err
	}
	s, err := c.app.Sessions.UpdateSteer(g[1], g[2], bodyText(body, "text"))
	if err != nil {
		return true, err
	}
	c.ok(pyjson.NewObject("session", s, "updated", true))
	return true, nil
}

// sessionsDelete is the DELETE session / steer branches of do_DELETE.
func (c *reqCtx) sessionsDelete(path string) (bool, error) {
	if g := reAgentSession.FindStringSubmatch(path); g != nil {
		if err := c.guardSessionAccess(g[1], "write"); err != nil {
			return true, err
		}
		if c.query.First("end", "") == "1" {
			// Delete from the session menu: end a running session first.
			return true, c.okErr(c.app.Sessions.EndAndDelete(g[1]))
		}
		return true, c.okErr(c.app.Sessions.Delete(g[1]))
	}
	if g := reSteer.FindStringSubmatch(path); g != nil {
		if err := c.guardSessionAccess(g[1], "write"); err != nil {
			return true, err
		}
		s, err := c.app.Sessions.RemoveSteer(g[1], g[2])
		if err != nil {
			return true, err
		}
		c.ok(pyjson.NewObject("session", s, "removed", true))
		return true, nil
	}
	return false, nil
}

func isSessionErr(err error) bool { _, ok := asSessionError(err); return ok }
