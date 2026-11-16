package server

import "crypto/subtle"

// IssuedToken reports whether token is one this server handed out itself:
// the agent write token or a live task session's read token. The desktop
// gate (internal/desktopgate) admits such requests without its cookie so
// agent CLIs started by ARK keep reaching the API.
func (a *App) IssuedToken(token string) bool {
	if token == "" {
		return false
	}
	if agent := a.AgentToken(); agent != "" && subtle.ConstantTimeCompare([]byte(token), []byte(agent)) == 1 {
		return true
	}
	return a.Sessions != nil && a.Sessions.ForReadToken(token) != nil
}
