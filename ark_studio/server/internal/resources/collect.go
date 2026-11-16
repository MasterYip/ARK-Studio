package resources

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"arkstudio/go/internal/platform/pathx"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/mobile"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/sessions"
)

// GPUSource is what the graph reads from the GPU service (gpu_probe.GpuService).
// It must never probe, open SSH or start the queue worker.
type GPUSource interface {
	// Servers is GpuService.servers(): the object's "enabled" (bool) and
	// "servers" (public server dicts: name, host, port, user, tags, note,
	// enabled, timeout_seconds; only name, tags, enabled for non-admins) are
	// read, and the Go-only "registration" (gpu.ScopedSource, T-0018).  An
	// error counts as {"enabled": false, "servers": []}.
	Servers() (*pyjson.Object, error)
	// LastProbe is GpuService.last_probe: nil until an explicit probe, then
	// {"probed_at": "<ISO, seconds, +00:00>", "servers": [per-server result]}
	// where a result is {"server": name, "ok": bool, "error": {"message"}|null,
	// "gpus": [{"index", "name", "memory_used_mb", "memory_total_mb",
	// "utilization_pct", "temperature_c"}], "system": {"cpu_percent",
	// "cpu_load_pct", "cores", "load1", "memory_used_mb", "memory_total_mb",
	// "processes": [{"gpu_index", "memory_mb", "job_id"}]}}.
	LastProbe() *pyjson.Object
	// QueueJobs is gpu.queue_list(project=id, status="starting,running",
	// limit=200)["jobs"] (each with "id", "server", "gpu_indices", "job")
	// when the queue worker already runs; nil, nil when it does not.
	QueueJobs(projectID string) ([]any, error)
}

// Deps are the server parts collect_sources reads.
type Deps struct {
	Org      *org.Manager
	Mobile   *mobile.Manager
	Sessions *sessions.Manager
	// GPU is nil while the GPU service is not ported: the graph then reads
	// no GPU servers, and a configured GPU server list makes Collect return
	// ErrGPUNotPorted (Python would show its servers).
	GPU GPUSource
	// Env is os.Getenv (tests replace it).
	Env func(string) string
}

// Project is the open project (id, name, folder).
type Project struct{ ID, Name, Path string }

// Host is the ARK server host: hostname and setup.machine_fingerprint().
type Host struct{ Hostname, Fingerprint string }

func platformID() string {
	switch runtime.GOOS {
	case "windows":
		return "windows"
	case "darwin":
		return "macos"
	case "linux", "freebsd", "openbsd", "netbsd":
		return "linux"
	}
	return runtime.GOOS
}

// GPUConfigPath is GpuService.config_path() for the default service.
func GPUConfigPath(env func(string) string) string {
	if p := strings.TrimSpace(env("ARK_GPU_SERVERS_CONFIG")); p != "" {
		return pathx.ExpandUser(p)
	}
	base := ""
	if p := strings.TrimSpace(env("ARK_PRIVATE_DIR")); p != "" {
		base = pathx.ExpandUser(p)
	} else {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".ark")
	}
	return filepath.Join(base, "gpu_servers.yaml")
}

