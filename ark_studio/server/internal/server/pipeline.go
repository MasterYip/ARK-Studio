package server

import (
	"fmt"
	"regexp"
	"strings"

	"arkstudio/go/internal/mobile"

	"arkstudio/go/internal/auth"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/compat/pyurl"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/project"
)

var historyRoute = regexp.MustCompile(`\A/api/(?:agent/v1/)?units/[^/]+/history(?:/[^/]+)?\z`)
var mediaRawRoute = regexp.MustCompile(`\A/api/media/\d+/raw\z`)

func set(items ...string) map[string]bool {
	out := map[string]bool{}
	for _, s := range items {
		out[s] = true
	}
	return out
}

// personalAgentPathAllowed is Handler._personal_agent_path_allowed.
func (c *reqCtx) personalAgentPathAllowed(path string) bool {
	if strings.HasPrefix(path, "/api/agent/v1/") || path == "/api/media/upload" || path == "/api/skills/agent-team/tasks" {
		return true
	}
	get := c.r.Method == "GET" || c.r.Method == "HEAD"
	return get && (path == "/api/export" || mediaRawRoute.MatchString(path))
}

// mobileToken is Handler._mobile_token(allow_query=GET/HEAD).
func (c *reqCtx) mobileToken() string {
	if t := pytext.Strip(c.header("X-ARK-Mobile-Token")); t != "" {
		return t
	}
	if c.r.Method == "GET" || c.r.Method == "HEAD" {
		if t := pytext.Strip(c.query.First("mobile_token", "")); t != "" {
			return t
		}
	}
	return auth.ParseCookie(c.header("Cookie"), "ark_mobile")
}

// readToken is Handler._read_token.
func (c *reqCtx) readToken() string {
	h := c.header("Authorization")
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return pytext.Strip(h[7:])
	}
	return c.header("X-ARK-Read-Token")
}

func (c *reqCtx) authSession() (*auth.Session, error) {
	if c.app.Auth == nil {
		return nil, nil
	}
	token := auth.ParseCookie(c.header("Cookie"), auth.CookieName)
	if token == "" {
		return nil, nil
	}
	return c.app.Auth.Authenticate(c.ctx, token)
}

// guardPublicAuth is Handler._guard_public_auth.
func (c *reqCtx) guardPublicAuth() error {
	if bound, err := c.bindPersonalToken(); bound || err != nil {
		return err
	}
	if c.app.Auth == nil {
		return nil
	}
	session, err := c.authSession()
	if err != nil {
		return err
	}
	if session != nil {
		c.session, c.user = session, session.Username
	}
	var ms *mobile.Session
	if session == nil {
		ms = c.mobileSessionForProject()
	}
	if ms != nil {
		// A paired phone acts as the account that created its pairing code
		// and is pinned to the paired project (_guard_mobile narrows it next).
		c.bindMobile(ms.User, ms)
	}
	// The Android update manifest is as public as the APKs (app_manifest.go).
	if publicAuthPaths[c.path] || loginAssets[c.path] || c.path == appManifestPath || session != nil || ms != nil {
		return nil
	}
	if ts := c.taskReadSession(); ts != nil && strings.HasPrefix(c.path, "/api/task-agent/") {
		c.bindTaskSession(ts)
		return nil
	}
	if (c.r.Method == "GET" || c.r.Method == "HEAD") && !strings.HasPrefix(c.path, "/api/") {
		return apiErr(302, c.loginLocation(nil), "")
	}
	return apiErr(401, "Sign in with the ARK account to continue.", "AUTH_REQUIRED")
}

// guardProjectScope is Handler._guard_project_scope.
func (c *reqCtx) guardProjectScope() error {
	path := c.path
	if !strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/api/task-agent/") {
		return nil
	}
	if globalAPIPaths[path] {
		return nil
	}
	// Go-only desktop-link routes check their own access (routes_desktop.go).
	// The notification feed covers the person's projects (notifications.go).
	if path == desktopLinkPath || path == mobile.DesktopProjectsPath || path == notificationsPath {
		return nil
	}
	// The prompt library checks per scope: a person's own prompts and pins
	// need read access, the project's need an admin (routes_prompts.go).
	if path == promptsPath {
		return nil
	}
	// Marking Needs you items seen only writes the person's own marks, so
	// read-only members may do it for their assignments (project_reviews.go).
	if path == needsSeenPath && c.r.Method == "POST" {
		_, err := c.require("read")
		return err
	}
	for _, p := range globalAPIPrefixes {
		if strings.HasPrefix(path, p) {
			return nil
		}
	}
	level := "write"
	if c.r.Method == "GET" || c.r.Method == "HEAD" {
		level = "read"
	}
	if historyRoute.MatchString(path) {
		_, err := c.nodeHistoryStore(level)
		return err
	}
	_, err := c.require(level)
	return err
}

