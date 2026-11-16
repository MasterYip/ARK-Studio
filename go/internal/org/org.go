// Package org ports the parts of arkstudio/org.py the Go server needs now:
// the organization database (schema, profile-column migration, agent token
// table), bootstrap at startup, account lookup for sign-in and the
// effective project permission of a user.
//
// The org database stays in journal_mode=DELETE exactly like Python, which
// opens one short connection per operation; Go keeps a small pool instead.
package org

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pytext"
	"arkstudio/go/internal/rnd"
	"arkstudio/go/internal/sqlitedb"
	"arkstudio/go/internal/store"
)

// Constants from org.py.
const (
	DefaultOrgID   = "org_default"
	DefaultOrgName = "ARK Studio"
	LocalUsername  = "local"
)

// RoleRank is ROLE_RANK.
var RoleRank = map[string]int{"owner": 3, "admin": 2, "member": 1, "viewer": 0}

// PermissionRank is PERMISSION_RANK.
var PermissionRank = map[string]int{"read": 1, "write": 2, "admin": 3}

// Error is OrgError.
type Error struct {
	Status        int
	Message, Code string
}

func (e *Error) Error() string { return e.Message }

// Manager is OrgManager on one org.sqlite3.
type Manager struct {
	Path string
	DB   *sqlitedb.DB

	memoMu    sync.Mutex
	memoStamp string
	memo      map[string]memoEntry
}

type memoEntry struct {
	row  sqlitedb.Row
	ok   bool
	text string
}

// stamp identifies the organization database state: this process's write
// generation plus the file's size and mtime.  The org database runs in
// journal_mode=DELETE, so every commit by any process (the Python server or
// CLI included) rewrites the main file.
func (m *Manager) stamp() string {
	st := strconv.FormatUint(m.DB.WriteGeneration(), 10)
	if fi, err := os.Stat(m.Path); err == nil {
		st += ":" + strconv.FormatInt(fi.Size(), 10) + ":" + strconv.FormatInt(fi.ModTime().UnixNano(), 10)
	}
	return st
}

// cached memoizes a lookup until the database changes.  Sign-in and
// permission checks run on every request; without this each request costs
// six to eight org queries.
func (m *Manager) cached(key string, fn func() (memoEntry, error)) (memoEntry, error) {
	stamp := m.stamp()
	m.memoMu.Lock()
	if m.memoStamp != stamp || len(m.memo) > 50000 {
		m.memoStamp, m.memo = stamp, map[string]memoEntry{}
	}
	e, hit := m.memo[key]
	m.memoMu.Unlock()
	if hit {
		return e, nil
	}
	e, err := fn()
	if err != nil {
		return e, err
	}
	m.memoMu.Lock()
	if m.memoStamp == stamp {
		m.memo[key] = e
	}
	m.memoMu.Unlock()
	return e, nil
}

// DefaultPath is default_org_path(project_root).
func DefaultPath(projectRoot string) string {
	if override := pytext.Strip(os.Getenv("ARK_ORG_DB")); override != "" {
		return resolve(override)
	}
	return filepath.Join(filepath.Dir(resolve(projectRoot)), "org.sqlite3")
}

func resolve(p string) string {
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[1:])
		}
	}
	abs, _ := filepath.Abs(p)
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