// Collect is collect_sources(app, project): every source read once.
func Collect(ctx context.Context, d Deps, p Project, host Host, now float64) (*pyjson.Object, error) {
	env := d.Env
	if env == nil {
		env = os.Getenv
	}
	layers, err := ReadLayers(p.Path)
	if err != nil {
		return nil, err
	}
	history, err := RecentHistory(p.Path, HistoryLimit)
	if err != nil {
		return nil, err
	}
	gpu := pyjson.NewObject("enabled", false, "servers", []any{}, "last_probe", nil, "jobs", []any{})
	if d.GPU == nil {
		if fi, err := os.Stat(GPUConfigPath(env)); err == nil && fi.Mode().IsRegular() {
			return nil, ErrGPUNotPorted
		}
	} else {
		if servers, err := d.GPU.Servers(); err == nil && servers != nil {
			gpu.Set("enabled", pyjson.Truthy(servers.Value("enabled")))
			gpu.Set("servers", or(servers.Value("servers"), []any{}))
			// Go only (T-0018): the project's GPU registration (gpu.ScopedSource).
			if reg, ok := servers.Value("registration").(*pyjson.Object); ok {
				gpu.Set("registration", reg)
			}
		}
		if probe := d.GPU.LastProbe(); probe != nil {
			gpu.Set("last_probe", probe)
		}
		if jobs, err := d.GPU.QueueJobs(p.ID); err == nil && jobs != nil {
			gpu.Set("jobs", jobs)
		}
	}
	items := []any{}
	if d.Sessions != nil {
		for _, s := range d.Sessions.GraphItems() {
			project := s.Value("project")
			if !pyjson.Truthy(project) {
				items = append(items, s)
				continue
			}
			if _, isObj := project.(*pyjson.Object); isObj {
				continue // str(dict) never equals an id (Python compares str(project))
			}
			if _, isList := project.([]any); isList {
				continue
			}
			if text := pyjson.Str(project); text == p.ID || text == p.Name {
				items = append(items, s)
			}
		}
	}
	var disk any
	if total, used, free, ok := diskUsage(p.Path); ok {
		disk = pyjson.NewObject("total", total, "used", used, "free", free)
	}
	var load any
	if l, ok := loadAvg(); ok {
		load = l
	}
	owners := map[string]bool{}
	for _, r := range layers.Value("users").([]any) {
		owners[pyjson.Str(or(r.(*pyjson.Object).Value("owner"), ""))] = true
	}
	phones := []any{}
	if d.Mobile != nil {
		for _, ph := range d.Mobile.ProjectDevices(p.ID) {
			phones = append(phones, ph)
			owners[pyjson.Str(or(ph.Value("user"), ""))] = true
		}
	}
	var names []string
	for name := range owners {
		if name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	users := pyjson.NewObject()
	prefs := pyjson.NewObject()
	for _, name := range names {
		display := any(name)
		if d.Org != nil {
			row, ok, err := d.Org.UserByName(ctx, name)
			if err != nil {
				return nil, err
			}
			if ok && pyjson.Truthy(row.Get("display_name")) {
				display = row.Get("display_name")
			}
		}
		users.Set(name, pyjson.NewObject("username", name, "display_name", display))
		dp, err := DevicePrefs(ctx, d.Org, name)
		if err != nil {
			return nil, err
		}
		prefs.Set(name, dp)
	}
	return pyjson.NewObject(
		"now", now,
		"project", pyjson.NewObject("id", p.ID, "name", p.Name, "path", p.Path, "db", filepath.Join(p.Path, "ark.sqlite3")),
		"server", pyjson.NewObject("hostname", host.Hostname, "platform", platformID(), "fingerprint", host.Fingerprint,
			"disk", disk, "load1", load),
		"layers", layers,
		"history", history,
		"gpu", gpu,
		"sessions", items,
		"phones", phones,
		"users", users,
		"prefs", prefs,
	), nil
}

// ------------------------------------------------- personal device settings

// DevicePrefKeys is DEVICE_PREF_KEYS.
var DevicePrefKeys = []string{"hidden", "share_details"}

var deviceIDRe = regexp.MustCompile(`\A[A-Za-z0-9:._-]{1,120}\z`)

// Error is ResourcesGraphError (status, message, code).
type Error struct {
	Status        int
	Message, Code string
}

func (e *Error) Error() string { return e.Message }

// DevicePrefs is device_prefs: {device_id: {hidden, share_details}}.
func DevicePrefs(ctx context.Context, o *org.Manager, username string) (*pyjson.Object, error) {
	if o == nil || username == "" {
		return pyjson.NewObject(), nil
	}
	user, ok, err := o.UserByName(ctx, username)
	if err != nil || !ok {
		return pyjson.NewObject(), err
	}
	row, ok, err := o.SettingsRow(ctx, user.Str("id"), "devices")
	if err != nil {
		return nil, err
	}
	raw := "{}"
	if ok && pyjson.Truthy(row.Get("data_json")) {
		raw = pyjson.Str(row.Get("data_json"))
	}
	data, _ := pyjson.Loads(raw)
	if o, ok := data.(*pyjson.Object); ok {
		if devices, ok := o.Value("devices").(*pyjson.Object); ok {
			return devices, nil
		}
	}
	return pyjson.NewObject(), nil
}

// SetDevicePref is set_device_pref; it returns {"device": id, hidden, share_details}.
func SetDevicePref(ctx context.Context, o *org.Manager, username, deviceID string, patch *pyjson.Object) (*pyjson.Object, error) {
	var user interface{ Str(string) string }
	if o != nil && username != "" {
		row, ok, err := o.UserByName(ctx, username)
		if err != nil {
			return nil, err
		}
		if ok {
			user = row
		}
	}
	if user == nil {
		return nil, &Error{400, "personal device settings need a signed-in account", "RESOURCES_ACCOUNT_REQUIRED"}
	}
	if patch.Len() == 0 {
		return nil, &Error{400, "device settings accept only hidden and share_details", "RESOURCES_INVALID"}
	}
	for _, k := range patch.Keys() {
		if k != "hidden" && k != "share_details" {
			return nil, &Error{400, "device settings accept only hidden and share_details", "RESOURCES_INVALID"}
		}
	}
	for _, k := range patch.Keys() {
		if _, ok := patch.Value(k).(bool); !ok {
			return nil, &Error{400, "hidden and share_details must be true or false", "RESOURCES_INVALID"}
		}
	}
	if !deviceIDRe.MatchString(deviceID) {
		return nil, &Error{400, "invalid device id", "RESOURCES_INVALID"}
	}
	current, err := DevicePrefs(ctx, o, username)
	if err != nil {
		return nil, err
	}
	devices := current.Copy()
	prev, _ := devices.Value(deviceID).(*pyjson.Object)
	state := pyjson.NewObject()
	for _, k := range DevicePrefKeys {
		state.Set(k, pyjson.Truthy(prev.Value(k)))
	}
	state.Update(patch)
	if pyjson.Truthy(state.Value("hidden")) || pyjson.Truthy(state.Value("share_details")) {
		devices.Set(deviceID, state)
	} else {
		devices.Delete(deviceID)
	}
	if err := o.SaveSettings(ctx, user.Str("id"), "devices", pyjson.NewObject("devices", devices), nil); err != nil {
		return nil, err
	}
	out := pyjson.NewObject("device", deviceID)
	out.Update(state)
	return out, nil
}
