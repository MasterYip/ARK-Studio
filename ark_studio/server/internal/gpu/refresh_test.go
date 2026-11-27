package gpu

import (
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"arkstudio/go/internal/compat/pyjson"
)

// The GPU tab's Refresh (2026-11-27, "slower than the Resources tab"): the
// system query no longer waits for the GPU query, and probes of the same
// server that overlap share one run.

func TestProbeServerRunsSystemQueryAlongside(t *testing.T) {
	server := pyjson.NewObject("name", "box", "host", "box.example", "port", int64(22), "user", "u")
	const each = 300 * time.Millisecond
	gpuRunner := func(*pyjson.Object, int) CommandResult {
		time.Sleep(each)
		return CommandResult{Stdout: "0, RTX 4090, 24564, 1200, 3\n"}
	}
	sysRunner := func(*pyjson.Object, int) CommandResult {
		time.Sleep(each)
		return CommandResult{Stdout: "__ARK_LOAD__\n0.1 0.2 0.3\n__ARK_NPROC__\n8\n"}
	}
	started := time.Now()
	out, err := ProbeServer(server, gpuRunner, 5, sysRunner)
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(started); took >= 2*each-50*time.Millisecond {
		t.Fatalf("GPU and system queries ran one after the other: %v", took)
	}
	if !pyjson.Truthy(out.Value("ok")) || out.Value("system") == nil {
		t.Fatalf("probe: %s", pyjson.Dumps(out))
	}
	// A failed GPU query returns at once and drops the system result.
	fail := func(*pyjson.Object, int) CommandResult {
		return CommandResult{ReturnCode: 255, ErrorKind: "unreachable", Message: "no route"}
	}
	out, err = ProbeServer(server, fail, 5, sysRunner)
	if err != nil || pyjson.Truthy(out.Value("ok")) || out.Value("system") != nil {
		t.Fatalf("failed probe: %v %s", err, pyjson.Dumps(out))
	}
}

func TestProbeSharedJoinsAProbeInFlight(t *testing.T) {
	var s Service
	server := pyjson.NewObject("name", "box", "host", "box.example", "port", int64(22), "user", "u")
	var runs atomic.Int32
	release := make(chan struct{})
	runner := func(*pyjson.Object, int) CommandResult {
		runs.Add(1)
		<-release
		return CommandResult{Stdout: "0, RTX 4090, 24564, 1200, 3\n"}
	}
	var wg sync.WaitGroup
	results := make([]*pyjson.Object, 3)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], _ = s.probeShared(server, runner, 5, nil, nil)
		}(i)
	}
	// Let the three callers meet the first probe, then finish it.
	deadline := time.Now().Add(2 * time.Second)
	for runs.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := runs.Load(); n != 1 {
		t.Fatalf("overlapping probes of one server ran %d times", n)
	}
	for i, r := range results {
		if r == nil || !pyjson.Truthy(r.Value("ok")) {
			t.Fatalf("caller %d: %v", i, r)
		}
	}
	// Once it finished, the next probe runs again (no stale cache).
	if _, err := s.probeShared(server, func(*pyjson.Object, int) CommandResult {
		runs.Add(1)
		return CommandResult{Stdout: "0, RTX 4090, 24564, 1200, 3\n"}
	}, 5, nil, nil); err != nil || runs.Load() != 2 {
		t.Fatalf("later probe: %v runs=%d", err, runs.Load())
	}
	// Another timeout is another probe.
	if s.probing != nil && len(s.probing) != 0 {
		t.Fatalf("probes left in flight: %d", len(s.probing))
	}
}

func TestSSHArgvSharesConnections(t *testing.T) {
	server := pyjson.NewObject("name", "box", "host", "box.example", "port", int64(2222), "user", "u")
	saved := muxDir
	defer func() { muxDir = saved }()

	muxDir = t.TempDir()
	argv := strings.Join(BuildCommandArgv(server, "ssh", 8, "true"), " ")
	for _, want := range []string{"ControlMaster=auto", "ControlPersist=60", "ControlPath=", "-p 2222 -- u@box.example"} {
		if !strings.Contains(argv, want) {
			t.Fatalf("argv lacks %q: %s", want, argv)
		}
	}
	muxDir = ""
	if argv := strings.Join(BuildSSHArgv(server, "ssh", 8), " "); strings.Contains(argv, "ControlMaster") {
		t.Fatalf("sharing off: %s", argv)
	}
	if runtime.GOOS == "windows" && sshMuxDir() != "" {
		t.Fatal("Windows OpenSSH has no ControlMaster")
	}
	t.Setenv("ARK_GPU_SSH_MUX", "0")
	if sshMuxDir() != "" {
		t.Fatal("ARK_GPU_SSH_MUX=0 must turn sharing off")
	}
}
