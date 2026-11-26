package server

// Scheduled Assistant tasks (Go only, 2026-11-26; internal/schedules,
// chat/scheduled.go).  The user: "对于日常维护，可以使用assistant的api.
// 比如建立一些定时任务（在设置里面）定时任务有固定的提示词，让assistant去维护这个图。"
//
//   GET  /api/project/schedules             tasks with state and next run,
//   GET  /api/agent/v1/schedules            recent runs, templates, failing
//                                           tasks (read access)
//   POST /api/project/schedules             {action: save, task} | {action:
//   POST /api/agent/v1/schedules            delete, id} | {action: run, id}
//                                           (project admins; the saving
//                                           admin becomes the task's owner)
//   GET  /api/project/schedules/transcript  ?run=<id>: one run's messages
//
// A task runs as its owner: the owner's Assistant model, the owner's access
// (a run fails when the owner is no longer an admin of the project), writes
// logged as "assistant:schedule:<id>".  Suggest mode (the default) changes
// nothing and files its proposals as one review unit.  The scheduler
// (StartSchedules) looks at every project once a minute; a task never runs
// twice at once, and after a restart a missed task runs once.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"arkstudio/go/internal/assistcfg"
	"arkstudio/go/internal/chat"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/rnd"
	"arkstudio/go/internal/schedules"
	"arkstudio/go/internal/store"
)

const (
	schedulesPath       = "/api/project/schedules"
	scheduleTranscript  = "/api/project/schedules/transcript"
	agentSchedulesPath  = "/api/agent/v1/schedules"
	scheduleActorPrefix = "assistant:schedule:"
)

var scheduleRoutesGoOnly = []PortedRoute{
	{"goonly:exact:" + schedulesPath, "GET,HEAD,POST"},
	{"goonly:exact:" + scheduleTranscript, "GET,HEAD"},
	{"goonly:exact:" + agentSchedulesPath, "GET,HEAD,POST"},
}

// scheduleRunner is swapped in tests (nil: the Assistant runs).
var scheduleRunner func(env *chat.Env, req chat.ScheduledRequest) (*chat.ScheduledResult, error)

// schedulesGet answers both GET routes.
func (c *reqCtx) schedulesGet() error {
	p, err := c.require("read")
	if err != nil {
		return err
	}
	perm, err := c.projectPermission()
	if err != nil {
		return err
	}
	f, err := schedules.Load(p.Path)
	if err != nil {
		return err
	}
	c.ok(schedulesPayload(f, perm == "admin", clock.Now()))
	return nil
}

func scheduleJSON(v any) any {
	raw, _ := json.Marshal(v)
	out, err := pyjson.Loads(string(raw))
	if err != nil {
		return nil
	}
	return out
}

func schedulesPayload(f *schedules.File, canEdit bool, now time.Time) *pyjson.Object {
	tasks := []any{}
	for _, t := range f.Tasks {
		st := f.State[t.ID]
		_, next := schedules.Due(t, st, now)
		o := scheduleJSON(t).(*pyjson.Object)
		o.Set("state", scheduleJSON(st)).Set("next_at", next.UTC().Format(time.RFC3339))
		tasks = append(tasks, o)
	}
	runs := []any{}
	for i := len(f.Runs) - 1; i >= 0 && len(runs) < 30; i-- {
		runs = append(runs, scheduleJSON(f.Runs[i]))
	}
	failing := []any{}
	for _, t := range f.Failing() {
		st := f.State[t.ID]
		failing = append(failing, pyjson.NewObject("id", t.ID, "name", t.Name, "error", st.LastError, "at", st.LastRunAt,
			"failures", int64(st.Failures)))
	}
	return pyjson.NewObject("tasks", tasks, "runs", runs, "failing", failing, "templates", scheduleJSON(schedules.Templates),
		"can_edit", canEdit, "limits", pyjson.NewObject("max_steps", int64(schedules.MaxSteps), "max_writes", int64(schedules.MaxWrites),
			"max_tokens", int64(schedules.MaxTokens)))
}

