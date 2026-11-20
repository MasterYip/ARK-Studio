package server

// Project invite links (Go only; org/invites.go).
//
// Project admins (browser session):
//   GET  /api/org/projects/{id}/invites                 the project's links
//   POST /api/org/projects/{id}/invites                 create one: {permission, expires_days,
//                                                       max_uses, allow_signup, label}; the reply
//                                                       carries the link, shown only this once
//   POST /api/org/projects/{id}/invites/{inv}/revoke    end a link
//   GET  /api/org/invite-settings                       the organization switch
//   POST /api/org/invite-settings                       {signup_enabled} (owner)
//
// Whoever opened <public URL>/#/join/<token> (no sign-in needed; answered
// before the sign-in guard, like /login):
//   POST /api/auth/invite/preview   {token}: state, project, inviter, permission
//   POST /api/auth/invite/accept    {token}: join with the signed-in account
//   POST /api/auth/invite/signup    {token, csrf, username, display_name, password}:
//                                   create an account, join, sign in
//
// The token travels in the URL fragment and in POST bodies only, so it never
// appears in a request line or an access log.

import (
	"net/url"
	"strings"
	"sync"

	"arkstudio/go/internal/auth"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/org"
)

const (
	inviteSettingsPath = "/api/org/invite-settings"
	invitePreviewPath  = "/api/auth/invite/preview"
	inviteAcceptPath   = "/api/auth/invite/accept"
	inviteSignupPath   = "/api/auth/invite/signup"
)

var (
	reProjectInvites = pyRe(`^/api/org/projects/([^/]+)/invites$`, false)
	reInviteRevoke   = pyRe(`^/api/org/projects/([^/]+)/invites/([A-Za-z0-9_]+)/revoke$`, false)
)

// inviteRoutesGoOnly are the Go-only ids of this file (GoOnlyRoutes).
var inviteRoutesGoOnly = []PortedRoute{
	{`goonly:regex:^/api/org/projects/([^/]+)/invites$`, "GET,HEAD,POST"},
	{`goonly:regex:^/api/org/projects/([^/]+)/invites/([A-Za-z0-9_]+)/revoke$`, "POST"},
	{"goonly:exact:" + inviteSettingsPath, "GET,HEAD,POST"},
	{"goonly:exact:" + invitePreviewPath, "POST"},
	{"goonly:exact:" + inviteAcceptPath, "POST"},
	{"goonly:exact:" + inviteSignupPath, "POST"},
}

// Rate limits of the public invite routes (sliding windows, per process).
const (
	inviteBadLimit, inviteBadWindow       = 30, 600.0  // unknown or unusable tokens per client
	inviteSignupIPLimit, inviteSignupIPWn = 10, 3600.0 // sign-up attempts per client
	inviteSignupTokLimit, inviteSignupTkW = 20, 3600.0 // sign-up attempts per link
)

// inviteLimiter counts events per key in sliding windows.
type inviteLimiter struct {
	mu      sync.Mutex
	buckets map[string][]float64
}

func (l *inviteLimiter) trim(key string, now, window float64) []float64 {
	b := l.buckets[key]
	for len(b) > 0 && now-b[0] >= window {
		b = b[1:]
	}
	if len(b) == 0 {
		delete(l.buckets, key)
	} else {
		l.buckets[key] = b
	}
	return b
}

// over reports whether key reached limit within window.
func (l *inviteLimiter) over(key string, limit int, window float64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buckets == nil {
		l.buckets = map[string][]float64{}
	}
	return len(l.trim(key, nowSeconds(), window)) >= limit
}

// add records one event for key.
func (l *inviteLimiter) add(key string, window float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buckets == nil {
		l.buckets = map[string][]float64{}
	}
	now := nowSeconds()
	l.buckets[key] = append(l.trim(key, now, window), now)
	if len(l.buckets) > 8192 {
		for k := range l.buckets {
			l.trim(k, now, 3600)
		}
	}
}

func inviteRateLimited() error {
	return apiErr(429, "Too many invite attempts. Wait a few minutes and try again.", "INVITE_RATE_LIMITED")
}

func invitesUnavailable() error {
	return apiErr(404, "Invite links need a server with account sign-in.", "INVITES_UNAVAILABLE")
}

