package server

// Desktop links (Go only): "Link this desktop" -> one-time code -> device
// token claimed by ARK Studio; the token lists and creates projects and
// nothing else, and is revocable like a phone's.

import (
	"context"
	"path/filepath"
	"testing"

	"arkstudio/go/internal/auth"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/mobile"
)

const desktopPassword = "correct horse battery"

// authDesktopApp is a server with sign-in (owner "owner"), one project and
// a member "ana" and a viewer "vic" with read access to it.
func authDesktopApp(t *testing.T) (*App, string) {
	t.Helper()
	root := t.TempDir()
	hash, err := auth.HashPassword(desktopPassword, 1000)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := auth.NewManager("owner", hash, auth.SessionTTLDefault)
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewApp(Config{Root: filepath.Join(root, "projects"), NoDemo: true,
		SessionsRoot: filepath.Join(root, "sessions")}, mgr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	app.Bind("127.0.0.1", 8765)
	owner := sessionCookie(app, "owner")
	r := gpuCall(t, app, "POST", "/api/projects", map[string]any{"name": "Team"}, owner)
	summary, _ := r.body["summary"].(map[string]any)
	projectID, _ := summary["id"].(string)
	if r.code != 200 || projectID == "" {
		t.Fatalf("create project: %d %s", r.code, r.raw)
	}
	ctx := context.Background()
	ownerID, _ := app.Org.UserIDFor(ctx, "owner")
	for name, role := range map[string]string{"ana": "member", "vic": "viewer"} {
		if _, err := app.Org.CreateUser(ctx, "owner", pyjson.NewObject("username", name,
			"password", desktopPassword, "role", role)); err != nil {
			t.Fatal(err)
		}
		id, _ := app.Org.UserIDFor(ctx, name)
		if err := app.Org.SetProjectGrant(ctx, projectID, id, "read", ownerID); err != nil {
			t.Fatal(err)
		}
	}
	return app, projectID
}

func sessionCookie(app *App, user string) map[string]string {
	token, _ := app.Auth.IssueSession(user, false)
	return map[string]string{"Cookie": auth.CookieName + "=" + token}
}

func mobileHeader(token string) map[string]string {
	return map[string]string{"X-ARK-Mobile-Token": token}
}

// linkDesktop runs "Link this desktop" for user and claims the code like the
// app does (no cookie, the code only). Returns the device token and pair id.
func linkDesktop(t *testing.T, app *App, user string) (string, string) {
	t.Helper()
	r := gpuCall(t, app, "POST", "/api/desktop/link", map[string]any{"device": "Test PC"}, sessionCookie(app, user))
	code, _ := r.body["code"].(string)
	if r.code != 200 || code == "" || r.body["expires_in"] != float64(mobile.DesktopLinkTTL) ||
		r.body["claim_path"] != "/api/mobile/pair/claim" {
		t.Fatalf("link %s: %d %s", user, r.code, r.raw)
	}
	pair, _ := r.body["pair"].(map[string]any)
	c := gpuCall(t, app, "POST", "/api/mobile/pair/claim", map[string]any{"code": code},
		map[string]string{"User-Agent": "ARK-Studio-Desktop/2.0.0", "Origin": ""})
	token, _ := c.body["mobile_token"].(string)
	if c.code != 200 || token == "" || c.body["permission"] != "read_only" || c.body["permanent"] != true || c.body["user"] != user {
		t.Fatalf("claim: %d %s", c.code, c.raw)
	}
	if again := gpuCall(t, app, "POST", "/api/mobile/pair/claim", map[string]any{"code": code}, nil); again.code != 401 ||
		again.errCode() != "PAIRING_USED" {
		t.Errorf("second claim: %d %s", again.code, again.raw)
	}
	return token, pair["id"].(string)
}

func TestDesktopLinkListsAndCreatesProjects(t *testing.T) {
	app, projectID := authDesktopApp(t)
	token, pairID := linkDesktop(t, app, "owner")
	dev := mobileHeader(token)

	r := gpuCall(t, app, "GET", "/api/desktop/projects", nil, dev)
	list, _ := r.body["projects"].([]any)
	if r.code != 200 || len(list) == 0 || r.body["user"] != "owner" || r.body["can_create"] != true {
		t.Fatalf("list: %d %s", r.code, r.raw)
	}
	var team map[string]any
	for _, item := range list {
		if row := item.(map[string]any); row["id"] == projectID {
			team = row
		}
	}
	_, hasPath := team["path"]
	if team == nil || team["name"] != "Team" || team["permission"] != "admin" || hasPath ||
		team["created_by_me"] != true || team["creator"].(map[string]any)["user"] != "owner" {
		t.Errorf("project row (id, name, description, created, updated, permission, people; no path): %v", team)
	}

	c := gpuCall(t, app, "POST", "/api/desktop/projects", map[string]any{"name": "From the desktop", "description": "d"}, dev)
	made, _ := c.body["project"].(map[string]any)
	if c.code != 200 || made["name"] != "From the desktop" || made["id"] == "" || made["permission"] != "admin" {
		t.Fatalf("create: %d %s", c.code, c.raw)
	}
	if r := gpuCall(t, app, "GET", "/api/desktop/projects", nil, dev); len(r.body["projects"].([]any)) != len(list)+1 {
		t.Errorf("list after create: %s", r.raw)
	}
	if c := gpuCall(t, app, "POST", "/api/desktop/projects", map[string]any{"name": " "}, dev); c.code != 400 {
		t.Errorf("empty name: %d %s", c.code, c.raw)
	}
	// Creating does not move the owner's browser session to the new project.
	if r := gpuCall(t, app, "GET", "/api/bootstrap", nil, sessionCookie(app, "owner")); r.code != 200 ||
		r.body["summary"].(map[string]any)["name"] == "From the desktop" {
		t.Errorf("browser session was switched: %d %.300s", r.code, r.raw)
	}
	// Its own session can be read; the device list shows the link.
	if s := gpuCall(t, app, "GET", "/api/mobile/session", nil, dev); s.code != 200 {
		t.Errorf("own session: %d %s", s.code, s.raw)
	}
	pairs := gpuCall(t, app, "GET", "/api/mobile/pairs", nil, sessionCookie(app, "owner"))
	found := false
	for _, item := range pairs.body["pairs"].([]any) {
		p := item.(map[string]any)
		if p["id"] == pairID {
			found = p["source"] == mobile.SourceDesktop && p["state"] == "claimed" && p["session_permanent"] == true
		}
	}
	if !found {
		t.Errorf("device list: %s", pairs.raw)
	}
}

func TestDesktopLinkScopeIsMinimal(t *testing.T) {
	app, _ := authDesktopApp(t)
	token, _ := linkDesktop(t, app, "owner")
	dev := mobileHeader(token)
	refused := []struct{ method, path string }{
		{"GET", "/api/bootstrap"},
		{"GET", "/api/units?limit=1"},
		{"GET", "/api/projects"},
		{"GET", "/api/project"},
		{"GET", "/api/me"},
		{"GET", "/api/org"},
		{"GET", "/api/mobile/session/projects"},
		{"GET", "/api/agent/token"},
		{"POST", "/api/projects"},
		{"POST", "/api/projects/open"},
		{"POST", "/api/units"},
		{"POST", "/api/desktop/link"},
		{"POST", "/api/mobile/session/project"},
	}
	for _, tc := range refused {
		var body any
		if tc.method == "POST" {
			body = map[string]any{"name": "x", "title": "x", "type": "note"}
		}
		r := gpuCall(t, app, tc.method, tc.path, body, dev)
		if r.code != 403 {
			t.Errorf("%s %s: %d %s, want 403", tc.method, tc.path, r.code, r.raw)
		}
	}
	if r := gpuCall(t, app, "GET", "/api/units?limit=1", nil, dev); r.errCode() != "DESKTOP_LINK_SCOPE" {
		t.Errorf("scope code: %s", r.raw)
	}
	if r := gpuCall(t, app, "GET", "/api/health", nil, dev); r.code != 200 {
		t.Errorf("health: %d", r.code)
	}
}

func TestDesktopLinkNeedsSignInAndADesktopToken(t *testing.T) {
	app, _ := authDesktopApp(t)
	if r := gpuCall(t, app, "POST", "/api/desktop/link", map[string]any{}, nil); r.code != 401 {
		t.Errorf("signed out link: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "GET", "/api/desktop/projects", nil, nil); r.code != 401 {
		t.Errorf("signed out list: %d %s", r.code, r.raw)
	}
	// A browser session is not a desktop link.
	if r := gpuCall(t, app, "GET", "/api/desktop/projects", nil, sessionCookie(app, "owner")); r.code != 401 ||
		r.errCode() != "DESKTOP_LINK_REQUIRED" {
		t.Errorf("browser session list: %d %s", r.code, r.raw)
	}
	// A phone (password sign-in) cannot use the desktop routes.
	login := gpuCall(t, app, "POST", "/api/mobile/login", map[string]any{"username": "owner", "password": desktopPassword}, nil)
	phone, _ := login.body["mobile_token"].(string)
	if login.code != 200 || phone == "" {
		t.Fatalf("phone login: %d %s", login.code, login.raw)
	}
	if r := gpuCall(t, app, "GET", "/api/desktop/projects", nil, mobileHeader(phone)); r.code != 403 {
		t.Errorf("phone list: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "POST", "/api/desktop/link", map[string]any{}, mobileHeader(phone)); r.code != 403 {
		t.Errorf("phone link: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "POST", "/api/mobile/pair/claim", map[string]any{"code": "arkp_nope"}, nil); r.code != 401 {
		t.Errorf("bad code: %d %s", r.code, r.raw)
	}
}

func TestDesktopLinkFollowsTheAccount(t *testing.T) {
	app, _ := authDesktopApp(t)
	// A viewer sees the projects it may read and may not create.
	viewer, _ := linkDesktop(t, app, "vic")
	r := gpuCall(t, app, "GET", "/api/desktop/projects", nil, mobileHeader(viewer))
	before := len(r.body["projects"].([]any))
	if r.code != 200 || before == 0 || r.body["can_create"] != false || r.body["user"] != "vic" {
		t.Errorf("viewer list: %d %s", r.code, r.raw)
	}
	if c := gpuCall(t, app, "POST", "/api/desktop/projects", map[string]any{"name": "Nope"}, mobileHeader(viewer)); c.code != 403 ||
		c.errCode() != "ORG_FORBIDDEN" {
		t.Errorf("viewer create: %d %s", c.code, c.raw)
	}
	// A member creates with its own account.
	member, _ := linkDesktop(t, app, "ana")
	before = len(gpuCall(t, app, "GET", "/api/desktop/projects", nil, mobileHeader(member)).body["projects"].([]any))
	c := gpuCall(t, app, "POST", "/api/desktop/projects", map[string]any{"name": "Ana's"}, mobileHeader(member))
	if c.code != 200 {
		t.Fatalf("member create: %d %s", c.code, c.raw)
	}
	made := c.body["project"].(map[string]any)
	if made["permission"] != "admin" {
		t.Errorf("creator permission: %s", c.raw)
	}
	if r := gpuCall(t, app, "GET", "/api/desktop/projects", nil, mobileHeader(member)); len(r.body["projects"].([]any)) != before+1 {
		t.Errorf("member list: %s", r.raw)
	}
}

func TestDesktopLinkIsRevocable(t *testing.T) {
	app, _ := authDesktopApp(t)
	owner := sessionCookie(app, "owner")
	token, pairID := linkDesktop(t, app, "owner")
	if r := gpuCall(t, app, "POST", "/api/mobile/pairs/"+pairID+"/revoke", map[string]any{}, owner); r.code != 200 {
		t.Fatalf("revoke: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "GET", "/api/desktop/projects", nil, mobileHeader(token)); r.code != 401 {
		t.Errorf("revoked list: %d %s", r.code, r.raw)
	}
	// Unlink from the app: DELETE its own session.
	token, _ = linkDesktop(t, app, "owner")
	if r := gpuCall(t, app, "DELETE", "/api/mobile/session", nil, mobileHeader(token)); r.code != 200 {
		t.Fatalf("unlink: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "GET", "/api/desktop/projects", nil, mobileHeader(token)); r.code != 401 {
		t.Errorf("after unlink: %d %s", r.code, r.raw)
	}
}

// Without accounts (a loopback server), the trusted local owner links.
func TestDesktopLinkLocalMode(t *testing.T) {
	app, _ := localGPUApp(t)
	r := gpuCall(t, app, "POST", "/api/desktop/link", map[string]any{}, nil)
	code, _ := r.body["code"].(string)
	if r.code != 200 || code == "" {
		t.Fatalf("local link: %d %s", r.code, r.raw)
	}
	c := gpuCall(t, app, "POST", "/api/mobile/pair/claim", map[string]any{"code": code}, nil)
	token, _ := c.body["mobile_token"].(string)
	if c.code != 200 || token == "" {
		t.Fatalf("claim: %d %s", c.code, c.raw)
	}
	l := gpuCall(t, app, "GET", "/api/desktop/projects", nil, mobileHeader(token))
	if l.code != 200 || len(l.body["projects"].([]any)) == 0 || l.body["can_create"] != true {
		t.Errorf("local list: %d %s", l.code, l.raw)
	}
	if u := gpuCall(t, app, "GET", "/api/units?limit=1", nil, mobileHeader(token)); u.code != 403 {
		t.Errorf("local scope: %d %s", u.code, u.raw)
	}
}
