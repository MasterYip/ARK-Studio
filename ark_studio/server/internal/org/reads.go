package org

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"arkstudio/go/internal/platform/clock"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/compat/pyurl"
	"arkstudio/go/internal/settings"
	"arkstudio/go/internal/sqlitedb"
	"arkstudio/go/internal/store"
)

// Read-only organization views (org.py) plus personal agent tokens
// (agent_tokens.py authenticate).

func (m *Manager) rows(ctx context.Context, query string, args ...any) ([]sqlitedb.Row, error) {
	var out []sqlitedb.Row
	err := m.DB.Read(ctx, func(q sqlitedb.Querier) error {
		var err error
		out, err = sqlitedb.Query(ctx, q, query, args...)
		return err
	})
	return out, err
}

func objects(rows []sqlitedb.Row) []any {
	out := make([]any, len(rows))
	for i, r := range rows {
		out[i] = store.RowObject(r)
	}
	return out
}

// AvatarColors is AVATAR_COLORS (set by the server from the static snapshot).
var AvatarColors []string

// DefaultAvatarColor is OrgManager.default_avatar_color.
func DefaultAvatarColor(username string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(username)))
	return AvatarColors[int(sum[0])%len(AvatarColors)]
}

func strField(r sqlitedb.Row, key string) string {
	v := r.Get(key)
	if !pyjson.Truthy(v) {
		return ""
	}
	return pyjson.Str(v)
}

// AvatarPublic is OrgManager.avatar_public.
func AvatarPublic(r sqlitedb.Row) *pyjson.Object {
	username := strField(r, "username")
	color := strField(r, "avatar_color")
	known := false
	for _, c := range AvatarColors {
		known = known || c == color
	}
	if !known {
		color = DefaultAvatarColor(username)
	}
	if strField(r, "avatar_kind") == "image" && pyjson.Truthy(r.Get("avatar_path")) {
		sum := sha256.Sum256([]byte(pyjson.Str(r.Get("avatar_path")) + "|" + pyjson.Str(r.Get("updated_at"))))
		version := hex.EncodeToString(sum[:])[:10]
		return pyjson.NewObject("kind", "image", "color", color,
			"url", "api/users/"+quoteAll(username)+"/avatar?v="+version)
	}
	return pyjson.NewObject("kind", "initials", "color", color)
}

// quoteAll is urllib.parse.quote(s, safe="").
func quoteAll(s string) string { return pyurl.Quote(s, "") }

// UserPublic is OrgManager.user_public(row, role).
func UserPublic(r sqlitedb.Row, role any) *pyjson.Object {
	if role == nil {
		role = r.Get("role")
	}
	display := strField(r, "display_name")
	if display == "" {
		display = strField(r, "username")
	}
	return pyjson.NewObject("id", r.Get("id"), "username", r.Get("username"), "display_name", display,
		"role", role, "disabled", pyjson.Truthy(r.Get("disabled")), "created_at", r.Get("created_at"),
		"updated_at", r.Get("updated_at"), "avatar", AvatarPublic(r), "bio", strField(r, "bio"))
}

// UserCard is OrgManager.user_card.
func UserCard(r sqlitedb.Row) *pyjson.Object {
	username := strField(r, "username")
	display := strField(r, "display_name")
	if display == "" {
		display = username
	}
	return pyjson.NewObject("username", username, "display_name", display, "avatar", AvatarPublic(r),
		"bio", strField(r, "bio"), "email", strField(r, "email"), "disabled", pyjson.Truthy(r.Get("disabled")))
}

// ListUsers is OrgStore.list_users.
func (m *Manager) ListUsers(ctx context.Context) ([]sqlitedb.Row, error) {
	return m.rows(ctx, "SELECT * FROM users ORDER BY username COLLATE NOCASE")
}

// UserDirectory is OrgManager.user_directory.
func (m *Manager) UserDirectory(ctx context.Context) ([]any, error) {
	rows, err := m.ListUsers(ctx)
	out := []any{}
	for _, r := range rows {
		out = append(out, UserCard(r))
	}
	return out, err
}

