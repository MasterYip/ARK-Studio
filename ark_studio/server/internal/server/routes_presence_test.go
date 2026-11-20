package server

// T-0043: agent presence (Go only): implicit cursors from agent reads and
// writes, POST /api/agent/v1/presence, GET /api/presence, the presence
// frames of GET /api/events?presence=1, and who may see them.

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/presence"
)

func presenceEntries(t *testing.T, app *App, headers map[string]string) []map[string]any {
	t.Helper()
	r := gpuCall(t, app, "GET", "/api/presence", nil, headers)
	if r.code != 200 {
		t.Fatalf("GET /api/presence: %d %s", r.code, r.raw)
	}
	list, _ := r.body["entries"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		out = append(out, item.(map[string]any))
	}
	return out
}

func presenceAgentHeaders(app *App, name string) map[string]string {
	return map[string]string{"X-ARK-Token": app.AgentToken(), "X-Agent-Name": name}
}

func TestPresenceIdentityAndColourKey(t *testing.T) {
	if got := agentLabel("agent:MasterYip/视频字体"); got != "MasterYip/视频字体" {
		t.Fatalf("label lost hierarchy: %q", got)
	}
	if got := presenceColorKey("agent:MasterYip/视频字体"); got != "agent:MasterYip" {
		t.Fatalf("colour key did not use parent actor: %q", got)
	}
	if got := presenceColorKey("task:session-1"); got != "task:session-1" {
		t.Fatalf("task identity was merged unexpectedly: %q", got)
	}
}

func TestPresenceFromAgentReadsAndWrites(t *testing.T) {
	app, _ := chatScopeApp(t)
	if got := presenceEntries(t, app, nil); len(got) != 0 {
		t.Fatalf("fresh project: %v", got)
	}
	// A person's write is not presence.
	if r := gpuCall(t, app, "POST", "/api/units", map[string]any{"type": "idea", "title": "Human"}, nil); r.code != 200 {
		t.Fatalf("human create: %d %s", r.code, r.raw)
	}
	if got := presenceEntries(t, app, nil); len(got) != 0 {
		t.Fatalf("human write tracked: %v", got)
	}
	claude := presenceAgentHeaders(app, "claude")
	r := gpuCall(t, app, "POST", "/api/agent/v1/units", map[string]any{"type": "task", "title": "Agent task"}, claude)
	if r.code != 200 {
		t.Fatalf("agent create: %d %s", r.code, r.raw)
	}
	unit := r.body["unit"].(map[string]any)
	id, uid := int64(unit["id"].(float64)), unit["uid"].(string)
	got := presenceEntries(t, app, nil)
	if len(got) != 1 || got[0]["actor"] != "agent:claude" || got[0]["label"] != "claude" || got[0]["state"] != "writing" ||
		got[0]["action"] != "create" || int64(got[0]["unit"].(float64)) != id || got[0]["uid"] != uid {
		t.Fatalf("after create: %v", got)
	}
	if got[0]["color_key"] != "agent:claude" {
		t.Fatalf("missing color key: %v", got[0])
	}
	if got[0]["color_actor"] != "agent:claude" {
		t.Fatalf("missing color actor: %v", got[0])
	}
	// A status change, a comment, then a read of another unit by another agent.
	if r := gpuCall(t, app, "POST", "/api/agent/v1/units/"+uid, map[string]any{"status": "active"}, claude); r.code != 200 {
		t.Fatalf("status write: %d %s", r.code, r.raw)
	}
	if got = presenceEntries(t, app, nil); got[0]["action"] != "status" {
		t.Fatalf("status: %v", got)
	}
	gpuCall(t, app, "POST", "/api/agent/v1/units/"+uid+"/comments", map[string]any{"body": "on it"}, claude)
	if got = presenceEntries(t, app, nil); got[0]["action"] != "comment" {
		t.Fatalf("comment: %v", got)
	}
	units := gpuCall(t, app, "GET", "/api/units?q=Human", nil, nil)
	human := units.body["items"].([]any)[0].(map[string]any)["uid"].(string)
	if r := gpuCall(t, app, "GET", "/api/agent/v1/units/"+human, nil, presenceAgentHeaders(app, "codex")); r.code != 200 {
		t.Fatalf("agent read: %d %s", r.code, r.raw)
	}
	got = presenceEntries(t, app, nil)
	if len(got) != 2 || got[1]["actor"] != "agent:codex" || got[1]["state"] != "reading" || got[1]["uid"] != human {
		t.Fatalf("after read: %v", got)
	}
	// The unit list is not a unit read.
	gpuCall(t, app, "GET", "/api/agent/v1/units", nil, presenceAgentHeaders(app, "gemini"))
	if got = presenceEntries(t, app, nil); len(got) != 2 {
		t.Fatalf("list read tracked: %v", got)
	}
	// Expiry: 30 s after the last touch.
	app.Presence.Now = func() time.Time { return time.Now().Add(presence.DefaultCursorTTL + time.Second) }
	if got = presenceEntries(t, app, nil); len(got) != 0 {
		t.Fatalf("not expired: %v", got)
	}
}

