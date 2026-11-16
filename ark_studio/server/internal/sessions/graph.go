package sessions

import "arkstudio/go/internal/compat/pyjson"

// GraphItems is the part of SessionManager.list()["sessions"] the resources
// graph reads, one object per session: "owner" (meta actor or ""), "status"
// (display status), "project" (meta project or {}) and "worktree" (the meta
// worktree object when it has a path, else None, like public()).
func (m *Manager) GraphItems() []*pyjson.Object {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*pyjson.Object, 0, len(m.order))
	for _, id := range m.order {
		s := m.sessions[id]
		if s == nil {
			continue
		}
		meta := s.Meta
		var worktree any
		if w, ok := meta.Value("worktree").(*pyjson.Object); ok && pyjson.Truthy(w.Value("path")) {
			worktree = w
		}
		out = append(out, pyjson.NewObject("owner", or(meta.Value("actor"), ""), "status", displayOnly(m.displayStatus(meta)),
			"project", or(meta.Value("project"), pyjson.NewObject()), "worktree", worktree))
	}
	return out
}