var profileLangs = map[string]bool{"en": true, "zh": true}
var profileThemes = map[string]bool{"system": true, "light": true, "dark": true}

// Prefs is OrgManager._prefs.
func Prefs(r sqlitedb.Row) *pyjson.Object {
	data, ok := pyjson.LoadsObject(nonEmpty(strField(r, "prefs_json"), "{}"))
	if !ok {
		data = pyjson.NewObject()
	}
	out := pyjson.NewObject()
	if s, ok := data.Value("lang").(string); ok && profileLangs[s] {
		out.Set("lang", s)
	}
	if s, ok := data.Value("theme").(string); ok && profileThemes[s] {
		out.Set("theme", s)
	}
	if display, ok := data.Value("display").(*pyjson.Object); ok {
		scale := any(int64(1))
		if v, has := display.Get("font_scale"); has {
			scale = v
		}
		var f float64
		valid := false
		switch x := scale.(type) {
		case int64:
			f, valid = float64(x), true
		case float64:
			f, valid = x, true
		}
		if valid && f >= 1 && f <= 1.5 {
			out.Set("display", pyjson.NewObject("font_scale", scale, "high_contrast", display.Value("high_contrast") == true))
		}
	}
	if appearance := settings.PersonalAppearance(data.Value("appearance")); appearance.Len() > 0 {
		out.Set("appearance", appearance)
	}
	// Saved workbench layout presets (workbench_prefs.py).
	if workbench, _ := settings.WorkbenchPrefs(data.Value("workbench"), false); workbench.Len() > 0 {
		out.Set("workbench", workbench)
	}
	// Go-only (T-0034): the project picker's pins and hidden projects.
	if projects, _ := settings.ProjectPrefs(data.Value("projects"), false); projects.Len() > 0 {
		out.Set("projects", projects)
	}
	// Go-only (T-0036): Mind card widths and pinned images per project.
	if layout := settings.MindLayoutPrefs(data.Value("mind_layout")); layout.Len() > 0 {
		out.Set("mind_layout", layout)
	}
	// Go-only: what Home's graph shows per project.
	if filters := settings.HomeFilterPrefs(data.Value("home_filter")); filters.Len() > 0 {
		out.Set("home_filter", filters)
	}
	return out
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// Profile is OrgManager.profile(username).
func (m *Manager) Profile(ctx context.Context, username string) (*pyjson.Object, error) {
	var user sqlitedb.Row
	ok := false
	if username != "" {
		var err error
		if user, ok, err = m.UserByName(ctx, username); err != nil {
			return nil, err
		}
	}
	if !ok || pyjson.Truthy(user.Get("disabled")) {
		return nil, &Error{404, "user not found", "USER_NOT_FOUND"}
	}
	card := UserCard(user)
	card.Delete("disabled")
	card.Set("id", user.Get("id")).Set("prefs", Prefs(user)).Set("updated_at", user.Get("updated_at"))
	return card, nil
}

var avatarNameRe = regexp.MustCompile(`\A([A-Za-z0-9_]+)\.(png|jpg|webp)\z`)

// AvatarFile is OrgManager.avatar_file.
func (m *Manager) AvatarFile(ctx context.Context, username string) (string, string, bool, error) {
	row, ok, err := m.UserByName(ctx, username)
	if err != nil || !ok || strField(row, "avatar_kind") != "image" {
		return "", "", false, err
	}
	match := avatarNameRe.FindStringSubmatch(strField(row, "avatar_path"))
	if match == nil || match[1] != row.Str("id") {
		return "", "", false, nil
	}
	path := filepath.Join(filepath.Dir(m.Path), ".org", "avatars", match[0])
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return "", "", false, nil
	}
	return path, map[string]string{"png": "image/png", "jpg": "image/jpeg", "webp": "image/webp"}[match[2]], true, nil
}

