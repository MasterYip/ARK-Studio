// Package resources is the HTTP side of the Resources graph (main db0be95):
// GET /api/resources/graph and POST /api/resources/devices/<id>, ports of
// App.resources_graph and App.update_device_privacy over internal/resources.
// The graph reads the server's GPU service (servers, last probe, running
// queue jobs; it never probes).  internal/server registers it (router.go).
package resources

import (
	"errors"
	"os"
	"strings"

	"arkstudio/go/internal/api"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pyurl"
	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/project"
	resdom "arkstudio/go/internal/resources"
	"arkstudio/go/internal/setup"
)

// Handlers answers the Resources routes for one server.
type Handlers struct {
	// Deps are the server's org, mobile, sessions and GPU sources.
	Deps resdom.Deps
	// GPUFor, when set, replaces Deps.GPU with the GPU source scoped to the
	// request's project and viewer (gpu.ScopedSource, T-0018).
	GPUFor func(p *project.Project) (resdom.GPUSource, error)
	// OnGraph, when set, is told that the project's graph was read (the
	// server schedules due location sizes in the background).
	OnGraph func(p *project.Project)
	// ServerNames, when set, are the ARK server's public host names for this
	// request (its Host header, the configured public URL).
	ServerNames func(c api.Ctx) []string
	// AllGPUServers, when set, is the whole server list for admins (nil for
	// everyone else): the ARK server's own SSH entry is found in it.
	AllGPUServers func(c api.Ctx) []any
}

// connectOpts are the shared machine records of p and the server's names.
func (h Handlers) connectOpts(c api.Ctx, p *project.Project) resdom.ConnectOpts {
	opts := resdom.ConnectOpts{Machines: resdom.ReadMachines(p.Path)}
	if h.ServerNames != nil {
		opts.ServerNames = h.ServerNames(c)
		opts.ServerAddrs = resdom.LookupAddrs(c.Context(), opts.ServerNames)
	}
	if h.AllGPUServers != nil && len(opts.ServerAddrs) > 0 {
		addrs := map[string]bool{}
		for _, a := range opts.ServerAddrs {
			addrs[a] = true
		}
		for _, item := range h.AllGPUServers(c) {
			if s, ok := item.(*pyjson.Object); ok {
				if host, _ := s.Value("host").(string); host != "" && addrs[strings.ToLower(host)] {
					opts.ServerSSH = pyjson.NewObject("name", s.Value("name"), "host", host, "port", s.Value("port"), "user", s.Value("user"))
					break
				}
			}
		}
	}
	return opts
}

// isAdmin: the viewer may change the project's shared machine records.
func isAdmin(c api.Ctx) bool {
	_, err := c.Require("admin")
	return err == nil
}

var reDevice = api.PyRe(`/api/resources/devices/([^/]+)`, true)

func (h Handlers) sources(c api.Ctx, p *project.Project) (*pyjson.Object, error) {
	host, _ := os.Hostname()
	now := float64(clock.Now().UnixMicro()) / 1e6
	deps := h.Deps
	if h.GPUFor != nil {
		src, err := h.GPUFor(p)
		if err != nil {
			return nil, err
		}
		deps.GPU = src
	}
	return resdom.Collect(c.Context(), deps,
		resdom.Project{ID: p.ID(), Name: p.Name(), Path: p.Path},
		resdom.Host{Hostname: host, Fingerprint: setup.MachineFingerprint(), LegacyFingerprint: setup.LegacyMachineFingerprint()}, now)
	// Deps.GPU is always set by the server, so resources.ErrGPUNotPorted cannot occur.
}

// Graph is GET /api/resources/graph.
func (h Handlers) Graph(c api.Ctx) error {
	p, err := c.Require("read")
	if err != nil {
		return err
	}
	src, err := h.sources(c, p)
	if src == nil || err != nil {
		return err
	}
	graph, err := resdom.BuildGraph(src, c.Actor())
	if err != nil {
		return err
	}
	if h.OnGraph != nil {
		h.OnGraph(p)
	}
	c.OK(graph)
	return nil
}

// GraphFor is the graph GET /api/resources/graph answers, for another reply
// (GET /api/project/support).  It does not schedule location sizes.
func (h Handlers) GraphFor(c api.Ctx, p *project.Project) (*pyjson.Object, error) {
	src, err := h.sources(c, p)
	if err != nil {
		return nil, err
	}
	if src == nil {
		return pyjson.NewObject("devices", []any{}, "resources", []any{}), nil
	}
	return resdom.BuildGraph(src, c.Actor())
}

