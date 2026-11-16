package server

import (
	"regexp"
	"strings"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pytext"
	"arkstudio/go/internal/pyurl"
	"arkstudio/go/internal/store"
)

// contractRoute is one row of docs/development/go-port/contract.json.
type contractRoute struct {
	ID, Kind, Pattern string
	Methods           []string
	Role              string
}

// pyRe compiles a Python re pattern used with re.match/fullmatch on a path:
// Python \d is Unicode, and a trailing `$` also matches before a final "\n".
func pyRe(pattern string, full bool) *regexp.Regexp {
	p := strings.TrimPrefix(pattern, "^")
	if strings.HasSuffix(p, "$") && !strings.HasSuffix(p, `\$`) {
		p = strings.TrimSuffix(p, "$") + `\n?\z`
	} else if full {
		p += `\z`
	}
	return pytext.MustCompile(`\A` + p)
}

type compiledRoute struct {
	contractRoute
	re *regexp.Regexp
}

var compiledContract = func() []compiledRoute {
	out := make([]compiledRoute, 0, len(contractRoutes))
	for _, r := range contractRoutes {
		cr := compiledRoute{contractRoute: r}
		switch r.Kind {
		case "regex":
			cr.re = pyRe(r.Pattern, false)
		case "fullmatch":
			cr.re = pyRe(r.Pattern, true)
		}
		out = append(out, cr)
	}
	return out
}()

// contractMatch reports whether method+path is a Python route (any role).
func contractMatch(method, path string) (string, bool) {
	for _, r := range compiledContract {
		if !containsMethod(r.Methods, method) {
			continue
		}
		switch r.Kind {
		case "exact":
			if path == r.Pattern {
				return r.ID, true
			}
		case "prefix":
			if strings.HasPrefix(path, r.Pattern) && r.Role == "endpoint" {
				return r.ID, true
			}
		case "regex", "fullmatch":
			if r.re.MatchString(path) {
				return r.ID, true
			}
		}
	}
	for _, r := range compiledContract {
		// "/api/" itself is do_GET's split between API and static files.
		if r.Kind == "prefix" && r.Pattern != "/api/" && containsMethod(r.Methods, method) && strings.HasPrefix(path, r.Pattern) {
			return r.ID, true
		}
	}
	return "", false
}

func containsMethod(methods []string, m string) bool {
	for _, x := range methods {
		if x == m {
			return true
		}
	}
	return false
}

// PortedRoute is one contract id and the verbs the Go server answers itself.
type PortedRoute struct {
	ID      string
	Methods string // comma-separated
}

