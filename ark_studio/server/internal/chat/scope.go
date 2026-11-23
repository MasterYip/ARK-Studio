package chat

// The Assistant's reach (Go only): the signed-in person's projects.  The
// current project is the default; any other project the person can read is
// reachable through the optional "project" argument of the tools, at the
// person's own access level there.  internal/server builds the Scope from
// the same permission checks as the rest of the API.

import (
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/store"
)

// ProjectRef is one project the person can read and their access there
// ("read", "write" or "admin").
type ProjectRef struct {
	ID, Name, Role string
}

// Target is an opened project: its store and the write identity for it.
type Target struct {
	Ref   ProjectRef
	Store *store.Store
	Who   store.Who
}

// Scope is what the Assistant may reach for one request.
type Scope struct {
	Current ProjectRef
	// Projects lists every project the person can read, the current one included.
	Projects func() ([]ProjectRef, error)
	// Open opens a project from Projects after checking the person's access
	// against level ("read" or "write").  A refusal is a ValueError, so the
	// model sees it as the tool's error.
	Open func(id, level string) (*Target, error)
}

var permissionRank = map[string]int{"read": 1, "write": 2, "admin": 3}

// RoleAllows reports whether role covers level.
func RoleAllows(role, level string) bool { return permissionRank[role] >= permissionRank[level] }

// CrossRef is the reference of a unit in another project, as bodies and the
// UI write it: [[prj_xxx/T-0042]].
func CrossRef(projectID, uid string) string { return "[[" + projectID + "/" + uid + "]]" }

// FindProject picks a project of list by id, else by its exact name
// (case-insensitive).
func FindProject(list []ProjectRef, ref string) (ProjectRef, bool) {
	ref = pytext.Strip(ref)
	for _, p := range list {
		if p.ID == ref {
			return p, true
		}
	}
	for _, p := range list {
		if strings.EqualFold(p.Name, ref) {
			return p, true
		}
	}
	return ProjectRef{}, false
}

// projectArg is the tool's "project" argument ("" = the current project).
func projectArg(args *pyjson.Object) string {
	if args == nil {
		return ""
	}
	return pytext.Strip(orEmpty(args.Value("project")))
}

// target opens the project a tool call names (the current one by default)
// at level.  Without a Scope (plugin conversations, tests) only the current
// project is reachable and writes are not re-checked here: the route did.
func (e *Env) target(args *pyjson.Object, level string) (*Target, error) {
	ref := projectArg(args)
	if e.Scope == nil {
		if ref != "" && ref != e.ProjectID && !strings.EqualFold(ref, e.ProjectName) {
			return nil, valueErr("Only the current project is available here.")
		}
		return &Target{Ref: ProjectRef{ID: e.ProjectID, Name: e.ProjectName}, Store: e.Store, Who: e.Who}, nil
	}
	id := e.Scope.Current.ID
	if ref != "" {
		list, err := e.Scope.Projects()
		if err != nil {
			return nil, err
		}
		p, ok := FindProject(list, ref)
		if !ok {
			return nil, valueErr("Project " + ref + " is not one of your projects. Use list_projects to see the projects you can read.")
		}
		id = p.ID
	}
	return e.Scope.Open(id, level)
}

// isCurrent reports whether t is the current project.
func (e *Env) isCurrent(t *Target) bool {
	if e.Scope != nil {
		return t.Ref.ID == e.Scope.Current.ID
	}
	return true
}

// withProject marks a tool result from another project: the project and,
// on every listed unit, the reference to cite.
func (e *Env) withProject(t *Target, out *pyjson.Object, lists ...[]any) *pyjson.Object {
	if e.isCurrent(t) {
		return out
	}
	out.Set("project", pyjson.NewObject("id", t.Ref.ID, "name", t.Ref.Name, "role", t.Ref.Role))
	for _, list := range lists {
		for _, it := range list {
			if o, ok := it.(*pyjson.Object); ok {
				if uid := orEmpty(o.Value("uid")); uid != "" {
					o.Set("cite", CrossRef(t.Ref.ID, uid))
				}
			}
		}
	}
	return out
}

