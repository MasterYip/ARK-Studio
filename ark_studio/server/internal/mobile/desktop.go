package mobile

import "strings"

// Desktop links (Go only; the frozen Python server has no counterpart).
//
// "Link this desktop" on a server's page (POST /api/desktop/link) creates a
// pairing with source "desktop"; the ARK Studio desktop app claims it with
// the ordinary pairing claim (POST /api/mobile/pair/claim) and keeps the
// device token. A desktop link is a device session like a phone's (revocable
// in the project's device list, ended by a password change), but its scope is
// minimal: it may only list the account's projects and create projects
// (/api/desktop/projects), read or end its own session (/api/mobile/session)
// and read /api/health. Nothing else of the server is reachable with it.

// SourceDesktop is the pairing source of a desktop link.
const SourceDesktop = "desktop"

// DesktopLinkTTL is how long a link code may be claimed (seconds).
const DesktopLinkTTL = 120

// DesktopProjectsPath lists (GET) and creates (POST) projects for a link.
const DesktopProjectsPath = "/api/desktop/projects"

// DesktopLinkPathAllowed reports whether a desktop-link session may call
// method path.
func DesktopLinkPathAllowed(path, method string) bool {
	verb := strings.ToUpper(method)
	read := verb == "GET" || verb == "HEAD"
	switch path {
	case DesktopProjectsPath:
		return read || verb == "POST"
	case "/api/mobile/session":
		return read || verb == "DELETE"
	case "/api/health":
		return read
	}
	return false
}

// IsDesktopLink reports whether s belongs to a desktop link.
func (m *Manager) IsDesktopLink(s *Session) bool {
	if s == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.pairings[s.PairingID]
	return r != nil && r.Source == SourceDesktop
}
