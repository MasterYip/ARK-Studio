// Package gpu is the HTTP side of the GPU service: /api/gpu/*, the agent and
// task-agent GPU job routes (a port of the matching branches of
// arkstudio/server.py over internal/gpu, the port of gpu_probe.py and
// vendor/gpu_server_probe).  internal/server registers it (router.go).
package gpu

import (
	"errors"
	"regexp"
	"strings"

	"arkstudio/go/internal/api"
	"arkstudio/go/internal/compat/pyjson"
	gpudom "arkstudio/go/internal/gpu"
	"arkstudio/go/internal/platform/pyerr"
	"arkstudio/go/internal/sessions"
)

// Handlers answers the GPU routes for one server.
type Handlers struct {
	// Svc is App.gpu (one GpuService per server).
	Svc func() *gpudom.Service
}

var (
	reJob         = api.PyRe(`^/api/gpu/jobs/([^/]+)$`, false)
	reLogs        = api.PyRe(`^/api/gpu/jobs/([^/]+)/logs$`, false)
	reCancel      = api.PyRe(`^/api/gpu/jobs/([^/]+)/cancel$`, false)
	reTaskLogs    = api.PyRe(`^/api/task-agent/v1/gpu/jobs/([^/]+)/logs$`, false)
	reTaskJob     = api.PyRe(`^/api/task-agent/v1/gpu/jobs/([^/]+)$`, false)
	reTaskCancel  = api.PyRe(`^/api/task-agent/v1/gpu/jobs/([^/]+)/cancel$`, false)
	reAgentJob    = api.PyRe(`^/api/agent/v1/gpu/jobs/([^/]+)(/logs)?$`, false)
	reAgentCancel = api.PyRe(`^/api/agent/v1/gpu/jobs/([^/]+)/cancel$`, false)
	reDigits      = regexp.MustCompile(`\A[0-9]+\z`)
)

// IsAgentJobPath reports a GET /api/agent/v1/gpu/jobs/<id>[/logs] path.
func IsAgentJobPath(path string) bool { return reAgentJob.MatchString(path) }

// gpuErr maps the GPU package errors like do_GET / do_POST.
func Err(err error) error {
	var ge *gpudom.Error
	var ve *gpudom.ValueError
	var ie *gpudom.InternalError
	var oe *gpudom.OverflowError
	var pe *gpudom.PythonError
	var re *gpudom.RecursionError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ge):
		return api.Err(ge.Status, ge.Message, ge.Code)
	case errors.As(err, &ve):
		return &pyerr.ValueError{Message: ve.Msg}
	case errors.As(err, &ie):
		return &pyerr.InternalError{Message: ie.Msg}
	case errors.As(err, &oe):
		return &pyerr.InternalError{Message: oe.Msg}
	case errors.As(err, &re):
		return &pyerr.InternalError{Message: re.Error()}
	case errors.As(err, &pe):
		if pe.Type == "ValueError" {
			return &pyerr.ValueError{Message: pe.Msg}
		}
		return &pyerr.InternalError{Message: pe.Msg}
	}
	return err
}