// Members is OrgStore.members.
func (m *Manager) Members(ctx context.Context) ([]sqlitedb.Row, error) {
	return m.rows(ctx, "SELECT u.*, m.role AS role FROM memberships m "+
		"JOIN users u ON u.id=m.user_id WHERE m.org_id=? "+
		"ORDER BY u.username COLLATE NOCASE", m.OrgID(ctx))
}

// ListMembers is OrgManager.list_members.
func (m *Manager) ListMembers(ctx context.Context) ([]any, error) {
	rows, err := m.Members(ctx)
	out := []any{}
	for _, r := range rows {
		out = append(out, UserPublic(r, r.Get("role")))
	}
	return out, err
}

// ListTeams is OrgManager.list_teams.
func (m *Manager) ListTeams(ctx context.Context) ([]any, error) {
	teams, err := m.rows(ctx, "SELECT * FROM teams WHERE org_id=? ORDER BY name COLLATE NOCASE", m.OrgID(ctx))
	if err != nil {
		return nil, err
	}
	out := []any{}
	for _, t := range teams {
		members, err := m.teamMembers(ctx, t.Str("id"))
		if err != nil {
			return nil, err
		}
		grants, err := m.rows(ctx, "SELECT * FROM team_grants WHERE team_id=?", t.Str("id"))
		if err != nil {
			return nil, err
		}
		out = append(out, store.RowObject(t).Set("members", members).Set("grants", objects(grants)))
	}
	return out, nil
}

func (m *Manager) teamMembers(ctx context.Context, teamID string) ([]any, error) {
	rows, err := m.rows(ctx, "SELECT u.id, u.username, u.display_name, tm.added_at FROM team_members tm "+
		"JOIN users u ON u.id=tm.user_id WHERE tm.team_id=? ORDER BY u.username COLLATE NOCASE", teamID)
	return objects(rows), err
}

// ListAudit is OrgStore.list_audit.
func (m *Manager) ListAudit(ctx context.Context, limit int64) ([]any, error) {
	rows, err := m.rows(ctx, "SELECT * FROM audit ORDER BY id DESC LIMIT ?", max(1, min(500, limit)))
	out := []any{}
	for _, r := range rows {
		item := pyjson.NewObject()
		for i, c := range r.Cols {
			if c != "detail_json" {
				item.Set(c, r.Vals[i])
			}
		}
		detail, ok := pyjson.LoadsObject(nonEmpty(strField(r, "detail_json"), "{}"))
		if !ok {
			detail = pyjson.NewObject()
		}
		out = append(out, item.Set("detail", detail))
	}
	return out, err
}

// LinuxAccountsMap is OrgStore.linux_accounts_map.
func (m *Manager) LinuxAccountsMap(ctx context.Context) (*pyjson.Object, error) {
	rows, err := m.rows(ctx, "SELECT u.username AS username, l.account AS account "+
		"FROM linux_accounts l JOIN users u ON u.id=l.user_id ORDER BY u.username, l.account")
	out := pyjson.NewObject()
	for _, r := range rows {
		key := pyjson.Str(r.Get("username"))
		list, _ := out.Value(key).([]any)
		out.Set(key, append(list, pyjson.Str(r.Get("account"))))
	}
	return out, err
}

// LinuxAccounts is OrgStore.linux_accounts(user_id) accounts.
func (m *Manager) LinuxAccounts(ctx context.Context, userID string) ([]any, error) {
	rows, err := m.rows(ctx, "SELECT * FROM linux_accounts WHERE user_id=? ORDER BY account", userID)
	out := []any{}
	for _, r := range rows {
		out = append(out, r.Get("account"))
	}
	return out, err
}

// UserByID is OrgStore.user_by_id.
func (m *Manager) UserByID(ctx context.Context, id string) (sqlitedb.Row, bool, error) {
	return m.row(ctx, "SELECT * FROM users WHERE id=?", id)
}

// RequireOrgAdmin is OrgManager.require_org_admin.
func (m *Manager) RequireOrgAdmin(ctx context.Context, username string) error {
	role, ok, err := m.RoleFor(ctx, username)
	if err != nil {
		return err
	}
	if !ok {
		return &Error{403, "Your account is not a member of this organization.", "ORG_FORBIDDEN"}
	}
	if RoleRank[role] < RoleRank["admin"] {
		return &Error{403, "This action needs an organization admin or owner.", "ORG_FORBIDDEN"}
	}
	return nil
}

