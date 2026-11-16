package server

import (
	resapi "arkstudio/go/internal/api/resources"
	"arkstudio/go/internal/gpu"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/resources"
)

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
		}}
}
