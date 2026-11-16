package server

// GPU routes (services module): /api/gpu/*, the agent and task-agent GPU job
// routes and the overview gpu_summary, answered by internal/gpu (a port of
// arkstudio/gpu_probe.py and vendor/gpu_server_probe).

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"sync"

	"arkstudio/go/internal/gpu"
	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/sessions"
	"arkstudio/go/internal/store"
)

var gpuPorted = []PortedRoute{
	{"exact:/api/gpu/status", "GET,HEAD"},
	{"exact:/api/gpu/servers", "GET,HEAD"},
	{"exact:/api/gpu/queue", "GET,HEAD"},
	{"exact:/api/gpu/dashboard", "GET,HEAD"},
	{"exact:/api/gpu/jobs", "GET,HEAD,POST"},
	{"regex:^/api/gpu/jobs/([^/]+)$", "GET,HEAD"},
	{"regex:^/api/gpu/jobs/([^/]+)/logs$", "GET,HEAD"},
	{"regex:^/api/gpu/jobs/([^/]+)/cancel$", "POST"},
	{"exact:/api/gpu/probe", "POST"},
	{"exact:/api/gpu/dispatch/prepare", "POST"},
	{"exact:/api/agent/v1/gpu/jobs", "GET,HEAD,POST"},
	{"regex:^/api/agent/v1/gpu/jobs/([^/]+)(/logs)?$", "GET,HEAD"},
	{"regex:^/api/agent/v1/gpu/jobs/([^/]+)/cancel$", "POST"},
	{"exact:/api/task-agent/v1/gpu/jobs", "GET,HEAD,POST"},
	{"regex:^/api/task-agent/v1/gpu/jobs/([^/]+)$", "GET,HEAD"},
	{"regex:^/api/task-agent/v1/gpu/jobs/([^/]+)/logs$", "GET,HEAD"},
	{"regex:^/api/task-agent/v1/gpu/jobs/([^/]+)/cancel$", "POST"},
}

func init() { Ported = append(Ported, gpuPorted...) }

var (
	reGPUJob        = pyRe(`^/api/gpu/jobs/([^/]+)$`, false)
	reGPULogs       = pyRe(`^/api/gpu/jobs/([^/]+)/logs$`, false)
	reGPUCancel     = pyRe(`^/api/gpu/jobs/([^/]+)/cancel$`, false)
	reTaskGPULogs   = pyRe(`^/api/task-agent/v1/gpu/jobs/([^/]+)/logs$`, false)
	reTaskGPUJob    = pyRe(`^/api/task-agent/v1/gpu/jobs/([^/]+)$`, false)
	reTaskGPUCancel = pyRe(`^/api/task-agent/v1/gpu/jobs/([^/]+)/cancel$`, false)
	reGPUDigits     = regexp.MustCompile(`\A[0-9]+\z`)

	gpuServices sync.Map // *App -> *gpu.Service (App.gpu)
)

// gpu is App.gpu (GpuService), created with the App.
func (a *App) gpu() *gpu.Service {
	if s, ok := gpuServices.Load(a); ok {
		return s.(*gpu.Service)
	}
	s, _ := gpuServices.LoadOrStore(a, &gpu.Service{Replay: os.Getenv("ARK_REPLAY_CLOCK") == "1"})
	return s.(*gpu.Service)
}

// GPU exposes App.gpu.  *gpu.Service implements resources.GPUSource
// (Servers, LastProbe, QueueJobs; none of them probes, opens SSH or starts
// the queue worker); the Resources graph reads it (routes_resources.go).
func (a *App) GPU() *gpu.Service { return a.gpu() }

// StartGPUQueue is serve()'s app.gpu.start_configured_queue().
func (a *App) StartGPUQueue() bool { return a.gpu().StartConfiguredQueue() }

// replayGPUTick runs one queue tick before a request that carries
// X-Replay-GpuTick (ARK_REPLAY_CLOCK=1 only; replayclock.py does the same).
func (a *App) replayGPUTick() {
	if q := a.gpu().QueueIfStarted(); q != nil {
		q.Tick() //nolint:errcheck // like the worker loop
	}
}

