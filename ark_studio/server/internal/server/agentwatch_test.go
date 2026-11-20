package server

// T-0044 (Go only): GET /api/agent/v1/updates and the experimental agent
// messages (routes_agentwatch.go; go_only.py ROUTES).

import (
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func agentHeaders(app *App) map[string]string {
	return map[string]string{"Authorization": "Bearer " + app.AgentToken(), "X-Agent-Name": "coder"}
}

func updatesCall(t *testing.T, app *App, query string) gpuReply {
	t.Helper()
	r := gpuCall(t, app, "GET", "/api/agent/v1/updates"+query, nil, agentHeaders(app))
	if r.code != 200 {
		t.Fatalf("updates%s: %d %s", query, r.code, r.raw)
	}
	return r
}

func itemKinds(r gpuReply) []string {
	var out []string
	items, _ := r.body["items"].([]any)
	for _, it := range items {
		out = append(out, it.(map[string]any)["kind"].(string))
	}
	return out
}

func mustOK(t *testing.T, r gpuReply, what string) gpuReply {
	t.Helper()
	if r.code != 200 {
		t.Fatalf("%s: %d %s", what, r.code, r.raw)
	}
	return r
}

func TestAgentUpdatesFeed(t *testing.T) {
	app := homeApp(t)
	homeCreate(t, app, "Watch", "")
	// The agent creates a review node; nothing for it yet.
	created := mustOK(t, gpuCall(t, app, "POST", "/api/agent/v1/units",
		map[string]any{"type": "task", "title": "Ablation", "status": "review"}, agentHeaders(app)), "agent create")
	uid := created.body["unit"].(map[string]any)["uid"].(string)
	first := updatesCall(t, app, "")
	cursor := first.body["cursor"].(string)
	if !strings.HasPrefix(cursor, "u1.") || len(itemKinds(first)) != 0 {
		t.Fatalf("first call %s", first.raw)
	}
	if again := updatesCall(t, app, "?since="+cursor); len(itemKinds(again)) != 0 || again.body["cursor"] != cursor {
		t.Fatalf("nothing new: %s", again.raw)
	}
	// The person comments, sends the review back and assigns a new task.
	mustOK(t, gpuCall(t, app, "POST", "/api/units/"+uid+"/comments", map[string]any{"body": "Which seed?"}, nil), "comment")
	mustOK(t, gpuCall(t, app, "POST", "/api/units/"+uid+"/comments",
		map[string]any{"body": "Add the baseline", "kind": "sent_back"}, nil), "send back")
	mustOK(t, gpuCall(t, app, "PATCH", "/api/units/"+uid, map[string]any{"status": "active"}, nil), "status")
	mustOK(t, gpuCall(t, app, "POST", "/api/units",
		map[string]any{"type": "task", "title": "Rerun with seed 2", "assignee": "local"}, nil), "assign")
	// The agent's own writes never show.
	mustOK(t, gpuCall(t, app, "POST", "/api/agent/v1/units/"+uid+"/comments",
		map[string]any{"body": "On it"}, agentHeaders(app)), "agent reply")
	r := updatesCall(t, app, "?since="+cursor)
	got := itemKinds(r)
	if strings.Join(got, ",") != "comment,sent_back,assigned" {
		t.Fatalf("kinds %v\n%s", got, r.raw)
	}
	back := r.body["items"].([]any)[1].(map[string]any)
	if back["uid"] != uid || back["change"].(map[string]any)["to"] != "active" ||
		back["comment"].(map[string]any)["body"] != "Add the baseline" {
		t.Fatalf("sent back item %v", back)
	}
	next := r.body["cursor"].(string)
	if next == cursor {
		t.Fatal("cursor did not advance")
	}
	if again := updatesCall(t, app, "?since="+next); len(itemKinds(again)) != 0 {
		t.Fatalf("repeat: %s", again.raw)
	}
	// Filters.
	if f := updatesCall(t, app, "?since="+cursor+"&kinds=sent_back"); strings.Join(itemKinds(f), ",") != "sent_back" {
		t.Fatalf("kinds filter %s", f.raw)
	}
	if bad := gpuCall(t, app, "GET", "/api/agent/v1/updates?kinds=nope", nil, agentHeaders(app)); bad.code != 400 {
		t.Fatalf("bad kinds: %d", bad.code)
	}
	// A malformed or foreign cursor resets to now.
	if reset := updatesCall(t, app, "?since=u1.other.3"); reset.body["reset"] != true || reset.body["cursor"] != next {
		t.Fatalf("reset %s", reset.raw)
	}
	// People cannot read the agent feed without the token.
	if anon := gpuCall(t, app, "GET", "/api/agent/v1/updates", nil, nil); anon.code != 401 {
		t.Fatalf("no token: %d", anon.code)
	}
}

func TestAgentUpdatesLongPoll(t *testing.T) {
	app := homeApp(t)
	homeCreate(t, app, "Long poll", "")
	created := mustOK(t, gpuCall(t, app, "POST", "/api/agent/v1/units",
		map[string]any{"type": "task", "title": "Sweep"}, agentHeaders(app)), "create")
	uid := created.body["unit"].(map[string]any)["uid"].(string)
	cursor := updatesCall(t, app, "").body["cursor"].(string)
	// Nothing arrives: the wait times out with an empty batch.
	start := time.Now()
	empty := updatesCall(t, app, "?wait=0.3&since="+cursor)
	if el := time.Since(start); el < 250*time.Millisecond || len(itemKinds(empty)) != 0 {
		t.Fatalf("empty wait returned after %v: %s", el, empty.raw)
	}
	// A comment 300 ms into a 20 s wait returns it within about a second.
	go func() {
		time.Sleep(300 * time.Millisecond)
		gpuCall(t, app, "POST", "/api/units/"+uid+"/comments", map[string]any{"body": "Stop at 10 epochs"}, nil)
	}()
	start = time.Now()
	r := updatesCall(t, app, "?wait=20&since="+cursor)
	if el := time.Since(start); el > 1500*time.Millisecond || strings.Join(itemKinds(r), ",") != "comment" {
		t.Fatalf("long poll returned after %v: %s", el, r.raw)
	}
	if bad := gpuCall(t, app, "GET", "/api/agent/v1/updates?wait=soon&since="+cursor, nil, agentHeaders(app)); bad.code != 400 {
		t.Fatalf("bad wait: %d", bad.code)
	}
}

func TestAgentUpdatesWaitLimit(t *testing.T) {
	app := homeApp(t)
	r := httptest.NewRequest("GET", "/api/agent/v1/updates", nil)
	r.RemoteAddr = "127.0.0.1:50000"
	c := &reqCtx{app: app, r: r}
	var releases []func()
	for i := 0; i < updatesWaitsPerKey; i++ {
		release, ok := c.acquireWait()
		if !ok {
			t.Fatalf("slot %d refused", i)
		}
		releases = append(releases, release)
	}
	if _, ok := c.acquireWait(); ok {
		t.Fatal("wait limit not enforced")
	}
	releases[0]()
	if release, ok := c.acquireWait(); !ok {
		t.Fatal("released slot not reusable")
	} else {
		release()
	}
	for _, r := range releases[1:] {
		r()
	}
}

func TestAgentMessages(t *testing.T) {
	app := homeApp(t)
	homeCreate(t, app, "Messages", "")
	created := mustOK(t, gpuCall(t, app, "POST", "/api/agent/v1/units",
		map[string]any{"type": "task", "title": "Long run"}, agentHeaders(app)), "create")
	uid := created.body["unit"].(map[string]any)["uid"].(string)
	cursor := updatesCall(t, app, "").body["cursor"].(string)
	sent := mustOK(t, gpuCall(t, app, "POST", "/api/agent-messages",
		map[string]any{"body": "Pause the sweep after this epoch", "unit": uid}, nil), "send")
	msg := sent.body["message"].(map[string]any)
	if msg["status"] != "queued" || msg["target"] != "mine" || msg["unit"].(map[string]any)["uid"] != uid {
		t.Fatalf("sent %s", sent.raw)
	}
	r := updatesCall(t, app, "?since="+cursor)
	if strings.Join(itemKinds(r), ",") != "message" {
		t.Fatalf("kinds %s", r.raw)
	}
	item := r.body["items"].([]any)[0].(map[string]any)
	m := item["message"].(map[string]any)
	if m["body"] != "Pause the sweep after this epoch" || m["status"] != "delivered" {
		t.Fatalf("message item %v", item)
	}
	id := int64(m["id"].(float64))
	path := "/api/agent/v1/messages/" + strconv.FormatInt(id, 10)
	mustOK(t, gpuCall(t, app, "POST", path+"/ack", map[string]any{}, agentHeaders(app)), "ack")
	replied := mustOK(t, gpuCall(t, app, "POST", path+"/reply", map[string]any{"body": "Paused at epoch 7."},
		agentHeaders(app)), "reply")
	if rm := replied.body["message"].(map[string]any); rm["status"] != "answered" || len(rm["thread"].([]any)) != 1 {
		t.Fatalf("reply %s", replied.raw)
	}
	mine := mustOK(t, gpuCall(t, app, "GET", "/api/agent-messages?unit="+uid, nil, nil), "list")
	list := mine.body["messages"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["thread"].([]any)[0].(map[string]any)["body"] != "Paused at epoch 7." {
		t.Fatalf("person's list %s", mine.raw)
	}
	// The agent's reply is not an update for itself.
	if again := updatesCall(t, app, "?since="+r.body["cursor"].(string)); len(itemKinds(again)) != 0 {
		t.Fatalf("after reply: %s", again.raw)
	}
	// Secrets, bad targets and agents sending are refused.
	if s := gpuCall(t, app, "POST", "/api/agent-messages",
		map[string]any{"body": "use key sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGH"}, nil); s.code != 400 ||
		s.errCode() != "MESSAGE_SECRET" {
		t.Fatalf("secret: %d %s", s.code, s.raw)
	}
	if s := gpuCall(t, app, "POST", "/api/agent-messages", map[string]any{"body": "hi", "target": "everyone"}, nil); s.code != 400 {
		t.Fatalf("bad target: %d", s.code)
	}
	if s := gpuCall(t, app, "POST", "/api/agent-messages", map[string]any{"body": "hi"}, agentHeaders(app)); s.code != 403 {
		t.Fatalf("agent sending: %d %s", s.code, s.raw)
	}
	if s := gpuCall(t, app, "GET", "/api/agent/v1/messages/999", nil, agentHeaders(app)); s.code != 404 {
		t.Fatalf("unknown message: %d", s.code)
	}
	// Rate limit.
	limited := false
	for i := 0; i < messagesPerWindow+1; i++ {
		if s := gpuCall(t, app, "POST", "/api/agent-messages", map[string]any{"body": "ping"}, nil); s.code == 429 {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("no rate limit")
	}
}

// The watcher scripts hold no secrets and are served without a token, also
// in account mode, so the command that downloads them carries none.
func TestWatcherScriptsArePublic(t *testing.T) {
	app, _ := authDesktopApp(t)
	for _, path := range []string{"/api/agent/v1/watcher.sh", "/api/agent/v1/watcher.ps1"} {
		r := gpuCall(t, app, "GET", path, nil, nil)
		if r.code != 200 || !strings.Contains(string(r.raw), "ARK watcher") || strings.Contains(string(r.raw), "arka_") {
			t.Fatalf("%s: %d", path, r.code)
		}
	}
	if r := gpuCall(t, app, "GET", "/api/agent/v1/updates", nil, nil); r.code != 401 {
		t.Fatalf("the feed itself needs a token: %d", r.code)
	}
}
