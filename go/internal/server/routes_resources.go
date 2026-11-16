package server

import (
	"errors"
	"os"

	"arkstudio/go/internal/project"
	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pyurl"
	"arkstudio/go/internal/resources"
	"arkstudio/go/internal/setup"
	"arkstudio/go/internal/store"
)

// Resources graph (main db0be95): GET /api/resources/graph and
// POST /api/resources/devices/<id>, ports of App.resources_graph and
// App.update_device_privacy.  The graph reads the app's GPU service
// (servers, last probe, running queue jobs; it never probes).

var reResourcesDevice = pyRe(`/api/resources/devices/([^/]+)`, true)

func init() {
	Ported = append(Ported,
		PortedRoute{"exact:/api/resources/graph", "GET,HEAD"},
		PortedRoute{"fullmatch:/api/resources/devices/([^/]+)", "POST"})
}

func (c *reqCtx) resourcesSources(p *project.Project) (*pyjson.Object, error) {
	host, _ := os.Hostname()
	now := float64(store.Clock().UnixMicro()) / 1e6
	src, err := resources.Collect(c.ctx, resources.Deps{Org: c.app.Org, Mobile: c.app.Mobile,
		Sessions: c.app.Sessions, GPU: c.app.GPU()},
		resources.Project{ID: p.ID(), Name: p.Name(), Path: p.Path},
		resources.Host{Hostname: host, Fingerprint: setup.MachineFingerprint()}, now)
	return src, err // d.GPU is always set here, so resources.ErrGPUNotPorted cannot occur
}

// resourcesGraph is GET /api/resources/graph.
func (c *reqCtx) resourcesGraph() error {
	p, err := c.require("read")
	if err != nil {
		return err
	}
	src, err := c.resourcesSources(p)
	if src == nil || err != nil {
		return err
	}
	graph, err := resources.BuildGraph(src, c.actor())
	if err != nil {
		return err
	}
	c.ok(graph)
	return nil
}

// resourcesDevicePost is POST /api/resources/devices/<id> (raw path).
func (c *reqCtx) resourcesDevicePost(path string, body *pyjson.Object) (bool, error) {
	m := reResourcesDevice.FindStringSubmatch(path)
	if m == nil {
		return false, nil
	}
	deviceID := pyurl.Unquote(m[1])
	p, err := c.require("read")
	if err != nil {
		return true, err
	}
	actor := c.actor()
	src, err := c.resourcesSources(p)
	if src == nil || err != nil {
		return true, err
	}
	graph, err := resources.BuildGraph(src, actor)
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
	prefs, err := resources.DevicePrefs(c.ctx, c.app.Org, actor)
	if err != nil {
		return true, err
	}
	if !mine && !prefs.Has(deviceID) {
		return true, apiErr(404, "personal device not found", "RESOURCES_DEVICE_NOT_FOUND")
	}
	saved, err := resources.SetDevicePref(c.ctx, c.app.Org, actor, deviceID, body)
	var re *resources.Error
	if errors.As(err, &re) {
		return true, apiErr(re.Status, re.Message, re.Code)
	}
	if err != nil {
		return true, err
	}
	c.ok(pyjson.NewObject("device", saved))
	return true, nil
}
