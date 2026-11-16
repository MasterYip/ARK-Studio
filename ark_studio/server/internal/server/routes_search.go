package server

import searchapi "arkstudio/go/internal/api/search"

// Workbench Search routes live in internal/api/search (GET /api/workspace/grep
// goes through internal/api/workspace); this is the registration entry point
// for router.go.

func (c *reqCtx) searchNodes() error { return searchapi.Nodes(c) }
