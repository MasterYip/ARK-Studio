package server

import (
	"encoding/hex"
	"sort"
	"strings"
	"time"

	"arkstudio/go/internal/blake2b"
	"arkstudio/go/internal/project"
)

// eventsSessionsEvery is EVENTS_SESSIONS_EVERY (main 6c43e31).
const eventsSessionsEvery = 3 * time.Second

// sessionsToken is Handler._sessions_token: blake2b-64 of the sorted
// "id:status" list of the sessions this account sees in p.
func (c *reqCtx) sessionsToken(p *project.Project) string {
	if c.app.Sessions == nil {
		return ""
	}
	user, admin, err := c.sessionViewer() // routes_providers.go
	if err != nil {
		return "" // except ApiError: return ""
	}
	items := c.app.Sessions.Visible(p.ID(), user, admin)
	sort.SliceStable(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = it.ID + ":" + it.Status
	}
	return hex.EncodeToString(blake2b.Sum([]byte(strings.Join(parts, "|")), 8))
}
