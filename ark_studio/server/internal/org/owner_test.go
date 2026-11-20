package org

import (
	"context"
	"path/filepath"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

// T-0028: ProjectOwner names who adopts a project's pre-v10 Assistant chat.
func TestProjectOwner(t *testing.T) {
	m, err := Open(filepath.Join(t.TempDir(), "org.sqlite3"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ctx := context.Background()
	hash := "pbkdf2_sha256$1000$c2FsdHNhbHRzYWx0$" + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if err := m.Bootstrap(ctx, "Boss", hash, false, []*pyjson.Object{pyjson.NewObject("id", "prj_1", "name", "P", "path", "/tmp/p.ark")}); err != nil {
		t.Fatal(err)
	}
	if len(AvatarColors) == 0 {
		AvatarColors = []string{"#4f46e5"} // the server sets them from the static snapshot
	}
	if _, err := m.CreateUser(ctx, "Boss", pyjson.NewObject("username", "ana", "password", "correct horse battery", "role", "member")); err != nil {
		t.Fatal(err)
	}
	// Registered by ana (created_by), by the local user, by nobody.
	if err := m.RegisterProject(ctx, pyjson.NewObject("id", "prj_2", "name", "Q", "path", "/tmp/q.ark"), "ana", true); err != nil {
		t.Fatal(err)
	}
	if err := m.Bootstrap(ctx, "", "", false, []*pyjson.Object{pyjson.NewObject("id", "prj_3", "name", "L", "path", "/tmp/l.ark")}); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterProject(ctx, pyjson.NewObject("id", "prj_4", "name", "N", "path", "/tmp/n.ark"), "", false); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"prj_1": "Boss", "prj_2": "ana", "prj_3": "Boss", "prj_4": "Boss", "prj_missing": "Boss"} {
		if got, err := m.ProjectOwner(ctx, id); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", id, got, err, want)
		}
	}
	// A disabled creator falls back to the org owner.
	if err := m.exec(ctx, "UPDATE users SET disabled=1 WHERE username='ana'"); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.ProjectOwner(ctx, "prj_2"); got != "Boss" {
		t.Errorf("disabled creator: %q", got)
	}
}

// Local mode: the only owner is "local", which does not count.
func TestProjectOwnerLocalOnly(t *testing.T) {
	m, err := Open(filepath.Join(t.TempDir(), "org.sqlite3"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ctx := context.Background()
	if err := m.Bootstrap(ctx, "", "", false, []*pyjson.Object{pyjson.NewObject("id", "prj_1", "name", "P", "path", "/tmp/p.ark")}); err != nil {
		t.Fatal(err)
	}
	if got, err := m.ProjectOwner(ctx, "prj_1"); err != nil || got != "" {
		t.Errorf("local only: %q %v", got, err)
	}
}
