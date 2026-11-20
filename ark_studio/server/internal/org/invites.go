package org

// Project invite links (Go only).  A project admin creates a link
// <public URL>/#/join/<token>; whoever opens it joins the project with the
// link's permission, signing in first or, when the link allows it and the
// organization has not turned invite sign-up off, creating an account.
// Accounts can be created without an org admin only this way: there is no
// open registration.
//
// The token is 192 random bits.  Only its SHA-256 is stored (like personal
// agent tokens), so a link is shown once, when it is created.  Using a link
// re-checks everything: not revoked, not expired, uses left, and the person
// who made it still administers the project.
//
// The tables are Go only: tests/python/contract/statediff.py GO_ONLY_TABLES
// leaves them out of the Python/Go state comparison.

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"arkstudio/go/internal/auth"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/rnd"
	"arkstudio/go/internal/sqlitedb"
)

const inviteSchema = `CREATE TABLE IF NOT EXISTS project_invites(
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  hint TEXT NOT NULL DEFAULT '',
  permission TEXT NOT NULL,
  allow_signup INTEGER NOT NULL DEFAULT 1,
  label TEXT NOT NULL DEFAULT '',
  max_uses INTEGER NOT NULL DEFAULT 1,
  uses INTEGER NOT NULL DEFAULT 0,
  created_by TEXT NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  revoked_at TEXT,
  revoked_by TEXT,
  last_used_at TEXT
);
CREATE INDEX IF NOT EXISTS project_invites_project ON project_invites(project_id);
CREATE TABLE IF NOT EXISTS org_options(
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TEXT NOT NULL DEFAULT '',
  updated_by TEXT NOT NULL DEFAULT ''
)`

// Invite limits and defaults.
const (
	InviteTokenBytes      = 24 // 192 bits
	InviteDefaultDays     = 7
	InviteDefaultMaxUses  = 1
	InviteMaxUses         = 1000
	InviteMaxActive       = 50 // per project
	InviteLabelMax        = 80
	SignupPasswordMin     = 8
	inviteSignupOption    = "invite_signup"
	inviteDefaultGrant    = "write"
	inviteMemberRole      = "member"
	inviteTokenMaxLen     = 128
	inviteStateValid      = "valid"
	inviteStateInvalid    = "invalid"
	inviteStateExpired    = "expired"
	inviteStateRevoked    = "revoked"
	inviteStateUsedUp     = "used_up"
	inviteActionCreate    = "project.invite.create"
	inviteActionRevoke    = "project.invite.revoke"
	inviteActionUse       = "project.invite.use"
	inviteActionJoin      = "project.member.join"
	inviteActionSignup    = "org.user.signup"
	inviteActionSignupOpt = "org.invite_signup"
)

// InviteExpiryDays are the offered lifetimes.
var InviteExpiryDays = []int64{1, 7, 30, 90}

// InviteActivity is one project activity row the server writes after an
// invite action (the org package has no project store).
type InviteActivity struct {
	ProjectID, Action string
	Detail            *pyjson.Object
}

// InviteHash is the stored form of an invite token.
func InviteHash(token string) string { return tokenHashOf("invite:" + token) }

