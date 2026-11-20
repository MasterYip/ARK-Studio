package server

// Agent presence (Go only, T-0043): which agent is on which node, live.
// internal/presence holds the entries (memory only); this file feeds it and
// serves it.
//
//   implicit   every activity row an agent's write logs (create, update,
//              status, comment, media, link, history) and every single-unit
//              read through the agent API (GET /api/agent/v1/units/{id},
//              GET /api/task-agent/v1/units/{id}) moves the agent's cursor
//              there (30 s)
//   POST /api/agent/v1/presence {"unit", "state": "working"|"done", "note"?}
//              an agent announces what it works on (5 min unless refreshed)
//   GET  /api/presence
//              the project's live entries (project members, read access)
//   GET  /api/events?presence=1
//              the event stream adds "event: presence" frames with the same
//              payload, at most 4 per second per project; without the
//              parameter the stream is unchanged
//
// Only agents are tracked: agent tokens, personal agent tokens (with or
// without X-Agent-Name) and task agents.  People's own edits are not.

import (
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/presence"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/sessions"
	"arkstudio/go/internal/store"
)

const (
	presencePath         = "/api/presence"
	presenceAnnouncePath = "/api/agent/v1/presence"
)

// presenceRoutesGoOnly are the Go-only ids of this file (GoOnlyRoutes).
var presenceRoutesGoOnly = []PortedRoute{
	{"goonly:exact:" + presencePath, "GET,HEAD"},
	{"goonly:exact:" + presenceAnnouncePath, "POST"},
}

// agentLabel is the display hierarchy of an "agent:..." actor:
// "agent:MasterYip/视频字体" -> "MasterYip/视频字体", "agent:codex" -> "codex".
func agentLabel(actor string) string {
	name := strings.TrimPrefix(actor, "agent:")
	if name == "" || name == "api" {
		return "agent"
	}
	return name
}

// presenceColorKey keeps sub-agents visually grouped under their parent while
// preserving the full actor as the identity used for entries and aggregation.
func presenceColorKey(actor string) string {
	if i := strings.IndexByte(actor, '/'); i > 0 {
		return actor[:i]
	}
	return actor
}

// taskPresenceActor is a task agent's identity and label.
func taskPresenceActor(s *sessions.Session) (string, string) {
	label := strOr(s.Meta.Value("agent"))
	if label == "" {
		label = "task"
	}
	return "task:" + s.ID, label
}

// presenceActor is the request's agent identity and label ("" for people).
func (c *reqCtx) presenceActor() (string, string) {
	if c.task != nil {
		return taskPresenceActor(c.task)
	}
	actor := c.writeActor()
	if !strings.HasPrefix(actor, "agent:") {
		return "", ""
	}
	return actor, agentLabel(actor)
}

// presenceAction is the short action of an activity row.
func presenceAction(ev store.ActivityEvent) string {
	switch {
	case ev.Status != "":
		return "status"
	case strings.HasPrefix(ev.Action, "comment."):
		return "comment"
	case strings.HasPrefix(ev.Action, "media."):
		return "media"
	case strings.HasPrefix(ev.Action, "link."):
		return "link"
	}
	if i := strings.IndexByte(ev.Action, '.'); i >= 0 {
		return ev.Action[i+1:]
	}
	return ev.Action
}

// observeActivity is the request's store activity observer: every write
// names the project's latest writer, and an agent's write moves its
// presence cursor to the unit.
func (c *reqCtx) observeActivity(ev store.ActivityEvent) {
	p := c.routeProject
	if p == nil {
		p = c.project
	}
	if p == nil {
		return
	}
	// Go only: the project picker's "updated by" (project_people.go).
	c.app.noteWriter(p.ID(), c.actor(), ev.Actor)
	if c.app.Presence == nil || ev.Unit == 0 || !strings.HasPrefix(ev.Actor, "agent:") {
		return
	}
	c.app.Presence.Record(p.ID(), presence.Touch{Actor: ev.Actor, Label: agentLabel(ev.Actor), ColorKey: presenceColorKey(ev.Actor), Unit: ev.Unit,
		UID: ev.UID, Title: ev.Title, Action: presenceAction(ev)})
}