func TestPresenceAnnounce(t *testing.T) {
	app, _ := chatScopeApp(t)
	claude := presenceAgentHeaders(app, "claude")
	r := gpuCall(t, app, "POST", "/api/agent/v1/units", map[string]any{"type": "task", "title": "Long task"}, claude)
	uid := r.body["unit"].(map[string]any)["uid"].(string)

	r = gpuCall(t, app, "POST", "/api/agent/v1/presence", map[string]any{"unit": uid, "state": "working", "note": "Refactoring"}, claude)
	if r.code != 200 {
		t.Fatalf("announce: %d %s", r.code, r.raw)
	}
	got := presenceEntries(t, app, nil)
	if len(got) != 1 || got[0]["kind"] != "working" || got[0]["state"] != "working" || got[0]["note"] != "Refactoring" {
		t.Fatalf("working: %v", got)
	}
	// Still there after the cursor lifetime, gone after the working one.
	app.Presence.Now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if got = presenceEntries(t, app, nil); len(got) != 1 {
		t.Fatalf("working expired early: %v", got)
	}
	app.Presence.Now = time.Now
	if r := gpuCall(t, app, "POST", "/api/agent/v1/presence", map[string]any{"unit": uid, "state": "done"}, claude); r.code != 200 {
		t.Fatalf("done: %d %s", r.code, r.raw)
	}
	if got = presenceEntries(t, app, nil); len(got) != 1 || got[0]["kind"] != "cursor" || got[0]["action"] != "done" {
		t.Fatalf("after done: %v", got)
	}
	for body, want := range map[string]int{
		`{"unit": "` + uid + `", "state": "busy"}`: 400,
		`{"state": "working"}`:                     400,
		`{"unit": "T-9999", "state": "working"}`:   404,
	} {
		var m map[string]any
		_ = json.Unmarshal([]byte(body), &m)
		if r := gpuCall(t, app, "POST", "/api/agent/v1/presence", m, claude); r.code != want {
			t.Errorf("%s: %d %s", body, r.code, r.raw)
		}
	}
	// An agent token is required.
	if r := gpuCall(t, app, "POST", "/api/agent/v1/presence", map[string]any{"unit": uid}, nil); r.code != 401 {
		t.Errorf("no token: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "POST", "/api/agent/v1/presence", map[string]any{"unit": uid},
		map[string]string{"X-ARK-Token": "wrong"}); r.code != 401 {
		t.Errorf("bad token: %d %s", r.code, r.raw)
	}
}

func TestPresenceOnlyForProjectMembers(t *testing.T) {
	app, projectID := authDesktopApp(t)
	ctx := context.Background()
	if _, err := app.Org.CreateUser(ctx, "owner", pyjson.NewObject("username", "eve",
		"password", desktopPassword, "role", "member")); err != nil {
		t.Fatal(err)
	}
	app.Presence.Record(projectID, presence.Touch{Actor: "agent:owner/claude", Label: "claude", Unit: 1, Action: "update"})
	scoped := func(user string) map[string]string {
		h := sessionCookie(app, user)
		h["X-ARK-Project"] = projectID
		return h
	}
	for _, user := range []string{"owner", "ana", "vic"} {
		if got := presenceEntries(t, app, scoped(user)); len(got) != 1 {
			t.Errorf("%s sees %v", user, got)
		}
	}
	// eve has no grant: her presence view is never the Team project.
	r := gpuCall(t, app, "GET", "/api/presence", nil, scoped("eve"))
	if r.code == 200 {
		if list, _ := r.body["entries"].([]any); len(list) != 0 || r.body["project"] == projectID {
			t.Errorf("non-member sees the presence: %s", r.raw)
		}
	}
	if r := gpuCall(t, app, "GET", "/api/presence", nil, map[string]string{"X-ARK-Project": projectID}); r.code != 401 {
		t.Errorf("signed out: %d %s", r.code, r.raw)
	}
}

// readEvents opens GET target on a live server and returns the event names
// (and data) seen until stop returns true or the timeout passes.
func readEvents(t *testing.T, app *App, target string, timeout time.Duration, during func(), stop func(name, data string) bool) []string {
	t.Helper()
	srv := httptest.NewServer(app)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+target, nil)
	req.Host = "127.0.0.1:8765"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var names []string
	sc := bufio.NewScanner(resp.Body)
	name := ""
	started := false
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			name = line[7:]
		case strings.HasPrefix(line, "data: "):
			names = append(names, name+" "+line[6:])
			if stop(name, line[6:]) {
				return names
			}
			if !started && during != nil {
				started = true
				go during()
			}
		}
	}
	return names
}

