package server

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"

	"arkstudio/go/internal/auth"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/compat/pyurl"
)

// orgPorted are the contract routes this file answers (router.go registers them).
var orgPorted = []PortedRoute{
	{"exact:/api/org/password", "POST"},
	{"exact:/api/org/teams", "POST"},
	{"exact:/api/org/users", "POST"},
	{"exact:/api/personal/agent-tokens", "POST"},
	{"regex:^/api/org/projects/([^/]+)/grants$", "POST"},
	{"regex:^/api/org/projects/([^/]+)/grants/revoke$", "POST"},
	{"regex:^/api/org/teams/([^/]+)$", "DELETE"},
	{"regex:^/api/org/teams/([^/]+)/members$", "POST"},
	{"regex:^/api/org/teams/([^/]+)/members/([^/]+)$", "DELETE"},
	{"regex:^/api/org/users/([^/]+)$", "DELETE"},
	{"regex:^/api/org/users/([^/]+)/linux-accounts$", "POST"},
	{"regex:^/api/org/users/([^/]+)/linux-accounts/([^/]+)$", "DELETE"},
	{"regex:^/api/org/users/([^/]+)/password$", "POST"},
	{"regex:^/api/personal/agent-tokens/([A-Za-z0-9_]+)$", "DELETE"},
}

// Organization administration, personal agent tokens and the self-service
// profile writes (do_POST / do_PATCH / do_DELETE org branches and
// Handler._handle_profile).

var (
	reOrgUserPassword = pyRe(`^/api/org/users/([^/]+)/password$`, false)
	reOrgLinuxPost    = pyRe(`^/api/org/users/([^/]+)/linux-accounts$`, false)
	reOrgTeamMembers  = pyRe(`^/api/org/teams/([^/]+)/members$`, false)
	reOrgGrants       = pyRe(`^/api/org/projects/([^/]+)/grants$`, false)
	reOrgGrantsRevoke = pyRe(`^/api/org/projects/([^/]+)/grants/revoke$`, false)
)

func (c *reqCtx) withList(key string, item any, listKey string, list []any, err error) error {
	if err != nil {
		return err
	}
	c.ok(pyjson.NewObject(key, item, listKey, list))
	return nil
}

// orgPost handles the org/personal POST branches of do_POST.
func (c *reqCtx) orgPost(path string, body *pyjson.Object) (bool, error) {
	o, actor := c.app.Org, c.actor()
	switch {
	case path == "/api/org/users":
		user, err := o.CreateUser(c.ctx, actor, body)
		if err != nil {
			return true, err
		}
		users, err := o.ListMembers(c.ctx)
		return true, c.withList("user", user, "users", users, err)
	case path == "/api/org/password":
		result, err := o.ChangePassword(c.ctx, actor, body)
		if err != nil {
			return true, err
		}
		if c.app.Auth != nil {
			current, err := c.authSession()
			if err != nil {
				return true, err
			}
			except := ""
			if current != nil {
				except = current.SidHash
			}
			result.Set("sessions_revoked", int64(c.app.Auth.RevokeUser(actor, except)))
			result.Set("devices_revoked", int64(c.app.Mobile.RevokeUser(actor, "")))
		}
		c.ok(result)
		return true, nil
	case reOrgUserPassword.MatchString(path):
		id := reOrgUserPassword.FindStringSubmatch(path)[1]
		result, err := o.ResetPassword(c.ctx, actor, id, body)
		if err != nil {
			return true, err
		}
		target, ok, err := o.UserByID(c.ctx, id)
		if err != nil {
			return true, err
		}
		if c.app.Auth != nil && ok {
			result.Set("sessions_revoked", int64(c.app.Auth.RevokeUser(target.Str("username"), "")))
		}
		c.ok(result)
		return true, nil
	case reOrgLinuxPost.MatchString(path):
		result, err := o.AssignLinuxAccount(c.ctx, actor, reOrgLinuxPost.FindStringSubmatch(path)[1], body)
		if err != nil {
			return true, err
		}
		accounts, err := o.LinuxAccountsMap(c.ctx)
		if err != nil {
			return true, err
		}
		c.ok(pyjson.NewObject("assignment", result, "linux_accounts", accounts))
		return true, nil
	case path == "/api/org/teams":
		team, err := o.CreateTeam(c.ctx, actor, body)
		if err != nil {
			return true, err
		}
		teams, err := o.ListTeams(c.ctx)
		return true, c.withList("team", team, "teams", teams, err)
	case reOrgTeamMembers.MatchString(path):
		result, err := o.AddTeamMember(c.ctx, actor, reOrgTeamMembers.FindStringSubmatch(path)[1], body)
		if err != nil {
			return true, err
		}
		c.ok(result)
		return true, nil
	case reOrgGrants.MatchString(path), reOrgGrantsRevoke.MatchString(path):
		revoke := reOrgGrantsRevoke.MatchString(path)
		var projectID string
		var result *pyjson.Object
		var err error
		if revoke {
			projectID = reOrgGrantsRevoke.FindStringSubmatch(path)[1]
			result, err = o.RevokeProject(c.ctx, actor, projectID, body)
		} else {
			projectID = reOrgGrants.FindStringSubmatch(path)[1]
			result, err = o.GrantProject(c.ctx, actor, projectID, body)
		}
		if err != nil {
			return true, err
		}
		detail, err := o.ProjectDetail(c.ctx, actor, projectID)
		if err != nil {
			return true, err
		}
		key := "grant"
		if revoke {
			key = "result"
		}
		c.ok(pyjson.NewObject(key, result, "project", detail))
		return true, nil
	case path == "/api/personal/agent-tokens":
		user, err := c.personalTokensGuard()
		if err != nil {
			return true, err
		}
		result, err := o.CreateAgentToken(c.ctx, user, body)
		if err != nil {
			return true, err
		}
		c.ok(result)
		return true, nil
	}
	return false, nil
}

