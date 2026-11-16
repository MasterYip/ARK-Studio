package org

import (
	"context"
	"errors"

	"arkstudio/go/internal/platform/clock"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/linuxacct"
)

// Linux account mapping (OrgManager.assign_linux_account, revoke_linux_account,
// selected_linux_account, linux_accounts_for).

func linuxErr(err error) error {
	var le *linuxacct.Error
	if errors.As(err, &le) {
		return &Error{le.Status, le.Message, le.Code}
	}
	return err
}

// AssignLinuxAccount is OrgManager.assign_linux_account.
func (m *Manager) AssignLinuxAccount(ctx context.Context, actor, userID string, body *pyjson.Object) (*pyjson.Object, error) {
	if _, err := m.requireOrgAdmin(ctx, actor); err != nil {
		return nil, err
	}
	target, ok, err := m.UserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, orgErr(404, "user not found", "USER_NOT_FOUND")
	}
	if _, has, err := m.membershipRole(ctx, target.Str("id")); err != nil {
		return nil, err
	} else if !has {
		return nil, orgErr(400, "the user is not an organization member", "USER_NOT_MEMBER")
	}
	info, err := linuxacct.Validate(pytext.Strip(strArg(body.Value("account"))))
	if err != nil {
		return nil, linuxErr(err)
	}
	actorID := m.userIDFor(ctx, actor)
	if err := m.exec(ctx, "INSERT INTO linux_accounts(user_id,account,assigned_by,created_at) "+
		"VALUES(?,?,?,?) ON CONFLICT(user_id,account) DO UPDATE SET assigned_by=excluded.assigned_by",
		target.Str("id"), info.Account, actorID, clock.UTCNow()); err != nil {
		return nil, err
	}
	if err := m.Log(ctx, actorID, "org.linux_account.assign", "", pyjson.NewObject("user", target.Get("username"), "account", info.Account)); err != nil {
		return nil, err
	}
	return pyjson.NewObject("user_id", target.Get("id"), "username", target.Get("username"), "account", info.Account,
		"home", info.Home, "uid", info.UID, "capability", linuxacct.Available()), nil
}

// SelectedLinuxAccount is OrgManager.selected_linux_account.
func (m *Manager) SelectedLinuxAccount(ctx context.Context, username string) (string, error) {
	user, ok, err := m.UserByName(ctx, username)
	if err != nil || !ok || pyjson.Truthy(user.Get("disabled")) {
		return "", err
	}
	row, ok, err := m.row(ctx, "SELECT * FROM user_settings WHERE user_id=? AND scope=?", user.Str("id"), "environment")
	if err != nil {
		return "", err
	}
	raw := "{}"
	if ok && row.Str("data_json") != "" {
		raw = row.Str("data_json")
	}
	data, _ := pyjson.LoadsObject(raw)
	selected := pytext.Strip(strArg(data.Value("linux_account")))
	if selected == "" {
		return "", nil
	}
	rows, err := m.rows(ctx, "SELECT * FROM linux_accounts WHERE user_id=? ORDER BY account", user.Str("id"))
	if err != nil {
		return "", err
	}
	for _, r := range rows {
		if r.Str("account") == selected {
			return selected, nil
		}
	}
	return "", nil
}

// LinuxAccountsFor is OrgManager.linux_accounts_for.
func (m *Manager) LinuxAccountsFor(ctx context.Context, username string) ([]string, error) {
	user, ok, err := m.UserByName(ctx, username)
	if err != nil || !ok || pyjson.Truthy(user.Get("disabled")) {
		return nil, err
	}
	rows, err := m.rows(ctx, "SELECT * FROM linux_accounts WHERE user_id=? ORDER BY account", user.Str("id"))
	out := []string{}
	for _, r := range rows {
		out = append(out, r.Str("account"))
	}
	return out, err
}

// SaveSettings is OrgStore.save_settings (secrets "" = leave user_secrets alone).
func (m *Manager) SaveSettings(ctx context.Context, userID, scope string, data *pyjson.Object, secrets *string) error {
	now := clock.UTCNow()
	if data == nil {
		data = pyjson.NewObject()
	}
	if err := m.exec(ctx, "INSERT INTO user_settings(user_id,scope,data_json,updated_at) VALUES(?,?,?,?) "+
		"ON CONFLICT(user_id,scope) DO UPDATE SET data_json=excluded.data_json, updated_at=excluded.updated_at",
		userID, scope, string(pyjson.Dumps(data)), now); err != nil {
		return err
	}
	if secrets != nil {
		return m.exec(ctx, "INSERT INTO user_secrets(user_id,scope,data_json,updated_at) "+
			"VALUES(?,?,?,?) ON CONFLICT(user_id,scope) DO UPDATE SET "+
			"data_json=excluded.data_json, updated_at=excluded.updated_at", userID, scope, *secrets, now)
	}
	return nil
}

// SaveSecrets is OrgStore.save_secrets: replace only the sealed-secrets row
// (used by repair-on-read).
func (m *Manager) SaveSecrets(ctx context.Context, userID, scope, secrets string) error {
	return m.exec(ctx, "INSERT INTO user_secrets(user_id,scope,data_json,updated_at) "+
		"VALUES(?,?,?,?) ON CONFLICT(user_id,scope) DO UPDATE SET "+
		"data_json=excluded.data_json, updated_at=excluded.updated_at", userID, scope, secrets, clock.UTCNow())
}

// RevokeLinuxAccount is OrgManager.revoke_linux_account.
func (m *Manager) RevokeLinuxAccount(ctx context.Context, actor, userID, account string) (*pyjson.Object, error) {
	if _, err := m.requireOrgAdmin(ctx, actor); err != nil {
		return nil, err
	}
	target, ok, err := m.UserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, orgErr(404, "user not found", "USER_NOT_FOUND")
	}
	account = pytext.Strip(account)
	selected, err := m.SelectedLinuxAccount(ctx, target.Str("username"))
	if err != nil {
		return nil, err
	}
	n, err := m.execCount(ctx, "DELETE FROM linux_accounts WHERE user_id=? AND account=?", target.Str("id"), account)
	if err != nil {
		return nil, err
	}
	removed := n > 0
	if removed && selected == account {
		if err := m.SaveSettings(ctx, target.Str("id"), "environment", pyjson.NewObject(), nil); err != nil {
			return nil, err
		}
	}
	if err := m.Log(ctx, m.userIDFor(ctx, actor), "org.linux_account.revoke", "", pyjson.NewObject("user", target.Get("username"),
		"account", account, "removed", removed)); err != nil {
		return nil, err
	}
	return pyjson.NewObject("removed", removed, "user_id", target.Get("id"), "username", target.Get("username"), "account", account), nil
}
