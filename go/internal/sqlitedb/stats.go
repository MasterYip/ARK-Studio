package sqlitedb

// Writer-queue and lock-error counters, published with expvar as
// "ark_sqlite".  The server serves http.DefaultServeMux (and so /debug/vars)
// only on the opt-in loopback ARK_PPROF listener; go/cmd/ark-load reads it
// with --stats-url.  Counters are process-wide, one set per journal mode
// ("wal" = project databases, "delete" = the organization database).

import (
	"context"
	"errors"
	"expvar"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// WaitBucketsMS are the upper bounds (ms) of the writer-wait histogram; one
// more bucket counts everything above the last bound.
var WaitBucketsMS = [...]float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 25, 50, 75, 100, 150, 200, 300, 400, 500,
	750, 1000, 1500, 2500, 5000, 10000, 30000}

// WriterStats counts one class of databases.  All fields are atomic.
type WriterStats struct {
	Writes         atomic.Uint64 // writer acquisitions (Write and Exec)
	Queued         atomic.Uint64 // acquisitions that found the writer taken and waited
	WaitCancelled  atomic.Uint64 // waits that ended with the request context
	WaitNanos      atomic.Int64  // total queue wait of all acquisitions
	WaitMaxNanos   atomic.Int64
	HoldNanos      atomic.Int64 // total time the writer was held
	HoldMaxNanos   atomic.Int64
	Depth          atomic.Int64 // holders + waiters right now
	DepthMax       atomic.Int64
	Busy           atomic.Uint64 // SQLITE_BUSY ("database is locked") errors, reads and writes
	Locked         atomic.Uint64 // SQLITE_LOCKED ("database table is locked") errors
	Checkpoints    atomic.Uint64 // TRUNCATE checkpoint attempts (maybeRestartWAL)
	CheckpointBusy atomic.Uint64 // of those, ones that found a reader still in the WAL
	WALMaxBytes    atomic.Int64  // largest -wal size seen by a check
	hist           [len(WaitBucketsMS) + 1]atomic.Uint64
}

var (
	walStats    WriterStats
	deleteStats WriterStats

	openMu sync.Mutex
	open   = map[*DB]struct{}{}
)

// StatsFor returns the counters of a journal mode ("WAL" or "DELETE").
func StatsFor(journal string) *WriterStats {
	if strings.EqualFold(journal, "DELETE") {
		return &deleteStats
	}
	return &walStats
}

func init() {
	expvar.Publish("ark_sqlite", expvar.Func(func() any { return Snapshot() }))
}

func storeMax(v *atomic.Int64, x int64) {
	for {
		cur := v.Load()
		if x <= cur || v.CompareAndSwap(cur, x) {
			return
		}
	}
}

func (s *WriterStats) observeWait(d time.Duration) {
	n := d.Nanoseconds()
	s.WaitNanos.Add(n)
	storeMax(&s.WaitMaxNanos, n)
	ms := float64(n) / 1e6
	i := 0
	for i < len(WaitBucketsMS) && ms > WaitBucketsMS[i] {
		i++
	}
	s.hist[i].Add(1)
}

// countErr records SQLITE_BUSY / SQLITE_LOCKED in err (nil is fine).
func (s *WriterStats) countErr(err error) {
	switch errClass(err) {
	case 5:
		s.Busy.Add(1)
	case 6:
		s.Locked.Add(1)
	}
}

// errClass is the primary SQLite result code 5 (BUSY) or 6 (LOCKED), else 0.
func errClass(err error) int {
	if err == nil {
		return 0
	}
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		if c := coded.Code() & 0xff; c == 5 || c == 6 {
			return c
		}
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "SQLITE_BUSY") || strings.Contains(msg, "database is locked"):
		return 5
	case strings.Contains(msg, "SQLITE_LOCKED") || strings.Contains(msg, "database table is locked"):
		return 6
	}
	return 0
}

// acquire takes the writer slot, counting the wait.
func (db *DB) acquire(ctx context.Context) error {
	s := db.stats
	storeMax(&s.DepthMax, s.Depth.Add(1))
	t0 := time.Now()
	select {
	case db.slot <- struct{}{}:
	default:
		s.Queued.Add(1)
		select {
		case db.slot <- struct{}{}:
		case <-ctx.Done():
			s.Depth.Add(-1)
			s.WaitCancelled.Add(1)
			return ctx.Err()
		}
	}
	now := time.Now()
	s.observeWait(now.Sub(t0))
	s.Writes.Add(1)
	db.heldAt = now
	return nil
}

// release frees the writer slot taken by acquire.
func (db *DB) release() {
	s := db.stats
	hold := time.Since(db.heldAt).Nanoseconds()
	s.HoldNanos.Add(hold)
	storeMax(&s.HoldMaxNanos, hold)
	<-db.slot
	s.Depth.Add(-1)
}

func nsToMS(n int64) float64 { return float64(n) / 1e6 }

// Snapshot is the "ark_sqlite" expvar value: per class the writer counters,
// the wait histogram and the read pools' database/sql wait statistics.
func Snapshot() map[string]any {
	type pool struct {
		dbs, waits, inUse, openConns int64
		wait                         time.Duration
	}
	pools := map[*WriterStats]*pool{&walStats: {}, &deleteStats: {}}
	openMu.Lock()
	for db := range open {
		p := pools[db.stats]
		st := db.reader.Stats()
		p.dbs++
		p.waits += st.WaitCount
		p.wait += st.WaitDuration
		p.inUse += int64(st.InUse)
		p.openConns += int64(st.OpenConnections)
	}
	openMu.Unlock()
	out := map[string]any{}
	for name, s := range map[string]*WriterStats{"wal": &walStats, "delete": &deleteStats} {
		hist := make([]uint64, len(s.hist))
		for i := range s.hist {
			hist[i] = s.hist[i].Load()
		}
		p := pools[s]
		out[name] = map[string]any{
			"databases": p.dbs, "writes": s.Writes.Load(), "queued": s.Queued.Load(),
			"wait_cancelled": s.WaitCancelled.Load(), "wait_total_ms": nsToMS(s.WaitNanos.Load()),
			"wait_max_ms": nsToMS(s.WaitMaxNanos.Load()), "hold_total_ms": nsToMS(s.HoldNanos.Load()),
			"hold_max_ms": nsToMS(s.HoldMaxNanos.Load()), "depth": s.Depth.Load(), "depth_max": s.DepthMax.Load(),
			"busy_errors": s.Busy.Load(), "locked_errors": s.Locked.Load(),
			"wal_checkpoints": s.Checkpoints.Load(), "wal_checkpoint_busy": s.CheckpointBusy.Load(),
			"wal_max_bytes":   s.WALMaxBytes.Load(),
			"wait_hist_le_ms": WaitBucketsMS[:], "wait_hist": hist,
			"read_pool": map[string]any{"wait_count": p.waits, "wait_total_ms": nsToMS(p.wait.Nanoseconds()),
				"in_use": p.inUse, "open": p.openConns},
		}
	}
	return out
}
