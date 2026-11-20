package gpu

// scope.go: per-project GPU server scoping and the member view (T-0018,
// Go only; docs/operations/gpu.md "Project scope").
//
// The server list (gpu_servers.yaml) is server-wide.  A project only sees,
// probes, dispatches to and queues on the servers its setup registers: a
// compute resource whose gpu_server names a server from the list, in the
// project layer or any member's user layer.  Connection details (host, port,
// user, config path, SSH preview) are for the server admin/owner and for
// local single-user mode only; everyone else gets the name, tags, enabled
// flag, status and probe metrics.

import (
	"errors"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
)

// Scope is one request's view of the server list.
type Scope struct {
	// All lifts the project filter (the unscoped methods, unit tests).
	All bool
	// Registered are the gpu_server names of the project's setup layers;
	// matched case-insensitively after stripping.
	Registered []string
	// Admin sees connection details: org owner/admin, or local mode.
	Admin bool
}

// Unscoped is the pre-T-0018 behaviour: every server, every detail.
func Unscoped() Scope { return Scope{All: true, Admin: true} }

const (
	// CodeNotRegistered: a named server is not registered in this project
	// (also used for names the server list does not know, so a member
	// cannot tell the two apart).
	CodeNotRegistered = "GPU_SERVER_NOT_REGISTERED"
	// CodeNoneRegistered: the project registers no server of the list.
	CodeNoneRegistered = "GPU_NO_REGISTERED_SERVERS"
	// HintNotRegistered is registration.hint_code when the list has servers
	// but none is registered in the project.
	HintNotRegistered = "not_registered"

	registerHint = "No GPU server is registered in this project. Add a compute resource of kind ssh " +
		"whose gpu_server names a server from the list (Setup > Resources, or POST " +
		"/api/agent/v1/setup/resource); setup discovery lists the available names."
	memberConfigError = "The GPU server list on this ARK server is invalid; ask a server admin to fix it."
	memberQueueError  = "The GPU queue could not start; ask a server admin."
)

// memberHowTo replaces how_to_enable (it names the private config path).
var memberHowTo = []any{
	"Ask a server admin to configure and enable the GPU server list.",
	"Register a server in this project: Setup > Resources, a compute resource of kind ssh with gpu_server set.",
}

func scopeKey(name string) string { return pytext.Lower(pytext.Strip(name)) }

// Allows reports whether the project may use the named server.
func (sc Scope) Allows(name string) bool {
	if sc.All {
		return true
	}
	k := scopeKey(name)
	if k == "" {
		return false
	}
	for _, r := range sc.Registered {
		if scopeKey(r) == k {
			return true
		}
	}
	return false
}

func serverName(item any) string {
	o, _ := item.(*pyjson.Object)
	if o == nil {
		return ""
	}
	return pyjson.Str(o.Value("name"))
}

// filter returns the configured servers the scope allows (config order).
func (sc Scope) filter(servers []any) []any {
	if sc.All {
		return servers
	}
	out := []any{}
	for _, item := range servers {
		if sc.Allows(serverName(item)) {
			out = append(out, item)
		}
	}
	return out
}

// canonical maps a requested name to the configured spelling (case-insensitive
// registration), unchanged when unscoped or unknown.
func (sc Scope) canonical(name string, servers []any) string {
	if sc.All {
		return name
	}
	for _, item := range servers {
		if n := serverName(item); scopeKey(n) == scopeKey(name) {
			return n
		}
	}
	return name
}

func errNotRegistered(name string) error {
	return &Error{404, "GPU server " + pyjson.Repr(name) + " is not registered in this project. " +
		"Register it in the project setup (a compute resource with gpu_server) first.", CodeNotRegistered}
}

func errNoneRegistered() error {
	return &Error{403, "No GPU server is registered in this project. Add a compute resource with " +
		"gpu_server in the project setup first.", CodeNoneRegistered}
}

// redactErr rewrites the config errors whose messages name the private
// config path or quote its content, for non-admin viewers.
func (sc Scope) redactErr(err error) error {
	if sc.Admin || err == nil {
		return err
	}
	var ge *Error
	if !errors.As(err, &ge) {
		return err
	}
	switch ge.Code {
	case "config_missing":
		return &Error{ge.Status, "No GPU server list is configured on this ARK server; ask a server admin.", ge.Code}
	case "config_invalid":
		return &Error{ge.Status, memberConfigError, ge.Code}
	case "disabled":
		return &Error{ge.Status, "GPU probing is disabled on this ARK server; ask a server admin to enable it.", ge.Code}
	}
	return err
}

// memberServerFields are what a non-admin sees of a configured server.
var memberServerFields = []string{"name", "tags", "enabled"}

func memberServer(server *pyjson.Object) *pyjson.Object {
	out := pyjson.NewObject()
	for _, k := range memberServerFields {
		if v, ok := server.Get(k); ok {
			out.Set(k, v)
		}
	}
	return out
}