func (m *Manager) projectGrants(ctx context.Context, projectID string) ([]any, error) {
	rows, err := m.rows(ctx, "SELECT g.user_id, u.username, u.display_name, g.permission, g.created_at "+
		"FROM project_grants g JOIN users u ON u.id=g.user_id "+
		"WHERE g.project_id=? ORDER BY u.username COLLATE NOCASE", projectID)
	return objects(rows), err
}

func (m *Manager) teamGrants(ctx context.Context, projectID string) ([]any, error) {
	rows, err := m.rows(ctx, "SELECT g.team_id, t.name, g.permission, g.created_at FROM team_grants g "+
		"JOIN teams t ON t.id=g.team_id WHERE g.project_id=? ORDER BY t.name", projectID)
	return objects(rows), err
}

// ListProjects is OrgStore.list_projects.
func (m *Manager) ListProjects(ctx context.Context) ([]sqlitedb.Row, error) {
	return m.rows(ctx, "SELECT * FROM projects WHERE org_id=? ORDER BY created_at", m.OrgID(ctx))
}

// ProjectsFor is OrgManager.projects_for.
func (m *Manager) ProjectsFor(ctx context.Context, username string) ([]*pyjson.Object, error) {
	user, ok, err := m.UserByName(ctx, username)
	if err != nil || !ok || pyjson.Truthy(user.Get("disabled")) {
		return nil, err
	}
	if _, ok, err := m.Membership(ctx, user.Str("id"), m.OrgID(ctx)); err != nil || !ok {
		return nil, err
	}
	projects, err := m.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	var out []*pyjson.Object
	for _, p := range projects {
		perm, err := m.ProjectPermission(ctx, username, p.Str("id"))
		if err != nil {
			return nil, err
		}
		if perm == "" {
			continue
		}
		grants, err := m.projectGrants(ctx, p.Str("id"))
		if err != nil {
			return nil, err
		}
		teams, err := m.teamGrants(ctx, p.Str("id"))
		if err != nil {
			return nil, err
		}
		out = append(out, store.RowObject(p).Set("permission", perm).Set("grants", grants).Set("team_grants", teams))
	}
	return out, nil
}

// OrgOverview is OrgManager.org_overview.
func (m *Manager) OrgOverview(ctx context.Context, username string) (*pyjson.Object, error) {
	org, _, err := m.Org(ctx)
	if err != nil {
		return nil, err
	}
	roleText, hasRole, err := m.RoleFor(ctx, username)
	if err != nil {
		return nil, err
	}
	var role any
	if hasRole {
		role = roleText
	}
	user, _, err := m.UserByName(ctx, username)
	if err != nil {
		return nil, err
	}
	projects, err := m.ProjectsFor(ctx, username)
	if err != nil {
		return nil, err
	}
	teams, err := m.ListTeams(ctx)
	if err != nil {
		return nil, err
	}
	admin := hasRole && RoleRank[roleText] >= RoleRank["admin"]
	var userRole any = role
	payload := pyjson.NewObject(
		"org", pyjson.NewObject("id", org.Get("id"), "name", org.Get("name"), "created_at", org.Get("created_at")),
		"user", UserPublicRole(user, userRole), "role", role, "is_admin", admin, "is_owner", roleText == "owner" && hasRole,
		"projects", orEmptyObjects(projects), "teams", teams)
	if admin {
		members, err := m.ListMembers(ctx)
		if err != nil {
			return nil, err
		}
		all, err := m.ListProjects(ctx)
		if err != nil {
			return nil, err
		}
		audit, err := m.ListAudit(ctx, 40)
		if err != nil {
			return nil, err
		}
		payload.Set("members", members).Set("all_projects", objects(all)).Set("audit", audit)
	}
	return payload, nil
}

