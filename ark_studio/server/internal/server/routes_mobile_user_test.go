package server

// A pairing logs the phone in as a user, not into one project: the phone
// switches between every project the account can read (native switcher or
// the web app's project list), the account's role in each project decides
// what it may do, the person manages their devices in Settings → Mobile
// (list, full / read-only, revoke), and phones paired before keep working.

import (
	"context"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

// pairPhone creates a QR pairing code as user on projectID and claims it
// like the app does (no cookie). Returns the device token and pair id.
func pairPhone(t *testing.T, app *App, user, projectID, permission string) (string, string) {
	t.Helper()
	h := sessionCookie(app, user)
	h["X-ARK-Project"] = projectID
	r := gpuCall(t, app, "POST", "/api/mobile/pair", map[string]any{"permission": permission}, h)
	code, _ := r.body["code"].(string)
	if r.code != 200 || code == "" {
		t.Fatalf("pair as %s: %d %s", user, r.code, r.raw)
	}
	c := gpuCall(t, app, "POST", "/api/mobile/pair/claim", map[string]any{"code": code},
		map[string]string{"User-Agent": "Mozilla/5.0 (Linux; Android 14) ARKAndroid/3.0"})
	token, _ := c.body["mobile_token"].(string)
	if c.code != 200 || token == "" || c.body["scope"] != "user" || c.body["user"] != user {
		t.Fatalf("claim: %d %s", c.code, c.raw)
	}
	pairID, _ := c.body["pairing_id"].(string)
	return token, pairID
}

func newProjectFor(t *testing.T, app *App, owner, name string) string {
	t.Helper()
	r := gpuCall(t, app, "POST", "/api/projects", map[string]any{"name": name}, sessionCookie(app, owner))
	summary, _ := r.body["summary"].(map[string]any)
	id, _ := summary["id"].(string)
	if r.code != 200 || id == "" {
		t.Fatalf("create %s: %d %s", name, r.code, r.raw)
	}
	return id
}

func grant(t *testing.T, app *App, projectID, user, permission string) {
	t.Helper()
	ctx := context.Background()
	ownerID, _ := app.Org.UserIDFor(ctx, "owner")
	id, _ := app.Org.UserIDFor(ctx, user)
	if err := app.Org.SetProjectGrant(ctx, projectID, id, permission, ownerID); err != nil {
		t.Fatal(err)
	}
}

func phoneProject(t *testing.T, app *App, token string) string {
	t.Helper()
	r := gpuCall(t, app, "GET", "/api/mobile/session", nil, mobileHeader(token))
	project, _ := r.body["project"].(map[string]any)
	if r.code != 200 || r.body["authenticated"] != true || project == nil {
		t.Fatalf("session: %d %s", r.code, r.raw)
	}
	return project["id"].(string)
}

func TestMobilePairingIsUserScoped(t *testing.T) {
	app, team := authDesktopApp(t) // ana: read on Team
	app.Bind("0.0.0.0", 8765)      // pairing codes need a phone-reachable server
	two := newProjectFor(t, app, "owner", "Two")
	grant(t, app, two, "ana", "write")
	private := newProjectFor(t, app, "owner", "Private")

	token, pairID := pairPhone(t, app, "ana", team, "full")
	phone := mobileHeader(token)

	// Every project of the account, with the account's role in each.
	r := gpuCall(t, app, "GET", "/api/mobile/session/projects", nil, phone)
	list, _ := r.body["projects"].([]any)
	perms := map[string]any{}
	for _, item := range list {
		row := item.(map[string]any)
		perms[row["id"].(string)] = row["permission"]
	}
	if r.code != 200 || r.body["switchable"] != true || r.body["current"] != team ||
		perms[team] != "read" || perms[two] != "write" || perms[private] != nil {
		t.Fatalf("projects: %d %s", r.code, r.raw)
	}
	st := gpuCall(t, app, "GET", "/api/mobile/session", nil, phone)
	if st.body["scope"] != "user" || st.body["switchable"] != true || st.body["access"] != "read" || st.body["permission"] != "full" {
		t.Errorf("status on Team: %s", st.raw)
	}

	// Read on Team: no writes, whatever the device permission.
	if w := gpuCall(t, app, "POST", "/api/units", map[string]any{"type": "task", "title": "no"}, phone); w.code != 403 {
		t.Errorf("write on a read project: %d %s", w.code, w.raw)
	}

	// Native switcher: write on Two.
	s := gpuCall(t, app, "POST", "/api/mobile/session/project", map[string]any{"project_id": two, "permission": "read_only"}, phone)
	if s.code != 200 || s.body["project_id"] != two || s.body["permission"] != "full" || s.body["access"] != "write" {
		t.Fatalf("switch to Two: %d %s", s.code, s.raw)
	}
	if got := phoneProject(t, app, token); got != two {
		t.Fatalf("session project after switch: %s", got)
	}
	if w := gpuCall(t, app, "POST", "/api/units", map[string]any{"type": "task", "title": "from the phone"}, phone); w.code != 200 {
		t.Errorf("write on Two: %d %s", w.code, w.raw)
	}
	boot := gpuCall(t, app, "GET", "/api/bootstrap", nil, phone)
	if summary, _ := boot.body["summary"].(map[string]any); boot.code != 200 || summary["id"] != two {
		t.Errorf("bootstrap follows the switch: %d %v", boot.code, boot.body["summary"])
	}

	// Not the account's project.
	if x := gpuCall(t, app, "POST", "/api/mobile/session/project", map[string]any{"project_id": private}, phone); x.code != 404 {
		t.Errorf("switch to a project without access: %d %s", x.code, x.raw)
	}
	if x := gpuCall(t, app, "POST", "/api/projects/open", map[string]any{"id": private}, phone); x.code == 200 {
		t.Errorf("web switch to a project without access: %d %s", x.code, x.raw)
	}

	// The web app's project list (WebView) moves the same session.
	o := gpuCall(t, app, "POST", "/api/projects/open", map[string]any{"id": team}, phone)
	if summary, _ := o.body["summary"].(map[string]any); o.code != 200 || summary["id"] != team {
		t.Fatalf("web switch to Team: %d %s", o.code, o.raw)
	}
	if got := phoneProject(t, app, token); got != team {
		t.Fatalf("session project after the web switch: %s", got)
	}

	// Settings → Mobile: the person's devices, not other people's.
	ana := sessionCookie(app, "ana")
	mine := gpuCall(t, app, "GET", "/api/mobile/pairs", nil, ana)
	pairs, _ := mine.body["pairs"].([]any)
	found := false
	for _, item := range pairs {
		row := item.(map[string]any)
		if row["id"] == pairID {
			found = row["label"] == "Android phone" && row["project_id"] == team && row["state"] == "claimed" && row["last_seen_at"] != nil
		}
	}
	if mine.code != 200 || !found {
		t.Fatalf("ana's devices: %d %s", mine.code, mine.raw)
	}
	other := gpuCall(t, app, "GET", "/api/mobile/pairs", nil, sessionCookie(app, "owner"))
	for _, item := range other.body["pairs"].([]any) {
		if item.(map[string]any)["id"] == pairID {
			t.Errorf("owner sees ana's device: %s", other.raw)
		}
	}

	// Read-only from Settings: the live session follows at once.
	if x := gpuCall(t, app, "POST", "/api/mobile/pairs/"+pairID+"/permission", map[string]any{"permission": "read_only"}, phone); x.code != 403 {
		t.Errorf("phone changes its own permission: %d %s", x.code, x.raw)
	}
	if x := gpuCall(t, app, "POST", "/api/mobile/pairs/"+pairID+"/permission", map[string]any{"permission": "read_only"},
		sessionCookie(app, "owner")); x.code != 404 {
		t.Errorf("someone else's device: %d %s", x.code, x.raw)
	}
	ro := gpuCall(t, app, "POST", "/api/mobile/pairs/"+pairID+"/permission", map[string]any{"permission": "read_only"}, ana)
	if pair, _ := ro.body["pair"].(map[string]any); ro.code != 200 || pair["permission"] != "read_only" {
		t.Fatalf("set read-only: %d %s", ro.code, ro.raw)
	}
	if s := gpuCall(t, app, "POST", "/api/mobile/session/project", map[string]any{"project_id": two}, phone); s.code != 200 || s.body["access"] != "read" {
		t.Fatalf("read-only phone switches: %d %s", s.code, s.raw)
	}
	if w := gpuCall(t, app, "POST", "/api/units", map[string]any{"type": "task", "title": "no"}, phone); w.code != 403 {
		t.Errorf("read-only phone writes on Two: %d %s", w.code, w.raw)
	}
	if full := gpuCall(t, app, "POST", "/api/mobile/pairs/"+pairID+"/permission", map[string]any{"permission": "full"}, ana); full.code != 200 {
		t.Fatalf("set full: %d %s", full.code, full.raw)
	}
	if w := gpuCall(t, app, "POST", "/api/units", map[string]any{"type": "task", "title": "again"}, phone); w.code != 200 {
		t.Errorf("full phone writes on Two again: %d %s", w.code, w.raw)
	}

	// Access to the current project removed: the session moves to a project
	// the account can still read instead of locking the phone out.
	if _, err := app.Org.RevokeProject(context.Background(), "owner", two, pyjson.NewObject("username", "ana")); err != nil {
		t.Fatal(err)
	}
	if got := phoneProject(t, app, token); got != team {
		t.Errorf("session after losing Two: %s", got)
	}

	// Revoke: only the owner of the device; the token stops working.
	if x := gpuCall(t, app, "POST", "/api/mobile/pairs/"+pairID+"/revoke", map[string]any{}, sessionCookie(app, "owner")); x.code != 404 {
		t.Errorf("revoke someone else's device: %d %s", x.code, x.raw)
	}
	rv := gpuCall(t, app, "POST", "/api/mobile/pairs/"+pairID+"/revoke", map[string]any{}, ana)
	if rv.code != 200 {
		t.Fatalf("revoke: %d %s", rv.code, rv.raw)
	}
	if x := gpuCall(t, app, "GET", "/api/bootstrap", nil, phone); x.code != 401 {
		t.Errorf("revoked phone: %d %s", x.code, x.raw)
	}
}

// Password sign-in gives the same user-scoped session; a read-only device
// stays read-only on every project.
func TestMobileLoginSwitchesAndKeepsCeiling(t *testing.T) {
	app, team := authDesktopApp(t)
	two := newProjectFor(t, app, "owner", "Two")
	grant(t, app, two, "ana", "write")
	login := gpuCall(t, app, "POST", "/api/mobile/login", map[string]any{"username": "ana", "password": desktopPassword,
		"project_id": team, "permission": "read_only"}, nil)
	token, _ := login.body["mobile_token"].(string)
	if login.code != 200 || token == "" || login.body["permission"] != "read_only" {
		t.Fatalf("login: %d %s", login.code, login.raw)
	}
	phone := mobileHeader(token)
	s := gpuCall(t, app, "POST", "/api/mobile/session/project", map[string]any{"project_id": two, "permission": "full"}, phone)
	if s.code != 200 || s.body["permission"] != "read_only" || s.body["access"] != "read" {
		t.Fatalf("switch: %d %s", s.code, s.raw)
	}
	if w := gpuCall(t, app, "POST", "/api/units", map[string]any{"type": "task", "title": "no"}, phone); w.code != 403 {
		t.Errorf("read-only device writes: %d %s", w.code, w.raw)
	}
}

// Web mode of the Android app: the WebView carries the device token only as
// the HttpOnly ark_mobile cookie (page scripts never see it) and adds the
// tab's X-ARK-Project. Its project list offers every project of the account
// (not just the current one), a pick there moves the phone, and the app
// download list is readable; pairing management stays desktop-only.
func TestMobileWebViewListsAndSwitchesProjects(t *testing.T) {
	app, team := authDesktopApp(t) // ana: read on Team
	app.Bind("0.0.0.0", 8765)
	two := newProjectFor(t, app, "owner", "Two")
	grant(t, app, two, "ana", "write")
	private := newProjectFor(t, app, "owner", "Private")
	token, _ := pairPhone(t, app, "ana", team, "full")
	web := func(tab string) map[string]string {
		return map[string]string{"Cookie": "ark_mobile=" + token, "X-ARK-Project": tab,
			"User-Agent": "Mozilla/5.0 (Linux; Android 12; ALN-AL00 Build/HUAWEIALN-AL00; wv) Chrome/114.0 Mobile Safari/537.36 ARKAndroid/3.0"}
	}

	r := gpuCall(t, app, "GET", "/api/projects", nil, web(team))
	ids := map[string]bool{}
	for _, item := range r.body["projects"].([]any) {
		ids[item.(map[string]any)["id"].(string)] = true
	}
	if r.code != 200 || !ids[team] || !ids[two] || ids[private] || ids[demoID] {
		t.Fatalf("WebView project list: %d %v", r.code, ids)
	}
	o := gpuCall(t, app, "POST", "/api/projects/open", map[string]any{"id": two}, web(team))
	if summary, _ := o.body["summary"].(map[string]any); o.code != 200 || summary["id"] != two {
		t.Fatalf("WebView switch: %d %s", o.code, o.raw)
	}
	if got := phoneProject(t, app, token); got != two {
		t.Fatalf("session after the WebView switch: %s", got)
	}
	// The tab still names Team until the reply arrives: the session wins.
	if b := gpuCall(t, app, "GET", "/api/bootstrap", nil, web(team)); b.body["summary"].(map[string]any)["id"] != two {
		t.Errorf("bootstrap after the switch: %v", b.body["summary"])
	}
	// A native switch shows in the WebView's next request.
	if s := gpuCall(t, app, "POST", "/api/mobile/session/project", map[string]any{"project_id": team}, mobileHeader(token)); s.code != 200 {
		t.Fatalf("native switch: %d %s", s.code, s.raw)
	}
	if b := gpuCall(t, app, "GET", "/api/bootstrap", nil, web(two)); b.body["summary"].(map[string]any)["id"] != team {
		t.Errorf("WebView after the native switch: %v", b.body["summary"])
	}

	if a := gpuCall(t, app, "GET", "/api/mobile/apps", nil, web(team)); a.code != 200 {
		t.Errorf("app list on a phone: %d %s", a.code, a.raw)
	}
	for _, path := range []string{"/api/mobile/connection", "/api/mobile/pairs"} {
		if x := gpuCall(t, app, "GET", path, nil, web(team)); x.code != 403 {
			t.Errorf("%s on a phone: %d %s", path, x.code, x.raw)
		}
	}
}
