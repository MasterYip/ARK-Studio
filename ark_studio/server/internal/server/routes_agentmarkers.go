package server

// GET /api/agent/v1/markers (Go only, read access): the project's node
// marker groups (internal/store/markers) with the tag of each marker, so an agent
// can mark a node by writing the tag "marker:<id>" in its tags.  The store
// applies the same rules to agent writes as to the web client: no marker
// twice, at most one marker of an exclusive group (the last one written
// wins).  The groups are edited in Settings › Appearance › Node markers
// (PATCH /api/project/settings {"markers": {"groups": [...]}}, admins).

import (
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/store/markers"
)

const agentMarkersPath = "/api/agent/v1/markers"

func agentMarkers(p *project.Project) *pyjson.Object {
	settings, _ := p.Manifest.Get("settings").(*pyjson.Object)
	groups, err := markers.Parse(settings.Value("markers"))
	custom := err == nil && groups != nil
	return markers.Describe(p.Store.MarkerGroups(), custom)
}
