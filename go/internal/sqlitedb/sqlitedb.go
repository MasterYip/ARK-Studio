// Package sqlitedb wraps one SQLite file for many concurrent requests.
//
// Concurrency model (docs/development/go-port/concurrency.md):
//
//   - exactly one writer connection; writers queue on a 1-slot semaphore that
//     honors the request context, so a stuck write cannot pile up goroutines
//     forever and a cancelled request leaves the queue;
//   - a pool of read connections (query_only) for WAL databases; every
//     multi-statement read runs in one read transaction, so it sees a single
//     snapshot just like Python's one-connection-plus-lock store did;
//   - busy_timeout on every connection, so the rare cross-process lock (the
//     Python CLI, a backup) waits instead of failing.
//
// Pure Go driver (modernc.org/sqlite): no cgo, so cross-compiling needs no C
// toolchain.
package sqlitedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// Options for Open.
type Options struct {
	// Journal is the journal_mode the Python code uses for this file: "WAL"
	// for projects, "DELETE" for the organization database.  It is never
	// changed to something Python would not set.
	Journal string
	// Readers is the read-pool size (WAL only; DELETE mode reads use the
	// same small pool, SQLite still allows concurrent shared locks).
	Readers int
	// BusyTimeoutMS matches sqlite3.connect(timeout=30) by default.
	BusyTimeoutMS int
	// ForeignKeys turns on PRAGMA foreign_keys like both Python stores.
	ForeignKeys bool
	// MmapBytes and CacheKiB tune reads (0 = SQLite defaults; the server
	// sets only CacheKiB, see dsn).
	MmapBytes int64
	CacheKiB  int
	// WALRestartBytes (WAL only): when the -wal file has grown past this
	// size the writer tries a TRUNCATE checkpoint (default 16 MiB, <0 off).
	// See maybeRestartWAL.
	WALRestartBytes int64
}

// walCheckEvery is how many commits pass between two -wal size checks.
const walCheckEvery = 64

// walRestartBusyMS is how long a forced checkpoint waits for readers that
// still need old WAL frames: not at all.  It runs holding the writer, and
// under sustained load some read is always older than any short wait (a
// 100 ms wait failed 102 of 108 times in the 600-user run and cost ~10 s
// of writer time).  The attempt then only succeeds in a quiet moment, which
// real traffic has; it is retried every walCheckEvery commits.
const walRestartBusyMS = 0

// walRetryAfter is the delay before a failed attempt is retried without a
// commit to trigger it (the WAL must not keep its peak size once the load
// is over).
const walRetryAfter = time.Second

// DB is one SQLite file with a single writer and a read pool.
type DB struct {
	Path     string
	writer   *sql.DB
	reader   *sql.DB
	slot     chan struct{}
	writes   atomic.Uint64
	closing  atomic.Bool
	stats    *WriterStats // StatsFor(Journal), see stats.go
	walMax   int64        // WALRestartBytes (0 = not WAL or off)
	busyMS   int          // the connections' busy_timeout
	walRetry bool         // a maybeRestartWAL retry is scheduled (guarded by slot)
	commits  uint64       // commits since open (guarded by slot)
	heldAt   time.Time    // when the writer slot was taken (guarded by slot)

	watchMu sync.Mutex
	watch   *sql.DB // one connection that only reads PRAGMA data_version
}

// ErrClosed is returned after Close.
var ErrClosed = errors.New("sqlitedb: closed")

func dsn(path string, opts Options, readOnly bool) string {
	q := url.Values{}
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", opts.BusyTimeoutMS))
	if opts.ForeignKeys {
		q.Add("_pragma", "foreign_keys(1)")
	}
	q.Add("_pragma", "synchronous(NORMAL)")
	// Reads: a memory map on the read-only connections (they then share the
	// OS page cache instead of each warming a private one) plus a larger
	// page cache.  With SQLite's defaults a full scan of a 10k-unit project
	// re-reads pages through a read syscall each time, expensive on Windows.
	// The writer stays unmapped: on Windows a file with mapped views cannot
	// be truncated, and only the writer ever truncates (VACUUM/auto-vacuum,
	// which ARK does not use; the WAL file is never mapped).  Neither pragma
	// changes any query result.
	if opts.MmapBytes > 0 && readOnly {
		q.Add("_pragma", fmt.Sprintf("mmap_size(%d)", opts.MmapBytes))
	}
	if opts.CacheKiB > 0 {
		q.Add("_pragma", fmt.Sprintf("cache_size(-%d)", opts.CacheKiB))
	}
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Add("_pragma", "journal_mode("+opts.Journal+")")
	}
	// Plain TEXT columns must come back as strings: never parse them as time.
	q.Add("_time_format", "sqlite")
	return "file:" + filepath.ToSlash(path) + "?" + q.Encode()
}

