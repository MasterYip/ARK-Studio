package store

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"arkstudio/go/internal/platform/textfile"

	"arkstudio/go/internal/compat/pyjson"
)

func seedStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	st, err := Open(dir, NewManifest(pyjson.NewObject("id", "prj_t", "name", "T")), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "ark.sqlite3")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 1; i <= 50; i++ {
		_, err := db.Exec(`INSERT INTO units(uid,type,title,body,created_at,updated_at) VALUES(?,?,?,?,?,?)`,
			fmt.Sprintf("N-%04d", i), "note", fmt.Sprintf("节点 %d", i), "body", "2026-01-01T00:00:00+00:00",
			fmt.Sprintf("2026-01-01T00:00:%02d+00:00", i))
		if err != nil {
			t.Fatal(err)
		}
		db.Exec(`INSERT INTO unit_tags(unit_id,tag) VALUES(?,?)`, i, "area:x")
		if i > 1 {
			db.Exec(`INSERT INTO unit_links(source_id,target_id,relation,created_at) VALUES(?,?,?,?)`, i, i-1, "part_of", "t")
		}
	}
	return st
}

func checkSame(t *testing.T, st *Store, what string) time.Duration {
	t.Helper()
	ctx := context.Background()
	start := time.Now()
	inc, err := st.GraphSnapshotEncoded(ctx, true)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	for _, bodies := range []bool{true, false} {
		if !bodies {
			if inc, err = st.GraphSnapshotEncoded(ctx, false); err != nil {
				t.Fatal(err)
			}
		}
		full, err := st.GraphSnapshot(ctx, bodies)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(inc, pyjson.Dumps(full)) {
			t.Fatalf("%s (bodies=%v): incremental snapshot differs from a full build", what, bodies)
		}
	}
	return elapsed
}

func TestIncrementalSnapshotEqualsFullBuild(t *testing.T) {
	st := seedStore(t)
	ctx := context.Background()
	checkSame(t, st, "first build")
	for i, ref := range []string{"3", "N-0007", "3"} {
		if _, err := st.AddComment(ctx, ref, fmt.Sprintf("comment %d 😀", i), "comment", nil, "local", "human:ui"); err != nil {
			t.Fatal(err)
		}
		checkSame(t, st, "after comment "+ref)
	}
	if _, err := st.ResolveComment(ctx, "3", "1", true, "local", "human:ui"); err != nil {
		t.Fatal(err)
	}
	checkSame(t, st, "after resolve")
	// Another process (the Python CLI, a second server) writes behind our
	// back: the cache must notice through the file stamp.
	other, err := sql.Open("sqlite", "file:"+filepath.ToSlash(st.DB.Path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.Exec(`UPDATE units SET title='changed elsewhere' WHERE id=9`); err != nil {
		t.Fatal(err)
	}
	checkSame(t, st, "after an external write")
	if _, err := other.Exec(`UPDATE units SET archived=1 WHERE id=12`); err != nil {
		t.Fatal(err)
	}
	checkSame(t, st, "after an external archive")
	if _, err := st.AddComment(ctx, "5", "after external", "comment", nil, "local", "human:ui"); err != nil {
		t.Fatal(err)
	}
	checkSame(t, st, "comment after external changes")
}

func TestIncrementalSnapshotUnderConcurrentWrites(t *testing.T) {
	st := seedStore(t)
	ctx := context.Background()
	checkSame(t, st, "first build")
	done := make(chan error, 8)
	for w := 0; w < 4; w++ {
		go func(w int) {
			for i := 0; i < 25; i++ {
				ref := fmt.Sprint(1 + (w*7+i)%50)
				if _, err := st.AddComment(ctx, ref, fmt.Sprintf("w%d-%d", w, i), "comment", nil, "local", "human:ui"); err != nil {
					done <- err
					return
				}
			}
			done <- nil
		}(w)
	}
	for r := 0; r < 4; r++ {
		go func() {
			for i := 0; i < 15; i++ {
				if _, err := st.GraphSnapshotEncoded(ctx, i%2 == 0); err != nil {
					done <- err
					return
				}
			}
			done <- nil
		}()
	}
	for i := 0; i < 8; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	checkSame(t, st, "after concurrent writes")
}

func manifestFile(t *testing.T, st *Store) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(st.Path, "project.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func manifestWant(st *Store) string {
	return string(textfile.Encode(string(pyjson.DumpsIndent(st.Manifest.Snapshot(), 2)) + "\n"))
}

// project.json: a write without queued writers rewrites it before it
// returns (what every sequential request and the replays see); under a
// burst the rewrite may be left to a later writer or the flusher, but the
// file always ends with the last write's manifest, and Close flushes.
func TestManifestRewriteUnderQueuedWriters(t *testing.T) {
	st := seedStore(t)
	ctx := context.Background()
	if _, err := st.AddComment(ctx, "1", "solo", "comment", nil, "local", "human:ui"); err != nil {
		t.Fatal(err)
	}
	if got, want := manifestFile(t, st), manifestWant(st); got != want {
		t.Fatalf("sequential write: project.json not rewritten\n got %q\nwant %q", got, want)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				if _, err := st.AddComment(ctx, fmt.Sprint(1+(w+i)%50), "burst", "comment", nil, "local", "human:ui"); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	// Poll gently: on Windows each read holds project.json open, and a
	// rewrite that meets the open handle has to wait for it.  The deadline
	// is generous for loaded machines; normally this returns at once.
	deadline := time.Now().Add(30 * time.Second)
	for {
		raw, err := os.ReadFile(filepath.Join(st.Path, "project.json"))
		if err == nil && string(raw) == manifestWant(st) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("project.json never caught up with the last write (read error: %v)", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// A deferred rewrite pending at Close is written by Close.
	st.Manifest.Mutate(func(o *pyjson.Object) error { o.Set("updated", "2099-01-01T00:00:00+00:00"); return nil }) //nolint:errcheck
	st.manifestDirty.Store(true)
	want := manifestWant(st)
	st.Close()
	if got := manifestFile(t, st); got != want {
		t.Fatalf("Close did not flush the deferred project.json\n got %q\nwant %q", got, want)
	}
}