// DevicePost is POST /api/resources/devices/<id> (raw path); false for
// other paths.
func (h Handlers) DevicePost(c api.Ctx, path string, body *pyjson.Object) (bool, error) {
	m := reDevice.FindStringSubmatch(path)
	if m == nil {
		return false, nil
	}
	deviceID := pyurl.Unquote(m[1])
	p, err := c.Require("read")
	if err != nil {
		return true, err
	}
	actor := c.Actor()
	src, err := h.sources(c, p)
	if src == nil || err != nil {
		return true, err
	}
	graph, err := resdom.BuildGraph(src, actor)
	if err != nil {
		return true, err
	}
	mine := false
	for _, d := range graph.Value("devices").([]any) {
		dev := d.(*pyjson.Object)
		if pyjson.Truthy(dev.Value("mine")) && dev.Value("id") == deviceID {
			mine = true
		}
	}
	// A hidden device is no longer in the graph; its owner can still unhide it.
	prefs, err := resdom.DevicePrefs(c.Context(), h.Deps.Org, actor)
	if err != nil {
		return true, err
	}
	if !mine && !prefs.Has(deviceID) {
		return true, api.Err(404, "personal device not found", "RESOURCES_DEVICE_NOT_FOUND")
	}
	saved, err := resdom.SetDevicePref(c.Context(), h.Deps.Org, actor, deviceID, body)
	var re *resdom.Error
	if errors.As(err, &re) {
		return true, api.Err(re.Status, re.Message, re.Code)
	}
	if err != nil {
		return true, err
	}
	c.OK(pyjson.NewObject("device", saved))
	return true, nil
}

// ConnectPath is GET /api/resources/connect (Go only, 2026-11-22): the SSH
// connections of the viewer's graph devices and the project's shared
// machine records (resources.ConnectionsWith, 2026-11-24).
const ConnectPath = "/api/resources/connect"

var reConnect = api.PyRe(`/api/resources/connect/([^/]+)`, true)

// Connect is GET /api/resources/connect.
func (h Handlers) Connect(c api.Ctx) error {
	p, err := c.Require("read")
	if err != nil {
		return err
	}
	src, err := h.sources(c, p)
	if src == nil || err != nil {
		return err
	}
	graph, err := resdom.BuildGraph(src, c.Actor())
	if err != nil {
		return err
	}
	saved, err := resdom.SSHSettings(c.Context(), h.Deps.Org, c.Actor())
	if err != nil {
		return err
	}
	out := resdom.ConnectionsWith(graph, src, saved, h.connectOpts(c, p))
	admin := isAdmin(c)
	personal := h.Deps.Org != nil && c.Actor() != ""
	out.Set("can_save", personal || admin)
	out.Set("can_save_personal", personal)
	out.Set("can_edit_shared", admin)
	c.OK(out)
	return nil
}

// ConnectPost is POST /api/resources/connect/<device id> (raw path): the
// viewer's own entry {alias, host, user, port, auth} and, for project
// admins, {"shared": {name, host, port, user}} and {"link": machine id}
// (resources.SaveConnection).  False for other paths.
func (h Handlers) ConnectPost(c api.Ctx, path string, body *pyjson.Object) (bool, error) {
	m := reConnect.FindStringSubmatch(path)
	if m == nil {
		return false, nil
	}
	deviceID := pyurl.Unquote(m[1])
	p, err := c.Require("read")
	if err != nil {
		return true, err
	}
	actor := c.Actor()
	src, err := h.sources(c, p)
	if src == nil || err != nil {
		return true, err
	}
	graph, err := resdom.BuildGraph(src, actor)
	if err != nil {
		return true, err
	}
	result, err := resdom.SaveConnection(c.Context(), h.Deps.Org, resdom.SaveRequest{
		ProjectPath: p.Path, Username: actor, DeviceID: deviceID, Body: body, Admin: isAdmin(c),
		Graph: graph, Sources: src, Opts: h.connectOpts(c, p)})
	var re *resdom.Error
	if errors.As(err, &re) {
		return true, api.Err(re.Status, re.Message, re.Code)
	}
	if err != nil {
		return true, err
	}
	c.OK(result)
	return true, nil
}
