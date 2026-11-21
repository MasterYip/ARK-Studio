package server

// Go only (T-0044): what people did that an agent should act on, and
// (experimental) messages from a person to their agents.
//
//   GET  /api/agent/v1/updates?since=&wait=&kinds=&mine=1&limit=
//        the update feed (internal/agentwatch), optionally long-polled
//   GET  /api/agent/v1/messages[?status=]     messages for this agent
//   GET  /api/agent/v1/messages/{id}
//   POST /api/agent/v1/messages/{id}/ack      mark read
//   POST /api/agent/v1/messages/{id}/reply    {"body": "..."}
//   GET  /api/agent-messages[?unit=]          the signed-in person's messages
//   POST /api/agent-messages                  {"body", "unit"?, "target"?}
//
// See docs/agents/working-mode.md ("Watch for replies") and
// docs/development/agent-messaging.md.

import (
	"arkstudio/go/internal/agentrules"
	"strconv"
	"strings"
	"sync"
	"time"

	"arkstudio/go/internal/agentwatch"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/secretredact"
	"arkstudio/go/internal/store"
)

const (
	agentUpdatesPath   = "/api/agent/v1/updates"
	agentWatcherSh     = "/api/agent/v1/watcher.sh"
	agentWatcherPs1    = "/api/agent/v1/watcher.ps1"
	agentMessagesPath  = "/api/agent/v1/messages"
	humanMessagesPath  = "/api/agent-messages"
	updatesMaxWait     = 60 * time.Second
	updatesRecheck     = 5 * time.Second        // re-read for writes by other processes
	updatesSettle      = 150 * time.Millisecond // let a burst (comment + status) commit
	updatesWaitsPerKey = 4                      // concurrent long polls per token
	messagesPerWindow  = 20                     // per sender and project
	messagesWindow     = 10 * time.Minute
)

var (
	reAgentMessage    = pyRe(`^/api/agent/v1/messages/(\d+)$`, false)
	reAgentMessageAct = pyRe(`^/api/agent/v1/messages/(\d+)/(ack|reply)$`, false)
)

// agentWatchGoOnly are the Go-only ids of this file (GoOnlyRoutes).
var agentWatchGoOnly = []PortedRoute{
	{"goonly:exact:" + agentUpdatesPath, "GET,HEAD"},
	{"goonly:exact:" + agentWatcherSh, "GET,HEAD"},
	{"goonly:exact:" + agentWatcherPs1, "GET,HEAD"},
	{"goonly:exact:" + agentMessagesPath, "GET,HEAD"},
	{`goonly:regex:^/api/agent/v1/messages/(\d+)$`, "GET,HEAD"},
	{`goonly:regex:^/api/agent/v1/messages/(\d+)/(ack|reply)$`, "POST"},
	{"goonly:exact:" + humanMessagesPath, "GET,HEAD,POST"},
}

// ------------------------------------------------------------ wait slots

type waitSlots struct {
	mu sync.Mutex
	n  map[string]int
}

var updateWaits sync.Map // *App -> *waitSlots

func (c *reqCtx) waitKey() string {
	if c.token != nil {
		return "token:" + pyjson.Str(c.token.row.Get("id"))
	}
	return "client:" + c.clientIP()
}

// acquireWait takes one long-poll slot of this token; release gives it back.
func (c *reqCtx) acquireWait() (release func(), ok bool) {
	v, _ := updateWaits.LoadOrStore(c.app, &waitSlots{n: map[string]int{}})
	slots := v.(*waitSlots)
	key := c.waitKey()
	slots.mu.Lock()
	defer slots.mu.Unlock()
	if slots.n[key] >= updatesWaitsPerKey {
		return nil, false
	}
	slots.n[key]++
	return func() {
		slots.mu.Lock()
		if slots.n[key]--; slots.n[key] <= 0 {
			delete(slots.n, key)
		}
		slots.mu.Unlock()
	}, true
}

// tokenLongPolling reports whether a request with this personal token is
// waiting on GET /api/agent/v1/updates now (a watcher is running).
func (a *App) tokenLongPolling(id string) bool {
	v, ok := updateWaits.Load(a)
	if !ok || id == "" {
		return false
	}
	slots := v.(*waitSlots)
	slots.mu.Lock()
	defer slots.mu.Unlock()
	return slots.n["token:"+id] > 0
}

// ------------------------------------------------------------ viewer

