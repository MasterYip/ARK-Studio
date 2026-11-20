package org

// Agent tokens for sessions ARK launches (T-0053, 2026-11-20).  In account
// mode the agent API takes only personal tokens, so a session gets its own
// personal token of the person who launched it: write scope for full
// access, read scope otherwise, a short lifetime, revoked when the session
// ends.  It does not count against the person's limit of active tokens and
// is labelled with the session id in their token list.

import (
	"context"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/rnd"
)

// SessionTokenLabel prefixes the label of a session's token.
const SessionTokenLabel = "ARK session "

// CreateSessionToken issues a token for one session; it returns the secret
// (never stored) and the token id.
func (m *Manager) CreateSessionToken(ctx context.Context, username, sessionID, scope string, ttl time.Duration) (string, string, error) {
	user, err := m.userOrError(ctx, username)
	if err != nil {
		return "", "", err
	}
	if scope != "read" && scope != "write" {
		return "", "", orgErr(400, "scope must be read or write", "AGENT_TOKEN_SCOPE_INVALID")
	}
	secret := tokenPrefix + rnd.URLSafe("agent_tokens", 30)
	now := TokenClock()
	id := "tok_" + rnd.Hex("agent_tokens", 8)
	label := SessionTokenLabel + sessionID
	if len(label) > maxTokenLabel {
		label = label[:maxTokenLabel]
	}
	if err := m.exec(ctx, "INSERT INTO agent_tokens(id,user_id,label,token_hash,prefix,scope,created_at,expires_at,last_used_at,revoked_at) "+
		"VALUES(?,?,?,?,?,?,?,?,NULL,NULL)", id, user.Str("id"), label, tokenHashOf(secret), secret[:len(tokenPrefix)+4], scope,
		isoAt(now), isoAt(now+ttl.Seconds())); err != nil {
		return "", "", err
	}
	if err := m.Log(ctx, user.Str("id"), "session.agent_token.create", "", pyjson.NewObject("token", id, "scope", scope,
		"session", sessionID)); err != nil {
		return "", "", err
	}
	return secret, id, nil
}

// RevokeTokenID revokes a token by id, whoever owns it (session end).
func (m *Manager) RevokeTokenID(ctx context.Context, tokenID string) error {
	_, err := m.execCount(ctx, "UPDATE agent_tokens SET revoked_at=? WHERE id=? AND revoked_at IS NULL", isoAt(TokenClock()), tokenID)
	return err
}
