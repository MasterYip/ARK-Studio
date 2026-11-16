package server

// Server side of the browser's project cache (docs/development/client-cache.md):
// conditional GETs, X-ARK-Project-Id, the per-account cache id, GET
// /api/changes, /api/unit-details, /api/graph/bodies and the revision event
// stream GET /api/events.

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"arkstudio/go/internal/blake2b"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/rnd"
	"arkstudio/go/internal/store"
)

// clientCacheSchema is CLIENT_CACHE_SCHEMA.
const clientCacheSchema = int64(1)

// Revision stream timing (EVENTS_TICK, EVENTS_KEEPALIVE, EVENTS_RECHECK,
// EVENTS_LIFETIME) and UNIT_DETAILS_LIMIT.
const (
	eventsTick       = time.Second
	eventsKeepalive  = 20 * time.Second
	eventsRecheck    = 10 * time.Second
	eventsLifetime   = 600 * time.Second
	unitDetailsLimit = 60
)

// etagMatches is _etag_matches: weak comparison of If-None-Match with one tag.
func etagMatches(header, etag string) bool {
	if header == "" || etag == "" {
		return false
	}
	if pytext.Strip(header) == "*" {
		return true
	}
	want := strings.TrimPrefix(etag, "W/")
	for _, item := range strings.Split(header, ",") {
		item = strings.TrimPrefix(pytext.Strip(item), "W/")
		if item == want {
			return true
		}
	}
	return false
}

// bodyETag is the weak validator _send gives a JSON reply's exact bytes.
func bodyETag(data []byte) string {
	return `W/"` + hex.EncodeToString(blake2b.Sum(data, 12)) + `"`
}

// apiGetJSON is Handler._api_get_json.
func (c *reqCtx) apiGetJSON(status int, ctype string) bool {
	return status == 200 && (c.r.Method == http.MethodGet || c.r.Method == http.MethodHead) &&
		strings.HasPrefix(ctype, "application/json") && strings.HasPrefix(c.rawPath, "/api/")
}

// servedProjectID is App.served_project_id(): the project this request
// resolved (current_project / require).
func (c *reqCtx) servedProjectID() string {
	if c.resolved && c.project != nil {
		return c.project.ID()
	}
	return ""
}

// conditional adds the validators of a JSON GET reply (ETag of body unless
// the route set one, X-ARK-Project-Id) and reports a matching If-None-Match.
func (c *reqCtx) conditional(headers headerList, body []byte) (headerList, bool) {
	etag, ok := headers.get("ETag")
	if !ok {
		etag = bodyETag(body)
		headers = append(headers, [2]string{"ETag", etag})
	}
	if served := c.servedProjectID(); served != "" {
		if _, has := headers.get("X-ARK-Project-Id"); !has {
			headers = append(headers, [2]string{"X-ARK-Project-Id", served})
		}
	}
	return headers, etagMatches(c.header("If-None-Match"), etag)
}

// notModified is Handler._send_not_modified: an empty 304.
func (c *reqCtx) notModified(headers headerList) {
	securityHeaders(c.w.Header(), headers.without("Content-Encoding", "Content-Length"))
	c.w.WriteHeader(http.StatusNotModified)
}

// projectPermissionFor is App.project_permission(project) as text ("" for None).
func (c *reqCtx) projectPermissionFor(p *project.Project) (string, error) {
	if c.app.Auth == nil {
		return "admin", nil
	}
	user := c.actor()
	if user == "" {
		return "", nil
	}
	if p.ID() == demoID {
		return "write", nil
	}
	return c.app.Org.ProjectPermission(c.ctx, user, p.ID())
}

// revisionETag is Handler._revision_etag.
func (c *reqCtx) revisionETag(p *project.Project) (string, error) {
	perm, err := c.projectPermissionFor(p)
	if err != nil {
		return "", err
	}
	revision, err := p.Store.Revision(c.ctx)
	if err != nil {
		return "", err
	}
	key := strings.Join([]string{pyjson.Str(clientCacheSchema), p.ID(), c.actor(), perm, latin1(c.target), revision}, "|")
	return `W/"r` + hex.EncodeToString(blake2b.Sum([]byte(key), 12)) + `"`, nil
}

// revisioned is Handler._ok_revisioned: a 304 before the body is built when
// the revision-keyed validator matches; otherwise serve(headers).
func (c *reqCtx) revisioned(p *project.Project, serve func(headers headerList) error) error {
	etag, err := c.revisionETag(p)
	if err != nil {
		return err
	}
	headers := headerList{{"ETag", etag}, {"X-ARK-Project-Id", p.ID()}}
	if etagMatches(c.header("If-None-Match"), etag) {
		c.notModified(headers)
		return nil
	}
	return serve(headers)
}

