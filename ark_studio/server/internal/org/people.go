package org

// Go only: who created each project and who made its latest change, for the
// project picker (internal/server/project_people.go).
//
// The creator is projects.created_by, but only when the row was registered
// by the create route: SyncProjects and the org bootstrap register projects
// they find on disk with the server owner as created_by, which is not who
// created them.  The create route registers the project right after writing
// it, so its row's created_at matches the manifest's "created"; a project
// found later has a later created_at.  CreatorWindow is that tolerance.
//
// The latest writer lives in project_last_writer (one row per project,
// upserted in batches by the server).  tests/python/contract/statediff.py
// GO_ONLY_TABLES leaves the table out of the Python/Go state comparison.

import (
	"context"
	"strings"
	"time"

	"arkstudio/go/internal/sqlitedb"
)

// CreatorWindow is how far the org row's created_at may be from the
// manifest's "created" for created_by to count as the creator.
const CreatorWindow = 10 * time.Second

const lastWriterSchema = `CREATE TABLE IF NOT EXISTS project_last_writer(
  project_id TEXT PRIMARY KEY,
  username TEXT NOT NULL DEFAULT '',
  actor TEXT NOT NULL DEFAULT '',
  at TEXT NOT NULL
)`

// LastWriter is the account and activity actor of a project's latest
// change ("human:ui", "agent:MasterYip/video-font", ...).
type LastWriter struct {
	User, Actor, At string
}

// ProjectCreatorRow is a project's org row: created_by (resolved to an
// account) and created_at.
type ProjectCreatorRow struct {
	UserID, Username, CreatedAt string
}

// Person is an account's username and display name.
type Person struct {
	ID, Username, DisplayName string
	Disabled                  bool
}

// ProjectCreators is every project's created_by account and created_at, by
// project id (one query).
func (m *Manager) ProjectCreators(ctx context.Context) (map[string]ProjectCreatorRow, error) {
	rows, err := m.rows(ctx, "SELECT p.id, p.created_at, COALESCE(p.created_by,'') created_by, COALESCE(u.username,'') username "+
		"FROM projects p LEFT JOIN users u ON u.id=p.created_by")
	if err != nil {
		return nil, err
	}
	out := make(map[string]ProjectCreatorRow, len(rows))
	for _, r := range rows {
		out[r.Str("id")] = ProjectCreatorRow{UserID: r.Str("created_by"), Username: r.Str("username"), CreatedAt: r.Str("created_at")}
	}
	return out, nil
}

// People is every account by lower-case username (one query).
func (m *Manager) People(ctx context.Context) (map[string]Person, error) {
	rows, err := m.rows(ctx, "SELECT id, username, display_name, disabled FROM users")
	if err != nil {
		return nil, err
	}
	out := make(map[string]Person, len(rows))
	for _, r := range rows {
		out[strings.ToLower(r.Str("username"))] = Person{ID: r.Str("id"), Username: r.Str("username"),
			DisplayName: strings.TrimSpace(r.Str("display_name")), Disabled: r.Int("disabled") != 0}
	}
	return out, nil
}

// LastWriters is the stored latest writer of every project (one query).
func (m *Manager) LastWriters(ctx context.Context) (map[string]LastWriter, error) {
	rows, err := m.rows(ctx, "SELECT project_id, username, actor, at FROM project_last_writer")
	if err != nil {
		return nil, err
	}
	out := make(map[string]LastWriter, len(rows))
	for _, r := range rows {
		out[r.Str("project_id")] = LastWriter{User: r.Str("username"), Actor: r.Str("actor"), At: r.Str("at")}
	}
	return out, nil
}

// SaveLastWriters upserts the given projects' latest writers in one write.
func (m *Manager) SaveLastWriters(ctx context.Context, writers map[string]LastWriter) error {
	if len(writers) == 0 {
		return nil
	}
	return m.DB.Write(ctx, func(q sqlitedb.Querier) error {
		for id, w := range writers {
			if _, err := q.ExecContext(ctx, "INSERT INTO project_last_writer(project_id,username,actor,at) VALUES(?,?,?,?) "+
				"ON CONFLICT(project_id) DO UPDATE SET username=excluded.username, actor=excluded.actor, at=excluded.at "+
				"WHERE excluded.at >= project_last_writer.at", id, w.User, w.Actor, w.At); err != nil {
				return err
			}
		}
		return nil
	}, nil)
}
