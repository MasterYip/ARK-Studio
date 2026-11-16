package org

import (
	"context"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/sqlitedb"
	"arkstudio/go/internal/store"
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
		projectID, userID, permission, grantedBy, store.UTCNow())
}

// UnregisterProject is OrgStore.unregister_project.
func (m *Manager) UnregisterProject(ctx context.Context, projectID string) error {
	return m.DB.Write(ctx, func(q sqlitedb.Querier) error {
		for _, sql := range []string{"DELETE FROM project_grants WHERE project_id=?",
			"DELETE FROM team_grants WHERE project_id=?", "DELETE FROM projects WHERE id=?"} {
			if _, err := q.ExecContext(ctx, sql, projectID); err != nil {
				return err
			}
		}
		return nil
	}, nil)
}