// Open is OrgStore.__init__ + AgentTokenStore.__init__ (schema + migration).
func Open(path string, readers int) (*Manager, error) {
	path = resolve(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sqlitedb.Open(path, sqlitedb.Options{Journal: "DELETE", Readers: readers, ForeignKeys: true})
	if err != nil {
		return nil, err
	}
	if err := db.Exec(store.OrgSchema); err != nil {
		db.Close()
		return nil, err
	}
	ctx := context.Background()
	err = db.Write(ctx, func(q sqlitedb.Querier) error {
		rows, err := sqlitedb.Query(ctx, q, "PRAGMA table_info(users)")
		if err != nil {
			return err
		}
		have := map[string]bool{}
		for _, r := range rows {
			have[r.Str("name")] = true
		}
		for _, col := range store.UserProfileColumns {
			if !have[col[0]] {
				if _, err := q.ExecContext(ctx, "ALTER TABLE users ADD COLUMN "+col[0]+" "+col[1]); err != nil {
					return err
				}
			}
		}
		_, err = q.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES('schema_version',?) "+
			"ON CONFLICT(key) DO UPDATE SET value=excluded.value", strconv.Itoa(store.OrgSchemaVersion))
		return err
	}, nil)
	if err == nil {
		err = db.Exec(store.AgentTokenSchema)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	os.Chmod(path, 0o600) //nolint:errcheck
	return &Manager{Path: path, DB: db}, nil
}

// Close closes the database.
func (m *Manager) Close() error { return m.DB.Close() }

func (m *Manager) row(ctx context.Context, query string, args ...any) (sqlitedb.Row, bool, error) {
	var r sqlitedb.Row
	var ok bool
	err := m.DB.Read(ctx, func(q sqlitedb.Querier) error {
		var err error
		r, ok, err = sqlitedb.QueryOne(ctx, q, query, args...)
		return err
	})
	return r, ok, err
}

func (m *Manager) exec(ctx context.Context, query string, args ...any) error {
	return m.DB.Write(ctx, func(q sqlitedb.Querier) error {
		_, err := q.ExecContext(ctx, query, args...)
		return err
	}, nil)
}

// Org is OrgStore.org().
func (m *Manager) Org(ctx context.Context) (sqlitedb.Row, bool, error) {
	return m.row(ctx, "SELECT * FROM orgs ORDER BY created_at LIMIT 1")
}

// OrgID is OrgManager.org_id.
func (m *Manager) OrgID(ctx context.Context) string {
	if r, ok, _ := m.Org(ctx); ok {
		return r.Str("id")
	}
	return DefaultOrgID
}

// UserByName is OrgStore.user_by_name (COLLATE NOCASE).
func (m *Manager) UserByName(ctx context.Context, username string) (sqlitedb.Row, bool, error) {
	text := pytext.Strip(username)
	if text == "" {
		return sqlitedb.Row{}, false, nil
	}
	return m.row(ctx, "SELECT * FROM users WHERE username=? COLLATE NOCASE", text)
}

// Account is OrgManager.account: an enabled user with a password hash.
func (m *Manager) Account(ctx context.Context, username string) (sqlitedb.Row, bool, error) {
	e, err := m.cached("account|"+pytext.Strip(username), func() (memoEntry, error) {
		r, ok, err := m.account(ctx, username)
		return memoEntry{row: r, ok: ok}, err
	})
	return e.row, e.ok, err
}

func (m *Manager) account(ctx context.Context, username string) (sqlitedb.Row, bool, error) {
	r, ok, err := m.UserByName(ctx, username)
	if err != nil || !ok || pyjson.Truthy(r.Get("disabled")) || pytext.Strip(r.Str("password_hash")) == "" {
		return sqlitedb.Row{}, false, err
	}
	return r, true, nil
}

// Membership is OrgStore.membership.
func (m *Manager) Membership(ctx context.Context, userID, orgID string) (sqlitedb.Row, bool, error) {
	return m.row(ctx, "SELECT * FROM memberships WHERE org_id=? AND user_id=?", orgID, userID)
}

// RoleFor is OrgManager.role_for.
func (m *Manager) RoleFor(ctx context.Context, username string) (string, bool, error) {
	e, err := m.cached("role|"+username, func() (memoEntry, error) {
		r, ok, err := m.roleFor(ctx, username)
		return memoEntry{text: r, ok: ok}, err
	})
	return e.text, e.ok, err
}

func (m *Manager) roleFor(ctx context.Context, username string) (string, bool, error) {
	u, ok, err := m.UserByName(ctx, username)
	if err != nil || !ok {
		return "", false, err
	}
	mem, ok, err := m.Membership(ctx, u.Str("id"), m.OrgID(ctx))
	if err != nil || !ok {
		return "", false, err
	}
	return mem.Str("role"), true, nil
}

// ProjectPermission is OrgManager.project_permission ("" = none).
func (m *Manager) ProjectPermission(ctx context.Context, username, projectID string) (string, error) {
	e, err := m.cached("perm|"+username+"|"+projectID, func() (memoEntry, error) {
		perm, err := m.projectPermission(ctx, username, projectID)
		return memoEntry{text: perm}, err
	})
	return e.text, err
}

func (m *Manager) projectPermission(ctx context.Context, username, projectID string) (string, error) {
	var result string
	err := m.DB.Read(ctx, func(q sqlitedb.Querier) error {
		text := pytext.Strip(username)
		if text == "" {
			return nil
		}
		user, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT * FROM users WHERE username=? COLLATE NOCASE", text)
		if err != nil || !ok || pyjson.Truthy(user.Get("disabled")) {
			return err
		}
		orgID := DefaultOrgID
		if o, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT * FROM orgs ORDER BY created_at LIMIT 1"); err != nil {
			return err
		} else if ok {
			orgID = o.Str("id")
		}
		mem, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT * FROM memberships WHERE org_id=? AND user_id=?", orgID, user.Str("id"))
		if err != nil || !ok {
			return err
		}
		if _, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT * FROM projects WHERE id=?", projectID); err != nil || !ok {
			return err
		}
		role := mem.Str("role")
		if role == "owner" || role == "admin" {
			result = "admin"
			return nil
		}
		best, bestRank := "", 0
		if role == "viewer" {
			best, bestRank = "read", 1
		}
		grants, err := sqlitedb.Query(ctx, q, "SELECT permission FROM project_grants WHERE project_id=? AND user_id=?", projectID, user.Str("id"))
		if err != nil {
			return err
		}
		teams, err := sqlitedb.Query(ctx, q, "SELECT g.permission FROM team_grants g JOIN team_members tm "+
			"ON tm.team_id=g.team_id WHERE g.project_id=? AND tm.user_id=?", projectID, user.Str("id"))
		if err != nil {
			return err
		}
		for _, g := range append(grants, teams...) {
			if rank := PermissionRank[g.Str("permission")]; rank > bestRank {
				best, bestRank = g.Str("permission"), rank
			}
			if best == "admin" {
				break
			}
		}
		result = best
		return nil
	})
	return result, err
}

