package server

// Desktop links (Go only): the ARK Studio desktop app lists this server's
// projects and creates projects here for a signed-in account, with a
// revocable device token instead of a password. See internal/mobile/desktop.go
// for the scope and docs/guide/desktop-link.md.
//
//   POST /api/desktop/link      signed-in browser session (or the trusted
//                               local owner): a one-time link code (120 s),
//                               claimed by the app with POST /api/mobile/pair/claim
//   GET  /api/desktop/projects  desktop link token: the account's projects
//   POST /api/desktop/projects  desktop link token: create a project

import (
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/mobile"
)

const desktopLinkPath = "/api/desktop/link"

// desktopRoutesGoOnly are the Go-only ids of this file (GoOnlyRoutes).
var desktopRoutesGoOnly = []PortedRoute{
	{"goonly:exact:" + desktopLinkPath, "POST"},
	{"goonly:exact:" + mobile.DesktopProjectsPath, "GET,HEAD,POST"},
}

// desktopSession is the request's desktop-link session, or an error reply.
func (c *reqCtx) desktopSession() (*mobile.Session, error) {
	s := c.mobileSession
	if s == nil || !c.app.Mobile.IsDesktopLink(s) {
		return nil, apiErr(401, "Link ARK Studio to this server first (Link this desktop).", "DESKTOP_LINK_REQUIRED")
	}
	return s, nil
}

// desktopLink is POST /api/desktop/link.
func (c *reqCtx) desktopLink(body *pyjson.Object) error {
	if c.mobileSession != nil {
		return apiErr(403, "A paired device cannot link another device.", "MOBILE_SCOPE")
	}
	if c.app.Auth != nil && c.session == nil {
		return apiErr(401, "Sign in with the ARK account to continue.", "AUTH_REQUIRED")
	}
	p, err := c.require("read")
	if err != nil {
		return err
	}
	actor := c.actor()
	device := pytext.Prefix(pytext.Strip(strOrEmptyAny(body.Value("device"))), 80)
	pair, err := c.app.Mobile.Create(p.ID(), p.Name(), "read_only", int64(mobile.DesktopLinkTTL), "permanent",
		"desktop-link:"+c.clientIP(), actor, mobile.SourceDesktop)
	if err != nil {
		return mobileErr(err)
	}
	details := pyjson.NewObject("pair", pair.Value("id"))
	if device != "" {
		details.Set("device", device)
	}
	if err := p.Store.LogActivity(c.ctx, "human:ui", "desktop.link.create", nil, details); err != nil {
		return err
	}
	actorID, err := c.app.Org.UserIDFor(c.ctx, actor)
	if err != nil {
		return err
	}
	if err := c.app.Org.Log(c.ctx, actorID, "desktop.link.create", p.ID(), details); err != nil {
		return err
	}
	var user any
	if actor != "" {
		user = actor
	}
	c.ok(pyjson.NewObject("code", pair.Value("token"), "expires_in", pair.Value("expires_in"),
		"claim_path", "/api/mobile/pair/claim",
		"pair", pyjson.NewObject("id", pair.Value("id"), "project", pair.Value("project_name"), "user", user,
			"expires_at", pair.Value("expires_at"))))
	return nil
}

// desktopCanCreate mirrors createProject's rule for the linked account.
func (c *reqCtx) desktopCanCreate(user string) (bool, error) {
	if c.app.Auth == nil {
		return true, nil
	}
	role, _, err := c.app.Org.RoleFor(c.ctx, user)
	if err != nil {
		return false, err
	}
	return role == "owner" || role == "admin" || role == "member", nil
}

// desktopProjectsGet is GET /api/desktop/projects.
func (c *reqCtx) desktopProjectsGet() error {
	s, err := c.desktopSession()
	if err != nil {
		return err
	}
	c.user = s.User
	c.resolved = false
	visible, err := c.visibleProjects()
	if err != nil {
		return err
	}
	c.withNewReviews(visible) // project_reviews.go (Go only)
	projects := []any{}
	for _, item := range visible {
		o := item.(*pyjson.Object)
		if o.Value("id") == demoID {
			continue
		}
		row := pyjson.NewObject("id", o.Value("id"), "name", o.Value("name"),
			"description", o.Value("description"), "created", o.Value("created"), "updated", o.Value("updated"),
			"permission", o.Value("permission"))
		for _, key := range []string{"review_new", "review_total"} {
			if v, ok := o.Get(key); ok {
				row.Set(key, v)
			}
		}
		projects = append(projects, row)
	}
	c.withProjectPeople(projects) // project_people.go (Go only)
	canCreate, err := c.desktopCanCreate(s.User)
	if err != nil {
		return err
	}
	var user any
	if s.User != "" {
		user = s.User
	}
	c.ok(pyjson.NewObject("projects", projects, "user", user, "can_create", canCreate))
	return nil
}

// desktopProjectsPost is POST /api/desktop/projects: like POST /api/projects
// but nothing is switched or opened (the app opens the project afterwards).
func (c *reqCtx) desktopProjectsPost(body *pyjson.Object) error {
	s, err := c.desktopSession()
	if err != nil {
		return err
	}
	c.user = s.User
	c.resolved = false
	p, err := c.createProjectRecord(body)
	if err != nil {
		return err
	}
	if err := p.Store.LogActivity(c.ctx, "human:desktop", "desktop.project.create", nil,
		pyjson.NewObject("pair", s.PairingID)); err != nil {
		return err
	}
	summary, err := p.Summary()
	if err != nil {
		return err
	}
	permission := "admin"
	if c.app.Auth != nil {
		if permission, err = c.app.Org.ProjectPermission(c.ctx, s.User, p.ID()); err != nil {
			return err
		}
	}
	c.ok(pyjson.NewObject("project", pyjson.NewObject("id", summary.Value("id"), "name", summary.Value("name"),
		"description", summary.Value("description"), "updated", summary.Value("updated"), "permission", permission)))
	return nil
}
