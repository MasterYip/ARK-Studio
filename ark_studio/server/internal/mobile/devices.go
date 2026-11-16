package mobile

import (
	"strings"

	"arkstudio/go/internal/compat/pyjson"
)

// ProjectDevices is PairingManager.project_devices: claimed, unrevoked
// phones of a project with their newest session request (read-only; used by
// the resources graph).  last_seen_at is nil without a session.
func (m *Manager) ProjectDevices(projectID string) []*pyjson.Object {
	m.mu.Lock()
	defer m.mu.Unlock()
	lastSeen := map[string]float64{}
	agents := map[string]string{}
	for _, hash := range m.sorder {
		s := m.sessions[hash]
		if s == nil {
			continue
		}
		if s.LastSeenAt >= lastSeen[s.PairingID] {
			lastSeen[s.PairingID] = s.LastSeenAt
			agents[s.PairingID] = s.UserAgent
		}
	}
	out := []*pyjson.Object{}
	for _, id := range m.order {
		r := m.pairings[id]
		if r == nil || r.ProjectID != projectID || (r.RevokedAt != nil && *r.RevokedAt != 0) ||
			r.ClaimedAt == nil || *r.ClaimedAt == 0 {
			continue
		}
		agent := strings.ToLower(agents[r.ID])
		label := "Phone"
		switch {
		case strings.Contains(agent, "iphone"):
			label = "iPhone"
		case strings.Contains(agent, "ipad"):
			label = "iPad"
		case strings.Contains(agent, "android"):
			label = "Android phone"
		}
		var seen any
		if v, ok := lastSeen[r.ID]; ok {
			seen = v
		}
		out = append(out, pyjson.NewObject("id", r.ID, "user", r.User, "label", label, "permission", r.Permission,
			"source", r.Source, "claimed_at", *r.ClaimedAt, "last_seen_at", seen))
	}
	return out
}
