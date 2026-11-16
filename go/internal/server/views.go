package server

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"arkstudio/go/internal/org"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/settings"
)

const demoID = "ark-demo"

func init() {
	var colors []any
	v, _ := pyjson.Loads(staticJSON["avatar_colors"])
	colors, _ = v.([]any)
	for _, c := range colors {
		org.AvatarColors = append(org.AvatarColors, c.(string))
	}
}

// staticRaw is a snapshot payload (gen_static.py) inserted verbatim.
func staticRaw(key string) pyjson.Raw { return pyjson.Raw(staticJSON[key]) }

func staticObject(key string) *pyjson.Object {
	o, _ := pyjson.LoadsObject(staticJSON[key])
	return o
}

// ------------------------------------------------------------- projects

// demoSummary is demo.summary(root, username).
func (a *App) demoSummary(username string) *pyjson.Object {
	if username == "" {
		username = "local"
	}
	sum := sha256.Sum256([]byte(username))
	key := hex.EncodeToString(sum[:])[:16]
	path := filepath.Join(a.Root, ".demo", key)
	d := staticObject("demo")
	return pyjson.NewObject("id", demoID, "name", d.Value("name"), "description", d.Value("description"),
		"path", path, "folder", key, "created", nil, "updated", nil, "demo", true, "temporary", true,
		"permission", "write")
}

func (c *reqCtx) ownerBootstrapName() string {
	if c.app.Auth != nil {
		return c.app.Auth.Username
	}
	return org.LocalUsername
}

// visibleProjects is App.visible_projects.
func (c *reqCtx) visibleProjects() ([]any, error) {
	summaries := c.app.Projects.ListProjects()
	if err := c.app.Org.SyncProjects(c.ctx, summaries, c.ownerBootstrapName()); err != nil {
		return nil, err
	}
	out := []any{}
	if c.app.Auth == nil {
		hasDemo := false
		for _, s := range summaries {
			out = append(out, s.Copy().Set("permission", "admin").Set("role", "owner"))
			hasDemo = hasDemo || s.Value("id") == demoID
		}
		if !hasDemo {
			out = append(out, c.app.demoSummary(org.LocalUsername).Set("role", "owner"))
		}
		return out, nil
	}
	user := c.actor()
	if user == "" {
		return out, nil
	}
	access, err := c.app.Org.ProjectsFor(c.ctx, user)
	if err != nil {
		return nil, err
	}
	byID := map[string]*pyjson.Object{}
	for _, row := range access {
		byID[pyjson.Str(row.Value("id"))] = row
	}
	hasDemo := false
	for _, s := range summaries {
		row, ok := byID[pyjson.Str(s.Value("id"))]
		if !ok {
			continue
		}
		item := s.Copy()
		item.Set("permission", row.Value("permission"))
		item.Set("grants", orList(row.Value("grants")))
		item.Set("team_grants", orList(row.Value("team_grants")))
		out = append(out, item)
		hasDemo = hasDemo || s.Value("id") == demoID
	}
	if !hasDemo {
		out = append(out, c.app.demoSummary(user).Set("grants", []any{}).Set("team_grants", []any{}))
	}
	return out, nil
}

func orList(v any) any {
	if !pyjson.Truthy(v) {
		return []any{}
	}
	return v
}

// projectPermission is App.project_permission().
func (c *reqCtx) projectPermission() (any, error) {
	if c.app.Auth == nil {
		return "admin", nil
	}
	p, err := c.currentProject()
	if err != nil || p == nil {
		return nil, err
	}
	user := c.actor()
	if user == "" {
		return nil, nil
	}
	if p.ID() == demoID {
		return "write", nil
	}
	perm, err := c.app.Org.ProjectPermission(c.ctx, user, p.ID())
	if err != nil || perm == "" {
		return nil, err
	}
	return perm, nil
}

