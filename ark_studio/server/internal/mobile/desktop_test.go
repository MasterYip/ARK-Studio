package mobile

import (
	"path/filepath"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

func TestDesktopLinkPathAllowed(t *testing.T) {
	cases := []struct {
		path, method string
		ok           bool
	}{
		{DesktopProjectsPath, "GET", true},
		{DesktopProjectsPath, "head", true},
		{DesktopProjectsPath, "POST", true},
		{DesktopProjectsPath, "DELETE", false},
		{"/api/mobile/session", "GET", true},
		{"/api/mobile/session", "DELETE", true},
		{"/api/mobile/session", "POST", false},
		{"/api/health", "GET", true},
		{"/api/health", "POST", false},
		{"/api/projects", "GET", false},
		{"/api/units", "GET", false},
		{"/api/mobile/session/projects", "GET", false},
		{DesktopProjectsPath + "/x", "GET", false},
	}
	for _, tc := range cases {
		if got := DesktopLinkPathAllowed(tc.path, tc.method); got != tc.ok {
			t.Errorf("%s %s: %v, want %v", tc.method, tc.path, got, tc.ok)
		}
	}
}

func claimed(t *testing.T, m *Manager, source string) string {
	t.Helper()
	pair, err := m.Create("prj_1", "One", "read_only", int64(DesktopLinkTTL), "permanent", "test", "alice", source)
	if err != nil {
		t.Fatal(err)
	}
	out, err := m.Claim(pyjson.Str(pair.Value("token")), "prj_1", "test", "ARK-Studio-Desktop")
	if err != nil {
		t.Fatal(err)
	}
	return pyjson.Str(out.Value("mobile_token"))
}

func TestDesktopLinkSessions(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "state.json"))
	desk := claimed(t, m, SourceDesktop)
	phone := claimed(t, m, "qr")

	if !m.IsDesktopLink(m.SessionForToken(desk)) || m.IsDesktopLink(m.SessionForToken(phone)) || m.IsDesktopLink(nil) {
		t.Fatal("IsDesktopLink")
	}
	// Read-only on its project, but it may create projects.
	if _, err := m.Authenticate(desk, "prj_1", "POST", DesktopProjectsPath); err != nil {
		t.Errorf("desktop create: %v", err)
	}
	if _, err := m.Authenticate(desk, "prj_1", "POST", "/api/units"); err == nil {
		t.Error("desktop write elsewhere must stay read-only")
	}
	// A read-only phone gets no such exception.
	if _, err := m.Authenticate(phone, "prj_1", "POST", DesktopProjectsPath); err == nil {
		t.Error("read-only phone create")
	}
	// The source survives a restart; the device list labels it.
	again := New(filepath.Join(filepath.Dir(m.statePath), "state.json"))
	if !again.IsDesktopLink(again.SessionForToken(desk)) {
		t.Error("source lost on reload")
	}
	devices := again.ProjectDevices("prj_1")
	labels := map[string]bool{}
	for _, d := range devices {
		labels[pyjson.Str(d.Value("label"))+"/"+pyjson.Str(d.Value("source"))] = true
	}
	if !labels["ARK Studio desktop/desktop"] || !labels["Phone/qr"] {
		t.Errorf("device labels: %v", labels)
	}
	// An unknown source is still a QR pairing.
	pair, _ := m.Create("prj_1", "One", "full", nil, nil, "test", "alice", "other")
	if r := m.Find(pyjson.Str(pair.Value("token"))); r == nil || r.Source != "qr" {
		t.Errorf("source fallback: %+v", r)
	}
}
