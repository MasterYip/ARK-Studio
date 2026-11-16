package org

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"arkstudio/go/internal/auth"
	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pytext"
	"arkstudio/go/internal/rnd"
	"arkstudio/go/internal/settings"
	"arkstudio/go/internal/sqlitedb"
	"arkstudio/go/internal/store"
)

// Organization administration of arkstudio/org.py (OrgManager write side):
// users, passwords, teams, project grants, Linux accounts, the self-service
// profile and avatars, and personal agent tokens.

const (
	maxDisplayName = 80
	maxTeamName    = 60
	maxBio         = 160
	maxEmail       = 254
	maxAvatarBytes = 200 * 1024
	maxAvatarPx    = 1024
)

// Roles and their rank (ROLE_RANK).
var (
	Roles = []string{"owner", "admin", "member", "viewer"}

	avatarColors = []string{"#4f46e5", "#0ea5e9", "#0d9488", "#16a34a", "#ca8a04",
		"#ea580c", "#dc2626", "#db2777", "#9333ea", "#475569"}
	avatarTypes     = map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/webp": "webp"}
	profileEditable = []string{"display_name", "bio", "email", "avatar", "prefs"}
	emailRe         = regexp.MustCompile(`^[^@` + pytext.SpaceClass + `<>"'(),;:\\]+@[^@` + pytext.SpaceClass +
		`<>"'(),;:\\]+\.[^@` + pytext.SpaceClass + `<>"'(),;:\\]+$`)
	controlRe = regexp.MustCompile(`[\x00-\x1f\x7f]+`)
	spacesRe  = regexp.MustCompile(`[` + pytext.SpaceClass + `]{2,}`)
	userIDRe  = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
)

func orgErr(status int, message, code string) error { return &Error{status, message, code} }

// conflictErr is OrgConflict.
func conflictErr(message, code string) error { return &Error{409, message, code} }

