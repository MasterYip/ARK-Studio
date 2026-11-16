package server

import (
	"strings"
	"sync"
	"time"

	"arkstudio/go/internal/project"
	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/store"
)

// responseCache coalesces deterministic project reads (units list + facets,
// graph snapshot, graph, timeline, project/home payload).
//
// Correctness rule: a request that arrives at time T with database state S
// may be answered with a body that was
//
//   - built for exactly state S (the state did not change, so a fresh build
//     would produce the same bytes), or
//   - built by a read that started at or after T (its snapshot already
//     contains every write that finished before the request was sent).
//
// Either way the client sees what Python would have returned for some moment
// between sending the request and receiving the answer.  Builds for one key
// run one at a time: under continuous writes each build serves every request
// that arrived before it started, so heavy work is bounded by one build per
// key per build duration instead of one per request.
type responseCache struct {
	mu      sync.Mutex
	entries map[string]*cachedBody
	bytes   int
}

type cachedBody struct {
	state     string
	startedAt time.Time
	done      chan struct{} // closed once body/err are set
	body      []byte        // full response: {"ok": true, ...}
	err       error
	gzip      sync.Once
	gz        []byte
	etagOnce  sync.Once
	etag      string // bodyETag(body), computed once
}

func (e *cachedBody) finished() bool {
	select {
	case <-e.done:
		return true
	default:
		return false
	}
}

const (
	maxCachedResponses = 512
	maxCachedBytes     = 256 << 20
)

func newResponseCache() *responseCache { return &responseCache{entries: map[string]*cachedBody{}} }

func (rc *responseCache) get(c *reqCtx, key string, arrived time.Time, state string, current func() string,
	build func() (*pyjson.Object, error)) (*cachedBody, error) {
	for {
		rc.mu.Lock()
		e := rc.entries[key]
		if e != nil && (e.state == state || !e.startedAt.Before(arrived)) {
			rc.mu.Unlock()
			select {
			case <-e.done:
			case <-c.ctx.Done():
				return nil, c.ctx.Err()
			}
			if e.err == nil {
				return e, nil
			}
			if c.ctx.Err() != nil {
				return nil, c.ctx.Err()
			}
			continue // that build failed (or its client left): build again
		}
		if e != nil && !e.finished() {
			rc.mu.Unlock()
			select { // one build per key at a time; then re-check
			case <-e.done:
			case <-c.ctx.Done():
				return nil, c.ctx.Err()
			}
			continue
		}
		if e != nil {
			rc.bytes -= len(e.body) + len(e.gz)
		}
		rc.evictLocked()
		e = &cachedBody{state: current(), startedAt: time.Now(), done: make(chan struct{})}
		rc.entries[key] = e
		rc.mu.Unlock()
		e.body, e.err = c.buildBody(build)
		rc.mu.Lock()
		if e.err != nil {
			if rc.entries[key] == e {
				delete(rc.entries, key) // failures are never served from the cache
			}
		} else {
			rc.bytes += len(e.body)
		}
		rc.mu.Unlock()
		close(e.done)
		return e, e.err
	}
}

func (rc *responseCache) evictLocked() {
	if len(rc.entries) < maxCachedResponses && rc.bytes < maxCachedBytes {
		return
	}
	for k, e := range rc.entries {
		if e.finished() {
			rc.bytes -= len(e.body) + len(e.gz)
			delete(rc.entries, k)
		}
	}
}

// buildBody runs an expensive read under the Heavy pool and encodes it.
func (c *reqCtx) buildBody(build func() (*pyjson.Object, error)) ([]byte, error) {
	if err := c.app.Heavy.Acquire(c.ctx); err != nil {
		return nil, err
	}
	defer c.app.Heavy.Release()
	data, err := build()
	if err != nil {
		return nil, err
	}
	if raw, ok := data.Value(rawMembers).(pyjson.Raw); ok && data.Len() == 1 {
		body := make([]byte, 0, len(raw)+12)
		body = append(body, `{"ok": true`...)
		if len(raw) > 2 {
			body = append(append(body, ", "...), raw[1:]...)
		} else {
			body = append(body, '}')
		}
		return body, nil
	}
	payload := pyjson.NewObject("ok", true)
	payload.Update(data)
	return pyjson.Dumps(payload), nil
}

// cachedRead answers a deterministic read through the response cache.  The
// actor is part of the key only when the query can resolve assignee=me.
func (c *reqCtx) cachedRead(p *project.Project, build func() (*pyjson.Object, error)) error {
	return c.cachedReadHeaders(p, nil, build)
}

// cachedReadHeaders is cachedRead with route headers (a revision-keyed ETag).
func (c *reqCtx) cachedReadHeaders(p *project.Project, headers headerList, build func() (*pyjson.Object, error)) error {
	arrived := time.Now()
	current := func() string { return p.Store.CacheKey() + "|" + store.Today() }
	key := p.Path + "|" + c.r.Method + "|" + c.target
	if strings.Contains(c.target, "assignee") {
		key += "|" + c.actor()
	}
	e, err := c.app.responses.get(c, key, arrived, current(), current, build)
	if err != nil {
		return err
	}
	if c.apiGetJSON(200, jsonType) {
		if _, has := headers.get("ETag"); !has {
			e.etagOnce.Do(func() { e.etag = bodyETag(e.body) })
			headers = append(headers, [2]string{"ETag", e.etag})
		}
		var match bool
		if headers, match = c.conditional(headers, e.body); match {
			c.notModified(headers)
			return nil
		}
	}
	if len(e.body) >= 1024 && AcceptsGzip(c.header("Accept-Encoding")) {
		e.gzip.Do(func() {
			if z := Gzip(e.body); len(z) < len(e.body) {
				e.gz = z
			}
		})
		if e.gz != nil {
			c.send(200, e.gz, jsonType, append(headers, [2]string{"Content-Encoding", "gzip"}, [2]string{"Vary", "Accept-Encoding"}), false)
			return nil
		}
	}
	c.send(200, e.body, jsonType, headers, false)
	return nil
}

// cachedReadRaw is cachedRead for a builder that returns an encoded object.
func (c *reqCtx) cachedReadRaw(p *project.Project, build func() ([]byte, error)) error {
	return c.cachedReadRawHeaders(p, nil, build)
}

// cachedReadRawHeaders is cachedReadRaw with route headers.
func (c *reqCtx) cachedReadRawHeaders(p *project.Project, headers headerList, build func() ([]byte, error)) error {
	return c.cachedReadHeaders(p, headers, func() (*pyjson.Object, error) {
		raw, err := build()
		if err != nil {
			return nil, err
		}
		return pyjson.NewObject(rawMembers, pyjson.Raw(raw)), nil
	})
}

// rawMembers marks an object whose single value is an encoded object to be
// merged into {"ok": true, ...} (see buildBody).
const rawMembers = "#ark-raw-members#"
