package mobile

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// Phones paired before pairings became user-scoped (a QR pairing was pinned
// to its project) load from the state file and can switch projects.
func TestOldQRPairingSwitchesProjects(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "mobile_sessions.json")
	now := Clock()
	token := "arkm_oldphonetoken"
	old := `{"format": "ark-mobile-sessions", "version": 1, "pairings": [` +
		`{"id": "pair_old", "token_hash": "x", "project_id": "prj_a", "project_name": "A", "permission": "full",` +
		` "created_at": 1, "expires_at": 2, "claimed_at": 1, "revoked_at": null, "session_ttl": 0, "user": "ana"},` +
		`{"id": "pair_desk", "token_hash": "y", "project_id": "prj_a", "project_name": "A", "permission": "read_only",` +
		` "created_at": 1, "expires_at": 2, "claimed_at": 1, "revoked_at": null, "session_ttl": 0, "user": "ana", "source": "desktop"}],` +
		` "sessions": [{"token_hash": "` + sha(token) + `", "pairing_id": "pair_old", "project_id": "prj_a", "project_name": "A",` +
		` "permission": "full", "created_at": 1, "expires_at": ` + ftoa(now+3600) + `, "last_seen_at": 1, "user_agent": "Android", "user": "ana"},` +
		`{"token_hash": "` + sha("arkm_desk") + `", "pairing_id": "pair_desk", "project_id": "prj_a", "project_name": "A",` +
		` "permission": "read_only", "created_at": 1, "expires_at": ` + ftoa(now+3600) + `, "last_seen_at": 1, "user_agent": "ARK-Studio-Desktop", "user": "ana"}]}`
	if err := os.WriteFile(state, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(state)
	s, err := m.Rebind(token, "prj_b", "B")
	if err != nil || s.ProjectID != "prj_b" || s.Permission != "full" {
		t.Fatalf("old QR pairing rebind: %v %+v", err, s)
	}
	if _, err := m.Rebind("arkm_desk", "prj_b", "B"); err == nil {
		t.Error("a desktop link moved")
	}
	// The move persists.
	if again := New(state).SessionForToken(token); again == nil || again.ProjectID != "prj_b" {
		t.Fatalf("after reload: %+v", again)
	}
	devices := m.UserDevices("ANA", false)
	if len(devices) != 2 || len(m.UserDevices("bob", false)) != 0 {
		t.Fatalf("devices: %v", devices)
	}
	if _, err := m.SetPermission("pair_old", "bob", false, "read_only"); err == nil {
		t.Error("someone else changed the permission")
	}
	if _, err := m.SetPermission("pair_desk", "ana", false, "full"); err == nil {
		t.Error("a desktop link became full")
	}
	if _, err := m.SetPermission("pair_old", "ana", false, "read_only"); err != nil || m.SessionForToken(token).Permission != "read_only" {
		t.Fatalf("set read-only: %v", err)
	}
	if _, err := m.RevokeOwned("pair_old", "ana", false); err != nil || m.SessionForToken(token) != nil {
		t.Fatalf("revoke: %v", err)
	}
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', 3, 64) }
