package server

import (
	gpuapi "arkstudio/go/internal/api/gpu"
	"os"
	"regexp"
	"strings"

	"arkstudio/go/internal/platform/clock"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/compat/pyurl"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/sessions"
	"arkstudio/go/internal/settings"
	"arkstudio/go/internal/store"
)

// viewsPorted are the contract routes this file answers (router.go registers them).
var viewsPorted = []PortedRoute{
	{"exact:/api/agent/token", "GET,HEAD"},
	{"exact:/api/agent/v1/context", "GET,HEAD"},
	{"exact:/api/agent/v1/export", "GET,HEAD"},
	{"exact:/api/agent/v1/insights", "GET,HEAD"},
	{"exact:/api/agent/v1/interface", "GET,HEAD"},
	{"exact:/api/agent/v1/project/palette", "GET,HEAD"},
	{"exact:/api/agent/v1/setup", "GET,HEAD"},
	{"exact:/api/agent/v1/skills", "GET,HEAD"},
	{"exact:/api/agent/v1/units", "GET,HEAD"},
	{"exact:/api/agent/v1/whoami", "GET,HEAD"},
	{"exact:/api/assistant/insights", "GET,HEAD"},
	{"exact:/api/bootstrap", "GET,HEAD"},
	{"exact:/api/export", "GET,HEAD"},
	{"exact:/api/me", "GET,HEAD"},
	{"exact:/api/org", "GET,HEAD"},
	{"exact:/api/org/", "GET,HEAD"},
	{"exact:/api/org/audit", "GET,HEAD"},
	{"exact:/api/org/teams", "GET,HEAD"},
	{"exact:/api/org/users", "GET,HEAD"},
	{"exact:/api/project/assignees", "GET,HEAD"},
	{"exact:/api/project/overview", "GET,HEAD"},
	{"exact:/api/project/settings", "GET,HEAD"},
	{"exact:/api/projects", "GET,HEAD"},
	{"exact:/api/skills", "GET,HEAD"},
	{"exact:/api/skills/interface", "GET,HEAD"},
	{"exact:/api/skills/setup/state", "GET,HEAD"},
	{"exact:/api/task-agent/v1/context", "GET,HEAD"},
	{"exact:/api/task-agent/v1/links", "GET,HEAD"},
	{"exact:/api/task-agent/v1/project", "GET,HEAD"},
	{"exact:/api/task-agent/v1/snapshot", "GET,HEAD"},
	{"exact:/api/task-agent/v1/units", "GET,HEAD"},
	{"exact:/api/topics", "GET,HEAD"},
	{`fullmatch:/api/agent/v1/media/(\d+)/raw`, "GET,HEAD"},
	{"fullmatch:/api/agent/v1/units/([^/]+)/history(?:/([^/]+))?", "GET,HEAD"},
	{"prefix:/api/agent/v1/skills/", "GET,HEAD"},
	{"regex:^/api/agent/v1/units/([^/]+)$", "GET,HEAD"},
	{"regex:^/api/agent/v1/units/([^/]+)/comments$", "GET,HEAD"},
	{"regex:^/api/agent/v1/units/([^/]+)/members$", "GET,HEAD"},
	{`regex:^/api/media/(\d+)/raw$`, "GET,HEAD"},
	{"regex:^/api/org/projects/([^/]+)$", "GET,HEAD"},
	{"regex:^/api/org/users/([^/]+)/linux-accounts$", "GET,HEAD"},
	{"regex:^/api/skills/([^/]+)$", "GET,HEAD"},
	{"regex:^/api/skills/([^/]+)/(prompt|agent)$", "GET,HEAD"},
	{"regex:^/api/task-agent/v1/units/([^/]+)$", "GET,HEAD"},
	{"regex:^/api/task-agent/v1/units/([^/]+)/comments$", "GET,HEAD"},
	{"regex:^/api/units/([^/]+)/members$", "GET,HEAD"},
	{"regex:^/api/users/([^/]+)/avatar$", "GET,HEAD"},
}

