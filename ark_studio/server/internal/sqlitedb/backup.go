package sqlitedb

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"

	sqlite "modernc.org/sqlite"
)

// BackupFile copies the database at src into a new file dst with SQLite's
// online backup API (Python: sqlite3.Connection.backup), from a fresh
// connection that sees every committed write.
func BackupFile(ctx context.Context, src, dst string) error {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(src)+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return err
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return conn.Raw(func(dc any) error {
		bk, ok := dc.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return errors.New("sqlite driver has no backup API")
		}
		backup, err := bk.NewBackup(dst)
		if err != nil {
			return err
		}
		for {
			more, err := backup.Step(-1)
			if err != nil {
				backup.Finish() //nolint:errcheck
				return err
			}
			if !more {
				return backup.Finish()
			}
		}
	})
}