// Open opens path with the writer first (which applies journal_mode), then
// the read pool.
func Open(path string, opts Options) (*DB, error) {
	if opts.BusyTimeoutMS == 0 {
		opts.BusyTimeoutMS = 30000
	}
	if opts.Readers <= 0 {
		opts.Readers = 4
	}
	if opts.Journal == "" {
		opts.Journal = "WAL"
	}
	writer, err := sql.Open("sqlite", dsn(path, opts, false))
	if err != nil {
		return nil, err
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)
	writer.SetConnMaxLifetime(0)
	if err := writer.Ping(); err != nil {
		writer.Close()
		return nil, err
	}
	reader, err := sql.Open("sqlite", dsn(path, opts, true))
	if err != nil {
		writer.Close()
		return nil, err
	}
	reader.SetMaxOpenConns(opts.Readers)
	reader.SetMaxIdleConns(opts.Readers)
	watch, err := sql.Open("sqlite", dsn(path, opts, true))
	if err != nil {
		writer.Close()
		reader.Close()
		return nil, err
	}
	watch.SetMaxOpenConns(1)
	watch.SetMaxIdleConns(1)
	db := &DB{busyMS: opts.BusyTimeoutMS, Path: path, writer: writer, reader: reader, watch: watch, slot: make(chan struct{}, 1),
		stats: StatsFor(opts.Journal)}
	if strings.EqualFold(opts.Journal, "WAL") {
		db.walMax = opts.WALRestartBytes
		if db.walMax == 0 {
			db.walMax = 16 << 20
		} else if db.walMax < 0 {
			db.walMax = 0
		}
	}
	openMu.Lock()
	open[db] = struct{}{}
	openMu.Unlock()
	return db, nil
}

// Close closes both pools.
func (db *DB) Close() error {
	db.closing.Store(true)
	openMu.Lock()
	delete(open, db)
	openMu.Unlock()
	err1 := db.reader.Close()
	err2 := db.writer.Close()
	err3 := db.watch.Close()
	return errors.Join(err1, err2, err3)
}

// DataVersion is PRAGMA data_version on a dedicated connection that never
// writes: it changes whenever any other connection (this process's writer,
// or another process) commits.  Values are only meaningful compared with
// earlier values from this same method.
func (db *DB) DataVersion(ctx context.Context) (int64, error) {
	db.watchMu.Lock()
	defer db.watchMu.Unlock()
	var v int64
	err := db.watch.QueryRowContext(ctx, "PRAGMA data_version").Scan(&v)
	return v, err
}

// WriteGeneration increases after every committed write by this process.
func (db *DB) WriteGeneration() uint64 { return db.writes.Load() }

// Read runs fn inside one read-only transaction (one WAL snapshot).
func (db *DB) Read(ctx context.Context, fn func(q Querier) error) error {
	if db.closing.Load() {
		return ErrClosed
	}
	tx, err := db.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		db.stats.countErr(err)
		return err
	}
	defer tx.Rollback() //nolint:errcheck // read-only
	if err := fn(tx); err != nil {
		db.stats.countErr(err)
		return err
	}
	err = tx.Commit()
	db.stats.countErr(err)
	return err
}

// Write runs fn in a write transaction on the single writer connection.
// Callers queue for the writer; ctx cancels the wait.  after runs (still
// holding the writer) once the commit succeeded, for file mirrors such as
// logs/activity.jsonl and project.json that must stay in commit order.
func (db *DB) Write(ctx context.Context, fn func(q Querier) error, after func()) error {
	if db.closing.Load() {
		return ErrClosed
	}
	if err := db.acquire(ctx); err != nil {
		return err
	}
	defer db.release()
	// Not BeginTx(ctx): a write that has started must not be rolled back half
	// way by a client disconnect; the queue wait above is what ctx bounds.
	tx, err := db.writer.Begin()
	if err != nil {
		db.stats.countErr(err)
		return err
	}
	if err := fn(tx); err != nil {
		db.stats.countErr(err)
		tx.Rollback() //nolint:errcheck
		return err
	}
	if err := tx.Commit(); err != nil {
		db.stats.countErr(err)
		return err
	}
	if after != nil {
		after()
	}
	// Bump only after the file mirrors (project.json, activity.jsonl) are
	// written: caches keyed on the generation then never pair new database
	// state with an old manifest.
	db.writes.Add(1)
	db.commits++
	if db.walMax > 0 && db.commits%walCheckEvery == 0 {
		db.maybeRestartWAL()
	}
	return nil
}

