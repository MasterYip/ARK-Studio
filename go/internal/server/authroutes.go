package server

import (
	"io"
	"strconv"
	"strings"
	"time"

	"arkstudio/go/internal/auth"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pytext"
	"arkstudio/go/internal/pyurl"
)

// handleAuthGet is Handler._handle_auth_get.
func (c *reqCtx) handleAuthGet() (bool, error) {
	switch c.path {
	case "/logout":
		var headers headerList
		if c.app.Auth != nil {
			headers = headerList{{"Set-Cookie", c.app.Auth.ClearCookieHeader(c.secure())}}
			c.app.Auth.Revoke(auth.ParseCookie(c.header("Cookie"), auth.CookieName))
		}
		c.redirect(c.externalPrefix(), 303, headers)
		return true, nil
	case "/api/auth/session":
		session, err := c.authSession()
		if err != nil {
			return true, err
		}
		return true, c.sendAuthSession(session)
	case "/login":
		if c.app.Auth == nil {
			c.redirect(c.externalPrefix(), 303, nil)
			return true, nil
		}
		session, err := c.authSession()
		if err != nil {
			return true, err
		}
		next := c.cleanAuthNext(c.query.First("next", ""))
		if strings.HasSuffix(strings.TrimRight(next, "/"), "/login") {
			next = c.externalPrefix()
		}
		if session != nil {
			c.redirect(next, 303, nil)
			return true, nil
		}
		return true, c.renderLogin(200, "", next, "", 0)
	}
	return false, nil
}

// sendAuthSession is Handler._send_auth_session.
func (c *reqCtx) sendAuthSession(session *auth.Session) error {
	enabled := c.app.Auth != nil
	var role, orgObj, user, expires any
	isAdmin := !enabled
	if enabled && session != nil {
		r, ok, err := c.app.Org.RoleFor(c.ctx, session.Username)
		if err != nil {
			return err
		}
		if ok {
			role = r
			isAdmin = org.RoleRank[r] >= org.RoleRank["admin"]
		}
		row, _, err := c.app.Org.Org(c.ctx)
		if err != nil {
			return err
		}
		orgObj = pyjson.NewObject("id", row.Get("id"), "name", row.Get("name"))
	}
	if !enabled {
		role, user = "owner", org.LocalUsername
	}
	if session != nil {
		user = session.Username
		expires = time.Unix(session.ExpiresAt, 0).UTC().Format("2006-01-02T15:04:05") + "+00:00"
	}
	c.ok(pyjson.NewObject("auth_required", enabled, "authenticated", session != nil || !enabled,
		"user", user, "expires_at", expires, "role", role, "is_admin", isAdmin, "local", !enabled, "org", orgObj))
	return nil
}

// renderLogin is Handler._render_login.
func (c *reqCtx) renderLogin(status int, errText, next, username string, retryAfter int) error {
	template, err := c.app.Assets.Read("login.html")
	if err != nil {
		return apiErr(500, "Login page asset is missing: "+err.Error(), "")
	}
	transport := "This page is served over plain HTTP. Use an HTTPS reverse proxy " +
		"before exposing ARK to the public internet; credentials and the " +
		"session cookie are otherwise readable on the network."
	if c.secure() {
		transport = "This page is served over HTTPS."
	}
	errorHTML := ""
	if errText != "" {
		errorHTML = `<div class="login-error" role="alert"><strong>Sign-in failed.</strong> ` + pyHTMLEscape(errText) + `</div>`
	}
	page := universalNewlines(string(template)) // Path.read_text()
	for _, kv := range [][2]string{
		{"{{ERROR}}", errorHTML},
		{"{{NEXT}}", pyHTMLEscape(auth.SafeNextPath(next, "/"))},
		{"{{CSRF}}", pyHTMLEscape(c.app.Auth.LoginCSRF())},
		{"{{USERNAME}}", pyHTMLEscape(username)},
		{"{{TRANSPORT}}", pyHTMLEscape(transport)},
	} {
		page = strings.ReplaceAll(page, kv[0], kv[1])
	}
	page = VersionedHTML(page, c.app.Assets.Version())
	headers := headerList{{"X-Robots-Tag", "noindex, nofollow"}}
	if status == 429 {
		headers = append(headers, [2]string{"Retry-After", strconv.Itoa(max(1, retryAfter))})
	}
	c.send(status, []byte(page), "text/html; charset=utf-8", headers, true)
	return nil
}

// pyHTMLEscape is html.escape(s, quote=True) (Go's escapes ' as &#39;).
func pyHTMLEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#x27;").Replace(s)
}

