package server

// GPU routes (services module): /api/gpu/*, the agent and task-agent GPU job
// routes and the overview gpu_summary, answered by internal/gpu (a port of
// arkstudio/gpu_probe.py and vendor/gpu_server_probe).

import (
	"context"
	"os"
	"sync"

	"arkstudio/go/internal/api"
	gpuapi "arkstudio/go/internal/api/gpu"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/gpu"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/resources"
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

var (
	gpuServices sync.Map // *App -> *gpu.Service (App.gpu)
)

// gpu is App.gpu (GpuService), created with the App.
func (a *App) gpu() *gpu.Service {
	if s, ok := gpuServices.Load(a); ok {
		return s.(*gpu.Service)
	}
	s, _ := gpuServices.LoadOrStore(a, &gpu.Service{Replay: os.Getenv("ARK_REPLAY_CLOCK") == "1",
		AllowedFor: a.gpuQueueScope, OnFinished: a.gpuJobFinished})
	return s.(*gpu.Service)
}

// ---------------------------------------------------------- project scope
//
// T-0018 (Go only; docs/operations/gpu.md "Project scope"): the server list
// is server-wide, but a project sees, probes, dispatches to and queues on
// only the servers its setup registers (resources.RegisteredGPUServers).
// The same rule holds in local mode; there the viewer is the admin.

// gpuAdmin reports whether the viewer may see GPU connection details: local
// single-user mode without auth, or an organization owner/admin (the role
// org.requireOrgAdmin accepts).
func (c *reqCtx) gpuAdmin() (bool, error) {
	if c.app.Auth == nil {
		return true, nil
	}
	actor := c.actor()
	if actor == "" {
		return false, nil
	}
	role, ok, err := c.app.Org.RoleFor(c.ctx, actor)
	if err != nil || !ok {
		return false, err
	}
	rank, known := org.RoleRank[role]
	return known && rank >= org.RoleRank["admin"], nil
}

// gpuScope is the GPU scope of project p for this request's viewer.
func (c *reqCtx) gpuScope(p *project.Project) (gpu.Scope, error) {
	admin, err := c.gpuAdmin()
	if err != nil {
		return gpu.Scope{}, err
	}
	var registered []string
	if p != nil {
		registered = resources.RegisteredGPUServers(p.Path)
	}
	return gpu.Scope{Registered: registered, Admin: admin}, nil
}

// gpuScopeByID is gpuScope for a project id (an unknown id registers nothing).
func (c *reqCtx) gpuScopeByID(id string) (gpu.Scope, error) {
	p, err := c.projectByID(id)
	if err != nil {
		return gpu.Scope{}, err
	}
	return c.gpuScope(p)
}

// gpuQueueScope is the queue worker's scope for a job (no request): the
// servers registered by the job's project; nothing when the project cannot
// be found (the job then stays queued).
func (a *App) gpuQueueScope(projectID, owner string) gpu.Scope {
	path := a.projectPathByID(context.Background(), projectID, owner)
	if path == "" {
		return gpu.Scope{}
	}
	return gpu.Scope{Registered: resources.RegisteredGPUServers(path)}
}

// projectPathByID is projectByID without a request: the org registry, then
// the projects folder; the demo project is the owner's own copy.
func (a *App) projectPathByID(ctx context.Context, id, owner string) string {
	id = pytext.Strip(id)
	if id == "" {
		return ""
	}
	if id == demoID {
		if owner == "" {
			owner = org.LocalUsername
		}
		return project.DemoUserPath(a.Root, owner)
	}
	if a.Org != nil {
		if row, ok, err := a.Org.ProjectRow(ctx, id); err == nil && ok {
			return row.Str("path")
		}
	}
	if a.Projects != nil {
		for _, s := range a.Projects.ListProjects() {
			if pyjson.Str(s.Value("id")) == id || pyjson.Str(s.Value("folder")) == id {
				return pyjson.Str(s.Value("path"))
			}
		}
	}
	return ""
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

// gpuHandlers is the GPU handler package bound to this server.
func (c *reqCtx) gpuHandlers() gpuapi.Handlers {
	return gpuapi.Handlers{Svc: c.app.gpu, Scope: c.gpuScope, ScopeByID: c.gpuScopeByID,
		Wake: func(_ api.Ctx, projectID, session string) gpu.Wake { return c.gpuWake(projectID, session) }}
}
