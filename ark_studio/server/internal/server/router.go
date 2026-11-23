package server

import (
	"regexp"
	"strings"

	chatapi "arkstudio/go/internal/api/chat"
	"arkstudio/go/internal/mobile"
)

// The router replaces the hand-written dispatch cascades of do_GET, do_POST,
// do_PATCH and do_DELETE.  Each table lists the registrations of one phase of
// a Python handler in Python's branch order (the order decides overlapping
// paths and which guard or body read runs first, so it is part of the
// contract).  Every registration declares the contract ids and verbs it
// answers; `Ported` (printed by `ark-server routes`, read by the harness, the
// differential replay and route_table.py) is derived from these declarations.
//
// The guards between phases (host check, sign-in, device gate, project scope,
// same-origin, the JSON body read) stay in doGet/doPost/doPatchDelete.

// route is one registration: the contract ids it answers and how it is tried.
// try reports whether the registration took the request.
type route struct {
	ids []PortedRoute
	try func(c *reqCtx, path string) (bool, error)
}

// exact registers a handler for one literal path.
func exact(p string, h func(*reqCtx) error, ids ...PortedRoute) route {
	return route{ids, func(c *reqCtx, path string) (bool, error) {
		if path != p {
			return false, nil
		}
		return true, h(c)
	}}
}

// match registers a handler for a Python re.match/fullmatch pattern; it gets
// the submatches.
func match(re *regexp.Regexp, h func(*reqCtx, []string) error, ids ...PortedRoute) route {
	return route{ids, func(c *reqCtx, path string) (bool, error) {
		m := re.FindStringSubmatch(path)
		if m == nil {
			return false, nil
		}
		return true, h(c, m)
	}}
}

// prefix registers a handler that takes every path under p (Python's
// `path.startswith(p)` branches that hand over to a sub-dispatcher).
func prefix(p string, h func(*reqCtx, string) error, ids ...PortedRoute) route {
	return route{ids, func(c *reqCtx, path string) (bool, error) {
		if !strings.HasPrefix(path, p) {
			return false, nil
		}
		return true, h(c, path)
	}}
}

// when registers a handler behind an arbitrary path predicate.
func when(ok func(string) bool, h func(*reqCtx, string) error, ids ...PortedRoute) route {
	return route{ids, func(c *reqCtx, path string) (bool, error) {
		if !ok(path) {
			return false, nil
		}
		return true, h(c, path)
	}}
}

// sub registers a module dispatcher that matches its own paths.
func sub(f func(*reqCtx, string) (bool, error), ids ...PortedRoute) route {
	return route{ids, f}
}

