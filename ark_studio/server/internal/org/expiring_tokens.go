package org

import (
	"context"
	"sort"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
)

// ExpiringTokens lists the person's live agent tokens that expire within
// `days` days (Go only, 2026-11-24; the Board and Home "Due soon" lists).
// Session tokens (short-lived, revoked with their session) and the starter
// tokens of other projects are left out; already expired tokens are not
// listed.  Soonest first; no secrets, only id, label, prefix and the
// expiry.
func (m *Manager) ExpiringTokens(ctx context.Context, username, project string, days int) ([]*pyjson.Object, error) {
	out := []*pyjson.Object{}
	user, ok, err := m.UserByName(ctx, username)
	if err != nil || !ok {
		return out, err
	}
	rows, err := m.rows(ctx, "SELECT * FROM agent_tokens WHERE user_id=? AND revoked_at IS NULL "+
		"AND expires_at IS NOT NULL AND expires_at != ''", user.Str("id"))
	if err != nil {
		return out, err
	}
	now := TokenClock()
	limit := now + float64(days)*86400
	for _, r := range rows {
		label := r.Str("label")
		if strings.HasPrefix(label, SessionTokenLabel) {
			continue
		}
		starter := isStarterLabel(label, "")
		if starter && strings.HasPrefix(label, starterLabelPrefix+" (") && (project == "" || !isStarterLabel(label, project)) {
			continue
		}
		at, ok := parseISO(r.Str("expires_at"))
		if !ok || at <= now || at > limit {
			continue
		}
		out = append(out, pyjson.NewObject("id", r.Get("id"), "label", label, "prefix", r.Str("prefix"),
			"expires_at", r.Get("expires_at"), "last_used_at", r.Get("last_used_at"), "starter", starter))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return pyjson.Str(out[i].Value("expires_at")) < pyjson.Str(out[j].Value("expires_at"))
	})
	return out, nil
}
