package store

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/sqlitedb"
)

// Incremental graph snapshot.
//
// GraphSnapshotEncoded returns exactly pyjson.Dumps(GraphSnapshot()).  It
// keeps the encoded JSON of every node from the last build.  Writes made by
// this process log the units they touched (noteWrite); a comment write
// changes only its unit's comment counts, so the next snapshot re-encodes
// just those nodes and splices them in.
//
// The cache is used only when the logged writes form an unbroken chain from
// the state the cache was built at to the current state.  States are named by
// dbStamp, the database part of Store.revision() (Python's own cross-client
// change token: counts and maxima of units, links, media, comments, activity
// and topic members; every Python write logs an activity row).  Each logged
// write records the stamp it started from and the stamp it produced.  Any gap
// (another process wrote in between) or a write that did not report its
// units forces a full rebuild.
//
// Locking: noteWrite runs inside a write (holding the database writer), so it
// takes only logMu, which is never held across a query.  Builders serialize
// on buildMu.

type snapCache struct {
	order []int64            // node ids, snapshot order
	frags map[int64]nodeFrag // encoded node objects
	edges []byte             // encoded edges list
}

// nodeFrag is one encoded snapshot node: the hydrated object without its
// closing brace, then "search_body" (only with bodies) and "body_truncated".
type nodeFrag struct {
	prefix    []byte
	body      []byte
	truncated bool
}

type loggedWrite struct {
	seq           int64
	before, after string
	units         []int64 // nil = anything may have changed
}

type snapLog struct {
	exists  bool          // a cache was built
	stamp   string        // state of the cache
	seq     int64         // increases with every noteWrite
	pending int           // writes between beginWrite and noteWrite
	writes  []loggedWrite // logged writes not yet folded into the cache
}

type snapState struct {
	buildMu sync.Mutex // one snapshot builder at a time; guards cache
	cache   snapCache
	logMu   sync.Mutex // guards log; never held across a query
	log     snapLog
}

// SnapshotStats counts how snapshots were served.
type SnapshotStats struct {
	Hits, Patches, Rebuilds, Gaps atomic.Int64
}

// Stats are process-wide counters, printed with ARK_DEBUG.
var Stats SnapshotStats

const maxLoggedWrites = 10000

// stamp names a database state: PRAGMA data_version on the watcher
// connection (changes with any commit, by anyone) plus dbStamp.
func (s *Store) stamp(ctx context.Context, q sqlitedb.Querier) (string, error) {
	dv, err := s.DB.DataVersion(ctx)
	if err != nil {
		return "", err
	}
	rev, err := dbStamp(ctx, q)
	if err != nil {
		return "", err
	}
	return pyjson.Str(dv) + "|" + rev, nil
}

// dbStamp is the database part of Store.revision() within one read, plus
// the total comment count and newest comment id.
func dbStamp(ctx context.Context, q sqlitedb.Querier) (string, error) {
	r, _, err := sqlitedb.QueryOne(ctx, q, "SELECT (SELECT COUNT(*) FROM units) units, "+
		"(SELECT COALESCE(MAX(updated_at),'') FROM units) unit_time, "+
		"(SELECT COUNT(*) FROM unit_links) links, "+
		"(SELECT COALESCE(MAX(id),0) FROM unit_links) link_id, "+
		"(SELECT COUNT(*) FROM unit_media) media, "+
		"(SELECT COUNT(*) FROM unit_comments WHERE resolved_at IS NULL) comments_open, "+
		"(SELECT COALESCE(MAX(id),0) FROM activity) activity, "+
		"(SELECT COUNT(*) FROM topic_members) members, "+
		"(SELECT COALESCE(MAX(updated_at),'') FROM topic_members) member_time, "+
		"(SELECT COUNT(*) FROM unit_comments) comments, "+
		"(SELECT COALESCE(MAX(id),0) FROM unit_comments) comment_id, "+
		"(SELECT COALESCE(MAX(n),0) FROM change_counter) changes")
	if err != nil {
		return "", err
	}
	parts := make([]string, len(r.Vals))
	for i, v := range r.Vals {
		parts[i] = pyjson.Str(v)
	}
	return strings.Join(parts, ":"), nil
}

// beginWrite is called inside a write transaction (holding the writer)
// before anything is modified: the stamp the write starts from.
func (s *Store) beginWrite(ctx context.Context, q sqlitedb.Querier) string {
	s.snap.logMu.Lock()
	s.snap.log.pending++
	s.snap.logMu.Unlock()
	stamp, err := s.stamp(ctx, q)
	if err != nil {
		return "" // never matches: the write breaks the chain
	}
	return stamp
}