// scrub removes a server's address from a message (ssh errors quote it).
func scrub(text, host, user string) string {
	if host == "" {
		return text
	}
	if user != "" {
		text = strings.ReplaceAll(text, user+"@"+host, "<host>")
	}
	return strings.ReplaceAll(text, host, "<host>")
}

// memberProbeResult is a per-server probe result without host, port, user;
// the address is scrubbed from the error message and warnings.
func memberProbeResult(item any) any {
	r, ok := item.(*pyjson.Object)
	if !ok {
		return item
	}
	host, user := pyjson.Str(orEmpty(r.Value("host"))), pyjson.Str(orEmpty(r.Value("user")))
	out := r.Copy()
	out.Delete("host")
	out.Delete("port")
	out.Delete("user")
	if e, ok := out.Value("error").(*pyjson.Object); ok {
		e = e.Copy()
		if m, ok := e.Value("message").(string); ok {
			e.Set("message", scrub(m, host, user))
		}
		out.Set("error", e)
	}
	if ws, ok := out.Value("warnings").([]any); ok {
		clean := make([]any, len(ws))
		for i, w := range ws {
			if s, ok := w.(string); ok {
				w = scrub(s, host, user)
			}
			clean[i] = w
		}
		out.Set("warnings", clean)
	}
	return out
}

// memberProbe redacts a probe payload in place: no config path, no
// addresses in the per-server results or the ranking.
func memberProbe(payload *pyjson.Object) {
	payload.Delete("config_path")
	if list, ok := payload.Value("servers").([]any); ok {
		out := make([]any, len(list))
		for i, item := range list {
			out[i] = memberProbeResult(item)
		}
		payload.Set("servers", out)
	}
	if list, ok := payload.Value("ranked").([]any); ok {
		out := make([]any, len(list))
		for i, item := range list {
			if o, ok := item.(*pyjson.Object); ok {
				o = o.Copy()
				o.Delete("host")
				item = o
			}
			out[i] = item
		}
		payload.Set("ranked", out)
	}
}

// registration is the Go-only "registration" member of the scoped replies:
// which configured servers the project registers, which it could register
// (names only, admins only) and the hint when it registers none.  Members get
// empty available/suggested lists: they register servers in Setup, whose
// discovery offers the names (T-0026).
func (s *Service) registration(sc Scope, cfg *pyjson.Object, projectID string) *pyjson.Object {
	var servers []any
	if cfg != nil {
		servers, _ = cfg.Value("servers").([]any)
	}
	registered, available := []any{}, []any{}
	known := map[string]bool{}
	for _, item := range servers {
		name := serverName(item)
		known[scopeKey(name)] = true
		if sc.Allows(name) {
			registered = append(registered, name)
		} else {
			available = append(available, name)
		}
	}
	missing := []any{}
	seen := map[string]bool{}
	for _, r := range sc.Registered {
		k := scopeKey(r)
		if k != "" && !known[k] && !seen[k] {
			seen[k] = true
			missing = append(missing, pytext.Strip(r))
		}
	}
	// Migration aid: servers this project's queue jobs already ran on.
	suggested := []any{}
	if q := s.QueueIfStarted(); q != nil && projectID != "" {
		if out, err := q.List(ListFilters{Project: projectID, Limit: int64(500)}); err == nil {
			jobs, _ := out.Value("jobs").([]any)
			used := map[string]bool{}
			for _, j := range jobs {
				if o, ok := j.(*pyjson.Object); ok {
					used[scopeKey(pyjson.Str(orEmpty(o.Value("server"))))] = true
				}
			}
			for _, name := range available {
				if used[scopeKey(name.(string))] {
					suggested = append(suggested, name)
				}
			}
		}
	}
	if !sc.Admin {
		available, suggested = []any{}, []any{}
	}
	out := pyjson.NewObject("registered", registered, "available", available, "missing", missing,
		"suggested", suggested)
	if len(registered) == 0 && len(servers) > 0 {
		out.Set("hint_code", HintNotRegistered).Set("hint", registerHint)
	}
	return out
}

// ScopedSource is the Resources graph's GPU source for one project and
// viewer (resources.GPUSource).
type ScopedSource struct {
	Svc       *Service
	Scope     Scope
	ProjectID string
}

// Servers is ServersFor(scope, project).
func (v ScopedSource) Servers() (*pyjson.Object, error) {
	return v.Svc.ServersFor(v.Scope, v.ProjectID)
}

// LastProbe is LastProbeFor(scope).
func (v ScopedSource) LastProbe() *pyjson.Object { return v.Svc.LastProbeFor(v.Scope) }

// QueueJobs is Service.QueueJobs (already filtered by project).
func (v ScopedSource) QueueJobs(projectID string) ([]any, error) { return v.Svc.QueueJobs(projectID) }