var (
	reAvatar        = pyRe(`^/api/users/([^/]+)/avatar$`, false)
	reMembers       = pyRe(`^/api/units/([^/]+)/members$`, false)
	reMediaRaw      = pyRe(`^/api/media/(\d+)/raw$`, false)
	reOrgLinux      = pyRe(`^/api/org/users/([^/]+)/linux-accounts$`, false)
	reOrgProject    = pyRe(`^/api/org/projects/([^/]+)$`, false)
	reAgentHistory  = pyRe(`/api/agent/v1/units/([^/]+)/history(?:/([^/]+))?`, true)
	reAgentUnit     = pyRe(`^/api/agent/v1/units/([^/]+)$`, false)
	reAgentComments = pyRe(`^/api/agent/v1/units/([^/]+)/comments$`, false)
	reAgentMembers  = pyRe(`^/api/agent/v1/units/([^/]+)/members$`, false)
	reAgentMedia    = pyRe(`/api/agent/v1/media/(\d+)/raw`, true)
	reTaskUnit      = pyRe(`^/api/task-agent/v1/units/([^/]+)$`, false)
	reTaskComments  = pyRe(`^/api/task-agent/v1/units/([^/]+)/comments$`, false)
	exportNameRe    = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
)

func orgErr(err error) error {
	if oe, ok := err.(*org.Error); ok {
		return apiErr(oe.Status, oe.Message, oe.Code)
	}
	return err
}

// blankTemplate reports a project created from the blank template.  Its
// topic list starts empty: GET /api/topics does not create the built-in
// topics there (Go-only; Python's ensure_default_topics runs for every
// project).  Paper, demo and tutorial projects, and projects whose manifest
// names no template, keep the lazy built-in topics; topics a project
// already has are never removed.
func blankTemplate(p *project.Project) bool {
	return p.Manifest.Get("template") == "blank" && !pyjson.Truthy(p.Manifest.Get("demo"))
}

