package mobile

import (
	"path/filepath"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

const (
	phoneA    = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
	phoneB    = "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"
	androidUA = "HUAWEI ALN-AL00 · ARKStudioAndroid/3.0"
)

// signIn is one sign-in of a phone (a password login creates and claims a
// pairing at once); it returns the pairing id and the session token.
func signIn(t *testing.T, m *Manager, user, deviceID string) (string, string) {
	t.Helper()
	pair, err := m.Create("prj_1", "One", "full", int64(60), "permanent", "login:"+user+deviceID[:min(4, len(deviceID))], user, "password")
	if err != nil {
		t.Fatal(err)
	}
	out, err := m.ClaimDevice(pyjson.Str(pair.Value("token")), "prj_1", "login:"+user+deviceID[:min(4, len(deviceID))], androidUA, deviceID)
	if err != nil {
		t.Fatal(err)
	}
	return pyjson.Str(out.Value("pairing_id")), pyjson.Str(out.Value("mobile_token"))
}

func activeIDs(m *Manager, user string) []string {
	var out []string
	for _, d := range m.UserDevices(user, false) {
		o := d.(*pyjson.Object)
		if o.Value("state") == "claimed" {
			out = append(out, pyjson.Str(o.Value("id")))
		}
	}
	return out
}

func use(t *testing.T, m *Manager, token string) {
	t.Helper()
	if _, err := m.Authenticate(token, "prj_1", "GET", "/api/bootstrap"); err != nil {
		t.Fatalf("token does not work: %v", err)
	}
}

// Signing in again from the same phone keeps one device: the old pairing
// is hidden at once and revoked when the new token is first used.
func TestSameDeviceIDKeepsOneDevice(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state.json")
	m := New(state)
	first, oldToken := signIn(t, m, "ana", phoneA)
	use(t, m, oldToken)
	second, newToken := signIn(t, m, "ANA", phoneA)
	if got := activeIDs(m, "ana"); len(got) != 1 || got[0] != second {
		t.Fatalf("listed after re-sign-in: %v", got)
	}
	if devs := m.ProjectDevices("prj_1"); len(devs) != 1 || devs[0].Value("id") != second || devs[0].Value("device_key") == nil {
		t.Fatalf("graph devices: %v", devs)
	}
	// Until the new token is used, the old one still works (lost reply).
	if m.SessionForToken(oldToken) == nil {
		t.Fatal("old token revoked before the new one was used")
	}
	use(t, m, newToken)
	if m.SessionForToken(oldToken) != nil {
		t.Fatal("old token still works after the new one was used")
	}
	reloaded := New(state)
	if r := reloaded.pairings[first]; r == nil || !isRevoked(r) || r.ReplacedBy != second {
		t.Fatalf("old pairing after reload: %+v", r)
	}
	if r := reloaded.pairings[second]; r == nil || r.DeviceKey == "" || len(r.Supersedes) != 0 {
		t.Fatalf("new pairing after reload: %+v", r)
	}
}

func TestDifferentDevicesStaySeparate(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "state.json"))
	_, a := signIn(t, m, "ana", phoneA)
	_, b := signIn(t, m, "ana", phoneB)
	_, c := signIn(t, m, "bob", phoneA) // same phone id, another account
	use(t, m, a)
	use(t, m, b)
	use(t, m, c)
	if got := activeIDs(m, "ana"); len(got) != 2 {
		t.Fatalf("ana: %v", got)
	}
	if got := activeIDs(m, "bob"); len(got) != 1 {
		t.Fatalf("bob: %v", got)
	}
	if len(m.ProjectDevices("prj_1")) != 3 {
		t.Fatal("graph merged different devices")
	}
}

// Apps without a device id keep working and are not deduplicated; Settings
// shows the update hint, and the next request with an id adopts it.
func TestOldAppWithoutDeviceID(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "state.json"))
	p1, t1 := signIn(t, m, "ana", "")
	_, t2 := signIn(t, m, "ana", "")
	use(t, m, t1)
	use(t, m, t2)
	if got := activeIDs(m, "ana"); len(got) != 2 {
		t.Fatalf("old app sign-ins: %v", got)
	}
	for _, d := range m.UserDevices("ana", false) {
		if d.(*pyjson.Object).Value("device_id_missing") != true {
			t.Fatalf("no update hint: %v", d)
		}
	}
	if DeviceKey("short") != "" || DeviceKey("bad id with spaces!!!!") != "" {
		t.Fatal("malformed ids accepted")
	}
	// The app updates: its live token adopts the id; a later sign-in of the
	// same phone replaces that pairing only.
	m.AdoptDevice(m.SessionForToken(t1), phoneA)
	if m.pairings[p1].DeviceKey == "" {
		t.Fatal("device id not adopted")
	}
	p3, t3 := signIn(t, m, "ana", phoneA)
	use(t, m, t3)
	if m.SessionForToken(t1) != nil || m.SessionForToken(t2) == nil {
		t.Fatal("re-sign-in revoked the wrong pairing")
	}
	if got := activeIDs(m, "ana"); len(got) != 2 || !(got[0] == p3 || got[1] == p3) {
		t.Fatalf("after update: %v", got)
	}
}

