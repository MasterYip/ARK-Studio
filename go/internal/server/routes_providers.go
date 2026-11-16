package server

import (
	"crypto/subtle"
	"errors"
	"strings"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/worktrees"
)

// GET /api/providers, POST /api/providers/refresh and the /api/worktrees
// routes (providers.py, worktrees.py, Handler._worktrees_payload).

// sessionViewer is Handler._session_viewer: (user, admin).
func (c *reqCtx) sessionViewer() (string, bool, error) {
	user := c.actor()
	bearer := c.agentToken()
	if c.token == nil && bearer != "" && subtle.ConstantTimeCompare([]byte(bearer), []byte(c.app.AgentToken())) == 1 {
		return user, true, nil
	}
	perm, err := c.projectPermission()
	if err != nil {
		return "", false, err
	}
	return user, perm == "admin", nil
}

// accessibleSessionIDs is Handler._accessible_session_ids.
func (c *reqCtx) accessibleSessionIDs() (map[string]bool, error) {
	current, err := c.currentProject()
	if err != nil {
		return nil, err
	}
	if current == nil {
		return map[string]bool{}, nil
	}
	user, admin, err := c.sessionViewer()
	if err != nil {
		return nil, err
	}
	return c.app.Sessions.AccessibleIDs(current.ID(), user, admin), nil
}

// filterSessionGroups is Handler._filter_session_groups (groups only).
func (c *reqCtx) filterSessionGroups(data *pyjson.Object) ([]any, error) {
	allowed, err := c.accessibleSessionIDs()
	if err != nil {
		return nil, err
	}
	groups := []any{}
	list, _ := data.Value("groups").([]any)
	for _, g := range list {
		group := g.(*pyjson.Object)
		var sessions []any
		items, _ := group.Value("sessions").([]any)
		for _, s := range items {
			if allowed[pyjson.Str(s.(*pyjson.Object).Value("id"))] {
				sessions = append(sessions, s)
			}
		}
		if len(sessions) == 0 {
			continue
		}
		item := group.Copy()
		item.Set("sessions", sessions).Set("active", int64(len(sessions)))
		groups = append(groups, item)
	}
	return groups, nil
}

// worktreeManager binds the shared manager to this request's project
// (project_provider=lambda: self.require().summary()).
func (c *reqCtx) worktreeManager() *worktrees.Manager {
	return c.app.Worktrees.For(func() (string, error) {
		p, err := c.require("read")
		if err != nil {
			return "", err
		}
		return p.Path, nil
	})
}

// worktreesPayload is Handler._worktrees_payload.
func (c *reqCtx) worktreesPayload() (*pyjson.Object, error) {
	listing, err := c.worktreeManager().List()
	if err != nil {
		return nil, err
	}
	activeByPath := pyjson.NewObject()
	items, _ := listing.Value("worktrees").([]any)
	for _, x := range items {
		item := x.(*pyjson.Object)
		path := pyjson.Str(item.Value("path"))
		ids := c.app.Sessions.ForWorktree(path, true)
		idList := make([]any, len(ids))
		for i, id := range ids {
			idList[i] = id
		}
		item.Set("active_sessions", idList)
		item.Set("active_count", int64(len(ids)))
		item.Set("conflict", len(ids) > 1)
		if len(ids) > 1 {
			item.Set("status", "conflict")
		} else if len(ids) > 0 && item.Value("status") == "clean" {
			item.Set("status", "busy")
		}
		activeByPath.Set(path, idList)
	}
	filtered := pyjson.NewObject()
	for _, k := range activeByPath.Keys() {
		if v := activeByPath.Value(k).([]any); len(v) > 0 {
			filtered.Set(k, v)
		}
	}
	listing.Set("active_by_path", filtered)
	groups, err := c.filterSessionGroups(c.app.Sessions.Groups())
	if err != nil {
		return nil, err
	}
	listing.Set("groups", groups)
	allowed, err := c.accessibleSessionIDs()
	if err != nil {
		return nil, err
	}
	listing.Set("session_total", int64(len(allowed)))
	return listing, nil
}

// providersGet answers the read routes of this file.
func (c *reqCtx) providersGet(path string) (bool, error) {
	switch path {
	case "/api/providers":
		c.ok(c.app.Providers.Snapshot(false))
		return true, nil
	case "/api/worktrees":
		payload, err := c.worktreesPayload()
		if err != nil {
			return true, err
		}
		c.ok(payload)
		return true, nil
	}
	return false, nil
}

// providersPost answers the write routes of this file.
func (c *reqCtx) providersPost(path string, body *pyjson.Object) (bool, error) {
	switch path {
	case "/api/providers/refresh":
		if err := c.rejectTaskAgentReadToken("task-agent read tokens cannot refresh the host provider catalog"); err != nil {
			return true, err
		}
		c.ok(c.app.Providers.Refresh())
		return true, nil
	case "/api/worktrees/create":
		if err := c.rejectTaskAgentReadToken("task-agent read tokens cannot create host Git worktrees"); err != nil {
			return true, err
		}
		if _, err := c.require("admin"); err != nil {
			return true, err
		}
		result, err := c.worktreeManager().Create(body)
		if err != nil {
			return true, err
		}
		payload, err := c.worktreesPayload()
		if err != nil {
			return true, err
		}
		c.ok(result.Set("worktrees", payload))
		return true, nil
	case "/api/worktrees/remove":
		if err := c.rejectTaskAgentReadToken("task-agent read tokens cannot remove host Git worktrees"); err != nil {
			return true, err
		}
		if _, err := c.require("admin"); err != nil {
			return true, err
		}
		ref := body.Value("path")
		if !pyjson.Truthy(ref) {
			ref = body.Value("name")
		}
		if !pyjson.Truthy(ref) {
			ref = body.Value("branch")
		}
		if !pyjson.Truthy(ref) {
			ref = ""
		}
		target, err := c.worktreeManager().ResolveSessionTarget(ref)
		if err != nil {
			return true, err
		}
		if active := c.app.Sessions.ForWorktree(pyjson.Str(target.Value("path")), true); len(active) > 0 {
			return true, &worktrees.Error{Message: "stop the active session(s) before removing this worktree: " +
				strings.Join(active, ", "), Status: 409, Code: "WORKTREE_SESSION_ACTIVE"}
		}
		result, err := c.worktreeManager().Remove(body)
		if err != nil {
			return true, err
		}
		payload, err := c.worktreesPayload()
		if err != nil {
			return true, err
		}
		c.ok(result.Set("worktrees", payload))
		return true, nil
	}
	return false, nil
}

func isWorktreeError(err error) bool {
	_, ok := asWorktreeError(err)
	return ok
}

func asWorktreeError(err error) (*worktrees.Error, bool) {
	var we *worktrees.Error
	ok := errors.As(err, &we)
	return we, ok
}
