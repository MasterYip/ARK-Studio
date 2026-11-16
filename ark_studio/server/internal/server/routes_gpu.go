package server

// GPU routes (services module): /api/gpu/*, the agent and task-agent GPU job
// routes and the overview gpu_summary, answered by internal/gpu (a port of
// arkstudio/gpu_probe.py and vendor/gpu_server_probe).

import (
	"os"
	"sync"

	gpuapi "arkstudio/go/internal/api/gpu"
	"arkstudio/go/internal/gpu"
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

// gpuHandlers is the GPU handler package bound to this server.
func (c *reqCtx) gpuHandlers() gpuapi.Handlers { return gpuapi.Handlers{Svc: c.app.gpu} }
