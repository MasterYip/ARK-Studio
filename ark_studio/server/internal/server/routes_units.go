package server

import (
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/compat/pyurl"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/platform/pyerr"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/store"
)

// storePorted are the contract routes this file answers (router.go registers them).
var storePorted = []PortedRoute{
	{"exact:/api/links", "POST"},
	{"exact:/api/media", "POST"},
	{"exact:/api/media/upload", "POST"},
	{"exact:/api/units", "POST"},
	{`regex:^/api/links/(\d+)$`, "DELETE,PATCH"},
	{`regex:^/api/media/(\d+)$`, "DELETE,PATCH"},
	{"regex:^/api/units/([^/]+)$", "DELETE,PATCH"},
	{"regex:^/api/units/([^/]+)/archive$", "POST"},
	{"regex:^/api/units/([^/]+)/insert-between$", "POST"},
	{"regex:^/api/units/([^/]+)/members$", "PATCH,POST"},
	{"regex:^/api/units/([^/]+)/members/([^/]+)$", "DELETE"},
	{"regex:^/api/units/([^/]+)/restore$", "POST"},
}

// Unit, link, media and topic-member writes (do_POST / do_PATCH / do_DELETE
// branches after ``store = self.app.require().store``).

var (
	reLink          = pyRe(`^/api/links/(\d+)$`, false)
	reMediaID       = pyRe(`^/api/media/(\d+)$`, false)
	reMemberDel     = pyRe(`^/api/units/([^/]+)/members/([^/]+)$`, false)
	reArchive       = pyRe(`^/api/units/([^/]+)/archive$`, false)
	reRestore       = pyRe(`^/api/units/([^/]+)/restore$`, false)
	reInsertBetween = pyRe(`^/api/units/([^/]+)/insert-between$`, false)
)

// resolveAssignee is App.resolve_assignee bound to a project.
func (c *reqCtx) resolveAssignee(name, projectID string) (string, error) {
	text := pytext.Strip(name)
	username := ""
	found := false
	if text != "" {
		row, ok, err := c.app.Org.UserByName(c.ctx, text)
		if err != nil {
			return "", err
		}
		if ok {
			found, username = true, row.Str("username")
		}
	}
	if c.app.Auth == nil {
		if pytext.Lower(text) == org.LocalUsername {
			return org.LocalUsername, nil
		}
	} else if found {
		member, err := c.app.Org.ProjectMember(c.ctx, username, projectID)
		if err != nil {
			return "", err
		}
		if member {
			return username, nil
		}
	}
	if found {
		return "", &store.Error{Message: pyjson.Repr(text) + " has not joined this project: assign a project member, or null to unassign."}
	}
	return "", &store.Error{Message: "Unknown assignee " + pyjson.Repr(text) + ": use an existing ARK account username, or null to unassign."}
}

func (c *reqCtx) who(p *project.Project) store.Who {
	pid := p.ID()
	return store.Who{
		Resolve: func(name string) (string, error) { return c.resolveAssignee(name, pid) },
		Creator: c.actor,
	}
}

// intArg is int(m.group(n)) of a \d+ route group (Unicode digits allowed).
func intArg(s string) (int64, error) {
	n, err := pytext.Int(s)
	if err != nil {
		if _, ok := err.(*pytext.IntError); ok {
			return 0, &pyerr.ValueError{Message: err.Error()}
		}
		return 0, &pyerr.InternalError{Message: "Python int too large to convert to SQLite INTEGER"}
	}
	return n, nil
}

func orDefault(v any, def any) any {
	if pyjson.Truthy(v) {
		return v
	}
	return def
}

