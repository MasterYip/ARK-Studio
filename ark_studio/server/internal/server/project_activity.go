package server

// Go-only (T-0034): GET /api/projects carries, per project, how active the
// signed-in person has been there, so the project picker can rank projects
// by personal use.  tests/python/contract/go_only.py AUGMENTED
// "/api/projects" strips the two members before the replay compares bodies.
//
//   my_activity       Σ 0.5^(age / 7 days) over the person's own activity
//                     entries of the last 30 days (a recent edit counts 1, a
//                     week-old one 0.5, ...), rounded to 0.01; 0 when none.
//   my_last_active_at the newest of those entries' "at" (also older than 30
//                     days when it is among the scanned rows), or null.
//
// Cost: one bounded query per project (the newest activityScanRows rows of
// the activity table, through its "at" index), cached per (database, actor)
// and reused until the database files change or activityTTL passes.  The
// score itself is recomputed from the cached timestamps on every request,
// so it decays without a new query.  A project whose database has not
// changed in activityWindow is not opened at all.

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/sqlitedb"
)

const (
	activityWindow   = 30 * 24 * time.Hour
	activityHalfLife = 7 * 24 * time.Hour
	activityScanRows = 5000 // newest activity rows looked at per project
	activityKeep     = 500  // newest own entries kept per project
	activityTTL      = 2 * time.Minute
	activityWorkers  = 4
	activityMaxCache = 4096 // (project, actor) pairs before the cache is reset
)

type activityKey struct{ db, actor string }

type activityEntry struct {
	sig     string      // database + WAL size and mtime when read
	read    time.Time   // wall-clock time of the read
	times   []time.Time // own entries, newest first (≤ activityKeep)
	last    string      // newest own "at" as stored
	skipped bool        // database unchanged for activityWindow: not opened
}

// projectActivity caches the per-project scans; the zero value is ready.
type projectActivity struct {
	mu      sync.Mutex
	entries map[activityKey]*activityEntry
}

func (a *projectActivity) get(k activityKey) *activityEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.entries[k]
}

func (a *projectActivity) put(k activityKey, e *activityEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.entries == nil || len(a.entries) >= activityMaxCache {
		a.entries = map[activityKey]*activityEntry{}
	}
	a.entries[k] = e
}

// dbSignature is the database and WAL files' size and mtime, and the newest
// of the mtimes; ok=false when the database file does not exist.
func dbSignature(db string) (sig string, newest time.Time, ok bool) {
	fi, err := os.Stat(db)
	if err != nil || fi.IsDir() {
		return "", time.Time{}, false
	}
	sig = fmt.Sprintf("%d:%d", fi.Size(), fi.ModTime().UnixNano())
	newest = fi.ModTime()
	if wal, err := os.Stat(db + "-wal"); err == nil {
		sig += fmt.Sprintf("|%d:%d", wal.Size(), wal.ModTime().UnixNano())
		if wal.ModTime().After(newest) {
			newest = wal.ModTime()
		}
	}
	return sig, newest, true
}

// activityScanSQL reads the actor's entries among the newest scanned rows.
const activityScanSQL = "SELECT at FROM (SELECT at, actor FROM activity ORDER BY at DESC LIMIT ?) " +
	"WHERE actor = ? ORDER BY at DESC LIMIT ?"

func scanActivity(ctx context.Context, q sqlitedb.Querier, actor string) ([]string, error) {
	rows, err := sqlitedb.Query(ctx, q, activityScanSQL, activityScanRows, actor, activityKeep)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Str("at"))
	}
	return out, nil
}

// readActivity runs the scan on the open project's read pool, or on a
// short-lived read-only connection when the project is not open.
func (a *App) readActivity(ctx context.Context, folder, db, actor string) ([]string, error) {
	if p := a.Projects.Cached(folder); p != nil && p.Store != nil {
		var out []string
		err := p.Store.DB.Read(ctx, func(q sqlitedb.Querier) error {
			var err error
			out, err = scanActivity(ctx, q, actor)
			return err
		})
		return out, err
	}
	v := url.Values{}
	v.Add("mode", "ro")
	v.Add("_pragma", "busy_timeout(2000)")
	v.Add("_pragma", "query_only(1)")
	conn, err := sql.Open("sqlite", "file:"+filepath.ToSlash(db)+"?"+v.Encode())
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	return scanActivity(ctx, conn, actor)
}

func parseActivityAt(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// activityFor is the (cached) scan of one project database for actor.
func (a *App) activityFor(ctx context.Context, folder, actor string, now time.Time) *activityEntry {
	db := filepath.Join(folder, "ark.sqlite3")
	key := activityKey{db, actor}
	sig, newest, ok := dbSignature(db)
	if !ok {
		return nil
	}
	cached := a.activity.get(key)
	if cached != nil && cached.sig == sig && time.Since(cached.read) < activityTTL {
		return cached
	}
	entry := &activityEntry{sig: sig, read: time.Now()}
	if now.Sub(newest) > activityWindow {
		entry.skipped = true
		a.activity.put(key, entry)
		return entry
	}
	stamps, err := a.readActivity(ctx, folder, db, actor)
	if err != nil {
		return cached // keep the last good figures (nil: none)
	}
	for _, s := range stamps {
		if t, ok := parseActivityAt(s); ok {
			entry.times = append(entry.times, t)
			if entry.last == "" {
				entry.last = s
			}
		}
	}
	a.activity.put(key, entry)
	return entry
}

// activityScore is Σ 0.5^(age/half-life) over entries of the last window
// (future stamps count as now), rounded to 0.01.
func activityScore(times []time.Time, now time.Time) float64 {
	score := 0.0
	for _, t := range times {
		age := now.Sub(t)
		if age < 0 {
			age = 0
		}
		if age > activityWindow {
			continue
		}
		score += math.Pow(0.5, float64(age)/float64(activityHalfLife))
	}
	return math.Round(score*100) / 100
}

// withMyActivity sets my_activity and my_last_active_at on every listed
// project (pyjson objects with a "path").
func (c *reqCtx) withMyActivity(projects []any) {
	actor := c.actor()
	if actor == "" || len(projects) == 0 {
		return
	}
	now := clock.Now()
	entries := make([]*activityEntry, len(projects))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < min(activityWorkers, len(projects)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				item, ok := projects[i].(*pyjson.Object)
				if !ok {
					continue
				}
				if folder := pyjson.Str(item.Value("path")); folder != "" {
					entries[i] = c.app.activityFor(c.ctx, folder, actor, now)
				}
			}
		}()
	}
	for i := range projects {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	for i, raw := range projects {
		item, ok := raw.(*pyjson.Object)
		if !ok {
			continue
		}
		score, last := 0.0, any(nil)
		if e := entries[i]; e != nil {
			score = activityScore(e.times, now)
			if e.last != "" {
				last = e.last
			}
		}
		item.Set("my_activity", score).Set("my_last_active_at", last)
	}
}
