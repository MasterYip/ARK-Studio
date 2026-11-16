package sessions

import (
	"strings"

	"arkstudio/go/internal/compat/pyjson"
)

// IDStatus is one session's id and display status.
type IDStatus struct{ ID, Status string }

// Visible lists the sessions of projectID that (user, admin) may see:
// SessionManager.list() filtered like Handler._accessible_session_ids
// (session_context.can_view: admin, shared, or the owner = meta "actor").
func (m *Manager) Visible(projectID, user string, admin bool) []IDStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []IDStatus
	for _, id := range m.order {
		meta := m.sessions[id].Meta
		project, _ := meta.Value("project").(*pyjson.Object)
		pid := ""
		if project != nil {
			pid = pyjson.Str(or(project.Value("id"), ""))
		}
		if pid != projectID {
			continue
		}
		owner := pyjson.Str(or(meta.Value("actor"), ""))
		if !(admin || pyjson.Truthy(meta.Value("shared")) || (owner != "" && strings.EqualFold(owner, user))) {
			continue
		}
		out = append(out, IDStatus{ID: pyjson.Str(meta.Value("id")), Status: displayOnly(m.displayStatus(meta))})
	}
	return out
}

func displayOnly(status, _ string) string { return status }
