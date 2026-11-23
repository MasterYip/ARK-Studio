package server

// The Assistant's reach (Go only, internal/chat/scope.go): the signed-in
// person's projects, each at their own access there, capped like every other
// request by a read-only paired device or an agent token's scope.  The
// current project is the default; the tutorial project of another person is
// never listed.

import (
	"sync"

	"arkstudio/go/internal/chat"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/platform/pyerr"
	"arkstudio/go/internal/project"
)

// assistantRole is the person's access to a project for the Assistant: ""
// when they cannot read it.
func (c *reqCtx) assistantRole(projectID string) (string, error) {
	perm := "admin" // a server without accounts
	if c.app.Auth != nil {
		if c.actor() == "" {
			return "", nil
		}
		if projectID == demoID {
			perm = "write"
		} else {
			var err error
			if perm, err = c.app.Org.ProjectPermission(c.ctx, c.actor(), projectID); err != nil || perm == "" {
				return "", err
			}
		}
	}
	if c.mobileSession != nil && c.mobileSession.Permission == "read_only" && org.PermissionRank[perm] > org.PermissionRank["read"] {
		perm = "read"
	}
	if c.token != nil {
		scope := pyjson.Str(c.token.row.Get("scope"))
		if _, ok := org.PermissionRank[scope]; !ok {
			scope = "read"
		}
		if org.PermissionRank[perm] > org.PermissionRank[scope] {
			perm = scope
		}
	}
	return perm, nil
}

// assistantScope is the chat.Scope of one Assistant request in project cur.
func (c *reqCtx) assistantScope(cur *project.Project) *chat.Scope {
	if cur == nil {
		return nil
	}
	curRole, err := c.assistantRole(cur.ID())
	if err != nil || curRole == "" {
		return nil
	}
	current := chat.ProjectRef{ID: cur.ID(), Name: cur.Name(), Role: curRole}
	var once sync.Once
	var listed []chat.ProjectRef
	var listErr error
	projects := func() ([]chat.ProjectRef, error) {
		once.Do(func() {
			listed = []chat.ProjectRef{current}
			items, err := c.visibleProjects()
			if err != nil {
				listErr = err
				return
			}
			for _, it := range items {
				s, _ := it.(*pyjson.Object)
				if s == nil {
					continue
				}
				id := pyjson.Str(s.Value("id"))
				if id == "" || id == current.ID || id == demoID || pyjson.Truthy(s.Value("demo")) {
					continue
				}
				role, err := c.assistantRole(id)
				if err != nil {
					listErr = err
					return
				}
				if role != "" {
					listed = append(listed, chat.ProjectRef{ID: id, Name: pyjson.Str(s.Value("name")), Role: role})
				}
			}
		})
		return listed, listErr
	}
	refuse := func(msg string) error { return &pyerr.ValueError{Message: msg} }
	open := func(id, level string) (*chat.Target, error) {
		list, err := projects()
		if err != nil {
			return nil, err
		}
		ref, ok := chat.FindProject(list, id)
		if !ok || ref.ID != id {
			return nil, refuse("Project not found or not accessible for this account.")
		}
		// Access is read again, not taken from the list: grants can change.
		role, err := c.assistantRole(id)
		if err != nil {
			return nil, err
		}
		if role == "" {
			return nil, refuse("Project not found or not accessible for this account.")
		}
		ref.Role = role
		if !chat.RoleAllows(role, level) {
			return nil, refuse("Your access to " + ref.Name + " is " + role + "; this change needs write access there.")
		}
		p := cur
		if id != cur.ID() {
			if p, err = c.projectByID(id); err != nil {
				return nil, err
			}
			if p == nil || p.ID() != id {
				return nil, refuse("Project not found or not accessible for this account.")
			}
		}
		return &chat.Target{Ref: ref, Store: p.Store, Who: c.who(p)}, nil
	}
	return &chat.Scope{Current: current, Projects: projects, Open: open}
}