func strArg(v any) string {
	if !pyjson.Truthy(v) {
		return ""
	}
	return pyjson.Str(v)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ValidateRole is validate_role.
func ValidateRole(value any) (string, error) {
	role := pytext.Lower(pytext.Strip(strArg(value)))
	if !contains(Roles, role) {
		return "", orgErr(400, "role must be one of owner, admin, member, viewer", "INVALID_ROLE")
	}
	return role, nil
}

// ValidatePermission is validate_permission.
func ValidatePermission(value any) (string, error) {
	p := pytext.Lower(pytext.Strip(strArg(value)))
	if p != "read" && p != "write" && p != "admin" {
		return "", orgErr(400, "permission must be read, write or admin", "INVALID_PERMISSION")
	}
	return p, nil
}

func validateUsername(value string) (string, error) {
	u, err := auth.ValidateUsername(value)
	if err != nil {
		return "", orgErr(400, err.Error(), "INVALID_USERNAME")
	}
	return u, nil
}

func cleanName(value any, field string, limit int) (string, error) {
	text := pytext.Strip(strings.ReplaceAll(strArg(value), "\x00", ""))
	if text == "" {
		return "", orgErr(400, field+" is required", "INVALID_NAME")
	}
	if pytext.Len(text) > limit {
		return "", orgErr(400, fmt.Sprintf("%s is longer than %d characters", field, limit), "INVALID_NAME")
	}
	return text, nil
}

func (m *Manager) userIDFor(ctx context.Context, username string) string {
	id, _ := m.UserIDFor(ctx, username)
	return id
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// insertUser is OrgStore.insert_user.
func (m *Manager) insertUser(ctx context.Context, username, passwordHash, display string) (sqlitedb.Row, error) {
	username, err := validateUsername(username)
	if err != nil {
		return sqlitedb.Row{}, err
	}
	if pytext.Strip(passwordHash) == "" {
		return sqlitedb.Row{}, orgErr(400, "a password is required", "PASSWORD_REQUIRED")
	}
	now := store.UTCNow()
	userID := "usr_" + rnd.Hex("org", 8)
	err = m.exec(ctx, "INSERT INTO users(id,username,display_name,password_hash,disabled,created_at,updated_at) VALUES(?,?,?,?,0,?,?)",
		userID, username, pytext.Prefix(pytext.Strip(display), maxDisplayName), passwordHash, now, now)
	if isUnique(err) {
		return sqlitedb.Row{}, conflictErr("user already exists: "+username, "USER_EXISTS")
	}
	if err != nil {
		return sqlitedb.Row{}, err
	}
	row, _, err := m.UserByID(ctx, userID)
	return row, err
}

// updateUserFields is OrgStore.update_user / update_profile: keys in order.
func (m *Manager) updateUserFields(ctx context.Context, userID string, keys []string, values []any) error {
	if len(keys) == 0 {
		return nil
	}
	sets := make([]string, len(keys))
	for i, k := range keys {
		sets[i] = k + "=?"
	}
	args := append(append([]any{}, values...), store.UTCNow(), userID)
	return m.exec(ctx, "UPDATE users SET "+strings.Join(sets, ", ")+", updated_at=? WHERE id=?", args...)
}

func (m *Manager) membershipRole(ctx context.Context, userID string) (string, bool, error) {
	row, ok, err := m.Membership(ctx, userID, m.OrgID(ctx))
	if err != nil || !ok {
		return "", false, err
	}
	return row.Str("role"), true, nil
}

func (m *Manager) ownerCount(ctx context.Context) (int, error) {
	owners, err := m.Owners(ctx)
	return len(owners), err
}

// CreateUser is OrgManager.create_user.
func (m *Manager) CreateUser(ctx context.Context, actor string, body *pyjson.Object) (*pyjson.Object, error) {
	actorRole, err := m.requireOrgAdmin(ctx, actor)
	if err != nil {
		return nil, err
	}
	username, err := validateUsername(strArg(body.Value("username")))
	if err != nil {
		return nil, err
	}
	hash, err := auth.HashPassword(strArg(body.Value("password")), auth.Iterations)
	if err != nil {
		return nil, orgErr(400, err.Error(), "PASSWORD_INVALID")
	}
	roleArg := body.Value("role")
	if !pyjson.Truthy(roleArg) {
		roleArg = "member"
	}
	role, err := ValidateRole(roleArg)
	if err != nil {
		return nil, err
	}
	if role == "owner" && actorRole != "owner" {
		return nil, orgErr(403, "Only an owner can add another owner", "ORG_OWNER_REQUIRED")
	}
	display := pytext.Prefix(pytext.Strip(strArg(body.Value("display_name"))), maxDisplayName)
	user, err := m.insertUser(ctx, username, hash, display)
	if err != nil {
		return nil, err
	}
	if err := m.setMembership(ctx, user.Str("id"), role, m.OrgID(ctx)); err != nil {
		return nil, err
	}
	if err := m.Log(ctx, m.userIDFor(ctx, actor), "org.user.create", "", pyjson.NewObject("user", username, "role", role)); err != nil {
		return nil, err
	}
	row, _, err := m.UserByID(ctx, user.Str("id"))
	if err != nil {
		return nil, err
	}
	return UserPublic(row, role), nil
}

func (m *Manager) requireOrgAdmin(ctx context.Context, username string) (string, error) {
	role, ok, err := m.RoleFor(ctx, username)
	if err != nil {
		return "", err
	}
	if !ok || role == "" {
		return "", orgErr(403, "Your account is not a member of this organization.", "ORG_FORBIDDEN")
	}
	rank, known := RoleRank[role]
	if !known || rank < RoleRank["admin"] {
		return "", orgErr(403, "This action needs an organization admin or owner.", "ORG_FORBIDDEN")
	}
	return role, nil
}

// UpdateUser is OrgManager.update_user; it returns the public user and
// whether the account was disabled by this call.
func (m *Manager) UpdateUser(ctx context.Context, actor, userID string, body *pyjson.Object) (*pyjson.Object, error) {
	actorRole, err := m.requireOrgAdmin(ctx, actor)
	if err != nil {
		return nil, err
	}
	target, ok, err := m.UserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, orgErr(404, "user not found", "USER_NOT_FOUND")
	}
	targetRole, hasRole, err := m.membershipRole(ctx, target.Str("id"))
	if err != nil {
		return nil, err
	}
	if hasRole && targetRole == "owner" && actorRole != "owner" {
		return nil, orgErr(403, "Only an owner can change an owner account", "ORG_OWNER_REQUIRED")
	}
	var keys []string
	var values []any
	if body.Has("display_name") {
		keys = append(keys, "display_name")
		values = append(values, pytext.Prefix(pytext.Strip(strArg(body.Value("display_name"))), maxDisplayName))
	}
	disabling := false
	if body.Has("disabled") {
		if target.Str("username") == actor {
			return nil, orgErr(409, "you cannot disable your own account", "ORG_SELF")
		}
		v := int64(0)
		if pyjson.Truthy(body.Value("disabled")) {
			v, disabling = 1, true
		}
		keys = append(keys, "disabled")
		values = append(values, v)
	}
	newRole := ""
	if r := body.Value("role"); r != nil && r != "" {
		if newRole, err = ValidateRole(r); err != nil {
			return nil, err
		}
	}
	if newRole != "" && newRole != targetRole {
		if newRole == "owner" && actorRole != "owner" {
			return nil, orgErr(403, "Only an owner can promote another owner", "ORG_OWNER_REQUIRED")
		}
		if targetRole == "owner" {
			if actorRole != "owner" {
				return nil, orgErr(403, "Only an owner can change an owner", "ORG_OWNER_REQUIRED")
			}
			if n, err := m.ownerCount(ctx); err != nil {
				return nil, err
			} else if n <= 1 {
				return nil, orgErr(409, "the organization needs at least one owner", "LAST_OWNER")
			}
		}
	}
	if newRole == "owner" && targetRole != "owner" {
		if n, err := m.ownerCount(ctx); err != nil {
			return nil, err
		} else if n >= 50 {
			return nil, orgErr(409, "too many owners", "TOO_MANY_OWNERS")
		}
	}
	if len(keys) > 0 {
		if err := m.updateUserFields(ctx, target.Str("id"), keys, values); err != nil {
			return nil, err
		}
		if disabling {
			if _, err := m.revokeTokensOf(ctx, target.Str("id")); err != nil {
				return nil, err
			}
		}
	}
	if newRole != "" {
		if err := m.setMembership(ctx, target.Str("id"), newRole, m.OrgID(ctx)); err != nil {
			return nil, err
		}
	}
	fields := append([]string(nil), keys...)
	if newRole != "" && !contains(fields, "role") {
		fields = append(fields, "role")
	}
	sort.Strings(fields)
	if err := m.Log(ctx, m.userIDFor(ctx, actor), "org.user.update", "", pyjson.NewObject("user", target.Get("username"),
		"fields", stringsAny(fields))); err != nil {
		return nil, err
	}
	row, _, err := m.UserByID(ctx, target.Str("id"))
	if err != nil {
		return nil, err
	}
	var role any = newRole
	if newRole == "" {
		role = nil
		if hasRole {
			role = targetRole
		}
	}
	return UserPublic(row, role), nil
}

func stringsAny(items []string) []any {
	out := make([]any, len(items))
	for i, s := range items {
		out[i] = s
	}
	return out
}

// DeleteUser is OrgManager.delete_user.
func (m *Manager) DeleteUser(ctx context.Context, actor, userID string) (*pyjson.Object, error) {
	actorRole, err := m.requireOrgAdmin(ctx, actor)
	if err != nil {
		return nil, err
	}
	target, ok, err := m.UserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, orgErr(404, "user not found", "USER_NOT_FOUND")
	}
	if target.Str("username") == actor {
		return nil, orgErr(409, "you cannot delete your own account", "ORG_SELF")
	}
	if role, has, err := m.membershipRole(ctx, target.Str("id")); err != nil {
		return nil, err
	} else if has && role == "owner" {
		if actorRole != "owner" {
			return nil, orgErr(403, "Only an owner can remove an owner", "ORG_OWNER_REQUIRED")
		}
		if n, err := m.ownerCount(ctx); err != nil {
			return nil, err
		} else if n <= 1 {
			return nil, orgErr(409, "the organization needs at least one owner", "LAST_OWNER")
		}
	}
	id := target.Str("id")
	err = m.DB.Write(ctx, func(q sqlitedb.Querier) error {
		for _, sql := range []string{"DELETE FROM memberships WHERE user_id=?", "DELETE FROM team_members WHERE user_id=?",
			"DELETE FROM project_grants WHERE user_id=?", "DELETE FROM user_settings WHERE user_id=?",
			"DELETE FROM user_secrets WHERE user_id=?", "DELETE FROM users WHERE id=?"} {
			if _, err := q.ExecContext(ctx, sql, id); err != nil {
				return err
			}
		}
		return nil
	}, nil)
	if err != nil {
		return nil, err
	}
	if err := m.exec(ctx, "DELETE FROM agent_tokens WHERE user_id=?", id); err != nil {
		return nil, err
	}
	if err := m.Log(ctx, m.userIDFor(ctx, actor), "org.user.delete", "", pyjson.NewObject("user", target.Get("username"))); err != nil {
		return nil, err
	}
	return pyjson.NewObject("deleted", true, "user", UserPublic(target, nil)), nil
}

