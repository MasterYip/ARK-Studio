package server

// Agent tokens of sessions ARK launches (T-0053, 2026-11-20).  Account
// mode: the agent API takes only personal tokens, and the shared server
// token was refused (401), so each run gets a personal token of the person
// who launched it: write scope for full access, read scope otherwise, 24 h
// at most, revoked when the run finishes or the session is deleted.  Local
// mode keeps the shared server token for full-access runs.

import (
	"context"
	"time"

	"arkstudio/go/internal/sessions"
)

const sessionTokenTTL = 24 * time.Hour

func (a *App) sessionAgentToken(actor, sessionID, access string) (string, string, error) {
	if a.Auth == nil {
		if access == "full_access" {
			return a.AgentToken(), "", nil
		}
		return "", "", nil
	}
	if actor == "" || a.Org == nil {
		return "", "", nil
	}
	scope := "read"
	if access == "full_access" {
		scope = "write"
	}
	return a.Org.CreateSessionToken(context.Background(), actor, sessionID, scope, sessionTokenTTL)
}

func (a *App) revokeSessionTokens(s *sessions.Session) {
	if a.Sessions == nil || a.Org == nil {
		return
	}
	for _, id := range a.Sessions.TakeAgentTokenIDs(s) {
		_ = a.Org.RevokeTokenID(context.Background(), id)
	}
}
