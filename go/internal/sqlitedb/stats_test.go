package sqlitedb

import (
	"context"
	"encoding/json"
	"errors"
	"expvar"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type codedErr int

func (e codedErr) Error() string { return fmt.Sprintf("sqlite error %d", int(e)) }
func (e codedErr) Code() int     { return int(e) }

func TestErrClass(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{nil, 0},
		{errors.New("no such table: x"), 0},
		{codedErr(5), 5},
		{codedErr(5 | 2<<8), 5}, // SQLITE_BUSY_SNAPSHOT
		{codedErr(6), 6},
		{fmt.Errorf("commit: %w", codedErr(261)), 5}, // SQLITE_BUSY_RECOVERY, wrapped
		{errors.New("database is locked (5) (SQLITE_BUSY)"), 5},
		{errors.New("database table is locked"), 6},
	}
	for _, c := range cases {
		if got := errClass(c.err); got != c.want {
			t.Errorf("errClass(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

func TestWriterStats(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "s.sqlite3"), Options{Journal: "WAL"})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := StatsFor("WAL")
	if db.stats != s {
		t.Fatal("project databases must use the WAL counters")
	}
	if err := db.Exec("CREATE TABLE t(x)"); err != nil {
		t.Fatal(err)
	}
	histSum := func() (n uint64) {
		for i := range s.hist {
			n += s.hist[i].Load()
		}
		return n
	}
	writes0, queued0, hist0, cancelled0 := s.Writes.Load(), s.Queued.Load(), histSum(), s.WaitCancelled.Load()
	busy0 := s.Busy.Load()

	// Writers that hold the slot for a while: all but the first must queue.
	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := db.Write(context.Background(), func(q Querier) error {
				time.Sleep(5 * time.Millisecond)
				_, err := q.ExecContext(context.Background(), "INSERT INTO t VALUES(?)", i)
				return err
			}, nil)
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if got := s.Writes.Load() - writes0; got != n {
		t.Errorf("writes = %d, want %d", got, n)
	}
	if got := histSum() - hist0; got != n {
		t.Errorf("histogram total = %d, want %d", got, n)
	}
	if s.Queued.Load()-queued0 == 0 || s.DepthMax.Load() < 2 {
		t.Errorf("queued = %d, depth max = %d: concurrent writers must queue", s.Queued.Load()-queued0, s.DepthMax.Load())
	}
	if s.WaitMaxNanos.Load() < int64(4*time.Millisecond) || s.HoldMaxNanos.Load() < int64(4*time.Millisecond) {
		t.Errorf("wait max %d ns, hold max %d ns: expected at least one 5 ms hold and wait", s.WaitMaxNanos.Load(), s.HoldMaxNanos.Load())
	}
	if d := s.Depth.Load(); d != 0 {
		t.Errorf("depth after all writes = %d, want 0", d)
	}

	// A queued write whose context ends is counted and leaves the queue.
	hold := make(chan struct{})
	held := make(chan struct{})
	go db.Write(context.Background(), func(Querier) error { close(held); <-hold; return nil }, nil) //nolint:errcheck
	<-held
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	if err := db.Write(ctx, func(Querier) error { return nil }, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("queued write: err = %v, want deadline", err)
	}
	cancel()
	close(hold)
	if got := s.WaitCancelled.Load() - cancelled0; got != 1 {
		t.Errorf("wait_cancelled = %d, want 1", got)
	}

	// A busy error returned through a write is counted.
	if err := db.Write(context.Background(), func(Querier) error { return codedErr(5) }, nil); err == nil {
		t.Fatal("want the error back")
	}
	if got := s.Busy.Load() - busy0; got != 1 {
		t.Errorf("busy = %d, want 1", got)
	}

	// The expvar value is JSON with both classes and the read pool.
	v := expvar.Get("ark_sqlite")
	if v == nil {
		t.Fatal("ark_sqlite is not published")
	}
	var snap map[string]struct {
		Databases int       `json:"databases"`
		Writes    uint64    `json:"writes"`
		Hist      []uint64  `json:"wait_hist"`
		Bounds    []float64 `json:"wait_hist_le_ms"`
		ReadPool  struct {
			Open int `json:"open"`
		} `json:"read_pool"`
	}
	if err := json.Unmarshal([]byte(v.String()), &snap); err != nil {
		t.Fatal(err, v.String())
	}
	wal, ok := snap["wal"]
	if _, okDel := snap["delete"]; !ok || !okDel {
		t.Fatalf("classes missing: %s", v.String())
	}
	if wal.Databases < 1 || wal.Writes < n || len(wal.Hist) != len(wal.Bounds)+1 {
		t.Errorf("unexpected wal snapshot: %+v", wal)
	}
}