// okWith is Handler._ok(data, headers=...).
func (c *reqCtx) okWith(data *pyjson.Object, headers headerList) {
	payload := pyjson.NewObject("ok", true)
	if data != nil {
		payload.Update(data)
	}
	c.sendJSON(200, payload, headers)
}

// sortedJSON is json.dumps(value, sort_keys=True, ensure_ascii=False).
func sortedJSON(value any) []byte { return pyjson.Dumps(sortKeys(value)) }

func sortKeys(value any) any {
	switch x := value.(type) {
	case *pyjson.Object:
		if x == nil {
			return x
		}
		keys := append([]string(nil), x.Keys()...)
		sort.Strings(keys)
		out := pyjson.NewObject()
		for _, k := range keys {
			out.Set(k, sortKeys(x.Value(k)))
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = sortKeys(v)
		}
		return out
	case []*pyjson.Object:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = sortKeys(v)
		}
		return out
	case pyjson.Raw:
		if v, err := pyjson.Loads(string(x)); err == nil {
			return sortKeys(v)
		}
	}
	return value
}

// overviewReply answers GET /api/project/overview: the validator ignores
// the diagnostic timings, which move to the end of the reply like Python's
// pop()/update().
func (c *reqCtx) overviewReply(data *pyjson.Object) {
	volatile := pyjson.NewObject()
	for _, key := range []string{"timing", "generated_ms", "store_ms"} {
		if data.Has(key) {
			volatile.Set(key, data.Delete(key))
		}
	}
	etag := bodyETag(sortedJSON(data))
	data.Update(volatile)
	c.okWith(data, headerList{{"ETag", etag}})
}

// ------------------------------------------------------------ cache identity

var cacheSalts sync.Map // *App -> *cacheSalt

type cacheSalt struct {
	mu    sync.Mutex
	value string
}

// cacheIdentity is App.cache_identity: sha256(salt:lower(user))[:20], the
// salt kept in <root>/.client-cache-salt.
func (c *reqCtx) cacheIdentity() string {
	user := c.actor()
	if user == "" {
		return ""
	}
	v, _ := cacheSalts.LoadOrStore(c.app, &cacheSalt{})
	cs := v.(*cacheSalt)
	cs.mu.Lock()
	salt := cs.value
	if salt == "" {
		path := filepath.Join(c.app.Root, ".client-cache-salt")
		if raw, err := os.ReadFile(path); err == nil {
			salt = pytext.Strip(string(raw))
		}
		if pytext.Len(salt) < 16 {
			salt = rnd.UUID4Hex() + rnd.UUID4Hex()
			os.WriteFile(path, []byte(salt), 0o644) //nolint:errcheck // Python ignores OSError
		}
		cs.value = salt
	}
	cs.mu.Unlock()
	sum := sha256.Sum256([]byte(salt + ":" + pytext.Lower(user)))
	return hex.EncodeToString(sum[:])[:20]
}

// cacheIDOrNil is "self.cache_identity() or None".
func (c *reqCtx) cacheIDOrNil() any {
	if id := c.cacheIdentity(); id != "" {
		return id
	}
	return nil
}

// ------------------------------------------------------------------ routes

func (c *reqCtx) bodiesFlag() bool {
	v := c.query.First("bodies", "1")
	return v != "0" && v != "false" && v != "no"
}

// clientCacheGet answers the cache routes of _api_get (handled=false: not one).
func (c *reqCtx) clientCacheGet(path string) (bool, error) {
	switch path {
	case "/api/events":
		return true, c.eventStream()
	case "/api/changes":
		p, err := c.require("read")
		if err != nil {
			return true, err
		}
		since := c.query.First("since", "")
		data, err := p.Store.Changes(c.ctx, since, c.bodiesFlag(), store.ChangesUnitLimit)
		if err == nil {
			c.ok(data)
		}
		return true, err
	case "/api/unit-details":
		p, err := c.require("read")
		if err != nil {
			return true, err
		}
		var refs []string
		for _, ref := range qlist(c.query, "ids") {
			if s := ref.(string); s != "" {
				refs = append(refs, s)
			}
		}
		if len(refs) > unitDetailsLimit {
			refs = refs[:unitDetailsLimit]
		}
		cursor, err := p.Store.ChangeCursor(c.ctx)
		if err != nil {
			return true, err
		}
		units, missing := []any{}, []any{}
		etags := pyjson.NewObject()
		for _, ref := range refs {
			unit, err := p.Store.GetUnit(c.ctx, ref)
			if err != nil {
				if _, notFound := err.(*store.Error); notFound {
					missing = append(missing, ref)
					continue
				}
				return true, err
			}
			units = append(units, unit)
		}
		// Per unit, the validator GET /api/units/{id} would send.
		for _, u := range units {
			unit := u.(*pyjson.Object)
			etags.Set(pyjson.Str(unit.Value("id")), bodyETag(pyjson.Dumps(pyjson.NewObject("ok", true, "unit", unit))))
		}
		c.ok(pyjson.NewObject("units", units, "missing", missing, "cursor", cursor, "etags", etags))
		return true, nil
	case "/api/graph/bodies":
		p, err := c.require("read")
		if err != nil {
			return true, err
		}
		return true, c.revisioned(p, func(headers headerList) error {
			return c.cachedReadHeaders(p, headers, func() (*pyjson.Object, error) { return p.Store.GraphBodies(c.ctx) })
		})
	}
	return false, nil
}