func newID(prefix string) string {
	return prefix + rnd.Hex("org", 8)
}

// Bootstrap is OrgManager.bootstrap(owner_username=..., projects=...).
// ownerUser == "" is the loopback local owner.
func (m *Manager) Bootstrap(ctx context.Context, ownerUser, ownerHash string, sync bool, projects []*pyjson.Object) error {
	if _, ok, err := m.Org(ctx); err != nil {
		return err
	} else if !ok {
		if err := m.exec(ctx, "INSERT INTO orgs(id,name,created_at) VALUES(?,?,?)", DefaultOrgID, DefaultOrgName, store.UTCNow()); err != nil {
			return err
		}
	}
	orgID := m.OrgID(ctx)
	username := pytext.Strip(ownerUser)
	var owner sqlitedb.Row
	var found bool
	var err error
	if username != "" {
		owner, found, err = m.UserByName(ctx, username)
		if err != nil {
			return err
		}
		if !found {
			if pytext.Strip(ownerHash) == "" {
				return &Error{500, "the bootstrap owner account has no password hash; ARK cannot create a sign-in account", "OWNER_PASSWORD_MISSING"}
			}
			now := store.UTCNow()
			display := []rune(username)
			if len(display) > 80 {
				display = display[:80]
			}
			if err := m.exec(ctx, "INSERT INTO users(id,username,display_name,password_hash,disabled,created_at,updated_at) VALUES(?,?,?,?,0,?,?)",
				newID("usr_"), username, string(display), ownerHash, now, now); err != nil {
				return err
			}
		} else if sync && pytext.Strip(ownerHash) != "" {
			if err := m.exec(ctx, "UPDATE users SET password_hash=?, updated_at=? WHERE id=?", ownerHash, store.UTCNow(), owner.Str("id")); err != nil {
				return err
			}
		}
		owner, _, err = m.UserByName(ctx, username)
		if err != nil {
			return err
		}
		if err := m.setMembership(ctx, owner.Str("id"), "owner", orgID); err != nil {
			return err
		}
	} else {
		username = LocalUsername
		owner, found, err = m.UserByName(ctx, username)
		if err != nil {
			return err
		}
		if !found {
			now := store.UTCNow()
			_ = m.exec(ctx, "INSERT INTO users(id,username,display_name,password_hash,disabled,created_at,updated_at) VALUES(?,?,?,NULL,0,?,?)",
				"usr_local", username, "Local user", now, now) // IntegrityError ignored like Python
			owner, found, err = m.UserByName(ctx, username)
			if err != nil {
				return err
			}
		}
		if found || owner.Cols != nil {
			if _, ok, err := m.Membership(ctx, owner.Str("id"), orgID); err != nil {
				return err
			} else if !ok {
				if err := m.exec(ctx, "INSERT INTO memberships(org_id,user_id,role,created_at) VALUES(?,?,?,?)", orgID, owner.Str("id"), "owner", store.UTCNow()); err != nil {
					return err
				}
			}
		}
	}
	for _, summary := range projects {
		if err := m.registerProject(ctx, summary, username, true); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) setMembership(ctx context.Context, userID, role, orgID string) error {
	mem, ok, err := m.Membership(ctx, userID, orgID)
	if err != nil {
		return err
	}
	if ok {
		if mem.Str("role") == role {
			return nil
		}
		return m.exec(ctx, "UPDATE memberships SET role=? WHERE org_id=? AND user_id=?", role, orgID, userID)
	}
	return m.exec(ctx, "INSERT INTO memberships(org_id,user_id,role,created_at) VALUES(?,?,?,?)", orgID, userID, role, store.UTCNow())
}

// registerProject is OrgManager.register_project(summary, grant_owner=...).
func (m *Manager) registerProject(ctx context.Context, summary *pyjson.Object, ownerUser string, grantOwner bool) error {
	projectID := pytext.Strip(strOf(summary.Value("id")))
	path := pytext.Strip(strOf(summary.Value("path")))
	if projectID == "" || path == "" {
		return nil
	}
	createdBy := ""
	if ownerUser != "" {
		if u, ok, err := m.UserByName(ctx, ownerUser); err != nil {
			return err
		} else if ok {
			createdBy = u.Str("id")
		}
	}
	name := strOf(summary.Value("name"))
	if name == "" {
		name = projectID
	}
	updated := strOf(summary.Value("updated"))
	recordID := projectID
	existing, ok, err := m.row(ctx, "SELECT * FROM projects WHERE path=?", path)
	if err != nil {
		return err
	}
	if ok {
		if existing.Str("id") != projectID {
			recordID = existing.Str("id")
		} else if name != existing.Str("name") || (updated != "" && updated != existing.Str("updated_at")) {
			newName := name
			if newName == "" {
				newName = existing.Str("name")
			}
			newUpdated := updated
			if newUpdated == "" {
				newUpdated = existing.Str("updated_at")
			}
			if err := m.exec(ctx, "UPDATE projects SET name=?, updated_at=? WHERE id=?", newName, newUpdated, projectID); err != nil {
				return err
			}
		}
	} else {
		now := store.UTCNow()
		if updated == "" {
			updated = now
		}
		if err := m.exec(ctx, "INSERT INTO projects(id,org_id,name,path,created_at,updated_at,created_by) VALUES(?,?,?,?,?,?,?)",
			projectID, m.OrgID(ctx), name, path, now, updated, createdBy); err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return &Error{409, "project is already registered", "PROJECT_EXISTS"}
			}
			return err
		}
	}
	if grantOwner && createdBy != "" {
		_, has, err := m.row(ctx, "SELECT 1 FROM project_grants WHERE project_id=? AND user_id=?", recordID, createdBy)
		if err != nil {
			return err
		}
		if !has {
			return m.exec(ctx, "INSERT INTO project_grants(project_id,user_id,permission,granted_by,created_at) VALUES(?,?,?,?,?) "+
				"ON CONFLICT(project_id,user_id) DO UPDATE SET permission=excluded.permission, granted_by=excluded.granted_by",
				recordID, createdBy, "admin", createdBy, store.UTCNow())
		}
	}
	return nil
}