// sessionPayload is App.session_payload.
func (c *reqCtx) sessionPayload() (*pyjson.Object, error) {
	orgRow, _, err := c.app.Org.Org(c.ctx)
	if err != nil {
		return nil, err
	}
	if c.app.Auth == nil {
		return pyjson.NewObject("required", false, "authenticated", true, "local", true, "user", org.LocalUsername,
			"role", "owner", "is_admin", true, "is_owner", true,
			"org", pyjson.NewObject("id", c.app.Org.OrgID(c.ctx), "name", orgRow.Get("name")),
			"project_permission", "admin", "cache_id", c.cacheIdentity()), nil
	}
	if c.session == nil {
		return pyjson.NewObject("required", true, "authenticated", false, "local", false, "user", nil, "role", nil,
			"is_admin", false, "is_owner", false, "org", nil, "project_permission", nil), nil
	}
	roleText, ok, err := c.app.Org.RoleFor(c.ctx, c.session.Username)
	if err != nil {
		return nil, err
	}
	var role any
	if ok {
		role = roleText
	}
	perm, err := c.projectPermission()
	if err != nil {
		return nil, err
	}
	return pyjson.NewObject("required", true, "authenticated", true, "local", false, "user", c.session.Username,
		"role", role, "is_admin", ok && org.RoleRank[roleText] >= org.RoleRank["admin"], "is_owner", ok && roleText == "owner",
		"org", pyjson.NewObject("id", orgRow.Get("id"), "name", orgRow.Get("name")), "project_permission", perm,
		"cache_id", c.cacheIdentity()), nil
}

// profilePayload is App.profile_payload.
func (c *reqCtx) profilePayload() (*pyjson.Object, error) {
	username := c.actor()
	profile, err := c.app.Org.Profile(c.ctx, username)
	if err != nil {
		return nil, err
	}
	session, err := c.sessionPayload()
	if err != nil {
		return nil, err
	}
	role := session.Value("role")
	if !pyjson.Truthy(role) {
		r, ok, err := c.app.Org.RoleFor(c.ctx, username)
		if err != nil {
			return nil, err
		}
		role = nil
		if ok {
			role = r
		}
	}
	profile.Set("permissions", pyjson.NewObject("role", role,
		"is_admin", pyjson.Truthy(session.Value("is_admin")), "is_owner", pyjson.Truthy(session.Value("is_owner")),
		"project_permission", session.Value("project_permission"), "local", c.app.Auth == nil,
		"device", c.mobileSession != nil, "read_only", c.mobileSession != nil && c.mobileSession.Permission == "read_only",
		"can_change_password", c.app.Auth != nil && c.mobileSession == nil))
	users, err := c.app.Org.UserDirectory(c.ctx)
	if err != nil {
		return nil, err
	}
	return pyjson.NewObject("profile", profile, "options", staticRaw("profile_options"), "users", users), nil
}

