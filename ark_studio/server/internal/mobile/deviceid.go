package mobile

// Device identity (Go only, 2026-11-26).  A phone sends a stable, hashed
// install id with its sign-in and with every request: the Android app the
// SHA-256 of Settings.Secure.ANDROID_ID with an app salt, the web app a
// random id kept in localStorage (header X-ARK-Device or cookie ark_device).
// The server stores sha256("ark-device|"+id) cut to 32 hex digits, so the
// client's value never sits in the state file.
//
// (user, device id) is the device: a new sign-in from the same device takes
// over the device's earlier pairings.  The older pairings are revoked once
// the new token has been used (a lost sign-in reply does not lock the phone
// out), and until then they are hidden behind the new one.  Older apps send
// no id: they keep working, each sign-in stays its own device, and Settings
// and the resources graph offer to merge them by hand.

import (
	"regexp"
	"sort"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
)

// StaleAfter is how long a device may stay unseen before Settings shows it
// as stale (the person removes stale devices themselves).
const StaleAfter = 30 * 24 * 3600.0

var deviceIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

// DeviceKey turns a client device id into the stored key ("" when the id is
// missing or malformed).
func DeviceKey(clientID string) string {
	id := strings.TrimSpace(clientID)
	if !deviceIDRe.MatchString(id) {
		return ""
	}
	return sha("ark-device|" + id)[:32]
}

func isRevoked(r *Pairing) bool { return r != nil && r.RevokedAt != nil && *r.RevokedAt != 0 }
func isClaimed(r *Pairing) bool { return r != nil && r.ClaimedAt != nil && *r.ClaimedAt != 0 }

// activeDevice: a claimed, unrevoked phone (desktop links are not phones).
func activeDevice(r *Pairing) bool { return isClaimed(r) && !isRevoked(r) && r.Source != SourceDesktop }

// siblingsLocked are owner's other active pairings with device key.
func (m *Manager) siblingsLocked(r *Pairing, key string) []string {
	var out []string
	if key == "" {
		return out
	}
	for _, id := range m.order {
		o := m.pairings[id]
		if o == nil || o.ID == r.ID || o.DeviceKey != key || !activeDevice(o) ||
			!strings.EqualFold(strings.TrimSpace(o.User), strings.TrimSpace(r.User)) {
			continue
		}
		out = append(out, o.ID)
	}
	return out
}

