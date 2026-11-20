package server

// Importing from another ARK project (Go only; store/importark.go, T-0070).
//
//   GET  /api/import-ark/graph?project=<id>&<filters>   the source's graph with
//        the usual filter parameters (as GET /api/graph) plus its facets
//   GET  /api/import-ark/unit?project=<id>&uid=<uid>    one source unit (preview)
//   POST /api/import-ark/preview {project, units, with_children}
//   POST /api/import-ark/apply   {project, units, with_children, lang}
//
// The current project is the target (write access); the source needs read
// access and is never changed.

import (
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/store"
)

const importArkBase = "/api/import-ark/"

// importArkGoOnly are the Go-only ids of this file (GoOnlyRoutes).
var importArkGoOnly = []PortedRoute{{"goonly:prefix:" + importArkBase, "GET,HEAD,POST"}}

func isImportArkPath(path string) bool { return strings.HasPrefix(path, importArkBase) }

// importSource is the source project, read access checked.
func (c *reqCtx) importSource(id string, target *project.Project) (*project.Project, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, apiErr(400, "project is required", "IMPORT_SOURCE_REQUIRED")
	}
	if c.app.Auth != nil {
		if err := c.requireProjectPermission(id, "read"); err != nil {
			return nil, err
		}
	}
	src, err := c.projectByID(id)
	if err != nil {
		return nil, err
	}
	if src == nil {
		return nil, apiErr(404, "project not found or your account is not authorized for it", "PROJECT_NOT_ACCESSIBLE")
	}
	if target != nil && src.ID() == target.ID() {
		return nil, apiErr(400, "the source is the current project", "IMPORT_SAME_PROJECT")
	}
	return src, nil
}

func (c *reqCtx) importArkGet(path string) error {
	target, err := c.require("read")
	if err != nil {
		return err
	}
	src, err := c.importSource(c.query.First("project", ""), target)
	if err != nil {
		return err
	}
	switch strings.TrimPrefix(path, importArkBase) {
	case "graph":
		f := c.filtersFromQuery()
		graph, err := src.Store.Graph(c.ctx, f, c.qTrue("context"), c.qInt("limit", 700, 10, 1200))
		if err != nil {
			return err
		}
		f.Limit, f.Offset = 1, 0
		list, err := src.Store.ListUnits(c.ctx, f, true)
		if err != nil {
			return err
		}
		imported, err := target.Store.ImportedFrom(c.ctx, src.ID())
		if err != nil {
			return err
		}
		graph.Set("facets", list.Value("facets")).Set("total", list.Value("total")).
			Set("project", pyjson.NewObject("id", src.ID(), "name", src.Name())).
			Set("target", pyjson.NewObject("id", target.ID(), "name", target.Name())).Set("imported", imported)
		c.ok(graph)
		return nil
	case "unit":
		u, err := src.Store.GetUnit(c.ctx, c.query.First("uid", ""))
		if err != nil {
			return err
		}
		c.ok(pyjson.NewObject("unit", u, "project", pyjson.NewObject("id", src.ID(), "name", src.Name())))
		return nil
	}
	return apiErr(404, "Not found", "")
}

func (c *reqCtx) importArkPost(path string) error {
	dryRun := false
	switch strings.TrimPrefix(path, importArkBase) {
	case "preview":
		dryRun = true
	case "apply":
	default:
		return apiErr(404, "Not found", "")
	}
	target, err := c.require("write")
	if err != nil {
		return err
	}
	b := c.body
	id, _ := b.Value("project").(string)
	src, err := c.importSource(id, target)
	if err != nil {
		return err
	}
	req := store.ImportRequest{SourceID: src.ID(), SourceName: src.Name(), WithChildren: b.Value("with_children") == true}
	req.Lang, _ = b.Value("lang").(string)
	list, _ := b.Value("units").([]any)
	for _, u := range list {
		if s, ok := u.(string); ok && s != "" {
			req.Units = append(req.Units, s)
		}
	}
	if len(req.Units) == 0 {
		return apiErr(400, "units is required", "IMPORT_UNITS_REQUIRED")
	}
	if len(req.Units) > 2000 {
		return apiErr(400, "at most 2000 units per import", "IMPORT_TOO_MANY")
	}
	out, err := target.Store.ImportFrom(c.ctx, src.Store, req, dryRun, c.writer)
	if err != nil {
		return err
	}
	c.ok(out)
	return nil
}
