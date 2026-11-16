package org

import (
	"context"

	"arkstudio/go/internal/sqlitedb"
)

// SettingsRow is OrgStore.settings_row (personal-device preferences use the
// scope "devices"; see go/internal/resources).
func (m *Manager) SettingsRow(ctx context.Context, userID, scope string) (sqlitedb.Row, bool, error) {
	return m.row(ctx, "SELECT * FROM user_settings WHERE user_id=? AND scope=?", userID, scope)
}
