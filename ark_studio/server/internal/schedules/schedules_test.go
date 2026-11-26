package schedules

import (
	"sync"
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestSpecCheckAndNext(t *testing.T) {
	bad := []Spec{{Kind: "monthly"}, {Kind: "daily", At: "25:00"}, {Kind: "daily", At: "7:00"}, {Kind: "weekly", At: "07:00", Weekday: 7},
		{Kind: "hours", Every: 0}, {Kind: "hours", Every: 200}, {Kind: "daily", At: "07:00", TZOffset: 15 * 60}}
	for _, s := range bad {
		if s.Check() == nil {
			t.Errorf("accepted %+v", s)
		}
	}
	// Daily 07:00 in UTC+8 is 23:00 UTC the day before.
	daily := Spec{Kind: "daily", At: "07:00", TZOffset: 480}
	if got := daily.Next(at("2026-11-26T08:00:00Z")); !got.Equal(at("2026-11-26T23:00:00Z")) {
		t.Errorf("daily next = %v", got)
	}
	if got := daily.Next(at("2026-11-26T22:59:00Z")); !got.Equal(at("2026-11-26T23:00:00Z")) {
		t.Errorf("daily just before = %v", got)
	}
	if got := daily.Next(at("2026-11-26T23:00:00Z")); !got.Equal(at("2026-11-27T23:00:00Z")) {
		t.Errorf("daily at the time is the next day: %v", got)
	}
	// Weekly Monday 08:00 UTC; 2026-11-26 is a Thursday.
	weekly := Spec{Kind: "weekly", At: "08:00", Weekday: 1}
	if got := weekly.Next(at("2026-11-26T10:00:00Z")); !got.Equal(at("2026-11-30T08:00:00Z")) {
		t.Errorf("weekly next = %v", got)
	}
	hours := Spec{Kind: "hours", Every: 6}
	if got := hours.Next(at("2026-11-26T10:00:00Z")); !got.Equal(at("2026-11-26T16:00:00Z")) {
		t.Errorf("hours next = %v", got)
	}
}

// A server down for three days runs a daily task once, then waits for the
// next day.
func TestDueRunsMissedTaskOnce(t *testing.T) {
	task := Task{ID: "st_1", Enabled: true, Spec: Spec{Kind: "daily", At: "07:00"}, CreatedAt: "2026-11-20T00:00:00Z"}
	st := State{LastRunAt: "2026-11-22T07:00:05Z"}
	now := at("2026-11-26T09:00:00Z")
	if due, _ := Due(task, st, now); !due {
		t.Fatal("missed task is not due")
	}
	st.LastRunAt = now.Format(time.RFC3339)
	due, next := Due(task, st, now.Add(time.Minute))
	if due || !next.Equal(at("2026-11-27T07:00:00Z")) {
		t.Fatalf("after the catch-up run: due %v next %v", due, next)
	}
	// Disabled or running: not due.
	task.Enabled = false
	if due, _ := Due(task, State{}, now); due {
		t.Fatal("disabled task due")
	}
	task.Enabled = true
	if due, _ := Due(task, State{LastRunAt: "2026-11-22T07:00:05Z", RunningFrom: now.Add(-time.Minute).Format(time.RFC3339)}, now); due {
		t.Fatal("running task due again")
	}
}

func TestBeginNeverOverlaps(t *testing.T) {
	dir := t.TempDir()
	task := Task{ID: "st_1", Name: "x"}
	now := at("2026-11-26T09:00:00Z")
	var wg sync.WaitGroup
	var mu sync.Mutex
	started := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ok, err := Begin(dir, task, Run{ID: "sr_" + string(rune('a'+i)), Task: task.ID}, now)
			if err != nil {
				t.Error(err)
			}
			if ok {
				mu.Lock()
				started++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if started != 1 {
		t.Fatalf("%d runs started at once", started)
	}
	f, _ := Load(dir)
	run := f.Runs[0]
	run.Status = "ok"
	if err := Finish(dir, run, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if ok, _ := Begin(dir, task, Run{ID: "sr_z", Task: task.ID}, now.Add(2*time.Minute)); !ok {
		t.Fatal("cannot start after the run finished")
	}
}

// A run left "running" by a stopped server is closed as failed on start,
// and the task can run again.
func TestRecoverStale(t *testing.T) {
	dir := t.TempDir()
	task := Task{ID: "st_1", Name: "x", Enabled: true, Spec: Spec{Kind: "hours", Every: 1}, CreatedAt: "2026-11-26T00:00:00Z"}
	start := at("2026-11-26T09:00:00Z")
	if err := Update(dir, func(f *File) error { f.Tasks = append(f.Tasks, task); return nil }); err != nil {
		t.Fatal(err)
	}
	if ok, _ := Begin(dir, task, Run{ID: "sr_a", Task: task.ID, StartedAt: start.Format(time.RFC3339)}, start); !ok {
		t.Fatal("begin")
	}
	later := start.Add(2 * time.Hour)
	if err := RecoverStale(dir, later); err != nil {
		t.Fatal(err)
	}
	f, _ := Load(dir)
	if f.Runs[0].Status != "failed" || f.State[task.ID].RunningFrom != "" {
		t.Fatalf("stale run: %+v %+v", f.Runs[0], f.State[task.ID])
	}
	if due, _ := Due(task, f.State[task.ID], later); !due {
		t.Fatal("not due after recovery")
	}
}

func TestNormalizeCaps(t *testing.T) {
	task := Task{Name: " x ", Prompt: "p", Spec: Spec{Kind: "hours", Every: 2}, MaxSteps: 999, MaxWrites: -1, MaxTokens: 10_000_000}
	if err := Normalize(&task); err != nil {
		t.Fatal(err)
	}
	if task.Name != "x" || task.Mode != "suggest" || task.MaxSteps != MaxSteps || task.MaxWrites != DefaultWrites || task.MaxTokens != MaxTokens {
		t.Fatalf("normalized: %+v", task)
	}
	for _, bad := range []Task{{Prompt: "p", Spec: task.Spec}, {Name: "n", Spec: task.Spec}, {Name: "n", Prompt: "p", Mode: "auto", Spec: task.Spec}} {
		if Normalize(&bad) == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	for _, tpl := range Templates {
		tt := Task{Name: tpl.Name, Prompt: tpl.Prompt, Spec: tpl.Spec, Mode: tpl.Mode}
		if err := Normalize(&tt); err != nil {
			t.Errorf("template %s: %v", tpl.ID, err)
		}
	}
}