// gpuErr maps the GPU package errors like do_GET / do_POST.
func gpuErr(err error) error {
	var ge *gpu.Error
	var ve *gpu.ValueError
	var ie *gpu.InternalError
	var oe *gpu.OverflowError
	var pe *gpu.PythonError
	var re *gpu.RecursionError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ge):
		return apiErr(ge.Status, ge.Message, ge.Code)
	case errors.As(err, &ve):
		return &store.ValueError{Message: ve.Msg}
	case errors.As(err, &ie):
		return &store.InternalError{Message: ie.Msg}
	case errors.As(err, &oe):
		return &store.InternalError{Message: oe.Msg}
	case errors.As(err, &re):
		return &store.InternalError{Message: re.Error()}
	case errors.As(err, &pe):
		if pe.Type == "ValueError" {
			return &store.ValueError{Message: pe.Msg}
		}
		return &store.InternalError{Message: pe.Msg}
	}
	return err
}

// gpuQueryInt is _gpu_query_int.
func (c *reqCtx) gpuQueryInt(name string, def, low, high int64) (int64, error) {
	values, ok := c.query[name]
	if !ok {
		values = []string{itoa64(def)}
	}
	if len(values) != 1 || !reGPUDigits.MatchString(values[0]) {
		return 0, apiErr(400, name+" must be an integer between "+itoa64(low)+" and "+itoa64(high), "")
	}
	n, ok := bigDigits(values[0])
	if !ok || n < low || n > high {
		return 0, apiErr(400, name+" must be between "+itoa64(low)+" and "+itoa64(high), "")
	}
	return n, nil
}

func itoa64(n int64) string { return pyjson.Str(n) }

// bigDigits parses ASCII digits; false when beyond int64 (then out of range).
func bigDigits(s string) (int64, bool) {
	s = strings.TrimLeft(s, "0")
	if len(s) > 18 {
		return 0, false
	}
	var n int64
	for i := 0; i < len(s); i++ {
		n = n*10 + int64(s[i]-'0')
	}
	return n, true
}

// gpuListQuery is _gpu_list_query.
func (c *reqCtx) gpuListQuery() (gpu.ListFilters, error) {
	var f gpu.ListFilters
	limit, err := c.gpuQueryInt("limit", 100, 1, 500)
	if err != nil {
		return f, err
	}
	offset, err := c.gpuQueryInt("offset", 0, 0, 1_000_000)
	if err != nil {
		return f, err
	}
	f.Limit, f.Offset = limit, offset
	for _, key := range []string{"name", "name_prefix", "since", "server"} {
		values, ok := c.query[key]
		if !ok {
			continue
		}
		if len(values) != 1 {
			return f, apiErr(400, key+" accepts one value", "")
		}
		switch key {
		case "name":
			f.Name = values[0]
		case "name_prefix":
			f.NamePrefix = values[0]
		case "since":
			f.Since = values[0]
		case "server":
			f.Server = values[0]
		}
	}
	if _, ok := c.query["status"]; ok {
		var parts []string
		for _, v := range qlist(c.query, "status") {
			parts = append(parts, v.(string))
		}
		f.Status = strings.Join(parts, ",")
		if f.Status == "" {
			return f, apiErr(400, "status must contain at least one job state", "")
		}
	}
	if _, ok := c.query["ids"]; ok {
		f.HasIDs = true
		f.IDs = qlist(c.query, "ids")
		if f.IDs == nil {
			f.IDs = []any{}
		}
	}
	return f, nil
}

func (c *reqCtx) okOrErr(out *pyjson.Object, err error) error {
	if err != nil {
		return gpuErr(err)
	}
	c.ok(out)
	return nil
}