func strOf(v any) string {
	if !pyjson.Truthy(v) {
		return ""
	}
	return pyjson.Str(v)
}

// ProjectRow is OrgStore.project_row.
func (m *Manager) ProjectRow(ctx context.Context, id string) (sqlitedb.Row, bool, error) {
	return m.row(ctx, "SELECT * FROM projects WHERE id=?", id)
}

// ProjectByPath is OrgStore.project_by_path.
func (m *Manager) ProjectByPath(ctx context.Context, path string) (sqlitedb.Row, bool, error) {
	return m.row(ctx, "SELECT * FROM projects WHERE path=?", path)
}

// VisibleProjectIDs is the id set of OrgManager.projects_for(username).
func (m *Manager) VisibleProjectIDs(ctx context.Context, username string) (map[string]bool, error) {
	out := map[string]bool{}
	var list []sqlitedb.Row
	// Never take a second pooled connection while holding one: with every
	// connection held by an outer read, the inner wait deadlocks the pool.
	orgID := m.OrgID(ctx)
	err := m.DB.Read(ctx, func(q sqlitedb.Querier) error {
		var err error
		list, err = sqlitedb.Query(ctx, q, "SELECT id FROM projects WHERE org_id=? ORDER BY created_at", orgID)
		return err
	})
	if err != nil {
		return nil, err
	}
	for _, p := range list {
		perm, err := m.ProjectPermission(ctx, username, p.Str("id"))
		if err != nil {
			return nil, err
		}
		if perm != "" {
			out[p.Str("id")] = true
		}
	}
	return out, nil
}

