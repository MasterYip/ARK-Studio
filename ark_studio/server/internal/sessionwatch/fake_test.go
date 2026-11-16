package sessionwatch

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

// recorded is one request exactly as it arrived on the socket.
type recorded struct {
	Head string // request line + header lines, CRLF-joined
	Body string
}

func (r recorded) method() string { m, _, _ := strings.Cut(r.Head, " "); return m }

func (r recorded) path() string {
	line, _, _ := strings.Cut(r.Head, "\r\n")
	parts := strings.Split(line, " ")
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

func (r recorded) header(name string) string {
	for _, line := range strings.Split(r.Head, "\r\n")[1:] {
		k, v, ok := strings.Cut(line, ": ")
		if ok && strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

func (r recorded) bodyObject(t *testing.T) *pyjson.Object {
	t.Helper()
	o, ok := pyjson.LoadsObject(r.Body)
	if !ok {
		t.Fatalf("body is not a JSON object: %q", r.Body)
	}
	return o
}

// fakeARK is a raw-socket stand-in for the ARK session endpoints.  Each
// request gets a fresh connection (urllib sends Connection: close).
type fakeARK struct {
	t  *testing.T
	ln net.Listener
	mu sync.Mutex

	enabled  bool
	limit    int64 // 0: no "limits" key
	received int64
	reqs     []recorded
	// override answers request #n (0-based) with a raw HTTP response.
	override map[int]string
	// hook may answer any request (return "" to fall through).
	hook func(n int, r recorded) string
}

func newFakeARK(t *testing.T) *fakeARK {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeARK{t: t, ln: ln, enabled: true}
	go f.serve()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *fakeARK) url() string { return "http://" + f.ln.Addr().String() }

func (f *fakeARK) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enabled, f.limit, f.received, f.reqs, f.override, f.hook = true, 0, 0, nil, nil, nil
}

func (f *fakeARK) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.reqs...)
}

func (f *fakeARK) serve() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handle(conn)
	}
}

func (f *fakeARK) handle(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	var lines []string
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		lines = append(lines, line)
	}
	rec := recorded{Head: strings.Join(lines, "\r\n")}
	if n, err := strconv.Atoi(rec.header("Content-Length")); err == nil && n > 0 {
		buf := make([]byte, n)
		if _, err := io.ReadFull(br, buf); err != nil {
			return
		}
		rec.Body = string(buf)
	}
	f.mu.Lock()
	n := len(f.reqs)
	f.reqs = append(f.reqs, rec)
	resp := f.override[n]
	hook := f.hook
	f.mu.Unlock()
	if resp == "" && hook != nil {
		resp = hook(n, rec)
	}
	if resp == closeMarker {
		return
	}
	if resp == "" {
		resp = f.answer(rec)
	}
	_, _ = conn.Write([]byte(resp))
}

func jsonResponse(status int, reason, body string) string {
	return fmt.Sprintf("HTTP/1.1 %d %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		status, reason, len(body), body)
}

func (f *fakeARK) answer(r recorded) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.path()
	switch {
	case r.method() == "GET" && strings.Contains(p, "/api/agent/v1/sessions/"):
		o := pyjson.NewObject("id", p[strings.LastIndex(p, "/")+1:], "uploads_enabled", f.enabled,
			"exists", f.received > 0, "received_bytes", f.received)
		if f.limit > 0 {
			o.Set("limits", pyjson.NewObject("chunk_bytes", f.limit))
		}
		return jsonResponse(200, "OK", string(pyjson.Dumps(o)))
	case r.method() == "POST" && strings.HasSuffix(p, "/upload"):
		body, ok := pyjson.LoadsObject(r.Body)
		if !ok {
			return jsonResponse(400, "Bad Request", `{"error": {"code": "BAD_JSON", "message": "bad body"}}`)
		}
		offset := pyInt(body.Value("offset"))
		length := pyInt(body.Value("length"))
		if pyjson.Str(body.Value("mode")) == "replace" && offset == 0 {
			f.received = 0
		}
		if offset != f.received {
			return jsonResponse(409, "Conflict", fmt.Sprintf(
				`{"error": {"code": "SESSION_OFFSET_MISMATCH", "message": "expected offset %d"}}`, f.received))
		}
		f.received += length
		return jsonResponse(200, "OK", fmt.Sprintf(`{"received_bytes": %d}`, f.received))
	}
	return jsonResponse(404, "Not Found", `{"error": {"code": "NOT_FOUND", "message": "no route"}}`)
}

// closedPort returns an address nothing listens on.
func closedPort(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func jsonl(lines ...string) []byte {
	var b bytes.Buffer
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return b.Bytes()
}

const closeMarker = "<close-without-response>"
