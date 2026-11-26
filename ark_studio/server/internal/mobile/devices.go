package mobile

import (
	"arkstudio/go/internal/compat/pyjson"
)

// ProjectDevices is PairingManager.project_devices: claimed, unrevoked
// phones of a project with their newest session request (read-only; used by
// the resources graph).  last_seen_at is nil without a session.  Pairings
// a newer sign-in of the same device replaces are left out.
func (m *Manager) ProjectDevices(projectID string) []*pyjson.Object {
	m.mu.Lock()
	defer m.mu.Unlock()
	lastSeen, agents := m.lastSeenLocked()
	hidden := m.hiddenLocked()
	out := []*pyjson.Object{}
	for _, id := range m.order {
		r := m.pairings[id]
		if r == nil || r.ProjectID != projectID || (r.RevokedAt != nil && *r.RevokedAt != 0) ||
			r.ClaimedAt == nil || *r.ClaimedAt == 0 || hidden[r.ID] != "" {
			continue
		}
		label := deviceLabel(r, agents[r.ID])
		var seen any
		if v, ok := lastSeen[r.ID]; ok {
			seen = v
		}
		item := pyjson.NewObject("id", r.ID, "user", r.User, "label", label, "permission", r.Permission,
			"source", r.Source, "claimed_at", *r.ClaimedAt, "last_seen_at", seen)
		// Go only (deviceid.go): one graph node per device across sign-ins.
		if r.DeviceKey != "" {
			item.Set("device_key", r.DeviceKey[:12])
		}
		out = append(out, item)
	}
	return out
}