// schedulesPost answers both POST routes (project admins).
func (c *reqCtx) schedulesPost(body *pyjson.Object) error {
	p, err := c.require("admin")
	if err != nil {
		return err
	}
	owner := c.actor()
	now := clock.Now().UTC()
	switch pyjson.Str(body.Value("action")) {
	case "save":
		raw := pyjson.Dumps(body.Value("task"))
		var t schedules.Task
		if err := json.Unmarshal(raw, &t); err != nil {
			return apiErr(400, "task: "+err.Error(), "")
		}
		if err := schedules.Normalize(&t); err != nil {
			return apiErr(400, err.Error(), "")
		}
		var saved schedules.Task
		err := schedules.Update(p.Path, func(f *schedules.File) error {
			stamp := now.Format(time.RFC3339)
			t.Owner = owner // whoever saves a task last is the admin it runs as
			t.UpdatedAt = stamp
			if t.ID == "" {
				if len(f.Tasks) >= schedules.MaxTasks {
					return apiErr(400, fmt.Sprintf("at most %d scheduled tasks per project", schedules.MaxTasks), "")
				}
				t.ID = "st_" + rnd.Hex("schedule", 4)
				t.CreatedAt = stamp
				f.Tasks = append(f.Tasks, t)
			} else {
				_, old := f.Find(t.ID)
				if old == nil {
					return apiErr(404, "scheduled task not found: "+t.ID, "")
				}
				t.CreatedAt = old.CreatedAt
				*old = t
			}
			saved = t
			return nil
		})
		if err != nil {
			return err
		}
		if err := p.Store.LogActivity(c.ctx, c.writeActor(), "schedule.save", nil,
			pyjson.NewObject("id", saved.ID, "name", saved.Name, "mode", saved.Mode, "enabled", saved.Enabled)); err != nil {
			return err
		}
	case "delete":
		id := pyjson.Str(body.Value("id"))
		err := schedules.Update(p.Path, func(f *schedules.File) error {
			i, t := f.Find(id)
			if t == nil {
				return apiErr(404, "scheduled task not found: "+id, "")
			}
			f.Tasks = append(f.Tasks[:i], f.Tasks[i+1:]...)
			delete(f.State, id)
			return nil
		})
		if err != nil {
			return err
		}
		if err := p.Store.LogActivity(c.ctx, c.writeActor(), "schedule.delete", nil, pyjson.NewObject("id", id)); err != nil {
			return err
		}
	case "run":
		id := pyjson.Str(body.Value("id"))
		f, err := schedules.Load(p.Path)
		if err != nil {
			return err
		}
		_, t := f.Find(id)
		if t == nil {
			return apiErr(404, "scheduled task not found: "+id, "")
		}
		run, started, err := c.app.beginSchedule(p, *t, "manual")
		if err != nil {
			return err
		}
		if !started {
			return apiErr(409, "this task is running already", "SCHEDULE_RUNNING")
		}
		go c.app.runSchedule(context.Background(), p, *t, run)
	default:
		return apiErr(400, `action must be "save", "delete" or "run"`, "")
	}
	f, err := schedules.Load(p.Path)
	if err != nil {
		return err
	}
	c.ok(schedulesPayload(f, true, now))
	return nil
}

// scheduleTranscriptGet is GET /api/project/schedules/transcript?run=.
func (c *reqCtx) scheduleTranscriptGet() error {
	p, err := c.require("read")
	if err != nil {
		return err
	}
	id := c.query.First("run", "")
	if !strings.HasPrefix(id, "sr_") || strings.ContainsAny(id, `/\.`) {
		return apiErr(400, "run is required", "")
	}
	raw, err := os.ReadFile(filepath.Join(p.Path, "logs", "schedules", id+".json"))
	if err != nil {
		return apiErr(404, "no transcript for this run", "")
	}
	data, err := pyjson.Loads(string(raw))
	if err != nil {
		return err
	}
	c.ok(pyjson.NewObject("run", id, "transcript", data))
	return nil
}