// Ported is printed by `ark-server routes`: the harness and diffreplay skip
// every other contract id, and tests/python/contract/route_table.py builds the
// ported/not-ported tables from it.  "exact:/api/nope" stands for the 404 of
// unknown paths.
var Ported = []PortedRoute{
	{"exact:/api/health", "GET,HEAD"},
	{"exact:/api/nope", "DELETE,GET,HEAD,OPTIONS,PATCH,POST,PUT"},
	{"static:/*", "GET,HEAD"},
	{"exact:/", "GET,HEAD"},
	{"exact:/login", "GET,HEAD,POST"},
	{"exact:/logout", "GET,HEAD,POST"},
	{"exact:/api/auth/session", "GET,HEAD"},
	{"exact:/api/auth/logout", "POST"},
	{"exact:/api/graph/snapshot", "GET,HEAD"},
	{"exact:/api/graph", "GET,HEAD"},
	{"exact:/api/units", "GET,HEAD,POST"},
	{`regex:^/api/units/([^/]+)$`, "DELETE,GET,HEAD,PATCH"},
	{`regex:^/api/units/([^/]+)/comments$`, "GET,HEAD,POST"},
	{`regex:^/api/units/([^/]+)/comments/(\d+)/resolve$`, "POST"},
	{"exact:/api/revision", "GET,HEAD"},
	// Client cache (main e41dfbf): delta, batched details, bodies, revision stream.
	{"exact:/api/changes", "GET,HEAD"},
	{"exact:/api/unit-details", "GET,HEAD"},
	{"exact:/api/graph/bodies", "GET,HEAD"},
	{"exact:/api/events", "GET,HEAD"},
	{"exact:/api/activity", "GET,HEAD"},
	{"exact:/api/timeline", "GET,HEAD"},
	{"exact:/api/project", "DELETE,GET,HEAD,PATCH"},
	// Phase 2: read-only views.
	{"exact:/api/bootstrap", "GET,HEAD"},
	{"exact:/api/projects", "GET,HEAD,POST"},
	{"exact:/api/projects/open", "POST"},
	{"exact:/api/blackbox", "GET,HEAD,POST"},
	{`regex:^/api/blackbox/([^/]+)/restore$`, "POST"},
	{"exact:/api/org", "GET,HEAD"},
	{"exact:/api/org/", "GET,HEAD"},
	{"exact:/api/org/users", "GET,HEAD,POST"},
	{`regex:^/api/org/users/([^/]+)$`, "DELETE,PATCH"},
	{`regex:^/api/org/users/([^/]+)/password$`, "POST"},
	{`regex:^/api/org/users/([^/]+)/linux-accounts/([^/]+)$`, "DELETE"},
	{`regex:^/api/org/teams/([^/]+)$`, "DELETE,PATCH"},
	{`regex:^/api/org/teams/([^/]+)/members$`, "POST"},
	{`regex:^/api/org/teams/([^/]+)/members/([^/]+)$`, "DELETE"},
	{`regex:^/api/org/projects/([^/]+)/grants$`, "POST"},
	{`regex:^/api/org/projects/([^/]+)/grants/revoke$`, "POST"},
	{"exact:/api/personal/agent-tokens", "GET,HEAD,POST"},
	{`regex:^/api/personal/agent-tokens/([A-Za-z0-9_]+)$`, "DELETE"},
	{"exact:/api/me/avatar", "DELETE,POST"},
	{"exact:/api/me/password", "POST"},
	{"exact:/api/org/password", "POST"},
	{"exact:/api/mobile/apps", "GET,HEAD"},
	{"exact:/api/remote/info", "GET,HEAD"},
	{"exact:/download/ARK-Studio-android.apk", "GET,HEAD"},
	{"exact:/download/ark.apk", "GET,HEAD"},
	{"exact:/download/ARK-Studio-webapp.apk", "GET,HEAD"},
	{"exact:/api/mobile/connection", "GET,HEAD"},
	{"exact:/api/mobile/pairs", "GET,HEAD"},
	{"exact:/api/mobile/session", "DELETE,GET,HEAD"},
	{"exact:/api/mobile/session/projects", "GET,HEAD"},
	{"exact:/api/mobile/session/project", "POST"},
	{"exact:/api/mobile/pair", "POST"},
	{"exact:/api/mobile/pair/claim", "POST"},
	{"exact:/api/mobile/login", "POST"},
	{`regex:^/api/mobile/pairs/([^/]+)/revoke$`, "POST"},
	{"exact:/api/org/teams", "GET,HEAD,POST"},
	{"exact:/api/org/audit", "GET,HEAD"},
	{`regex:^/api/org/users/([^/]+)/linux-accounts$`, "GET,HEAD,POST"},
	{`regex:^/api/org/projects/([^/]+)$`, "GET,HEAD"},
	{"exact:/api/me", "GET,HEAD,PATCH"},
	{`regex:^/api/users/([^/]+)/avatar$`, "GET,HEAD"},
	{"exact:/api/project/overview", "GET,HEAD"},
	{"exact:/api/project/assignees", "GET,HEAD"},
	{"exact:/api/project/settings", "GET,HEAD,PATCH"},
	{"exact:/api/topics", "GET,HEAD"},
	{`regex:^/api/units/([^/]+)/members$`, "GET,HEAD,PATCH,POST"},
	{"exact:/api/export", "GET,HEAD"},
	{`regex:^/api/media/(\d+)/raw$`, "GET,HEAD"},
	{"exact:/api/assistant/insights", "GET,HEAD"},
	{"exact:/api/agent/token", "GET,HEAD"},
	{"exact:/api/skills", "GET,HEAD"},
	{"exact:/api/skills/interface", "GET,HEAD"},
	{"exact:/api/skills/setup/state", "GET,HEAD"},
	{`regex:^/api/skills/([^/]+)$`, "GET,HEAD"},
	{`regex:^/api/skills/([^/]+)/(prompt|agent)$`, "GET,HEAD"},
	// Phase 2: agent and task-agent read APIs.
	{"exact:/api/agent/v1/whoami", "GET,HEAD"},
	{"exact:/api/agent/v1/context", "GET,HEAD"},
	{"exact:/api/agent/v1/export", "GET,HEAD"},
	{`fullmatch:/api/agent/v1/media/(\d+)/raw`, "GET,HEAD"},
	{"exact:/api/agent/v1/interface", "GET,HEAD"},
	{"exact:/api/agent/v1/insights", "GET,HEAD"},
	{"exact:/api/agent/v1/setup", "GET,HEAD"},
	{"exact:/api/agent/v1/skills", "GET,HEAD"},
	{"prefix:/api/agent/v1/skills/", "GET,HEAD"},
	{"exact:/api/task-agent/v1/context", "GET,HEAD"},
	{"exact:/api/task-agent/v1/project", "GET,HEAD"},
	{"exact:/api/task-agent/v1/units", "GET,HEAD"},
	{`regex:^/api/task-agent/v1/units/([^/]+)$`, "GET,HEAD"},
	{`regex:^/api/task-agent/v1/units/([^/]+)/comments$`, "GET,HEAD"},
	{"exact:/api/task-agent/v1/links", "GET,HEAD"},
	{"exact:/api/task-agent/v1/snapshot", "GET,HEAD"},
	// Phase 3: unit, link, media and topic writes.
	{`regex:^/api/units/([^/]+)/members/([^/]+)$`, "DELETE"},
	{`regex:^/api/units/([^/]+)/archive$`, "POST"},
	{`regex:^/api/units/([^/]+)/restore$`, "POST"},
	{`regex:^/api/units/([^/]+)/insert-between$`, "POST"},
	{"exact:/api/links", "POST"},
	{`regex:^/api/links/(\d+)$`, "DELETE,PATCH"},
	{"exact:/api/media", "POST"},
	{"exact:/api/media/upload", "POST"},
	{"exact:/api/agent/v1/units", "GET,HEAD,POST"},
	{"exact:/api/agent/v1/links", "POST"},
	{"exact:/api/agent/v1/media", "POST"},
	{"exact:/api/agent/v1/media/upload", "POST"},
	{"exact:/api/agent/v1/assets/upload", "POST"},
	{"exact:/api/agent/v1/project/palette", "GET,HEAD,POST"},
	{`regex:^/api/agent/v1/units/([^/]+)$`, "GET,HEAD,POST"},
	{`regex:^/api/agent/v1/units/([^/]+)/comments$`, "GET,HEAD,POST"},
	{`regex:^/api/agent/v1/units/([^/]+)/comments/(\d+)/resolve$`, "POST"},
	{`regex:^/api/agent/v1/units/([^/]+)/members$`, "GET,HEAD,POST"},
	{`fullmatch:/api/units/([^/]+)/history(?:/([^/]+))?`, "GET,HEAD,POST"},
	{`fullmatch:/api/agent/v1/units/([^/]+)/history(?:/([^/]+))?`, "GET,HEAD,POST"},
	{`regex:^/api/media/(\d+)$`, "DELETE,PATCH"},
	{"exact:/api/chat", "DELETE"},
	{"exact:/api/search/nodes", "GET,HEAD"},
	{"exact:/api/workspace/grep", "GET,HEAD"},
	{"exact:/api/workspace", "GET,HEAD,POST"},
	{"prefix:/api/workspace/", "GET,HEAD,POST"},
	{"exact:/api/workspace/info", "GET,HEAD"},
	{"exact:/api/workspace/tree", "GET,HEAD"},
	{"exact:/api/workspace/search", "GET,HEAD"},
	{"exact:/api/workspace/file", "GET,HEAD,POST"},
	{"exact:/api/workspace/raw", "GET,HEAD"},
	{"exact:/api/workspace/changes", "GET,HEAD"},
	{"exact:/api/workspace/diff", "GET,HEAD"},
	{"exact:/api/workspace/create", "POST"},
	{"exact:/api/workspace/rename", "POST"},
	{"exact:/api/workspace/delete", "POST"},
	{"exact:/api/workspace/terminals", "GET,HEAD,POST"},
	{`regex:^/api/workspace/terminals/([^/]+)$`, "DELETE,GET,HEAD"},
	{`regex:^/api/workspace/terminals/([^/]+)/stream$`, "GET,HEAD"},
	{`regex:^/api/workspace/terminals/([^/]+)/resize$`, "POST"},
	{`regex:^/api/workspace/terminals/([^/]+)/stdin$`, "POST"},
	{`regex:^/api/workspace/terminals/([^/]+)/cancel$`, "POST"},
	{"exact:/api/providers", "GET,HEAD"},
	{"exact:/api/providers/refresh", "POST"},
	{"exact:/api/worktrees", "GET,HEAD"},
	{"exact:/api/worktrees/create", "POST"},
	{"exact:/api/worktrees/remove", "POST"},
}