func newInviteToken() string {
	buf := make([]byte, InviteTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		buf = rnd.Bytes("invites", InviteTokenBytes)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// WellFormedInviteToken reports whether text can be an invite token at all
// (cheap check before any lookup).
func WellFormedInviteToken(text string) bool {
	if len(text) < 16 || len(text) > inviteTokenMaxLen {
		return false
	}
	for _, r := range text {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func intArg(v any, def int64) (int64, bool) {
	switch x := v.(type) {
	case nil:
		return def, true
	case bool:
		return 0, false
	case int64:
		return x, true
	case float64:
		if x != float64(int64(x)) {
			return 0, false
		}
		return int64(x), true
	case string:
		if pytext.Strip(x) == "" {
			return def, true
		}
		n, err := pytext.Int(x)
		return n, err == nil
	}
	return 0, false
}

func boolArg(v any, def bool) bool {
	switch x := v.(type) {
	case nil:
		return def
	case bool:
		return x
	case string:
		s := pytext.Lower(pytext.Strip(x))
		return s == "1" || s == "true" || s == "yes" || s == "on"
	}
	return pyjson.Truthy(v)
}

// ------------------------------------------------------------- org option

// InviteSignupEnabled reports the organization switch "new accounts may be
// created through invite links" (on unless an owner turned it off).
func (m *Manager) InviteSignupEnabled(ctx context.Context) (bool, error) {
	r, ok, err := m.row(ctx, "SELECT value FROM org_options WHERE key=?", inviteSignupOption)
	if err != nil {
		return false, err
	}
	return !ok || r.Str("value") != "0", nil
}

// SetInviteSignup turns invite sign-up on or off (organization owner only).
func (m *Manager) SetInviteSignup(ctx context.Context, actor string, enabled bool) (*pyjson.Object, error) {
	role, err := m.requireOrgAdmin(ctx, actor)
	if err != nil {
		return nil, err
	}
	if role != "owner" {
		return nil, orgErr(403, "Only an organization owner can change invite sign-up.", "ORG_OWNER_REQUIRED")
	}
	value := "0"
	if enabled {
		value = "1"
	}
	actorID := m.userIDFor(ctx, actor)
	if err := m.exec(ctx, "INSERT INTO org_options(key,value,updated_at,updated_by) VALUES(?,?,?,?) "+
		"ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at, updated_by=excluded.updated_by",
		inviteSignupOption, value, clock.UTCNow(), actorID); err != nil {
		return nil, err
	}
	if err := m.Log(ctx, actorID, inviteActionSignupOpt, "", pyjson.NewObject("enabled", enabled)); err != nil {
		return nil, err
	}
	return m.InviteSettings(ctx, actor)
}

// InviteSettings is the organization invite settings for an org member.
func (m *Manager) InviteSettings(ctx context.Context, actor string) (*pyjson.Object, error) {
	role, ok, err := m.RoleFor(ctx, actor)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, orgErr(403, "Your account is not a member of this organization.", "ORG_FORBIDDEN")
	}
	enabled, err := m.InviteSignupEnabled(ctx)
	if err != nil {
		return nil, err
	}
	return pyjson.NewObject("signup_enabled", enabled, "can_change", role == "owner",
		"expiry_days", []any{int64(1), int64(7), int64(30), int64(90)},
		"default_days", int64(InviteDefaultDays), "default_max_uses", int64(InviteDefaultMaxUses),
		"default_permission", inviteDefaultGrant, "password_min", int64(SignupPasswordMin)), nil
}

// ------------------------------------------------------------- admin side

func inviteState(r sqlitedb.Row, now time.Time) string {
	if r.Str("revoked_at") != "" {
		return inviteStateRevoked
	}
	if exp, ok := parseISO(r.Str("expires_at")); !ok || exp <= float64(now.UnixNano())/1e9 {
		return inviteStateExpired
	}
	if max := r.Int("max_uses"); max > 0 && r.Int("uses") >= max {
		return inviteStateUsedUp
	}
	return inviteStateValid
}

func (m *Manager) invitePublic(r sqlitedb.Row, now time.Time) *pyjson.Object {
	creator := pyjson.NewObject("id", r.Get("created_by"), "username", r.Get("creator_username"),
		"display_name", r.Get("creator_display"))
	if strField(r, "creator_display") == "" {
		creator.Set("display_name", r.Get("creator_username"))
	}
	return pyjson.NewObject("id", r.Get("id"), "project_id", r.Get("project_id"), "label", r.Str("label"),
		"hint", r.Str("hint"), "permission", r.Get("permission"), "allow_signup", pyjson.Truthy(r.Get("allow_signup")),
		"max_uses", r.Int("max_uses"), "uses", r.Int("uses"), "created_at", r.Get("created_at"),
		"expires_at", r.Get("expires_at"), "revoked_at", r.Get("revoked_at"), "last_used_at", r.Get("last_used_at"),
		"created_by", creator, "state", inviteState(r, now))
}

const inviteSelect = "SELECT i.*, COALESCE(u.username,'') creator_username, COALESCE(u.display_name,'') creator_display " +
	"FROM project_invites i LEFT JOIN users u ON u.id=i.created_by "

// ListInvites lists a project's invite links, newest first: the active
// ones and those that ended within the last 30 days (project admin only).
func (m *Manager) ListInvites(ctx context.Context, actor, projectID string) ([]any, error) {
	if _, err := m.RequireProjectPermission(ctx, actor, projectID, "admin"); err != nil {
		return nil, err
	}
	rows, err := m.rows(ctx, inviteSelect+"WHERE i.project_id=? ORDER BY i.created_at DESC, i.id", projectID)
	if err != nil {
		return nil, err
	}
	now := clock.Now()
	cutoff := now.Add(-30 * 24 * time.Hour)
	out := []any{}
	for _, r := range rows {
		if inviteState(r, now) != inviteStateValid {
			ended := r.Str("revoked_at")
			if ended == "" {
				ended = r.Str("expires_at")
			}
			if r.Str("revoked_at") == "" && inviteState(r, now) == inviteStateUsedUp {
				ended = r.Str("last_used_at")
			}
			if at, ok := parseISO(ended); ok && at < float64(cutoff.UnixNano())/1e9 {
				continue
			}
		}
		out = append(out, m.invitePublic(r, now))
	}
	return out, nil
}

// CreateInvite makes a new invite link for a project.  It returns the
// public row, the token (shown once) and the activity row to record.
func (m *Manager) CreateInvite(ctx context.Context, actor, projectID string, body *pyjson.Object) (*pyjson.Object, string, *InviteActivity, error) {
	actorPerm, err := m.RequireProjectPermission(ctx, actor, projectID, "admin")
	if err != nil {
		return nil, "", nil, err
	}
	permArg := body.Value("permission")
	if !pyjson.Truthy(permArg) {
		permArg = inviteDefaultGrant
	}
	permission, err := ValidatePermission(permArg)
	if err != nil {
		return nil, "", nil, err
	}
	if PermissionRank[permission] > PermissionRank[actorPerm] {
		return nil, "", nil, orgErr(403, "You cannot grant more access than you have ("+actorPerm+").", "INVITE_PERMISSION_TOO_HIGH")
	}
	days, ok := intArg(body.Value("expires_days"), InviteDefaultDays)
	valid := false
	for _, d := range InviteExpiryDays {
		valid = valid || (ok && d == days)
	}
	if !valid {
		return nil, "", nil, orgErr(400, "expires_days must be 1, 7, 30 or 90", "INVITE_EXPIRY_INVALID")
	}
	maxUses, ok := intArg(body.Value("max_uses"), InviteDefaultMaxUses)
	if !ok || maxUses < 0 || maxUses > InviteMaxUses {
		return nil, "", nil, orgErr(400, fmt.Sprintf("max_uses must be 0 (no limit) to %d", InviteMaxUses), "INVITE_MAX_USES_INVALID")
	}
	label, err := plainLine(body.Value("label"), "label", InviteLabelMax)
	if err != nil {
		return nil, "", nil, orgErr(400, err.Error(), "INVITE_LABEL_INVALID")
	}
	allowSignup := boolArg(body.Value("allow_signup"), true)
	now := clock.Now()
	active := 0
	rows, err := m.rows(ctx, "SELECT * FROM project_invites WHERE project_id=? AND revoked_at IS NULL", projectID)
	if err != nil {
		return nil, "", nil, err
	}
	for _, r := range rows {
		if inviteState(r, now) == inviteStateValid {
			active++
		}
	}
	if active >= InviteMaxActive {
		return nil, "", nil, orgErr(409, fmt.Sprintf("This project already has %d active invite links. Revoke one first.", InviteMaxActive), "INVITE_LIMIT")
	}
	actorID := m.userIDFor(ctx, actor)
	token := newInviteToken()
	id := "inv_" + rnd.Hex("invites", 8)
	createdAt := clock.Format(now)
	expires := clock.Format(now.Add(time.Duration(days) * 24 * time.Hour))
	if err := m.exec(ctx, "INSERT INTO project_invites(id,project_id,token_hash,hint,permission,allow_signup,label,max_uses,uses,"+
		"created_by,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?,0,?,?,?)",
		id, projectID, InviteHash(token), token[:4], permission, map[bool]int{true: 1, false: 0}[allowSignup], label, maxUses,
		actorID, createdAt, expires); err != nil {
		return nil, "", nil, err
	}
	detail := pyjson.NewObject("invite", id, "permission", permission, "max_uses", maxUses, "expires_at", expires,
		"allow_signup", allowSignup, "user", actor)
	if err := m.Log(ctx, actorID, inviteActionCreate, projectID, detail); err != nil {
		return nil, "", nil, err
	}
	r, _, err := m.row(ctx, inviteSelect+"WHERE i.id=?", id)
	if err != nil {
		return nil, "", nil, err
	}
	return m.invitePublic(r, now), token, &InviteActivity{projectID, inviteActionCreate, detail}, nil
}

// RevokeInvite ends an invite link (project admin only).
func (m *Manager) RevokeInvite(ctx context.Context, actor, projectID, inviteID string) (*InviteActivity, error) {
	if _, err := m.RequireProjectPermission(ctx, actor, projectID, "admin"); err != nil {
		return nil, err
	}
	r, ok, err := m.row(ctx, "SELECT * FROM project_invites WHERE id=? AND project_id=?", inviteID, projectID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, orgErr(404, "invite link not found", "INVITE_NOT_FOUND")
	}
	if r.Str("revoked_at") != "" {
		return nil, nil
	}
	actorID := m.userIDFor(ctx, actor)
	if err := m.exec(ctx, "UPDATE project_invites SET revoked_at=?, revoked_by=? WHERE id=? AND revoked_at IS NULL",
		clock.UTCNow(), actorID, inviteID); err != nil {
		return nil, err
	}
	detail := pyjson.NewObject("invite", inviteID, "user", actor)
	if err := m.Log(ctx, actorID, inviteActionRevoke, projectID, detail); err != nil {
		return nil, err
	}
	return &InviteActivity{projectID, inviteActionRevoke, detail}, nil
}

// ------------------------------------------------------------ joining side

// inviteLookup is an invite found by token with what joining needs.
type inviteLookup struct {
	row         sqlitedb.Row
	state       string
	projectName string
	creator     sqlitedb.Row
}

func (l *inviteLookup) projectID() string { return l.row.Str("project_id") }

// lookupInvite finds the invite of a token and works out its state.  An
// unknown token, a deleted project or a creator who no longer administers
// the project make the link "invalid" or "revoked".
func (m *Manager) lookupInvite(ctx context.Context, token string) (*inviteLookup, error) {
	token = pytext.Strip(token)
	if !WellFormedInviteToken(token) {
		return &inviteLookup{state: inviteStateInvalid}, nil
	}
	hash := InviteHash(token)
	r, ok, err := m.row(ctx, "SELECT * FROM project_invites WHERE token_hash=?", hash)
	if err != nil {
		return nil, err
	}
	if !ok || subtle.ConstantTimeCompare([]byte(r.Str("token_hash")), []byte(hash)) != 1 {
		return &inviteLookup{state: inviteStateInvalid}, nil
	}
	l := &inviteLookup{row: r, state: inviteState(r, clock.Now())}
	project, ok, err := m.ProjectRow(ctx, r.Str("project_id"))
	if err != nil {
		return nil, err
	}
	if !ok {
		l.state = inviteStateInvalid
		return l, nil
	}
	l.projectName = project.Str("name")
	creator, ok, err := m.UserByID(ctx, r.Str("created_by"))
	if err != nil {
		return nil, err
	}
	if l.state == inviteStateValid {
		if !ok || pyjson.Truthy(creator.Get("disabled")) {
			l.state = inviteStateRevoked
		} else if perm, err := m.ProjectPermission(ctx, creator.Str("username"), l.projectID()); err != nil {
			return nil, err
		} else if PermissionRank[perm] < PermissionRank["admin"] || PermissionRank[perm] < PermissionRank[r.Str("permission")] {
			l.state = inviteStateRevoked
		}
	}
	l.creator = creator
	return l, nil
}

// InviteErrorFor is the error of a link that cannot be used.
func InviteErrorFor(state string) error {
	switch state {
	case inviteStateExpired:
		return orgErr(410, "This invite link has expired. Ask for a new one.", "INVITE_EXPIRED")
	case inviteStateRevoked:
		return orgErr(410, "This invite link was revoked. Ask for a new one.", "INVITE_REVOKED")
	case inviteStateUsedUp:
		return orgErr(410, "This invite link has already been used. Ask for a new one.", "INVITE_USED_UP")
	}
	return orgErr(404, "This invite link is not valid. Check that you copied all of it.", "INVITE_INVALID")
}

// InviteValid reports whether a preview state can be used.
func InviteValid(state string) bool { return state == inviteStateValid }

// PreviewInvite describes a link to whoever opened it.  Only a usable
// link names its project and inviter.  viewer is the signed-in account
// ("" when nobody is signed in).
func (m *Manager) PreviewInvite(ctx context.Context, token, viewer string) (*pyjson.Object, error) {
	l, err := m.lookupInvite(ctx, token)
	if err != nil {
		return nil, err
	}
	out := pyjson.NewObject("state", l.state)
	if l.state != inviteStateValid {
		return out, nil
	}
	signupOrg, err := m.InviteSignupEnabled(ctx)
	if err != nil {
		return nil, err
	}
	inviter := strField(l.creator, "display_name")
	if inviter == "" {
		inviter = l.creator.Str("username")
	}
	out.Set("project", pyjson.NewObject("id", l.projectID(), "name", l.projectName)).
		Set("inviter", pyjson.NewObject("username", l.creator.Str("username"), "display_name", inviter)).
		Set("permission", l.row.Str("permission")).
		Set("expires_at", l.row.Get("expires_at")).
		Set("signup", signupOrg && pyjson.Truthy(l.row.Get("allow_signup"))).
		Set("password_min", int64(SignupPasswordMin))
	if viewer != "" {
		perm, err := m.ProjectPermission(ctx, viewer, l.projectID())
		if err != nil {
			return nil, err
		}
		out.Set("viewer", pyjson.NewObject("username", viewer, "permission", perm,
			"member", perm != "" && PermissionRank[perm] >= PermissionRank[l.row.Str("permission")]))
	}
	return out, nil
}

// consumeInvite re-checks an invite inside the write transaction and
// counts one use.
func consumeInvite(ctx context.Context, q sqlitedb.Querier, id, now string) error {
	r, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT * FROM project_invites WHERE id=?", id)
	if err != nil {
		return err
	}
	if !ok {
		return InviteErrorFor(inviteStateInvalid)
	}
	if state := inviteState(r, clock.Now()); state != inviteStateValid {
		return InviteErrorFor(state)
	}
	_, err = q.ExecContext(ctx, "UPDATE project_invites SET uses=uses+1, last_used_at=? WHERE id=?", now, id)
	return err
}

// joinTx gives userID the invite's access inside the write transaction:
// organization membership (member) when the account has none, and the
// project grant unless the account already has at least as much.
func joinTx(ctx context.Context, q sqlitedb.Querier, orgID, userID, projectID, permission, grantedBy, now string) error {
	if _, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT 1 FROM memberships WHERE org_id=? AND user_id=?", orgID, userID); err != nil {
		return err
	} else if !ok {
		if _, err := q.ExecContext(ctx, "INSERT INTO memberships(org_id,user_id,role,created_at) VALUES(?,?,?,?)",
			orgID, userID, inviteMemberRole, now); err != nil {
			return err
		}
	}
	g, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT permission FROM project_grants WHERE project_id=? AND user_id=?", projectID, userID)
	if err != nil {
		return err
	}
	if ok && PermissionRank[g.Str("permission")] >= PermissionRank[permission] {
		return nil
	}
	_, err = q.ExecContext(ctx, "INSERT INTO project_grants(project_id,user_id,permission,granted_by,created_at) VALUES(?,?,?,?,?) "+
		"ON CONFLICT(project_id,user_id) DO UPDATE SET permission=excluded.permission, granted_by=excluded.granted_by",
		projectID, userID, permission, grantedBy, now)
	return err
}

// JoinResult is what accepting a link did.
type JoinResult struct {
	ProjectID, ProjectName, Permission, Username string
	AlreadyMember                                bool
	Activity                                     []InviteActivity
}

// Object is the API reply of a join.
func (j *JoinResult) Object() *pyjson.Object {
	return pyjson.NewObject("ok", true, "project", pyjson.NewObject("id", j.ProjectID, "name", j.ProjectName),
		"permission", j.Permission, "already_member", j.AlreadyMember, "user", j.Username)
}

func (m *Manager) usableInvite(ctx context.Context, token string) (*inviteLookup, error) {
	l, err := m.lookupInvite(ctx, token)
	if err != nil {
		return nil, err
	}
	if l.state != inviteStateValid {
		return nil, InviteErrorFor(l.state)
	}
	return l, nil
}

func (m *Manager) joinActivity(l *inviteLookup, username string, signup bool) []InviteActivity {
	use := pyjson.NewObject("invite", l.row.Str("id"), "user", username, "permission", l.row.Str("permission"),
		"invited_by", l.creator.Str("username"), "new_account", signup)
	join := pyjson.NewObject("user", username, "permission", l.row.Str("permission"), "via", "invite",
		"invite", l.row.Str("id"), "invited_by", l.creator.Str("username"), "new_account", signup)
	return []InviteActivity{{l.projectID(), inviteActionUse, use}, {l.projectID(), inviteActionJoin, join}}
}

// AcceptInvite joins a signed-in account to the link's project.  An account
// that already has at least the link's access just opens the project and
// the link keeps its use.
func (m *Manager) AcceptInvite(ctx context.Context, token, username string) (*JoinResult, error) {
	l, err := m.usableInvite(ctx, token)
	if err != nil {
		return nil, err
	}
	user, ok, err := m.UserByName(ctx, username)
	if err != nil {
		return nil, err
	}
	if !ok || pyjson.Truthy(user.Get("disabled")) {
		return nil, orgErr(403, "This account cannot join projects.", "ACCOUNT_DISABLED")
	}
	res := &JoinResult{ProjectID: l.projectID(), ProjectName: l.projectName, Permission: l.row.Str("permission"),
		Username: user.Str("username")}
	perm, err := m.ProjectPermission(ctx, username, l.projectID())
	if err != nil {
		return nil, err
	}
	if perm != "" && PermissionRank[perm] >= PermissionRank[res.Permission] {
		res.AlreadyMember, res.Permission = true, perm
		return res, nil
	}
	orgID, now := m.OrgID(ctx), clock.UTCNow()
	err = m.DB.Write(ctx, func(q sqlitedb.Querier) error {
		if err := consumeInvite(ctx, q, l.row.Str("id"), now); err != nil {
			return err
		}
		return joinTx(ctx, q, orgID, user.Str("id"), l.projectID(), res.Permission, l.row.Str("created_by"), now)
	}, nil)
	if err != nil {
		return nil, err
	}
	res.Activity = m.joinActivity(l, res.Username, false)
	for _, a := range res.Activity {
		if err := m.Log(ctx, user.Str("id"), a.Action, a.ProjectID, a.Detail); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// SignupCheck validates an invite sign-up before the password is hashed:
// the link allows sign-up, the switch is on and the username is free.
func (m *Manager) SignupCheck(ctx context.Context, token, username, password string) (*inviteLookup, string, error) {
	l, err := m.usableInvite(ctx, token)
	if err != nil {
		return nil, "", err
	}
	enabled, err := m.InviteSignupEnabled(ctx)
	if err != nil {
		return nil, "", err
	}
	if !enabled || !pyjson.Truthy(l.row.Get("allow_signup")) {
		return nil, "", orgErr(403, "This invite link does not allow creating an account. Sign in with an existing account.", "INVITE_SIGNUP_DISABLED")
	}
	name, err := validateUsername(username)
	if err != nil {
		return nil, "", err
	}
	if strings.EqualFold(name, LocalUsername) {
		return nil, "", conflictErr("This username is taken.", "USER_EXISTS")
	}
	if n := pytext.Len(password); n < SignupPasswordMin {
		return nil, "", orgErr(400, fmt.Sprintf("The password must be at least %d characters.", SignupPasswordMin), "PASSWORD_TOO_SHORT")
	} else if n > auth.PasswordMaxLength {
		return nil, "", orgErr(400, fmt.Sprintf("The password must be at most %d characters.", auth.PasswordMaxLength), "PASSWORD_INVALID")
	}
	if _, taken, err := m.UserByName(ctx, name); err != nil {
		return nil, "", err
	} else if taken {
		return nil, "", conflictErr("This username is taken.", "USER_EXISTS")
	}
	return l, name, nil
}

// SignupWithInvite creates an account through an invite link and joins it
// to the project in one transaction.  hash is the PBKDF2 hash of the
// password (computed by the caller outside every lock).
func (m *Manager) SignupWithInvite(ctx context.Context, l *inviteLookup, username, display, hash string) (*JoinResult, error) {
	display, err := plainLine(display, "display name", maxDisplayName)
	if err != nil {
		return nil, err
	}
	orgID, now := m.OrgID(ctx), clock.UTCNow()
	userID := "usr_" + rnd.Hex("org", 8)
	err = m.DB.Write(ctx, func(q sqlitedb.Querier) error {
		if err := consumeInvite(ctx, q, l.row.Str("id"), now); err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, "INSERT INTO users(id,username,display_name,password_hash,disabled,created_at,updated_at) VALUES(?,?,?,?,0,?,?)",
			userID, username, display, hash, now, now); err != nil {
			if isUnique(err) {
				return conflictErr("This username is taken.", "USER_EXISTS")
			}
			return err
		}
		if err := joinTx(ctx, q, orgID, userID, l.projectID(), l.row.Str("permission"), l.row.Str("created_by"), now); err != nil {
			return err
		}
		_, err := q.ExecContext(ctx, "UPDATE users SET last_project_id=? WHERE id=?", l.projectID(), userID)
		return err
	}, nil)
	if err != nil {
		return nil, err
	}
	if err := m.Log(ctx, userID, inviteActionSignup, l.projectID(), pyjson.NewObject("user", username,
		"role", inviteMemberRole, "invite", l.row.Str("id"), "invited_by", l.creator.Str("username"))); err != nil {
		return nil, err
	}
	res := &JoinResult{ProjectID: l.projectID(), ProjectName: l.projectName, Permission: l.row.Str("permission"), Username: username}
	res.Activity = m.joinActivity(l, username, true)
	for _, a := range res.Activity {
		if err := m.Log(ctx, userID, a.Action, a.ProjectID, a.Detail); err != nil {
			return nil, err
		}
	}
	return res, nil
}