// writeFailed undoes beginWrite for a write that did not commit.
func (s *Store) writeFailed() {
	s.snap.logMu.Lock()
	if s.snap.log.pending > 0 {
		s.snap.log.pending--
	}
	s.snap.logMu.Unlock()
}

// noteWrite is called after a commit by this process (still holding the
// writer).  units == nil means "anything may have changed".
func (s *Store) noteWrite(ctx context.Context, before string, units []int64) {
	after := ""
	if err := s.DB.WriterRead(ctx, func(q sqlitedb.Querier) error {
		var err error
		after, err = s.stamp(ctx, q)
		return err
	}); err != nil {
		after = ""
	}
	defer s.signalWrite() // wake long polls (updates.go, Go only)
	l := &s.snap.log
	s.snap.logMu.Lock()
	defer s.snap.logMu.Unlock()
	if l.pending > 0 {
		l.pending--
	}
	l.seq++
	if len(l.writes) >= maxLoggedWrites {
		l.exists = false
		l.writes = nil
	}
	l.writes = append(l.writes, loggedWrite{seq: l.seq, before: before, after: after, units: units})
}

// chain checks that writes lead from stamp to current without a gap and
// returns the units they touched.
func chain(stamp, current string, writes []loggedWrite) ([]any, bool) {
	at := stamp
	seen := map[int64]bool{}
	var units []any
	for _, w := range writes {
		if w.units == nil || w.before == "" || w.before != at {
			return nil, false
		}
		for _, u := range w.units {
			if !seen[u] {
				seen[u] = true
				units = append(units, u)
			}
		}
		at = w.after
	}
	return units, at == current && current != ""
}

func encodeSnapshot(order []int64, frags map[int64]nodeFrag, edges []byte, cursor string, bodies bool) []byte {
	size := len(edges) + 128
	for _, f := range frags {
		size += len(f.prefix) + 32
		if bodies {
			size += len(f.body) + 18
		}
	}
	var b bytes.Buffer
	b.Grow(size)
	b.WriteString(`{"nodes": [`)
	for i, id := range order {
		if i > 0 {
			b.WriteString(", ")
		}
		f := frags[id]
		b.Write(f.prefix)
		if bodies {
			b.WriteString(`, "search_body": `)
			b.Write(f.body)
		}
		if f.truncated {
			b.WriteString(`, "body_truncated": true}`)
		} else {
			b.WriteString(`, "body_truncated": false}`)
		}
	}
	b.WriteString(`], "edges": `)
	b.Write(edges)
	b.WriteString(`, "total": `)
	b.WriteString(pyjson.Str(int64(len(order))))
	b.WriteString(`, "cursor": `)
	b.Write(pyjson.Dumps(cursor))
	if bodies {
		b.WriteString(`, "bodies": true}`)
	} else {
		b.WriteString(`, "bodies": false}`)
	}
	return b.Bytes()
}

// snapshotNode encodes a hydrated node with the snapshot-only fields.
func snapshotNode(node *pyjson.Object, row sqlitedb.Row) nodeFrag {
	enc := pyjson.Dumps(node)
	return nodeFrag{prefix: enc[:len(enc)-1], body: pyjson.Dumps(row.Get("body")),
		truncated: pytext.Len(row.Str("body")) > 240}
}

// GraphSnapshotEncoded is pyjson.Dumps(Store.graph_snapshot(bodies=bodies)).
func (s *Store) GraphSnapshotEncoded(ctx context.Context, bodies bool) ([]byte, error) {
	st := &s.snap
	st.buildMu.Lock()
	defer st.buildMu.Unlock()

	for attempt := 0; ; attempt++ {
		out, err, retry := s.tryCachedSnapshot(ctx, bodies)
		if err == nil {
			return out, nil
		}
		if err != errFullRebuild {
			return nil, err
		}
		if !retry || attempt >= 20 {
			Stats.Gaps.Add(1)
			break
		}
		// A write committed but has not logged itself yet: wait for it.
		time.Sleep(time.Millisecond)
	}
	Stats.Rebuilds.Add(1)
	return s.rebuildSnapshot(ctx, bodies)
}