// ChangePassword is OrgManager.change_password.
func (m *Manager) ChangePassword(ctx context.Context, actor string, body *pyjson.Object) (*pyjson.Object, error) {
	user, ok, err := m.UserByName(ctx, actor)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, orgErr(404, "user not found", "USER_NOT_FOUND")
	}
	if !auth.VerifyPassword(user.Str("password_hash"), strArg(body.Value("current_password"))) {
		return nil, orgErr(401, "current password is incorrect", "PASSWORD_INVALID")
	}
	hash, err := auth.HashPassword(strArg(body.Value("new_password")), auth.Iterations)
	if err != nil {
		return nil, orgErr(400, err.Error(), "PASSWORD_INVALID")
	}
	if err := m.updateUserFields(ctx, user.Str("id"), []string{"password_hash"}, []any{hash}); err != nil {
		return nil, err
	}
	if err := m.Log(ctx, user.Str("id"), "org.password.change", "", nil); err != nil {
		return nil, err
	}
	result := pyjson.NewObject("changed", true)
	if pyjson.Truthy(body.Value("revoke_agent_tokens")) {
		n, err := m.RevokeUserTokens(ctx, user.Str("username"))
		if err != nil {
			return nil, err
		}
		result.Set("agent_tokens_revoked", int64(n))
	}
	return result, nil
}

// ResetPassword is OrgManager.reset_password.
func (m *Manager) ResetPassword(ctx context.Context, actor, userID string, body *pyjson.Object) (*pyjson.Object, error) {
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
	targetRole, _, err := m.RoleFor(ctx, target.Str("username"))
	if err != nil {
		return nil, err
	}
	actorRole, _, err := m.RoleFor(ctx, actor)
	if err != nil {
		return nil, err
	}
	if targetRole == "owner" && actorRole != "owner" {
		return nil, orgErr(403, "Only an owner can reset an owner's password", "ORG_OWNER_REQUIRED")
	}
	hash, err := auth.HashPassword(strArg(body.Value("new_password")), auth.Iterations)
	if err != nil {
		return nil, orgErr(400, err.Error(), "PASSWORD_INVALID")
	}
	if err := m.updateUserFields(ctx, target.Str("id"), []string{"password_hash"}, []any{hash}); err != nil {
		return nil, err
	}
	if err := m.Log(ctx, m.userIDFor(ctx, actor), "org.password.reset", "", pyjson.NewObject("user", target.Get("username"))); err != nil {
		return nil, err
	}
	return pyjson.NewObject("reset", true), nil
}

// ------------------------------------------------------------------ teams

func (m *Manager) teamByID(ctx context.Context, id string) (sqlitedb.Row, bool, error) {
	return m.row(ctx, "SELECT * FROM teams WHERE id=?", id)
}

func (m *Manager) teamByName(ctx context.Context, name string) (sqlitedb.Row, bool, error) {
	return m.row(ctx, "SELECT * FROM teams WHERE org_id=? AND name=? COLLATE NOCASE", m.OrgID(ctx), pytext.Strip(name))
}

// CreateTeam is OrgManager.create_team.
func (m *Manager) CreateTeam(ctx context.Context, actor string, body *pyjson.Object) (*pyjson.Object, error) {
	if _, err := m.requireOrgAdmin(ctx, actor); err != nil {
		return nil, err
	}
	teamID := "team_" + rnd.Hex("org", 6)
	name, err := cleanName(strArg(body.Value("name")), "team name", maxTeamName)
	if err != nil {
		return nil, err
	}
	err = m.exec(ctx, "INSERT INTO teams(id,org_id,name,description,created_at) VALUES(?,?,?,?,?)",
		teamID, m.OrgID(ctx), name, pytext.Prefix(pytext.Strip(strArg(body.Value("description"))), 400), store.UTCNow())
	if isUnique(err) {
		return nil, conflictErr("team already exists: "+strArg(body.Value("name")), "TEAM_EXISTS")
	}
	if err != nil {
		return nil, err
	}
	team, _, err := m.teamByID(ctx, teamID)
	if err != nil {
		return nil, err
	}
	if err := m.Log(ctx, m.userIDFor(ctx, actor), "org.team.create", "", pyjson.NewObject("team", team.Get("name"))); err != nil {
		return nil, err
	}
	return rowObject(team).Set("members", []any{}).Set("grants", []any{}), nil
}

func rowObject(r sqlitedb.Row) *pyjson.Object { return store.RowObject(r) }

// UpdateTeam is OrgManager.update_team.
func (m *Manager) UpdateTeam(ctx context.Context, actor, teamID string, body *pyjson.Object) (*pyjson.Object, error) {
	if _, err := m.requireOrgAdmin(ctx, actor); err != nil {
		return nil, err
	}
	team, ok, err := m.teamByID(ctx, teamID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, orgErr(404, "team not found", "TEAM_NOT_FOUND")
	}
	if d := body.Value("description"); d != nil {
		if err := m.exec(ctx, "UPDATE teams SET description=? WHERE id=?", pytext.Prefix(pytext.Strip(pyjson.Str(d)), 400), team.Str("id")); err != nil {
			return nil, err
		}
	}
	if n := body.Value("name"); n != nil && n != "" {
		name, err := cleanName(n, "team name", maxTeamName)
		if err != nil {
			return nil, err
		}
		err = m.exec(ctx, "UPDATE teams SET name=? WHERE id=?", name, team.Str("id"))
		if isUnique(err) {
			return nil, conflictErr("team already exists: "+name, "TEAM_EXISTS")
		}
		if err != nil {
			return nil, err
		}
	}
	if err := m.Log(ctx, m.userIDFor(ctx, actor), "org.team.update", "", pyjson.NewObject("team", team.Get("name"))); err != nil {
		return nil, err
	}
	fresh, ok, err := m.teamByID(ctx, team.Str("id"))
	if err != nil {
		return nil, err
	}
	if !ok {
		fresh = team
	}
	return rowObject(fresh), nil
}

