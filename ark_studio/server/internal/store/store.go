// Package store is the Go port of arkstudio/store.py (read path, comments)
// and arkstudio/db.py (schema, migrations).  Every function keeps the name
// of its Python original in its comment; key order of every object follows
// the Python dict literal it mirrors.
package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/platform/pyerr"
	"arkstudio/go/internal/sqlitedb"
)

// Error is StoreError: a client error answered with HTTP 400.
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

func storeErr(format string, args ...any) error { return &Error{fmt.Sprintf(format, args...)} }

// Manifest is the shared project.json object (Python shares one dict
// between Project and Store).
type Manifest struct {
	mu  sync.Mutex
	obj *pyjson.Object
}

// NewManifest wraps a decoded project.json object.
func NewManifest(obj *pyjson.Object) *Manifest { return &Manifest{obj: obj} }

// Snapshot returns a shallow copy safe to encode.
func (m *Manifest) Snapshot() *pyjson.Object {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.obj.Copy()
}

// Get reads one key.
func (m *Manifest) Get(key string) any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.obj.Value(key)
}

// Store is one open project database.
type Store struct {
	Path     string // project folder (resolved)
	DB       *sqlitedb.DB
	Manifest *Manifest
	logPath  string

	assignee          atomic.Pointer[assigneeCache]
	creatorBackfilled atomic.Bool
	chatAdopted       atomic.Bool  // AdoptLegacyChat ran (or found its meta key)
	writesSincePrune  atomic.Int64 // Store._writes_since_prune
	backfillMu        sync.Mutex
	snap              snapState
	historyOnce       sync.Once
	closing           chan struct{}
	closeOnce         sync.Once
	manifestDirty     atomic.Bool   // project.json rewrite deferred (touchManifest)
	manifestKick      chan struct{} // wakes manifestFlusher
	signalMu          sync.Mutex    // WriteSignal (updates.go, Go only)
	signal            chan struct{}
	ovMu              sync.Mutex // overview cache (overview.go)
	ovCache           []overviewEntry
}

// Open is Store.__init__: open/migrate ark.sqlite3 and prepare logs/.
func Open(path string, manifest *Manifest, readers int) (*Store, error) {
	db, err := OpenProjectDB(filepath.Join(path, "ark.sqlite3"), readers)
	if err != nil {
		return nil, err
	}
	s := &Store{Path: path, DB: db, Manifest: manifest, logPath: filepath.Join(path, "logs", "activity.jsonl"),
		closing: make(chan struct{}), manifestKick: make(chan struct{}, 1)}
	if err := os.MkdirAll(filepath.Dir(s.logPath), 0o755); err != nil {
		db.Close()
		return nil, err
	}
	if s.PendingTextHistory(context.Background()) {
		s.startHistoryWorker()
	}
	go s.manifestFlusher()
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error {
	s.closeOnce.Do(func() { close(s.closing) })
	s.DB.Locked(context.Background(), s.FlushManifest) //nolint:errcheck // already closed: nothing deferred since
	return s.DB.Close()
}

// PendingTextHistory reports node_text_pending rows (Store.__init__ starts
// the history worker for them).
func (s *Store) PendingTextHistory(ctx context.Context) bool {
	found := false
	s.DB.Read(ctx, func(q sqlitedb.Querier) error { //nolint:errcheck
		_, found, _ = sqlitedb.QueryOne(ctx, q, "SELECT 1 FROM node_text_pending LIMIT 1")
		return nil
	})
	return found
}

func (s *Store) read(ctx context.Context, fn func(q sqlitedb.Querier) error) error {
	return s.DB.Read(ctx, fn)
}

// unitRow is Store._unit_row: digits are an id, anything else a uid.
func unitRow(ctx context.Context, q sqlitedb.Querier, ref string) (sqlitedb.Row, error) {
	var rows []sqlitedb.Row
	var err error
	if pytext.IsDigit(ref) {
		n, ierr := pytext.Int(ref)
		if ierr != nil {
			return sqlitedb.Row{}, &pyerr.ValueError{Message: ierr.Error()}
		}
		rows, err = sqlitedb.Query(ctx, q, "SELECT * FROM units WHERE id=?", n)
	} else {
		rows, err = sqlitedb.Query(ctx, q, "SELECT * FROM units WHERE uid=?", ref)
	}
	if err != nil {
		return sqlitedb.Row{}, err
	}
	if len(rows) == 0 {
		return sqlitedb.Row{}, storeErr("unit not found: %s", ref)
	}
	return rows[0], nil
}

// parseDetail is _parse_detail: a JSON object or {}.
func parseDetail(raw any) *pyjson.Object {
	text, _ := raw.(string)
	if text == "" {
		text = "{}"
	}
	if o, ok := pyjson.LoadsObject(text); ok {
		return o
	}
	return pyjson.NewObject()
}

// unitBrief is _unit_brief.
func unitBrief(r sqlitedb.Row) *pyjson.Object {
	return pyjson.NewObject("id", r.Get("id"), "uid", r.Get("uid"), "type", r.Get("type"),
		"title", r.Get("title"), "status", r.Get("status"), "importance", r.Get("importance"),
		"start_at", r.Get("start_at"), "end_at", r.Get("end_at"))
}

func ids(rows []sqlitedb.Row) []any {
	out := make([]any, len(rows))
	for i, r := range rows {
		out[i] = r.Int("id")
	}
	return out
}

// IsCanceled reports a context error (client went away / timeout).
func IsCanceled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