var (
	reUnit        = pyRe(`^/api/units/([^/]+)$`, false)
	reComments    = pyRe(`^/api/units/([^/]+)/comments$`, false)
	reResolve     = pyRe(`^/api/units/([^/]+)/comments/(\d+)/resolve$`, false)
	reHistoryFull = pyRe(`/api/units/([^/]+)/history(?:/([^/]+))?`, true)
)

// doGet is Handler.do_GET.
func (c *reqCtx) doGet() error {
	if err := c.checkHost(); err != nil {
		return err
	}
	if handled, err := c.handleAuthGet(); handled || err != nil {
		return err
	}
	if err := c.guardPublicAuth(); err != nil {
		return err
	}
	if err := c.guardMobile(); err != nil {
		return err
	}
	if err := c.guardProjectScope(); err != nil {
		return err
	}
	if strings.HasPrefix(c.path, "/api/") {
		return c.apiGet()
	}
	if apk, ok := apkByPath[c.path]; ok {
		return c.sendAPK(apk)
	}
	return c.static()
}

// qInt is _int((query.get(name) or [def])[0], default, low, high).
func (c *reqCtx) qInt(name string, def, low, high int64) int64 {
	n, err := pytext.Int(c.query.First(name, ""))
	if err != nil {
		return def
	}
	return max(low, min(high, n))
}

