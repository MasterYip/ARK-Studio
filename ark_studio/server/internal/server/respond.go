package server

import (
	"arkstudio/go/internal/api"
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"arkstudio/go/internal/compat/pyjson"
)

// Header values copied from server.Handler.
const (
	DefaultCSP = "default-src 'self'; img-src 'self' data: blob: https: http:; " +
		"style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; " +
		"font-src 'self' data:; worker-src 'self'; manifest-src 'self'; " +
		"form-action 'self'; base-uri 'none'; object-src 'none'; " +
		"frame-ancestors 'none'"
	VizCSP = "sandbox allow-scripts; default-src 'none'; " +
		"script-src 'unsafe-inline' 'unsafe-eval'; style-src 'unsafe-inline'; " +
		"img-src data: blob:; media-src data: blob:; font-src data:; " +
		"connect-src 'none'; form-action 'none'; base-uri 'none'; " +
		"frame-ancestors 'self'"
	jsonType = api.JSONType
	// ServerHeader replaces "ARKStudio/2.0 Python/3.x" (not compared).
	ServerHeader = "ARKStudio/2.0 Go"
)

// ApiError is server.ApiError (api.Error, shared with the handler packages).
// Status 302 means "redirect to Message".
type ApiError = api.Error

func apiErr(status int, message, code string) *ApiError { return api.Err(status, message, code) }

// headerList keeps Python's dict order for extra headers.
type headerList [][2]string

func (h headerList) get(key string) (string, bool) {
	for _, kv := range h {
		if kv[0] == key {
			return kv[1], true
		}
	}
	return "", false
}

func (h headerList) without(keys ...string) headerList {
	var out headerList
outer:
	for _, kv := range h {
		for _, k := range keys {
			if kv[0] == k {
				continue outer
			}
		}
		out = append(out, kv)
	}
	return out
}

// AcceptsGzip is static_assets.accepts_gzip (api.AcceptsGzip).
func AcceptsGzip(header string) bool { return api.AcceptsGzip(header) }

var gzipPool = sync.Pool{New: func() any {
	w, _ := gzip.NewWriterLevel(nil, 5)
	return w
}}

// Gzip compresses at level 5 (gzip.compress(compresslevel=5)).
func Gzip(data []byte) []byte {
	var buf bytes.Buffer
	w := gzipPool.Get().(*gzip.Writer)
	w.Reset(&buf)
	w.Write(data) //nolint:errcheck
	w.Close()
	gzipPool.Put(w)
	return buf.Bytes()
}

// send is Handler._send: body, Content-Type, optional gzip, security headers.
func (c *reqCtx) send(status int, body []byte, ctype string, headers headerList, compress bool) {
	if c.apiGetJSON(status, ctype) {
		// Conditional GET (clientcache.go): validators, then a 304 on a match.
		var match bool
		if headers, match = c.conditional(headers, body); match {
			c.notModified(headers)
			return
		}
	}
	if compress && len(body) >= 1024 && AcceptsGzip(c.r.Header.Get("Accept-Encoding")) {
		if _, has := headers.get("Content-Encoding"); !has {
			if z := Gzip(body); len(z) < len(body) {
				headers = append(headers, [2]string{"Content-Encoding", "gzip"}, [2]string{"Vary", "Accept-Encoding"})
				body = z
			}
		}
	}
	h := c.w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	securityHeaders(h, headers)
	c.w.WriteHeader(status)
	if c.r.Method != http.MethodHead {
		c.w.Write(body) //nolint:errcheck // client went away
	}
}

// securityHeaders is _send_security_headers.
func securityHeaders(h http.Header, headers headerList) {
	csp, ok := headers.get("Content-Security-Policy")
	if !ok {
		csp = DefaultCSP
	}
	frame, ok := headers.get("X-Frame-Options")
	if !ok {
		frame = "DENY"
	}
	cache, ok := headers.get("Cache-Control")
	if !ok {
		cache = "no-store"
	}
	h.Set("Cache-Control", cache)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", frame)
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", csp)
	for _, kv := range headers.without("Content-Security-Policy", "X-Frame-Options", "Cache-Control") {
		h.Add(kv[0], kv[1])
	}
}

// sendJSON is _send(status, dict).
func (c *reqCtx) sendJSON(status int, value any, headers headerList) {
	c.send(status, pyjson.Dumps(value), jsonType, headers, true)
}

// ok is Handler._ok.
func (c *reqCtx) ok(data *pyjson.Object) {
	payload := pyjson.NewObject("ok", true)
	if data != nil {
		payload.Update(data)
	}
	c.sendJSON(200, payload, nil)
}

// okRaw sends {"ok": true, <pre-encoded object members>} for cached bodies.
func (c *reqCtx) okEncoded(object []byte) {
	body := make([]byte, 0, len(object)+12)
	body = append(body, `{"ok": true`...)
	if len(object) > 2 {
		body = append(body, ", "...)
		body = append(body, object[1:]...)
	} else {
		body = append(body, '}')
	}
	c.send(200, body, jsonType, nil, true)
}

// fail is Handler._error.
func (c *reqCtx) fail(status int, message, code string) {
	if code == "" {
		code = "error"
	}
	c.drainUnreadBody()
	c.sendJSON(status, pyjson.NewObject("ok", false, "error", pyjson.NewObject(
		"status", int64(status), "code", code, "message", message)), headerList{{"Connection", "close"}})
}

// drainUnreadBody is Handler._drain_unread_body: before an error reply,
// read an unread request body of at most 1 MB within 2 s, so closing the
// connection does not reset it before the client reads the reply.
func (c *reqCtx) drainUnreadBody() {
	if c.bodyConsumed || c.r.Method == http.MethodGet || c.r.Method == http.MethodHead {
		return
	}
	n, err := contentLength(c.header("Content-Length"))
	if err != nil || n <= 0 || n > 1<<20 {
		return
	}
	c.bodyConsumed = true
	rc := http.NewResponseController(c.w)
	rc.SetReadDeadline(time.Now().Add(2 * time.Second)) //nolint:errcheck
	io.CopyN(io.Discard, c.r.Body, n)                   //nolint:errcheck
	rc.SetReadDeadline(time.Time{})                     //nolint:errcheck
}

// redirect is _secure_redirect.
func (c *reqCtx) redirect(location string, status int, headers headerList) {
	h := c.w.Header()
	h.Set("Location", location)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	for _, kv := range headers {
		h.Add(kv[0], kv[1])
	}
	h.Set("Content-Length", "0")
	c.w.WriteHeader(status)
}
