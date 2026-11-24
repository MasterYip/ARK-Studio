package server

// Review requests over HTTP (Go only, routes_reviews.go): review_note on
// the agent and web unit writes, GET .../review, review groups and the
// agent update feed items for them.

import (
	"strings"
	"testing"
)

func TestReviewNoteRoundTrip(t *testing.T) {
	app := homeApp(t)
	homeCreate(t, app, "Reviews", "")
	agent := agentHeaders(app)
	created := mustOK(t, gpuCall(t, app, "POST", "/api/agent/v1/units",
		map[string]any{"type": "experiment", "title": "Ablation", "body": "# Results\nacc 0.5"}, agent), "create")
	uid := created.body["unit"].(map[string]any)["uid"].(string)
	// Agent API: status review with a note; a bad anchor comes back as a warning.
	r := mustOK(t, gpuCall(t, app, "POST", "/api/agent/v1/units/"+uid,
		map[string]any{"status": "review", "review_note": "See [Results](#results) and [x](#missing)"}, agent), "review")
	warnings, _ := r.body["review_warnings"].([]any)
	if len(warnings) != 1 || !strings.Contains(warnings[0].(map[string]any)["problem"].(string), "missing") {
		t.Fatalf("warnings: %s", r.raw)
	}
	// Not in review: rejected.
	other := mustOK(t, gpuCall(t, app, "POST", "/api/agent/v1/units", map[string]any{"type": "note", "title": "n"}, agent), "other")
	ouid := other.body["unit"].(map[string]any)["uid"].(string)
	if r := gpuCall(t, app, "POST", "/api/agent/v1/units/"+ouid, map[string]any{"review_note": "x"}, agent); r.code != 400 {
		t.Fatalf("note without review: %d %s", r.code, r.raw)
	}
	// The project payload's needs_you item carries it.
	p := mustOK(t, gpuCall(t, app, "GET", "/api/project", nil, nil), "project")
	var item map[string]any
	for _, it := range p.body["needs_you"].([]any) {
		if it.(map[string]any)["uid"] == uid {
			item = it.(map[string]any)
		}
	}
	if item == nil || item["review_round"] != float64(1) || !strings.Contains(item["review_note"].(string), "Results") {
		t.Fatalf("needs item: %v", item)
	}
	// Web API: a person edits the note; GET /api/units/{uid}/review and the agent variant.
	mustOK(t, gpuCall(t, app, "PATCH", "/api/units/"+uid, map[string]any{"review_note": "Only the table"}, nil), "web note")
	for _, path := range []string{"/api/units/" + uid + "/review", "/api/agent/v1/units/" + uid + "/review"} {
		var headers map[string]string
		if strings.Contains(path, "/agent/") {
			headers = agent
		}
		info := mustOK(t, gpuCall(t, app, "GET", path, nil, headers), path)
		if info.body["note"] != "Only the table" || info.body["round"] != float64(1) || info.body["in_review"] != true {
			t.Fatalf("%s: %s", path, info.raw)
		}
	}
}

func TestReviewGroupRoutes(t *testing.T) {
	app := homeApp(t)
	homeCreate(t, app, "Groups", "")
	agent := agentHeaders(app)
	var uids []string
	for _, title := range []string{"Claim A", "Claim B"} {
		r := mustOK(t, gpuCall(t, app, "POST", "/api/agent/v1/units",
			map[string]any{"type": "evidence", "title": title, "body": "# Claim\n" + title}, agent), "create")
		uids = append(uids, r.body["unit"].(map[string]any)["uid"].(string))
	}
	feed := updatesCall(t, app, "")
	cursor := feed.body["cursor"].(string)
	// One member: rejected with a pointer to review_note.
	if r := gpuCall(t, app, "POST", "/api/agent/v1/reviews", map[string]any{"title": "x", "members": []any{uids[0]}}, agent); r.code != 400 ||
		!strings.Contains(r.raw, "review_note") {
		t.Fatalf("single member: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "POST", "/api/agent/v1/reviews", map[string]any{"members": uids}, agent); r.code != 400 {
		t.Fatalf("no title: %d %s", r.code, r.raw)
	}
	r := mustOK(t, gpuCall(t, app, "POST", "/api/agent/v1/reviews", map[string]any{
		"title": "Delivery 1", "summary": "Two claims", "review_note": "[A](" + uids[0] + "#claim) and media:999",
		"members": []any{map[string]any{"uid": uids[0], "note": "[claim](#claim)"}, uids[1]}}, agent), "create group")
	group := r.body["review"].(map[string]any)
	ref := group["id"].(string)
	if len(r.body["warnings"].([]any)) != 1 {
		t.Fatalf("warnings (media 999): %s", r.raw)
	}
	// Agents may not decide.
	if r := gpuCall(t, app, "POST", "/api/agent/v1/reviews/"+ref, map[string]any{"action": "accept"}, agent); r.code != 403 {
		t.Fatalf("agent decide: %d %s", r.code, r.raw)
	}
	// The person comments and sends one member back.
	mustOK(t, gpuCall(t, app, "POST", "/api/reviews/"+ref+"/comments", map[string]any{"body": "Which seed?"}, nil), "comment")
	out := mustOK(t, gpuCall(t, app, "POST", "/api/reviews/"+ref+"/decide", map[string]any{
		"action": "accept", "note": "B lacks a baseline", "members": map[string]any{uids[1]: "send_back"}}, nil), "decide")
	if out.body["review"].(map[string]any)["status"] != "sent-back" {
		t.Fatalf("decide: %s", out.raw)
	}
	kinds := itemKinds(updatesCall(t, app, "?since="+cursor))
	joined := strings.Join(kinds, ",")
	if !strings.Contains(joined, "comment") || !strings.Contains(joined, "sent_back") {
		t.Fatalf("feed kinds: %v", kinds)
	}
	// Resubmit, accept the rest: the group is gone.
	if r := gpuCall(t, app, "POST", "/api/agent/v1/reviews/"+ref, map[string]any{"status": "review"}, agent); r.code != 422 ||
		!strings.Contains(r.raw, "REVIEW_NOTE_REQUIRED") {
		t.Fatalf("resubmit without a note: %d %s", r.code, r.raw)
	}
	mustOK(t, gpuCall(t, app, "POST", "/api/agent/v1/reviews/"+ref, map[string]any{"status": "review",
		"review_note": "Added the baseline to [B](" + uids[1] + "#claim) as asked."}, agent), "resubmit")
	done := mustOK(t, gpuCall(t, app, "POST", "/api/reviews/"+ref+"/decide", map[string]any{"action": "accept"}, nil), "accept")
	if done.body["closed"] != true {
		t.Fatalf("accept all: %s", done.raw)
	}
	if r := gpuCall(t, app, "GET", "/api/reviews/"+ref, nil, nil); r.code == 200 {
		t.Fatalf("closed group still there: %s", r.raw)
	}
	list := mustOK(t, gpuCall(t, app, "GET", "/api/agent/v1/reviews", nil, agent), "list")
	if len(list.body["reviews"].([]any)) != 0 {
		t.Fatalf("list: %s", list.raw)
	}
	// Never a unit: search and the graph do not know it.
	if s := gpuCall(t, app, "GET", "/api/units?q=Delivery", nil, nil); strings.Contains(s.raw, "Delivery 1") {
		t.Fatalf("listed as unit: %s", s.raw)
	}
}
