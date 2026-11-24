package server

// An agent hands work to review only with a review_note (422
// REVIEW_NOTE_REQUIRED, store/reviewnote_required.go); people are not
// blocked.

import (
	"strings"
	"testing"
)

func noteRequired(t *testing.T, r gpuReply, what string) {
	t.Helper()
	if r.code != 422 || r.errCode() != "REVIEW_NOTE_REQUIRED" {
		t.Fatalf("%s: want 422 REVIEW_NOTE_REQUIRED, got %d %s", what, r.code, r.raw)
	}
}

func TestReviewNoteRequiredForAgents(t *testing.T) {
	app := homeApp(t)
	homeCreate(t, app, "Notes", "")
	agent := agentHeaders(app)
	mk := func(title string) string {
		r := mustOK(t, gpuCall(t, app, "POST", "/api/agent/v1/units",
			map[string]any{"type": "task", "title": title, "body": "# Results\nacc 0.5\n\n# Setup\nseed 1"}, agent), "create")
		return r.body["unit"].(map[string]any)["uid"].(string)
	}
	a, b, c := mk("A"), mk("B"), mk("C")

	// Agent API: no note, a short note, a note without an anchor.
	r := gpuCall(t, app, "POST", "/api/agent/v1/units/"+a, map[string]any{"status": "review"}, agent)
	noteRequired(t, r, "no note")
	if msg := r.body["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "review_note") ||
		!strings.Contains(msg, "#results") || !strings.Contains(msg, "Example") && !strings.Contains(msg, "e.g.") {
		t.Fatalf("message does not say what to send: %s", msg)
	}
	noteRequired(t, gpuCall(t, app, "POST", "/api/agent/v1/units/"+a, map[string]any{"status": "review", "review_note": "done"}, agent), "short")
	noteRequired(t, gpuCall(t, app, "POST", "/api/agent/v1/units/"+a,
		map[string]any{"status": "review", "review_note": "Please check the new results table"}, agent), "no anchor")
	if u := mustOK(t, gpuCall(t, app, "GET", "/api/units/"+a, nil, nil), "get"); u.body["unit"].(map[string]any)["status"] == "review" {
		t.Fatal("a rejected write changed the status")
	}
	// Create straight into review needs one too.
	noteRequired(t, gpuCall(t, app, "POST", "/api/agent/v1/units",
		map[string]any{"type": "task", "title": "D", "status": "review"}, agent), "create in review")
	mustOK(t, gpuCall(t, app, "POST", "/api/agent/v1/units", map[string]any{"type": "task", "title": "D", "status": "review",
		"review_note": "Check the plan for the D run and its budget."}, agent), "create in review with a note")
	// With a note: fine.
	mustOK(t, gpuCall(t, app, "POST", "/api/agent/v1/units/"+a,
		map[string]any{"status": "review", "review_note": "Check [Results](#results): acc is now 0.5 with seed 1."}, agent), "with note")
	// Emptying the note while in review is refused as well.
	noteRequired(t, gpuCall(t, app, "POST", "/api/agent/v1/units/"+a, map[string]any{"review_note": ""}, agent), "clear note")

	// The web unit route with an agent token counts as an agent write.
	noteRequired(t, gpuCall(t, app, "PATCH", "/api/units/"+b, map[string]any{"status": "review"}, agent), "web route, agent token")

	// Batch: one unit without a usable note fails the whole batch.
	noteRequired(t, gpuCall(t, app, "POST", "/api/units/batch",
		map[string]any{"units": []any{b, c}, "patch": map[string]any{"status": "review"}}, agent), "batch")
	if u := mustOK(t, gpuCall(t, app, "GET", "/api/units/"+b, nil, nil), "get"); u.body["unit"].(map[string]any)["status"] == "review" {
		t.Fatal("a rejected batch changed a unit")
	}
	mustOK(t, gpuCall(t, app, "POST", "/api/units/batch", map[string]any{"updates": []any{
		map[string]any{"unit": b, "patch": map[string]any{"status": "review", "review_note": "Check [Results](#results) of B: acc 0.5."}},
		map[string]any{"unit": c, "patch": map[string]any{"status": "review", "review_note": "Check [Setup](#setup) of C: seed 1 only."}},
	}}, agent), "batch with notes")

	// A person in the web UI is not blocked.
	d := mk("E")
	mustOK(t, gpuCall(t, app, "PATCH", "/api/units/"+d, map[string]any{"status": "review"}, nil), "person without a note")

	// Resubmission after a send-back without a note: 422, and it says so.
	mustOK(t, gpuCall(t, app, "PATCH", "/api/units/"+a, map[string]any{"status": "active"}, nil), "send back")
	r = gpuCall(t, app, "POST", "/api/agent/v1/units/"+a, map[string]any{"status": "review"}, agent)
	noteRequired(t, r, "resubmit")
	if !strings.Contains(r.raw, "for each comment") {
		t.Fatalf("resubmit message: %s", r.raw)
	}
	mustOK(t, gpuCall(t, app, "POST", "/api/agent/v1/units/"+a, map[string]any{"status": "review",
		"review_note": "Fixed the comment: [Setup](#setup) now lists the seed."}, agent), "resubmit with note")
}

func TestReviewNoteRequiredForGroups(t *testing.T) {
	app := homeApp(t)
	homeCreate(t, app, "GroupNotes", "")
	agent := agentHeaders(app)
	var uids []any
	for _, title := range []string{"Claim A", "Claim B"} {
		r := mustOK(t, gpuCall(t, app, "POST", "/api/agent/v1/units",
			map[string]any{"type": "evidence", "title": title, "body": "# Claim\n" + title}, agent), "create")
		uids = append(uids, r.body["unit"].(map[string]any)["uid"].(string))
	}
	noteRequired(t, gpuCall(t, app, "POST", "/api/agent/v1/reviews",
		map[string]any{"title": "Delivery", "members": uids}, agent), "group without note")
	noteRequired(t, gpuCall(t, app, "POST", "/api/agent/v1/reviews",
		map[string]any{"title": "Delivery", "review_note": "Two claims from the B2 run, please check", "members": uids}, agent), "group without anchor")
	// A person creating a group is not blocked.
	mustOK(t, gpuCall(t, app, "POST", "/api/reviews", map[string]any{"title": "By hand", "members": uids}, nil), "person group")
	mustOK(t, gpuCall(t, app, "POST", "/api/agent/v1/reviews", map[string]any{"title": "Delivery",
		"review_note": "Two claims from the B2 run: start with [A](" + uids[0].(string) + "#claim).", "members": uids}, agent), "group with note")
}
