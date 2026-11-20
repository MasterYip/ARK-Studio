package server

import (
	"regexp"
	"strings"

	"arkstudio/go/internal/api"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/compat/pyurl"
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
	if c.serveWatcherScript() { // public: no secrets, so no token on the download's command line
		return nil
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

// qInt is api.QInt over the request query.
func (c *reqCtx) qInt(name string, def, low, high int64) int64 {
	return api.QInt(c.query, name, def, low, high)
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

// apiGet is Handler._api_get: the GET registrations of router.go.
func (c *reqCtx) apiGet() error {
	if handled, err := dispatch(c, getRoutes, c.path); handled {
		return err
	}
	return c.unknownOrNotPorted()
}

func (c *reqCtx) getHealth() error {
	payload := pyjson.NewObject("app", "arkstudio", "version", "2.0")
	if c.app.Auth != nil {
		payload.Set("auth_required", true)
	} else {
		payload.Set("root", c.app.Root)
	}
	c.ok(payload)
	return nil
}

func (c *reqCtx) getProject() error {
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
}

func (c *reqCtx) getRevision() error {
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
}

func (c *reqCtx) getUnits() error {
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
}

func (c *reqCtx) getGraphSnapshot() error {
	p, err := c.require("read")
	if err != nil {
		return err
	}
	bodies := c.bodiesFlag()
	return c.revisioned(p, func(headers headerList) error {
		return c.cachedReadRawHeaders(p, headers, func() ([]byte, error) { return p.Store.GraphSnapshotEncoded(c.ctx, bodies) })
	})
}

func (c *reqCtx) getGraph() error {
	p, err := c.require("read")
	if err != nil {
		return err
	}
	f := c.filtersFromQuery()
	withContext := c.qTrue("context")
	limit := c.qInt("limit", 700, 10, 1200)
	return c.cachedRead(p, func() (*pyjson.Object, error) { return p.Store.Graph(c.ctx, f, withContext, limit) })
}

func (c *reqCtx) getTimeline() error {
	p, err := c.require("read")
	if err != nil {
		return err
	}
	f := c.filtersFromQuery()
	return c.cachedRead(p, func() (*pyjson.Object, error) { return p.Store.Timeline(c.ctx, f) })
}

func (c *reqCtx) getActivity() error {
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

func (c *reqCtx) getUnit(m []string) error {
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

func (c *reqCtx) getComments(m []string) error {
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
	if handled, err := c.handleInvitePost(); handled || err != nil { // Go only (routes_invites.go)
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
	if handled, err := dispatch(c, postBeforeOrigin, path); handled {
		return err
	}
	if !c.sameOrigin() {
		return apiErr(403, "Cross-origin writes are rejected", "")
	}
	if strings.HasPrefix(path, "/api/task-agent/") {
		return apiErr(403, "Task-agent sessions have read-only ARK access; route changes through the inner assistant.", "")
	}
	if handled, err := dispatch(c, postBeforeBody, path); handled {
		return err
	}
	body, err := c.readJSON()
	if err != nil {
		return err
	}
	c.body, c.writer = body, c.writeActor()
	if handled, err := dispatch(c, postRoutes, path); handled {
		return err
	}
	return c.unknownOrNotPorted()
}

func (c *reqCtx) postComment(m []string) error {
	p, err := c.require("write")
	if err != nil {
		return err
	}
	kind := c.body.Value("kind")
	if !pyjson.Truthy(kind) {
		kind = "comment"
	}
	author := c.actor()
	if author == "" {
		author = "user"
	}
	comment, err := p.Store.AddComment(c.ctx, m[1], c.body.Value("body"), pyjson.Str(kind), c.body.Value("reply_to"), author, c.writer)
	if err != nil {
		return err
	}
	return c.commentsReply(p.Store, m[1], comment)
}

func (c *reqCtx) postResolve(m []string) error {
	p, err := c.require("write")
	if err != nil {
		return err
	}
	resolved := true
	if v, ok := c.body.Get("resolved"); ok && v == false {
		resolved = false
	}
	author := c.actor()
	if author == "" {
		author = "user"
	}
	comment, err := p.Store.ResolveComment(c.ctx, m[1], m[2], resolved, author, c.writer)
	if err != nil {
		return err
	}
	return c.commentsReply(p.Store, m[1], comment)
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
	if handled, err := dispatch(c, writeProfileRoutes, path); handled {
		return err
	}
	if c.r.Method == "PATCH" {
		body, err := c.readJSON()
		if err != nil {
			return err
		}
		c.body, c.writer = body, c.writeActor()
		if handled, err := dispatch(c, patchBeforeProject, path); handled {
			return err
		}
		if c.routeProject, err = c.require("read"); err != nil {
			return err
		}
		if handled, err := dispatch(c, patchRoutes, path); handled {
			return err
		}
		return apiErr(404, "Unknown endpoint: "+path, "")
	}
	if handled, err := dispatch(c, deleteBeforeActor, path); handled {
		return err
	}
	c.writer = c.writeActor()
	if handled, err := dispatch(c, deleteBeforeProject, path); handled {
		return err
	}
	var err error
	if c.routeProject, err = c.require("read"); err != nil {
		return err
	}
	if handled, err := dispatch(c, deleteRoutes, path); handled {
		return err
	}
	return apiErr(404, "Unknown endpoint: "+path, "")
}

// deleteChat is App.chat_clear: this user's Assistant history in this
// project only (local mode: with the pre-v10 rows; T-0028).
func (c *reqCtx) deleteChat() error {
	p := c.routeProject
	who := c.actor()
	n, err := p.Store.ClearChat(c.ctx, who, c.app.Auth == nil)
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