func (c *reqCtx) watchViewer() agentwatch.Viewer {
	v := agentwatch.Viewer{User: c.actor(), AgentActor: agentActor(c.writeActor()),
		AgentName: pytext.Prefix(pytext.Strip(c.header("X-Agent-Name")), 40)}
	if c.token != nil {
		v.TokenID = pyjson.Str(c.token.row.Get("id"))
		v.TokenLabel = strOrEmptyAny(c.token.row.Get("label"))
	} else {
		v.AnyAgent = true // the legacy server token is shared by every local agent
	}
	return v
}

// ------------------------------------------------------------ agent GET

// agentWatchGet answers the Go-only agent GET routes of this file (agent
// auth and read access were checked by agentGet).
func (c *reqCtx) agentWatchGet(path string, p *project.Project) (bool, error) {
	if c.serveWatcherScript() {
		return true, nil
	}
	switch {
	case path == agentUpdatesPath:
		return true, c.agentUpdates(p)
	case path == agentMessagesPath:
		v := c.watchViewer()
		list, err := p.Store.Messages(c.ctx, store.MessageFilter{Owner: v.User, Limit: int(c.qInt("limit", 50, 1, 200))})
		if err != nil {
			return true, err
		}
		status := c.query.First("status", "")
		out := []any{}
		var queued []int64
		for _, m := range list {
			o := m.(*pyjson.Object)
			if !v.MessageFor(o) || (status != "" && o.Value("status") != status) {
				continue
			}
			if o.Value("status") == "queued" {
				id, _ := o.Value("id").(int64)
				queued = append(queued, id)
			}
			out = append(out, o)
		}
		if err := p.Store.MarkMessagesDelivered(c.ctx, queued, v.AgentActor); err != nil {
			return true, err
		}
		c.ok(pyjson.NewObject("messages", out, "total", int64(len(out))))
		return true, nil
	}
	if m := reAgentMessage.FindStringSubmatch(path); m != nil {
		msg, err := c.agentMessage(p, m[1])
		if err == nil {
			c.ok(pyjson.NewObject("message", msg))
		}
		return true, err
	}
	return false, nil
}

// serveWatcherScript answers GET/HEAD of the watcher scripts (T-0053): no
// ark-server needed on the agent's machine.  They hold no secrets and are
// served without a token, so the command that fetches them never carries
// one; the scripts read the token from a file or the environment.
func (c *reqCtx) serveWatcherScript() bool {
	if c.r.Method != "GET" && c.r.Method != "HEAD" {
		return false
	}
	switch c.path {
	case agentWatcherSh:
		c.send(200, agentwatch.WatcherSh, "text/x-sh; charset=utf-8", headerList{
			{"Content-Disposition", `attachment; filename="ark-watch.sh"`}, {"Cache-Control", "no-store"}}, true)
	case agentWatcherPs1:
		c.send(200, agentwatch.WatcherPs1, "text/plain; charset=utf-8", headerList{
			{"Content-Disposition", `attachment; filename="ark-watch.ps1"`}, {"Cache-Control", "no-store"}}, true)
	default:
		return false
	}
	return true
}

