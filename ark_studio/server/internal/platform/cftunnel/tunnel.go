package cftunnel

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Config is one tunnel: cloudflared at Binary forwards to Origin (the ARK
// tunnel listener, http://127.0.0.1:<port>).
type Config struct {
	Binary   string
	Origin   string
	Mode     string // "quick" (trycloudflare.com) or "named" (Token + Hostname)
	Token    string // named: passed in the environment (TUNNEL_TOKEN), never argv
	Hostname string // named: the public hostname routed to Origin
}

// Status is what Settings shows.
type Status struct {
	State     string    `json:"state"` // starting, running, restarting, stopped
	URL       string    `json:"url"`   // https://<host>/ once known
	Error     string    `json:"error"`
	StartedAt time.Time `json:"started_at"`
	Restarts  int       `json:"restarts"`
	Log       []string  `json:"log"` // the last cloudflared lines (no secrets)
}

// Tunnel supervises one cloudflared process and restarts it when it exits.
// A quick tunnel gets a new URL on every start; OnURL reports each one.
type Tunnel struct {
	cfg   Config
	onURL func(url string)

	mu     sync.Mutex
	st     Status
	cmd    *exec.Cmd
	stop   chan struct{}
	done   chan struct{}
	closed bool
}

var (
	quickURL   = regexp.MustCompile(`https://[a-z0-9][a-z0-9-]{0,62}\.trycloudflare\.com`)
	registered = regexp.MustCompile(`(?i)registered tunnel connection`)
	secretish  = regexp.MustCompile(`[A-Za-z0-9+/=_-]{40,}`)
)

// Backoff between restarts (tests shorten it).
var Backoff = []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second, 60 * time.Second}

// command builds the cloudflared invocation (tests replace it).
var command = func(cfg Config) *exec.Cmd {
	args := []string{"tunnel", "--no-autoupdate"}
	if cfg.Mode == "named" {
		args = append(args, "run", "--url", cfg.Origin)
	} else {
		args = append(args, "--url", cfg.Origin)
	}
	cmd := exec.Command(cfg.Binary, args...)
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(kv), "TUNNEL_") {
			env = append(env, kv)
		}
	}
	if cfg.Mode == "named" {
		env = append(env, "TUNNEL_TOKEN="+cfg.Token)
	}
	cmd.Env = env
	return cmd
}

// Start launches cloudflared and keeps it running until Stop.
func Start(cfg Config, onURL func(url string)) *Tunnel {
	t := &Tunnel{cfg: cfg, onURL: onURL, stop: make(chan struct{}), done: make(chan struct{})}
	t.st.State = "starting"
	if cfg.Mode == "named" && cfg.Hostname != "" {
		t.st.URL = "https://" + cfg.Hostname + "/"
	}
	go t.supervise()
	return t
}

// Status is a copy of the current state.
func (t *Tunnel) Status() Status {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.st
	st.Log = append([]string(nil), t.st.Log...)
	return st
}

// Stop ends cloudflared and waits for the supervisor.
func (t *Tunnel) Stop() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		<-t.done
		return
	}
	t.closed = true
	close(t.stop)
	cmd := t.cmd
	t.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		cmd.Process.Kill() //nolint:errcheck
	}
	select {
	case <-t.done:
	case <-time.After(10 * time.Second):
	}
}

func (t *Tunnel) supervise() {
	defer close(t.done)
	failures := 0
	for {
		started := time.Now()
		err := t.runOnce()
		t.mu.Lock()
		if t.closed {
			t.st.State = "stopped"
			t.mu.Unlock()
			return
		}
		if time.Since(started) > 2*time.Minute {
			failures = 0
		}
		wait := Backoff[min(failures, len(Backoff)-1)]
		failures++
		t.st.State, t.st.Restarts = "restarting", t.st.Restarts+1
		if err != nil {
			t.st.Error = err.Error()
		} else if t.st.Error == "" {
			t.st.Error = "cloudflared exited"
		}
		if t.cfg.Mode != "named" {
			t.st.URL = "" // the next quick tunnel gets a new address
		}
		t.mu.Unlock()
		select {
		case <-t.stop:
			t.mu.Lock()
			t.st.State = "stopped"
			t.mu.Unlock()
			return
		case <-time.After(wait):
		}
	}
}

func (t *Tunnel) runOnce() error {
	cmd := command(t.cfg)
	prepare(cmd)
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	if err := cmd.Start(); err != nil {
		t.mu.Unlock()
		pw.Close()
		return err
	}
	adopt(cmd)
	t.cmd = cmd
	t.st.StartedAt = time.Now().UTC()
	if t.st.State != "restarting" {
		t.st.State = "starting"
	}
	t.mu.Unlock()
	go func() {
		cmd.Wait() //nolint:errcheck
		pw.Close()
	}()
	scanner := bufio.NewScanner(pr)
	scanner.Buffer(make([]byte, 64<<10), 64<<10)
	for scanner.Scan() {
		t.line(scanner.Text())
	}
	io.Copy(io.Discard, pr) //nolint:errcheck
	t.mu.Lock()
	t.cmd = nil
	t.mu.Unlock()
	if cmd.ProcessState != nil && !cmd.ProcessState.Success() {
		return &exitError{cmd.ProcessState.String()}
	}
	return nil
}

type exitError struct{ s string }

func (e *exitError) Error() string { return "cloudflared " + e.s }

func (t *Tunnel) line(text string) {
	text = secretish.ReplaceAllString(strings.TrimSpace(text), "[redacted]")
	var announce string
	t.mu.Lock()
	t.st.Log = append(t.st.Log, text)
	if len(t.st.Log) > 30 {
		t.st.Log = t.st.Log[len(t.st.Log)-30:]
	}
	if t.cfg.Mode != "named" && t.st.URL == "" {
		if u := quickURL.FindString(text); u != "" && !strings.HasPrefix(u, "https://api.") {
			t.st.URL = u + "/"
			announce = t.st.URL
		}
	}
	if registered.MatchString(text) && t.st.URL != "" {
		t.st.State, t.st.Error = "running", ""
	}
	if strings.Contains(text, " ERR ") && t.st.State != "running" {
		t.st.Error = text
	}
	t.mu.Unlock()
	if announce != "" && t.onURL != nil {
		t.onURL(announce)
	}
}