// gpuQueryInt is _gpu_query_int.
func queryInt(c api.Ctx, name string, def, low, high int64) (int64, error) {
	values, ok := c.Query()[name]
	if !ok {
		values = []string{itoa64(def)}
	}
	if len(values) != 1 || !reDigits.MatchString(values[0]) {
		return 0, api.Err(400, name+" must be an integer between "+itoa64(low)+" and "+itoa64(high), "")
	}
	n, ok := bigDigits(values[0])
	if !ok || n < low || n > high {
		return 0, api.Err(400, name+" must be between "+itoa64(low)+" and "+itoa64(high), "")
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
func listQuery(c api.Ctx) (gpudom.ListFilters, error) {
	var f gpudom.ListFilters
	limit, err := queryInt(c, "limit", 100, 1, 500)
	if err != nil {
		return f, err
	}
	offset, err := queryInt(c, "offset", 0, 0, 1_000_000)
	if err != nil {
		return f, err
	}
	f.Limit, f.Offset = limit, offset
	for _, key := range []string{"name", "name_prefix", "since", "server"} {
		values, ok := c.Query()[key]
		if !ok {
			continue
		}
		if len(values) != 1 {
			return f, api.Err(400, key+" accepts one value", "")
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
	if _, ok := c.Query()["status"]; ok {
		var parts []string
		for _, v := range api.QList(c.Query(), "status") {
			parts = append(parts, v.(string))
		}
		f.Status = strings.Join(parts, ",")
		if f.Status == "" {
			return f, api.Err(400, "status must contain at least one job state", "")
		}
	}
	if _, ok := c.Query()["ids"]; ok {
		f.HasIDs = true
		f.IDs = api.QList(c.Query(), "ids")
		if f.IDs == nil {
			f.IDs = []any{}
		}
	}
	return f, nil
}

// reply sends a service result: 200 with the object, or the mapped error.
func reply(c api.Ctx) func(*pyjson.Object, error) error {
	return func(out *pyjson.Object, err error) error {
		if err != nil {
			return Err(err)
		}
		c.OK(out)
		return nil
	}
}

// gpuGet answers GET /api/gpu/* (Handler._api_get).
func (h Handlers) Get(c api.Ctx, path string) (bool, error) {
	svc := h.Svc()
	switch path {
	case "/api/gpu/status":
		return true, reply(c)(svc.Status())
	case "/api/gpu/servers":
		return true, reply(c)(svc.Servers())
	case "/api/gpu/queue":
		return true, reply(c)(svc.QueueStatus())
	case "/api/gpu/dashboard":
		p, err := c.Require("read")
		if err != nil {
			return true, err
		}
		return true, reply(c)(svc.Dashboard(p.ID()))
	case "/api/gpu/jobs":
		if _, err := c.Require("read"); err != nil {
			return true, err
		}
		p, err := c.Require("read")
		if err != nil {
			return true, err
		}
		f, err := listQuery(c)
		if err != nil {
			return true, err
		}
		f.Project = p.ID()
		return true, reply(c)(svc.QueueList(f))
	}
	if m := reJob.FindStringSubmatch(path); m != nil {
		job, err := h.projectJob(c, m[1], "read")
		if err != nil {
			return true, err
		}
		c.OK(pyjson.NewObject("job", job))
		return true, nil
	}
	if m := reLogs.FindStringSubmatch(path); m != nil {
		if _, err := h.projectJob(c, m[1], "read"); err != nil {
			return true, err
		}
		tail, err := queryInt(c, "tail", 200, 1, 500)
		if err != nil {
			return true, err
		}
		return true, reply(c)(svc.QueueLogs(m[1], tail))
	}
	return false, nil
}

// projectGPUJob is Handler._project_gpu_job.
func (h Handlers) projectJob(c api.Ctx, id, permission string) (*pyjson.Object, error) {
	p, err := c.Require(permission)
	if err != nil {
		return nil, err
	}
	job, err := h.Svc().QueueGet(id)
	if err != nil {
		return nil, Err(err)
	}
	if pyjson.Str(orStrAny(job.Value("project"))) != p.ID() {
		return nil, api.Err(404, "GPU job not found", "GPU_JOB_NOT_FOUND")
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
func (h Handlers) submitProjectJob(c api.Ctx, body *pyjson.Object) error {
	p, err := c.Require("write")
	if err != nil {
		return err
	}
	task := api.StrOr(body.Value("task"))
	if task == "" {
		task = api.StrOr(body.Value("task_ref"))
	}
	job, err := h.Svc().QueueSubmit(body, c.Actor(), p.ID(), api.StrOr(body.Value("session")), task)
	if err != nil {
		return Err(err)
	}
	c.OK(pyjson.NewObject("job", job))
	return nil
}

func (h Handlers) cancelProjectJob(c api.Ctx, id string) error {
	if _, err := h.projectJob(c, id, "write"); err != nil {
		return err
	}
	job, err := h.Svc().QueueCancel(id)
	if err != nil {
		return Err(err)
	}
	c.OK(pyjson.NewObject("job", job))
	return nil
}

// gpuPost answers POST /api/gpu/* (do_POST, after the JSON body).
func (h Handlers) Post(c api.Ctx, path string, body *pyjson.Object) (bool, error) {
	switch path {
	case "/api/gpu/jobs":
		return true, h.submitProjectJob(c, body)
	case "/api/gpu/probe":
		return true, reply(c)(h.Svc().Probe(body))
	case "/api/gpu/dispatch/prepare":
		return true, reply(c)(h.Svc().PrepareDispatch(body))
	}
	if m := reCancel.FindStringSubmatch(path); m != nil {
		return true, h.cancelProjectJob(c, m[1])
	}
	return false, nil
}

// agentGPUGet answers GET /api/agent/v1/gpu/jobs[/<id>[/logs]].
func (h Handlers) AgentGet(c api.Ctx, path string, projectID string) error {
	if path == "/api/agent/v1/gpu/jobs" {
		f, err := listQuery(c)
		if err != nil {
			return err
		}
		f.Project = projectID
		return reply(c)(h.Svc().QueueList(f))
	}
	m := reAgentJob.FindStringSubmatch(path)
	job, err := h.projectJob(c, m[1], "read")
	if err != nil {
		return err
	}
	if m[2] == "" {
		c.OK(pyjson.NewObject("job", job))
		return nil
	}
	tail, err := queryInt(c, "tail", 200, 1, 500)
	if err != nil {
		return err
	}
	return reply(c)(h.Svc().QueueLogs(pyjson.Str(job.Value("id")), tail))
}

// agentGPUPost answers POST /api/agent/v1/gpu/jobs and .../cancel.
func (h Handlers) AgentPost(c api.Ctx, path string, body *pyjson.Object) (bool, error) {
	if path == "/api/agent/v1/gpu/jobs" {
		return true, h.submitProjectJob(c, body)
	}
	if m := reAgentCancel.FindStringSubmatch(path); m != nil {
		return true, h.cancelProjectJob(c, m[1])
	}
	return false, nil
}

func taskRef(s *sessions.Session) string {
	ref, _ := s.Meta.Value("project").(*pyjson.Object)
	if ref == nil {
		return ""
	}
	return api.StrOr(ref.Value("id"))
}

// taskGPUJob is the queue_get plus project/session check of the task-agent routes.
func (h Handlers) taskJob(s *sessions.Session, id string) (*pyjson.Object, error) {
	job, err := h.Svc().QueueGet(id)
	if err != nil {
		return nil, Err(err)
	}
	if api.StrOr(job.Value("project")) != taskRef(s) || api.StrOr(job.Value("session")) != s.ID {
		return nil, api.Err(404, "GPU job not found", "GPU_JOB_NOT_FOUND")
	}
	return job, nil
}

// taskGPUGet answers GET /api/task-agent/v1/gpu/... (false: unknown path).
func (h Handlers) TaskGet(c api.Ctx, path string, s *sessions.Session) (bool, error) {
	if path == "/api/task-agent/v1/gpu/jobs" {
		f, err := listQuery(c)
		if err != nil {
			return true, err
		}
		f.Owner, f.Project, f.Session = api.StrOr(s.Meta.Value("actor")), taskRef(s), s.ID
		return true, reply(c)(h.Svc().QueueList(f))
	}
	if m := reTaskLogs.FindStringSubmatch(path); m != nil {
		if _, err := h.taskJob(s, m[1]); err != nil {
			return true, err
		}
		tail, err := queryInt(c, "tail", 200, 1, 500)
		if err != nil {
			return true, err
		}
		return true, reply(c)(h.Svc().QueueLogs(m[1], tail))
	}
	if m := reTaskJob.FindStringSubmatch(path); m != nil {
		job, err := h.taskJob(s, m[1])
		if err != nil {
			return true, err
		}
		c.OK(pyjson.NewObject("job", job))
		return true, nil
	}
	return false, nil
}

// taskGPUPost is do_POST's task-agent GPU block (before the same-origin
// check); handled false for other paths.
func (h Handlers) TaskPost(c api.Ctx, path string) (bool, error) {
	cancel := reTaskCancel.FindStringSubmatch(path)
	if path != "/api/task-agent/v1/gpu/jobs" && cancel == nil {
		return false, nil
	}
	s, err := c.RequireTaskAgent()
	if err != nil {
		return true, err
	}
	if pyjson.Str(orStrAny(s.Meta.Value("access_mode"))) != "full_access" {
		return true, api.Err(403, "GPU queue dispatch requires a full-access task session.", "GPU_QUEUE_PERMISSION")
	}
	c.BindTaskSession(s)
	if cancel != nil {
		if _, err := c.Require("write"); err != nil {
			return true, err
		}
		if _, err := h.taskJob(s, cancel[1]); err != nil {
			return true, err
		}
		job, err := h.Svc().QueueCancel(cancel[1])
		if err != nil {
			return true, Err(err)
		}
		c.OK(pyjson.NewObject("job", job))
		return true, nil
	}
	body, err := c.ReadJSON()
	if err != nil {
		return true, err
	}
	if _, err := c.Require("write"); err != nil {
		return true, err
	}
	project := taskRef(s)
	if project == "" {
		p, err := c.Require("read")
		if err != nil {
			return true, err
		}
		project = p.ID()
	}
	task := api.StrOr(s.Meta.Value("task_ref"))
	if task == "" {
		task = api.StrOr(body.Value("task"))
	}
	job, err := h.Svc().QueueSubmit(body, api.StrOr(s.Meta.Value("actor")), project, s.ID, task)
	if err != nil {
		return true, Err(err)
	}
	c.OK(pyjson.NewObject("job", job))
	return true, nil
}
