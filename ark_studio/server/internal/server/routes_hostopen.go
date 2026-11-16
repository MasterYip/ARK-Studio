package server

import (
	"arkstudio/go/internal/api/hostopen"
	"arkstudio/go/internal/compat/pyjson"
)

// POST /api/open and POST/OPTIONS /api/local/open live in internal/api/hostopen.

var hostopenPorted = []PortedRoute{
	{"exact:/api/open", "POST"},
	{"exact:/api/local/open", "POST"},
}

func (c *reqCtx) openExternal(body *pyjson.Object) error {
	return (&hostopen.Handlers{Sessions: c.app.Sessions}).Open(c, body)
}

func (c *reqCtx) localOpenOptions() { hostopen.LocalOpenOptions(c) }

func (c *reqCtx) localOpen() error { return hostopen.LocalOpen(c) }