// DeleteTeam is OrgManager.delete_team.
func (m *Manager) DeleteTeam(ctx context.Context, actor, teamID string) (*pyjson.Object, error) {
	if _, err := m.requireOrgAdmin(ctx, actor); err != nil {
		return nil, err
	}
	team, ok, err := m.teamByID(ctx, teamID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, orgErr(404, "team not found", "TEAM_NOT_FOUND")
	}
	err = m.DB.Write(ctx, func(q sqlitedb.Querier) error {
		for _, sql := range []string{"DELETE FROM team_members WHERE team_id=?", "DELETE FROM team_grants WHERE team_id=?",
			"DELETE FROM teams WHERE id=?"} {
			if _, err := q.ExecContext(ctx, sql, teamID); err != nil {
				return err
			}
		}
		return nil
	}, nil)
	if err != nil {
		return nil, err
	}
	if err := m.Log(ctx, m.userIDFor(ctx, actor), "org.team.delete", "", pyjson.NewObject("team", team.Get("name"))); err != nil {
		return nil, err
	}
	return pyjson.NewObject("deleted", true, "team", team.Get("name")), nil
}

func (m *Manager) userByIDOrName(ctx context.Context, id, name string) (sqlitedb.Row, bool, error) {
	if row, ok, err := m.UserByID(ctx, id); err != nil || ok {
		return row, ok, err
	}
	return m.UserByName(ctx, name)
}

// AddTeamMember is OrgManager.add_team_member.
func (m *Manager) AddTeamMember(ctx context.Context, actor, teamID string, body *pyjson.Object) (*pyjson.Object, error) {
	if _, err := m.requireOrgAdmin(ctx, actor); err != nil {
		return nil, err
	}
	team, ok, err := m.teamByID(ctx, teamID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, orgErr(404, "team not found", "TEAM_NOT_FOUND")
	}
	name := body.Value("user")
	if !pyjson.Truthy(name) {
		name = body.Value("username")
	}
	user, ok, err := m.userByIDOrName(ctx, strArg(body.Value("user_id")), strArg(name))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, orgErr(404, "user not found", "USER_NOT_FOUND")
	}
	if _, has, err := m.membershipRole(ctx, user.Str("id")); err != nil {
		return nil, err
	} else if !has {
		return nil, orgErr(400, "the user is not an organization member", "USER_NOT_MEMBER")
	}
	err = m.exec(ctx, "INSERT INTO team_members(team_id,user_id,added_at) VALUES(?,?,?)", team.Str("id"), user.Str("id"), store.UTCNow())
	added := err == nil
	if err != nil && !isUnique(err) {
		return nil, err
	}
	if err := m.Log(ctx, m.userIDFor(ctx, actor), "org.team.member.add", "", pyjson.NewObject("team", team.Get("name"), "user", user.Get("username"))); err != nil {
		return nil, err
	}
	return pyjson.NewObject("added", added, "team", team.Get("name"), "user", user.Get("username")), nil
}

func (m *Manager) execCount(ctx context.Context, query string, args ...any) (int64, error) {
	var n int64
	err := m.DB.Write(ctx, func(q sqlitedb.Querier) error {
		res, err := q.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	}, nil)
	return n, err
}

// RemoveTeamMember is OrgManager.remove_team_member.
func (m *Manager) RemoveTeamMember(ctx context.Context, actor, teamID, userID string) (*pyjson.Object, error) {
	if _, err := m.requireOrgAdmin(ctx, actor); err != nil {
		return nil, err
	}
	team, ok, err := m.teamByID(ctx, teamID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, orgErr(404, "team not found", "TEAM_NOT_FOUND")
	}
	user, ok, err := m.userByIDOrName(ctx, userID, userID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, orgErr(404, "user not found", "USER_NOT_FOUND")
	}
	n, err := m.execCount(ctx, "DELETE FROM team_members WHERE team_id=? AND user_id=?", team.Str("id"), user.Str("id"))
	if err != nil {
		return nil, err
	}
	if err := m.Log(ctx, m.userIDFor(ctx, actor), "org.team.member.remove", "", pyjson.NewObject("team", team.Get("name"), "user", user.Get("username"))); err != nil {
		return nil, err
	}
	return pyjson.NewObject("removed", n > 0, "team", team.Get("name"), "user", user.Get("username")), nil
}

// ------------------------------------------------------------- grants

// RequireProjectPermission is OrgService.require_project_permission.
func (m *Manager) RequireProjectPermission(ctx context.Context, username, projectID, level string) (string, error) {
	perm, err := m.ProjectPermission(ctx, username, projectID)
	if err != nil {
		return "", err
	}
	if perm == "" {
		return "", orgErr(404, "project not found or your account is not authorized for it", "PROJECT_NOT_ACCESSIBLE")
	}
	if PermissionRank[perm] < PermissionRank[level] {
		return "", orgErr(403, "this action needs "+level+" access to the project (your access is "+perm+")", "PROJECT_FORBIDDEN")
	}
	return perm, nil
}

func (m *Manager) grantTarget(ctx context.Context, body *pyjson.Object) (sqlitedb.Row, bool, error) {
	target := body.Value("user")
	for _, k := range []string{"username", "user_id"} {
		if !pyjson.Truthy(target) {
			target = body.Value(k)
		}
	}
	text := strArg(target)
	if strings.HasPrefix(text, "usr_") {
		return m.UserByID(ctx, text)
	}
	return m.UserByName(ctx, text)
}

func (m *Manager) grantTeam(ctx context.Context, body *pyjson.Object) (sqlitedb.Row, bool, error) {
	ref := body.Value("team")
	if !pyjson.Truthy(ref) {
		ref = body.Value("team_id")
	}
	if row, ok, err := m.teamByID(ctx, strArg(ref)); err != nil || ok {
		return row, ok, err
	}
	return m.teamByName(ctx, strArg(body.Value("team")))
}