// presenceRead notes an agent's read of one unit.
func (c *reqCtx) presenceRead(p *project.Project, unit *pyjson.Object, task *sessions.Session) {
	if c.app.Presence == nil || unit == nil || c.r.Method != "GET" {
		return
	}
	actor, label := c.presenceActor()
	if task != nil {
		actor, label = taskPresenceActor(task)
	}
	if actor == "" {
		return
	}
	id, _ := unit.Value("id").(int64)
	uid, _ := unit.Value("uid").(string)
	title, _ := unit.Value("title").(string)
	c.app.Presence.Record(p.ID(), presence.Touch{Actor: actor, Label: label, ColorKey: presenceColorKey(actor), Unit: id, UID: uid, Title: title, Action: "read"})
}

// presencePayload is the project's presence as GET /api/presence and the
// presence event send it.
func (c *reqCtx) presencePayload(projectID string) *pyjson.Object {
	entries, _ := c.app.Presence.Snapshot(projectID)
	items := make([]any, 0, len(entries))
	for _, e := range entries {
		item := pyjson.NewObject("actor", e.Actor, "label", e.Label, "color_actor", e.ColorKey, "color_key", e.ColorKey, "kind", e.Kind, "state", e.State,
			"unit", e.Unit, "uid", e.UID, "title", e.Title, "action", e.Action, "note", e.Note,
			"at", clock.Format(e.At), "expires_at", clock.Format(e.Expires))
		items = append(items, item)
	}
	return pyjson.NewObject("project", projectID, "entries", items, "now", clock.Format(c.app.Presence.Now()),
		"ttl", pyjson.NewObject("cursor", int64(c.app.Presence.CursorTTL.Seconds()),
			"working", int64(c.app.Presence.WorkingTTL.Seconds())))
}

// presenceGet is GET /api/presence.
func (c *reqCtx) presenceGet() error {
	p, err := c.require("read")
	if err != nil {
		return err
	}
	c.ok(c.presencePayload(p.ID()))
	return nil
}

// presenceAnnounce is POST /api/agent/v1/presence.
func (c *reqCtx) presenceAnnounce(body *pyjson.Object) error {
	if err := c.requireAgent(); err != nil {
		return err
	}
	p, err := c.require("read")
	if err != nil {
		return err
	}
	state := pytext.Strip(strOrEmptyAny(body.Value("state")))
	if state == "" {
		state = presence.StateWorking
	}
	if state != presence.StateWorking && state != "done" {
		return apiErr(400, `state must be "working" or "done"`, "PRESENCE_STATE")
	}
	ref := pytext.Strip(strOrEmptyAny(body.Value("unit")))
	if n, ok := body.Value("unit").(int64); ok {
		ref = pyjson.Str(n)
	}
	if ref == "" && state == presence.StateWorking {
		return apiErr(400, "unit is required", "PRESENCE_UNIT")
	}
	actor, label := c.presenceActor()
	if actor == "" {
		actor, label = "agent:api", "agent"
	}
	t := presence.Touch{Actor: actor, Label: label, ColorKey: presenceColorKey(actor), Note: pytext.Strip(strOrEmptyAny(body.Value("note")))}
	if ref != "" {
		id, uid, title, err := p.Store.UnitBrief(c.ctx, ref)
		if err != nil {
			if _, missing := err.(*store.Error); missing {
				return apiErr(404, "unit not found: "+ref, "UNIT_NOT_FOUND")
			}
			return err
		}
		t.Unit, t.UID, t.Title = id, uid, title
	}
	c.app.Presence.Announce(p.ID(), t, state)
	c.ok(c.presencePayload(p.ID()))
	return nil
}
