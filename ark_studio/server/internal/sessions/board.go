package sessions

import (
	"sort"

	"arkstudio/go/internal/compat/pyjson"
)

// BoardItems are the sessions of one project for the Board and Home support
// panel (GET /api/project/support, Go only, 2026-11-24): every session the
// viewer may see (allowed: AccessibleIDs), newest activity first, with what
// the panel shows — title, provider and model, display status, background
// work, pending approvals, the unit it works on and the error of a failed
// run.  Reads meta only (no transcript beyond Busy's incremental look).
func (m *Manager) BoardItems(projectID string, allowed map[string]bool) []*pyjson.Object {
	var out []*pyjson.Object
	for _, s := range m.all() {
		if !allowed[s.ID] {
			continue
		}
		m.mu.Lock()
		meta := deepCopy(s.Meta).(*pyjson.Object)
		m.mu.Unlock()
		if strOr(objOr(meta.Value("project")).Value("id")) != projectID {
			continue
		}
		status, raw := m.displayStatus(meta)
		_, pending, _ := approvalState(meta)
		linked := meta.Value("linked_units")
		if !pyjson.Truthy(linked) {
			linked = []any{}
		}
		item := pyjson.NewObject("id", or(meta.Value("id"), s.ID), "title", or(meta.Value("title"), ""),
			"platform", or(meta.Value("platform"), ""), "platform_label", or(meta.Value("platform_label"), ""),
			"model", or(meta.Value("model"), ""), "agent", or(meta.Value("agent"), ""),
			"status", status, "raw_status", raw, "approvals_pending", pending,
			"task_ref", or(meta.Value("task_ref"), ""), "linked_units", linked,
			"owner", or(meta.Value("actor"), ""), "parent_id", or(meta.Value("parent_id"), ""),
			"relation", or(meta.Value("relation"), ""), "side_chat_of", or(meta.Value("side_chat_of"), ""),
			"created_at", meta.Value("created_at"), "last_activity_at", or(meta.Value("last_activity_at"), meta.Value("created_at")),
			"ended_at", meta.Value("ended_at"), "error", or(meta.Value("error"), ""),
			"closed", pyjson.Truthy(meta.Value("closed")))
		if b := m.Busy(s); b.Any() {
			item.Set("busy", b.public())
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strOr(out[i].Value("last_activity_at")) > strOr(out[j].Value("last_activity_at"))
	})
	return out
}
