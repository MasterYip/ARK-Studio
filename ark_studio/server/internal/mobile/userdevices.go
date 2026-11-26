package mobile

import (
	"sort"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
)

// A pairing belongs to a user, not to a project (Settings → Mobile lists
// the person's devices across all projects).  owner matches the pairing's
// user case-insensitively; everyone (a server without accounts) matches all.

func ownedBy(r *Pairing, owner string, everyone bool) bool {
	if r == nil {
		return false
	}
	return everyone || (owner != "" && strings.EqualFold(strings.TrimSpace(r.User), strings.TrimSpace(owner)))
}

// deviceLabel names a device from its pairing source and user agent.
func deviceLabel(r *Pairing, agent string) string {
	agent = strings.ToLower(agent)
	switch {
	case r.Source == SourceDesktop:
		return "ARK Studio desktop"
	case strings.Contains(agent, "iphone"):
		return "iPhone"
	case strings.Contains(agent, "ipad"):
		return "iPad"
	case strings.Contains(agent, "android"):
		return "Android phone"
	}
	return "Phone"
}

// UserDevices lists the pairings of owner (newest first): codes still
// waiting for a scan, paired devices and revoked ones, each with the
// project the device is on now, its label and when it was last seen.
// Pairings a newer sign-in of the same device replaces are left out
// (deviceid.go).
func (m *Manager) UserDevices(owner string, everyone bool) []any {
	m.mu.Lock()
	defer m.mu.Unlock()
	lastSeen, agents := m.lastSeenLocked()
	hidden := m.hiddenLocked()
	var items []*pyjson.Object
	for _, id := range m.order {
		r := m.pairings[id]
		if !ownedBy(r, owner, everyone) || hidden[r.ID] != "" {
			continue
		}
		items = append(items, m.deviceItemLocked(r, lastSeen, agents))
	}
	sort.SliceStable(items, func(i, j int) bool {
		return pyjson.Str(items[i].Value("created_at")) > pyjson.Str(items[j].Value("created_at"))
	})
	out := []any{}
	for _, i := range items {
		out = append(out, i)
	}
	return out
}

// RevokeOwned revokes a pairing of owner and drops its sessions.
func (m *Manager) RevokeOwned(pairID, owner string, everyone bool) (*pyjson.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.pairings[pairID]
	if !ownedBy(r, owner, everyone) {
		return nil, merr(404, "Pairing session not found", "PAIRING_NOT_FOUND")
	}
	m.revokeLocked(r, "")
	m.save()
	return r.Public(), nil
}

// SetPermission changes what a paired device of owner may do: "full" (all
// the account may do in each project) or "read_only".  Live sessions follow
// at once.  Desktop links stay read-only.
func (m *Manager) SetPermission(pairID, owner string, everyone bool, permission any) (*pyjson.Object, error) {
	perm, err := Permission(permission)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.pairings[pairID]
	if !ownedBy(r, owner, everyone) {
		return nil, merr(404, "Pairing session not found", "PAIRING_NOT_FOUND")
	}
	if r.RevokedAt != nil && *r.RevokedAt != 0 {
		return nil, merr(409, "This device was revoked. Pair it again.", "PAIRING_REVOKED")
	}
	if r.Source == SourceDesktop && perm != "read_only" {
		return nil, merr(400, "A linked desktop is always read-only.", "INVALID_PERMISSION")
	}
	r.Permission = perm
	for _, h := range m.sorder {
		if s := m.sessions[h]; s != nil && s.PairingID == r.ID {
			s.Permission = perm
		}
	}
	m.save()
	return r.Public(), nil
}