// bootstrap is App.bootstrap.
func (c *reqCtx) bootstrap() (*pyjson.Object, error) {
	p, err := c.currentProject()
	if err != nil {
		return nil, err
	}
	var payload *pyjson.Object
	if p != nil {
		if payload, err = p.Store.ProjectPayload(c.ctx); err != nil {
			return nil, err
		}
		summary, err := p.Summary()
		if err != nil {
			return nil, err
		}
		payload.Set("summary", summary)
	} else {
		payload = pyjson.NewObject("manifest", nil, "path", "", "stats", pyjson.NewObject(), "activity", []any{},
			"next_actions", []any{}, "needs_you", []any{}, "updated_at", nil, "summary", nil)
	}
	session, err := c.sessionPayload()
	if err != nil {
		return nil, err
	}
	projects, err := c.visibleProjects()
	if err != nil {
		return nil, err
	}
	payload.Set("projects", projects).Set("session", session)
	if actor := c.actor(); actor != "" {
		overview, err := c.app.Org.OrgOverview(c.ctx, actor)
		if err != nil {
			return nil, err
		}
		users, err := c.app.Org.UserDirectory(c.ctx)
		if err != nil {
			return nil, err
		}
		payload.Set("org", overview).Set("users", users)
		if profile, err := c.profilePayload(); err == nil {
			payload.Set("profile", profile.Value("profile"))
		} else if _, isOrg := err.(*org.Error); isOrg {
			payload.Set("profile", nil)
		} else if _, isAPI := err.(*ApiError); isAPI {
			payload.Set("profile", nil)
		} else {
			return nil, err
		}
	} else {
		payload.Set("org", nil).Set("users", []any{}).Set("profile", nil)
	}
	payload.Set("vocabulary", staticRaw("vocabulary")).Set("palette_presets", settings.PresetsObject()).
		Set("server", pyjson.NewObject("started_at", c.app.StartedAt, "version", "2.0", "cache_schema", clientCacheSchema))
	return payload, nil
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

func msSince(t time.Time) float64 {
	return math.Round(float64(time.Since(t).Microseconds())/10) / 100
}

// memberNames is App.project_member_names (usernames and display names).
func (c *reqCtx) memberNames(p *project.Project) ([]*pyjson.Object, error) {
	if c.app.Auth == nil {
		return []*pyjson.Object{pyjson.NewObject("username", org.LocalUsername, "display_name", org.LocalUsername)}, nil
	}
	return c.app.Org.ProjectMemberNames(c.ctx, p.ID())
}

// projectOverview is App.project_overview.
func (c *reqCtx) projectOverview(p *project.Project) (*pyjson.Object, error) {
	started := time.Now()
	timing := pyjson.NewObject()
	lap := time.Now()
	mark := func(name string) {
		timing.Set(name, msSince(lap))
		lap = time.Now()
	}
	setup, err := c.setupPayload(p)
	if err != nil {
		return nil, err
	}
	gpu := c.app.gpu().Overview(p.ID())
	members, err := c.memberNames(p)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(members))
	for i, m := range members {
		names[i] = pyjson.Str(m.Value("username"))
	}
	mark("members")
	data, err := p.Store.ProjectOverview(c.ctx, names)
	if err != nil {
		return nil, err
	}
	mark("store")
	sub := pyjson.NewObject()
	for _, k := range []string{"state", "layers", "readiness", "host"} {
		sub.Set(k, setup.Value(k))
	}
	data.Set("setup", sub)
	mark("setup")
	data.Set("runs", c.app.Sessions.RunsSummary(p.ID()))
	mark("runs")
	data.Set("gpu", gpu)
	mark("gpu")
	data.Set("me", c.actor()).Set("timing", timing).Set("generated_ms", msSince(started))
	return data, nil
}

// assignableUsers is App.assignable_users.
func (c *reqCtx) assignableUsers(p *project.Project) (*pyjson.Object, error) {
	me := c.actor()
	users, err := c.memberNames(p)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, u := range users {
		seen[strings.ToLower(pyjson.Str(u.Value("username")))] = true
	}
	inUse, err := p.Store.AssigneesInUse(c.ctx)
	if err != nil {
		return nil, err
	}
	former := []string{}
	for _, name := range inUse {
		if !seen[strings.ToLower(name)] {
			former = append(former, name)
		}
	}
	if users == nil {
		users = []*pyjson.Object{}
	}
	return pyjson.NewObject("users", users, "me", me, "me_member", me != "" && seen[strings.ToLower(me)], "former", former), nil
}

// mobileOrgOverview is App.mobile_org_overview.
func (c *reqCtx) mobileOrgOverview() (*pyjson.Object, error) {
	overview, err := c.app.Org.OrgOverview(c.ctx, c.actor())
	if err != nil {
		return nil, err
	}
	current, err := c.currentProject()
	if err != nil {
		return nil, err
	}
	projects := []any{}
	var list []*pyjson.Object
	switch x := overview.Value("projects").(type) {
	case []*pyjson.Object:
		list = x
	case []any:
		for _, item := range x {
			if o, ok := item.(*pyjson.Object); ok {
				list = append(list, o)
			}
		}
	}
	for _, o := range list {
		if current != nil && o.Value("id") == current.ID() {
			projects = append(projects, o)
		}
	}
	return pyjson.NewObject("org", overview.Value("org"), "user", overview.Value("user"), "role", overview.Value("role"),
		"is_admin", overview.Value("is_admin"), "is_owner", overview.Value("is_owner"), "projects", projects,
		"teams", []any{}, "device", true), nil
}