// apiGetViews handles the ported read-only views; ok=false falls through.
func (c *reqCtx) apiGetViews() (bool, error) {
	path := c.path
	if handled, err := c.handleProfile(); handled || err != nil {
		return true, err
	}
	switch path {
	case "/api/bootstrap":
		data, err := c.bootstrap()
		if err == nil {
			c.ok(data)
		}
		return true, err
	case "/api/projects":
		projects, err := c.visibleProjects()
		if err != nil {
			return true, err
		}
		if s := c.mobileSession; s != nil {
			// A paired phone is signed in as its user: the web app's project
			// list (Android Web mode) offers every project the account can
			// read, like the native switcher, except the tutorial project,
			// which a phone cannot open. A read-only device reads them all.
			// A desktop link only ever sees its current project.
			link := c.app.Mobile.IsDesktopLink(s)
			current, err := c.currentProject()
			if err != nil {
				return true, err
			}
			kept := []any{}
			for _, item := range projects {
				o := item.(*pyjson.Object)
				id := o.Value("id")
				if id == demoID || (link && (current == nil || id != current.ID())) {
					continue
				}
				if s.Permission == "read_only" && o.Has("permission") {
					o.Set("permission", "read")
				}
				kept = append(kept, o)
			}
			projects = kept
		}
		// Go-only: the person's own activity per project (project_activity.go).
		c.withMyActivity(projects)
		c.withProjectPeople(projects) // project_people.go
		c.withNewReviews(projects)    // project_reviews.go
		session, err := c.sessionPayload()
		if err == nil {
			c.ok(pyjson.NewObject("projects", projects, "session", session))
		}
		return true, err
	case "/api/org", "/api/org/":
		if c.mobileSession != nil {
			data, err := c.mobileOrgOverview()
			if err == nil {
				c.ok(data)
			}
			return true, err
		}
		data, err := c.app.Org.OrgOverview(c.ctx, c.actor())
		if err == nil {
			c.ok(data)
		}
		return true, err
	case "/api/org/users":
		if err := c.app.Org.RequireOrgAdmin(c.ctx, c.actor()); err != nil {
			return true, orgErr(err)
		}
		users, err := c.app.Org.ListMembers(c.ctx)
		if err != nil {
			return true, err
		}
		linux, err := c.app.Org.LinuxAccountsMap(c.ctx)
		if err == nil {
			c.ok(pyjson.NewObject("users", users, "linux_accounts", linux))
		}
		return true, err
	case "/api/org/teams":
		teams, err := c.app.Org.ListTeams(c.ctx)
		if err == nil {
			c.ok(pyjson.NewObject("teams", teams))
		}
		return true, err
	case "/api/org/audit":
		if err := c.app.Org.RequireOrgAdmin(c.ctx, c.actor()); err != nil {
			return true, orgErr(err)
		}
		audit, err := c.app.Org.ListAudit(c.ctx, c.qInt("limit", 100, 1, 500))
		if err == nil {
			c.ok(pyjson.NewObject("audit", audit))
		}
		return true, err
	case "/api/project/overview":
		p, err := c.require("read")
		if err != nil {
			return true, err
		}
		data, err := c.projectOverview(p)
		if err == nil {
			c.overviewReply(data)
		}
		return true, err
	case "/api/project/assignees":
		p, err := c.require("read")
		if err != nil {
			return true, err
		}
		data, err := c.assignableUsers(p)
		if err == nil {
			c.ok(data)
		}
		return true, err
	case "/api/project/settings":
		p, err := c.require("read")
		if err != nil {
			return true, err
		}
		s, err := settings.ProjectSettings(p.Manifest.Snapshot())
		if err == nil {
			c.ok(pyjson.NewObject("settings", s))
		}
		return true, err
	case "/api/topics":
		p, err := c.require("read")
		if err != nil {
			return true, err
		}
		if perm, err := c.projectPermission(); err != nil {
			return true, err
		} else if (perm == "write" || perm == "admin") && !blankTemplate(p) {
			if _, err := p.Store.EnsureDefaultTopics(c.ctx, c.who(p)); err != nil {
				return true, err
			}
		}
		data, err := p.Store.Topics(c.ctx)
		if err == nil {
			c.ok(data)
		}
		return true, err
	case "/api/assistant/insights":
		p, err := c.require("read")
		if err != nil {
			return true, err
		}
		items, err := p.Store.Insights(c.ctx)
		if err == nil {
			c.ok(pyjson.NewObject("insights", items))
		}
		return true, err
	case "/api/agent/token":
		if c.app.Auth != nil {
			if _, err := c.require("admin"); err != nil {
				return true, err
			}
		}
		c.ok(pyjson.NewObject("token", c.app.AgentToken()))
		return true, nil
	case "/api/skills":
		c.okEncoded([]byte(staticJSON["skills_catalog"]))
		return true, nil
	case "/api/export":
		return true, c.export()
	}
	if m := reOrgLinux.FindStringSubmatch(path); m != nil {
		if err := c.app.Org.RequireOrgAdmin(c.ctx, c.actor()); err != nil {
			return true, orgErr(err)
		}
		target, ok, err := c.app.Org.UserByID(c.ctx, m[1])
		if err != nil {
			return true, err
		}
		if !ok {
			return true, apiErr(404, "user not found", "USER_NOT_FOUND")
		}
		accounts, err := c.app.Org.LinuxAccounts(c.ctx, target.Str("id"))
		if err == nil {
			c.ok(pyjson.NewObject("user", org.UserPublic(target, nil), "linux_accounts", accounts, "capability", linuxCapability()))
		}
		return true, err
	}
	if m := reOrgProject.FindStringSubmatch(path); m != nil {
		data, err := c.app.Org.ProjectDetail(c.ctx, c.actor(), m[1])
		if err == nil {
			c.ok(pyjson.NewObject("project", data))
		}
		return true, orgErr(err)
	}
	if strings.HasPrefix(path, "/api/skills/") {
		return true, c.skillsGet()
	}
	if strings.HasPrefix(path, "/api/agent/v1/") {
		if err := c.requireAgent(); err != nil {
			return true, err
		}
		return c.agentGet()
	}
	if strings.HasPrefix(path, "/api/task-agent/v1/") {
		s, err := c.requireTaskAgent()
		if err != nil {
			return true, err
		}
		return c.taskAgentGet(s)
	}
	if m := reMembers.FindStringSubmatch(path); m != nil {
		p, err := c.require("read")
		if err != nil {
			return true, err
		}
		data, err := p.Store.Topic(c.ctx, m[1], c.qTrue("full"))
		if err == nil {
			c.ok(data)
		}
		return true, err
	}
	if m := reMediaRaw.FindStringSubmatch(path); m != nil {
		return true, c.mediaRaw(m[1])
	}
	return false, nil
}

