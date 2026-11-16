package sqlitedb

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// walSizes runs n commits while readers keep read transactions open back to
// back (as the server's read pool does under load), stops the readers and
// returns the -wal size then and once it stopped shrinking (no more
// commits: the writer's retry timer must do it).
func walSizes(t *testing.T, restartBytes int64, n int) (loaded, quiet int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "w.sqlite3")
	db, err := Open(path, Options{Journal: "WAL", Readers: 4, WALRestartBytes: restartBytes})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Exec("CREATE TABLE t(id INTEGER PRIMARY KEY, v TEXT)"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	payload := strings.Repeat("x", 3000)
	write := func(k int) {
		for i := 0; i < k; i++ {
			if err := db.Write(ctx, func(q Querier) error {
				_, err := q.ExecContext(ctx, "INSERT INTO t(v) VALUES(?)", payload)
				return err
			}, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	size := func() int64 {
		fi, err := os.Stat(path + "-wal")
		if err != nil {
			t.Fatal(err)
		}
		return fi.Size()
	}
	var stop atomic.Bool
	var wg sync.WaitGroup
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				db.Read(ctx, func(q Querier) error { //nolint:errcheck
					_, _, err := QueryOne(ctx, q, "SELECT COUNT(*) n, MAX(id) m FROM t")
					time.Sleep(200 * time.Microsecond)
					return err
				})
			}
		}()
	}
	write(n)
	stop.Store(true)
	wg.Wait()
	loaded = size()
	deadline := time.Now().Add(3 * walRetryAfter)
	for size() > 1<<20 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	return loaded, size()
}

// The read pool always has a transaction open, so under load SQLite's own
// PASSIVE checkpoints never let the WAL start over and it grows with every
// commit (the 1,500-user load test reached 419 MB for a 5.6 MB project);
// without help it then keeps that size.  The writer's non-blocking TRUNCATE
// checkpoint gives the file back at the first quiet moment.
func TestWALTruncatedAfterLoad(t *testing.T) {
	const writes = 1500
	before := walStats.Checkpoints.Load()
	loaded, quiet := walSizes(t, 512<<10, writes)
	if walStats.Checkpoints.Load() == before {
		t.Fatal("no checkpoint attempt")
	}
	if quiet > 1<<20 {
		t.Fatalf("WAL after the readers stopped is %d bytes (%d under load), want < 1 MiB", quiet, loaded)
	}
	offLoaded, offQuiet := walSizes(t, -1, writes)
	t.Logf("WAL after %d commits under reads / once quiet: %d / %d bytes with truncation, %d / %d without",
		writes, loaded, quiet, offLoaded, offQuiet)
	if offQuiet < 4<<20 {
		t.Logf("SQLite shrank the WAL on its own this time")
	}
}