// ------------------------------------------------------------ running

func (a *App) beginSchedule(p *project.Project, t schedules.Task, trigger string) (schedules.Run, bool, error) {
	now := clock.Now().UTC()
	run := schedules.Run{ID: "sr_" + rnd.Hex("schedule-run", 6), Task: t.ID, Name: t.Name, Trigger: trigger, Mode: t.Mode,
		Owner: t.Owner, StartedAt: now.Format(time.RFC3339)}
	started, err := schedules.Begin(p.Path, t, run, now)
	return run, started, err
}

// scheduleCtx is a request context acting as the task's owner.
func (a *App) scheduleCtx(ctx context.Context, p *project.Project, owner string) *reqCtx {
	return &reqCtx{app: a, ctx: ctx, user: owner, project: p, resolved: true}
}

// runSchedule runs one begun task and records the result.
func (a *App) runSchedule(ctx context.Context, p *project.Project, t schedules.Task, run schedules.Run) {
	result, err := a.executeSchedule(ctx, p, t, &run)
	now := clock.Now().UTC()
	run.FinishedAt = now.Format(time.RFC3339)
	switch {
	case err != nil:
		run.Status, run.Error = "failed", truncate(err.Error(), 600)
	case result.Stopped != "":
		run.Status = "stopped"
	default:
		run.Status = "ok"
	}
	if result != nil {
		run.Summary = truncate(result.Message, 2000)
		run.Touched, run.Steps, run.Tokens = result.Touched, result.Steps, result.Tokens
		run.Writes, run.Proposals, run.Stopped, run.Model = len(result.Writes), len(result.Proposals), result.Stopped, result.Model
		if dir := filepath.Join(p.Path, "logs", "schedules"); os.MkdirAll(dir, 0o755) == nil {
			if raw := pyjson.Dumps(result.Transcript); len(raw) > 0 {
				_ = os.WriteFile(filepath.Join(dir, run.ID+".json"), raw, 0o644)
			}
		}
	}
	_ = schedules.Finish(p.Path, run, now)
	_ = p.Store.LogActivity(ctx, scheduleActorPrefix+t.ID, "schedule.run", nil, pyjson.NewObject("id", t.ID, "name", t.Name,
		"run", run.ID, "status", run.Status, "mode", t.Mode, "touched", int64(len(run.Touched)), "review", run.Review, "error", run.Error))
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func (a *App) executeSchedule(ctx context.Context, p *project.Project, t schedules.Task, run *schedules.Run) (*chat.ScheduledResult, error) {
	c := a.scheduleCtx(ctx, p, t.Owner)
	perm, err := c.projectPermission()
	if err != nil {
		return nil, err
	}
	if perm != "admin" {
		return nil, fmt.Errorf("%s is no longer an admin of this project; an admin must save the task again", t.Owner)
	}
	summary, err := p.Summary()
	if err != nil {
		return nil, err
	}
	global, err := assistcfg.Load()
	if err != nil {
		return nil, err
	}
	config, err := a.Org.ResolveAssistant(ctx, summary, t.Owner, global)
	if err != nil {
		return nil, err
	}
	actor := scheduleActorPrefix + t.ID
	who := c.who(p)
	env := &chat.Env{Ctx: ctx, Store: p.Store, ProjectID: p.ID(), ProjectName: p.Name(), ProjectPath: p.Path, Config: config,
		AgentToken: a.AgentToken(), Who: who, Actor: actor}
	req := chat.ScheduledRequest{Name: t.Name, Prompt: t.Prompt, Suggest: t.Mode != "apply", MaxSteps: t.MaxSteps,
		MaxWrites: t.MaxWrites, MaxTokens: t.MaxTokens, Extra: scheduleTools(ctx, p.Store, actor, who)}
	runner := scheduleRunner
	if runner == nil {
		runner = func(env *chat.Env, req chat.ScheduledRequest) (*chat.ScheduledResult, error) {
			return env.ScheduledRun(req)
		}
	}
	result, err := runner(env, req)
	if err != nil {
		if result == nil {
			return nil, err
		}
		return result, err
	}
	if req.Suggest && len(result.Proposals) > 0 {
		uid, err := fileProposals(ctx, p.Store, t, *run, result, actor, who)
		if err != nil {
			return result, err
		}
		run.Review = uid
	}
	return result, nil
}

// fileProposals writes a dry run's proposals as one review unit.
func fileProposals(ctx context.Context, st *store.Store, t schedules.Task, run schedules.Run, r *chat.ScheduledResult, actor string, who store.Who) (string, error) {
	var b strings.Builder
	b.WriteString("## 建议的改动 {#changes}\n\n")
	for i, op := range r.Proposals {
		b.WriteString(fmt.Sprintf("%d. %s\n", i+1, proposalLine(op)))
	}
	b.WriteString("\n## 总结 {#summary}\n\n")
	b.WriteString(strings.TrimSpace(r.Message))
	b.WriteString(fmt.Sprintf("\n\n## 运行 {#run}\n\n定时任务「%s」（%s），以 %s 的身份试运行（只建议，不改动）；运行 %s，%d 次模型调用、%d tokens。\n\n```json\n",
		t.Name, t.ID, t.Owner, run.ID, r.Steps, r.Tokens))
	raw := pyjson.Dumps(r.Proposals)
	b.Write(raw)
	b.WriteString("\n```\n")
	title := fmt.Sprintf("定时任务建议：%s · %s", t.Name, clock.Now().UTC().Format("2006-01-02"))
	note := fmt.Sprintf("看 [建议的改动](#changes)：定时任务「%s」提出 %d 项改动，都还没有执行；接受后由人或 agent 照此修改。", t.Name, len(r.Proposals))
	unit, err := st.CreateUnit(ctx, pyjson.NewObject("type", "artifact", "title", title, "body", b.String(), "status", "review",
		"importance", "p3", "origin", "schedule", "review_note", note), actor, who)
	if err != nil {
		return "", err
	}
	return pyjson.Str(unit.Value("uid")), nil
}

func proposalLine(v any) string {
	o, _ := v.(*pyjson.Object)
	if o == nil {
		return pyjson.Str(v)
	}
	switch pyjson.Str(o.Value("op")) {
	case "create":
		return fmt.Sprintf("新建 %s「%s」（%s）", pyjson.Str(o.Value("type")), pyjson.Str(o.Value("title")), pyjson.Str(o.Value("ref")))
	case "update":
		return fmt.Sprintf("修改 %s：%s", pyjson.Str(o.Value("uid")), string(pyjson.Dumps(o)))
	case "link":
		return fmt.Sprintf("关联 %s —%s→ %s", pyjson.Str(o.Value("source")), pyjson.Str(o.Value("relation")), pyjson.Str(o.Value("target")))
	case "add_tags":
		args, _ := o.Value("args").(*pyjson.Object)
		return fmt.Sprintf("给 %s 加标签 %s", pyjson.Str(args.Value("ref")), string(pyjson.Dumps(args.Value("tags"))))
	}
	return string(pyjson.Dumps(o))
}

// scheduleTools are the extra tools of a run: the line helpers and a tag
// writer that only adds.
func scheduleTools(ctx context.Context, st *store.Store, actor string, who store.Who) []chat.ExtraTool {
	spec := func(name, desc string, props *pyjson.Object, required ...any) *pyjson.Object {
		params := pyjson.NewObject("type", "object", "properties", props)
		if len(required) > 0 {
			params.Set("required", required)
		}
		return pyjson.NewObject("type", "function", "function", pyjson.NewObject("name", name, "description", desc, "parameters", params))
	}
	str := pyjson.NewObject("type", "string")
	return []chat.ExtraTool{
		{Name: "branch_status", Spec: spec("branch_status", "List the project's lines of work (line tags such as branch:<name>; the tag namespace is prefix): size, open units, waiting items, last activity, status (active, quiet, stalled, done) and their topic.", pyjson.NewObject()),
			Run: func(*pyjson.Object) (*pyjson.Object, error) { return st.Branches(ctx) }},
		{Name: "suggest_line_tags", Spec: spec("suggest_line_tags", "Line tags proposed for units without one, from part_of links: inherit (the nearest tagged ancestor's tags) and new_lines (an untagged goal with several units below it). Read only.", pyjson.NewObject()),
			Run: func(*pyjson.Object) (*pyjson.Object, error) {
				out, err := st.BranchSuggestions(ctx, store.BranchSuggestOptions{Limit: 80})
				return out, err
			}},
		{Name: "orphan_units", Spec: spec("orphan_units", "Units (not topics) without a part_of or derived_from parent, newest first.", pyjson.NewObject("limit", pyjson.NewObject("type", "integer", "minimum", 1, "maximum", 200))),
			Run: func(args *pyjson.Object) (*pyjson.Object, error) {
				n, _ := args.Value("limit").(int64)
				return st.OrphanUnits(ctx, int(n))
			}},
		{Name: "add_tags", Write: true, Spec: spec("add_tags", "Add tags to one unit (other tags stay). Use for line tags.", pyjson.NewObject("ref", str, "tags", pyjson.NewObject("type", "array", "items", str)), "ref", "tags"),
			Run: func(args *pyjson.Object) (*pyjson.Object, error) {
				var tags []string
				list, _ := args.Value("tags").([]any)
				for _, x := range list {
					if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
						tags = append(tags, strings.TrimSpace(s))
					}
				}
				ref := strings.TrimSpace(pyjson.Str(args.Value("ref")))
				if ref == "" || len(tags) == 0 {
					return pyjson.NewObject("error", "ref and tags are required"), nil
				}
				if _, err := st.BatchUpdate(ctx, []store.BatchItem{{Ref: ref, TagsAdd: tags}}, "", actor, who); err != nil {
					return nil, err
				}
				return pyjson.NewObject("uid", ref, "added", list), nil
			}},
	}
}