func concat(lists ...[]PortedRoute) []PortedRoute {
	var out []PortedRoute
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

// dispatch tries the registrations of one phase in order.
func dispatch(c *reqCtx, table []route, path string) (bool, error) {
	for _, r := range table {
		if ok, err := r.try(c, path); ok {
			return true, err
		}
	}
	return false, nil
}

// pipelineRoutes are answered before the router: by the request pipeline
// (unknown endpoints, static files, sign-in pages, APK downloads) or by the
// handler prologue (the local-open bridge, auth routes).
var pipelineRoutes = []PortedRoute{
	{"exact:/api/nope", "DELETE,GET,HEAD,OPTIONS,PATCH,POST,PUT"},
	{"static:/*", "GET,HEAD"},
	{"exact:/", "GET,HEAD"},
	{"exact:/login", "GET,HEAD,POST"},
	{"exact:/logout", "GET,HEAD,POST"},
	{"exact:/api/auth/session", "GET,HEAD"},
	{"exact:/api/auth/logout", "POST"},
	{"exact:/download/ARK-Studio-android.apk", "GET,HEAD"},
	{"exact:/download/ark.apk", "GET,HEAD"},
	{"exact:/download/ARK-Studio-webapp.apk", "GET,HEAD"},
}

// The dispatch tables (filled in init so that handlers may refer to them
// without an initialization cycle).
var (
	getRoutes           []route // Handler._api_get, after the guards
	postBeforeOrigin    []route // do_POST before the same-origin check
	postBeforeBody      []route // do_POST before the JSON body is read
	postRoutes          []route // do_POST with the body read
	writeProfileRoutes  []route // do_PATCH / do_DELETE before the body
	patchBeforeProject  []route // do_PATCH before the project is required
	patchRoutes         []route // do_PATCH with the project
	deleteBeforeActor   []route // do_DELETE before the write actor is resolved
	deleteBeforeProject []route // do_DELETE before the project is required
	deleteRoutes        []route // do_DELETE with the project
	allDispatchTables   [][]route
)

// Ported lists every contract id and verb the Go server answers itself.
var Ported []PortedRoute

func init() {
	profile := when(isProfilePath, (*reqCtx).profileWrite,
		PortedRoute{"exact:/api/me", "PATCH"},
		PortedRoute{"exact:/api/me/avatar", "DELETE,POST"},
		PortedRoute{"exact:/api/me/password", "POST"})

	getRoutes = []route{
		// Go only (routes_desktop.go; ids in GoOnlyRoutes).
		exact(mobile.DesktopProjectsPath, (*reqCtx).desktopProjectsGet),
		// Go only (routes_presence.go, T-0043 agent presence).
		exact(presencePath, (*reqCtx).presenceGet),
		// Go only (media_thumb.go: list thumbnails of images and videos).
		match(reMediaThumb, func(c *reqCtx, m []string) error { return c.mediaThumb(m[1]) }),
		// Go only (routes_invites.go: project invite links).
		match(reProjectInvites, (*reqCtx).projectInvitesGet),
		exact(inviteSettingsPath, (*reqCtx).inviteSettingsGet),
		// Go only (routes_storage.go: where new projects are stored).
		exact(projectStoragePath, (*reqCtx).projectStorageGet),
		sub(func(c *reqCtx, _ string) (bool, error) {
			ok, err := c.apiGetViews()
			return ok || err != nil, err
		}, viewsPorted...),
		// servicesGet / servicesPost also dispatch the chat, GPU and setup routes.
		sub((*reqCtx).servicesGet, concat(servicesPorted, chatPorted, gpuPorted, setupPorted)...),
		sub((*reqCtx).settingsGet, settingsPorted...),
		sub((*reqCtx).clientCacheGet,
			PortedRoute{"exact:/api/changes", "GET,HEAD"},
			PortedRoute{"exact:/api/unit-details", "GET,HEAD"},
			PortedRoute{"exact:/api/graph/bodies", "GET,HEAD"},
			PortedRoute{"exact:/api/events", "GET,HEAD"}),
		exact("/api/health", (*reqCtx).getHealth, PortedRoute{"exact:/api/health", "GET,HEAD"}),
		exact("/api/project", (*reqCtx).getProject, PortedRoute{"exact:/api/project", "GET,HEAD"}),
		exact("/api/resources/graph", func(c *reqCtx) error { return c.resourcesHandlers().Graph(c) }, PortedRoute{"exact:/api/resources/graph", "GET,HEAD"}),
		// Go only (routes_resources.go: SSH connections of the graph's devices).
		exact(resourcesConnectPath, func(c *reqCtx) error { return c.resourcesHandlers().Connect(c) }),
		exact("/api/search/nodes", (*reqCtx).searchNodes, PortedRoute{"exact:/api/search/nodes", "GET,HEAD"}),
		exact("/api/revision", (*reqCtx).getRevision, PortedRoute{"exact:/api/revision", "GET,HEAD"}),
		exact("/api/units", (*reqCtx).getUnits, PortedRoute{"exact:/api/units", "GET,HEAD"}),
		exact("/api/graph/snapshot", (*reqCtx).getGraphSnapshot, PortedRoute{"exact:/api/graph/snapshot", "GET,HEAD"}),
		exact("/api/graph", (*reqCtx).getGraph, PortedRoute{"exact:/api/graph", "GET,HEAD"}),
		exact("/api/timeline", (*reqCtx).getTimeline, PortedRoute{"exact:/api/timeline", "GET,HEAD"}),
		exact("/api/blackbox", (*reqCtx).blackboxGet, PortedRoute{"exact:/api/blackbox", "GET,HEAD"}),
		// Go only (routes_trash.go; ids in GoOnlyRoutes).
		exact(trashPath, (*reqCtx).trashGet),
		// Go only (routes_projectmove.go, T-0068).
		when(isProjectMovePath, func(c *reqCtx, path string) error {
			return c.projectMoveRoute(strings.TrimPrefix(path, projectMoveBase), false)
		}),
		// Go only (notifications.go; ids in GoOnlyRoutes).
		exact(notificationsPath, (*reqCtx).getNotifications),
		// Go only (project_reviews.go; ids in GoOnlyRoutes).
		exact(needsAssignedPath, (*reqCtx).getNeedsAssigned),
		// Go only (routes_importark.go, T-0070: import from another ARK project).
		when(isImportArkPath, (*reqCtx).importArkGet),
		// Go only (routes_reviews.go; ids in GoOnlyRoutes).
		when(reviewsGetPath, (*reqCtx).reviewsGet),
		// Go only (mobile_tunnel.go; ids in GoOnlyRoutes).
		exact(mobileTunnelPath, (*reqCtx).mobileTunnelGet),
		exact(mobilePendingPath, (*reqCtx).mobilePendingGet),
		exact("/api/remote/info", (*reqCtx).remoteInfo, PortedRoute{"exact:/api/remote/info", "GET,HEAD"}),
		when(isMobileGetPath, func(c *reqCtx, path string) error { _, err := c.mobileGet(path); return err },
			PortedRoute{"exact:/api/mobile/apps", "GET,HEAD"},
			PortedRoute{"exact:/api/mobile/connection", "GET,HEAD"},
			PortedRoute{"exact:/api/mobile/pairs", "GET,HEAD"},
			PortedRoute{"exact:/api/mobile/session/projects", "GET,HEAD"},
			PortedRoute{"exact:/api/mobile/session", "GET,HEAD"}),
		exact("/api/personal/agent-tokens", (*reqCtx).personalTokensGet, PortedRoute{"exact:/api/personal/agent-tokens", "GET,HEAD"}),
		exact("/api/activity", (*reqCtx).getActivity, PortedRoute{"exact:/api/activity", "GET,HEAD"}),
		// Go only (routes_agentwatch.go; ids in GoOnlyRoutes).
		exact(humanMessagesPath, (*reqCtx).humanMessages),
		// Go only (routes_agentrules.go, T-0053).
		exact(agentRulesPath, (*reqCtx).agentRules),
		// Go only (routes_prompts.go: the Home prompt library).
		exact(promptsPath, (*reqCtx).prompts),
		sub((*reqCtx).providersGet,
			PortedRoute{"exact:/api/providers", "GET,HEAD"},
			PortedRoute{"exact:/api/worktrees", "GET,HEAD"}),
		prefix("/api/agent-sessions", (*reqCtx).sessionsGet, sessionsPorted...),
		sub((*reqCtx).sessionContextsGet, sessionContextsPorted...),
		when(isWorkspacePath, (*reqCtx).workspaceGet, workspacePorted...),
		match(reHistoryFull, func(c *reqCtx, m []string) error { return c.nodeHistoryGet(m[1], m[2]) },
			PortedRoute{`fullmatch:/api/units/([^/]+)/history(?:/([^/]+))?`, "GET,HEAD"}),
		// Go only (routes_batch.go; ids in GoOnlyRoutes): before the unit route.
		exact(unitsBatchPath, (*reqCtx).unitsBatchGet),
		match(reUnit, (*reqCtx).getUnit, PortedRoute{`regex:^/api/units/([^/]+)$`, "GET,HEAD"}),
		match(reComments, (*reqCtx).getComments, PortedRoute{`regex:^/api/units/([^/]+)/comments$`, "GET,HEAD"}),
	}

	postBeforeOrigin = []route{sub(func(c *reqCtx, path string) (bool, error) { return c.gpuHandlers().TaskPost(c, path) })}
	postBeforeBody = []route{
		profile,
		exact("/api/chat/stream", (*reqCtx).chatStream),
	}
	postRoutes = []route{
		// Go only (routes_desktop.go; ids in GoOnlyRoutes).
		exact(desktopLinkPath, func(c *reqCtx) error { return c.desktopLink(c.body) }),
		exact(mobile.DesktopProjectsPath, func(c *reqCtx) error { return c.desktopProjectsPost(c.body) }),
		// Go only (routes_chat.go, D-0003: the ai:chat permission of view plugins).
		exact(chatapi.PluginChatPath, func(c *reqCtx) error { return c.chatHandlers().PluginChat(c, c.body, c.writer) }),
		// Go only (routes_presence.go, T-0043: an agent announces what it works on).
		exact(presenceAnnouncePath, func(c *reqCtx) error { return c.presenceAnnounce(c.body) }),
		// Go only (routes_agentwatch.go, T-0044 experimental messages).
		exact(humanMessagesPath, (*reqCtx).humanMessages),
		// Go only (routes_agentrules.go, T-0053).
		exact(agentRulesPath, (*reqCtx).agentRules),
		// Go only (routes_projectmove.go, T-0068: split and merge projects).
		when(isProjectMovePath, func(c *reqCtx, path string) error {
			return c.projectMoveRoute(strings.TrimPrefix(path, projectMoveBase), true)
		}),
		// Go only (routes_prompts.go: the Home prompt library).
		exact(promptsPath, (*reqCtx).prompts),
		// Go only (routes_importark.go, T-0070).
		when(isImportArkPath, (*reqCtx).importArkPost),
		// Go only (routes_invites.go: project invite links).
		match(reProjectInvites, (*reqCtx).projectInvitesPost),
		match(reInviteRevoke, (*reqCtx).projectInviteRevoke),
		exact(inviteSettingsPath, func(c *reqCtx) error { return c.inviteSettingsPost() }),
		// Go only (routes_storage.go).
		exact(projectStoragePath, (*reqCtx).projectStoragePost),
		exact(projectRevealPath, (*reqCtx).projectReveal),
		sub(func(c *reqCtx, path string) (bool, error) { return c.orgPost(path, c.body) }, orgPorted...),
		sub(func(c *reqCtx, path string) (bool, error) { return c.personalSettingsPost(path, c.body) }),
		// Go only (mobile_tunnel.go): claims through the tunnel wait for the computer.
		sub((*reqCtx).tunnelClaim),
		exact(mobileTunnelPath, func(c *reqCtx) error { return c.mobileTunnelPost(c.body) }),
		match(reMobilePendingConfirm, func(c *reqCtx, m []string) error { return c.mobilePendingConfirm(m[1], c.body) }),
		sub(func(c *reqCtx, path string) (bool, error) { return c.mobilePost(path, c.body) }, mobilePorted...),
		// Go only (routes_mobile.go: what one of the person's devices may do).
		match(reMobilePairPermission, func(c *reqCtx, m []string) error { return c.mobilePairPermission(m[1], c.body) }),
		sub(func(c *reqCtx, path string) (bool, error) { return c.servicesPost(path, c.body, c.writer) }),
		when(isWorkspacePath, func(c *reqCtx, path string) error { return c.workspacePost(path, c.body) }),
		sub(func(c *reqCtx, path string) (bool, error) { return c.providersPost(path, c.body) },
			PortedRoute{"exact:/api/providers/refresh", "POST"},
			PortedRoute{"exact:/api/worktrees/create", "POST"},
			PortedRoute{"exact:/api/worktrees/remove", "POST"}),
		sub(func(c *reqCtx, path string) (bool, error) { return c.sessionsPost(path, c.body, c.writer) }),
		sub(func(c *reqCtx, path string) (bool, error) { return c.sessionContextsPost(path, c.body) }),
		prefix("/api/agent/v1/", func(c *reqCtx, path string) error {
			if err := c.requireAgent(); err != nil {
				return err
			}
			return c.agentWrite(path, c.body, c.writer)
		}, agentWritePorted...),
		prefix("/api/skills/", func(c *reqCtx, path string) error { return c.skillsPost(path, c.body, c.writer) }, skillsPorted...),
		exact("/api/projects", func(c *reqCtx) error { return c.createProject(c.body) }, PortedRoute{"exact:/api/projects", "POST"}),
		exact("/api/projects/open", func(c *reqCtx) error { return c.openProject(c.body) }, PortedRoute{"exact:/api/projects/open", "POST"}),
		// Go only (project_reviews.go; ids in GoOnlyRoutes).
		exact(needsSeenPath, (*reqCtx).postNeedsSeen),
		sub(func(c *reqCtx, path string) (bool, error) { return c.blackboxPost(path, c.body) },
			PortedRoute{"exact:/api/blackbox", "POST"},
			PortedRoute{`regex:^/api/blackbox/([^/]+)/restore$`, "POST"}),
		match(reHistoryRaw, func(c *reqCtx, m []string) error { return c.nodeHistoryWrite(m[1], m[2], c.body, c.writer) },
			PortedRoute{`fullmatch:/api/units/([^/]+)/history(?:/([^/]+))?`, "POST"}),
		// Go only (routes_reviews.go; ids in GoOnlyRoutes).
		when(isReviewsPostPath, (*reqCtx).reviewsPost),
		// Go only (routes_batch.go; ids in GoOnlyRoutes).
		when(isBatchPostPath, (*reqCtx).batchPost),
		sub(func(c *reqCtx, path string) (bool, error) { return c.storePost(path, c.body, c.writer) }, storePorted...),
		match(reComments, (*reqCtx).postComment, PortedRoute{`regex:^/api/units/([^/]+)/comments$`, "POST"}),
		match(reResolve, (*reqCtx).postResolve, PortedRoute{`regex:^/api/units/([^/]+)/comments/(\d+)/resolve$`, "POST"}),
		sub(func(c *reqCtx, path string) (bool, error) { return c.assistantConfigPost(path, c.body) }),
		sub(func(c *reqCtx, path string) (bool, error) { return c.resourcesHandlers().DevicePost(c, path, c.body) },
			PortedRoute{"fullmatch:/api/resources/devices/([^/]+)", "POST"}),
		// Go only (routes_resources.go).
		sub(func(c *reqCtx, path string) (bool, error) { return c.resourcesHandlers().ConnectPost(c, path, c.body) }),
	}

	writeProfileRoutes = []route{profile}
	patchBeforeProject = []route{
		sub(func(c *reqCtx, path string) (bool, error) { return c.orgPatch(path, c.body) },
			PortedRoute{`regex:^/api/org/users/([^/]+)$`, "PATCH"},
			PortedRoute{`regex:^/api/org/teams/([^/]+)$`, "PATCH"}),
	}
	patchRoutes = []route{
		exact("/api/project", func(c *reqCtx) error { return c.updateProject(c.body) }, PortedRoute{"exact:/api/project", "PATCH"}),
		exact("/api/project/settings", func(c *reqCtx) error { return c.updateProjectSettings(c.body) },
			PortedRoute{"exact:/api/project/settings", "PATCH"}),
		sub(func(c *reqCtx, path string) (bool, error) { return c.sessionContextsPatch(path, c.body) },
			PortedRoute{"fullmatch:/api/session-contexts/([^/]+)", "PATCH"}),
		sub(func(c *reqCtx, path string) (bool, error) { return c.sessionsPatch(path, c.body) },
			PortedRoute{`regex:^/api/agent-sessions/([^/]+)/steer/([^/]+)$`, "PATCH"}),
		// Go only (routes_agentlinks.go; ids in GoOnlyRoutes).
		when(isAgentLinkWritePath, (*reqCtx).agentLinkWrite),
		sub(func(c *reqCtx, path string) (bool, error) {
			return c.storePatch(c.routeProject, path, c.body, c.writer)
		}),
	}

	deleteBeforeActor = []route{
		exact("/api/mobile/session", (*reqCtx).mobileDelete, PortedRoute{"exact:/api/mobile/session", "DELETE"}),
	}
	deleteBeforeProject = []route{
		sub((*reqCtx).orgDelete),
		exact("/api/project", func(c *reqCtx) error {
			body, err := c.readJSON()
			if err != nil {
				return err
			}
			return c.deleteProject(body)
		}, PortedRoute{"exact:/api/project", "DELETE"}),
	}
	deleteRoutes = []route{
		match(reTerminal, func(c *reqCtx, m []string) error { return c.terminalDelete(m[1]) },
			PortedRoute{`regex:^/api/workspace/terminals/([^/]+)$`, "DELETE"}),
		exact("/api/chat", (*reqCtx).deleteChat, PortedRoute{"exact:/api/chat", "DELETE"}),
		sub((*reqCtx).sessionsDelete,
			PortedRoute{`regex:^/api/agent-sessions/([^/]+)$`, "DELETE"},
			PortedRoute{`regex:^/api/agent-sessions/([^/]+)/steer/([^/]+)$`, "DELETE"}),
		sub((*reqCtx).sessionContextsDelete, PortedRoute{"fullmatch:/api/session-contexts/([^/]+)", "DELETE"}),
		// Go only (routes_trash.go; ids in GoOnlyRoutes).
		exact(trashPath, (*reqCtx).trashEmpty),
		match(reTrashUnit, (*reqCtx).trashPurge),
		// Go only (routes_agentlinks.go; ids in GoOnlyRoutes).
		when(isAgentLinkWritePath, (*reqCtx).agentLinkWrite),
		sub(func(c *reqCtx, path string) (bool, error) { return c.storeDelete(c.routeProject, path, c.writer) }),
	}

	allDispatchTables = [][]route{getRoutes, postBeforeOrigin, postBeforeBody, postRoutes, writeProfileRoutes,
		patchBeforeProject, patchRoutes, deleteBeforeActor, deleteBeforeProject, deleteRoutes}
	Ported = derivePorted()
}

// derivePorted merges the pipeline ids and every registration's ids, one
// record per contract id with its verbs in first-seen order.
func derivePorted() []PortedRoute {
	var order []string
	verbs := map[string][]string{}
	add := func(r PortedRoute) {
		if _, ok := verbs[r.ID]; !ok {
			order = append(order, r.ID)
		}
		for _, m := range strings.Split(r.Methods, ",") {
			if !containsMethod(verbs[r.ID], m) {
				verbs[r.ID] = append(verbs[r.ID], m)
			}
		}
	}
	for _, r := range pipelineRoutes {
		add(r)
	}
	for _, r := range hostopenPorted {
		add(r)
	}
	for _, table := range allDispatchTables {
		for _, rt := range table {
			for _, r := range rt.ids {
				add(r)
			}
		}
	}
	out := make([]PortedRoute, len(order))
	for i, id := range order {
		out[i] = PortedRoute{id, strings.Join(verbs[id], ",")}
	}
	return out
}

func isMobileGetPath(path string) bool {
	switch path {
	case "/api/mobile/apps", "/api/mobile/connection", "/api/mobile/pairs", "/api/mobile/session/projects", "/api/mobile/session":
		return true
	}
	return false
}
