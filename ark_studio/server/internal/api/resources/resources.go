// Package resources is the HTTP side of the Resources graph (main db0be95):
// GET /api/resources/graph and POST /api/resources/devices/<id>, ports of
// App.resources_graph and App.update_device_privacy over internal/resources.
// The graph reads the server's GPU service (servers, last probe, running
// queue jobs; it never probes).  internal/server registers it (router.go).
package resources

import (
	"errors"
	"os"

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
		resdom.Host{Hostname: host, Fingerprint: setup.MachineFingerprint()}, now)
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
	c.OK(graph)
	return nil
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