// UserPublicRole is user_public(row, role=role) where role None falls back
// to row.get("role") (absent on a users row, so None).
func UserPublicRole(r sqlitedb.Row, role any) *pyjson.Object { return UserPublic(r, role) }

func orEmptyObjects(items []*pyjson.Object) []*pyjson.Object {
	if items == nil {
		return []*pyjson.Object{}
	}
	return items
}

// ProjectDetail is OrgManager.project_detail.
func (m *Manager) ProjectDetail(ctx context.Context, username, projectID string) (*pyjson.Object, error) {
	perm, err := m.ProjectPermission(ctx, username, projectID)
	if err != nil {
		return nil, err
	}
	if perm == "" {
		return nil, &Error{404, "project not found or your account is not authorized for it", "PROJECT_NOT_ACCESSIBLE"}
	}
	row, _, err := m.ProjectRow(ctx, projectID)
	if err != nil {
		return nil, err
	}
	project := store.RowObject(row)
	grants, err := m.projectGrants(ctx, projectID)
	if err != nil {
		return nil, err
	}
	tgrants, err := m.teamGrants(ctx, projectID)
	if err != nil {
		return nil, err
	}
	teamRows, err := m.rows(ctx, "SELECT * FROM teams WHERE org_id=? ORDER BY name COLLATE NOCASE", m.OrgID(ctx))
	if err != nil {
		return nil, err
	}
	teams := []any{}
	for _, t := range teamRows {
		members, err := m.teamMembers(ctx, t.Str("id"))
		if err != nil {
			return nil, err
		}
		teams = append(teams, store.RowObject(t).Set("members", members))
	}
	members, err := m.ListMembers(ctx)
	if err != nil {
		return nil, err
	}
	return project.Set("permission", perm).Set("grants", grants).Set("team_grants", tgrants).
		Set("teams", teams).Set("members", members), nil
}

// ProjectMemberNames is OrgManager.project_member_names.
func (m *Manager) ProjectMemberNames(ctx context.Context, projectID string) ([]*pyjson.Object, error) {
	rows, err := m.rows(ctx, `SELECT u.username, u.display_name FROM users u
               JOIN memberships m ON m.user_id=u.id AND m.org_id=?
              WHERE u.disabled=0 AND (
                    EXISTS(SELECT 1 FROM project_grants g WHERE g.project_id=? AND g.user_id=u.id)
                 OR EXISTS(SELECT 1 FROM team_grants tg JOIN team_members tm ON tm.team_id=tg.team_id
                           WHERE tg.project_id=? AND tm.user_id=u.id))
              ORDER BY u.username COLLATE NOCASE`, m.OrgID(ctx), projectID, projectID)
	var out []*pyjson.Object
	for _, r := range rows {
		display := strField(r, "display_name")
		if display == "" {
			display = pyjson.Str(r.Get("username"))
		}
		out = append(out, pyjson.NewObject("username", r.Get("username"), "display_name", display))
	}
	return out, err
}