func (c *reqCtx) qTrue(name string) bool {
	v := c.query.First(name, "")
	return v == "1" || v == "true" || v == "yes"
}

// qlist is _qlist(query, name, alias).
func qlist(q pyurl.Values, names ...string) []any {
	var out []any
	seen := map[string]bool{}
	for _, key := range names {
		for _, raw := range q[key] {
			for _, part := range strings.Split(raw, ",") {
				if part = pytext.Strip(part); part != "" && !seen[part] {
					seen[part] = true
					out = append(out, part)
				}
			}
		}
	}
	return out
}

func optional(q pyurl.Values, name string) any {
	if v := q.First(name, ""); v != "" {
		return v
	}
	return nil
}

// filtersFromQuery is server.filters_from_query.
func (c *reqCtx) filtersFromQuery() store.Filters {
	q := c.query
	sortKey := "updated"
	if v, ok := q["sort"]; ok && len(v) > 0 {
		sortKey = v[0]
	}
	return store.Filters{
		Q: pytext.Strip(q.First("q", "")), Types: qlist(q, "types", "type"), TypesNone: q.First("types_none", "") == "1",
		Origins: qlist(q, "origins", "origin"), Statuses: qlist(q, "statuses", "status"),
		Importance: qlist(q, "importance"), Tags: qlist(q, "tags", "tag"), TagsUnset: qlist(q, "tags_unset", "tag_unset"),
		Roots: c.qTrue("roots"), Slim: c.qTrue("slim"), StartFrom: optional(q, "from"), StartTo: optional(q, "to"),
		UpdatedSince: optional(q, "updated_since"), Overdue: c.qTrue("overdue"), CreatedSince: optional(q, "created_since"),
		Sort: sortKey, Assignees: qlist(q, "assignees", "assignee"), Me: c.actor(),
	}
}