func TestMergeKeepsNewestAndRevokesOthers(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "state.json"))
	base := 1_000_000.0
	clock := base
	saved := Clock
	Clock = func() float64 { return clock }
	defer func() { Clock = saved }()
	p1, t1 := signIn(t, m, "ana", "")
	clock += 100
	p2, t2 := signIn(t, m, "ana", "")
	clock += 100
	p3, t3 := signIn(t, m, "ana", "")
	clock += 100
	use(t, m, t1) // the oldest pairing was used last: it is the one to keep
	_, other := signIn(t, m, "bob", "")
	desk, err := m.Create("prj_1", "One", "read_only", int64(60), "permanent", "desk", "ana", SourceDesktop)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Claim(pyjson.Str(desk.Value("token")), "prj_1", "desk", "ARK-Studio-Desktop"); err != nil {
		t.Fatal(err)
	}

	preview, err := m.MergeDevices("ana", false, nil, p2, true)
	if err != nil {
		t.Fatal(err)
	}
	keep := preview.Value("keep").(*pyjson.Object)
	if keep.Value("id") != p1 || len(preview.Value("revoke").([]any)) != 2 {
		t.Fatalf("preview: %v", preview)
	}
	if m.SessionForToken(t2) == nil || m.SessionForToken(t3) == nil {
		t.Fatal("a preview revoked something")
	}
	if _, err := m.MergeDevices("bob", false, []string{p1, p2}, "", false); err == nil {
		t.Fatal("someone else merged ana's devices")
	}
	if _, err := m.MergeDevices("ana", false, []string{p1}, "", false); err == nil {
		t.Fatal("merged a single device")
	}
	if _, err := m.MergeDevices("ana", false, nil, p3, false); err != nil {
		t.Fatal(err)
	}
	if m.SessionForToken(t1) == nil || m.SessionForToken(t2) != nil || m.SessionForToken(t3) != nil {
		t.Fatal("merge kept the wrong token")
	}
	if m.SessionForToken(other) == nil {
		t.Fatal("merge touched another account")
	}
	if got := activeIDs(m, "ana"); len(got) != 2 { // the phone and the desktop link
		t.Fatalf("after merge: %v", got)
	}
	if r := m.pairings[p2]; r.ReplacedBy != p1 {
		t.Fatalf("replaced_by: %q", r.ReplacedBy)
	}
	// Devices that report different ids are different phones.
	_, ta := signIn(t, m, "cy", phoneA)
	_, tb := signIn(t, m, "cy", phoneB)
	use(t, m, ta)
	use(t, m, tb)
	ids := activeIDs(m, "cy")
	if _, err := m.MergeDevices("cy", false, ids, "", false); err == nil {
		t.Fatal("merged two phones with different ids")
	}
}

func TestStaleDevicesNeedTheUser(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "state.json"))
	clock := 1_000_000.0
	saved := Clock
	Clock = func() float64 { return clock }
	defer func() { Clock = saved }()
	_, old := signIn(t, m, "ana", "")
	clock += 10 * 24 * 3600
	_, recent := signIn(t, m, "ana", phoneB)
	clock += 25 * 24 * 3600
	use(t, m, recent)
	stale := 0
	for _, d := range m.UserDevices("ana", false) {
		if d.(*pyjson.Object).Value("stale") == true {
			stale++
		}
	}
	if stale != 1 || m.SessionForToken(old) == nil {
		t.Fatalf("stale shown %d; listing revoked: %v", stale, m.SessionForToken(old) == nil)
	}
	if got := m.StaleDevices("ana", false, true); len(got) != 1 || m.SessionForToken(old) == nil {
		t.Fatal("dry run revoked")
	}
	if got := m.StaleDevices("ana", false, false); len(got) != 1 || m.SessionForToken(old) != nil || m.SessionForToken(recent) == nil {
		t.Fatal("stale cleanup revoked the wrong device")
	}
}