// personalTokensGuard is Handler._personal_agent_tokens_guard.
func (c *reqCtx) personalTokensGuard() (string, error) {
	if c.token != nil || c.agentToken() != "" || c.header("X-ARK-Read-Token") != "" {
		return "", apiErr(403, "Agent and task tokens cannot manage personal agent tokens.", "AGENT_TOKEN_FORBIDDEN")
	}
	if c.mobileSession != nil || c.mobileToken() != "" {
		return "", apiErr(403, "A paired phone cannot manage personal agent tokens.", "MOBILE_AGENT_TOKENS_BLOCKED")
	}
	user := c.actor()
	if user == "" || (c.app.Auth != nil && c.session == nil) {
		return "", apiErr(401, "Sign in with the ARK account to continue.", "AUTH_REQUIRED")
	}
	return user, nil
}

// personalTokensGet is GET /api/personal/agent-tokens.
func (c *reqCtx) personalTokensGet() error {
	user, err := c.personalTokensGuard()
	if err != nil {
		return err
	}
	result, err := c.app.Org.ListAgentTokens(c.ctx, user)
	if err != nil {
		return err
	}
	c.ok(result)
	return nil
}

// orgPatch handles PATCH /api/org/users/<id> and /api/org/teams/<id>.
func (c *reqCtx) orgPatch(path string, body *pyjson.Object) (bool, error) {
	o, actor := c.app.Org, c.actor()
	if m := reOrgUser.FindStringSubmatch(path); m != nil {
		user, err := o.UpdateUser(c.ctx, actor, m[1], body)
		if err != nil {
			return true, err
		}
		if pyjson.Truthy(body.Value("disabled")) && c.app.Auth != nil {
			c.app.Auth.RevokeUser(pyjson.Str(user.Value("username")), "")
		}
		users, err := o.ListMembers(c.ctx)
		return true, c.withList("user", user, "users", users, err)
	}
	if m := reOrgTeam.FindStringSubmatch(path); m != nil {
		team, err := o.UpdateTeam(c.ctx, actor, m[1], body)
		if err != nil {
			return true, err
		}
		teams, err := o.ListTeams(c.ctx)
		return true, c.withList("team", team, "teams", teams, err)
	}
	return false, nil
}

// orgDelete handles the personal-token and org DELETE branches.
func (c *reqCtx) orgDelete(path string) (bool, error) {
	o, actor := c.app.Org, c.actor()
	var result *pyjson.Object
	var err error
	switch {
	case rePersonalTok.MatchString(path):
		user, gerr := c.personalTokensGuard()
		if gerr != nil {
			return true, gerr
		}
		result, err = o.RevokeAgentToken(c.ctx, user, rePersonalTok.FindStringSubmatch(path)[1])
	case reOrgUser.MatchString(path):
		// Python looks the target up after deleting it, so it never revokes
		// sessions here (the account cannot sign in again anyway).
		result, err = o.DeleteUser(c.ctx, actor, reOrgUser.FindStringSubmatch(path)[1])
	case reLinuxAccount.MatchString(path):
		m := reLinuxAccount.FindStringSubmatch(path)
		var removed *pyjson.Object
		if removed, err = o.RevokeLinuxAccount(c.ctx, actor, m[1], pyurl.Unquote(m[2])); err == nil {
			accounts, aerr := o.LinuxAccountsMap(c.ctx)
			if aerr != nil {
				return true, aerr
			}
			result = pyjson.NewObject("result", removed, "linux_accounts", accounts)
		}
	case reOrgTeam.MatchString(path):
		result, err = o.DeleteTeam(c.ctx, actor, reOrgTeam.FindStringSubmatch(path)[1])
	case reTeamMember.MatchString(path):
		m := reTeamMember.FindStringSubmatch(path)
		result, err = o.RemoveTeamMember(c.ctx, actor, m[1], m[2])
	default:
		return false, nil
	}
	if err != nil {
		return true, err
	}
	c.ok(result)
	return true, nil
}

