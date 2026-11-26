package sessions

import (
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

func finishByID(m *Manager, id string) Finish {
	for _, f := range m.Finishes() {
		if f.ID == id {
			return f
		}
	}
	return Finish{}
}

func TestResultOutcome(t *testing.T) {
	cases := []struct {
		obj             *pyjson.Object
		outcome, reason string
	}{
		{pyjson.NewObject("type", "result", "subtype", "success", "is_error", false, "result", "All done."), "done", ""},
		{pyjson.NewObject("type", "result", "subtype", "success", "is_error", true,
			"result", "API Error: 529 overloaded\nretry later"), "error", "API Error: 529 overloaded\nretry later"},
		{pyjson.NewObject("type", "result", "subtype", "error_max_turns", "is_error", true), "error", "error max turns"},
	}
	for _, c := range cases {
		if o, r := resultOutcome(c.obj); o != c.outcome || r != c.reason {
			t.Errorf("%v: got %q %q", c.obj, o, r)
		}
	}
	// The stored reason is one short line.
	s := &Session{Meta: pyjson.NewObject()}
	setTurnEndLocked(s, "error", "  API Error: 529 overloaded\nretry later")
	if te := s.Meta.Value("turn_end").(*pyjson.Object); te.Value("reason") != "API Error: 529 overloaded" || te.Value("outcome") != "error" {
		t.Errorf("turn_end: %v", te)
	}
	setTurnEndLocked(s, "error", strings.Repeat("x", 400))
	if r := strOr(s.Meta.Value("turn_end").(*pyjson.Object).Value("reason")); len(r) != turnEndReasonMax {
		t.Errorf("reason length %d", len(r))
	}
}

func TestFinishOf(t *testing.T) {
	te := func(outcome string) *pyjson.Object {
		return pyjson.NewObject("at", "2026-11-26T10:00:00Z", "outcome", outcome, "reason", "")
	}
	project := pyjson.NewObject("id", "prj_a")
	// A stream session waiting after a clean turn: done, not busy.
	waiting := pyjson.NewObject("title", "WM-HOI", "actor", "ana", "project", project, "stream_input", true,
		"turn_state", "waiting", "turn_end", te("done"))
	if f := finishOf("s_1", waiting, "idle", "running"); f.Outcome != "done" || f.Busy || !f.Main || f.Owner != "ana" || f.ProjectID != "prj_a" {
		t.Errorf("waiting: %+v", f)
	}
	// The next turn runs: busy.
	running := pyjson.NewObject("stream_input", true, "turn_state", "running", "turn_end", te("done"))
	if f := finishOf("s_1", running, "running", "running"); !f.Busy {
		t.Errorf("running: %+v", f)
	}
	// A one-shot session still in its turn (idle only by quiet output): busy.
	if f := finishOf("s_2", pyjson.NewObject(), "idle", "running"); !f.Busy || f.Outcome != "" {
		t.Errorf("one-shot turn: %+v", f)
	}
	// A child session an agent started is not the main conversation; a fork is.
	if f := finishOf("s_3", pyjson.NewObject("parent_id", "s_1", "relation", "", "turn_end", te("done")), "stopped", "stopped"); f.Main {
		t.Errorf("sub-agent session: %+v", f)
	}
	if f := finishOf("s_4", pyjson.NewObject("parent_id", "s_1", "relation", "side_chat", "turn_end", te("done")), "stopped", "stopped"); !f.Main {
		t.Errorf("side chat: %+v", f)
	}
	// Errored after a clean turn: the error wins.
	errored := pyjson.NewObject("turn_end", te("done"), "ended_at", "2026-11-26T10:05:00Z", "error", "process exited with code 2")
	if f := finishOf("s_5", errored, "errored", "errored"); f.Outcome != "error" || f.Reason != "process exited with code 2" || f.At != "2026-11-26T10:05:00Z" {
		t.Errorf("errored: %+v", f)
	}
	// Closed, or unknown outcomes.
	if f := finishOf("s_6", pyjson.NewObject("closed", true, "turn_end", te("done")), "stopped", "stopped"); !f.Closed {
		t.Errorf("closed: %+v", f)
	}
	if f := finishOf("s_7", pyjson.NewObject("turn_end", te("odd")), "stopped", "stopped"); f.Outcome != "" {
		t.Errorf("odd outcome: %+v", f)
	}
}

func TestStreamTurnEnds(t *testing.T) {
	m := streamManager(t)
	s, err := m.Start(nil, pyjson.NewObject("platform", "claude", "prompt", "hello", "title", "WM-HOI"), "http://127.0.0.1:1", "alice")
	if err != nil {
		t.Fatal(err)
	}
	defer killSession(m, s)
	// A clean turn: done, waiting for the person.
	waitFor(t, "first turn", func() bool { return metaOf(m, s, "turn_state") == "waiting" })
	f := finishByID(m, s.ID)
	if f.Outcome != "done" || f.Busy || !f.Main || f.Owner != "alice" || f.At == "" {
		t.Fatalf("after the first turn: %+v", f)
	}
	first := f.At
	if te, _ := m.Public(s, false).Value("turn_end").(*pyjson.Object); te == nil || te.Value("outcome") != "done" {
		t.Fatalf("payload turn_end: %v", te)
	}
	// The next turn: busy while it runs, a new finish after.
	if _, err := m.Steer(nil, s.ID, "slow work", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "slow turn", func() bool { return strings.Contains(logText(m, s), "echo: slow work") })
	if f := finishByID(m, s.ID); !f.Busy {
		t.Fatalf("during a turn: %+v", f)
	}
	// Stop interrupts the turn: the person stopped it, nothing to report.
	if _, err := m.RequestStop(s.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "interrupted", func() bool { return metaOf(m, s, "turn_state") == "waiting" })
	if f := finishByID(m, s.ID); f.Outcome != "" {
		t.Fatalf("after an interrupt: %+v", f)
	}
	if _, err := m.Steer(nil, s.ID, "again", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second finish", func() bool { return finishByID(m, s.ID).Outcome == "done" })
	if f := finishByID(m, s.ID); f.At < first {
		t.Fatalf("second finish: %+v (first %s)", f, first)
	}
	// A crash: error with the reason.
	if _, err := m.Steer(nil, s.ID, "crash now", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "crashed", func() bool { return !m.IsActive(s) })
	if f := finishByID(m, s.ID); f.Outcome != "error" || !strings.Contains(f.Reason, "code 2") || f.Busy {
		t.Fatalf("after a crash: %+v", f)
	}
}