// currentProject is App.current_project (+ _project_for_request).
func (c *reqCtx) currentProject() (*project.Project, error) {
	if c.resolved {
		return c.project, nil
	}
	a := c.app
	if a.Auth == nil {
		a.mu.Lock()
		c.project = a.project
		a.mu.Unlock()
		c.resolved = true
		return c.project, nil
	}
	user := c.actor()
	if user == "" {
		c.resolved = true
		return nil, nil
	}
	if c.forcedProject != "" {
		p, err := c.projectByID(c.forcedProject)
		c.project, c.resolved = p, err == nil
		return p, err
	}
	// The browser tab's X-ARK-Project (a preference, only when readable).
	if c.session != nil {
		tab, err := c.readableProject(c.tabProjectID(), user)
		if err != nil {
			return nil, err
		}
		if tab != nil {
			c.project, c.resolved = tab, true
			return tab, nil
		}
	}
	if c.session != nil {
		a.mu.Lock()
		path := a.sessionProjects[c.session.SidHash]
		a.mu.Unlock()
		if path != "" {
			if p, err := a.Projects.Open(path); err == nil {
				perm := "write"
				if p.ID() != demoID {
					if perm, err = a.Org.ProjectPermission(c.ctx, user, p.ID()); err != nil {
						return nil, err
					}
				}
				if perm != "" {
					c.project, c.resolved = p, true
					return p, nil
				}
			}
			a.mu.Lock()
			delete(a.sessionProjects, c.session.SidHash)
			a.mu.Unlock()
		}
	}
	p, err := c.rememberedProjectFor(user, c.session)
	if err != nil {
		return nil, err
	}
	if p == nil {
		if p, err = c.defaultProjectFor(user); err != nil {
			return nil, err
		}
	}
	if p != nil && c.session != nil {
		a.mu.Lock()
		a.sessionProjects[c.session.SidHash] = p.Path
		a.mu.Unlock()
		// Pin the session (and, the first time, the person) to it, so a
		// restart or a project someone else creates later never changes
		// what this session shows.
		a.Auth.SetSessionProject(c.session, p.ID())
		userID, err := a.Org.UserIDFor(c.ctx, user)
		if err != nil {
			return nil, err
		}
		if userID != "" && p.ID() != demoID {
			last, err := a.Org.LastProject(c.ctx, userID)
			if err != nil {
				return nil, err
			}
			if last == "" {
				if err := a.Org.SetLastProject(c.ctx, userID, p.ID()); err != nil {
					return nil, err
				}
			}
		}
	}
	c.project, c.resolved = p, true
	return p, nil
}

// tabProjectID is Handler._tab_project_header: X-ARK-Project, else the
// ark_project query parameter (URLs a browser loads itself).
func (c *reqCtx) tabProjectID() string {
	if c.tabProjectSet {
		return c.tabProject
	}
	value := c.header("X-ARK-Project")
	if value == "" {
		value = c.query.First("ark_project", "")
	}
	return pytext.Prefix(pytext.Strip(value), 120)
}

// readableProject is App._readable_project.
func (c *reqCtx) readableProject(projectID, user string) (*project.Project, error) {
	if projectID == "" {
		return nil, nil
	}
	p, err := c.projectByID(projectID)
	if err != nil || p == nil {
		return nil, err
	}
	if p.ID() == demoID {
		return p, nil
	}
	perm, err := c.app.Org.ProjectPermission(c.ctx, user, p.ID())
	if err != nil || perm == "" {
		return nil, err
	}
	return p, nil
}

