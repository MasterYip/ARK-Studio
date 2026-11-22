package sessions

import (
	"strings"

	"arkstudio/go/internal/compat/pyjson"
)

// Go only: what the notification feed (server/notifications.go) reads of
// every session, in one pass under the lock.

// AlertApproval is one pending approval request of a session.
type AlertApproval struct {
	ID, Kind, Tool, Command, Path, Description, RequestedAt string
}

// Alert is one session as the feed sees it.  Status is the display status;
// Restarted marks a session that ARK stopped because the server restarted
// while it ran (Open).
type Alert struct {
	ID, Title, ProjectID, Owner string
	Shared                      bool
	Status, Error, Notice       string
	EndedAt                     string
	ExitCode                    any
	Restarted                   bool
	Approvals                   []AlertApproval
}

// restartNotice is the notice Open sets on sessions it finds still active.
const restartNotice = "ARK restarted"

// Alerts lists every session (newest last, like List).
func (m *Manager) Alerts() []Alert {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Alert, 0, len(m.order))
	for _, id := range m.order {
		s := m.sessions[id]
		if s == nil {
			continue
		}
		meta := s.Meta
		status, _ := m.displayStatus(meta)
		a := Alert{ID: id, Title: strOr(meta.Value("title")), ProjectID: strOr(objOr(meta.Value("project")).Value("id")),
			Owner: strOr(meta.Value("actor")), Shared: pyjson.Truthy(meta.Value("shared")), Status: status,
			Error: strOr(meta.Value("error")), Notice: strOr(meta.Value("notice")), EndedAt: strOr(meta.Value("ended_at")),
			ExitCode: meta.Value("exit_code")}
		a.Restarted = status == "stopped" && strings.HasPrefix(a.Notice, restartNotice)
		for _, r := range objects(meta.Value("approvals")) {
			if r.Value("status") != "pending" {
				continue
			}
			a.Approvals = append(a.Approvals, AlertApproval{ID: strOr(r.Value("id")), Kind: strOr(r.Value("kind")),
				Tool: strOr(r.Value("tool")), Command: strOr(r.Value("command")), Path: strOr(r.Value("path")),
				Description: strOr(r.Value("description")), RequestedAt: strOr(r.Value("requested_at"))})
		}
		out = append(out, a)
	}
	return out
}
