package server

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// IssuedToken reports whether token is one this server handed out itself and
// would accept: the agent write token, a live task session's read token, or
// a valid personal arka_ token from this server's token store (minted in
// Settings > Agent tokens, a "Copy prompt" starter token, or the token of a
// run ARK launched). The desktop gate (internal/api/desktopgate) admits such
// requests without its cookie so agents keep reaching the API; the request
// then goes through the normal authentication and permission checks.
//
// Personal tokens are looked up with the same check the agent API uses
// (authenticatePersonalToken: hash lookup, revoked, expired, disabled user,
// organisation membership), uncached, so a revocation takes effect at once.
// Failed personal-token guesses count against the same per-client limiter.
func (a *App) IssuedToken(r *http.Request, token string) bool {
	if token == "" {
		return false
	}
	if agent := a.AgentToken(); agent != "" && subtle.ConstantTimeCompare([]byte(token), []byte(agent)) == 1 {
		return true
	}
	if a.Sessions != nil && a.Sessions.ForReadToken(token) != nil {
		return true
	}
	if !strings.HasPrefix(token, "arka_") || a.Org == nil {
		return false
	}
	client := splitHostPort(r.RemoteAddr)
	if a.failures.check(client) != nil {
		return false
	}
	_, _, ok, err := a.authenticatePersonalToken(r.Context(), token)
	if err == nil && !ok {
		a.failures.record(client)
	}
	return err == nil && ok
}