// tryCachedSnapshot serves from the cache when the write log connects the
// cached state to the current one.  retry=true means the only gap may be a
// write that is still logging itself.
func (s *Store) tryCachedSnapshot(ctx context.Context, bodies bool) ([]byte, error, bool) {
	st := &s.snap
	st.logMu.Lock()
	exists, stamp, pending := st.log.exists, st.log.stamp, st.log.pending
	writes := append([]loggedWrite(nil), st.log.writes...)
	st.logMu.Unlock()
	if !exists {
		return nil, errFullRebuild, false
	}
	var out []byte
	var current string
	patched := false
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		var err error
		if current, err = s.stamp(ctx, q); err != nil {
			return err
		}
		units, ok := chain(stamp, current, writes)
		if !ok {
			return errFullRebuild
		}
		cursor, _, err := changeCursor(ctx, q)
		if err != nil {
			return err
		}
		if len(units) == 0 {
			out = encodeSnapshot(st.cache.order, st.cache.frags, st.cache.edges, cursor, bodies)
			return nil
		}
		patched = true
		return s.patchSnapshot(ctx, q, units, cursor, bodies, &out)
	})
	if err != nil {
		return nil, err, pending > 0
	}
	if patched {
		Stats.Patches.Add(1)
	} else {
		Stats.Hits.Add(1)
	}
	st.logMu.Lock()
	st.log.stamp = current // fold the checked writes in; later ones stay logged
	st.log.writes = st.log.writes[len(writes):]
	st.logMu.Unlock()
	return out, nil, false
}

// patchSnapshot re-encodes the given nodes into the cached snapshot.
func (s *Store) patchSnapshot(ctx context.Context, q sqlitedb.Querier, units []any, cursor string, bodies bool, out *[]byte) error {
	c := &s.snap.cache
	rows, err := groupRows(ctx, q, "SELECT * FROM units WHERE id IN ({marks})", units)
	if err != nil {
		return err
	}
	if len(rows) != len(units) {
		return errFullRebuild // deleted: membership changed
	}
	for _, r := range rows {
		if r.Int("archived") != 0 {
			return errFullRebuild // archived: membership changed
		}
		if _, ok := c.frags[r.Int("id")]; !ok {
			return errFullRebuild // new node
		}
	}
	nodes, err := s.hydrate(ctx, q, rows, false)
	if err != nil {
		return err
	}
	for i, n := range nodes {
		c.frags[rows[i].Int("id")] = snapshotNode(n, rows[i])
	}
	*out = encodeSnapshot(c.order, c.frags, c.edges, cursor, bodies)
	return nil
}

// rebuildSnapshot builds every node within one read and makes the result
// the new cache at that read's stamp.  Writes logged meanwhile stay logged;
// the next request checks their chain from this stamp.
func (s *Store) rebuildSnapshot(ctx context.Context, bodies bool) ([]byte, error) {
	st := &s.snap
	st.logMu.Lock()
	seq0 := st.log.seq
	st.logMu.Unlock()
	var fresh snapCache
	var stamp, cursor string
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		var err error
		if stamp, err = s.stamp(ctx, q); err != nil {
			return err
		}
		if cursor, _, err = changeCursor(ctx, q); err != nil {
			return err
		}
		rows, err := sqlitedb.Query(ctx, q, "SELECT * FROM units WHERE archived=0 ORDER BY updated_at DESC, id DESC")
		if err != nil {
			return err
		}
		rel, err := relatedAllLive(ctx, q)
		if err != nil {
			return err
		}
		fresh = snapCache{frags: make(map[int64]nodeFrag, len(rows)), order: make([]int64, len(rows))}
		for off := 0; off < len(rows); off += 400 {
			batch := rows[off:min(off+400, len(rows))]
			nodes, err := s.hydrateWith(ctx, q, batch, false, rel)
			if err != nil {
				return err
			}
			for i, n := range nodes {
				id := batch[i].Int("id")
				fresh.order[off+i] = id
				fresh.frags[id] = snapshotNode(n, batch[i])
			}
		}
		edgeRows, err := sqlitedb.Query(ctx, q, "SELECT l.id, l.source_id AS source, l.target_id AS target, l.relation, l.note "+
			"FROM unit_links l JOIN units s ON s.id=l.source_id JOIN units t ON t.id=l.target_id "+
			"WHERE s.archived=0 AND t.archived=0 ORDER BY l.id")
		if err != nil {
			return err
		}
		fresh.edges = pyjson.Dumps(rowObjects(edgeRows))
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := encodeSnapshot(fresh.order, fresh.frags, fresh.edges, cursor, bodies)
	st.logMu.Lock()
	st.cache = fresh
	st.log.exists, st.log.stamp = true, stamp
	var keep []loggedWrite
	for _, w := range st.log.writes {
		if w.seq > seq0 { // may postdate the read; checked next time
			keep = append(keep, w)
		}
	}
	st.log.writes = keep
	st.logMu.Unlock()
	return out, nil
}

type rebuildErr struct{}

func (rebuildErr) Error() string { return "snapshot needs a full rebuild" }

var errFullRebuild error = rebuildErr{}