// profileWrite is Handler._handle_profile for POST/PATCH/DELETE.
func (c *reqCtx) profileWrite(path string) error {
	if err := c.rejectTaskReadToken("read or change account profiles"); err != nil {
		return err
	}
	username := c.actor()
	if username == "" {
		return apiErr(401, "Sign in with the ARK account to continue.", "AUTH_REQUIRED")
	}
	method := c.r.Method
	if reAvatar.MatchString(path) {
		return apiErr(405, "Method not allowed", "")
	}
	o := c.app.Org
	reply := func(err error) error {
		if err != nil {
			return err
		}
		data, err := c.profilePayload()
		if err != nil {
			return orgErr(err)
		}
		c.ok(data)
		return nil
	}
	switch {
	case path == "/api/me" && method == "PATCH":
		body, err := c.readJSON()
		if err != nil {
			return err
		}
		_, err = o.UpdateProfile(c.ctx, username, body)
		return reply(err)
	case path == "/api/me/avatar" && method == "POST":
		if raw := c.header("Content-Length"); raw != "" {
			n, err := pytext.Int(raw)
			if err != nil {
				return apiErr(400, err.Error(), "")
			}
			if n > 200*1024*2 {
				return apiErr(413, "avatar image is larger than 200 KB", "AVATAR_TOO_LARGE")
			}
		}
		body, err := c.readJSON()
		if err != nil {
			return err
		}
		encoded := strArgServer(body.Value("data"))
		if strings.Contains(encoded, ",") && strings.HasPrefix(encoded, "data:") {
			encoded = strings.SplitN(encoded, ",", 2)[1]
		}
		raw, ok := strictBase64(encoded)
		if !ok {
			return apiErr(400, "avatar data must be base64", "AVATAR_INVALID")
		}
		_, err = o.SetAvatarImage(c.ctx, username, raw, strArgServer(body.Value("content_type")))
		return reply(err)
	case path == "/api/me/avatar" && method == "DELETE":
		_, err := o.RemoveAvatar(c.ctx, username)
		return reply(err)
	case path == "/api/me/password" && method == "POST":
		return c.changeOwnPassword()
	}
	return apiErr(405, "Method not allowed", "")
}

func strArgServer(v any) string {
	if !pyjson.Truthy(v) {
		return ""
	}
	return pyjson.Str(v)
}

// strictBase64 is base64.b64decode(s, validate=True).
func strictBase64(s string) ([]byte, bool) {
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '+' || r == '/' || r == '=') {
			return nil, false
		}
	}
	out, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, false
	}
	return out, true
}

// changeOwnPassword is Handler._change_own_password.
func (c *reqCtx) changeOwnPassword() error {
	if c.app.Auth == nil {
		c.drainBody()
		return apiErr(409, "This local ARK has no sign-in password to change.", "LOCAL_MODE")
	}
	if c.mobileSession != nil {
		return apiErr(403, "Change your password from a signed-in browser.", "MOBILE_SCOPE")
	}
	session, err := c.authSession()
	if err != nil {
		return err
	}
	if session == nil {
		return apiErr(401, "Sign in with the ARK account to continue.", "AUTH_REQUIRED")
	}
	body, err := c.readJSON()
	if err != nil {
		return err
	}
	current := strArgServer(body.Value("current_password"))
	next := strArgServer(body.Value("new_password"))
	confirm := strArgServer(body.Value("confirm_password"))
	if current == "" || next == "" {
		return apiErr(400, "Enter your current password and a new password.", "PASSWORD_REQUIRED")
	}
	if confirm != next {
		return apiErr(400, "The new passwords do not match.", "PASSWORD_MISMATCH")
	}
	if next == current {
		return apiErr(400, "The new password must differ from the current one.", "PASSWORD_UNCHANGED")
	}
	if pytext.Len(next) < 8 {
		return apiErr(400, "The new password must be at least 8 characters.", "PASSWORD_TOO_SHORT")
	}
	username, err := c.app.Auth.VerifyCredentials(c.ctx, session.Username, current, c.clientIP())
	if err != nil {
		var ae *auth.Error
		if errors.As(err, &ae) {
			if ae.Status == 429 {
				retry := ae.RetryAfter
				if retry == 0 {
					retry = 60
				}
				c.sendJSON(429, pyjson.NewObject("ok", false, "error", pyjson.NewObject("status", int64(429),
					"code", ae.Code, "message", ae.Message)), headerList{{"Retry-After", strconv.Itoa(max(1, retry))}})
				return nil
			}
			return apiErr(403, "The current password is incorrect.", "CURRENT_PASSWORD_INVALID")
		}
		return err
	}
	hash, err := auth.HashPassword(next, auth.Iterations)
	if err != nil {
		return apiErr(400, err.Error(), "PASSWORD_INVALID")
	}
	if err := c.app.Org.SetPasswordHash(c.ctx, username, hash); err != nil {
		return err
	}
	revoked := c.app.Auth.RevokeUser(username, session.SidHash)
	devices := c.app.Mobile.RevokeUser(username, "")
	tokens := int64(0)
	if body.Value("revoke_agent_tokens") == true {
		if tokens, err = c.app.Org.RevokeUserTokens(c.ctx, username); err != nil {
			return err
		}
	}
	c.ok(pyjson.NewObject("changed", true, "sessions_revoked", int64(revoked), "devices_revoked", int64(devices),
		"agent_tokens_revoked", tokens))
	return nil
}
