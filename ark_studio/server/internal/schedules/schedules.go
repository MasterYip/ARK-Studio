// Package schedules holds a project's scheduled Assistant tasks (Go only,
// 2026-11-26): a fixed prompt the built-in Assistant runs on a schedule as
// the project admin who created it, either applying its changes or, in
// suggest mode, filing them as one review unit.  This package knows the
// task file, the schedules and when a task is due; the server runs them
// (internal/server/routes_schedules.go).
//
// File <project>/schedules.json:
//
//	{"tasks": [Task...], "state": {"<id>": State}, "runs": [Run...]}
//
// Runs keep the newest MaxRuns.  A task is due when the first scheduled
// time after its last run (or its creation) has passed, so a server that
// was down for several periods runs it once, not once per missed period.
package schedules

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// FileName is the task file in the project folder.
const FileName = "schedules.json"

// Limits.
const (
	MaxTasks      = 20
	MaxRuns       = 60
	MaxPrompt     = 6000
	MaxName       = 80
	DefaultSteps  = 8
	MaxSteps      = 24
	DefaultWrites = 20
	MaxWrites     = 100
	DefaultTokens = 60000
	MaxTokens     = 400000
	// StaleRun: a run marked running for longer is taken as lost (the
	// server stopped during it).
	StaleRun = 30 * time.Minute
)

// Spec is when a task runs.  Times are local to TZOffset minutes east of UTC.
type Spec struct {
	Kind     string `json:"kind"`                // "daily" | "weekly" | "hours"
	At       string `json:"at,omitempty"`        // "HH:MM" for daily / weekly
	Weekday  int    `json:"weekday,omitempty"`   // 0 = Sunday, weekly
	Every    int    `json:"every,omitempty"`     // hours, 1-168
	TZOffset int    `json:"tz_offset,omitempty"` // minutes east of UTC
}

