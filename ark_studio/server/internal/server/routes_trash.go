package server

// Recycle bin (Go only; store/trash.go).
//
//   GET    /api/trash         the deleted units (read access)
//   DELETE /api/trash         empty the bin: delete every unit in it for good
//   DELETE /api/trash/{ref}   delete one unit in the bin for good
//
// Deleting a node (DELETE /api/units/{id}, POST /api/units/{id}/archive) puts
// it in the bin; POST /api/units/{id}/restore takes it out again.  Permanent
// deletes need write access, like the delete itself.

const trashPath = "/api/trash"

var reTrashUnit = pyRe(`^/api/trash/([^/]+)$`, false)

// trashRoutesGoOnly are the Go-only ids of this file (GoOnlyRoutes).
var trashRoutesGoOnly = []PortedRoute{
	{"goonly:exact:" + trashPath, "DELETE,GET,HEAD"},
	{`goonly:regex:^/api/trash/([^/]+)$`, "DELETE"},
}

// trashGet is GET /api/trash.
func (c *reqCtx) trashGet() error {
	p, err := c.require("read")
	if err != nil {
		return err
	}
	out, err := p.Store.Trash(c.ctx)
	if err != nil {
		return err
	}
	c.ok(out)
	return nil
}

// trashEmpty is DELETE /api/trash.
func (c *reqCtx) trashEmpty() error {
	p, err := c.require("write")
	if err != nil {
		return err
	}
	out, err := p.Store.EmptyTrash(c.ctx, c.writer)
	if err != nil {
		return err
	}
	c.ok(out)
	return nil
}

// trashPurge is DELETE /api/trash/{ref}.
func (c *reqCtx) trashPurge(m []string) error {
	p, err := c.require("write")
	if err != nil {
		return err
	}
	out, err := p.Store.PurgeUnit(c.ctx, m[1], c.writer)
	if err != nil {
		return err
	}
	c.ok(out)
	return nil
}