// apiGet is Handler._api_get for the ported routes.
func (c *reqCtx) apiGet() error {
	path := c.path
	if handled, err := c.apiGetViews(); handled || err != nil {
		return err
	}
	if handled, err := c.servicesGet(path); handled {
		return err
	}
	if handled, err := c.settingsGet(path); handled {
		return err
	}
	if handled, err := c.clientCacheGet(path); handled {
		return err
	}
	switch path {
	case "/api/health":
		payload := pyjson.NewObject("app", "arkstudio", "version", "2.0")
		if c.app.Auth != nil {
			payload.Set("auth_required", true)
		} else {
			payload.Set("root", c.app.Root)
		}
		c.ok(payload)
		return nil
	case "/api/project":
		p, err := c.require("read")
		if err != nil {
			return err
		}
		return c.cachedRead(p, func() (*pyjson.Object, error) {
			payload, err := p.Store.ProjectPayload(c.ctx)
			if err != nil {
				return nil, err
			}
			summary, err := p.Summary()
			if err != nil {
				return nil, err
			}
			return payload.Set("summary", summary), nil
		})
	case "/api/resources/graph":
		return c.resourcesGraph()
	case "/api/search/nodes":
		return c.searchNodes()
	case "/api/revision":
		p, err := c.require("read")
		if err != nil {
			return err
		}
		rev, err := p.Store.Revision(c.ctx)
		if err != nil {
			return err
		}
		cursor, err := p.Store.ChangeCursor(c.ctx)
		if err != nil {
			return err
		}
		c.ok(pyjson.NewObject("revision", rev, "cursor", cursor))
		return nil
	case "/api/units":
		p, err := c.require("read")
		if err != nil {
			return err
		}
		f := c.filtersFromQuery()
		f.Limit = c.qInt("limit", 200, 1, 1000)
		f.Offset = c.qInt("offset", 0, 0, 1_000_000)
		f.IDs = nil
		for _, x := range qlist(c.query, "ids") {
			if s := x.(string); pytext.IsDigit(s) {
				n, err := store.PyInt(s)
				if err != nil {
					return err
				}
				f.IDs = append(f.IDs, n)
			}
		}
		facets := c.query.First("facets", "1") != "0"
		return c.revisioned(p, func(headers headerList) error {
			return c.cachedReadHeaders(p, headers, func() (*pyjson.Object, error) { return p.Store.ListUnits(c.ctx, f, facets) })
		})
	case "/api/graph/snapshot":
		p, err := c.require("read")
		if err != nil {
			return err
		}
		bodies := c.bodiesFlag()
		return c.revisioned(p, func(headers headerList) error {
			return c.cachedReadRawHeaders(p, headers, func() ([]byte, error) { return p.Store.GraphSnapshotEncoded(c.ctx, bodies) })
		})
	case "/api/graph":
		p, err := c.require("read")
		if err != nil {
			return err
		}
		f := c.filtersFromQuery()
		withContext := c.qTrue("context")
		limit := c.qInt("limit", 700, 10, 1200)
		return c.cachedRead(p, func() (*pyjson.Object, error) { return p.Store.Graph(c.ctx, f, withContext, limit) })
	case "/api/timeline":
		p, err := c.require("read")
		if err != nil {
			return err
		}
		f := c.filtersFromQuery()
		return c.cachedRead(p, func() (*pyjson.Object, error) { return p.Store.Timeline(c.ctx, f) })
	case "/api/blackbox":
		return c.blackboxGet()
	case "/api/remote/info":
		return c.remoteInfo()
	case "/api/mobile/apps", "/api/mobile/connection", "/api/mobile/pairs", "/api/mobile/session/projects", "/api/mobile/session":
		_, err := c.mobileGet(path)
		return err
	case "/api/personal/agent-tokens":
		return c.personalTokensGet()
	case "/api/activity":
		p, err := c.require("read")
		if err != nil {
			return err
		}
		items, err := p.Store.Activity(c.ctx, c.qInt("limit", 40, 1, 400))
		if err != nil {
			return err
		}
		c.ok(pyjson.NewObject("activity", items))
		return nil
	}
	if handled, err := c.providersGet(path); handled {
		return err
	}
	if strings.HasPrefix(path, "/api/agent-sessions") {
		return c.sessionsGet(path)
	}
	if handled, err := c.sessionContextsGet(path); handled {
		return err
	}
	if isWorkspacePath(path) {
		return c.workspaceGet(path)
	}
	if m := reHistoryFull.FindStringSubmatch(path); m != nil {
		return c.nodeHistoryGet(m[1], m[2])
	}
	if m := reUnit.FindStringSubmatch(path); m != nil {
		p, err := c.require("read")
		if err != nil {
			return err
		}
		unit, err := p.Store.GetUnit(c.ctx, m[1])
		if err != nil {
			return err
		}
		c.ok(pyjson.NewObject("unit", unit))
		return nil
	}
	if m := reComments.FindStringSubmatch(path); m != nil {
		p, err := c.require("read")
		if err != nil {
			return err
		}
		comments, err := p.Store.Comments(c.ctx, m[1])
		if err != nil {
			return err
		}
		c.ok(pyjson.NewObject("comments", comments))
		return nil
	}
	return c.unknownOrNotPorted()
}

