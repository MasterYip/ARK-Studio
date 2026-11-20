package server

// Multi-selection routes (Go only; store/batch.go).
//
//   GET  /api/units/batch?units=A,B,...    the selected units (no body) with
//                                          their topics, for the shared-attribute
//                                          inspector (read access)
//   POST /api/units/batch                  one edit for many units in one
//                                          transaction: {units, patch, tags_add,
//                                          tags_remove} or {updates: [{unit,
//                                          patch, tags_add, tags_remove}]};
//                                          optional batch id
//   POST /api/units/group                  {units, parent, relation, title, type,
//                                          tags}: a new parent for siblings
//   POST /api/units/{ref}/ungroup          {relation}: the inverse of group
//
// Writes need write access (guardProjectScope); every activity entry of one
// call carries the same "batch" id.

import (
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/store"
)

const (
	unitsBatchPath = "/api/units/batch"
	unitsGroupPath = "/api/units/group"
)

var reUngroup = pyRe(`^/api/units/([^/]+)/ungroup$`, false)

// batchRoutesGoOnly are the Go-only ids of this file (GoOnlyRoutes).
var batchRoutesGoOnly = []PortedRoute{
	{"goonly:exact:" + unitsBatchPath, "GET,HEAD,POST"},
	{"goonly:exact:" + unitsGroupPath, "POST"},
	{`goonly:regex:^/api/units/([^/]+)/ungroup$`, "POST"},
}

func isBatchPostPath(path string) bool {
	return path == unitsBatchPath || path == unitsGroupPath || reUngroup.MatchString(path)
}

// unitsBatchGet is GET /api/units/batch.
func (c *reqCtx) unitsBatchGet() error {
	p, err := c.require("read")
	if err != nil {
		return err
	}
	out, err := p.Store.UnitsBatch(c.ctx, qlist(c.query, "units", "ids"))
	if err != nil {
		return err
	}
	c.ok(out)
	return nil
}

func stringList(v any) []string {
	list, _ := v.([]any)
	var out []string
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func objectOrNil(v any) (*pyjson.Object, error) {
	if v == nil {
		return nil, nil
	}
	o, ok := v.(*pyjson.Object)
	if !ok {
		return nil, &store.Error{Message: "patch must be an object"}
	}
	return o, nil
}

// batchItems reads the two body shapes of POST /api/units/batch.
func batchItems(body *pyjson.Object) ([]store.BatchItem, error) {
	if raw, ok := body.Get("updates"); ok {
		list, isList := raw.([]any)
		if !isList {
			return nil, &store.Error{Message: "updates must be a list"}
		}
		items := make([]store.BatchItem, 0, len(list))
		for _, entry := range list {
			o, isObj := entry.(*pyjson.Object)
			if !isObj || !pyjson.Truthy(o.Value("unit")) {
				return nil, &store.Error{Message: "each update needs a unit"}
			}
			patch, err := objectOrNil(o.Value("patch"))
			if err != nil {
				return nil, err
			}
			items = append(items, store.BatchItem{Ref: o.Value("unit"), Patch: patch,
				TagsAdd: stringList(o.Value("tags_add")), TagsRemove: stringList(o.Value("tags_remove"))})
		}
		return items, nil
	}
	refs, isList := body.Value("units").([]any)
	if !isList {
		return nil, &store.Error{Message: "units must be a list"}
	}
	patch, err := objectOrNil(body.Value("patch"))
	if err != nil {
		return nil, err
	}
	add, remove := stringList(body.Value("tags_add")), stringList(body.Value("tags_remove"))
	items := make([]store.BatchItem, 0, len(refs))
	for _, ref := range refs {
		items = append(items, store.BatchItem{Ref: ref, Patch: patch, TagsAdd: add, TagsRemove: remove})
	}
	return items, nil
}

// batchPost answers the POST routes above.
func (c *reqCtx) batchPost(path string) error {
	p, err := c.require("write")
	if err != nil {
		return err
	}
	s := p.Store
	body := c.body
	if body == nil {
		body = pyjson.NewObject()
	}
	batch := store.CleanBatchID(body.Value("batch"))
	var out *pyjson.Object
	switch {
	case path == unitsBatchPath:
		items, ierr := batchItems(body)
		if ierr != nil {
			return ierr
		}
		out, err = s.BatchUpdate(c.ctx, items, batch, c.writer, c.who(p))
	case path == unitsGroupPath:
		refs, isList := body.Value("units").([]any)
		if !isList {
			return &store.Error{Message: "units must be a list"}
		}
		data := pyjson.NewObject("title", body.Value("title"), "type", orDefault(body.Value("type"), "note"),
			"status", orDefault(body.Value("status"), "open"), "importance", orDefault(body.Value("importance"), "p2"),
			"tags", orDefault(body.Value("tags"), []any{}), "origin", "mind")
		out, err = s.GroupUnder(c.ctx, refs, body.Value("parent"), body.Value("relation"), data, batch, c.writer, c.who(p))
	default:
		m := reUngroup.FindStringSubmatch(path)
		out, err = s.Ungroup(c.ctx, m[1], body.Value("relation"), batch, c.writer)
	}
	if err != nil {
		return err
	}
	c.ok(out)
	return nil
}
