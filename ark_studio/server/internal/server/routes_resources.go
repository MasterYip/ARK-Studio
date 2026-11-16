package server

import (
	resapi "arkstudio/go/internal/api/resources"
	"arkstudio/go/internal/resources"
)

// resourcesHandlers is the Resources handler package bound to this server.
func (c *reqCtx) resourcesHandlers() resapi.Handlers {
	return resapi.Handlers{Deps: resources.Deps{Org: c.app.Org, Mobile: c.app.Mobile,
		Sessions: c.app.Sessions, GPU: c.app.GPU()}}
}