// rememberedProjectFor is App._remembered_project_for: the session's last
// project, else the person's last project.
func (c *reqCtx) rememberedProjectFor(user string, s *auth.Session) (*project.Project, error) {
	var ids []string
	if s != nil {
		ids = append(ids, s.ProjectID)
	} else {
		ids = append(ids, "")
	}
	userID, err := c.app.Org.UserIDFor(c.ctx, user)
	if err != nil {
		return nil, err
	}
	last, err := c.app.Org.LastProject(c.ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, id := range append(ids, last) {
		p, err := c.readableProject(id, user)
		if err != nil {
			return nil, err
		}
		if p != nil {
			return p, nil
		}
	}
	return nil, nil
}

// defaultProjectFor is App._default_project_for (without the tutorial).
func (c *reqCtx) defaultProjectFor(user string) (*project.Project, error) {
	visible, err := c.app.Org.VisibleProjectIDs(c.ctx, user)
	if err != nil {
		return nil, err
	}
	for _, summary := range c.app.Projects.ListProjects() {
		id, _ := summary.Value("id").(string)
		if visible[id] {
			if p, err := c.app.Projects.Open(summary.Value("path").(string)); err == nil {
				return p, nil
			}
		}
	}
	// Every account may open its own copy of the tutorial project.
	p, err := c.demoProject(false)
	if err != nil {
		return nil, nil
	}
	return p, nil
}

// accountName is App.account_name.
func (c *reqCtx) accountName(name string) (string, bool) {
	text := pytext.Strip(name)
	if text == "" {
		return "", false
	}
	if c.app.Auth == nil {
		if strings.ToLower(text) == org.LocalUsername {
			return org.LocalUsername, true
		}
		return "", false
	}
	row, ok, err := c.app.Org.UserByName(c.ctx, text)
	if err != nil || !ok {
		return "", false
	}
	return row.Str("username"), true
}

// require is App.require(level).
func (c *reqCtx) require(level string) (*project.Project, error) {
	p, err := c.currentProject()
	if err != nil {
		return nil, err
	}
	if p != nil {
		if err := p.Store.BackfillCreatorAssignees(c.ctx, c.accountName); err != nil {
			return nil, err
		}
		if err := c.app.adoptLegacyChat(c.ctx, p); err != nil {
			return nil, err
		}
	}
	if p == nil {
		if c.app.Auth != nil {
			return nil, apiErr(409, "No project is available for your account yet. Ask an "+
				"organization admin for a project grant.", "NO_PROJECT_ACCESS")
		}
		return nil, apiErr(409, "No project is open. Create or open one first.", "")
	}
	if c.token != nil {
		scope := pyjson.Str(c.token.row.Get("scope"))
		cap, ok := org.PermissionRank[scope]
		if !ok {
			cap = 1
		}
		if org.PermissionRank[level] > cap {
			return nil, apiErr(403, fmt.Sprintf("This agent token has %s scope; the action needs %s access.", scope, level), "AGENT_TOKEN_SCOPE")
		}
	}
	if c.app.Auth != nil {
		if err := c.checkProjectAccess(p, level); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// checkProjectAccess is App.check_project_access.
func (c *reqCtx) checkProjectAccess(p *project.Project, level string) error {
	user := c.actor()
	if user == "" {
		return apiErr(401, "Sign in with an ARK account to continue.", "AUTH_REQUIRED")
	}
	perm := "write" // the tutorial project
	if p.ID() != demoID {
		var err error
		if perm, err = c.app.Org.ProjectPermission(c.ctx, user, p.ID()); err != nil {
			return err
		}
	}
	if perm == "" {
		return apiErr(404, "Project not found or not accessible for this account.", "PROJECT_NOT_ACCESSIBLE")
	}
	if c.mobileSession != nil && c.mobileSession.Permission == "read_only" {
		perm = "read"
	}
	if org.PermissionRank[perm] < org.PermissionRank[level] {
		return apiErr(403, fmt.Sprintf("This action needs %s access to the project (your access is %s).", level, perm), "PROJECT_FORBIDDEN")
	}
	return nil
}

// loginLocation is Handler._login_location.
func (c *reqCtx) loginLocation(next *string) string {
	prefix := c.externalPrefix()
	target := c.externalPath()
	if next != nil {
		target = *next
	}
	return prefix + "login?next=" + pyurl.Quote(c.cleanAuthNext(target), "")
}

var mobileTokenParam = regexp.MustCompile(`([?&])mobile_token=[^&#]*`)

// cleanAuthNext is Handler._clean_auth_next.  Python's second regex uses a
// lookahead, `[?&]+(?=#|$)`, which RE2 lacks; it is implemented by hand,
// including `$` matching before a final newline.
func (c *reqCtx) cleanAuthNext(raw string) string {
	text := mobileTokenParam.ReplaceAllString(raw, "$1")
	var b strings.Builder
	for i := 0; i < len(text); {
		if text[i] == '?' || text[i] == '&' {
			j := i
			for j < len(text) && (text[j] == '?' || text[j] == '&') {
				j++
			}
			// Python's regex can match a suffix of the run too; the whole run
			// goes whenever its end is followed by #, the end, or a final "\n".
			if j == len(text) || text[j] == '#' || (text[j] == '\n' && j == len(text)-1) {
				i = j
				continue
			}
			b.WriteString(text[i:j])
			i = j
			continue
		}
		b.WriteByte(text[i])
		i++
	}
	return auth.SafeNextPath(b.String(), c.externalPrefix())
}
