package org

// Organization options (Go only): small values in org_options (the table is
// created with the invite schema, invites.go).

import (
	"context"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/platform/clock"
)

// Option is the stored value of key ("" and false when it was never set).
func (m *Manager) Option(ctx context.Context, key string) (string, bool, error) {
	r, ok, err := m.row(ctx, "SELECT value FROM org_options WHERE key=?", key)
	if err != nil || !ok {
		return "", false, err
	}
	return r.Str("value"), true, nil
}

// SetOption stores value under key and logs action with detail. Callers
// check the permission first (see RequireOrgAdmin).
func (m *Manager) SetOption(ctx context.Context, actor, key, value, action string, detail *pyjson.Object) error {
	actorID := m.userIDFor(ctx, actor)
	if err := m.exec(ctx, "INSERT INTO org_options(key,value,updated_at,updated_by) VALUES(?,?,?,?) "+
		"ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at, updated_by=excluded.updated_by",
		key, value, clock.UTCNow(), actorID); err != nil {
		return err
	}
	if action == "" {
		return nil
	}
	if detail == nil {
		detail = pyjson.NewObject()
	}
	return m.Log(ctx, actorID, action, "", detail)
}
