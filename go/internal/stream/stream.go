// Package stream is the pattern every streaming endpoint (SSE terminal
// output, NDJSON chat) uses in the Go server.
//
// Rules it enforces:
//
//   - one goroutine per stream: the net/http handler goroutine itself runs the
//     loop; nothing is spawned per connection, so a disconnected client cannot
//     leave a goroutine behind;
//   - the loop ends on client disconnect (request context), on server
//     shutdown (Hub.Close via http.Server.RegisterOnShutdown, so Shutdown does
//     not wait for long-lived streams), or when the source is done;
//   - every write is flushed and has a write deadline, so a stalled client
//     cannot pin the handler;
//   - heartbeats keep proxies from closing idle streams.
//
// Subscribers get a bounded buffer; a subscriber that cannot keep up is
// dropped instead of blocking the publisher (Python's bounded terminal
// buffers behave the same way: slow readers re-read from an offset).
package stream

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

// Event is one message.  Name "" sends a data-only SSE event.
type Event struct {
	Name string
	Data []byte
}

// Hub fans events out to subscribers of one topic.
type Hub struct {
	mu     sync.Mutex
	subs   map[chan Event]struct{}
	closed chan struct{}
	once   sync.Once
}

// NewHub returns an open hub.
func NewHub() *Hub { return &Hub{subs: map[chan Event]struct{}{}, closed: make(chan struct{})} }

// Subscribe registers a subscriber with a bounded buffer.
func (h *Hub) Subscribe(buffer int) (<-chan Event, func()) {
	ch := make(chan Event, buffer)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
		h.mu.Unlock()
	}
}

// Publish delivers e to every subscriber; full subscribers are dropped.
func (h *Hub) Publish(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- e:
		default:
			delete(h.subs, ch)
			close(ch)
		}
	}
}

// Subscribers is the current subscriber count.
func (h *Hub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// Close ends every stream (server shutdown).
func (h *Hub) Close() { h.once.Do(func() { close(h.closed) }) }

// Done is closed by Close.
func (h *Hub) Done() <-chan struct{} { return h.closed }

// ErrNoFlush is returned when the ResponseWriter cannot stream.
var ErrNoFlush = errors.New("stream: response writer cannot flush")

// ServeSSE streams events from ch as text/event-stream until the client
// leaves, the hub closes, or ch is closed.
func ServeSSE(w http.ResponseWriter, r *http.Request, hub *Hub, ch <-chan Event, heartbeat, writeTimeout time.Duration) error {
	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		return ErrNoFlush
	}
	tick := time.NewTicker(heartbeat)
	defer tick.Stop()
	write := func(b []byte) error {
		rc.SetWriteDeadline(time.Now().Add(writeTimeout)) //nolint:errcheck
		if _, err := w.Write(b); err != nil {
			return err
		}
		return rc.Flush()
	}
	for {
		select {
		case <-r.Context().Done():
			return r.Context().Err()
		case <-hub.Done():
			return nil
		case <-tick.C:
			if err := write([]byte(": keepalive\n\n")); err != nil {
				return err
			}
		case e, ok := <-ch:
			if !ok {
				return nil
			}
			var b []byte
			if e.Name != "" {
				b = append(b, "event: "+e.Name+"\n"...)
			}
			b = append(b, "data: "...)
			b = append(b, e.Data...)
			b = append(b, "\n\n"...)
			if err := write(b); err != nil {
				return err
			}
		}
	}
}

// Detach returns a context that is not cancelled with the request but
// keeps its values, for work that must finish after the client left.
func Detach(ctx context.Context) context.Context { return context.WithoutCancel(ctx) }
