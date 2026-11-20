package org

import (
	"context"

	"arkstudio/go/internal/platform/clock"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/sqlitedb"
)

// Project registry writes used by project create / delete.

// RegisterProject is OrgService.register_project.
func (m *Manager) RegisterProject(ctx context.Context, summary *pyjson.Object, ownerUser string, grantOwner bool) error {
	return m.registerProject(ctx, summary, ownerUser, grantOwner)
}

// Owners is OrgStore.owners(org_id).
func (m *Manager) Owners(ctx context.Context) ([]sqlitedb.Row, error) {
	rows, err := m.Members(ctx)
	if err != nil {
		return nil, err
	}
	var out []sqlitedb.Row
	for _, r := range rows {
		if r.Str("role") == "owner" {
			out = append(out, r)
		}
	}
	return out, nil
}

// ProjectGrantUsers is the user ids of OrgStore.project_grants(project_id).
func (m *Manager) ProjectGrantUsers(ctx context.Context, projectID string) (map[string]bool, error) {
	rows, err := m.rows(ctx, "SELECT g.user_id FROM project_grants g JOIN users u ON u.id=g.user_id WHERE g.project_id=?", projectID)
	out := map[string]bool{}
	for _, r := range rows {
		out[r.Str("user_id")] = true
	}
	return out, err
}

// SetProjectGrant is OrgStore.set_project_grant (permission already valid).
func (m *Manager) SetProjectGrant(ctx context.Context, projectID, userID, permission, grantedBy string) error {
	return m.exec(ctx, "INSERT INTO project_grants(project_id,user_id,permission,granted_by,created_at)"+
		" VALUES(?,?,?,?,?) ON CONFLICT(project_id,user_id) DO UPDATE SET "+
		"permission=excluded.permission, granted_by=excluded.granted_by",
		projectID, userID, permission, grantedBy, clock.UTCNow())
}

// UnregisterProject is OrgStore.unregister_project.
func (m *Manager) UnregisterProject(ctx context.Context, projectID string) error {
	return m.DB.Write(ctx, func(q sqlitedb.Querier) error {
		for _, sql := range []string{"DELETE FROM project_grants WHERE project_id=?",
			"DELETE FROM team_grants WHERE project_id=?", "DELETE FROM project_last_writer WHERE project_id=?",
			"DELETE FROM project_invites WHERE project_id=?",
			"DELETE FROM projects WHERE id=?"} {
			if _, err := q.ExecContext(ctx, sql, projectID); err != nil {
				return err
			}
		}
		return nil
	}, nil)
}

// ProjectOwner is the account that owns a project's pre-per-user data
// (T-0028, store.AdoptLegacyChat): the enabled user who created the project
// (projects.created_by), else the org's first enabled owner.  The loopback
// "local" user does not count (a project created in local mode and served
// with accounts later goes to the org owner).  "" when nobody qualifies.
func (m *Manager) ProjectOwner(ctx context.Context, projectID string) (string, error) {
	var name string
	err := m.DB.Read(ctx, func(q sqlitedb.Querier) error {
		r, ok, err := sqlitedb.QueryOne(ctx, q, `SELECT u.username FROM projects p JOIN users u ON u.id=p.created_by
			WHERE p.id=? AND COALESCE(u.disabled,0)=0 AND lower(u.username)<>?`, projectID, LocalUsername)
		if err != nil {
			return err
		}
		if !ok {
			r, ok, err = sqlitedb.QueryOne(ctx, q, `SELECT u.username FROM memberships m JOIN users u ON u.id=m.user_id
				WHERE m.org_id=? AND m.role='owner' AND COALESCE(u.disabled,0)=0 AND lower(u.username)<>?
				ORDER BY m.created_at, u.username LIMIT 1`, m.orgIDq(ctx, q), LocalUsername)
			if err != nil {
				return err
			}
		}
		if ok {
			name = r.Str("username")
		}
		return nil
	})
	return name, err
}

// orgIDq is OrgID inside a read (the first org, else the default id).
func (m *Manager) orgIDq(ctx context.Context, q sqlitedb.Querier) string {
	if r, ok, _ := sqlitedb.QueryOne(ctx, q, "SELECT id FROM orgs ORDER BY created_at LIMIT 1"); ok {
		return r.Str("id")
	}
	return DefaultOrgID
}
