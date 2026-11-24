package gpu

import (
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

type finished struct {
	job  *pyjson.Object
	wake Wake
}

// A job queued with a wake target is announced once when it ends (reaped,
// failed or cancelled); a job nobody waits for, or one from before the
// wake column, is not; the request body cannot choose whom to wake.
func TestQueueAnnouncesFinishedJobsOnce(t *testing.T) {
	svc, _ := scopeService(t, scopeConfig)
	var got []finished
	svc.OnFinished = func(job *pyjson.Object, wake Wake) error {
		got = append(got, finished{job, wake})
		return nil
	}
	q, err := svc.queueService()
	if err != nil {
		t.Fatal(err)
	}
	q.ProbeRunner = func(server *pyjson.Object, timeout int) CommandResult {
		return CommandResult{Stdout: "0, NVIDIA A100, 81920, 0, 0, 0, 30, 50\n"}
	}
	remote := "4242\n"
	q.CommandRunner = func(server *pyjson.Object, command string, timeout int) CommandResult {
		return CommandResult{Stdout: remote}
	}
	submit := func(name string, wake Wake, extra ...any) string {
		body := pyjson.NewObject(append([]any{"command", "python train.py", "job", name, "server", "h20",
			"min_free_memory_mb", int64(1)}, extra...)...)
		job, err := svc.QueueSubmitWake(Unscoped(), body, "ana", "p1", "", "", wake)
		if err != nil {
			t.Fatal(err)
		}
		if job.Has("wake") || job.Has("notified") {
			t.Fatalf("job JSON shows the wake columns: %s", pyjson.Dumps(job))
		}
		return pyjson.Str(job.Value("id"))
	}
	train := submit("train", Wake{Session: "ses_1"})
	quiet := submit("quiet", Wake{}, "wake", map[string]any{"session": "ses_evil"})
	if err := q.Tick(); err != nil { // both start
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("announced before the end: %+v", got)
	}
	remote = "DONE:0\n"
	if err := q.Tick(); err != nil { // both end
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].wake != (Wake{Session: "ses_1"}) || got[0].job.Value("id") != train ||
		got[0].job.Value("status") != "completed" || got[0].job.Value("exit_code") != int64(0) {
		t.Fatalf("completed: %+v", got)
	}
	if j, _ := q.Get(quiet); j.Value("status") != "completed" {
		t.Fatalf("quiet job: %s", pyjson.Dumps(j))
	}
	if err := q.Tick(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("announced twice: %d", len(got))
	}

	// A failing listener leaves the job to the next look.
	got = nil
	fail := true
	svc.OnFinished = func(job *pyjson.Object, wake Wake) error {
		if fail {
			return &QueueError{"busy", 500}
		}
		got = append(got, finished{job, wake})
		return nil
	}
	q.OnFinished = svc.OnFinished
	agentJob := submit("agent", Wake{Token: "tok_1", Agent: true})
	if _, err := q.Cancel(agentJob); err != nil {
		t.Fatal(err)
	}
	fail = false
	q.NotifyFinished()
	if len(got) != 1 || got[0].wake != (Wake{Token: "tok_1", Agent: true}) || got[0].job.Value("status") != "canceled" {
		t.Fatalf("cancelled: %+v", got)
	}
	q.NotifyFinished()
	if len(got) != 1 {
		t.Fatal("cancelled job announced twice")
	}
}

// Jobs that ended before the wake column existed are not announced after
// the upgrade (the column defaults to notified).
func TestQueueWakeMigrationKeepsOldJobsQuiet(t *testing.T) {
	svc, _ := scopeService(t, scopeConfig)
	q, err := svc.queueService()
	if err != nil {
		t.Fatal(err)
	}
	if err := q.exec(`INSERT INTO gpu_jobs(id,status,created_at,updated_at,job,project,owner,session,command,work_dir,
                      env_json,timeout_minutes,ended_at) VALUES('gpu_old','completed','2026-11-01T00:00:00Z',
                      '2026-11-01T00:00:00Z','old','p1','ana','','x','','{}',10,'2026-11-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	called := 0
	q.OnFinished = func(*pyjson.Object, Wake) error { called++; return nil }
	q.NotifyFinished()
	if called != 0 {
		t.Fatal("an old job was announced")
	}
}

func TestJobDuration(t *testing.T) {
	job := pyjson.NewObject("created_at", "2026-11-23T10:00:00Z", "started_at", "2026-11-23T10:01:00Z",
		"ended_at", "2026-11-23T11:03:05Z")
	if d := JobDuration(job); d.Seconds() != 3725 {
		t.Fatalf("duration %v", d)
	}
	if !strings.Contains(Wake{Session: "s"}.encode(), `"session"`) || (Wake{}).encode() != "" {
		t.Fatal("encode")
	}
}