func TestPresenceEventStream(t *testing.T) {
	app, _ := chatScopeApp(t)
	claude := presenceAgentHeaders(app, "claude")
	r := gpuCall(t, app, "POST", "/api/agent/v1/units", map[string]any{"type": "task", "title": "Streamed"}, claude)
	uid := r.body["unit"].(map[string]any)["uid"].(string)

	// Without ?presence=1 the stream is unchanged.
	plain := readEvents(t, app, "/api/events", 1500*time.Millisecond, nil, func(string, string) bool { return false })
	for _, e := range plain {
		if strings.HasPrefix(e, "presence ") {
			t.Fatalf("presence frame without the parameter: %v", plain)
		}
	}
	// With it: the current presence first, then the changes.
	events := readEvents(t, app, "/api/events?presence=1", 5*time.Second, func() {
		gpuCall(t, app, "GET", "/api/agent/v1/units/"+uid, nil, presenceAgentHeaders(app, "codex"))
	}, func(name, data string) bool { return name == "presence" && strings.Contains(data, "agent:codex") })
	var frames []string
	for _, e := range events {
		if strings.HasPrefix(e, "presence ") {
			frames = append(frames, e)
		}
	}
	if len(frames) < 2 || !strings.Contains(frames[0], `"actor": "agent:claude"`) || strings.Contains(frames[0], "codex") ||
		!strings.Contains(frames[len(frames)-1], `"state": "reading"`) {
		t.Fatalf("presence frames: %v", events)
	}
}

func TestPresenceGoOnlyRoutesListed(t *testing.T) {
	ids := map[string]bool{}
	for _, r := range GoOnlyRoutes {
		ids[r.ID] = true
	}
	if !ids["goonly:exact:/api/presence"] || !ids["goonly:exact:/api/agent/v1/presence"] {
		t.Fatalf("GoOnlyRoutes: %v", GoOnlyRoutes)
	}
}

// Without sign-in the stream's access recheck must not deny the anonymous
// caller (it used to send "denied" after EVENTS_RECHECK).
func TestEventStreamWithoutSignInIsNotDenied(t *testing.T) {
	app, _ := chatScopeApp(t)
	if app.Auth != nil {
		t.Skip("test app has sign-in")
	}
	saved := eventsRecheck
	eventsRecheck = 50 * time.Millisecond
	defer func() { eventsRecheck = saved }()
	events := readEvents(t, app, "/api/events", 2500*time.Millisecond, nil,
		func(name, _ string) bool { return name == "denied" || name == "auth" })
	for _, e := range events {
		if strings.HasPrefix(e, "denied ") || strings.HasPrefix(e, "auth ") {
			t.Fatalf("stream ended with %q: %v", e, events)
		}
	}
}
