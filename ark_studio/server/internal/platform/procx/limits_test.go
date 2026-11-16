package procx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPoolBoundsConcurrencyAndHonorsContext(t *testing.T) {
	p := NewPool(2)
	var mu sync.Mutex
	active, peak := 0, 0
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.Acquire(context.Background()); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			active++
			peak = max(peak, active)
			mu.Unlock()
			time.Sleep(5 * time.Millisecond)
			mu.Lock()
			active--
			mu.Unlock()
			p.Release()
		}()
	}
	wg.Wait()
	if peak > 2 {
		t.Fatalf("peak concurrency %d > 2", peak)
	}
	p.Acquire(context.Background()) //nolint:errcheck
	p.Acquire(context.Background()) //nolint:errcheck
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := p.Acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued Acquire must give up with the context, got %v", err)
	}
	if p.Waiting() != 0 {
		t.Fatalf("a cancelled waiter must leave the queue")
	}
}

// TestRunTimeoutKillsChild: a child that outlives Run's timeout is killed
// together with its own child.  The children are this test binary in helper
// mode (no shell start-up cost), they record their pids, and the test polls
// for both processes to be gone with a generous deadline instead of
// assuming how long start-up and the kill take on a loaded host.
func TestRunTimeoutKillsChild(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Skip("no test executable: " + err.Error())
	}
	if !processAlive(os.Getpid()) {
		t.Fatal("processAlive does not see this process")
	}
	timeout := 2 * time.Second
	for attempt := 0; ; attempt++ {
		dir := t.TempDir()
		p := NewPool(1)
		env := append(os.Environ(), helperEnv+"=tree", helperDirEnv+"="+dir)
		start := time.Now()
		_, err := p.Run(context.Background(), timeout, nil, 1<<16, env, self, "-test.run=^$")
		returned := time.Since(start)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("want deadline error, got %v", err)
		}
		if p.InUse() != 0 {
			t.Fatal("slot leaked")
		}
		// Run returns after the kill (plus WaitDelay for inherited pipes),
		// far before the helper's own end.
		if returned > helperSleep/2 {
			t.Fatalf("Run returned after %s; the child was not killed", returned)
		}
		pids := readPids(dir)
		if len(pids) < 2 {
			// The host was too slow for the helper to start both processes
			// within the timeout: nothing to check yet, retry with more time.
			if attempt == 3 {
				t.Fatalf("helper processes did not start within %s", timeout)
			}
			timeout *= 2
			continue
		}
		deadline := time.Now().Add(60 * time.Second)
		for _, pid := range pids {
			for processAlive(pid) {
				if time.Now().After(deadline) {
					t.Fatalf("process %d still running after the timeout kill", pid)
				}
				time.Sleep(50 * time.Millisecond)
			}
		}
		return
	}
}

const (
	helperEnv    = "ARK_PROCX_TEST_HELPER"
	helperDirEnv = "ARK_PROCX_TEST_DIR"
	helperSleep  = 10 * time.Minute
)

// TestMain runs the helper processes of TestRunTimeoutKillsChild: "tree"
// starts a "sleep" grandchild, then both write <pid>.pid and sleep.
func TestMain(m *testing.M) {
	switch os.Getenv(helperEnv) {
	case "":
		os.Exit(m.Run())
	case "tree":
		self, _ := os.Executable()
		cmd := exec.Command(self, "-test.run=^$")
		cmd.Env = append(os.Environ(), helperEnv+"=sleep")
		if err := cmd.Start(); err != nil {
			os.Exit(3)
		}
	}
	dir := os.Getenv(helperDirEnv)
	pid := strconv.Itoa(os.Getpid())
	tmp := filepath.Join(dir, pid+".tmp")
	if os.WriteFile(tmp, nil, 0o644) == nil {
		os.Rename(tmp, filepath.Join(dir, pid+".pid")) //nolint:errcheck
	}
	time.Sleep(helperSleep)
	os.Exit(0)
}

func readPids(dir string) []int {
	var pids []int
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if n, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".pid")); err == nil && strings.HasSuffix(e.Name(), ".pid") {
			pids = append(pids, n)
		}
	}
	return pids
}