// maxPromptProjects caps the other projects named in the system prompt
// (list_projects has them all).
const maxPromptProjects = 30

// scopePrompt is the system prompt's part about the other projects.
func (e *Env) scopePrompt() ([]string, error) {
	if e.Scope == nil {
		return nil, nil
	}
	list, err := e.Scope.Projects()
	if err != nil {
		return nil, err
	}
	cur := e.Scope.Current
	lines := []string{
		"",
		"Scope: the current project is " + cur.Name + " (id " + cur.ID + ", your access: " + cur.Role + "). Every tool works on it unless you pass project.",
	}
	var others []string
	for _, p := range list {
		if p.ID == cur.ID {
			continue
		}
		if len(others) == maxPromptProjects {
			others = append(others, "… (list_projects has the rest)")
			break
		}
		others = append(others, p.Name+" (id "+p.ID+", "+p.Role+")")
	}
	if len(others) == 0 {
		return append(lines, "The user has no other projects you can read."), nil
	}
	return append(lines,
		"You may also read the user's other projects, at the user's own access there: "+strings.Join(others, "; ")+".",
		"Read another project only when the user asks about it or it clearly helps; pass its id as project. Change another project only when the user asks for it there, and only where their access is write or admin.",
		"UIDs repeat across projects (T-0001 exists in many). A bare UID always means the current project. When you mention a unit from another project, name the project and write the unit as [[<project id>/<UID>]], for example "+CrossRef("prj_abc123", "T-0004")+"; tool results give it as cite.",
	), nil
}

// listProjects is the list_projects tool.
func (e *Env) listProjects() (*pyjson.Object, error) {
	if e.Scope == nil {
		return pyjson.NewObject("current", e.ProjectID, "projects", []any{
			pyjson.NewObject("id", e.ProjectID, "name", e.ProjectName, "current", true)}), nil
	}
	list, err := e.Scope.Projects()
	if err != nil {
		return nil, err
	}
	out := []any{}
	for _, p := range list {
		out = append(out, pyjson.NewObject("id", p.ID, "name", p.Name, "role", p.Role, "current", p.ID == e.Scope.Current.ID))
	}
	return pyjson.NewObject("current", e.Scope.Current.ID, "projects", out), nil
}

// neighbors is the get_neighbors tool: the units within depth links of ref.
func (e *Env) neighbors(t *Target, ref string, depth int64) (*pyjson.Object, error) {
	start, _, err := t.Store.UnitLinks(e.Ctx, ref)
	if err != nil {
		return nil, err
	}
	const maxNodes = 60
	seen := map[string]bool{start.UID: true}
	nodes := []any{pyjson.NewObject("uid", start.UID, "title", start.Title, "depth", int64(0))}
	edges := []any{}
	edgeSeen := map[string]bool{}
	frontier := []string{start.UID}
	truncated := false
	for d := int64(1); d <= depth && len(frontier) > 0; d++ {
		var next []string
		for _, uid := range frontier {
			_, links, err := t.Store.UnitLinks(e.Ctx, uid)
			if err != nil {
				return nil, err
			}
			for _, l := range links {
				link := obj(l)
				key := pyjson.Str(link.Value("id"))
				if !edgeSeen[key] {
					edgeSeen[key] = true
					edges = append(edges, pyjson.NewObject("source", link.Value("source"), "target", link.Value("target"),
						"relation", link.Value("relation")))
				}
				other := obj(link.Value("other"))
				ouid := orEmpty(other.Value("uid"))
				if ouid == "" || seen[ouid] {
					continue
				}
				if len(nodes) >= maxNodes {
					truncated = true
					continue
				}
				seen[ouid] = true
				nodes = append(nodes, pyjson.NewObject("uid", ouid, "title", other.Value("title"), "type", other.Value("type"),
					"status", other.Value("status"), "depth", d))
				next = append(next, ouid)
			}
		}
		frontier = next
	}
	return e.withProject(t, pyjson.NewObject("unit", start.UID, "depth", depth, "nodes", nodes, "edges", edges,
		"truncated", truncated), nodes), nil
}
