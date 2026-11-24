package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/gpu"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/sessionnudge"
	"arkstudio/go/internal/sessions"
)

const wakeServers = `enabled: true
defaults:
  user: alice
servers:
  - name: a
    host: a.example.internal
queue:
  enabled: true
  poll_seconds: 300
  min_free_memory_mb: 0
  max_utilization_pct: 100
`

// jobWakeApp is a local-mode app with a project that registers GPU server
// "a" and a queue whose SSH runs are stubbed: no worker loop, the test
// ticks; a launched job runs until `finish` reports its exit code.
type jobWakeApp struct {
	app    *App
	pid    string
	q      *gpu.Queue
	remote string
}

func newJobWakeApp(t *testing.T) *jobWakeApp {
	t.Helper()
	root := t.TempDir()
	cfg := filepath.Join(root, "gpu_servers.yaml")
	if err := os.WriteFile(cfg, []byte(wakeServers), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARK_GPU_SERVERS_CONFIG", cfg)
	t.Setenv("ARK_GPU_QUEUE_DB", filepath.Join(root, "queue.sqlite3"))
	t.Setenv("ARK_GPU_PROBE", "")
	app, err := NewApp(Config{Root: filepath.Join(root, "projects"), NoDemo: true,
		SessionsRoot: filepath.Join(root, "sessions")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	t.Cleanup(func() { app.gpu().Close() })
	app.Bind("127.0.0.1", 8765)
	app.gpu().Replay = true // the test ticks the queue
	if r := gpuCall(t, app, "POST", "/api/projects", map[string]any{"name": "Job wake"}, nil); r.code != 200 {
		t.Fatalf("create project: %d %s", r.code, r.raw)
	}
	p, err := (&reqCtx{app: app, ctx: context.Background()}).currentProject()
	if err != nil || p == nil {
		t.Fatalf("no current project: %v", err)
	}
	registerGPU(t, p.Path, "a")
	if !app.StartGPUQueue() {
		t.Fatal("queue did not start")
	}
	w := &jobWakeApp{app: app, pid: p.ID(), q: app.gpu().QueueIfStarted(), remote: "4242\n"}
	w.q.ProbeRunner = func(*pyjson.Object, int) gpu.CommandResult {
		return gpu.CommandResult{Stdout: "0, NVIDIA A100, 81920, 0, 0, 0, 30, 50\n"}
	}
	w.q.CommandRunner = func(*pyjson.Object, string, int) gpu.CommandResult { return gpu.CommandResult{Stdout: w.remote} }
	return w
}

// submit queues a job through the agent API (the server token, as an ARK
// session in local mode uses it) and starts it.
func (w *jobWakeApp) submit(t *testing.T, name, session string) string {
	t.Helper()
	body := map[string]any{"command": "python train.py", "job": name, "server": "a"}
	if session != "" {
		body["session"] = session
	}
	r := gpuCall(t, w.app, "POST", "/api/agent/v1/gpu/jobs", body,
		map[string]string{"Authorization": "Bearer " + w.app.AgentToken()})
	if r.code != 200 {
		t.Fatalf("submit: %d %s", r.code, r.raw)
	}
	id := r.body["job"].(map[string]any)["id"].(string)
	w.remote = "4242\n"
	if err := w.q.Tick(); err != nil {
		t.Fatal(err)
	}
	return id
}

// finish ends every running job with this exit code.
func (w *jobWakeApp) finish(t *testing.T, code string) {
	t.Helper()
	w.remote = "DONE:" + code + "\n__ARK_QUEUE_LOG_TAIL_V1__\nepoch 3\nCUDA out of memory\n"
	if code == "0" {
		w.remote = "DONE:0\n"
	}
	if err := w.q.Tick(); err != nil {
		t.Fatal(err)
	}
}

// startSession is a long-lived fake Claude Code session in the project.
func (w *jobWakeApp) startSession(t *testing.T, prompt string) *sessions.Session {
	t.Helper()
	m := w.app.Sessions
	exe, _ := os.Executable()
	m.Presets = []*pyjson.Object{pyjson.NewObject("id", "claude", "label", "Claude Code", "available", true,
		"command", []any{exe, "-p", "{prompt}"}, "prompt_mode", "arg", "followup", false)}
	t.Setenv("ARK_FAKE_CLAUDE_STREAM", "1")
	s, err := m.Start(nil, pyjson.NewObject("platform", "claude", "prompt", prompt), "http://127.0.0.1:1", org.LocalUsername)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = m.Close(s.ID)
		waitUntil(t, "session ended", func() bool { return !m.IsActive(s) })
	})
	m.SetMeta(s, "project", pyjson.NewObject("id", w.pid, "name", "Job wake"))
	waitUntil(t, "rest", func() bool { return m.MetaValue(s, "turn_state") == "waiting" })
	return s
}

// A GPU job an ARK session queued wakes that session when it ends: at once
// when it rests, after the turn when one runs, and after a server restart
// (the nudger's saved cursor).  Another session of the same person and
// project hears nothing.
func TestGPUJobWakesTheSessionThatQueuedIt(t *testing.T) {
	w := newJobWakeApp(t)
	m := w.app.Sessions
	s1 := w.startSession(t, "hello")
	s2 := w.startSession(t, "hello")
	n := &sessionnudge.Nudger{D: w.app.NudgeDeps()}
	ctx := context.Background()
	n.Tick(ctx) // first sight

	// Resting.
	j1 := w.submit(t, "train", s1.ID)
	if got := n.Tick(ctx); len(got) != 0 {
		t.Fatalf("woken before the job ended: %v", got)
	}
	w.finish(t, "1")
	if got := n.Tick(ctx); len(got) != 1 || got[0] != s1.ID {
		t.Fatalf("resting session not woken: %v", got)
	}
	waitUntil(t, "the job in the session", func() bool {
		out := stdoutOf(t, w.app, s1.ID)
		return strings.Contains(out, "echo: ARK UPDATE for ARK session "+s1.ID) &&
			strings.Contains(out, `GPU job `+j1+` "train" errored on a (exit 1)`) &&
			strings.Contains(out, "CUDA out of memory") && strings.Contains(out, "/api/agent/v1/gpu/jobs/"+j1+"/logs")
	})
	waitUntil(t, "rest", func() bool { return m.MetaValue(s1, "turn_state") == "waiting" })

	// Busy: queued until the turn ends; then the server restarts.
	if _, err := m.SendInput(nil, s1.ID, "slow turn", nil); err != nil {
		t.Fatal(err)
	}
	j2 := w.submit(t, "eval", s1.ID)
	w.finish(t, "0")
	if got := n.Tick(ctx); len(got) != 0 {
		t.Fatalf("injected during a running turn: %v", got)
	}
	restarted := &sessionnudge.Nudger{D: w.app.NudgeDeps()}
	if got := restarted.Tick(ctx); len(got) != 0 {
		t.Fatalf("injected during a running turn after the restart: %v", got)
	}
	waitUntil(t, "the slow turn ends", func() bool { return m.MetaValue(s1, "turn_state") == "waiting" })
	if got := restarted.Tick(ctx); len(got) != 1 || got[0] != s1.ID {
		t.Fatalf("job from the running turn lost: %v", got)
	}
	waitUntil(t, "the second job", func() bool {
		return strings.Contains(stdoutOf(t, w.app, s1.ID), `GPU job `+j2+` "eval" completed on a (exit 0)`)
	})
	waitUntil(t, "rest", func() bool { return m.MetaValue(s1, "turn_state") == "waiting" })
	if got := restarted.Tick(ctx); len(got) != 0 {
		t.Fatalf("delivered twice: %v", got)
	}
	if out := stdoutOf(t, w.app, s2.ID); strings.Contains(out, "GPU job") || strings.Contains(out, "ARK UPDATE") {
		t.Fatalf("another session heard of the job:\n%s", out)
	}

	// A session id in the body that is not the caller's own project's
	// session wakes nobody there.
	if wake := (&reqCtx{app: w.app, ctx: ctx, path: "/api/agent/v1/gpu/jobs"}).gpuWake("prj_other", s1.ID); wake.Session != "" {
		t.Fatalf("woke a session of another project: %+v", wake)
	}
}

// A job queued by an agent ARK did not launch is a gpu_job_done item in the
// update feed: a waiting watcher wakes; ARK sessions do not get it.
func TestGPUJobOfExternalAgentWakesItsWatcher(t *testing.T) {
	w := newJobWakeApp(t)
	s1 := w.startSession(t, "hello")
	n := &sessionnudge.Nudger{D: w.app.NudgeDeps()}
	ctx := context.Background()
	n.Tick(ctx)
	agent := map[string]string{"Authorization": "Bearer " + w.app.AgentToken()}
	head := gpuCall(t, w.app, "GET", "/api/agent/v1/updates", nil, agent)
	cursor, _ := head.body["cursor"].(string)
	if head.code != 200 || cursor == "" {
		t.Fatalf("feed head: %d %s", head.code, head.raw)
	}
	id := w.submit(t, "sweep", "")
	type reply struct{ r gpuReply }
	done := make(chan reply, 1)
	go func() {
		done <- reply{gpuCall(t, w.app, "GET", "/api/agent/v1/updates?wait=30&kinds=gpu_job_done&since="+cursor, nil, agent)}
	}()
	time.Sleep(300 * time.Millisecond) // the watcher waits
	w.finish(t, "0")
	var got gpuReply
	select {
	case d := <-done:
		got = d.r
	case <-time.After(40 * time.Second):
		t.Fatal("the watcher did not wake")
	}
	items, _ := got.body["items"].([]any)
	if got.code != 200 || len(items) != 1 {
		t.Fatalf("feed: %d %s", got.code, got.raw)
	}
	it := items[0].(map[string]any)
	job, _ := it["job"].(map[string]any)
	if it["kind"] != "gpu_job_done" || job["job"] != id || job["status"] != "completed" ||
		!strings.Contains(it["summary"].(string), `GPU job `+id+` "sweep" completed on a (exit 0)`) {
		t.Fatalf("item: %v", it)
	}
	if got := n.Tick(ctx); len(got) != 0 {
		t.Fatalf("an ARK session got an external agent's job: %v", got)
	}
	if out := stdoutOf(t, w.app, s1.ID); strings.Contains(out, "GPU job") {
		t.Fatalf("session heard of it:\n%s", out)
	}
}

// Background commands of a resting session that end without the CLI taking
// them up are told in one message (with the output tail); when the CLI
// takes them up itself, ARK says nothing.  Another session hears nothing.
func TestBackgroundTaskEndTold(t *testing.T) {
	old := bgNoticeGrace
	bgNoticeGrace = 300 * time.Millisecond
	t.Cleanup(func() { bgNoticeGrace = old })
	w := newJobWakeApp(t)
	m := w.app.Sessions
	s1 := w.startSession(t, "start two A")
	s2 := w.startSession(t, "hello")
	waitUntil(t, "two background tasks", func() bool { return m.Busy(s1).Background == 2 })
	n := &sessionnudge.Nudger{D: w.app.NudgeDeps()}
	ctx := context.Background()
	n.Tick(ctx) // first sight

	outFile := filepath.Join(t.TempDir(), "trainA.output")
	if err := os.WriteFile(outFile, []byte("epoch 1\nepoch 2\nsaved model.pt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SendInput(nil, s1.ID, "finish quietly A|"+outFile, nil); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "both ended", func() bool {
		return m.MetaValue(s1, "turn_state") == "waiting" && !m.Busy(s1).Any()
	})
	if got := n.Tick(ctx); len(got) != 0 { // within the grace period
		t.Fatalf("told before the grace period: %v", got)
	}
	time.Sleep(bgNoticeGrace + 100*time.Millisecond)
	if got := n.Tick(ctx); len(got) != 1 || got[0] != s1.ID {
		t.Fatalf("not told: %v", got)
	}
	waitUntil(t, "one message", func() bool {
		out := stdoutOf(t, w.app, s1.ID)
		return strings.Contains(out, "echo: ARK UPDATE for ARK session "+s1.ID+", ARK project Job wake ("+w.pid+"): 2 background task(s) of this session finished:") &&
			strings.Contains(out, `- Background task sh_trainA "trainA" completed (exit 0)`) &&
			strings.Contains(out, "    saved model.pt") &&
			strings.Contains(out, `- Background task sh_evalA "evalA" completed (exit 3)`)
	})
	if c := strings.Count(stdoutOf(t, w.app, s1.ID), "echo: ARK UPDATE"); c != 1 {
		t.Fatalf("%d messages, want 1", c)
	}
	waitUntil(t, "rest", func() bool { return m.MetaValue(s1, "turn_state") == "waiting" })
	n2 := &sessionnudge.Nudger{D: w.app.NudgeDeps()}
	if got := append(n2.Tick(ctx), n2.Tick(ctx)...); len(got) != 0 { // a restarted nudger goes on from the saved cursor
		t.Fatalf("told twice: %v", got)
	}

	// The CLI wakes itself: nothing from ARK.
	if _, err := m.SendInput(nil, s1.ID, "start two B", nil); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "B running", func() bool {
		return m.MetaValue(s1, "turn_state") == "waiting" && m.Busy(s1).Background == 2
	})
	if _, err := m.SendInput(nil, s1.ID, "finish wake B", nil); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the CLI took them up", func() bool {
		return strings.Contains(stdoutOf(t, w.app, s1.ID), "self-woke: both finished") &&
			m.MetaValue(s1, "turn_state") == "waiting"
	})
	time.Sleep(bgNoticeGrace + 100*time.Millisecond)
	n3 := &sessionnudge.Nudger{D: w.app.NudgeDeps()}
	if got := append(n3.Tick(ctx), n3.Tick(ctx)...); len(got) != 0 { // a restarted nudger goes on from the saved cursor
		t.Fatalf("told although the CLI took it up: %v", got)
	}
	if strings.Contains(stdoutOf(t, w.app, s1.ID), "sh_trainB") && strings.Count(stdoutOf(t, w.app, s1.ID), "echo: ARK UPDATE") != 1 {
		t.Fatal("a second message")
	}
	if out := stdoutOf(t, w.app, s2.ID); strings.Contains(out, "ARK UPDATE") {
		t.Fatalf("another session was told:\n%s", out)
	}
}