// gpuGet answers GET /api/gpu/* (Handler._api_get).
func (c *reqCtx) gpuGet(path string) (bool, error) {
	svc := c.app.gpu()
	switch path {
	case "/api/gpu/status":
		return true, c.okOrErr(svc.Status())
	case "/api/gpu/servers":
		return true, c.okOrErr(svc.Servers())
	case "/api/gpu/queue":
		return true, c.okOrErr(svc.QueueStatus())
	case "/api/gpu/dashboard":
		p, err := c.require("read")
		if err != nil {
			return true, err
		}
		return true, c.okOrErr(svc.Dashboard(p.ID()))
	case "/api/gpu/jobs":
		if _, err := c.require("read"); err != nil {
			return true, err
		}
		p, err := c.require("read")
		if err != nil {
			return true, err
		}
		f, err := c.gpuListQuery()
		if err != nil {
			return true, err
		}
		f.Project = p.ID()
		return true, c.okOrErr(svc.QueueList(f))
	}
	if m := reGPUJob.FindStringSubmatch(path); m != nil {
		job, err := c.projectGPUJob(m[1], "read")
		if err != nil {
			return true, err
		}
		c.ok(pyjson.NewObject("job", job))
		return true, nil
	}
	if m := reGPULogs.FindStringSubmatch(path); m != nil {
		if _, err := c.projectGPUJob(m[1], "read"); err != nil {
			return true, err
		}
		tail, err := c.gpuQueryInt("tail", 200, 1, 500)
		if err != nil {
			return true, err
		}
		return true, c.okOrErr(svc.QueueLogs(m[1], tail))
	}
	return false, nil
}

// projectGPUJob is Handler._project_gpu_job.
func (c *reqCtx) projectGPUJob(id, permission string) (*pyjson.Object, error) {
	p, err := c.require(permission)
	if err != nil {
		return nil, err
	}
	job, err := c.app.gpu().QueueGet(id)
	if err != nil {
		return nil, gpuErr(err)
	}
	if pyjson.Str(orStrAny(job.Value("project"))) != p.ID() {
		return nil, apiErr(404, "GPU job not found", "GPU_JOB_NOT_FOUND")
	}
	return job, nil
}

func orStrAny(v any) any {
	if !pyjson.Truthy(v) {
		return ""
	}
	return v
}

// submitProjectGPUJob is Handler._submit_project_gpu_job.
func (c *reqCtx) submitProjectGPUJob(body *pyjson.Object) error {
	p, err := c.require("write")
	if err != nil {
		return err
	}
	task := strOr(body.Value("task"))
	if task == "" {
		task = strOr(body.Value("task_ref"))
	}
	job, err := c.app.gpu().QueueSubmit(body, c.actor(), p.ID(), strOr(body.Value("session")), task)
	if err != nil {
		return gpuErr(err)
	}
	c.ok(pyjson.NewObject("job", job))
	return nil
}

func (c *reqCtx) cancelProjectGPUJob(id string) error {
	if _, err := c.projectGPUJob(id, "write"); err != nil {
		return err
	}
	job, err := c.app.gpu().QueueCancel(id)
	if err != nil {
		return gpuErr(err)
	}
	c.ok(pyjson.NewObject("job", job))
	return nil
}

// gpuPost answers POST /api/gpu/* (do_POST, after the JSON body).
func (c *reqCtx) gpuPost(path string, body *pyjson.Object) (bool, error) {
	switch path {
	case "/api/gpu/jobs":
		return true, c.submitProjectGPUJob(body)
	case "/api/gpu/probe":
		return true, c.okOrErr(c.app.gpu().Probe(body))
	case "/api/gpu/dispatch/prepare":
		return true, c.okOrErr(c.app.gpu().PrepareDispatch(body))
	}
	if m := reGPUCancel.FindStringSubmatch(path); m != nil {
		return true, c.cancelProjectGPUJob(m[1])
	}
	return false, nil
}

// agentGPUGet answers GET /api/agent/v1/gpu/jobs[/<id>[/logs]].
func (c *reqCtx) agentGPUGet(path string, projectID string) error {
	if path == "/api/agent/v1/gpu/jobs" {
		f, err := c.gpuListQuery()
		if err != nil {
			return err
		}
		f.Project = projectID
		return c.okOrErr(c.app.gpu().QueueList(f))
	}
	m := reAgentGPUJob.FindStringSubmatch(path)
	job, err := c.projectGPUJob(m[1], "read")
	if err != nil {
		return err
	}
	if m[2] == "" {
		c.ok(pyjson.NewObject("job", job))
		return nil
	}
	tail, err := c.gpuQueryInt("tail", 200, 1, 500)
	if err != nil {
		return err
	}
	return c.okOrErr(c.app.gpu().QueueLogs(pyjson.Str(job.Value("id")), tail))
}

