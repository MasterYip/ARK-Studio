package server

// Project invite links (Go only, routes_invites.go / org/invites.go).

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/sqlitedb"
)

func createInvite(t *testing.T, app *App, user, projectID string, body map[string]any) (string, map[string]any) {
	t.Helper()
	r := gpuCall(t, app, "POST", "/api/org/projects/"+projectID+"/invites", body, sessionCookie(app, user))
	link, _ := r.body["url"].(string)
	inv, _ := r.body["invite"].(map[string]any)
	if r.code != 200 || !strings.Contains(link, "#/join/") || inv == nil {
		t.Fatalf("create invite as %s: %d %s", user, r.code, r.raw)
	}
	return link[strings.Index(link, "#/join/")+len("#/join/"):], inv
}

func invitePost(t *testing.T, app *App, path string, body map[string]any, headers map[string]string) gpuReply {
	t.Helper()
	return gpuCall(t, app, "POST", path, body, headers)
}

func signup(t *testing.T, app *App, token, username, password string) gpuReply {
	t.Helper()
	return invitePost(t, app, inviteSignupPath, map[string]any{"token": token,
		"csrf": app.Auth.LoginCSRF(), "username": username, "display_name": "New " + username, "password": password}, nil)
}

func TestInviteCreateListRevoke(t *testing.T) {
	app, projectID := authDesktopApp(t)
	ctx := context.Background()
	token, inv := createInvite(t, app, "owner", projectID, map[string]any{"label": "Lab", "max_uses": 5})
	if inv["permission"] != "write" || inv["max_uses"] != float64(5) || inv["allow_signup"] != true ||
		inv["state"] != "valid" || inv["label"] != "Lab" || inv["hint"] != token[:4] {
		t.Errorf("defaults: %v", inv)
	}
	if len(token) < 32 {
		t.Errorf("token too short: %q", token)
	}
	// Only the hash is stored.
	if err := app.Org.DB.Read(ctx, func(q sqlitedb.Querier) error {
		rows, err := sqlitedb.Query(ctx, q, "SELECT * FROM project_invites")
		for _, r := range rows {
			for _, col := range r.Cols {
				if v := r.Str(col); strings.Contains(v, token) {
					t.Errorf("column %s holds the token", col)
				}
			}
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if r := gpuCall(t, app, "GET", "/api/org/projects/"+projectID+"/invites", nil, sessionCookie(app, "owner")); r.code != 200 ||
		len(r.body["invites"].([]any)) != 1 || strings.Contains(r.raw, token) {
		t.Fatalf("list: %d %s", r.code, r.raw)
	}
	// ana has read access only: no invite management.
	if r := gpuCall(t, app, "POST", "/api/org/projects/"+projectID+"/invites", map[string]any{}, sessionCookie(app, "ana")); r.code != 403 {
		t.Errorf("read user creates: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "GET", "/api/org/projects/"+projectID+"/invites", nil, sessionCookie(app, "ana")); r.code != 403 {
		t.Errorf("read user lists: %d %s", r.code, r.raw)
	}
	// Bad options.
	for _, body := range []map[string]any{{"permission": "owner"}, {"expires_days": 3}, {"max_uses": -1}, {"max_uses": 5000}} {
		if r := gpuCall(t, app, "POST", "/api/org/projects/"+projectID+"/invites", body, sessionCookie(app, "owner")); r.code != 400 {
			t.Errorf("%v: %d %s", body, r.code, r.raw)
		}
	}
	// Revoke: the link stops working.
	r := gpuCall(t, app, "POST", "/api/org/projects/"+projectID+"/invites/"+inv["id"].(string)+"/revoke", map[string]any{}, sessionCookie(app, "owner"))
	if r.code != 200 || r.body["invites"].([]any)[0].(map[string]any)["state"] != "revoked" {
		t.Fatalf("revoke: %d %s", r.code, r.raw)
	}
	p := invitePost(t, app, invitePreviewPath, map[string]any{"token": token}, nil)
	if p.code != 200 || p.body["state"] != "revoked" || p.body["project"] != nil {
		t.Errorf("preview revoked: %d %s", p.code, p.raw)
	}
	if s := signup(t, app, token, "newbie", "longpassword"); s.code != 410 || s.errCode() != "INVITE_REVOKED" {
		t.Errorf("signup on revoked: %d %s", s.code, s.raw)
	}
}

func TestInviteProjectAdminAndPermissionCap(t *testing.T) {
	app, projectID := authDesktopApp(t)
	ctx := context.Background()
	anaID, _ := app.Org.UserIDFor(ctx, "ana")
	ownerID, _ := app.Org.UserIDFor(ctx, "owner")
	// ana administers the project without being an org admin.
	if err := app.Org.SetProjectGrant(ctx, projectID, anaID, "admin", ownerID); err != nil {
		t.Fatal(err)
	}
	token, inv := createInvite(t, app, "ana", projectID, map[string]any{"permission": "admin", "expires_days": 1})
	if inv["permission"] != "admin" {
		t.Errorf("admin invite: %v", inv)
	}
	// ana loses admin: her links stop working.
	if err := app.Org.SetProjectGrant(ctx, projectID, anaID, "write", ownerID); err != nil {
		t.Fatal(err)
	}
	if p := invitePost(t, app, invitePreviewPath, map[string]any{"token": token}, nil); p.body["state"] != "revoked" {
		t.Errorf("creator lost admin: %s", p.raw)
	}
	if r := gpuCall(t, app, "POST", "/api/org/projects/"+projectID+"/invites", map[string]any{"permission": "write"}, sessionCookie(app, "ana")); r.code != 403 {
		t.Errorf("write user creates: %d %s", r.code, r.raw)
	}
	if _, _, _, err := app.Org.CreateInvite(ctx, "vic", projectID, pyjson.NewObject("permission", "read")); err == nil {
		t.Errorf("viewer created an invite")
	}
}

func TestInviteSignupJoinsAndSignsIn(t *testing.T) {
	app, projectID := authDesktopApp(t)
	ctx := context.Background()
	token, _ := createInvite(t, app, "owner", projectID, map[string]any{"permission": "write", "max_uses": 1})

	p := invitePost(t, app, invitePreviewPath, map[string]any{"token": token}, nil)
	project, _ := p.body["project"].(map[string]any)
	inviter, _ := p.body["inviter"].(map[string]any)
	if p.code != 200 || p.body["state"] != "valid" || project["name"] != "Team" || inviter["username"] != "owner" ||
		p.body["permission"] != "write" || p.body["signup"] != true || p.body["signed_in"] != false {
		t.Fatalf("preview: %d %s", p.code, p.raw)
	}
	// Validation: short password, bad name, taken name, missing CSRF.
	if s := signup(t, app, token, "newbie", "short"); s.code != 400 || s.errCode() != "PASSWORD_TOO_SHORT" {
		t.Errorf("short password: %d %s", s.code, s.raw)
	}
	if s := signup(t, app, token, "bad name!", "longpassword"); s.code != 400 {
		t.Errorf("bad username: %d %s", s.code, s.raw)
	}
	for _, taken := range []string{"ANA", "local"} {
		if s := signup(t, app, token, taken, "longpassword"); s.code != 409 || s.errCode() != "USER_EXISTS" ||
			!strings.Contains(s.raw, "This username is taken.") {
			t.Errorf("taken %s: %d %s", taken, s.code, s.raw)
		}
	}
	if s := invitePost(t, app, inviteSignupPath, map[string]any{"token": token, "username": "x1", "password": "longpassword"}, nil); s.code != 403 {
		t.Errorf("no csrf: %d %s", s.code, s.raw)
	}
	if s := invitePost(t, app, inviteSignupPath, map[string]any{"token": token, "csrf": app.Auth.LoginCSRF(), "username": "x1",
		"password": "longpassword"}, map[string]string{"Origin": "https://evil.example"}); s.code != 403 {
		t.Errorf("cross-site: %d %s", s.code, s.raw)
	}

	s := signup(t, app, token, "newbie", "longpassword")
	if s.code != 200 || s.body["created"] != true || s.body["permission"] != "write" || s.body["user"] != "newbie" {
		t.Fatalf("signup: %d %s", s.code, s.raw)
	}
	if perm, _ := app.Org.ProjectPermission(ctx, "newbie", projectID); perm != "write" {
		t.Errorf("permission after signup: %q", perm)
	}
	if role, _, _ := app.Org.RoleFor(ctx, "newbie"); role != "member" {
		t.Errorf("org role: %q", role)
	}
	// The new account can sign in with its password and lands in the project.
	if name, err := app.Auth.VerifyCredentials(ctx, "newbie", "longpassword", "10.0.0.9"); err != nil || name != "newbie" {
		t.Errorf("sign in: %q %v", name, err)
	}
	if b := gpuCall(t, app, "GET", "/api/bootstrap", nil, sessionCookie(app, "newbie")); b.code != 200 ||
		b.body["summary"].(map[string]any)["id"] != projectID {
		t.Errorf("bootstrap: %d %.300s", b.code, b.raw)
	}
	// One use: the link is used up.
	if p := invitePost(t, app, invitePreviewPath, map[string]any{"token": token}, nil); p.body["state"] != "used_up" {
		t.Errorf("after use: %s", p.raw)
	}
	if s := signup(t, app, token, "second", "longpassword"); s.code != 410 || s.errCode() != "INVITE_USED_UP" {
		t.Errorf("second signup: %d %s", s.code, s.raw)
	}
	// Activity and audit name the new account.
	act := gpuCall(t, app, "GET", "/api/activity?limit=50", nil, map[string]string{"Cookie": sessionCookie(app, "owner")["Cookie"],
		"X-ARK-Project": projectID})
	if !strings.Contains(act.raw, "project.member.join") || !strings.Contains(act.raw, "project.invite.use") ||
		!strings.Contains(act.raw, "project.invite.create") || !strings.Contains(act.raw, "newbie") {
		t.Errorf("activity: %.600s", act.raw)
	}
	audit := gpuCall(t, app, "GET", "/api/org/audit", nil, sessionCookie(app, "owner"))
	if !strings.Contains(audit.raw, "org.user.signup") || !strings.Contains(audit.raw, "project.member.join") {
		t.Errorf("audit: %.600s", audit.raw)
	}
	if strings.Contains(act.raw, token) || strings.Contains(audit.raw, token) {
		t.Errorf("token in a log")
	}
}

func TestInviteAcceptExistingAccount(t *testing.T) {
	app, projectID := authDesktopApp(t)
	ctx := context.Background()
	token, inv := createInvite(t, app, "owner", projectID, map[string]any{"permission": "write", "max_uses": 2})
	// Not signed in: accept needs a session.
	if r := invitePost(t, app, inviteAcceptPath, map[string]any{"token": token}, nil); r.code != 401 {
		t.Errorf("anonymous accept: %d %s", r.code, r.raw)
	}
	// vic (read) is upgraded to write.
	p := invitePost(t, app, invitePreviewPath, map[string]any{"token": token}, sessionCookie(app, "vic"))
	viewer, _ := p.body["viewer"].(map[string]any)
	if p.body["signed_in"] != true || viewer["permission"] != "read" || viewer["member"] != false {
		t.Errorf("preview as vic: %s", p.raw)
	}
	r := invitePost(t, app, inviteAcceptPath, map[string]any{"token": token}, sessionCookie(app, "vic"))
	if r.code != 200 || r.body["already_member"] != false || r.body["permission"] != "write" {
		t.Fatalf("accept: %d %s", r.code, r.raw)
	}
	if perm, _ := app.Org.ProjectPermission(ctx, "vic", projectID); perm != "write" {
		t.Errorf("vic now: %q", perm)
	}
	// The owner already has more: nothing changes, no use is spent.
	r = invitePost(t, app, inviteAcceptPath, map[string]any{"token": token}, sessionCookie(app, "owner"))
	if r.code != 200 || r.body["already_member"] != true || r.body["permission"] != "admin" {
		t.Errorf("owner accept: %d %s", r.code, r.raw)
	}
	list := gpuCall(t, app, "GET", "/api/org/projects/"+projectID+"/invites", nil, sessionCookie(app, "owner"))
	if got := list.body["invites"].([]any)[0].(map[string]any); got["id"] != inv["id"] || got["uses"] != float64(1) {
		t.Errorf("uses: %v", got)
	}
	// A brand-new org member without access gets member + grant.
	if _, err := app.Org.CreateUser(ctx, "owner", pyjson.NewObject("username", "zed", "password", desktopPassword, "role", "member")); err != nil {
		t.Fatal(err)
	}
	if r := invitePost(t, app, inviteAcceptPath, map[string]any{"token": token}, sessionCookie(app, "zed")); r.code != 200 {
		t.Errorf("zed: %d %s", r.code, r.raw)
	}
	if r := invitePost(t, app, inviteAcceptPath, map[string]any{"token": token}, sessionCookie(app, "ana")); r.code != 410 ||
		r.errCode() != "INVITE_USED_UP" {
		t.Errorf("third use: %d %s", r.code, r.raw)
	}
}

func TestInviteExpiryAndSignupSwitches(t *testing.T) {
	app, projectID := authDesktopApp(t)
	ctx := context.Background()
	token, inv := createInvite(t, app, "owner", projectID, map[string]any{"max_uses": 0})
	if err := app.Org.DB.Write(ctx, func(q sqlitedb.Querier) error {
		_, err := q.ExecContext(ctx, "UPDATE project_invites SET expires_at='2020-01-01T00:00:00+00:00' WHERE id=?", inv["id"])
		return err
	}, nil); err != nil {
		t.Fatal(err)
	}
	if p := invitePost(t, app, invitePreviewPath, map[string]any{"token": token}, nil); p.body["state"] != "expired" {
		t.Errorf("expired: %s", p.raw)
	}
	if r := invitePost(t, app, inviteAcceptPath, map[string]any{"token": token}, sessionCookie(app, "vic")); r.code != 410 || r.errCode() != "INVITE_EXPIRED" {
		t.Errorf("accept expired: %d %s", r.code, r.raw)
	}

	// A link without sign-up: existing accounts only.
	noSignup, _ := createInvite(t, app, "owner", projectID, map[string]any{"allow_signup": false, "max_uses": 0})
	if p := invitePost(t, app, invitePreviewPath, map[string]any{"token": noSignup}, nil); p.body["signup"] != false {
		t.Errorf("preview no signup: %s", p.raw)
	}
	if s := signup(t, app, noSignup, "nosign", "longpassword"); s.code != 403 || s.errCode() != "INVITE_SIGNUP_DISABLED" {
		t.Errorf("signup on no-signup link: %d %s", s.code, s.raw)
	}

	// The organization switch: owner only; off blocks every sign-up.
	open, _ := createInvite(t, app, "owner", projectID, map[string]any{"max_uses": 0})
	if _, err := app.Org.CreateUser(ctx, "owner", pyjson.NewObject("username", "adm", "password", desktopPassword, "role", "admin")); err != nil {
		t.Fatal(err)
	}
	if r := gpuCall(t, app, "POST", inviteSettingsPath, map[string]any{"signup_enabled": false}, sessionCookie(app, "adm")); r.code != 403 {
		t.Errorf("admin switch: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "POST", inviteSettingsPath, map[string]any{"signup_enabled": false}, sessionCookie(app, "owner")); r.code != 200 ||
		r.body["settings"].(map[string]any)["signup_enabled"] != false {
		t.Fatalf("owner switch: %d %s", r.code, r.raw)
	}
	if s := signup(t, app, open, "blocked", "longpassword"); s.code != 403 || s.errCode() != "INVITE_SIGNUP_DISABLED" {
		t.Errorf("signup switched off: %d %s", s.code, s.raw)
	}
	if p := invitePost(t, app, invitePreviewPath, map[string]any{"token": open}, nil); p.body["signup"] != false || p.body["state"] != "valid" {
		t.Errorf("preview switched off: %s", p.raw)
	}
	// Existing accounts still join.
	if r := invitePost(t, app, inviteAcceptPath, map[string]any{"token": open}, sessionCookie(app, "vic")); r.code != 200 {
		t.Errorf("accept with sign-up off: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "POST", inviteSettingsPath, map[string]any{"signup_enabled": true}, sessionCookie(app, "owner")); r.code != 200 {
		t.Fatalf("switch on: %d %s", r.code, r.raw)
	}
	if s := signup(t, app, open, "allowed", "longpassword"); s.code != 200 {
		t.Errorf("signup switched on: %d %s", s.code, s.raw)
	}
}

func TestInviteNoOpenRegistration(t *testing.T) {
	app, _ := authDesktopApp(t)
	for _, token := range []string{"", "short", "not a token at all !!", strings.Repeat("A", 32)} {
		s := signup(t, app, token, "intruder", "longpassword")
		if s.code != 404 || s.errCode() != "INVITE_INVALID" {
			t.Errorf("signup with %q: %d %s", token, s.code, s.raw)
		}
		p := invitePost(t, app, invitePreviewPath, map[string]any{"token": token}, nil)
		if p.code != 200 || p.body["state"] != "invalid" || p.body["project"] != nil {
			t.Errorf("preview %q: %d %s", token, p.code, p.raw)
		}
	}
	if _, ok, _ := app.Org.UserByName(context.Background(), "intruder"); ok {
		t.Errorf("an account was created without a link")
	}
	// The org user route still needs an admin.
	if r := gpuCall(t, app, "POST", "/api/org/users", map[string]any{"username": "intruder", "password": "longpassword"}, nil); r.code != 401 {
		t.Errorf("anonymous org user: %d %s", r.code, r.raw)
	}
}

func TestInviteRateLimit(t *testing.T) {
	app, projectID := authDesktopApp(t)
	token, _ := createInvite(t, app, "owner", projectID, map[string]any{"max_uses": 0})
	bogus := strings.Repeat("B", 32)
	for i := 0; i < inviteBadLimit; i++ {
		if p := invitePost(t, app, invitePreviewPath, map[string]any{"token": bogus}, nil); p.code != 200 {
			t.Fatalf("preview %d: %d %s", i, p.code, p.raw)
		}
	}
	// Now even a good token from this client waits.
	if p := invitePost(t, app, invitePreviewPath, map[string]any{"token": token}, nil); p.code != 429 || p.errCode() != "INVITE_RATE_LIMITED" {
		t.Errorf("after %d bad tokens: %d %s", inviteBadLimit, p.code, p.raw)
	}
	// Another client is not affected.
	if p := invitePost(t, app, invitePreviewPath, map[string]any{"token": token}, map[string]string{"X-Forwarded-For": "10.1.1.1"}); p.code == 429 &&
		app.Cfg.TrustProxy {
		t.Errorf("other client limited")
	}

	// Sign-up attempts per client.
	app2, project2 := authDesktopApp(t)
	tok2, _ := createInvite(t, app2, "owner", project2, map[string]any{"max_uses": 0})
	for i := 0; i < inviteSignupIPLimit; i++ {
		signup(t, app2, tok2, "u"+strings.Repeat("x", i+1), "short")
	}
	if s := signup(t, app2, tok2, "ok_user", "longpassword"); s.code != 429 {
		t.Errorf("signup after %d attempts: %d %s", inviteSignupIPLimit, s.code, s.raw)
	}
}

func TestInvitesNeedAccounts(t *testing.T) {
	root := t.TempDir()
	app, err := NewApp(Config{Root: filepath.Join(root, "projects"), NoDemo: true, SessionsRoot: filepath.Join(root, "sessions")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	app.Bind("127.0.0.1", 8765)
	if p := invitePost(t, app, invitePreviewPath, map[string]any{"token": strings.Repeat("A", 32)}, nil); p.code != 404 ||
		p.errCode() != "INVITES_UNAVAILABLE" {
		t.Errorf("local preview: %d %s", p.code, p.raw)
	}
	if r := gpuCall(t, app, "GET", inviteSettingsPath, nil, nil); r.code != 404 {
		t.Errorf("local settings: %d %s", r.code, r.raw)
	}
}

func TestInviteRoutesAreGoOnly(t *testing.T) {
	ids := map[string]bool{}
	for _, r := range GoOnlyRoutes {
		ids[r.ID] = true
	}
	for _, r := range inviteRoutesGoOnly {
		if !ids[r.ID] {
			t.Errorf("%s missing from GoOnlyRoutes", r.ID)
		}
	}
	if org.InviteHash("abc") == "abc" || len(org.InviteHash("abc")) != 64 {
		t.Errorf("hash")
	}
}