// ErrNotFound is returned by lookups that found nothing.
var ErrNotFound = errors.New("org: not found")

// TouchProject is OrgStore.touch_project.
func (m *Manager) TouchProject(ctx context.Context, projectID, name, updatedAt string) error {
	row, ok, err := m.ProjectRow(ctx, projectID)
	if err != nil || !ok {
		return err
	}
	if name == "" {
		name = row.Str("name")
	}
	if updatedAt == "" {
		updatedAt = store.UTCNow()
	}
	return m.exec(ctx, "UPDATE projects SET name=?, updated_at=? WHERE id=?", name, updatedAt, projectID)
}

// Log is OrgStore.log (the audit table).
func (m *Manager) Log(ctx context.Context, userID, action, projectID string, detail *pyjson.Object) error {
	if detail == nil {
		detail = pyjson.NewObject()
	}
	payload := pytext.Prefix(string(pyjson.Dumps(detail)), 4000)
	return m.exec(ctx, "INSERT INTO audit(at,user_id,action,project_id,detail_json) VALUES(?,?,?,?,?)",
		store.UTCNow(), userID, pytext.Prefix(action, 120), projectID, payload)
}

// SetLastProject is OrgStore.set_last_project.
func (m *Manager) SetLastProject(ctx context.Context, userID, projectID string) error {
	return m.exec(ctx, "UPDATE users SET last_project_id=? WHERE id=?", projectID, userID)
}

// LastProject is OrgStore.last_project.
func (m *Manager) LastProject(ctx context.Context, userID string) (string, error) {
	r, ok, err := m.row(ctx, "SELECT last_project_id FROM users WHERE id=?", userID)
	if err != nil || !ok {
		return "", err
	}
	return r.Str("last_project_id"), nil
}

// UserIDFor is OrgService.user_id_for ("" = None).
func (m *Manager) UserIDFor(ctx context.Context, username string) (string, error) {
	row, ok, err := m.UserByName(ctx, username)
	if err != nil || !ok {
		return "", err
	}
	return row.Str("id"), nil
}