// maybeRestartWAL gives the -wal file back after a load spike.  SQLite's
// automatic checkpoint (PASSIVE, every 1000 pages) copies frames back into
// the database, but the WAL only starts over from its beginning when no
// reader still uses it, and the file is never shrunk (journal_size_limit
// is unset, as in Python).  With a read pool that always has a transaction
// open the WAL grows for as long as the load lasts (419 MB for a 5.6 MB
// project after 90 s of 1,500 users) and then stays that size.  Python's
// single connection never has a reader open while it writes, so its WAL
// stays small.  A TRUNCATE checkpoint succeeds once no reader still needs
// the old frames and resets the file to zero bytes; the database content is
// unchanged.  Runs holding the writer and never waits (walRestartBusyMS),
// so under sustained load it fails cheaply; it is retried every
// walCheckEvery commits and, when writes stop, every walRetryAfter until a
// quiet moment.
func (db *DB) maybeRestartWAL() {
	fi, err := os.Stat(db.Path + "-wal")
	if err != nil || fi.Size() < db.walMax {
		return
	}
	s := db.stats
	ctx := context.Background()
	conn, err := db.writer.Conn(ctx)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout=%d", walRestartBusyMS)) //nolint:errcheck
	var busy, logFrames, done int64
	err = conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &done)
	conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout=%d", db.busyMS)) //nolint:errcheck
	s.Checkpoints.Add(1)
	if err != nil || busy != 0 {
		s.CheckpointBusy.Add(1)
		if !db.walRetry {
			db.walRetry = true
			time.AfterFunc(walRetryAfter, func() {
				db.Locked(context.Background(), func() { //nolint:errcheck // closed: nothing to do
					db.walRetry = false
					db.maybeRestartWAL()
				})
			})
		}
	}
	storeMax(&s.WALMaxBytes, fi.Size())
}

// Waiting reports whether other callers are queued for the writer.  Only
// meaningful while holding it (inside Write's fn or after).
func (db *DB) Waiting() bool { return db.stats.Depth.Load() > 1 }

// Locked runs fn holding the writer slot, outside a transaction (deferred
// file mirrors that must not interleave with writes).
func (db *DB) Locked(ctx context.Context, fn func()) error {
	if db.closing.Load() {
		return ErrClosed
	}
	if err := db.acquire(ctx); err != nil {
		return err
	}
	defer db.release()
	fn()
	return nil
}

// WriterRead runs fn in a read transaction on the writer connection.  Only
// for callers that hold the writer (Write's after callback): its page cache
// is warm and current, where a read-pool connection would first wait for a
// free connection and then reload every page the write invalidated, all
// while the writer queue waits.
func (db *DB) WriterRead(ctx context.Context, fn func(q Querier) error) error {
	tx, err := db.writer.Begin()
	if err != nil {
		db.stats.countErr(err)
		return err
	}
	defer tx.Rollback() //nolint:errcheck // read only
	err = fn(tx)
	db.stats.countErr(err)
	return err
}

// Exec runs statements outside a transaction on the writer (schema setup).
func (db *DB) Exec(script string) error {
	db.acquire(context.Background()) //nolint:errcheck // never cancelled
	defer db.release()
	_, err := db.writer.Exec(script)
	db.stats.countErr(err)
	return err
}

// Querier is satisfied by *sql.Tx.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Row is one result row with Python sqlite3 value types: int64, float64,
// string, []byte or nil, plus the column order.
type Row struct {
	Cols []string
	Vals []any
}

// Get returns a column value by name (nil when absent).
func (r Row) Get(name string) any {
	for i, c := range r.Cols {
		if c == name {
			return r.Vals[i]
		}
	}
	return nil
}

// Str returns a TEXT column ("" for NULL).
func (r Row) Str(name string) string {
	s, _ := r.Get(name).(string)
	return s
}

// Int returns an INTEGER column (0 for NULL).
func (r Row) Int(name string) int64 {
	switch v := r.Get(name).(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	}
	return 0
}

// Query returns every row of a statement as Rows.
func Query(ctx context.Context, q Querier, query string, args ...any) ([]Row, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []Row
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		for i, v := range vals {
			if b, ok := v.([]byte); ok && b != nil {
				vals[i] = append([]byte(nil), b...)
			}
		}
		out = append(out, Row{Cols: cols, Vals: vals})
	}
	return out, rows.Err()
}

// QueryOne returns the first row or (Row{}, false).
func QueryOne(ctx context.Context, q Querier, query string, args ...any) (Row, bool, error) {
	rows, err := Query(ctx, q, query, args...)
	if err != nil || len(rows) == 0 {
		return Row{}, false, err
	}
	return rows[0], true, nil
}

// Marks is ",".join("?" * n).
func Marks(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

// Args converts a typed slice to []any for variadic query arguments.
func Args[T any](items []T) []any {
	out := make([]any, len(items))
	for i, v := range items {
		out[i] = v
	}
	return out
}
