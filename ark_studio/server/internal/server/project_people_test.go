package server

// Go only: GET /api/projects names each project's creator and latest writer
// (project_people.go).

import (
	"context"
	"testing"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/platform/clock"
)

// peopleApp: owner "owner" (org owner, created "Team"), member "ana" who
// created "AnaProj" herself (the owner is admin there but did not create it).
func peopleApp(t *testing.T) (*App, string, string) {
	t.Helper()
	app, teamID := authDesktopApp(t)
	r := gpuCall(t, app, "POST", "/api/projects", map[string]any{"name": "AnaProj"}, sessionCookie(app, "ana"))
	summary, _ := r.body["summary"].(map[string]any)
	anaID, _ := summary["id"].(string)
	if r.code != 200 || anaID == "" {
		t.Fatalf("ana creates a project: %d %s", r.code, r.raw)
	}
	return app, teamID, anaID
}

func listedAs(t *testing.T, app *App, user string) map[string]map[string]any {
	t.Helper()
	r := gpuCall(t, app, "GET", "/api/projects", nil, sessionCookie(app, user))
	if r.code != 200 {
		t.Fatalf("GET /api/projects as %s: %d %s", user, r.code, r.raw)
	}
	out := map[string]map[string]any{}
	for _, item := range r.body["projects"].([]any) {
		m := item.(map[string]any)
		out[m["id"].(string)] = m
	}
	return out
}

