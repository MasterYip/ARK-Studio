package gpu

// service.go is arkstudio/gpu_probe.py: GpuService, the adapter between the
// HTTP API and the vendored probe (config re-read on every call).

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pyos"
	"arkstudio/go/internal/pytext"
	"arkstudio/go/internal/store"
)

const (
	vendorRelative    = "vendor/gpu_server_probe"
	exampleRelative   = vendorRelative + "/servers_rp.example.yaml"
	defaultConfigName = "gpu_servers.yaml"
	minTimeoutSeconds = 2
	maxTimeoutSeconds = 30
	maxParallelProbes = 8
	vendorVersion     = "0.1.0"
)

// Error is gpu_probe.GpuError.
type Error struct {
	Status        int
	Message, Code string
}

func (e *Error) Error() string { return e.Message }

func truthy(v string) bool {
	switch pytext.Lower(pytext.Strip(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// Service is GpuService.
type Service struct {
	// ConfigPathOverride is GpuService(config_path=...) (tests).
	ConfigPathOverride   string
	Runner, SystemRunner Runner

	mu         sync.Mutex
	queue      *Queue
	startError any
	// Replay disables the worker loop: ticks run only on request (see
	// server: X-Replay-GpuTick under ARK_REPLAY_CLOCK=1).
	Replay bool

	summaryMu sync.Mutex
	// LastProbeSummary is last_probe_summary (Home); lastProbe is last_probe
	// (servers with GPUs/system data, read by the resources graph).
	LastProbeSummary *pyjson.Object
	lastProbe        *pyjson.Object
}

// ConfigPath is GpuService.config_path().
func (s *Service) ConfigPath() string {
	if s.ConfigPathOverride != "" {
		return pathStr(pyos.ExpandUser(s.ConfigPathOverride))
	}
	if override := pytext.Strip(os.Getenv("ARK_GPU_SERVERS_CONFIG")); override != "" {
		return pathStr(pyos.ExpandUser(override))
	}
	base := filepath.Join(pyos.Home(), ".ark")
	if private := pytext.Strip(os.Getenv("ARK_PRIVATE_DIR")); private != "" {
		base = pyos.ExpandUser(private)
	}
	return pathStr(base + string(os.PathSeparator) + defaultConfigName)
}

type inspection struct {
	path    string
	present bool
	config  *pyjson.Object
	err     string
	fatal   error // an exception other than ConfigError (escapes like in Python)
}

func (s *Service) inspect() inspection {
	path := s.ConfigPath()
	st := inspection{path: path, present: pyos.IsFile(path)}
	if st.present {
		cfg, err := LoadConfig(path)
		var ce *ConfigError
		if errors.As(err, &ce) {
			st.err = ce.Error()
		} else if err == nil {
			st.config = cfg
		} else {
			st.fatal = err
		}
	}
	return st
}

func (s *Service) enabled(cfg *pyjson.Object) bool {
	if cfg == nil {
		return false
	}
	return truthy(os.Getenv("ARK_GPU_PROBE")) || pyjson.Truthy(cfg.Value("enabled"))
}

func (s *Service) requireEnabled() (*pyjson.Object, string, error) {
	st := s.inspect()
	if st.fatal != nil {
		return nil, "", st.fatal
	}
	if !st.present {
		return nil, "", &Error{409, "No GPU server list found at " + st.path + ". Copy " + exampleRelative +
			" there (hosts and ports only) and set enabled: true.", "config_missing"}
	}
	if st.err != "" {
		return nil, "", &Error{422, "GPU server list is invalid: " + st.err, "config_invalid"}
	}
	if !s.enabled(st.config) {
		return nil, "", &Error{403, "GPU probing is disabled by default. Set 'enabled: true' in " + st.path +
			" or start ARK with ARK_GPU_PROBE=1, then reload.", "disabled"}
	}
	return st.config, st.path, nil
}

func errOrNil(text string) any {
	if text == "" {
		return nil
	}
	return text
}

// Status is GpuService.status().
func (s *Service) Status() (*pyjson.Object, error) {
	st := s.inspect()
	if st.fatal != nil {
		return nil, st.fatal
	}
	source := "default-off"
	if truthy(os.Getenv("ARK_GPU_PROBE")) {
		source = "environment"
	} else if st.config != nil && pyjson.Truthy(st.config.Value("enabled")) {
		source = "config"
	}
	var count int64
	var defaults any
	if st.config != nil {
		servers, _ := st.config.Value("servers").([]any)
		count = int64(len(servers))
		defaults = st.config.Value("defaults")
	}
	return pyjson.NewObject(
		"available", true,
		"enabled", s.enabled(st.config),
		"enabled_source", source,
		"vendor", pyjson.NewObject("name", "gpu_server_probe", "version", vendorVersion, "path", vendorRelative),
		"config_path", st.path,
		"config_present", st.present,
		"config_error", errOrNil(st.err),
		"server_count", count,
		"defaults", defaults,
		"ssh", DependencyStatus(),
		"asyncssh", pyjson.NewObject("available", false, "required", false,
			"note", "asyncssh is optional; ARK uses the OpenSSH client."),
		"how_to_enable", []any{
			"Copy " + exampleRelative + " to " + st.path + " and edit hosts.",
			"Set 'enabled: true' in that file, or start ARK with ARK_GPU_PROBE=1.",
			"Keep the file outside the repository; authentication uses your SSH agent or ~/.ssh/config.",
		},
		"security", pyjson.NewObject(
			"credentials", "not supported or stored; the schema rejects passwords, keys, tokens and identity files",
			"remote_commands", "read-only nvidia-smi probe by default; dispatch preparation is dry-run; "+
				"the opt-in queue may execute allocated jobs over SSH",
			"storage", "user-level private config; never inside a project folder"),
		"checked_at", store.UTCNow()), nil
}

// Servers is GpuService.servers().
func (s *Service) Servers() (*pyjson.Object, error) {
	st := s.inspect()
	if st.fatal != nil {
		return nil, st.fatal
	}
	enabled := s.enabled(st.config)
	payload := pyjson.NewObject("available", true, "enabled", enabled, "config_path", st.path,
		"config_present", st.present, "config_error", errOrNil(st.err), "servers", []any{}, "count", int64(0),
		"checked_at", store.UTCNow())
	switch {
	case enabled && st.config != nil:
		servers, _ := st.config.Value("servers").([]any)
		out := make([]any, len(servers))
		for i, item := range servers {
			out[i] = PublicServer(item.(*pyjson.Object))
		}
		payload.Set("servers", out).Set("count", int64(len(out))).Set("defaults", st.config.Value("defaults"))
	case !st.present:
		payload.Set("hint", "No server list configured yet.")
	case st.err != "":
		payload.Set("hint", "The server list is invalid; fix config_error and reload.")
	case !enabled:
		payload.Set("hint", "GPU probing is disabled by default; set enabled: true.")
	}
	return payload, nil
}

// names is gpu_probe._names.
func names(raw any) ([]string, error) {
	var values []string
	switch x := raw.(type) {
	case nil:
		return nil, nil
	case string:
		if x == "" {
			return nil, nil
		}
		for _, part := range strings.Split(x, ",") {
			values = append(values, pytext.Strip(part))
		}
	case []any:
		for _, part := range x {
			values = append(values, pytext.Strip(pyjson.Str(part)))
		}
	default:
		return nil, &Error{400, "servers must be a name, comma-separated string or list", "invalid_request"}
	}
	var out []string
	seen := map[string]bool{}
	for _, v := range values {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out, nil
}

// timeoutArg is GpuService._timeout: 0 = None.
func timeoutArg(raw any) (int64, error) {
	if raw == nil || raw == "" {
		return 0, nil
	}
	bad := &Error{400, "timeout_seconds must be an integer", "invalid_request"}
	if _, isBool := raw.(bool); isBool {
		return 0, bad
	}
	n, ok, err := pyIntArg(raw)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, bad
	}
	if n.Cmp(bigInt(minTimeoutSeconds)) < 0 || n.Cmp(bigInt(maxTimeoutSeconds)) > 0 {
		return 0, &Error{400, "timeout_seconds must be between " + strconv.Itoa(minTimeoutSeconds) + " and " +
			strconv.Itoa(maxTimeoutSeconds), "invalid_request"}
	}
	return n.Int64(), nil
}

func serverEnabled(server *pyjson.Object) bool {
	v, ok := server.Get("enabled")
	return !ok || pyjson.Truthy(v)
}

// Probe is GpuService.probe(body).
func (s *Service) Probe(body *pyjson.Object) (*pyjson.Object, error) {
	cfg, path, err := s.requireEnabled()
	if err != nil {
		return nil, err
	}
	if body == nil {
		body = pyjson.NewObject()
	}
	raw := body.Value("server")
	if body.Has("servers") {
		raw = body.Value("servers")
	}
	if list, ok := raw.([]any); ok && len(list) == 0 {
		raw = nil
	}
	requested, err := names(raw)
	if err != nil {
		return nil, err
	}
	all, _ := cfg.Value("servers").([]any)
	if len(all) == 0 {
		return nil, &Error{409, "No GPU servers are configured in the server list.", "no_servers"}
	}
	var targets []*pyjson.Object
	if len(requested) > 0 {
		known := map[string]bool{}
		for _, item := range all {
			known[pyjson.Str(item.(*pyjson.Object).Value("name"))] = true
		}
		var unknown []string
		want := map[string]bool{}
		for _, name := range requested {
			want[name] = true
			if !known[name] {
				unknown = append(unknown, name)
			}
		}
		if len(unknown) > 0 {
			return nil, &Error{400, "Unknown server(s): " + strings.Join(unknown, ", "), "unknown_server"}
		}
		for _, item := range all {
			if o := item.(*pyjson.Object); want[pyjson.Str(o.Value("name"))] {
				targets = append(targets, o)
			}
		}
	} else {
		for _, item := range all {
			targets = append(targets, item.(*pyjson.Object))
		}
	}
	override, err := timeoutArg(body.Value("timeout_seconds"))
	if err != nil {
		return nil, err
	}
	var active []*pyjson.Object
	skipped := []any{}
	for _, server := range targets {
		if serverEnabled(server) {
			active = append(active, server)
		} else {
			skipped = append(skipped, server.Value("name"))
		}
	}
	if len(active) == 0 {
		return nil, &Error{409, "All selected servers are disabled in the server list.", "no_servers"}
	}
	runner, system := s.Runner, s.SystemRunner
	if runner == nil {
		runner = RunRemote
		if system == nil {
			system = RunRemoteSystem
		}
	}
	results := make([]*pyjson.Object, len(active))
	errs := make([]error, len(active))
	slots := make(chan struct{}, min(maxParallelProbes, len(active)))
	var wg sync.WaitGroup
	for i, server := range active {
		wg.Add(1)
		go func(i int, server *pyjson.Object) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			limit := intOr(server.Value("timeout_seconds"), 8)
			cap := int64(maxTimeoutSeconds)
			if override != 0 {
				cap = override
			}
			results[i], errs[i] = ProbeServer(server, runner, int(max(minTimeoutSeconds, min(limit, cap))), system)
		}(i, server)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	ranked, err := RankGPUs(results)
	if err != nil {
		return nil, err
	}
	var okCount, gpus int64
	resultList := make([]any, len(results))
	for i, r := range results {
		if pyjson.Truthy(r.Value("ok")) {
			okCount++
		}
		if list, ok := r.Value("gpus").([]any); ok {
			gpus += int64(len(list))
		}
		resultList[i] = r
	}
	s.summaryMu.Lock()
	s.LastProbeSummary = pyjson.NewObject("probed_at", store.UTCNow(), "free_gpus", int64(len(ranked)), "gpus", gpus)
	s.summaryMu.Unlock()
	var timeout any
	if override != 0 {
		timeout = override
	}
	payload := pyjson.NewObject("enabled", true, "read_only", true, "config_path", path,
		"probed_at", store.UTCNow(), "timeout_seconds", timeout, "servers", resultList, "skipped", skipped,
		"ranked", ranked, "summary", pyjson.NewObject("configured", int64(len(targets)),
			"probed", int64(len(results)), "ok", okCount, "failed", int64(len(results))-okCount,
			"gpus", gpus, "free_gpus", int64(len(ranked))))
	s.summaryMu.Lock()
	s.lastProbe = pyjson.NewObject("probed_at", payload.Value("probed_at"), "servers", resultList)
	s.summaryMu.Unlock()
	return payload, nil
}

// LastProbe is GpuService.last_probe: nil until an explicit probe, then
// {"probed_at", "servers": [per-server probe results]}.  Never probes
// (resources.GPUSource, the Resources graph on feat/go-backend-cli).
func (s *Service) LastProbe() *pyjson.Object {
	s.summaryMu.Lock()
	defer s.summaryMu.Unlock()
	return s.lastProbe
}

// QueueJobs is the resources graph's queue read: the "jobs" of
// queue_list(project=id, status="starting,running", limit=200), only when
// the queue already exists (never creates it or starts the worker); else
// (nil, nil).  resources.GPUSource.
func (s *Service) QueueJobs(projectID string) ([]any, error) {
	q := s.QueueIfStarted()
	if q == nil {
		return nil, nil
	}
	out, err := q.List(ListFilters{Project: projectID, Status: "starting,running", Limit: int64(200)})
	if err != nil {
		return nil, asGpuError(err, "queue_invalid")
	}
	jobs, _ := out.Value("jobs").([]any)
	return jobs, nil
}

// Summary returns last_probe_summary (nil before the first probe).
func (s *Service) Summary() *pyjson.Object {
	s.summaryMu.Lock()
	defer s.summaryMu.Unlock()
	return s.LastProbeSummary
}

// Dashboard is GpuService.dashboard(project=...).
func (s *Service) Dashboard(project string) (*pyjson.Object, error) {
	probe, err := s.Probe(pyjson.NewObject())
	if err != nil {
		return nil, err
	}
	jobs, err := s.QueueList(ListFilters{Project: project, Limit: int64(200)})
	if err != nil {
		return nil, err
	}
	list := jobs.Value("jobs")
	if list == nil {
		list = []any{}
	}
	return pyjson.NewObject("checked_at", store.UTCNow(), "probe", probe, "jobs", list,
		"queue_enabled", pyjson.Truthy(jobs.Value("queue_enabled"))), nil
}

func queueConfig(cfg *pyjson.Object) *pyjson.Object {
	if cfg == nil {
		return nil
	}
	q, _ := cfg.Value("queue").(*pyjson.Object)
	return q
}

// StartConfiguredQueue is GpuService.start_configured_queue (server start).
func (s *Service) StartConfiguredQueue() bool {
	st := s.inspect()
	q := queueConfig(st.config)
	if !s.enabled(st.config) || q == nil || !pyjson.Truthy(q.Value("enabled")) {
		return false
	}
	if _, err := s.queueService(); err != nil {
		s.mu.Lock()
		s.startError = err.Error()
		s.mu.Unlock()
		return false
	}
	s.mu.Lock()
	s.startError = nil
	s.mu.Unlock()
	return true
}

func (s *Service) queueService() (*Queue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.queue == nil {
		q, err := NewQueue(s.ConfigPath())
		if err != nil {
			return nil, err
		}
		if !s.Replay {
			q.Start()
		}
		s.queue = q
	}
	return s.queue, nil
}

// QueueIfStarted returns the queue when one exists (replay ticks).
func (s *Service) QueueIfStarted() *Queue {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queue
}

// QueueStatus is GpuService.queue_status().
func (s *Service) QueueStatus() (*pyjson.Object, error) {
	st := s.inspect()
	if st.fatal != nil {
		return nil, st.fatal
	}
	q := queueConfig(st.config)
	s.mu.Lock()
	started, startErr := s.queue != nil, s.startError
	s.mu.Unlock()
	policy := pyjson.NewObject("allow_colocation", false, "max_jobs_per_gpu", nil, "max_utilization_pct", nil,
		"min_free_memory_mb", nil, "path_map", []any{})
	if q != nil {
		policy = pyjson.NewObject("allow_colocation", pyjson.Truthy(q.Value("allow_colocation")),
			"max_jobs_per_gpu", q.Value("max_jobs_per_gpu"), "max_utilization_pct", q.Value("max_utilization_pct"),
			"min_free_memory_mb", q.Value("min_free_memory_mb"), "path_map", q.Value("path_map"))
	}
	return pyjson.NewObject("available", true, "enabled", q != nil && pyjson.Truthy(q.Value("enabled")),
		"worker_started", started, "worker_error", startErr, "config_path", st.path,
		"config_present", st.present, "config_error", errOrNil(st.err), "policy", policy,
		"checked_at", store.UTCNow()), nil
}

func asGpuError(err error, code string) error {
	var qe *QueueError
	if errors.As(err, &qe) {
		return &Error{qe.Status, qe.Msg, code}
	}
	return err
}

// QueueSubmit is GpuService.queue_submit.
func (s *Service) QueueSubmit(body *pyjson.Object, owner, project, session, task string) (*pyjson.Object, error) {
	payload := pyjson.NewObject()
	if body != nil {
		payload = body.Copy()
	}
	payload.Set("owner", owner).Set("project", project).Set("session", session)
	if task != "" {
		payload.Set("task", task)
	}
	q, err := s.queueService()
	if err != nil {
		return nil, err
	}
	job, err := q.Submit(payload, !s.Replay)
	return job, asGpuError(err, "queue_invalid")
}

// QueueList is GpuService.queue_list.
func (s *Service) QueueList(f ListFilters) (*pyjson.Object, error) {
	q, err := s.queueService()
	if err != nil {
		return nil, err
	}
	out, err := q.List(f)
	return out, asGpuError(err, "queue_invalid")
}

// QueueGet is GpuService.queue_get.
func (s *Service) QueueGet(id string) (*pyjson.Object, error) {
	q, err := s.queueService()
	if err != nil {
		return nil, err
	}
	out, err := q.Get(id)
	return out, asGpuError(err, "queue_not_found")
}

// QueueCancel is GpuService.queue_cancel.
func (s *Service) QueueCancel(id string) (*pyjson.Object, error) {
	q, err := s.queueService()
	if err != nil {
		return nil, err
	}
	out, err := q.Cancel(id)
	return out, asGpuError(err, "queue_not_found")
}

// QueueLogs is GpuService.queue_logs.
func (s *Service) QueueLogs(id string, limit int64) (*pyjson.Object, error) {
	q, err := s.queueService()
	if err != nil {
		return nil, err
	}
	out, err := q.Logs(id, limit)
	return out, asGpuError(err, "queue_not_found")
}

// Close is GpuService.close.
func (s *Service) Close() {
	s.mu.Lock()
	q := s.queue
	s.queue = nil
	s.mu.Unlock()
	if q != nil {
		q.Close()
		q.db.Close() //nolint:errcheck
	}
}

// PrepareDispatch is GpuService.prepare_dispatch.
func (s *Service) PrepareDispatch(body *pyjson.Object) (*pyjson.Object, error) {
	cfg, path, err := s.requireEnabled()
	if err != nil {
		return nil, err
	}
	if body == nil {
		body = pyjson.NewObject()
	}
	servers, _ := cfg.Value("servers").([]any)
	request, err := BuildRequest(body, servers, store.UTCNow())
	if err != nil {
		var de *DispatchError
		if errors.As(err, &de) {
			return nil, &Error{400, de.Error(), "dispatch_invalid"}
		}
		return nil, err
	}
	request.Set("config_path", path).Set("prepared_by", "ARK Studio GPU adapter")
	return request, nil
}

// Overview is overview.gpu_summary(gpu, project=...).
func (s *Service) Overview(project string) *pyjson.Object {
	out := pyjson.NewObject("available", false, "configured", false, "enabled", false, "queue_enabled", false,
		"servers", int64(0), "jobs", pyjson.NewObject(), "free_gpus", nil, "gpus", nil, "probed_at", nil)
	st := s.inspect()
	if st.fatal != nil {
		return out // except Exception: Home renders without GPU support
	}
	cfg := st.config
	out.Set("available", true)
	out.Set("configured", st.present && st.err == "")
	out.Set("enabled", cfg != nil && s.enabled(cfg))
	if cfg != nil {
		servers, _ := cfg.Value("servers").([]any)
		out.Set("servers", int64(len(servers)))
	}
	q := queueConfig(cfg)
	out.Set("queue_enabled", q != nil && pyjson.Truthy(q.Value("enabled")))
	if last := s.Summary(); last != nil {
		out.Set("free_gpus", last.Value("free_gpus")).Set("gpus", last.Value("gpus")).Set("probed_at", last.Value("probed_at"))
	}
	if out.Value("queue_enabled") == true && out.Value("enabled") == true {
		var jobs []any
		if list, err := s.QueueList(ListFilters{Project: project, Limit: int64(200)}); err == nil {
			jobs, _ = list.Value("jobs").([]any)
		}
		counts := pyjson.NewObject()
		for _, item := range jobs {
			job := item.(*pyjson.Object)
			key := orStr(job.Value("status"), orStr(job.Value("state"), "unknown"))
			n, _ := counts.Value(key).(int64)
			counts.Set(key, n+1)
		}
		out.Set("jobs", counts)
	}
	return out
}