// storePost handles the store-backed POST routes; handled=false means the
// path is not one of them.
func (c *reqCtx) storePost(path string, body *pyjson.Object, actor string) (bool, error) {
	route := ""
	var m []string
	switch {
	case path == "/api/units", path == "/api/links", path == "/api/media", path == "/api/media/upload":
		route = path
	case reMembers.MatchString(path):
		route, m = "members", reMembers.FindStringSubmatch(path)
	case reArchive.MatchString(path):
		route, m = "archive", reArchive.FindStringSubmatch(path)
	case reRestore.MatchString(path):
		route, m = "restore", reRestore.FindStringSubmatch(path)
	case reInsertBetween.MatchString(path):
		route, m = "insert", reInsertBetween.FindStringSubmatch(path)
	default:
		return false, nil
	}
	p, err := c.require("read")
	if err != nil {
		return true, err
	}
	s := p.Store
	var out *pyjson.Object
	switch route {
	case "/api/units":
		unit, err := s.CreateUnit(c.ctx, body, actor, c.who(p))
		if err != nil {
			return true, err
		}
		out = pyjson.NewObject("unit", unit)
	case "/api/links":
		out, err = s.CreateLink(c.ctx, body.Value("source"), body.Value("target"),
			orDefault(body.Value("relation"), "relates_to"), orDefault(body.Value("note"), ""), actor)
	case "/api/media/upload":
		out, err = c.embedMedia(body, actor)
	case "/api/media":
		out, err = s.AddMedia(c.ctx, body.Value("unit"), body.Value("url"), orDefault(body.Value("kind"), "link"),
			orDefault(body.Value("caption"), ""), actor)
	case "members":
		add := body.Value("add")
		if !pyjson.Truthy(add) {
			add = body.Value("members")
		}
		if !pyjson.Truthy(add) {
			add = []any{}
		}
		return true, c.designReply(s, m[1], body.Copy().Set("add", add), actor)
	case "archive":
		out, err = s.ArchiveUnit(c.ctx, m[1], actor)
	case "restore":
		out, err = s.RestoreUnit(c.ctx, m[1], actor)
	case "insert":
		out, err = s.InsertBetween(c.ctx, m[1], orDefault(body.Value("title"), "New node"),
			orDefault(body.Value("type"), "note"), actor, c.who(p))
	}
	if err != nil {
		return true, err
	}
	c.ok(out)
	return true, nil
}

func (c *reqCtx) designReply(s *store.Store, ref string, body *pyjson.Object, actor string) error {
	report, err := s.DesignTopic(c.ctx, ref, body, actor)
	if err != nil {
		return err
	}
	topic, err := s.Topic(c.ctx, ref, false)
	if err != nil {
		return err
	}
	out := pyjson.NewObject("report", report)
	for _, k := range topic.Keys() {
		out.Set(k, topic.Value(k))
	}
	c.ok(out)
	return nil
}

// storePatch handles the store-backed PATCH routes.
func (c *reqCtx) storePatch(p *project.Project, path string, body *pyjson.Object, actor string) (bool, error) {
	s := p.Store
	var out *pyjson.Object
	switch {
	case reMediaID.MatchString(path):
		id, err := intArg(reMediaID.FindStringSubmatch(path)[1])
		if err != nil {
			return true, err
		}
		media, err := s.UpdateMedia(c.ctx, id, body, actor)
		if err != nil {
			return true, err
		}
		out = pyjson.NewObject("media", media)
	case reLink.MatchString(path):
		id, err := intArg(reLink.FindStringSubmatch(path)[1])
		if err != nil {
			return true, err
		}
		if out, err = s.UpdateLink(c.ctx, id, orDefault(body.Value("relation"), "unset"), actor); err != nil {
			return true, err
		}
	case reMembers.MatchString(path):
		return true, c.designReply(s, reMembers.FindStringSubmatch(path)[1], body, actor)
	case reUnit.MatchString(path):
		unit, err := s.UpdateUnit(c.ctx, reUnit.FindStringSubmatch(path)[1], body, actor, c.who(p))
		if err != nil {
			return true, err
		}
		out = pyjson.NewObject("unit", unit)
	default:
		return false, nil
	}
	c.ok(out)
	return true, nil
}

// storeDelete handles the store-backed DELETE routes.
func (c *reqCtx) storeDelete(p *project.Project, path string, actor string) (bool, error) {
	s := p.Store
	var out *pyjson.Object
	var err error
	switch {
	case reLink.MatchString(path):
		id, ierr := intArg(reLink.FindStringSubmatch(path)[1])
		if ierr != nil {
			return true, ierr
		}
		out, err = s.DeleteLink(c.ctx, id, actor)
	case reMediaID.MatchString(path):
		id, ierr := intArg(reMediaID.FindStringSubmatch(path)[1])
		if ierr != nil {
			return true, ierr
		}
		out, err = s.DeleteMedia(c.ctx, id, actor)
	case reMemberDel.MatchString(path):
		m := reMemberDel.FindStringSubmatch(path)
		out, err = s.RemoveTopicMember(c.ctx, m[1], pyurl.Unquote(m[2]), actor)
	case reUnit.MatchString(path):
		out, err = s.ArchiveUnit(c.ctx, reUnit.FindStringSubmatch(path)[1], actor)
	default:
		return false, nil
	}
	if err != nil {
		return true, err
	}
	c.ok(out)
	return true, nil
}