func (c *reqCtx) unknownOrNotPorted() error {
	method := c.r.Method
	if _, ok := contractMatch(method, c.path); ok {
		c.notPorted()
		return nil
	}
	return apiErr(404, "Unknown endpoint: "+c.path, "")
}

// doPost is Handler.do_POST for the ported routes.
func (c *reqCtx) doPost() error {
	if err := c.checkHost(); err != nil {
		return err
	}
	if c.rawPath == "/api/local/open" {
		return c.localOpen()
	}
	if handled, err := c.handleAuthPost(); handled || err != nil {
		return err
	}
	if err := c.guardPublicAuth(); err != nil {
		return err
	}
	if err := c.guardMobile(); err != nil {
		return err
	}
	if err := c.guardProjectScope(); err != nil {
		return err
	}
	path := c.rawPath // do_POST dispatches on the *raw* path
	if handled, err := c.taskGPUPost(path); handled {
		return err
	}
	if !c.sameOrigin() {
		return apiErr(403, "Cross-origin writes are rejected", "")
	}
	if strings.HasPrefix(path, "/api/task-agent/") {
		return apiErr(403, "Task-agent sessions have read-only ARK access; route changes through the inner assistant.", "")
	}
	if isProfilePath(path) {
		return c.profileWrite(path)
	}
	if path == "/api/chat/stream" {
		return c.chatStream()
	}
	body, err := c.readJSON()
	if err != nil {
		return err
	}
	actor := c.writeActor()
	if handled, err := c.orgPost(path, body); handled {
		return err
	}
	if handled, err := c.personalSettingsPost(path, body); handled {
		return err
	}
	if handled, err := c.mobilePost(path, body); handled {
		return err
	}
	if handled, err := c.servicesPost(path, body, actor); handled {
		return err
	}
	if isWorkspacePath(path) {
		return c.workspacePost(path, body)
	}
	if handled, err := c.providersPost(path, body); handled {
		return err
	}
	if handled, err := c.sessionsPost(path, body, actor); handled {
		return err
	}
	if handled, err := c.sessionContextsPost(path, body); handled {
		return err
	}
	if strings.HasPrefix(path, "/api/agent/v1/") {
		if err := c.requireAgent(); err != nil {
			return err
		}
		return c.agentWrite(path, body, actor)
	}
	if strings.HasPrefix(path, "/api/skills/") {
		return c.skillsPost(path, body, actor)
	}
	switch path {
	case "/api/projects":
		return c.createProject(body)
	case "/api/projects/open":
		return c.openProject(body)
	}
	if handled, err := c.blackboxPost(path, body); handled {
		return err
	}
	if m := reHistoryRaw.FindStringSubmatch(path); m != nil {
		return c.nodeHistoryWrite(m[1], m[2], body, actor)
	}
	if handled, err := c.storePost(path, body, actor); handled {
		return err
	}
	if m := reComments.FindStringSubmatch(path); m != nil {
		p, err := c.require("write")
		if err != nil {
			return err
		}
		kind := body.Value("kind")
		if !pyjson.Truthy(kind) {
			kind = "comment"
		}
		author := c.actor()
		if author == "" {
			author = "user"
		}
		comment, err := p.Store.AddComment(c.ctx, m[1], body.Value("body"), pyjson.Str(kind), body.Value("reply_to"), author, actor)
		if err != nil {
			return err
		}
		return c.commentsReply(p.Store, m[1], comment)
	}
	if m := reResolve.FindStringSubmatch(path); m != nil {
		p, err := c.require("write")
		if err != nil {
			return err
		}
		resolved := true
		if v, ok := body.Get("resolved"); ok && v == false {
			resolved = false
		}
		author := c.actor()
		if author == "" {
			author = "user"
		}
		comment, err := p.Store.ResolveComment(c.ctx, m[1], m[2], resolved, author, actor)
		if err != nil {
			return err
		}
		return c.commentsReply(p.Store, m[1], comment)
	}
	if handled, err := c.assistantConfigPost(path, body); handled {
		return err
	}
	if handled, err := c.resourcesDevicePost(path, body); handled {
		return err
	}
	return c.unknownOrNotPorted()
}