// SyncProjects is OrgManager.sync_projects (registers and touches projects,
// grants owners admin), as App.visible_projects runs it on every listing.
func (m *Manager) SyncProjects(ctx context.Context, summaries []*pyjson.Object, ownerUser string) error {
	for _, s := range summaries {
		if _, ok, err := m.ProjectByPath(ctx, strOf(s.Value("path"))); err != nil {
			return err
		} else if !ok {
			if err := m.registerProject(ctx, s, ownerUser, false); err != nil {
				return err
			}
		}
	}
	for _, s := range summaries {
		rec, ok, err := m.ProjectByPath(ctx, strOf(s.Value("path")))
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		name, updated := strOf(s.Value("name")), strOf(s.Value("updated"))
		if name == "" {
			name = rec.Str("name")
		}
		if updated == "" {
			updated = clock.UTCNow()
		}
		if err := m.exec(ctx, "UPDATE projects SET name=?, updated_at=? WHERE id=?", name, updated, rec.Str("id")); err != nil {
			return err
		}
		owners, err := m.Members(ctx)
		if err != nil {
			return err
		}
		for _, o := range owners {
			if o.Str("role") != "owner" {
				continue
			}
			_, has, err := m.row(ctx, "SELECT 1 FROM project_grants WHERE project_id=? AND user_id=?", rec.Str("id"), o.Str("id"))
			if err != nil {
				return err
			}
			if !has {
				if err := m.exec(ctx, "INSERT INTO project_grants(project_id,user_id,permission,granted_by,created_at) VALUES(?,?,?,?,?) "+
					"ON CONFLICT(project_id,user_id) DO UPDATE SET permission=excluded.permission, granted_by=excluded.granted_by",
					rec.Str("id"), o.Str("id"), "admin", o.Str("id"), clock.UTCNow()); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// ------------------------------------------------------ personal tokens

// TokenPrefix is agent_tokens.TOKEN_PREFIX.
const TokenPrefix = "arka_"

var lastUsed sync.Map // token id -> float64 seconds

func parseISO(text string) (float64, bool) {
	if text == "" {
		return 0, false
	}
	t, err := time.Parse("2006-01-02T15:04:05-07:00", text)
	if err != nil {
		if t, err = time.Parse("2006-01-02T15:04:05.999999-07:00", text); err != nil {
			return 0, false
		}
	}
	return float64(t.UnixNano()) / 1e9, true
}

// AuthenticateToken is AgentTokenStore.authenticate: the token row and the
// user row for a live token of an enabled user.
func (m *Manager) AuthenticateToken(ctx context.Context, secret string) (sqlitedb.Row, sqlitedb.Row, bool, error) {
	none := sqlitedb.Row{}
	if !strings.HasPrefix(secret, TokenPrefix) || pytext.Len(secret) > 200 {
		return none, none, false, nil
	}
	sum := sha256.Sum256([]byte(secret))
	row, ok, err := m.row(ctx, "SELECT * FROM agent_tokens WHERE token_hash=?", hex.EncodeToString(sum[:]))
	if err != nil || !ok || pyjson.Truthy(row.Get("revoked_at")) {
		return none, none, false, err
	}
	now := float64(time.Now().UnixNano()) / 1e9
	if exp, ok := parseISO(strField(row, "expires_at")); ok && exp <= now {
		return none, none, false, nil
	}
	user, ok, err := m.UserByID(ctx, row.Str("user_id"))
	if err != nil || !ok || pyjson.Truthy(user.Get("disabled")) {
		return none, none, false, err
	}
	last, _ := lastUsed.Load(row.Str("id"))
	if l, _ := last.(float64); now-l >= 60 {
		lastUsed.Store(row.Str("id"), now)
		stamp := time.Unix(int64(now), 0).UTC().Format("2006-01-02T15:04:05") + "+00:00"
		if err := m.exec(ctx, "UPDATE agent_tokens SET last_used_at=? WHERE id=?", stamp, row.Str("id")); err != nil {
			return none, none, false, err
		}
		for i, c := range row.Cols {
			if c == "last_used_at" {
				row.Vals[i] = stamp
			}
		}
	}
	return row, user, true, nil
}

// ProjectMember is OrgService.project_member: an enabled org member with a
// user or team grant on the project.
func (m *Manager) ProjectMember(ctx context.Context, username, projectID string) (bool, error) {
	user, ok, err := m.UserByName(ctx, username)
	if err != nil || !ok || pyjson.Truthy(user.Get("disabled")) {
		return false, err
	}
	if _, ok, err := m.Membership(ctx, user.Str("id"), m.OrgID(ctx)); err != nil || !ok {
		return false, err
	}
	rows, err := m.rows(ctx, "SELECT permission FROM project_grants WHERE project_id=? AND user_id=?", projectID, user.Str("id"))
	if err != nil || len(rows) > 0 {
		return len(rows) > 0, err
	}
	rows, err = m.rows(ctx, "SELECT g.permission FROM team_grants g JOIN team_members tm "+
		"ON tm.team_id=g.team_id WHERE g.project_id=? AND tm.user_id=?", projectID, user.Str("id"))
	return len(rows) > 0, err
}
