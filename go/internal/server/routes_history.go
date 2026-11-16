package server

import (
	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pytext"
	"arkstudio/go/internal/store"
)

// Node text history routes: Handler._node_history_store / _get / _write.

var (
	reHistoryRaw      = pyRe(`/api/units/([^/]+)/history(?:/([^/]+))?`, true)
	reAgentHistoryRaw = pyRe(`/api/agent/v1/units/([^/]+)/history(?:/([^/]+))?`, true)
)

// nodeHistoryStore is Handler._node_history_store(level).
func (c *reqCtx) nodeHistoryStore(level string) (*store.Store, error) {
	projectID := pytext.Strip(c.header("X-ARK-Project"))
	if projectID == "" || c.token != nil {
		p, err := c.require(level)
		if err != nil {
			return nil, err
		}
		return p.Store, nil
	}
	p, err := c.projectByID(projectID)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, apiErr(404, "Project not found", "")
	}
	if c.app.Auth != nil {
		if err := c.checkProjectAccess(p, level); err != nil {
			return nil, err
		}
	}
	return p.Store, nil
}

// nodeHistoryGet is Handler._node_history_get.
func (c *reqCtx) nodeHistoryGet(ref, action string) error {
	s, err := c.nodeHistoryStore("read")
	if err != nil {
		return err
	}
	var out *pyjson.Object
	switch action {
	case "":
		out, err = s.NodeHistory(c.ctx, ref, c.qInt("limit", 50, 1, 200), c.qInt("offset", 0, 0, 1_000_000))
	case "diff":
		out, err = s.NodeDiff(c.ctx, ref, firstOrNil(c.query["from"]), firstOrNil(c.query["to"]))
	default:
		var rev *pyjson.Object
		if rev, err = s.NodeRevision(c.ctx, ref, action); err == nil {
			out = pyjson.NewObject("revision", rev)
		}
	}
	if err != nil {
		return err
	}
	c.ok(out)
	return nil
}

func firstOrNil(values []string) any {
	if len(values) == 0 {
		return nil
	}
	return values[0]
}

// nodeHistoryWrite is Handler._node_history_write.
func (c *reqCtx) nodeHistoryWrite(ref, action string, body *pyjson.Object, actor string) error {
	s, err := c.nodeHistoryStore("write")
	if err != nil {
		return err
	}
	var out *pyjson.Object
	switch action {
	case "":
		out, err = s.SetNodeHistory(c.ctx, ref, body.Value("enabled"), actor)
	case "restore":
		var unit *pyjson.Object
		if unit, err = s.RestoreNodeRevision(c.ctx, ref, body.Value("revision"), body.Value("expected_hash"), actor); err == nil {
			out = pyjson.NewObject("unit", unit)
		}
	case "apply":
		var unit *pyjson.Object
		if unit, err = s.ApplyNodeHistoryChange(c.ctx, ref, body.Value("from"), body.Value("to"), body.Value("file"),
			body.Value("change"), body.Value("expected_hash"), actor); err == nil {
			out = pyjson.NewObject("unit", unit)
		}
	case "checkpoint":
		out, err = s.SaveNodeVersion(c.ctx, ref, body.Value("expected_hash"), actor)
	default:
		return apiErr(404, "Unknown node history action", "")
	}
	if err != nil {
		return err
	}
	c.ok(out)
	return nil
}