// agentGPUPost answers POST /api/agent/v1/gpu/jobs and .../cancel.
func (c *reqCtx) agentGPUPost(path string, body *pyjson.Object) (bool, error) {
	if path == "/api/agent/v1/gpu/jobs" {
		return true, c.submitProjectGPUJob(body)
	}
	if m := reAgentGPUCancel.FindStringSubmatch(path); m != nil {
		return true, c.cancelProjectGPUJob(m[1])
	}
	return false, nil
}

func taskRef(s *sessions.Session) string {
	ref, _ := s.Meta.Value("project").(*pyjson.Object)
	if ref == nil {
		return ""
	}
	return strOr(ref.Value("id"))
}

// taskGPUJob is the queue_get plus project/session check of the task-agent routes.
func (c *reqCtx) taskGPUJob(s *sessions.Session, id string) (*pyjson.Object, error) {
	job, err := c.app.gpu().QueueGet(id)
	if err != nil {
		return nil, gpuErr(err)
	}
	if strOr(job.Value("project")) != taskRef(s) || strOr(job.Value("session")) != s.ID {
		return nil, apiErr(404, "GPU job not found", "GPU_JOB_NOT_FOUND")
	}
	return job, nil
}

// taskGPUGet answers GET /api/task-agent/v1/gpu/... (false: unknown path).
func (c *reqCtx) taskGPUGet(path string, s *sessions.Session) (bool, error) {
	if path == "/api/task-agent/v1/gpu/jobs" {
		f, err := c.gpuListQuery()
		if err != nil {
			return true, err
		}
		f.Owner, f.Project, f.Session = strOr(s.Meta.Value("actor")), taskRef(s), s.ID
		return true, c.okOrErr(c.app.gpu().QueueList(f))
	}
	if m := reTaskGPULogs.FindStringSubmatch(path); m != nil {
		if _, err := c.taskGPUJob(s, m[1]); err != nil {
			return true, err
		}
		tail, err := c.gpuQueryInt("tail", 200, 1, 500)
		if err != nil {
			return true, err
		}
		return true, c.okOrErr(c.app.gpu().QueueLogs(m[1], tail))
	}
	if m := reTaskGPUJob.FindStringSubmatch(path); m != nil {
		job, err := c.taskGPUJob(s, m[1])
		if err != nil {
			return true, err
		}
		c.ok(pyjson.NewObject("job", job))
		return true, nil
	}
	return false, nil
}

// taskGPUPost is do_POST's task-agent GPU block (before the same-origin
// check); handled false for other paths.
func (c *reqCtx) taskGPUPost(path string) (bool, error) {
	cancel := reTaskGPUCancel.FindStringSubmatch(path)
	if path != "/api/task-agent/v1/gpu/jobs" && cancel == nil {
		return false, nil
	}
	s, err := c.requireTaskAgent()
	if err != nil {
		return true, err
	}
	if pyjson.Str(orStrAny(s.Meta.Value("access_mode"))) != "full_access" {
		return true, apiErr(403, "GPU queue dispatch requires a full-access task session.", "GPU_QUEUE_PERMISSION")
	}
	c.bindTaskSession(s)
	if cancel != nil {
		if _, err := c.require("write"); err != nil {
			return true, err
		}
		if _, err := c.taskGPUJob(s, cancel[1]); err != nil {
			return true, err
		}
		job, err := c.app.gpu().QueueCancel(cancel[1])
		if err != nil {
			return true, gpuErr(err)
		}
		c.ok(pyjson.NewObject("job", job))
		return true, nil
	}
	body, err := c.readJSON()
	if err != nil {
		return true, err
	}
	if _, err := c.require("write"); err != nil {
		return true, err
	}
	project := taskRef(s)
	if project == "" {
		p, err := c.require("read")
		if err != nil {
			return true, err
		}
		project = p.ID()
	}
	task := strOr(s.Meta.Value("task_ref"))
	if task == "" {
		task = strOr(body.Value("task"))
	}
	job, err := c.app.gpu().QueueSubmit(body, strOr(s.Meta.Value("actor")), project, s.ID, task)
	if err != nil {
		return true, gpuErr(err)
	}
	c.ok(pyjson.NewObject("job", job))
	return true, nil
}