// bindDeviceLocked gives r the device key and marks the device's earlier
// pairings for replacement.  A pairing never changes device.
func (m *Manager) bindDeviceLocked(r *Pairing, key string) bool {
	if key == "" || r.Source == SourceDesktop || (r.DeviceKey != "" && r.DeviceKey != key) {
		return false
	}
	changed := r.DeviceKey == ""
	r.DeviceKey = key
	for _, id := range m.siblingsLocked(r, key) {
		if !contains(r.Supersedes, id) {
			r.Supersedes = append(r.Supersedes, id)
			changed = true
		}
	}
	return changed
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// revokeLocked revokes a pairing (replacedBy: the pairing that took over).
func (m *Manager) revokeLocked(r *Pairing, replacedBy string) {
	if !isRevoked(r) {
		now := Clock()
		r.RevokedAt = &now
	}
	if replacedBy != "" {
		r.ReplacedBy = replacedBy
	}
	for _, h := range append([]string(nil), m.sorder...) {
		if m.sessions[h].PairingID == r.ID {
			m.dropSession(h)
		}
	}
}

// settleLocked revokes what r replaces: its token works.
func (m *Manager) settleLocked(r *Pairing) bool {
	if r == nil || len(r.Supersedes) == 0 {
		return false
	}
	for _, id := range r.Supersedes {
		if o := m.pairings[id]; o != nil && !isRevoked(o) {
			m.revokeLocked(o, r.ID)
		}
	}
	r.Supersedes = nil
	return true
}

// AdoptDevice records the device id a signed-in phone sends with a request
// (an app updated after it paired) and settles replaced pairings.
func (m *Manager) AdoptDevice(s *Session, clientID string) {
	if s == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.pairings[s.PairingID]
	if r == nil || isRevoked(r) {
		return
	}
	changed := false
	if key := DeviceKey(clientID); key != "" && r.DeviceKey == "" {
		changed = m.bindDeviceLocked(r, key)
	}
	if m.settleLocked(r) {
		changed = true
	}
	if changed {
		m.save()
	}
}

// hiddenLocked: pairings a newer sign-in of the same device replaces (shown
// as one device until the new token is used and they are revoked).
func (m *Manager) hiddenLocked() map[string]string {
	out := map[string]string{}
	for _, id := range m.order {
		r := m.pairings[id]
		if r == nil || isRevoked(r) {
			continue
		}
		for _, old := range r.Supersedes {
			out[old] = r.ID
		}
	}
	return out
}

// lastSeenLocked is the newest session request of each pairing and its
// user agent.
func (m *Manager) lastSeenLocked() (map[string]float64, map[string]string) {
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
	return lastSeen, agents
}

// seenAt is when a pairing was last used (its claim time without a session).
func seenAt(r *Pairing, lastSeen map[string]float64) float64 {
	if v, ok := lastSeen[r.ID]; ok {
		return v
	}
	if r.ClaimedAt != nil {
		return *r.ClaimedAt
	}
	return r.CreatedAt
}

// DeviceKind is the kind of device a pairing is ("Android phone", "iPhone",
// ...); merging needs one kind.
func (m *Manager) kindLocked(r *Pairing, agents map[string]string) string {
	return deviceLabel(r, agents[r.ID])
}

// MergePlan is what merging devices does: keep one pairing (the newest
// token), revoke the others.
func (m *Manager) mergePlanLocked(owner string, everyone bool, ids []string, like string) (*Pairing, []*Pairing, error) {
	lastSeen, agents := m.lastSeenLocked()
	var picked []*Pairing
	if like != "" {
		base := m.pairings[like]
		if !ownedBy(base, owner, everyone) || !activeDevice(base) {
			return nil, nil, merr(404, "Device not found", "PAIRING_NOT_FOUND")
		}
		kind := m.kindLocked(base, agents)
		for _, id := range m.order {
			r := m.pairings[id]
			if activeDevice(r) && strings.EqualFold(strings.TrimSpace(r.User), strings.TrimSpace(base.User)) &&
				m.kindLocked(r, agents) == kind && (r.DeviceKey == "" || base.DeviceKey == "" || r.DeviceKey == base.DeviceKey) {
				picked = append(picked, r)
			}
		}
	} else {
		seen := map[string]bool{}
		for _, id := range ids {
			r := m.pairings[id]
			if !ownedBy(r, owner, everyone) || seen[id] {
				return nil, nil, merr(404, "Device not found", "PAIRING_NOT_FOUND")
			}
			if !activeDevice(r) {
				return nil, nil, merr(409, "Only paired phones that are still active can be merged.", "DEVICE_NOT_ACTIVE")
			}
			seen[id] = true
			picked = append(picked, r)
		}
	}
	if len(picked) < 2 {
		return nil, nil, merr(400, "Pick at least two devices to merge.", "MERGE_TOO_FEW")
	}
	kind, user, key := m.kindLocked(picked[0], agents), strings.TrimSpace(picked[0].User), ""
	for _, r := range picked {
		if m.kindLocked(r, agents) != kind || !strings.EqualFold(strings.TrimSpace(r.User), user) {
			return nil, nil, merr(400, "Only devices of the same person and type can be merged.", "MERGE_MIXED")
		}
		if r.DeviceKey != "" {
			if key != "" && key != r.DeviceKey {
				return nil, nil, merr(409, "These devices report different device ids: they are different phones.", "MERGE_DIFFERENT_DEVICES")
			}
			key = r.DeviceKey
		}
	}
	sort.SliceStable(picked, func(i, j int) bool {
		a, b := seenAt(picked[i], lastSeen), seenAt(picked[j], lastSeen)
		if a != b {
			return a > b
		}
		return picked[i].CreatedAt > picked[j].CreatedAt
	})
	return picked[0], picked[1:], nil
}

// MergeDevices merges owner's pairings into one device: ids (or every active
// device of the same person and type as like).  The most recently used
// pairing stays signed in; the others are revoked.  dryRun only previews.
func (m *Manager) MergeDevices(owner string, everyone bool, ids []string, like string, dryRun bool) (*pyjson.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	keep, drop, err := m.mergePlanLocked(owner, everyone, ids, like)
	if err != nil {
		return nil, err
	}
	lastSeen, agents := m.lastSeenLocked()
	view := func(r *Pairing) *pyjson.Object {
		return m.deviceItemLocked(r, lastSeen, agents)
	}
	dropped := []any{}
	for _, r := range drop {
		dropped = append(dropped, view(r))
	}
	if !dryRun {
		key := keep.DeviceKey
		for _, r := range drop {
			if key == "" {
				key = r.DeviceKey
			}
			m.revokeLocked(r, keep.ID)
		}
		keep.DeviceKey = key
		var rest []string
		for _, id := range keep.Supersedes {
			if o := m.pairings[id]; o != nil && !isRevoked(o) {
				rest = append(rest, id)
			}
		}
		keep.Supersedes = rest
		m.save()
	}
	return pyjson.NewObject("keep", view(keep), "revoke", dropped, "dry_run", dryRun), nil
}

// StaleDevices lists (dryRun) or revokes owner's active devices unseen for
// StaleAfter seconds.
func (m *Manager) StaleDevices(owner string, everyone bool, dryRun bool) []any {
	m.mu.Lock()
	defer m.mu.Unlock()
	lastSeen, agents := m.lastSeenLocked()
	now := Clock()
	out := []any{}
	for _, id := range append([]string(nil), m.order...) {
		r := m.pairings[id]
		if !ownedBy(r, owner, everyone) || !activeDevice(r) || now-seenAt(r, lastSeen) < StaleAfter {
			continue
		}
		out = append(out, m.deviceItemLocked(r, lastSeen, agents))
		if !dryRun {
			m.revokeLocked(r, "")
		}
	}
	if !dryRun && len(out) > 0 {
		m.save()
	}
	return out
}

// deviceItemLocked is one device row (Settings, merge previews).
func (m *Manager) deviceItemLocked(r *Pairing, lastSeen map[string]float64, agents map[string]string) *pyjson.Object {
	item := r.Public()
	var seen any
	if v, ok := lastSeen[r.ID]; ok {
		seen = ISO(v)
	}
	stale := activeDevice(r) && Clock()-seenAt(r, lastSeen) >= StaleAfter
	var key any
	if r.DeviceKey != "" {
		key = r.DeviceKey[:12]
	}
	var replacedBy any
	if r.ReplacedBy != "" {
		replacedBy = r.ReplacedBy
	}
	item.Set("label", deviceLabel(r, agents[r.ID])).Set("last_seen_at", seen).
		Set("project_id", r.ProjectID).Set("scope", "user").Set("device_key", key).
		Set("device_id_missing", activeDevice(r) && r.DeviceKey == "").Set("stale", stale).
		Set("replaced_by", replacedBy)
	return item
}