// GrantProject is OrgManager.grant_project.
func (m *Manager) GrantProject(ctx context.Context, actor, projectID string, body *pyjson.Object) (*pyjson.Object, error) {
	if _, err := m.RequireProjectPermission(ctx, actor, projectID, "admin"); err != nil {
		return nil, err
	}
	permArg := body.Value("permission")
	if !pyjson.Truthy(permArg) {
		permArg = "read"
	}
	permission, err := ValidatePermission(permArg)
	if err != nil {
		return nil, err
	}
	actorID := m.userIDFor(ctx, actor)
	if pyjson.Truthy(body.Value("team")) || pyjson.Truthy(body.Value("team_id")) {
		team, ok, err := m.grantTeam(ctx, body)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, orgErr(404, "team not found", "TEAM_NOT_FOUND")
		}
		if _, has, err := m.membershipRole(ctx, actorID); err != nil {
			return nil, err
		} else if !has {
			return nil, orgErr(403, "not an organization member", "ORG_FORBIDDEN")
		}
		if err := m.exec(ctx, "INSERT INTO team_grants(project_id,team_id,permission,granted_by,created_at)"+
			" VALUES(?,?,?,?,?) ON CONFLICT(project_id,team_id) DO UPDATE SET "+
			"permission=excluded.permission, granted_by=excluded.granted_by",
			projectID, team.Str("id"), permission, actorID, store.UTCNow()); err != nil {
			return nil, err
		}
		if err := m.Log(ctx, actorID, "project.grant.team", projectID, pyjson.NewObject("team", team.Get("name"), "permission", permission)); err != nil {
			return nil, err
		}
		return pyjson.NewObject("project_id", projectID, "team_id", team.Get("id"), "permission", permission, "team", team.Get("name")), nil
	}
	user, ok, err := m.grantTarget(ctx, body)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, orgErr(404, "user not found", "USER_NOT_FOUND")
	}
	if _, has, err := m.membershipRole(ctx, user.Str("id")); err != nil {
		return nil, err
	} else if !has {
		return nil, orgErr(400, "the user is not an organization member", "USER_NOT_MEMBER")
	}
	if err := m.SetProjectGrant(ctx, projectID, user.Str("id"), permission, actorID); err != nil {
		return nil, err
	}
	if err := m.Log(ctx, actorID, "project.grant.user", projectID, pyjson.NewObject("user", user.Get("username"), "permission", permission)); err != nil {
		return nil, err
	}
	return pyjson.NewObject("project_id", projectID, "user_id", user.Get("id"), "permission", permission, "user", user.Get("username")), nil
}

// RevokeProject is OrgManager.revoke_project.
func (m *Manager) RevokeProject(ctx context.Context, actor, projectID string, body *pyjson.Object) (*pyjson.Object, error) {
	if _, err := m.RequireProjectPermission(ctx, actor, projectID, "admin"); err != nil {
		return nil, err
	}
	actorID := m.userIDFor(ctx, actor)
	if pyjson.Truthy(body.Value("team")) || pyjson.Truthy(body.Value("team_id")) {
		team, ok, err := m.grantTeam(ctx, body)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, orgErr(404, "team not found", "TEAM_NOT_FOUND")
		}
		n, err := m.execCount(ctx, "DELETE FROM team_grants WHERE project_id=? AND team_id=?", projectID, team.Str("id"))
		if err != nil {
			return nil, err
		}
		if err := m.Log(ctx, actorID, "project.revoke.team", projectID, pyjson.NewObject("team", team.Get("name"))); err != nil {
			return nil, err
		}
		return pyjson.NewObject("revoked", n > 0, "team", team.Get("name")), nil
	}
	user, ok, err := m.grantTarget(ctx, body)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, orgErr(404, "user not found", "USER_NOT_FOUND")
	}
	n, err := m.execCount(ctx, "DELETE FROM project_grants WHERE project_id=? AND user_id=?", projectID, user.Str("id"))
	if err != nil {
		return nil, err
	}
	if err := m.Log(ctx, actorID, "project.revoke.user", projectID, pyjson.NewObject("user", user.Get("username"))); err != nil {
		return nil, err
	}
	return pyjson.NewObject("revoked", n > 0, "user", user.Get("username")), nil
}

// ------------------------------------------------------------- profile

func (m *Manager) profileUser(ctx context.Context, username string) (sqlitedb.Row, error) {
	if username == "" {
		return sqlitedb.Row{}, orgErr(404, "user not found", "USER_NOT_FOUND")
	}
	user, ok, err := m.UserByName(ctx, username)
	if err != nil {
		return user, err
	}
	if !ok || pyjson.Truthy(user.Get("disabled")) {
		return user, orgErr(404, "user not found", "USER_NOT_FOUND")
	}
	return user, nil
}

// plainLine is OrgManager._plain_line.
func plainLine(value any, field string, limit int) (string, error) {
	text := ""
	if value != nil {
		text = pyjson.Str(value)
	}
	text = pytext.Strip(controlRe.ReplaceAllString(text, " "))
	text = spacesRe.ReplaceAllString(text, " ")
	if pytext.Len(text) > limit {
		return "", orgErr(400, fmt.Sprintf("%s is longer than %d characters", field, limit), "PROFILE_INVALID")
	}
	return text, nil
}

// AvatarDir is OrgManager.avatar_dir.
func (m *Manager) AvatarDir() string { return filepath.Join(filepath.Dir(m.Path), ".org", "avatars") }

func (m *Manager) removeAvatarFiles(userID string) {
	for _, ext := range []string{"png", "jpg", "webp"} {
		os.Remove(filepath.Join(m.AvatarDir(), userID+"."+ext)) //nolint:errcheck
	}
}

