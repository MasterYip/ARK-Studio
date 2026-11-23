package server

import (
	"sync"

	apidocs "arkstudio/go/internal/api/agentdocs"
	chatapi "arkstudio/go/internal/api/chat"
)

// The agent docs API lives in internal/api/agentdocs.

// GoOnlyRoutes are routes the frozen Python server does not have (added
// after the 2026-11-15 switch to ark-server).  They are not contract ids:
// `ark-server routes` lists them with a "goonly:" prefix, the contract
// harness skips cases on them against Python, and diffreplay strips their
// additions to /api/agent/v1/interface before comparing with Python
// (tests/python/contract/go_only.py).
var GoOnlyRoutes = append([]PortedRoute{
	{"goonly:exact:/api/agent/v1/docs", "GET,HEAD"},
	{"goonly:prefix:/api/agent/v1/docs/", "GET,HEAD"},
	{"goonly:exact:" + chatapi.PluginChatPath, "POST"}, // routes_chat.go (view plugins' ai:chat)
	needsSeenGoOnly[0],     // project_reviews.go
	needsSeenGoOnly[1],     // project_reviews.go
	notificationsGoOnly[0], // notifications.go
	mobileGoOnly[0],        // routes_mobile.go
	appManifestGoOnly[0],   // app_manifest.go
	{"goonly:exact:" + agentMarkersPath, "GET,HEAD"}, // routes_agentmarkers.go
}, append(append(append(desktopRoutesGoOnly, presenceRoutesGoOnly...), // routes_desktop.go, routes_presence.go
	agentWatchGoOnly...), append(append(append(append(trashRoutesGoOnly, agentRulesGoOnly...), inviteRoutesGoOnly...), mediaThumbGoOnly...), append(append(append(append(append(reviewRoutesGoOnly, importArkGoOnly...), projectMoveGoOnly...), agentLinksGoOnly...), batchRoutesGoOnly...), append(append(storageRoutesGoOnly, resourcesRoutesGoOnly...), promptsGoOnly...)...)...)...)...) // routes_agentwatch.go, routes_trash.go, routes_agentrules.go, routes_invites.go, media_thumb.go, routes_reviews.go, routes_importark.go, routes_agentlinks.go, routes_batch.go, routes_storage.go, routes_resources.go, routes_projectmove.go

const markdownType = apidocs.MarkdownType

// agentDocsGet answers GET/HEAD /api/agent/v1/docs[/{path}] (agent auth was
// checked by the caller).
func (c *reqCtx) agentDocsGet(path string) (bool, error) {
	webDir := ""
	if c.app.Assets != nil {
		webDir = c.app.Assets.Dir
	}
	return apidocs.Get(c, path, webDir)
}

var (
	agentInterfaceOnce sync.Once
	agentInterfaceBody []byte
)

func agentInterfaceEncoded() []byte {
	agentInterfaceOnce.Do(func() { agentInterfaceBody = apidocs.InterfaceWithDocs(staticJSON["skills_interface"]) })
	return agentInterfaceBody
}