// inviteAdminGuard: a signed-in browser session (no agent, task or phone
// token) on a server with accounts.
func (c *reqCtx) inviteAdminGuard() (string, error) {
	if c.app.Auth == nil {
		return "", invitesUnavailable()
	}
	if c.token != nil || c.agentToken() != "" || c.header("X-ARK-Read-Token") != "" {
		return "", apiErr(403, "Agent and task tokens cannot manage invite links.", "AGENT_TOKEN_FORBIDDEN")
	}
	if c.mobileSession != nil || c.mobileToken() != "" {
		return "", apiErr(403, "A paired device cannot manage invite links.", "MOBILE_INVITES_BLOCKED")
	}
	if c.session == nil || c.actor() == "" {
		return "", apiErr(401, "Sign in with the ARK account to continue.", "AUTH_REQUIRED")
	}
	return c.actor(), nil
}

// inviteBaseURL is where invite links point: the configured public URL,
// else the address this request came in on.
func (c *reqCtx) inviteBaseURL() string {
	if c.app.publicURL != "" {
		return strings.TrimRight(c.app.publicURL, "/") + "/"
	}
	return c.requestScheme() + "://" + c.r.Host + c.externalPrefix()
}

// InviteURL is the link for a token.
func InviteURL(base, token string) string {
	return base + "#/join/" + url.PathEscape(token)
}

// recordInviteActivity writes project activity rows for invite actions.
// Activity is best effort: the org audit already holds the record.
func (c *reqCtx) recordInviteActivity(items ...org.InviteActivity) {
	for _, a := range items {
		p, err := c.projectByID(a.ProjectID)
		if err != nil || p == nil || p.ID() == demoID {
			continue
		}
		_ = p.Store.LogActivity(c.ctx, "human:ui", a.Action, nil, a.Detail)
	}
}

func (c *reqCtx) invitesReply(actor, projectID string, extra ...any) error {
	list, err := c.app.Org.ListInvites(c.ctx, actor, projectID)
	if err != nil {
		return orgErr(err)
	}
	settings, err := c.app.Org.InviteSettings(c.ctx, actor)
	if err != nil {
		return orgErr(err)
	}
	out := pyjson.NewObject("project_id", projectID, "invites", list, "settings", settings)
	for i := 0; i+1 < len(extra); i += 2 {
		out.Set(extra[i].(string), extra[i+1])
	}
	c.ok(out)
	return nil
}

// projectInvitesGet is GET /api/org/projects/{id}/invites.
func (c *reqCtx) projectInvitesGet(m []string) error {
	actor, err := c.inviteAdminGuard()
	if err != nil {
		return err
	}
	return c.invitesReply(actor, m[1])
}

// projectInvitesPost is POST /api/org/projects/{id}/invites.
func (c *reqCtx) projectInvitesPost(m []string) error {
	actor, err := c.inviteAdminGuard()
	if err != nil {
		return err
	}
	projectID := m[1]
	invite, token, activity, err := c.app.Org.CreateInvite(c.ctx, actor, projectID, c.body)
	if err != nil {
		return orgErr(err)
	}
	c.recordInviteActivity(*activity)
	return c.invitesReply(actor, projectID, "invite", invite, "url", InviteURL(c.inviteBaseURL(), token))
}

// projectInviteRevoke is POST /api/org/projects/{id}/invites/{inv}/revoke.
func (c *reqCtx) projectInviteRevoke(m []string) error {
	actor, err := c.inviteAdminGuard()
	if err != nil {
		return err
	}
	projectID := m[1]
	activity, err := c.app.Org.RevokeInvite(c.ctx, actor, projectID, m[2])
	if err != nil {
		return orgErr(err)
	}
	if activity != nil {
		c.recordInviteActivity(*activity)
	}
	return c.invitesReply(actor, projectID, "revoked", m[2])
}

// inviteSettingsGet is GET /api/org/invite-settings.
func (c *reqCtx) inviteSettingsGet() error {
	actor, err := c.inviteAdminGuard()
	if err != nil {
		return err
	}
	out, err := c.app.Org.InviteSettings(c.ctx, actor)
	if err != nil {
		return orgErr(err)
	}
	c.ok(pyjson.NewObject("settings", out))
	return nil
}

// inviteSettingsPost is POST /api/org/invite-settings.
func (c *reqCtx) inviteSettingsPost() error {
	actor, err := c.inviteAdminGuard()
	if err != nil {
		return err
	}
	v := c.body.Value("signup_enabled")
	enabled, isBool := v.(bool)
	if !isBool {
		return apiErr(400, "signup_enabled must be true or false", "INVITE_SETTINGS_INVALID")
	}
	out, err := c.app.Org.SetInviteSignup(c.ctx, actor, enabled)
	if err != nil {
		return orgErr(err)
	}
	c.ok(pyjson.NewObject("settings", out))
	return nil
}

// ------------------------------------------------------------ public side