// Task is one scheduled task.
type Task struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Prompt    string `json:"prompt"`
	Template  string `json:"template,omitempty"`
	Spec      Spec   `json:"schedule"`
	Mode      string `json:"mode"` // "suggest" (dry run, default) | "apply"
	Enabled   bool   `json:"enabled"`
	Owner     string `json:"owner"` // the admin it runs as
	MaxSteps  int    `json:"max_steps"`
	MaxWrites int    `json:"max_writes"`
	MaxTokens int64  `json:"max_tokens"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// State is a task's last run.
type State struct {
	LastRunAt   string `json:"last_run_at,omitempty"`
	LastStatus  string `json:"last_status,omitempty"` // ok | failed | stopped
	LastError   string `json:"last_error,omitempty"`
	LastRunID   string `json:"last_run_id,omitempty"`
	RunningFrom string `json:"running_since,omitempty"`
	Failures    int    `json:"failures,omitempty"` // failed runs in a row
}

// Run is one logged run.
type Run struct {
	ID         string   `json:"id"`
	Task       string   `json:"task"`
	Name       string   `json:"name"`
	Trigger    string   `json:"trigger"` // schedule | manual
	Mode       string   `json:"mode"`
	Owner      string   `json:"owner"`
	StartedAt  string   `json:"started_at"`
	FinishedAt string   `json:"finished_at,omitempty"`
	Status     string   `json:"status"` // running | ok | failed | stopped
	Summary    string   `json:"summary,omitempty"`
	Error      string   `json:"error,omitempty"`
	Touched    []string `json:"touched,omitempty"`
	Review     string   `json:"review,omitempty"` // the review unit of a dry run
	Steps      int      `json:"steps"`
	Tokens     int64    `json:"tokens"`
	Writes     int      `json:"writes"`
	Proposals  int      `json:"proposals"`
	Stopped    string   `json:"stopped,omitempty"`
	Model      string   `json:"model,omitempty"`
}

// File is the whole task file.
type File struct {
	Tasks []Task           `json:"tasks"`
	State map[string]State `json:"state"`
	Runs  []Run            `json:"runs"`
}

var hhmm = regexp.MustCompile(`^([01]\d|2[0-3]):([0-5]\d)$`)

// Check validates a schedule.
func (s Spec) Check() error {
	switch s.Kind {
	case "daily", "weekly":
		if !hhmm.MatchString(s.At) {
			return errors.New(`schedule.at must be "HH:MM"`)
		}
		if s.Kind == "weekly" && (s.Weekday < 0 || s.Weekday > 6) {
			return errors.New("schedule.weekday must be 0 (Sunday) to 6")
		}
	case "hours":
		if s.Every < 1 || s.Every > 168 {
			return errors.New("schedule.every must be 1 to 168 hours")
		}
	default:
		return errors.New(`schedule.kind must be "daily", "weekly" or "hours"`)
	}
	if s.TZOffset < -14*60 || s.TZOffset > 14*60 {
		return errors.New("schedule.tz_offset must be within ±14 hours")
	}
	return nil
}

// Next is the first scheduled time strictly after t.
func (s Spec) Next(t time.Time) time.Time {
	t = t.UTC()
	if s.Kind == "hours" {
		return t.Add(time.Duration(s.Every) * time.Hour)
	}
	zone := time.FixedZone("task", s.TZOffset*60)
	local := t.In(zone)
	var h, m int
	fmt.Sscanf(s.At, "%d:%d", &h, &m)
	day := time.Date(local.Year(), local.Month(), local.Day(), h, m, 0, 0, zone)
	for i := 0; i < 9; i++ {
		cand := day.AddDate(0, 0, i)
		if !cand.After(local) {
			continue
		}
		if s.Kind == "weekly" && int(cand.Weekday()) != s.Weekday {
			continue
		}
		return cand.UTC()
	}
	return day.AddDate(0, 0, 7).UTC()
}

func parseTime(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, s)
	return t, err == nil
}

// Due reports whether task runs at now, and its next time.
func Due(task Task, st State, now time.Time) (bool, time.Time) {
	from, ok := parseTime(st.LastRunAt)
	if !ok {
		if from, ok = parseTime(task.CreatedAt); !ok {
			from = now
		}
	}
	next := task.Spec.Next(from)
	if !task.Enabled {
		return false, next
	}
	if running, ok := parseTime(st.RunningFrom); ok && now.Sub(running) < StaleRun {
		return false, next
	}
	return !next.After(now), next
}

// Normalize fills defaults and checks a task (owner and times are the caller's).
func Normalize(t *Task) error {
	t.Name = strings.TrimSpace(t.Name)
	t.Prompt = strings.TrimSpace(t.Prompt)
	if t.Name == "" || len([]rune(t.Name)) > MaxName {
		return fmt.Errorf("name is required (at most %d characters)", MaxName)
	}
	if t.Prompt == "" || len([]rune(t.Prompt)) > MaxPrompt {
		return fmt.Errorf("prompt is required (at most %d characters)", MaxPrompt)
	}
	if t.Mode == "" {
		t.Mode = "suggest"
	}
	if t.Mode != "suggest" && t.Mode != "apply" {
		return errors.New(`mode must be "suggest" or "apply"`)
	}
	if err := t.Spec.Check(); err != nil {
		return err
	}
	clampInt := func(v *int, def, hi int) {
		if *v <= 0 {
			*v = def
		}
		if *v > hi {
			*v = hi
		}
	}
	clampInt(&t.MaxSteps, DefaultSteps, MaxSteps)
	clampInt(&t.MaxWrites, DefaultWrites, MaxWrites)
	if t.MaxTokens <= 0 {
		t.MaxTokens = DefaultTokens
	}
	if t.MaxTokens > MaxTokens {
		t.MaxTokens = MaxTokens
	}
	return nil
}

// ------------------------------------------------------------ the file

var locks sync.Map // project path -> *sync.Mutex

func lockFor(dir string) *sync.Mutex {
	m, _ := locks.LoadOrStore(filepath.Clean(dir), &sync.Mutex{})
	return m.(*sync.Mutex)
}

// Exists reports whether the project has a task file.
func Exists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, FileName))
	return err == nil
}

func read(dir string) (*File, error) {
	f := &File{State: map[string]State{}}
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, f); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	if f.State == nil {
		f.State = map[string]State{}
	}
	return f, nil
}

func write(dir string, f *File) error {
	if len(f.Runs) > MaxRuns {
		f.Runs = f.Runs[len(f.Runs)-MaxRuns:]
	}
	raw, err := json.MarshalIndent(f, "", " ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, FileName+".tmp")
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, FileName))
}

// Load reads the task file.
func Load(dir string) (*File, error) {
	mu := lockFor(dir)
	mu.Lock()
	defer mu.Unlock()
	return read(dir)
}

// Update changes the task file under its lock.
func Update(dir string, fn func(f *File) error) error {
	mu := lockFor(dir)
	mu.Lock()
	defer mu.Unlock()
	f, err := read(dir)
	if err != nil {
		return err
	}
	if err := fn(f); err != nil {
		return err
	}
	return write(dir, f)
}

// Find returns the task with id.
func (f *File) Find(id string) (int, *Task) {
	for i := range f.Tasks {
		if f.Tasks[i].ID == id {
			return i, &f.Tasks[i]
		}
	}
	return -1, nil
}

// Begin marks task id as running (false when it already runs: no overlap)
// and logs the run.
func Begin(dir string, task Task, run Run, now time.Time) (bool, error) {
	started := false
	err := Update(dir, func(f *File) error {
		st := f.State[task.ID]
		if since, ok := parseTime(st.RunningFrom); ok && now.Sub(since) < StaleRun {
			return nil
		}
		st.RunningFrom = now.UTC().Format(time.RFC3339)
		f.State[task.ID] = st
		run.Status = "running"
		f.Runs = append(f.Runs, run)
		started = true
		return nil
	})
	return started && err == nil, err
}

// Finish records the end of a run.
func Finish(dir string, run Run, now time.Time) error {
	return Update(dir, func(f *File) error {
		st := f.State[run.Task]
		st.RunningFrom = ""
		st.LastRunAt = now.UTC().Format(time.RFC3339)
		st.LastStatus = run.Status
		st.LastRunID = run.ID
		st.LastError = run.Error
		if run.Status == "failed" {
			st.Failures++
		} else {
			st.Failures = 0
		}
		f.State[run.Task] = st
		for i := range f.Runs {
			if f.Runs[i].ID == run.ID {
				f.Runs[i] = run
				return nil
			}
		}
		f.Runs = append(f.Runs, run)
		return nil
	})
}

// RecoverStale clears runs left "running" by a stopped server: the run is
// logged as failed and the task becomes due by its schedule again.
func RecoverStale(dir string, now time.Time) error {
	if !Exists(dir) {
		return nil
	}
	return Update(dir, func(f *File) error {
		for id, st := range f.State {
			if st.RunningFrom == "" {
				continue
			}
			if since, ok := parseTime(st.RunningFrom); ok && now.Sub(since) < StaleRun {
				continue
			}
			st.RunningFrom = ""
			f.State[id] = st
		}
		for i := range f.Runs {
			if f.Runs[i].Status != "running" {
				continue
			}
			if since, ok := parseTime(f.Runs[i].StartedAt); ok && now.Sub(since) < StaleRun {
				continue
			}
			f.Runs[i].Status = "failed"
			f.Runs[i].Error = "the server stopped during the run"
			f.Runs[i].FinishedAt = now.UTC().Format(time.RFC3339)
		}
		return nil
	})
}

// Failing lists the enabled tasks whose last run failed.
func (f *File) Failing() []Task {
	var out []Task
	for _, t := range f.Tasks {
		if t.Enabled && f.State[t.ID].LastStatus == "failed" {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