func person(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func TestProjectCreatorRecordedAtCreation(t *testing.T) {
	app, teamID, anaID := peopleApp(t)
	got := listedAs(t, app, "owner")
	if c := person(got[anaID]["creator"]); c["user"] != "ana" || c["name"] != "ana" {
		t.Errorf("AnaProj creator = %v", got[anaID]["creator"])
	}
	// The owner is admin of AnaProj but did not create it.
	if got[anaID]["created_by_me"] != false {
		t.Errorf("AnaProj created_by_me for the admin = %v", got[anaID]["created_by_me"])
	}
	if c := person(got[teamID]["creator"]); c["user"] != "owner" || got[teamID]["created_by_me"] != true {
		t.Errorf("Team for its creator: %v / %v", got[teamID]["creator"], got[teamID]["created_by_me"])
	}
	mine := listedAs(t, app, "ana")
	if mine[anaID]["created_by_me"] != true || mine[teamID]["created_by_me"] != false {
		t.Errorf("ana: AnaProj %v, Team %v", mine[anaID]["created_by_me"], mine[teamID]["created_by_me"])
	}
	// The display name follows the account.
	if err := app.Org.DB.Exec("UPDATE users SET display_name='Ana Lee' WHERE username='ana'"); err != nil {
		t.Fatal(err)
	}
	if c := person(listedAs(t, app, "owner")[anaID]["creator"]); c["user"] != "ana" || c["name"] != "Ana Lee" {
		t.Errorf("renamed creator = %v", c)
	}
}

func TestLegacyProjectHasNoCreator(t *testing.T) {
	app, teamID, _ := peopleApp(t)
	// A project found on disk is registered later than its manifest says it
	// was created, with the server owner as created_by: not its creator.
	if err := app.Org.DB.Exec("UPDATE projects SET created_at='2026-12-01T00:00:00+00:00' WHERE id='" + teamID + "'"); err != nil {
		t.Fatal(err)
	}
	got := listedAs(t, app, "owner")[teamID]
	if _, has := got["creator"]; has {
		t.Errorf("legacy project creator = %v", got["creator"])
	}
	if got["created_by_me"] != false {
		t.Errorf("legacy project created_by_me = %v", got["created_by_me"])
	}
}

func TestLastActorFollowsWrites(t *testing.T) {
	app, _, anaID := peopleApp(t)
	// Creating the project is its first change.
	if a := person(listedAs(t, app, "owner")[anaID]["last_actor"]); a["user"] != "ana" || a["agent"] != nil {
		t.Errorf("after creation: %v", a)
	}

	// The owner's agent writes: the agent's label, and the account it acts for.
	tok, err := app.Org.CreateAgentToken(context.Background(), "owner", pyjson.NewObject())
	if err != nil {
		t.Fatal(err)
	}
	agent := map[string]string{"Authorization": "Bearer " + pyjson.Str(tok.Value("token")),
		"X-ARK-Project": anaID, "X-Agent-Name": "video-font"}
	if r := gpuCall(t, app, "POST", "/api/agent/v1/units", map[string]any{"type": "task", "title": "By agent"}, agent); r.code != 200 {
		t.Fatalf("agent write: %d %s", r.code, r.raw)
	}
	a := person(listedAs(t, app, "ana")[anaID]["last_actor"])
	if a["user"] != "owner" || a["agent"] != "owner/video-font" || a["at"] == nil {
		t.Errorf("after the agent's write: %v", a)
	}

	// Ana writes in the browser: a person, no agent label.
	if r := gpuCall(t, app, "POST", "/api/units", map[string]any{"type": "idea", "title": "By ana"}, sessionCookie(app, "ana")); r.code != 200 {
		t.Fatalf("ana's write: %d %s", r.code, r.raw)
	}
	if a := person(listedAs(t, app, "owner")[anaID]["last_actor"]); a["user"] != "ana" || a["agent"] != nil {
		t.Errorf("after ana's write: %v", a)
	}

	// Kept across restarts: saved to the org database.
	if err := app.writers.flush(context.Background(), app.Org); err != nil {
		t.Fatal(err)
	}
	app.writers.mu.Lock()
	app.writers.mem = map[string]org.LastWriter{}
	app.writers.mu.Unlock()
	if a := person(listedAs(t, app, "owner")[anaID]["last_actor"]); a["user"] != "ana" {
		t.Errorf("from the org database: %v", a)
	}

	// A writer recorded long before the latest change does not name it.
	app.writers.note(anaID, org.LastWriter{User: "owner", Actor: "human:ui", At: clock.Format(time.Now().Add(-time.Hour))})
	if err := app.writers.flush(context.Background(), app.Org); err != nil {
		t.Fatal(err)
	}
	if err := app.Org.DB.Exec("UPDATE project_last_writer SET at='2020-01-01T00:00:00+00:00'"); err != nil {
		t.Fatal(err)
	}
	app.writers.mu.Lock()
	app.writers.mem = map[string]org.LastWriter{}
	app.writers.mu.Unlock()
	if a, has := listedAs(t, app, "owner")[anaID]["last_actor"]; has {
		t.Errorf("stale writer shown: %v", a)
	}
}

func TestProjectPeopleOnlyMembersNamed(t *testing.T) {
	app, _, anaID := peopleApp(t)
	ctx := context.Background()
	anaUser, _ := app.Org.UserIDFor(ctx, "ana")
	// Ana leaves her project: the owner (still a member) no longer sees her
	// named there, neither as creator nor as the latest writer.
	if err := app.Org.DB.Exec("DELETE FROM project_grants WHERE project_id='" + anaID + "' AND user_id='" + anaUser + "'"); err != nil {
		t.Fatal(err)
	}
	got := listedAs(t, app, "owner")[anaID]
	if _, has := got["creator"]; has {
		t.Errorf("creator of a non-member shown: %v", got["creator"])
	}
	if _, has := got["last_actor"]; has {
		t.Errorf("last actor of a non-member shown: %v", got["last_actor"])
	}
}

func TestProjectPeopleLocalMode(t *testing.T) {
	app, _ := activityApp(t, "Mine")
	got := projectsByName(t, app)["Mine"]
	if c := person(got["creator"]); c["user"] != "local" || got["created_by_me"] != true {
		t.Errorf("local project: creator %v, created_by_me %v", got["creator"], got["created_by_me"])
	}
	if a := person(got["last_actor"]); a["user"] != "local" || a["agent"] != nil {
		t.Errorf("local project last_actor = %v", got["last_actor"])
	}
}