// ------------------------------------------------------------ event stream

// eventStream is Handler._event_stream (GET /api/events).  Like the terminal
// stream it leaves the admission pool and the per-request deadline; it ends
// on the client, server shutdown, a failed access re-check or its lifetime.
func (c *reqCtx) eventStream() error {
	p, err := c.require("read")
	if err != nil {
		return err
	}
	session := c.session
	mobileSession := c.mobileSession
	c.detachFromRequestLimits()
	c.ctx = c.r.Context() // no request timeout from here on
	h := c.w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	securityHeaders(h, headerList{{"Cache-Control", "no-cache, no-transform"}, {"X-Accel-Buffering", "no"},
		{"Connection", "close"}, {"X-ARK-Project-Id", p.ID()}})
	c.w.WriteHeader(200)
	rc := http.NewResponseController(c.w)
	write := func(text string) bool {
		rc.SetWriteDeadline(time.Now().Add(30 * time.Second)) //nolint:errcheck
		if _, err := c.w.Write([]byte(text)); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !write("retry: 3000\n\n") {
		return nil
	}
	started := time.Now()
	lastWrite, lastCheck := started, started
	lastRevision, haveRevision := "", false
	lastSessions, haveSessions := "", false
	var sessionsChecked time.Time
	for {
		select {
		case <-c.app.Streams.Done():
			return nil
		default:
		}
		now := time.Now()
		if now.Sub(lastCheck) >= eventsRecheck {
			lastCheck = now
			event := ""
			if c.app.Auth != nil {
				if mobileSession != nil {
					if c.mobileSessionForProject() == nil {
						event = "auth"
					}
				} else if session != nil {
					if s, err := c.authSession(); err == nil && s == nil {
						event = "auth"
					} else if err != nil {
						return nil
					}
				}
			}
			if event == "" {
				if err := c.checkProjectAccess(p, "read"); err != nil {
					if _, denied := err.(*ApiError); !denied {
						return nil
					}
					event = "denied"
				}
			}
			if event != "" {
				write("event: " + event + "\ndata: " + string(pyjson.Dumps(pyjson.NewObject("project", p.ID()))) + "\n\n")
				return nil
			}
		}
		st := p.Store
		revision, err := st.Revision(c.ctx)
		cursor := ""
		if err == nil && (!haveRevision || revision != lastRevision) {
			cursor, err = st.ChangeCursor(c.ctx)
		}
		if err != nil && c.ctx.Err() != nil {
			return nil
		}
		// A read error counts as "unchanged" (Python: revision = last_revision).
		if err == nil && (!haveRevision || revision != lastRevision) {
			haveRevision, lastRevision = true, revision
			data := pyjson.Dumps(pyjson.NewObject("revision", revision, "cursor", cursor, "project", p.ID()))
			if !write("id: " + cursor + "\nevent: revision\ndata: " + string(data) + "\n\n") {
				return nil
			}
			lastWrite = now
		}
		if now.Sub(sessionsChecked) >= eventsSessionsEvery {
			sessionsChecked = now
			if token := c.sessionsToken(p); !haveSessions || token != lastSessions {
				haveSessions, lastSessions = true, token
				data := pyjson.Dumps(pyjson.NewObject("token", token, "project", p.ID()))
				if !write("event: sessions\ndata: " + string(data) + "\n\n") {
					return nil
				}
				lastWrite = now
			}
		}
		if now.Sub(lastWrite) >= eventsKeepalive {
			if !write(": keep-alive\n\n") {
				return nil
			}
			lastWrite = now
		}
		if now.Sub(started) >= eventsLifetime {
			write("event: reconnect\ndata: {}\n\n")
			return nil
		}
		select {
		case <-c.r.Context().Done():
			return nil
		case <-c.app.Streams.Done():
			return nil
		case <-time.After(eventsTick):
		}
	}
}
