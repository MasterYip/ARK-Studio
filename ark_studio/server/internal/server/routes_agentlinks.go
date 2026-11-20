package server

// Agent link editing (Go only; store/linkquery.go, store.DeleteLink and
// store.UpdateLink, the same store paths as DELETE / PATCH /api/links/{id}):
//
//   GET    /api/agent/v1/units/{uid}/links   the unit's links, in and out (read access)
//   DELETE /api/agent/v1/links               {source, target, relation?} in the body or the query
//   DELETE /api/agent/v1/links/{id}          one link by id
//   PATCH  /api/agent/v1/links/{id}          {relation}: change the relation of one link
//
// Writes need write access (a read-scope agent token gets 403 from
// guardProjectScope), log link.delete / link.update with the agent actor and
// so reach the updates feed and presence like every other agent write.

import (
	"errors"
	"fmt"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/store"
)

const agentLinksPath = "/api/agent/v1/links"

var (
	reAgentLinkID    = pyRe(`^/api/agent/v1/links/(\d+)$`, false)
	reAgentUnitLinks = pyRe(`^/api/agent/v1/units/([^/]+)/links$`, false)
)

// agentLinksGoOnly are the Go-only ids of this file (GoOnlyRoutes).
var agentLinksGoOnly = []PortedRoute{
	{"goonly:exact:" + agentLinksPath, "DELETE"},
	{`goonly:regex:^/api/agent/v1/links/(\d+)$`, "DELETE,PATCH"},
	{`goonly:regex:^/api/agent/v1/units/([^/]+)/links$`, "GET,HEAD"},
}

func isAgentLinkWritePath(path string) bool {
	return path == agentLinksPath || reAgentLinkID.MatchString(path)
}

// linkNotFound turns the store's "unit not found" / "link not found" errors
// into 404 replies; other errors pass through.
func linkNotFound(err error) error {
	var se *store.Error
	if errors.As(err, &se) {
		switch {
		case strings.HasPrefix(se.Message, "unit not found"):
			return apiErr(404, se.Message, "UNIT_NOT_FOUND")
		case strings.HasPrefix(se.Message, "link not found"):
			return apiErr(404, se.Message, "LINK_NOT_FOUND")
		}
	}
	return err
}

// agentUnitLinks answers GET /api/agent/v1/units/{uid}/links
// (?direction=in|out, ?relation=).
func (c *reqCtx) agentUnitLinks(s *store.Store, ref string) error {
	unit, links, err := s.UnitLinks(c.ctx, ref)
	if err != nil {
		return linkNotFound(err)
	}
	direction := pytext.Lower(pytext.Strip(c.query.First("direction", "")))
	relation := pytext.Strip(c.query.First("relation", ""))
	kept := []any{}
	for _, item := range links {
		link := item.(*pyjson.Object)
		if (direction == "in" || direction == "out") && link.Value("direction") != direction {
			continue
		}
		if relation != "" && pyjson.Str(link.Value("relation")) != relation {
			continue
		}
		kept = append(kept, link)
	}
	c.ok(pyjson.NewObject("unit", pyjson.NewObject("id", unit.ID, "uid", unit.UID, "title", unit.Title),
		"links", kept, "total", int64(len(kept))))
	return nil
}

// agentLinkWrite answers DELETE /api/agent/v1/links[/{id}] and
// PATCH /api/agent/v1/links/{id}.
func (c *reqCtx) agentLinkWrite(path string) error {
	if err := c.requireAgent(); err != nil {
		return err
	}
	p, err := c.require("write")
	if err != nil {
		return err
	}
	s := p.Store
	if c.r.Method == "PATCH" {
		m := reAgentLinkID.FindStringSubmatch(path)
		if m == nil {
			return apiErr(405, "Change a link with PATCH /api/agent/v1/links/{id} {relation}; "+
				"GET /api/agent/v1/units/{uid}/links lists the ids.", "")
		}
		id, err := intArg(m[1])
		if err != nil {
			return err
		}
		return c.agentLinkRelation(s, id, c.body)
	}
	if m := reAgentLinkID.FindStringSubmatch(path); m != nil {
		id, err := intArg(m[1])
		if err != nil {
			return err
		}
		link, ok, err := s.Link(c.ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return apiErr(404, fmt.Sprintf("link not found: %d", id), "LINK_NOT_FOUND")
		}
		return c.agentDeleteLink(s, link)
	}
	body, err := c.readJSON()
	if err != nil {
		return err
	}
	arg := func(name string) string {
		if v := body.Value(name); pyjson.Truthy(v) {
			return pytext.Strip(pyjson.Str(v))
		}
		return pytext.Strip(c.query.First(name, ""))
	}
	source, target, relation := arg("source"), arg("target"), arg("relation")
	if source == "" || target == "" {
		return apiErr(400, "source and target are required (unit uids), relation is optional; "+
			"or delete one link with DELETE /api/agent/v1/links/{id}.", "")
	}
	src, dst, forward, reverse, err := s.LinksBetween(c.ctx, source, target)
	if err != nil {
		return linkNotFound(err)
	}
	match := func(links []*pyjson.Object) []*pyjson.Object {
		var out []*pyjson.Object
		for _, l := range links {
			if relation == "" || pyjson.Str(l.Value("relation")) == relation {
				out = append(out, l)
			}
		}
		return out
	}
	candidates := match(forward)
	if len(candidates) == 0 && relation != "" && isSymmetricRelation(relation) {
		candidates = match(reverse) // relates_to / unset have no direction
	}
	switch len(candidates) {
	case 0:
		return apiErr(404, noLinkMessage(src, dst, relation, forward, match(reverse)), "LINK_NOT_FOUND")
	case 1:
		return c.agentDeleteLink(s, candidates[0])
	}
	relations := make([]string, 0, len(candidates))
	items := make([]any, 0, len(candidates))
	for _, l := range candidates {
		relations = append(relations, pyjson.Str(l.Value("relation")))
		items = append(items, l)
	}
	msg := fmt.Sprintf("%s and %s are linked by %d relations (%s); pass relation to choose one, or delete by id.",
		src.UID, dst.UID, len(candidates), strings.Join(relations, ", "))
	c.drainUnreadBody()
	c.sendJSON(409, pyjson.NewObject("ok", false, "error", pyjson.NewObject("status", int64(409), "code", "LINK_AMBIGUOUS",
		"message", msg), "links", items), headerList{{"Connection", "close"}})
	return nil
}

