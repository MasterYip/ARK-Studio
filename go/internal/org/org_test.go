package org

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"arkstudio/go/internal/pyjson"
)

// Hundreds of concurrent first requests resolve permissions at once; a
// nested pooled read once deadlocked the 4-connection pool here.
func TestConcurrentPermissionLookupsDoNotDeadlock(t *testing.T) {
	m, err := Open(filepath.Join(t.TempDir(), "org.sqlite3"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ctx := context.Background()
	hash := "pbkdf2_sha256$1000$c2FsdHNhbHRzYWx0$" + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	project := pyjson.NewObject("id", "prj_1", "name", "P", "path", "/tmp/p.ark")
	if err := m.Bootstrap(ctx, "owner", hash, false, []*pyjson.Object{project}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids, err := m.VisibleProjectIDs(ctx, "owner")
			if err == nil && !ids["prj_1"] {
				err = fmt.Errorf("owner must see the project, got %v", ids)
			}
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	perm, err := m.ProjectPermission(ctx, "OWNER", "prj_1")
	if err != nil || perm != "admin" {
		t.Fatalf("case-insensitive owner lookup: %q %v", perm, err)
	}
}
