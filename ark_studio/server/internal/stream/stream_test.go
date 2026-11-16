package stream

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

// 300 clients connect, receive an event, and disconnect; afterwards no
// goroutine and no subscriber may be left over.  Then server shutdown must
// end open streams immediately.
func TestNoGoroutineLeakAndShutdown(t *testing.T) {
	hub := NewHub()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ch, unsubscribe := hub.Subscribe(16)
		defer unsubscribe()
		ServeSSE(w, r, hub, ch, time.Second, 5*time.Second) //nolint:errcheck
	}))
	srv.Config.RegisterOnShutdown(hub.Close)
	srv.Start()
	base := runtime.NumGoroutine()

	const clients = 300
	cancels := make([]context.CancelFunc, clients)
	lines := make(chan string, clients)
	dial := make(chan struct{}, 32) // the Windows accept backlog is small
	for i := 0; i < clients; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancels[i] = cancel
		req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
		go func() {
			dial <- struct{}{}
			resp, err := srv.Client().Do(req)
			<-dial
			if err != nil {
				lines <- "error: " + err.Error()
				return
			}
			defer resp.Body.Close()
			sc := bufio.NewScanner(resp.Body)
			for sc.Scan() {
				if strings.HasPrefix(sc.Text(), "data: ") {
					lines <- sc.Text()
					return
				}
			}
			lines <- "eof"
		}()
	}
	deadline := time.Now().Add(10 * time.Second)
	for hub.Subscribers() < clients && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	hub.Publish(Event{Name: "out", Data: []byte(`{"text":"hello"}`)})
	for i := 0; i < clients; i++ {
		if got := <-lines; got != `data: {"text":"hello"}` {
			t.Fatalf("client %d got %q", i, got)
		}
	}
	for _, cancel := range cancels {
		cancel()
	}
	for hub.Subscribers() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := hub.Subscribers(); n != 0 {
		t.Fatalf("%d subscribers left after disconnect", n)
	}
	srv.CloseClientConnections()
	for runtime.NumGoroutine() > base+5 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if g := runtime.NumGoroutine(); g > base+5 {
		t.Fatalf("goroutines: %d before, %d after %d disconnects", base, g, clients)
	}

	// An open stream must not hold up graceful shutdown.
	ch, _ := hub.Subscribe(1)
	_ = ch
	go func() {
		resp, err := srv.Client().Get(srv.URL)
		if err == nil {
			resp.Body.Close()
		}
	}()
	time.Sleep(100 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	if err := srv.Config.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown waited for the stream: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("shutdown took %s", time.Since(start))
	}
}