func (c *reqCtx) commentsReply(s *store.Store, ref string, comment *pyjson.Object) error {
	comments, err := s.Comments(c.ctx, ref)
	if err != nil {
		return err
	}
	c.ok(pyjson.NewObject("comment", comment, "comments", comments))
	return nil
}

func isProfilePath(path string) bool {
	return path == "/api/me" || path == "/api/me/avatar" || path == "/api/me/password" || reAvatar.MatchString(path)
}

var (
	reOrgUser      = pyRe(`^/api/org/users/([^/]+)$`, false)
	reOrgTeam      = pyRe(`^/api/org/teams/([^/]+)$`, false)
	reSteer        = pyRe(`^/api/agent-sessions/([^/]+)/steer/([^/]+)$`, false)
	rePersonalTok  = pyRe(`^/api/personal/agent-tokens/([A-Za-z0-9_]+)$`, false)
	reLinuxAccount = pyRe(`^/api/org/users/([^/]+)/linux-accounts/([^/]+)$`, false)
	reTeamMember   = pyRe(`^/api/org/teams/([^/]+)/members/([^/]+)$`, false)
	reTerminal     = pyRe(`^/api/workspace/terminals/([^/]+)$`, false)
	reAgentSession = pyRe(`^/api/agent-sessions/([^/]+)$`, false)
)

// doPatchDelete is do_PATCH / do_DELETE in Python's branch order; branches
// not ported yet answer 501.
func (c *reqCtx) doPatchDelete() error {
	if err := c.checkHost(); err != nil {
		return err
	}
	if err := c.guardPublicAuth(); err != nil {
		return err
	}
	if err := c.guardMobile(); err != nil {
		return err
	}
	if err := c.guardProjectScope(); err != nil {
		return err
	}
	if !c.sameOrigin() {
		return apiErr(403, "Cross-origin writes are rejected", "")
	}
	path := c.rawPath
	if strings.HasPrefix(path, "/api/task-agent/") {
		return apiErr(403, "Task-agent sessions have read-only ARK access", "")
	}
	if isProfilePath(path) {
		return c.profileWrite(path)
	}
	if c.r.Method == "PATCH" {
		body, err := c.readJSON()
		if err != nil {
			return err
		}
		actor := c.writeActor()
		if handled, err := c.orgPatch(path, body); handled {
			return err
		}
		p, err := c.require("read")
		if err != nil {
			return err
		}
		switch path {
		case "/api/project":
			return c.updateProject(body)
		case "/api/project/settings":
			return c.updateProjectSettings(body)
		}
		if handled, err := c.sessionContextsPatch(path, body); handled {
			return err
		}
		if handled, err := c.sessionsPatch(path, body); handled {
			return err
		}
		if handled, err := c.storePatch(p, path, body, actor); handled {
			return err
		}
		return apiErr(404, "Unknown endpoint: "+path, "")
	}
	if path == "/api/mobile/session" {
		return c.mobileDelete()
	}
	actor := c.writeActor()
	if handled, err := c.orgDelete(path); handled {
		return err
	}
	if path == "/api/project" {
		body, err := c.readJSON()
		if err != nil {
			return err
		}
		return c.deleteProject(body)
	}
	p, err := c.require("read")
	if err != nil {
		return err
	}
	if m := reTerminal.FindStringSubmatch(path); m != nil {
		return c.terminalDelete(m[1])
	}
	if path == "/api/chat" {
		// App.chat_clear: this user's Assistant history in this project only.
		who := c.actor()
		n, err := p.Store.ClearChat(c.ctx, who)
		if err != nil {
			return err
		}
		logActor := "human:ui"
		if who != "" {
			logActor = "human:" + who
		}
		if err := p.Store.LogActivity(c.ctx, logActor, "chat.clear", nil, store.ChatClearDetail(n)); err != nil {
			return err
		}
		c.ok(pyjson.NewObject("cleared", n))
		return nil
	}
	if handled, err := c.sessionsDelete(path); handled {
		return err
	}
	if handled, err := c.sessionContextsDelete(path); handled {
		return err
	}
	if handled, err := c.storeDelete(p, path, actor); handled {
		return err
	}
	return apiErr(404, "Unknown endpoint: "+path, "")
}
