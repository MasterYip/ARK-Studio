package server

import (
	"net"
	"net/url"
	"strings"

	"arkstudio/go/internal/api"
	resapi "arkstudio/go/internal/api/resources"
	"arkstudio/go/internal/gpu"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/resources"
)

// resourcesConnectPath is GET /api/resources/connect (Go only).
const resourcesConnectPath = resapi.ConnectPath

// resourcesRoutesGoOnly are the Go-only ids of this file (GoOnlyRoutes).
var resourcesRoutesGoOnly = []PortedRoute{
	{"goonly:exact:" + resapi.ConnectPath, "GET,HEAD"},
	{"goonly:fullmatch:" + resapi.ConnectPath + "/([^/]+)", "POST"},
}

// resourcesHandlers is the Resources handler package bound to this server.
func (c *reqCtx) resourcesHandlers() resapi.Handlers {
	return resapi.Handlers{Deps: resources.Deps{Org: c.app.Org, Mobile: c.app.Mobile,
		Sessions: c.app.Sessions, GPU: c.app.GPU()},
		// T-0018: the graph shows only the project's registered GPU servers.
		GPUFor: func(p *project.Project) (resources.GPUSource, error) {
			sc, err := c.gpuScope(p)
			if err != nil {
				return nil, err
			}
			return gpu.ScopedSource{Svc: c.app.GPU(), Scope: sc, ProjectID: p.ID()}, nil
		},
		OnGraph:     func(p *project.Project) { c.app.sizes().scan(p.Path) },
		ServerNames: func(api.Ctx) []string { return c.serverNames() },
		AllGPUServers: func(api.Ctx) []any {
			if admin, err := c.gpuAdmin(); err != nil || !admin || c.app.GPU() == nil {
				return nil
			}
			list, err := c.app.GPU().Servers()
			if err != nil || list == nil {
				return nil
			}
			servers, _ := list.Value("servers").([]any)
			return servers
		}}
}

// serverNames are the ARK server's public host names for this request: the
// Host header and the configured public URL (lower case, no port).
func (c *reqCtx) serverNames() []string {
	var out []string
	add := func(hostport string) {
		host := strings.ToLower(strings.TrimSpace(hostport))
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.Trim(host, "[]")
		if host == "" {
			return
		}
		for _, s := range out {
			if s == host {
				return
			}
		}
		out = append(out, host)
	}
	if c.app.publicURL != "" {
		if u, err := url.Parse(c.app.publicURL); err == nil {
			add(u.Host)
		}
	}
	add(c.header("Host"))
	return out
}
