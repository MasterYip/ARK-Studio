package cftunnel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMain doubles as a fake cloudflared: with CFTUNNEL_FAKE set the test
// binary prints what a quick tunnel prints and exits after a while.
func TestMain(m *testing.M) {
	if run := os.Getenv("CFTUNNEL_FAKE"); run != "" {
		fmt.Fprintln(os.Stderr, "2026-11-23T10:57:12Z INF Requesting new quick Tunnel on trycloudflare.com...")
		fmt.Fprintf(os.Stderr, "2026-11-23T10:57:16Z INF |  https://fake-%s-tunnel-name.trycloudflare.com  |\n", run)
		fmt.Fprintln(os.Stderr, "2026-11-23T10:57:20Z INF Registered tunnel connection connIndex=0 location=hkg01 protocol=quic")
		if os.Getenv("TUNNEL_TOKEN") != "" {
			fmt.Fprintln(os.Stderr, "token leaked into a quick tunnel")
		}
		time.Sleep(400 * time.Millisecond)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func TestDownloadVerifiesChecksum(t *testing.T) {
	payload := []byte("pretend cloudflared binary")
	sum := sha256.Sum256(payload)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(payload) }))
	defer srv.Close()
	old := ReleaseBase
	ReleaseBase = srv.URL + "/"
	defer func() { ReleaseBase = old }()

	dir := t.TempDir()
	if _, err := download(context.Background(), dir, asset{"cf", strings.Repeat("0", 64), false}, nil); err == nil ||
		!strings.Contains(err.Error(), "checksum") {
		t.Fatalf("bad checksum accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, exeName())); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a binary that failed its checksum was installed: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("leftover files: %v", entries)
	}
	bin, err := download(context.Background(), dir, asset{"cf", hex.EncodeToString(sum[:]), false}, nil)
	if err != nil || bin.Source != "download" {
		t.Fatalf("download: %v %+v", err, bin)
	}
	got, _ := os.ReadFile(bin.Path)
	if string(got) != string(payload) {
		t.Fatal("installed file differs")
	}
	// Found again without downloading, unless PATH has one.
	lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	defer func() { lookPath = exec.LookPath }()
	if b, ok := Find("", dir); !ok || b.Path != bin.Path || b.Source != "download" {
		t.Fatalf("find: %+v %v", b, ok)
	}
	if _, ok := Find(filepath.Join(dir, "missing"), dir); ok {
		t.Fatal("a missing custom path was accepted")
	}
}

func TestPinnedAssetsCoverDesktopPlatforms(t *testing.T) {
	for _, p := range []string{"windows/amd64", "linux/amd64", "linux/arm64", "darwin/arm64", "darwin/amd64"} {
		a, ok := assets[p]
		if !ok || len(a.sha256) != 64 {
			t.Errorf("no pinned cloudflared for %s", p)
		}
	}
}

// A quick tunnel reports its URL, is "running" once registered, and after
// cloudflared exits it restarts and reports the new URL.
func TestTunnelRestartsWithNewURL(t *testing.T) {
	oldCmd, oldBackoff := command, Backoff
	defer func() { command, Backoff = oldCmd, oldBackoff }()
	Backoff = []time.Duration{50 * time.Millisecond}
	runs := 0
	var mu sync.Mutex
	command = func(cfg Config) *exec.Cmd {
		mu.Lock()
		runs++
		n := runs
		mu.Unlock()
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), fmt.Sprintf("CFTUNNEL_FAKE=run%d", n))
		return cmd
	}
	var urls []string
	tun := Start(Config{Binary: "fake", Origin: "http://127.0.0.1:1", Mode: "quick"}, func(u string) {
		mu.Lock()
		urls = append(urls, u)
		mu.Unlock()
	})
	defer tun.Stop()
	deadline := time.Now().Add(20 * time.Second)
	for {
		mu.Lock()
		n := len(urls)
		mu.Unlock()
		if n >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("urls: %v status %+v", urls, tun.Status())
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if urls[0] != "https://fake-run1-tunnel-name.trycloudflare.com/" || urls[1] != "https://fake-run2-tunnel-name.trycloudflare.com/" {
		t.Fatalf("urls: %v", urls)
	}
	st := tun.Status()
	if st.Restarts < 1 {
		t.Fatalf("restarts: %+v", st)
	}
	for _, l := range st.Log {
		if strings.Contains(l, "leaked") {
			t.Fatal("TUNNEL_TOKEN reached a quick tunnel")
		}
	}
}

func TestCommandKeepsTokenOutOfArgv(t *testing.T) {
	t.Setenv("TUNNEL_TOKEN", "inherited-should-not-pass")
	cmd := command(Config{Binary: "cloudflared", Origin: "http://127.0.0.1:9", Mode: "named", Token: "secret-token-value", Hostname: "ark.example.com"})
	for _, a := range cmd.Args {
		if strings.Contains(a, "secret-token-value") {
			t.Fatal("token on the command line")
		}
	}
	n := 0
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "TUNNEL_TOKEN=") {
			n++
			if kv != "TUNNEL_TOKEN=secret-token-value" {
				t.Fatalf("env: %s", kv)
			}
		}
	}
	if n != 1 {
		t.Fatalf("TUNNEL_TOKEN entries: %d", n)
	}
	quick := command(Config{Binary: "cloudflared", Origin: "http://127.0.0.1:9", Mode: "quick"})
	for _, kv := range quick.Env {
		if strings.HasPrefix(kv, "TUNNEL_TOKEN=") {
			t.Fatal("quick tunnel inherited TUNNEL_TOKEN")
		}
	}
	if strings.Join(quick.Args[1:], " ") != "tunnel --no-autoupdate --url http://127.0.0.1:9" {
		t.Fatalf("args: %v", quick.Args)
	}
}

func TestLogRedactsLongSecrets(t *testing.T) {
	tun := &Tunnel{cfg: Config{Mode: "named"}}
	tun.line("ERR failed token=eyJhIjoiMTIzNDU2Nzg5MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MCJ9")
	if strings.Contains(tun.Status().Log[0], "eyJhIjoi") {
		t.Fatalf("log kept a secret: %s", tun.Status().Log[0])
	}
}