func linuxCapability() *pyjson.Object {
	sudo := strings.TrimSpace(os.Getenv("ARK_SUDO_BINARY"))
	if sudo == "" {
		sudo = lookPath("sudo")
	}
	uidMin := int64(1000)
	if v := os.Getenv("ARK_LINUX_UID_MIN"); v != "" {
		if n, err := pytext.Int(v); err == nil {
			uidMin = n
		}
	}
	platform := map[string]string{"windows": "win32", "linux": "linux", "darwin": "darwin"}[goos]
	linux := goos == "linux"
	hint, _ := pyjson.Loads(staticJSON["sudo_hint"])
	return pyjson.NewObject("platform", platform, "available", linux && sudo != "", "linux", linux,
		"sudo", sudo, "uid_min", max(0, uidMin), "hint", hint)
}

// handleProfile is Handler._handle_profile for GET/HEAD.
func (c *reqCtx) handleProfile() (bool, error) {
	path := c.path
	avatar := reAvatar.FindStringSubmatch(path)
	if path != "/api/me" && path != "/api/me/avatar" && path != "/api/me/password" && avatar == nil {
		return false, nil
	}
	if err := c.rejectTaskReadToken("read or change account profiles"); err != nil {
		return true, err
	}
	if c.actor() == "" {
		return true, apiErr(401, "Sign in with the ARK account to continue.", "AUTH_REQUIRED")
	}
	if avatar != nil {
		file, ctype, ok, err := c.app.Org.AvatarFile(c.ctx, pyurl.Unquote(avatar[1]))
		if err != nil {
			return true, err
		}
		if !ok {
			return true, apiErr(404, "no avatar image", "AVATAR_NOT_FOUND")
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return true, err
		}
		cache := "private, no-cache"
		if strings.Contains(pyurl.Parse(c.target).Query, "v=") {
			cache = "private, max-age=31536000, immutable"
		}
		c.send(200, data, ctype, headerList{{"Cache-Control", cache}, {"Content-Disposition", `inline; filename="avatar"`},
			{"Content-Security-Policy", "default-src 'none'; sandbox"}}, true)
		return true, nil
	}
	if path == "/api/me" {
		data, err := c.profilePayload()
		if err == nil {
			c.ok(data)
		}
		return true, orgErr(err)
	}
	return true, apiErr(405, "Method not allowed", "")
}

// export is the /api/export branch of Handler._api_get.
func (c *reqCtx) export() error {
	format := strings.ToLower(c.query.First("format", "json"))
	p, err := c.require("read")
	if err != nil {
		return err
	}
	if wanted := pytext.Strip(c.query.First("project", "")); wanted != "" && wanted != p.ID() {
		other, err := c.projectByID(wanted)
		if err != nil {
			return err
		}
		if other == nil {
			return apiErr(404, "Project not found or not accessible for this account.", "PROJECT_NOT_ACCESSIBLE")
		}
		if c.app.Auth != nil {
			if err := c.checkProjectAccess(other, "read"); err != nil {
				return err
			}
		}
		p = other
	}
	name := strings.Trim(exportNameRe.ReplaceAllString(p.Name(), "-"), "-")
	if name == "" {
		name = "ark-project"
	}
	attach := func(file string) headerList {
		return headerList{{"Content-Disposition", `attachment; filename="` + file + `"`}}
	}
	switch format {
	case "bundle":
		return c.sendMarkdownBundle(p)
	case "project":
		return c.sendProjectArchive(p, name)
	case "json":
		data, err := p.Store.ProjectJSON(c.ctx)
		if err == nil {
			c.send(200, pyjson.DumpsIndent(data, 2), "application/json; charset=utf-8", attach(name+".json"), true)
		}
		return err
	case "markdown":
		text, err := p.Store.ProjectMarkdown(c.ctx)
		if err == nil {
			c.send(200, []byte(text), "text/markdown; charset=utf-8", attach(name+".md"), true)
		}
		return err
	case "csv":
		text, err := p.Store.ProjectCSV(c.ctx)
		if err == nil {
			c.send(200, []byte(text), "text/csv; charset=utf-8", attach(name+".csv"), true)
		}
		return err
	case "svg":
		text, err := p.Store.GraphSVG(c.ctx)
		if err == nil {
			c.send(200, []byte(text), "image/svg+xml; charset=utf-8", attach(name+"-map.svg"), true)
		}
		return err
	}
	return apiErr(400, "format must be json, markdown, csv, svg, bundle or project", "")
}