// handleInvitePost answers the public invite routes (before the sign-in
// guard).  It reports whether the path was one of them.
func (c *reqCtx) handleInvitePost() (bool, error) {
	switch c.path {
	case invitePreviewPath, inviteAcceptPath, inviteSignupPath:
	default:
		return false, nil
	}
	if c.app.Auth == nil {
		c.drainBody()
		return true, invitesUnavailable()
	}
	if !c.sameOrigin() {
		c.drainBody()
		return true, apiErr(403, "Cross-site invite requests are rejected.", "AUTH_CSRF")
	}
	body, err := c.readLoginForm()
	if err != nil {
		return true, err
	}
	ip := c.clientIP()
	if ip == "" {
		ip = "unknown"
	}
	lim := &c.app.invites
	if lim.over("bad:"+ip, inviteBadLimit, inviteBadWindow) {
		return true, inviteRateLimited()
	}
	token := pytext.Strip(body["token"])
	session, err := c.authSession()
	if err != nil {
		return true, err
	}
	viewer := ""
	if session != nil {
		viewer = session.Username
	}
	// A token that cannot be used counts against the client.
	badToken := func(state string) {
		if !org.InviteValid(state) {
			lim.add("bad:"+ip, inviteBadWindow)
		}
	}
	switch c.path {
	case invitePreviewPath:
		out, err := c.app.Org.PreviewInvite(c.ctx, token, viewer)
		if err != nil {
			return true, err
		}
		state, _ := out.Value("state").(string)
		badToken(state)
		out.Set("ok", true).Set("signed_in", viewer != "")
		c.sendJSON(200, out, headerList{{"Cache-Control", "no-store"}, {"X-Robots-Tag", "noindex, nofollow"}})
		return true, nil
	case inviteAcceptPath:
		if session == nil {
			return true, apiErr(401, "Sign in to join this project.", "AUTH_REQUIRED")
		}
		res, err := c.app.Org.AcceptInvite(c.ctx, token, viewer)
		if err != nil {
			if oe, ok := err.(*org.Error); ok && strings.HasPrefix(oe.Code, "INVITE_") {
				lim.add("bad:"+ip, inviteBadWindow)
			}
			return true, orgErr(err)
		}
		c.recordInviteActivity(res.Activity...)
		c.app.Auth.SetSessionProject(session, res.ProjectID)
		if id, err := c.app.Org.UserIDFor(c.ctx, viewer); err == nil && id != "" {
			_ = c.app.Org.SetLastProject(c.ctx, id, res.ProjectID)
		}
		c.ok(res.Object())
		return true, nil
	}
	// Sign-up: the sign-in form's CSRF token proves the page came from here.
	if !c.app.Auth.VerifyLoginCSRF(body["csrf"]) {
		return true, apiErr(403, "The form expired or was not opened from this server. Reload and try again.", "AUTH_CSRF")
	}
	tokKey := "tok:" + org.InviteHash(token)
	if lim.over("signup:"+ip, inviteSignupIPLimit, inviteSignupIPWn) || lim.over(tokKey, inviteSignupTokLimit, inviteSignupTkW) {
		return true, inviteRateLimited()
	}
	lim.add("signup:"+ip, inviteSignupIPWn)
	if org.WellFormedInviteToken(token) {
		lim.add(tokKey, inviteSignupTkW)
	}
	lookup, username, err := c.app.Org.SignupCheck(c.ctx, token, body["username"], body["password"])
	if err != nil {
		if oe, ok := err.(*org.Error); ok && (oe.Code == "INVITE_INVALID" || oe.Code == "INVITE_EXPIRED" ||
			oe.Code == "INVITE_REVOKED" || oe.Code == "INVITE_USED_UP") {
			lim.add("bad:"+ip, inviteBadWindow)
		}
		return true, orgErr(err)
	}
	hash, err := auth.HashPassword(body["password"], auth.Iterations) // outside every lock
	if err != nil {
		return true, apiErr(400, err.Error(), "PASSWORD_INVALID")
	}
	res, err := c.app.Org.SignupWithInvite(c.ctx, lookup, username, body["display_name"], hash)
	if err != nil {
		return true, orgErr(err)
	}
	c.recordInviteActivity(res.Activity...)
	cookie, s := c.app.Auth.IssueSession(res.Username, false)
	c.app.Auth.SetSessionProject(s, res.ProjectID)
	c.sendJSON(200, res.Object().Set("created", true), headerList{{"Set-Cookie", c.app.Auth.CookieHeader(cookie, c.secure())},
		{"Cache-Control", "no-store"}})
	return true, nil
}
