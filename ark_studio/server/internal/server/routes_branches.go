package server

// Lines of work (Go only, Q-0003; store/branches.go).  A line is a tag
// "branch:<name>" (the namespace is settings.branches.prefix):
//
//   GET /api/project/branches           every line with its size, what waits
//   GET /api/agent/v1/branches          in Needs you, last activity, sessions,
//                                       status and stored topic, plus the
//                                       lines of each Needs you item
//   GET /api/project/branches/suggest   line tags proposed for units without
//       ?depth=&relations=&min_units=   one (default: part_of, 6 steps, the
//       &stop_at_topics=0               climb ends at a topic); nothing is
//                                       written
//   POST /api/agent/v1/project/branches {"prefix": "line"}: the line tag
//                                       namespace (project admins; null or
//                                       "" go back to "branch")
//   POST /api/project/branches/rename   {from, to, dry_run, units}: move a
//                                       line and the lines below it (write
//                                       access; units limits it, for undo)
//   GET /api/project/branch-topic       a line as a topic page that is not
//       ?name=<name>[&full=1]           stored (the Topic payload)
//
// Read access.  Answers are kept per project revision (store/branches.go).

import (
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/settings"

	"arkstudio/go/internal/store"
)

const (
	branchesPath        = "/api/project/branches"
	branchSuggestPath   = "/api/project/branches/suggest"
	branchTopicPath     = "/api/project/branch-topic"
	agentBranchesPath   = "/api/agent/v1/branches"
	agentBranchPrefix   = "/api/agent/v1/project/branches"
	branchRenamePath    = "/api/project/branches/rename"
	branchMaxDepth      = 12
	branchMaxMinUnits   = 500
	branchSuggestLimit  = 300
	branchMaxSuggestion = 2000
)

// branchRoutesGoOnly are the Go-only ids of this file (GoOnlyRoutes).
var branchRoutesGoOnly = []PortedRoute{
	{"goonly:exact:" + branchesPath, "GET,HEAD"},
	{"goonly:exact:" + branchSuggestPath, "GET,HEAD"},
	{"goonly:exact:" + branchTopicPath, "GET,HEAD"},
	{"goonly:exact:" + agentBranchesPath, "GET,HEAD"},
	{"goonly:exact:" + agentBranchPrefix, "POST"},
	{"goonly:exact:" + branchRenamePath, "POST"},
}

// branchesGet is GET /api/project/branches.
func (c *reqCtx) branchesGet() error {
	p, err := c.require("read")
	if err != nil {
		return err
	}
	out, err := p.Store.Branches(c.ctx)
	if err != nil {
		return err
	}
	c.ok(out)
	return nil
}

// branchSuggestGet is GET /api/project/branches/suggest.
func (c *reqCtx) branchSuggestGet() error {
	p, err := c.require("read")
	if err != nil {
		return err
	}
	opt := store.BranchSuggestOptions{
		Depth:        int(c.qInt("depth", 6, 1, branchMaxDepth)),
		KeepClimbing: c.query.First("stop_at_topics", "") == "0",
		MinUnits:     int(c.qInt("min_units", 3, 1, branchMaxMinUnits)),
		Limit:        int(c.qInt("limit", branchSuggestLimit, 1, branchMaxSuggestion)),
	}
	for _, r := range qlist(c.query, "relations") {
		rel := strings.TrimSpace(r.(string))
		known := false
		for _, k := range store.Relations {
			if k == rel {
				known = true
			}
		}
		if !known {
			return apiErr(400, "unknown relation: "+rel, "")
		}
		opt.Relations = append(opt.Relations, rel)
	}
	out, err := p.Store.BranchSuggestions(c.ctx, opt)
	if err != nil {
		return err
	}
	c.ok(out)
	return nil
}

// branchTopicGet is GET /api/project/branch-topic.
func (c *reqCtx) branchTopicGet() error {
	p, err := c.require("read")
	if err != nil {
		return err
	}
	name := c.query.First("name", "")
	if strings.TrimSpace(name) == "" {
		return apiErr(400, "name is required", "")
	}
	out, err := p.Store.BranchTopic(c.ctx, name, c.qTrue("full"), c.qTrue("exact"))
	if err != nil {
		return err
	}
	c.ok(out)
	return nil
}

// agentBranchPrefixPost is POST /api/agent/v1/project/branches: a project
// admin's agent sets settings.branches (the line tag namespace).
func (c *reqCtx) agentBranchPrefixPost(body *pyjson.Object, actor string) error {
	p, err := c.require("admin")
	if err != nil {
		return err
	}
	var next *pyjson.Object
	encoded, err := p.Manifest.Mutate(func(m *pyjson.Object) error {
		s, err := settings.SettingsPatch(m, pyjson.NewObject("branches", body), settings.PresetIDs())
		if err != nil {
			if se, ok := err.(*settings.Error); ok {
				return apiErr(400, se.Message, "")
			}
			return err
		}
		next = s
		m.Set("settings", s).Set("updated", clock.UTCNow())
		return nil
	})
	if err != nil {
		return err
	}
	if err := c.writeManifest(p, encoded); err != nil {
		return err
	}
	prefix := p.Store.BranchPrefix()
	if err := p.Store.LogActivity(c.ctx, agentActor(actor), "project.settings",
		nil, pyjson.NewObject("sections", []any{"branches"}, "prefix", prefix)); err != nil {
		return err
	}
	c.ok(pyjson.NewObject("prefix", prefix, "branches", next.Value("branches")))
	return nil
}

// branchRenamePost is POST /api/project/branches/rename.
func (c *reqCtx) branchRenamePost(body *pyjson.Object) error {
	p, err := c.require("write")
	if err != nil {
		return err
	}
	var units []string
	if list, ok := body.Value("units").([]any); ok {
		for _, u := range list {
			if s, ok := u.(string); ok && s != "" {
				units = append(units, s)
			}
		}
	}
	out, err := p.Store.RenameLine(c.ctx, pyjson.Str(body.Value("from")), pyjson.Str(body.Value("to")), units,
		pyjson.Truthy(body.Value("dry_run")), c.writeActor(), c.who(p))
	if err != nil {
		return err
	}
	c.ok(out)
	return nil
}