// skillsGet is Handler._skills_get.
func (c *reqCtx) skillsGet() error {
	rest := strings.Trim(c.path[len("/api/skills/"):], "/")
	if rest == "" {
		c.okEncoded([]byte(staticJSON["skills_catalog"]))
		return nil
	}
	if rest == "interface" {
		c.okEncoded([]byte(staticJSON["skills_interface"]))
		return nil
	}
	parts := strings.Split(rest, "/")
	if len(parts) == 2 && parts[0] == "setup" && parts[1] == "state" {
		return c.setupState()
	}
	if len(parts) == 1 {
		key := "skill:" + parts[0]
		if d := c.query.First("docs", "1"); d == "0" || d == "false" || d == "no" {
			key = "skill_nodocs:" + parts[0]
		}
		raw, ok := staticJSON[key]
		if !ok {
			return apiErr(404, "unknown skill: "+parts[0], "")
		}
		c.ok(pyjson.NewObject("skill", pyjson.Raw(raw)))
		return nil
	}
	if len(parts) == 2 && (parts[1] == "prompt" || parts[1] == "agent") {
		raw, ok := staticJSON["prompt:"+parts[0]]
		if !ok {
			return apiErr(404, "unknown skill: "+parts[0], "")
		}
		c.ok(pyjson.NewObject("skill", parts[0], "prompt", pyjson.Raw(raw)))
		return nil
	}
	return apiErr(404, "Unknown skills endpoint: "+c.path, "")
}

func (c *reqCtx) setupState() error {
	p, err := c.require("read")
	if err != nil {
		return err
	}
	data, err := c.setupPayload(p)
	if err == nil {
		c.ok(data)
	}
	return err
}

func commentsPayload(comments []any) *pyjson.Object {
	open := int64(0)
	for _, cm := range comments {
		o := cm.(*pyjson.Object)
		if o.Value("author_kind") == "user" && o.Value("resolved") == false {
			open++
		}
	}
	return pyjson.NewObject("comments", comments, "total", int64(len(comments)), "open_user_comments", open)
}

