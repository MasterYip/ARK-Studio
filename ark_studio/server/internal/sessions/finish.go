package sessions

import (
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
)

// Go only: how a session's last turn ended, for the session list's dots, the
// "finished" bubbles of Execute › Runs and the finish notifications
// (server/session_notices.go, server/notifications.go).
//
//   meta.turn_end  {at, outcome, reason}: outcome "done" (the main
//                  conversation's turn ended cleanly and the agent waits for
//                  the person) or "error" (the turn or the process failed;
//                  reason is a short text).  nil when the person stopped the
//                  turn, and on sessions from before this field.
//
// Only the main conversation counts: results of sub-agents (a
// parent_tool_use_id) never set it, and child sessions an agent started
// (parent_id without a fork relation) are not "main" (Finish.Main).

const turnEndReasonMax = 160

// Finish is one session as the finished bubbles and notifications see it.
type Finish struct {
	ID, Title, ProjectID, Owner string
	// Outcome is "done" or "error" (empty: nothing to report).
	Outcome, At, Reason string
	// Main: the person's own conversation (not a session an agent started).
	Main bool
	// Busy: a turn runs now (the last finish is no longer news).
	Busy bool
	// Closed: the person closed the session.
	Closed bool
}

// setTurnEndLocked records how the turn ended (caller holds m.mu).
func setTurnEndLocked(s *Session, outcome, reason string) {
	reason = pytext.Strip(reason)
	if i := strings.IndexByte(reason, '\n'); i >= 0 {
		reason = pytext.Strip(reason[:i])
	}
	s.Meta.Set("turn_end", pyjson.NewObject("at", NowISO(), "outcome", outcome, "reason", limit(reason, turnEndReasonMax)))
}

// resultOutcome reads a stream `result` event: "done", or "error" with a
// short reason (API errors, usage limits, max turns).
func resultOutcome(obj *pyjson.Object) (string, string) {
	sub := strOr(obj.Value("subtype"))
	if !pyjson.Truthy(obj.Value("is_error")) && (sub == "" || sub == "success") {
		return "done", ""
	}
	reason := strOr(obj.Value("result"))
	if reason == "" {
		reason = strings.ReplaceAll(sub, "_", " ")
	}
	if reason == "" {
		reason = "error"
	}
	return "error", reason
}

// finishOf derives a session's Finish from its meta and display status.
func finishOf(id string, meta *pyjson.Object, status, raw string) Finish {
	f := Finish{ID: id, Title: strOr(meta.Value("title")), ProjectID: strOr(objOr(meta.Value("project")).Value("id")),
		Owner: strOr(meta.Value("actor")), Closed: pyjson.Truthy(meta.Value("closed"))}
	f.Main = strOr(meta.Value("parent_id")) == "" || strOr(meta.Value("relation")) != ""
	waiting := pyjson.Truthy(meta.Value("stream_input")) && meta.Value("turn_state") == "waiting"
	f.Busy = activeStatuses[raw] && !waiting
	if te, ok := meta.Value("turn_end").(*pyjson.Object); ok {
		f.Outcome, f.At, f.Reason = strOr(te.Value("outcome")), strOr(te.Value("at")), strOr(te.Value("reason"))
		if f.Outcome != "done" && f.Outcome != "error" {
			f.Outcome = ""
		}
	}
	if status == "errored" && f.Outcome == "done" { // ended badly after a clean turn
		f.Outcome, f.At, f.Reason = "error", strOr(meta.Value("ended_at")), strOr(meta.Value("error"))
	}
	return f
}

// Finishes lists every session's Finish (newest last, like List).
func (m *Manager) Finishes() []Finish {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Finish, 0, len(m.order))
	for _, id := range m.order {
		s := m.sessions[id]
		if s == nil {
			continue
		}
		status, raw := m.displayStatus(s.Meta)
		out = append(out, finishOf(id, s.Meta, status, raw))
	}
	return out
}