// agentMessage reads one message meant for this agent (404 otherwise).
func (c *reqCtx) agentMessage(p *project.Project, ref string) (*pyjson.Object, error) {
	id, err := strconv.ParseInt(ref, 10, 64)
	if err != nil {
		return nil, apiErr(404, "message not found: "+ref, "MESSAGE_NOT_FOUND")
	}
	msg, ok, err := p.Store.Message(c.ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok || !c.watchViewer().MessageFor(msg) {
		return nil, apiErr(404, "message not found: "+ref, "MESSAGE_NOT_FOUND")
	}
	return msg, nil
}

// agentMessagesPost answers POST /api/agent/v1/messages/{id}/(ack|reply).
func (c *reqCtx) agentMessagesPost(path string, body *pyjson.Object, actor string) (bool, error) {
	m := reAgentMessageAct.FindStringSubmatch(path)
	if m == nil {
		return false, nil
	}
	p, err := c.require("write")
	if err != nil {
		return true, err
	}
	msg, err := c.agentMessage(p, m[1])
	if err != nil {
		return true, err
	}
	id, _ := msg.Value("id").(int64)
	by := agentActor(actor)
	if m[2] == "ack" {
		msg, err = p.Store.AckMessage(c.ctx, id, by)
	} else {
		text := pyjson.Str(orDefault(body.Value("body"), ""))
		if _, n := secretredact.Text(text, nil); n > 0 {
			return true, apiErr(400, "The reply looks like it contains a secret; send a reference (env:NAME) instead.", "MESSAGE_SECRET")
		}
		msg, err = p.Store.ReplyMessage(c.ctx, id, text, by)
	}
	if err == nil {
		c.ok(pyjson.NewObject("message", msg))
	}
	return true, err
}

// ------------------------------------------------------------ updates

func (c *reqCtx) agentUpdates(p *project.Project) error {
	kinds, err := agentwatch.ParseKinds(c.query.First("kinds", ""))
	if err != nil {
		return apiErr(400, err.Error(), "BAD_KINDS")
	}
	wait := time.Duration(0)
	if raw := pytext.Strip(c.query.First("wait", "")); raw != "" {
		secs, perr := strconv.ParseFloat(raw, 64)
		if perr != nil || secs < 0 || secs != secs {
			return apiErr(400, "wait must be a number of seconds (0–60)", "BAD_WAIT")
		}
		wait = min(time.Duration(secs*float64(time.Second)), updatesMaxWait)
	}
	limit := int(c.qInt("limit", 100, 1, 500))
	v := c.watchViewer()
	v.Kinds = kinds
	v.Mine = c.qTrue("mine")
	st := p.Store
	epoch, top, err := st.ActivityHead(c.ctx)
	if err != nil {
		return err
	}
	// The rules version (GET /api/agent/v1/rules): a watcher wakes the agent
	// when a person changed the rules in Settings, as for any reply.
	rulesVersion := ""
	if cat, err := agentrules.Default(); err == nil {
		if proj, pers, err := c.agentRulesSettings(p); err == nil {
			rulesVersion = cat.Compose(proj, pers).Version
		}
	}
	reply := func(cursor string, items []*pyjson.Object, more bool, reset string, waited time.Duration) {
		list := make([]any, len(items))
		for i, it := range items {
			list[i] = it
		}
		out := pyjson.NewObject("cursor", cursor, "items", list, "count", int64(len(items)), "more", more,
			"reset", reset != "", "mine", v.Mine, "kinds", agentwatch.SortedKinds(kinds),
			"waited_ms", waited.Milliseconds(), "rules_version", rulesVersion)
		if reset != "" {
			out.Set("reason", reset)
		}
		c.ok(out)
	}
	since := pytext.Strip(c.query.First("since", ""))
	if since == "" {
		// The first call starts the feed at "now".
		reply(agentwatch.Cursor(epoch, top), nil, false, "", 0)
		return nil
	}
	gotEpoch, after, ok := agentwatch.ParseCursor(since)
	if !ok || gotEpoch != epoch || after > top {
		reason := "cursor"
		if ok && gotEpoch != epoch {
			reason = "restored" // a snapshot restore renewed the project's history
		}
		reply(agentwatch.Cursor(epoch, top), nil, false, reason, 0)
		return nil
	}
	started := time.Now()
	if wait > 0 {
		release, ok := c.acquireWait()
		if !ok {
			return apiErr(429, "This token already has "+strconv.Itoa(updatesWaitsPerKey)+
				" waiting update requests; wait for one to return.", "AGENT_WAIT_LIMIT")
		}
		defer release()
		// A long poll leaves the admission pool and the request deadline;
		// the client, shutdown and wait bound it instead.
		c.detachFromRequestLimits()
		c.ctx = c.r.Context()
	}
	deadline := started.Add(wait)
	for {
		signal := st.WriteSignal()
		batch, err := st.UpdatesSince(c.ctx, after, limit)
		if err != nil {
			if c.ctx.Err() != nil {
				return nil
			}
			return err
		}
		if batch.Epoch != epoch {
			reply(agentwatch.Cursor(batch.Epoch, batch.Max), nil, false, "restored", time.Since(started))
			return nil
		}
		messages, err := c.batchMessages(p, batch.Rows, v)
		if err != nil {
			return err
		}
		items := agentwatch.Items(batch.Rows, v, messages)
		after = batch.Top
		if len(items) > 0 || (batch.More && time.Now().After(deadline)) {
			if err := c.markDelivered(p, items, v); err != nil {
				return err
			}
			reply(agentwatch.Cursor(epoch, after), items, batch.More, "", time.Since(started))
			return nil
		}
		if batch.More {
			continue // only rows for nobody here: read on
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			reply(agentwatch.Cursor(epoch, after), nil, false, "", time.Since(started))
			return nil
		}
		select {
		case <-signal:
			select { // let the rest of a burst commit (Accept writes a comment, then the status)
			case <-time.After(updatesSettle):
			case <-c.ctx.Done():
				return nil
			}
		case <-time.After(min(remaining, updatesRecheck)):
		case <-c.ctx.Done():
			return nil
		case <-c.app.Streams.Done():
			reply(agentwatch.Cursor(epoch, after), nil, false, "", time.Since(started))
			return nil
		}
	}
}

// batchMessages loads the messages that rows announce (only the viewer's).
func (c *reqCtx) batchMessages(p *project.Project, rows []store.UpdateRow, v agentwatch.Viewer) (map[int64]*pyjson.Object, error) {
	ids := agentwatch.MessageIDs(rows)
	if len(ids) == 0 || v.User == "" {
		return nil, nil
	}
	list, err := p.Store.Messages(c.ctx, store.MessageFilter{Owner: v.User, IDs: ids, Limit: 200})
	if err != nil {
		return nil, err
	}
	out := map[int64]*pyjson.Object{}
	for _, m := range list {
		o := m.(*pyjson.Object)
		id, _ := o.Value("id").(int64)
		out[id] = o
	}
	return out, nil
}

// markDelivered records that the message items reached this agent.
func (c *reqCtx) markDelivered(p *project.Project, items []*pyjson.Object, v agentwatch.Viewer) error {
	var ids []int64
	for _, it := range items {
		if m, ok := it.Value("message").(*pyjson.Object); ok && m.Value("status") == "queued" {
			id, _ := m.Value("id").(int64)
			ids = append(ids, id)
			m.Set("status", "delivered")
		}
	}
	return p.Store.MarkMessagesDelivered(c.ctx, ids, v.AgentActor)
}

// ------------------------------------------------------------ people

// humanMessages answers GET/POST /api/agent-messages (signed-in people only).
func (c *reqCtx) humanMessages() error {
	if c.agentToken() != "" || c.token != nil {
		return apiErr(403, "Messages to agents come from people signed in to ARK.", "MESSAGE_HUMAN_ONLY")
	}
	if c.mobileSession != nil && c.mobileSession.Permission == "read_only" {
		return apiErr(403, "A read-only device cannot message agents.", "MOBILE_SCOPE")
	}
	user := c.actor()
	if user == "" {
		return apiErr(401, "Sign in with an ARK account to continue.", "AUTH_REQUIRED")
	}
	if c.r.Method == "GET" || c.r.Method == "HEAD" {
		p, err := c.require("read")
		if err != nil {
			return err
		}
		list, err := p.Store.Messages(c.ctx, store.MessageFilter{Owner: user, Sender: user,
			Unit: pytext.Strip(c.query.First("unit", "")), Limit: int(c.qInt("limit", 50, 1, 200))})
		if err != nil {
			return err
		}
		c.ok(pyjson.NewObject("messages", list, "total", int64(len(list))))
		return nil
	}
	p, err := c.require("write")
	if err != nil {
		return err
	}
	body := c.body
	text := pyjson.Str(orDefault(body.Value("body"), ""))
	if _, n := secretredact.Text(text, nil); n > 0 {
		return apiErr(400, "The message looks like it contains a secret (a token, key or password). "+
			"Send a reference such as env:NAME instead.", "MESSAGE_SECRET")
	}
	target := pytext.Strip(pyjson.Str(orDefault(body.Value("target"), "mine")))
	if target == "" {
		target = "mine"
	}
	if target != "mine" && !strings.HasPrefix(target, "token:") && !strings.HasPrefix(target, "label:") {
		return apiErr(400, "target must be mine, token:<id> or label:<label>", "BAD_TARGET")
	}
	if pytext.Len(target) > 120 {
		return apiErr(400, "target is too long", "BAD_TARGET")
	}
	since := clock.Format(clock.Now().Add(-messagesWindow))
	sent, err := p.Store.MessagesSentSince(c.ctx, user, since)
	if err != nil {
		return err
	}
	if sent >= messagesPerWindow {
		return apiErr(429, "Too many messages to agents in the last 10 minutes; try again later.", "MESSAGE_RATE_LIMIT")
	}
	unit := ""
	if u := body.Value("unit"); pyjson.Truthy(u) {
		unit = pytext.Strip(pyjson.Str(u))
	}
	msg, err := p.Store.CreateMessage(c.ctx, store.MessageInput{Sender: user, Owner: user, Target: target,
		Unit: unit, Body: text, Actor: "human:" + user})
	if err != nil {
		return err
	}
	c.ok(pyjson.NewObject("message", msg))
	return nil
}