// agentGet is Handler._agent_get.
func (c *reqCtx) agentGet() (bool, error) {
	path := c.path
	if path == "/api/agent/v1/whoami" {
		data, err := c.agentWhoami()
		if err == nil {
			c.ok(data)
		}
		return true, err
	}
	if handled, err := c.agentDocsGet(path); handled { // Go only (routes_agentdocs.go)
		return true, err
	}
	p, err := c.require("read")
	if err != nil {
		return true, err
	}
	st := p.Store
	okObj := func(o *pyjson.Object, err error) (bool, error) {
		if err == nil {
			c.ok(o)
		}
		return true, err
	}
	if handled, err := c.agentSessionsGet(path); handled {
		return true, err
	}
	if rest, ok := strings.CutPrefix(path, agentImportArkBase); ok { // Go only (routes_projectmove.go)
		return true, c.agentImportArkRoute(rest, false)
	}
	if rest, ok := strings.CutPrefix(path, agentProjectMoveBase); ok { // Go only (routes_projectmove.go)
		return true, c.projectMoveRoute(rest, false)
	}
	if handled, err := c.agentWatchGet(path, p); handled { // Go only (routes_agentwatch.go)
		return true, err
	}
	if path == agentRulesAgentPath { // Go only (routes_agentrules.go)
		return true, c.agentRulesGet(p)
	}
	if path == promptsAgentPath { // Go only (routes_prompts.go)
		return true, c.agentPromptsGet(p)
	}
	if handled, err := c.agentReviewsGet(p, path); handled { // Go only (routes_reviews.go)
		return true, err
	}
	switch path {
	case "/api/agent/v1/export":
		return true, c.export()
	case "/api/agent/v1/gpu/jobs":
		return true, c.gpuHandlers().AgentGet(c, path, p.ID())
	case "/api/agent/v1/interface":
		c.okEncoded(agentInterfaceEncoded())
		return true, nil
	case "/api/agent/v1/context":
		return okObj(st.AgentContext(c.ctx))
	case "/api/agent/v1/project/palette":
		s, err := settings.ProjectSettings(p.Manifest.Snapshot())
		if err != nil {
			return true, err
		}
		return okObj(pyjson.NewObject("palette", s.Value("palette"), "presets", settings.PresetsObject()), nil)
	case "/api/agent/v1/units":
		f := c.filtersFromQuery()
		f.Limit = c.qInt("limit", 100, 1, 500)
		return okObj(st.ListUnits(c.ctx, f, false))
	case agentMarkersPath: // Go only (routes_agentmarkers.go)
		return okObj(agentMarkers(p), nil)
	case "/api/agent/v1/insights":
		items, err := st.Insights(c.ctx)
		return okObj(pyjson.NewObject("insights", items), err)
	case "/api/agent/v1/skills":
		c.okEncoded([]byte(staticJSON["skills_catalog"]))
		return true, nil
	case "/api/agent/v1/setup":
		return true, c.setupState()
	}
	if m := reAgentMedia.FindStringSubmatch(path); m != nil {
		return true, c.mediaRaw(m[1])
	}
	if gpuapi.IsAgentJobPath(path) {
		return true, c.gpuHandlers().AgentGet(c, path, p.ID())
	}
	if m := reAgentHistory.FindStringSubmatch(path); m != nil {
		return true, c.nodeHistoryGet(m[1], m[2])
	}
	if m := reAgentUnit.FindStringSubmatch(path); m != nil {
		unit, err := st.GetUnit(c.ctx, m[1])
		if err == nil {
			c.presenceRead(p, unit, nil) // routes_presence.go (Go only)
		}
		return okObj(pyjson.NewObject("unit", unit), err)
	}
	if m := reAgentComments.FindStringSubmatch(path); m != nil {
		comments, err := st.Comments(c.ctx, m[1])
		if err != nil {
			return true, err
		}
		return okObj(commentsPayload(comments), nil)
	}
	if m := reAgentMembers.FindStringSubmatch(path); m != nil {
		return okObj(st.Topic(c.ctx, m[1], false))
	}
	if m := reAgentUnitLinks.FindStringSubmatch(path); m != nil { // Go only (routes_agentlinks.go)
		return true, c.agentUnitLinks(st, m[1])
	}
	if strings.HasPrefix(path, "/api/agent/v1/skills/") {
		id := path[strings.LastIndex(path, "/")+1:]
		raw, ok := staticJSON["skill:"+id]
		if !ok {
			return true, apiErr(404, "unknown skill: "+id, "")
		}
		c.okEncoded([]byte(raw))
		return true, nil
	}
	return true, apiErr(404, "Unknown agent endpoint: "+path, "")
}

// agentWhoami is Handler._agent_whoami.
func (c *reqCtx) agentWhoami() (*pyjson.Object, error) {
	current, err := c.currentProject()
	if err != nil {
		return nil, err
	}
	var items []any
	if c.app.Auth != nil {
		if items, err = c.visibleProjects(); err != nil {
			return nil, err
		}
	} else if current != nil {
		summary, err := current.Summary()
		if err != nil {
			return nil, err
		}
		items = []any{summary.Set("permission", "admin")}
	}
	cap := ""
	if c.token != nil {
		cap = pyjson.Str(c.token.row.Get("scope"))
	}
	projects := []any{}
	for _, it := range items {
		item := it.(*pyjson.Object)
		perm := strOr(item.Value("permission"))
		if perm == "" {
			perm = "read"
		}
		if cap != "" {
			rank, ok := org.PermissionRank[perm]
			if !ok {
				rank = 1
			}
			if rank > org.PermissionRank[cap] {
				perm = cap
			}
		}
		name := item.Value("name")
		if !pyjson.Truthy(name) {
			name = item.Value("id")
		}
		projects = append(projects, pyjson.NewObject("id", item.Value("id"), "name", name, "permission", perm,
			"current", current != nil && item.Value("id") == current.ID()))
	}
	actor := c.writeActor()
	if !strings.HasPrefix(actor, "agent:") {
		actor = "agent:api"
	}
	var user, label, tokenID, scope, expires any
	if u := c.actor(); u != "" {
		user = u
	}
	kind := "server"
	label = "Server token (legacy)"
	if c.token != nil {
		kind = "personal"
		label, tokenID, expires = c.token.row.Get("label"), c.token.row.Get("id"), c.token.row.Get("expires_at")
	}
	if cap != "" {
		scope = cap
	}
	return pyjson.NewObject("user", user, "actor", actor, "token_kind", kind, "token_label", label,
		"token_id", tokenID, "scope", scope, "expires_at", expires, "project_header", "X-ARK-Project",
		"projects", projects), nil
}