// ------------------------------------------------------------ scheduler

var scheduleLoopOnce sync.Once

// StartSchedules checks every project for due tasks every `every` until ctx
// ends.  Runs left "running" by a stopped server are closed first.
func (a *App) StartSchedules(ctx context.Context, every time.Duration) {
	scheduleLoopOnce.Do(func() {
		go func() {
			a.scheduleTick(ctx, true)
			ticker := time.NewTicker(every)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					a.scheduleTick(ctx, false)
				}
			}
		}()
	})
}

// scheduleTick starts the due tasks of every project (recover: first tick).
func (a *App) scheduleTick(ctx context.Context, recover bool) {
	rows, err := a.Org.ListProjects(ctx)
	if err != nil {
		return
	}
	now := clock.Now().UTC()
	for _, row := range rows {
		path := row.Str("path")
		if path == "" || !schedules.Exists(path) {
			continue
		}
		if recover {
			_ = schedules.RecoverStale(path, now)
		}
		f, err := schedules.Load(path)
		if err != nil || len(f.Tasks) == 0 {
			continue
		}
		var p *project.Project
		for _, t := range f.Tasks {
			if due, _ := schedules.Due(t, f.State[t.ID], now); !due {
				continue
			}
			if p == nil {
				if p = a.nudgeProject(ctx, row.Str("id")); p == nil {
					break
				}
			}
			run, started, err := a.beginSchedule(p, t, "schedule")
			if err != nil || !started {
				continue
			}
			go a.runSchedule(ctx, p, t, run)
		}
	}
}