// UpdateProfile is OrgManager.update_profile.
func (m *Manager) UpdateProfile(ctx context.Context, username string, body *pyjson.Object) (*pyjson.Object, error) {
	user, err := m.profileUser(ctx, username)
	if err != nil {
		return nil, err
	}
	var unknown []string
	for _, k := range body.Keys() {
		if !contains(profileEditable, k) {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, orgErr(400, "these fields cannot be changed here: "+strings.Join(unknown, ", "), "PROFILE_FIELD_FORBIDDEN")
	}
	var keys []string
	var values []any
	set := func(k string, v any) {
		for i, x := range keys {
			if x == k {
				values[i] = v
				return
			}
		}
		keys, values = append(keys, k), append(values, v)
	}
	if body.Has("display_name") {
		name, err := plainLine(body.Value("display_name"), "display name", maxDisplayName)
		if err != nil {
			return nil, err
		}
		if name == "" {
			return nil, orgErr(400, "display name is required", "PROFILE_INVALID")
		}
		set("display_name", name)
	}
	if body.Has("bio") {
		bio, err := plainLine(body.Value("bio"), "bio", maxBio)
		if err != nil {
			return nil, err
		}
		set("bio", bio)
	}
	if body.Has("email") {
		email, err := plainLine(body.Value("email"), "email", maxEmail)
		if err != nil {
			return nil, err
		}
		if email != "" && !emailRe.MatchString(email) {
			return nil, orgErr(400, "email address is not valid", "PROFILE_INVALID")
		}
		set("email", email)
	}
	removeImage := false
	if body.Has("avatar") {
		avatar, ok := body.Value("avatar").(*pyjson.Object)
		if !ok || avatar.Value("kind") != "initials" {
			return nil, orgErr(400, "choose an initials colour or upload an avatar image", "PROFILE_INVALID")
		}
		color := pytext.Lower(strArg(avatar.Value("color")))
		if !contains(avatarColors, color) {
			return nil, orgErr(400, "avatar colour must be one of the palette colours", "PROFILE_INVALID")
		}
		set("avatar_kind", "initials")
		set("avatar_color", color)
		set("avatar_path", "")
		removeImage = user.Str("avatar_path") != ""
	}
	if body.Has("prefs") {
		prefs, ok := body.Value("prefs").(*pyjson.Object)
		if !ok {
			return nil, orgErr(400, "prefs must be an object", "PROFILE_INVALID")
		}
		merged := Prefs(user)
		if prefs.Has("lang") {
			lang, isStr := prefs.Value("lang").(string)
			if !isStr || !profileLangs[lang] {
				return nil, orgErr(400, "language must be en or zh", "PROFILE_INVALID")
			}
			merged.Set("lang", lang)
		}
		if prefs.Has("theme") {
			theme, isStr := prefs.Value("theme").(string)
			if !isStr || !profileThemes[theme] {
				return nil, orgErr(400, "theme must be system, light or dark", "PROFILE_INVALID")
			}
			merged.Set("theme", theme)
		}
		if prefs.Has("display") {
			display, ok := prefs.Value("display").(*pyjson.Object)
			valid := ok
			if ok {
				for _, k := range display.Keys() {
					if k != "font_scale" && k != "high_contrast" {
						valid = false
					}
				}
			}
			if !valid {
				return nil, orgErr(400, "invalid display preferences", "PROFILE_INVALID")
			}
			current, _ := merged.Value("display").(*pyjson.Object)
			if current == nil {
				current = pyjson.NewObject("font_scale", int64(1), "high_contrast", false)
			}
			scale, has := display.Get("font_scale")
			if !has {
				scale = current.Value("font_scale")
			}
			high, has := display.Get("high_contrast")
			if !has {
				high = current.Value("high_contrast")
			}
			var f float64
			okScale := false
			switch x := scale.(type) {
			case int64:
				f, okScale = float64(x), true
			case float64:
				f, okScale = x, true
			}
			if _, isBool := high.(bool); !okScale || f < 1 || f > 1.5 || !isBool {
				return nil, orgErr(400, "invalid display preferences", "PROFILE_INVALID")
			}
			merged.Set("display", pyjson.NewObject("font_scale", scale, "high_contrast", high))
		}
		if prefs.Has("appearance") {
			appearance, err := settings.UpdatePersonalAppearance(merged.Value("appearance"), prefs.Value("appearance"))
			if err != nil {
				var se *settings.Error
				if errors.As(err, &se) {
					return nil, orgErr(400, se.Message, "PROFILE_INVALID")
				}
				return nil, err
			}
			if appearance.Len() > 0 {
				merged.Set("appearance", appearance)
			} else {
				merged.Delete("appearance")
			}
		}
		if prefs.Has("workbench") {
			// Replaces the saved layout presets; null clears them.
			if prefs.Value("workbench") == nil {
				merged.Delete("workbench")
			} else {
				workbench, err := settings.WorkbenchPrefs(prefs.Value("workbench"), true)
				if err != nil {
					return nil, orgErr(400, err.Error(), "PROFILE_INVALID")
				}
				merged.Set("workbench", workbench)
			}
		}
		var extra []string
		for _, k := range prefs.Keys() {
			if k != "lang" && k != "theme" && k != "display" && k != "appearance" && k != "workbench" {
				extra = append(extra, k)
			}
		}
		if len(extra) > 0 {
			sort.Strings(extra)
			return nil, orgErr(400, "unknown preference: "+strings.Join(extra, ", "), "PROFILE_INVALID")
		}
		set("prefs_json", string(pyjson.DumpsASCII(sortKeys(merged))))
	}
	if len(keys) > 0 {
		if err := m.updateUserFields(ctx, user.Str("id"), keys, values); err != nil {
			return nil, err
		}
		if removeImage {
			m.removeAvatarFiles(user.Str("id"))
		}
		var fields []string
		for _, k := range body.Keys() {
			if contains(profileEditable, k) {
				fields = append(fields, k)
			}
		}
		sort.Strings(fields)
		if err := m.Log(ctx, user.Str("id"), "org.profile.update", "", pyjson.NewObject("fields", stringsAny(fields))); err != nil {
			return nil, err
		}
	}
	return m.Profile(ctx, user.Str("username"))
}

// sortKeys re-creates objects with sorted keys (json.dumps(sort_keys=True)).
func sortKeys(v any) any {
	switch x := v.(type) {
	case *pyjson.Object:
		keys := append([]string(nil), x.Keys()...)
		sort.Strings(keys)
		out := pyjson.NewObject()
		for _, k := range keys {
			out.Set(k, sortKeys(x.Value(k)))
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = sortKeys(item)
		}
		return out
	}
	return v
}

// asciiJSON is json.dumps(value) with the default ensure_ascii=True.
func asciiJSON(v any) []byte {
	var out strings.Builder
	for _, r := range string(pyjson.Dumps(v)) {
		switch {
		case r < 0x80:
			out.WriteRune(r)
		case r < 0x10000:
			fmt.Fprintf(&out, `\u%04x`, r)
		default:
			r -= 0x10000
			fmt.Fprintf(&out, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
		}
	}
	return []byte(out.String())
}

// SniffAvatar is OrgManager.sniff_avatar.
func SniffAvatar(data []byte) string {
	switch {
	case len(data) >= 16 && string(data[:8]) == "\x89PNG\r\n\x1a\n" && string(data[12:16]) == "IHDR":
		return "image/png"
	case len(data) >= 3 && string(data[:3]) == "\xff\xd8\xff":
		return "image/jpeg"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
}

// SetAvatarImage is OrgManager.set_avatar_image.
func (m *Manager) SetAvatarImage(ctx context.Context, username string, data []byte, contentType string) (*pyjson.Object, error) {
	user, err := m.profileUser(ctx, username)
	if err != nil {
		return nil, err
	}
	declared := pytext.Lower(pytext.Strip(strings.SplitN(contentType, ";", 2)[0]))
	if _, ok := avatarTypes[declared]; !ok {
		return nil, orgErr(415, "avatar must be a PNG, JPEG or WebP image", "AVATAR_TYPE")
	}
	if len(data) == 0 {
		return nil, orgErr(400, "avatar image is empty", "AVATAR_INVALID")
	}
	if len(data) > maxAvatarBytes {
		return nil, orgErr(413, fmt.Sprintf("avatar image is larger than %d KB", maxAvatarBytes/1024), "AVATAR_TOO_LARGE")
	}
	sniffed := SniffAvatar(data)
	if sniffed != declared {
		return nil, orgErr(415, "avatar content does not match its image type", "AVATAR_TYPE")
	}
	if sniffed == "image/png" {
		be := func(b []byte) uint64 {
			var n uint64
			for _, c := range b {
				n = n<<8 | uint64(c)
			}
			return n
		}
		var width, height uint64
		if len(data) >= 20 {
			width = be(data[16:20])
		} else if len(data) > 16 {
			width = be(data[16:])
		}
		if len(data) >= 24 {
			height = be(data[20:24])
		} else if len(data) > 20 {
			height = be(data[20:])
		}
		if !(width > 0 && width <= maxAvatarPx && height > 0 && height <= maxAvatarPx) {
			return nil, orgErr(400, "avatar image dimensions are too large", "AVATAR_INVALID")
		}
	}
	userID := user.Str("id")
	if !userIDRe.MatchString(userID) {
		return nil, orgErr(500, "unexpected user id", "AVATAR_INVALID")
	}
	name := userID + "." + avatarTypes[sniffed]
	folder := m.AvatarDir()
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return nil, err
	}
	tmp := filepath.Join(folder, "."+name+"."+rnd.Hex("org", 4)+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return nil, err
	}
	m.removeAvatarFiles(userID)
	if err := os.Rename(tmp, filepath.Join(folder, name)); err != nil {
		return nil, err
	}
	if err := m.updateUserFields(ctx, userID, []string{"avatar_kind", "avatar_path"}, []any{"image", name}); err != nil {
		return nil, err
	}
	if err := m.Log(ctx, userID, "org.profile.avatar", "", pyjson.NewObject("type", sniffed, "bytes", int64(len(data)))); err != nil {
		return nil, err
	}
	return m.Profile(ctx, user.Str("username"))
}

// RemoveAvatar is OrgManager.remove_avatar.
func (m *Manager) RemoveAvatar(ctx context.Context, username string) (*pyjson.Object, error) {
	user, err := m.profileUser(ctx, username)
	if err != nil {
		return nil, err
	}
	m.removeAvatarFiles(user.Str("id"))
	if err := m.updateUserFields(ctx, user.Str("id"), []string{"avatar_kind", "avatar_path"}, []any{"initials", ""}); err != nil {
		return nil, err
	}
	if err := m.Log(ctx, user.Str("id"), "org.profile.avatar_remove", "", nil); err != nil {
		return nil, err
	}
	return m.Profile(ctx, user.Str("username"))
}

// SetPasswordHash is OrgManager.set_password_hash.
func (m *Manager) SetPasswordHash(ctx context.Context, username, hash string) error {
	user, err := m.profileUser(ctx, username)
	if err != nil {
		return err
	}
	if err := m.updateUserFields(ctx, user.Str("id"), []string{"password_hash"}, []any{hash}); err != nil {
		return err
	}
	return m.Log(ctx, user.Str("id"), "org.password.change", "", nil)
}

// -------------------------------------------------------- agent tokens

const (
	tokenPrefix     = "arka_"
	maxActiveTokens = 10
	maxTokenLabel   = 80
)

// TokenClock is the agent-token clock (AgentTokenStore._now); the replay pins it.
var TokenClock = func() float64 {
	if t, ok := store.ReplayClock(); ok {
		return float64(t.UnixNano()) / 1e9
	}
	return float64(time.Now().UnixNano()) / 1e9
}

func isoAt(epoch float64) string {
	return time.Unix(0, int64(epoch*1e9)).UTC().Format("2006-01-02T15:04:05") + "+00:00"
}

func (m *Manager) tokenPublic(r sqlitedb.Row) *pyjson.Object {
	expires, hasExpiry := parseISO(r.Str("expires_at"))
	label := r.Str("label")
	scope := r.Str("scope")
	if scope == "" {
		scope = "write"
	}
	return pyjson.NewObject("id", r.Get("id"), "label", label, "prefix", r.Str("prefix"), "scope", scope,
		"created_at", r.Get("created_at"), "expires_at", r.Get("expires_at"), "last_used_at", r.Get("last_used_at"),
		"expired", hasExpiry && r.Str("expires_at") != "" && expires <= TokenClock())
}

func (m *Manager) tokenList(ctx context.Context, userID string) ([]any, int, error) {
	rows, err := m.rows(ctx, "SELECT * FROM agent_tokens WHERE user_id=? AND revoked_at IS NULL ORDER BY created_at DESC, id", userID)
	out := []any{}
	active := 0
	for _, r := range rows {
		p := m.tokenPublic(r)
		if p.Value("expired") != true {
			active++
		}
		out = append(out, p)
	}
	return out, active, err
}

func (m *Manager) userOrError(ctx context.Context, username string) (sqlitedb.Row, error) {
	user, ok, err := m.UserByName(ctx, username)
	if err != nil {
		return user, err
	}
	if !ok || pyjson.Truthy(user.Get("disabled")) {
		return user, orgErr(403, "the account is not available", "ACCOUNT_UNAVAILABLE")
	}
	return user, nil
}

// ListAgentTokens is OrgManager.list_agent_tokens.
func (m *Manager) ListAgentTokens(ctx context.Context, username string) (*pyjson.Object, error) {
	user, err := m.userOrError(ctx, username)
	if err != nil {
		return nil, err
	}
	tokens, _, err := m.tokenList(ctx, user.Str("id"))
	if err != nil {
		return nil, err
	}
	return pyjson.NewObject("user", user.Get("username"), "tokens", tokens, "limit", int64(maxActiveTokens),
		"scopes", []any{"read", "write"}, "expiry_days", []any{int64(0), int64(30), int64(90)}), nil
}

// CreateAgentToken is OrgManager.create_agent_token.
func (m *Manager) CreateAgentToken(ctx context.Context, username string, body *pyjson.Object) (*pyjson.Object, error) {
	user, err := m.userOrError(ctx, username)
	if err != nil {
		return nil, err
	}
	scopeArg := body.Value("scope")
	if !pyjson.Truthy(scopeArg) {
		scopeArg = "write"
	}
	scope := pytext.Lower(pytext.Strip(strArg(scopeArg)))
	if scope == "" {
		scope = "write"
	}
	if scope != "read" && scope != "write" {
		return nil, orgErr(400, "scope must be read or write", "AGENT_TOKEN_SCOPE_INVALID")
	}
	days := int64(-1)
	switch x := body.Value("expires_days").(type) {
	case nil:
		days = 0
	case bool:
		days = map[bool]int64{true: 1, false: 0}[x]
	case int64:
		days = x
	case float64:
		days = int64(x)
	case string:
		if x == "" {
			days = 0
		} else if n, err := pytext.Int(x); err == nil {
			days = n
		}
	}
	if days != 0 && days != 30 && days != 90 {
		return nil, orgErr(400, "expires_days must be 0 (never), 30 or 90", "AGENT_TOKEN_EXPIRY_INVALID")
	}
	label := pytext.Prefix(strings.Join(strings.FieldsFunc(strArg(body.Value("label")), pytext.IsSpace), " "), maxTokenLabel)
	if label == "" {
		label = "Agent token"
	}
	_, active, err := m.tokenList(ctx, user.Str("id"))
	if err != nil {
		return nil, err
	}
	if active >= maxActiveTokens {
		return nil, orgErr(409, fmt.Sprintf("You already have %d active agent tokens. Revoke one before creating another.", maxActiveTokens), "AGENT_TOKEN_LIMIT")
	}
	secret := tokenPrefix + rnd.URLSafe("agent_tokens", 30)
	now := TokenClock()
	id := "tok_" + rnd.Hex("agent_tokens", 8)
	var expires any
	if days != 0 {
		expires = isoAt(now + float64(days*86400))
	}
	created := isoAt(now)
	prefix := secret[:len(tokenPrefix)+4]
	if err := m.exec(ctx, "INSERT INTO agent_tokens(id,user_id,label,token_hash,prefix,scope,created_at,expires_at,last_used_at,revoked_at) "+
		"VALUES(?,?,?,?,?,?,?,?,NULL,NULL)", id, user.Str("id"), label, tokenHashOf(secret), prefix, scope, created, expires); err != nil {
		return nil, err
	}
	item := pyjson.NewObject("id", id, "label", label, "prefix", prefix, "scope", scope, "created_at", created,
		"expires_at", expires, "last_used_at", nil, "expired", false)
	if expires != nil {
		item.Set("expired", now+float64(days*86400) <= TokenClock())
	}
	if err := m.Log(ctx, user.Str("id"), "personal.agent_token.create", "", pyjson.NewObject("token", id, "scope", scope)); err != nil {
		return nil, err
	}
	list, err := m.ListAgentTokens(ctx, username)
	if err != nil {
		return nil, err
	}
	out := pyjson.NewObject("token", secret, "item", item)
	for _, k := range list.Keys() {
		out.Set(k, list.Value(k))
	}
	return out, nil
}

// RevokeAgentToken is OrgManager.revoke_agent_token.
func (m *Manager) RevokeAgentToken(ctx context.Context, username, tokenID string) (*pyjson.Object, error) {
	user, err := m.userOrError(ctx, username)
	if err != nil {
		return nil, err
	}
	n, err := m.execCount(ctx, "UPDATE agent_tokens SET revoked_at=? WHERE id=? AND user_id=? AND revoked_at IS NULL",
		isoAt(TokenClock()), tokenID, user.Str("id"))
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, orgErr(404, "agent token not found", "AGENT_TOKEN_NOT_FOUND")
	}
	if err := m.Log(ctx, user.Str("id"), "personal.agent_token.revoke", "", pyjson.NewObject("token", tokenID)); err != nil {
		return nil, err
	}
	list, err := m.ListAgentTokens(ctx, username)
	if err != nil {
		return nil, err
	}
	out := pyjson.NewObject("revoked", true)
	for _, k := range list.Keys() {
		out.Set(k, list.Value(k))
	}
	return out, nil
}

func (m *Manager) revokeTokensOf(ctx context.Context, userID string) (int64, error) {
	return m.execCount(ctx, "UPDATE agent_tokens SET revoked_at=? WHERE user_id=? AND revoked_at IS NULL", isoAt(TokenClock()), userID)
}

// RevokeUserTokens is OrgManager.revoke_user_tokens.
func (m *Manager) RevokeUserTokens(ctx context.Context, username string) (int64, error) {
	user, ok, err := m.UserByName(ctx, username)
	if err != nil || !ok {
		return 0, err
	}
	n, err := m.revokeTokensOf(ctx, user.Str("id"))
	if err != nil {
		return 0, err
	}
	if n > 0 {
		if err := m.Log(ctx, user.Str("id"), "personal.agent_token.revoke_all", "", pyjson.NewObject("count", n)); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// tokenHashOf is agent_tokens.token_hash.
func tokenHashOf(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