// taskAgentGet is Handler._task_agent_get.
func (c *reqCtx) taskAgentGet(s *sessions.Session) (bool, error) {
	path := c.path
	p, err := c.require("read")
	if err != nil {
		return true, err
	}
	st := p.Store
	okObj := func(o *pyjson.Object, err error) (bool, error) {
		if err == nil {
			c.ok(o)
		}
		return true, err
	}
	switch path {
	case "/api/task-agent/v1/context":
		return okObj(c.taskContext(p, s))
	case "/api/task-agent/v1/project":
		summary, err := p.Summary()
		if err != nil {
			return true, err
		}
		stats, err := st.Stats(c.ctx)
		return okObj(pyjson.NewObject("project", summary, "stats", stats, "read_only", true), err)
	case "/api/task-agent/v1/units":
		f := c.filtersFromQuery()
		f.Limit = c.qInt("limit", 200, 1, 1000)
		f.Offset = c.qInt("offset", 0, 0, 1_000_000)
		for _, x := range qlist(c.query, "ids") {
			if sx := x.(string); pytext.IsDigit(sx) {
				n, err := store.PyInt(sx)
				if err != nil {
					return true, err
				}
				f.IDs = append(f.IDs, n)
			}
		}
		return okObj(st.ListUnits(c.ctx, f, false))
	case "/api/task-agent/v1/links":
		edges, err := st.GraphEdges(c.ctx)
		return okObj(pyjson.NewObject("links", edges, "total", int64(len(edges)), "read_only", true), err)
	case "/api/task-agent/v1/snapshot":
		snap, ok := sessions.ContextSnapshot(s)
		if !ok {
			return true, apiErr(404, "context snapshot is missing", "")
		}
		return okObj(snap, nil)
	case "/api/task-agent/v1/session":
		return okObj(pyjson.NewObject("session", c.app.Sessions.Public(s, false)), nil)
	}
	if handled, err := c.gpuHandlers().TaskGet(c, path, s); handled {
		return true, err
	}
	if m := reTaskUnit.FindStringSubmatch(path); m != nil {
		unit, err := st.GetUnit(c.ctx, m[1])
		if err == nil {
			c.presenceRead(p, unit, s) // routes_presence.go (Go only)
		}
		return okObj(pyjson.NewObject("unit", unit, "read_only", true), err)
	}
	if m := reTaskComments.FindStringSubmatch(path); m != nil {
		comments, err := st.Comments(c.ctx, m[1])
		if err != nil {
			return true, err
		}
		return okObj(commentsPayload(comments).Set("read_only", true), nil)
	}
	return true, apiErr(404, "Unknown task-agent endpoint: "+path, "")
}

// taskContext is App.task_context(session).
func (c *reqCtx) taskContext(p *project.Project, s *sessions.Session) (*pyjson.Object, error) {
	units, links, stats, err := p.Store.TaskContextUnits(c.ctx)
	if err != nil {
		return nil, err
	}
	summary, err := p.Summary()
	if err != nil {
		return nil, err
	}
	out := pyjson.NewObject("format", "ark-task-agent-context", "version", int64(1), "generated_at", clock.UTCNow(),
		"read_only", true, "project", summary, "stats", stats, "units", units, "links", links)
	if s == nil { // the snapshot written for a new session (App.task_context())
		return out, nil
	}
	worktree, _ := s.Meta.Value("worktree").(*pyjson.Object)
	var wt any
	if worktree != nil && pyjson.Truthy(worktree.Value("path")) {
		wt = pyjson.NewObject("path", worktree.Value("path"), "branch", orStr(worktree.Value("branch")))
	}
	out.Set("session", pyjson.NewObject("id", s.ID, "platform", s.Meta.Value("platform"),
		"task_ref", orStr(s.Meta.Value("task_ref")), "worktree", wt, "read_only", true))
	return out, nil
}

func orStr(v any) any {
	if pyjson.Truthy(v) {
		return v
	}
	return ""
}
