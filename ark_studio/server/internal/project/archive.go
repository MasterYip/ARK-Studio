package project

import (
	"archive/zip"
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"arkstudio/go/internal/platform/clock"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/sqlitedb"
)

// Port of arkstudio/project_archive.py: restorable project ZIPs with an
// online SQLite backup and the project's internal files.

var (
	archiveExcludeDirs  = map[string]bool{".git": true, ".venv": true, "venv": true, "__pycache__": true, ".pytest_cache": true, "node_modules": true, "exports": true}
	archiveExcludeFiles = map[string]bool{"ark.sqlite3": true, "ark.sqlite3-wal": true, "ark.sqlite3-shm": true, "ark.sqlite3-journal": true, "project.json": true}
)

func isLink(path string) bool {
	fi, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0
}

func inside(path, root string) bool {
	r := Resolve(path)
	return r == root || strings.HasPrefix(r, root+string(filepath.Separator))
}

type archiveFile struct{ source, rel string }

// internalFiles is project_archive._internal_files (os.walk, sorted, no links).
func internalFiles(root string, includeSessions bool) []archiveFile {
	var out []archiveFile
	var walk func(dir string)
	walk = func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		var dirs, files []string
		for _, e := range entries {
			full := filepath.Join(dir, e.Name())
			fi, err := os.Stat(full)
			if e.IsDir() || (err == nil && fi.IsDir() && isLink(full)) {
				dirs = append(dirs, e.Name())
			} else {
				files = append(files, e.Name())
			}
		}
		sort.Strings(files)
		for _, name := range files {
			source := filepath.Join(dir, name)
			if dir == root && archiveExcludeFiles[name] {
				continue
			}
			if fi, err := os.Stat(source); !isLink(source) && inside(source, root) && err == nil && fi.Mode().IsRegular() {
				rel, _ := filepath.Rel(root, source)
				out = append(out, archiveFile{source, filepath.ToSlash(rel)})
			}
		}
		sort.Strings(dirs)
		for _, name := range dirs {
			full := filepath.Join(dir, name)
			// sessions/ (agent session transcripts) is opt-in; see routes_exports.go.
			if archiveExcludeDirs[name] || (dir == root && name == "sessions" && !includeSessions) || isLink(full) || !inside(full, root) {
				continue
			}
			walk(full)
		}
	}
	walk(root)
	return out
}

// WriteArchiveSessions is write_project_archive(..., include_sessions=...):
// sessions/ adds uploaded external transcripts and the redacted mirrors of
// ARK task sessions (sessions/internal/<id>/).
func WriteArchiveSessions(ctx context.Context, p *Project, w io.Writer, scratch string, includeSessions bool) (*pyjson.Object, error) {
	return WriteArchiveChat(ctx, p, w, scratch, includeSessions, nil)
}

// ArchiveChatExcluded is the manifest "excluded" entry of an archive whose
// database keeps only the exporter's own Assistant chat.
const ArchiveChatExcluded = "other users' Assistant chat"

// WriteArchiveChat is WriteArchiveSessions whose database copy, when
// chatUser is not nil, keeps only that user's Assistant chat (T-0028: a
// non-admin export must not carry other members' threads).  nil keeps
// every row (local mode, project admins).
func WriteArchiveChat(ctx context.Context, p *Project, w io.Writer, scratch string, includeSessions bool, chatUser *string) (*pyjson.Object, error) {
	root := Resolve(p.Path)
	folder := filepath.Base(root)
	if folder == "" || folder == "." || folder == ".." || strings.Contains(folder, `\`) {
		return nil, &Error{"Invalid project folder name"}
	}
	if !strings.HasSuffix(folder, ".ark") {
		folder += ".ark"
	}
	now := clock.Now()
	excluded := []any{"external files and symlinks", "exports/", "development caches"}
	if !includeSessions {
		excluded = append(excluded, "sessions/ (agent session transcripts)")
	}
	if chatUser != nil {
		excluded = append(excluded, ArchiveChatExcluded)
	}
	manifest := pyjson.NewObject("format", "ark-studio-project-archive", "version", int64(1), "folder", folder,
		"exported_at", clock.UTCNow(),
		"excluded", excluded,
		"sessions_included", includeSessions,
		"restore", "Extract the .ark folder into an ARK projects directory, then open it in ARK.")
	database := filepath.Join(scratch, "ark.sqlite3")
	zw := zip.NewWriter(w)
	count := 0
	add := func(name string, data []byte) error {
		f, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: now})
		if err != nil {
			return err
		}
		count++
		_, err = f.Write(data)
		return err
	}
	addFile := func(source, name string) error {
		data, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		return add(name, data)
	}
	var files []archiveFile
	// History objects and pending text state must describe the same checkpoint
	// as the database copy: hold the writer while copying them.
	err := p.Store.DB.Write(ctx, func(q sqlitedb.Querier) error {
		if err := sqlitedb.BackupFile(ctx, filepath.Join(root, "ark.sqlite3"), database); err != nil {
			return err
		}
		if chatUser != nil {
			if err := keepOwnChat(ctx, database, *chatUser); err != nil {
				return err
			}
		}
		if err := addFile(database, folder+"/ark.sqlite3"); err != nil {
			return err
		}
		if err := add(folder+"/project.json", append(pyjson.DumpsIndent(p.Manifest.Snapshot(), 2), '\n')); err != nil {
			return err
		}
		files = internalFiles(root, includeSessions)
		for _, f := range files {
			if strings.HasPrefix(f.rel, "history.git/") || strings.HasPrefix(f.rel, "logs/") {
				if err := addFile(f.source, folder+"/"+f.rel); err != nil {
					return err
				}
			}
		}
		return nil
	}, nil)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if !strings.HasPrefix(f.rel, "history.git/") && !strings.HasPrefix(f.rel, "logs/") {
			if err := addFile(f.source, folder+"/"+f.rel); err != nil {
				return nil, err
			}
		}
	}
	if includeSessions {
		seen := map[string]bool{}
		for _, f := range files {
			if strings.HasPrefix(f.rel, "sessions/internal/") && strings.Count(f.rel, "/") >= 3 {
				seen[strings.Split(f.rel, "/")[2]] = true
			}
		}
		manifest.Set("internal_sessions", int64(len(seen)))
	}
	manifest.Set("files", int64(count))
	if err := add("ARK-EXPORT.json", append(pyjson.DumpsIndent(manifest, 2), '\n')); err != nil {
		return nil, err
	}
	return manifest, zw.Close()
}

// keepOwnChat removes every Assistant chat row but user's from the database
// copy at path (pre-v10 rows with an empty user included), and the other
// users' clear marks, then VACUUMs so no deleted page keeps their text.
func keepOwnChat(ctx context.Context, path, user string) error {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{"DELETE FROM chat_messages WHERE user<>? OR user=''", []any{user}},
		{"DELETE FROM meta WHERE key LIKE 'chat_cleared:%' AND substr(key,1,13)='chat_cleared:' AND key<>?", []any{"chat_cleared:" + user}},
		{"VACUUM", nil},
	} {
		if _, err := db.ExecContext(ctx, stmt.sql, stmt.args...); err != nil {
			return err
		}
	}
	return db.Close()
}