// handleAuthPost is Handler._handle_auth_post.
func (c *reqCtx) handleAuthPost() (bool, error) {
	switch c.path {
	case "/api/auth/logout":
		if !c.sameOrigin() {
			return true, apiErr(403, "Cross-origin sign-out is rejected", "AUTH_CSRF")
		}
		c.drainBody()
		revoked := false
		var headers headerList
		if c.app.Auth != nil {
			revoked = c.app.Auth.Revoke(auth.ParseCookie(c.header("Cookie"), auth.CookieName))
			headers = headerList{{"Set-Cookie", c.app.Auth.ClearCookieHeader(c.secure())}}
		}
		c.sendJSON(200, pyjson.NewObject("ok", true, "logged_out", revoked), headers)
		return true, nil
	case "/logout":
		if !c.sameOrigin() {
			return true, apiErr(403, "Cross-origin sign-out is rejected", "AUTH_CSRF")
		}
		c.drainBody()
		var headers headerList
		if c.app.Auth != nil {
			c.app.Auth.Revoke(auth.ParseCookie(c.header("Cookie"), auth.CookieName))
			headers = headerList{{"Set-Cookie", c.app.Auth.ClearCookieHeader(c.secure())}}
		}
		c.redirect(c.externalPrefix()+"login", 303, headers)
		return true, nil
	case "/login":
	default:
		return false, nil
	}
	if c.app.Auth == nil {
		c.redirect(c.externalPrefix(), 303, nil)
		return true, nil
	}
	if !c.sameOrigin() {
		return true, c.renderLogin(403, "Cross-site sign-in attempts are rejected.", c.externalPrefix(), "", 0)
	}
	body, err := c.readLoginForm()
	if err != nil {
		return true, err
	}
	next := c.cleanAuthNext(body["next"])
	username := body["username"]
	if !c.app.Auth.VerifyLoginCSRF(body["csrf"]) {
		return true, c.renderLogin(403, "The sign-in form expired or was not opened from this server. Reload and try again.", next, username, 0)
	}
	remember := strings.ToLower(body["remember"])
	token, _, err := c.app.Auth.Login(c.ctx, username, body["password"], c.clientIP(),
		remember == "1" || remember == "on" || remember == "true" || remember == "yes")
	if err != nil {
		if ae, ok := err.(*auth.Error); ok {
			retry := ae.RetryAfter
			if retry == 0 {
				retry = 60
			}
			return true, c.renderLogin(ae.Status, ae.Message, next, username, retry)
		}
		return true, err
	}
	if strings.HasSuffix(strings.TrimRight(next, "/"), "/login") {
		next = c.externalPrefix()
	}
	c.redirect(next, 303, headerList{{"Set-Cookie", c.app.Auth.CookieHeader(token, c.secure())}})
	return true, nil
}

func (c *reqCtx) drainBody() {
	n, err := contentLength(c.header("Content-Length"))
	if err == nil && n > 0 && n <= maxBody && !c.bodyConsumed {
		c.bodyConsumed = true
		io.CopyN(io.Discard, c.r.Body, n) //nolint:errcheck
	}
}

// readLoginForm is Handler._read_login_form (values: str() of JSON values).
func (c *reqCtx) readLoginForm() (map[string]string, error) {
	length, err := contentLength(c.header("Content-Length"))
	if err != nil {
		return nil, apiErr(400, "Invalid Content-Length", "")
	}
	if length > maxLoginBody {
		return nil, apiErr(413, "Login request body is too large", "")
	}
	raw := []byte{}
	c.bodyConsumed = true
	if length > 0 {
		raw = make([]byte, length)
		if _, err := io.ReadFull(c.r.Body, raw); err != nil {
			return nil, apiErr(400, "Login form must be UTF-8", "")
		}
	}
	ctype := strings.ToLower(pytext.Strip(strings.SplitN(c.header("Content-Type"), ";", 2)[0]))
	out := map[string]string{}
	text := pyurl.DecodeStrict(raw)
	if ctype == "application/json" {
		if text == nil {
			return nil, apiErr(400, "Login request must be JSON or a form", "")
		}
		t := *text
		if t == "" {
			t = "{}"
		}
		v, err := pyjson.Loads(t)
		if err != nil {
			return nil, apiErr(400, "Login request must be JSON or a form", "")
		}
		if o, ok := v.(*pyjson.Object); ok {
			for _, k := range o.Keys() {
				if pyjson.Truthy(o.Value(k)) {
					out[k] = pyjson.Str(o.Value(k))
				}
			}
		}
		return out, nil
	}
	if text == nil {
		return nil, apiErr(400, "Login form must be UTF-8", "")
	}
	for k, values := range pyurl.ParseQSKeepBlank(*text) {
		out[k] = values[len(values)-1]
	}
	return out, nil
}