func isSymmetricRelation(relation string) bool {
	for _, r := range store.SymmetricRelations {
		if r == relation {
			return true
		}
	}
	return false
}

func relationsOf(links []*pyjson.Object) string {
	var out []string
	for _, l := range links {
		out = append(out, pyjson.Str(l.Value("relation")))
	}
	return strings.Join(out, ", ")
}

func noLinkMessage(src, dst store.LinkEnd, relation string, forward, reverse []*pyjson.Object) string {
	what := "link"
	if relation != "" {
		what = relation + " link"
	}
	msg := fmt.Sprintf("No %s from %s to %s.", what, src.UID, dst.UID)
	if len(forward) > 0 {
		msg += fmt.Sprintf(" Links from %s to %s: %s.", src.UID, dst.UID, relationsOf(forward))
	}
	if len(reverse) > 0 {
		msg += fmt.Sprintf(" There is a link from %s to %s (%s); swap source and target to delete it.",
			dst.UID, src.UID, relationsOf(reverse))
	}
	return msg
}

// agentDeleteLink deletes one link with store.DeleteLink (activity
// link.delete) and replies {id, deleted, source, target, relation}.
func (c *reqCtx) agentDeleteLink(s *store.Store, link *pyjson.Object) error {
	id, _ := link.Value("id").(int64)
	out, err := s.DeleteLink(c.ctx, id, c.writer)
	if err != nil {
		return linkNotFound(err)
	}
	out.Set("source", link.Value("source")).Set("target", link.Value("target")).Set("relation", link.Value("relation"))
	c.ok(out)
	return nil
}

// agentLinkRelation is PATCH /api/agent/v1/links/{id} {relation}.
func (c *reqCtx) agentLinkRelation(s *store.Store, id int64, body *pyjson.Object) error {
	relation := ""
	if v := body.Value("relation"); pyjson.Truthy(v) {
		relation = pytext.Strip(pyjson.Str(v))
	}
	if relation == "" {
		return apiErr(400, "relation is required", "")
	}
	if !store.IsKnownRelation(relation) {
		return apiErr(400, fmt.Sprintf("unknown relation: %s; the relations are listed in GET /api/agent/v1/interface.",
			pyjson.Repr(relation)), "")
	}
	link, ok, err := s.Link(c.ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return apiErr(404, fmt.Sprintf("link not found: %d", id), "LINK_NOT_FOUND")
	}
	if pyjson.Str(link.Value("relation")) == relation {
		c.ok(link.Set("updated", false))
		return nil
	}
	_, _, forward, _, err := s.LinksBetween(c.ctx, link.Value("source"), link.Value("target"))
	if err != nil {
		return err
	}
	for _, l := range forward {
		if pyjson.Str(l.Value("relation")) == relation {
			return apiErr(409, fmt.Sprintf("%s and %s already have a %s link (id %s); delete one of the two instead.",
				pyjson.Str(link.Value("source")), pyjson.Str(link.Value("target")), relation, pyjson.Str(l.Value("id"))),
				"LINK_EXISTS")
		}
	}
	if _, err := s.UpdateLink(c.ctx, id, relation, c.writer); err != nil {
		return linkNotFound(err)
	}
	c.ok(link.Set("relation", relation).Set("updated", true))
	return nil
}
