package server

// GET /api/project/support (Go only, 2026-11-24): what supports the work
// of one project, live, for the Board's "Workspace support" panel and the
// Home "Agents and hosts" card (web/js/support-model.js).  One read instead
// of a call per item:
//
//   agents     the viewer's visible ARK sessions of the project (active ones
//              and those of the last days, top level, sub-sessions counted)
//              and the agents that wrote through the agent API in the last
//              24 hours, with the unit of their last write and live
//              presence.  An agent whose writes came with its session's
//              own token or an X-ARK-Session header (agent_session.go) is
//              listed on that session ("linked_agents"), not again as an
//              external agent; an unlinked one that probably is a listed
//              session gets "likely_session".
//   hosts      the devices of the Resources graph (ARK server, setup
//              devices, GPU servers, compute) plus the viewer's own setup
//              devices: online / last seen, running sessions, GPU jobs, and
//              free GPUs from the last probe when there is one.  Never
//              probes, never opens SSH, never starts the queue worker.
//   library    the setup resources by kind (repositories, workspaces,
//              storage, databases, ...) and their measured size
//   deadlines  the viewer's agent tokens expiring within 14 days, running
//              GPU jobs' time limits, review rounds open for 3 days or more
//              (units with due dates come from the units the page has)
//
// Read access; "can_write" tells the client whether to offer actions.
// Answers are kept 10 s per project, viewer and permission.

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/gpu"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/setup"
)

const supportPath = "/api/project/support"

// supportRoutesGoOnly are the Go-only ids of this file (GoOnlyRoutes).
var supportRoutesGoOnly = []PortedRoute{
	{"goonly:exact:" + supportPath, "GET,HEAD"},
}

const (
	supportAgentHours   = 24               // external agents: writes in this window
	supportSessionDays  = 3                // ended sessions stay listed this long
	supportSessionLimit = 12               // sessions listed
	supportAgentLimit   = 8                // external agents listed
	supportTokenDays    = 14               // tokens expiring within
	supportReviewDays   = 3                // review rounds open at least
	supportTTL          = 10 * time.Second // answers kept per project and viewer
	supportOnlineWindow = 15 * time.Minute // a device seen this recently is online
)

var supportActive = map[string]bool{"starting": true, "running": true, "idle": true, "stopping": true, "conflict": true}

type supportEntry struct {
	at   time.Time
	data *pyjson.Object
}

var supportCache sync.Map // "<app>|<project>|<viewer>|<perm>" -> supportEntry

// projectSupportGet is GET /api/project/support.
func (c *reqCtx) projectSupportGet() error {
	p, err := c.require("read")
	if err != nil {
		return err
	}
	perm, err := c.projectPermissionFor(p)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("%p|%s|%s|%s", c.app, p.ID(), c.actor(), perm)
	if v, ok := supportCache.Load(key); ok {
		if e := v.(supportEntry); time.Since(e.at) < supportTTL {
			c.ok(e.data)
			return nil
		}
	}
	data, err := c.projectSupport(p, perm)
	if err != nil {
		return err
	}
	supportCache.Store(key, supportEntry{at: time.Now(), data: data})
	c.ok(data)
	return nil
}

func (c *reqCtx) projectSupport(p *project.Project, perm string) (*pyjson.Object, error) {
	now := clock.Now().UTC()
	agents, sessionCounts, err := c.supportAgents(p, now)
	if err != nil {
		return nil, err
	}
	graph, err := c.resourcesHandlers().GraphFor(c, p)
	if err != nil {
		return nil, err
	}
	gpuScope, err := c.gpuScope(p)
	if err != nil {
		return nil, err
	}
	gpuSvc := c.app.gpu()
	var jobs []*pyjson.Object
	if q := gpuSvc.QueueIfStarted(); q != nil {
		if list, err := q.List(gpu.ListFilters{Project: p.ID(), Status: "queued,pending,starting,running", Limit: int64(200)}); err == nil {
			jobs = objectList(list.Value("jobs"))
		}
	}
	hosts := c.supportHosts(p, graph, sessionCounts, jobs, now)
	deadlines, err := c.supportDeadlines(p, jobs, now)
	if err != nil {
		return nil, err
	}
	rank := org.PermissionRank[perm]
	return pyjson.NewObject(
		"project", p.ID(),
		"generated_at", clock.Format(now),
		"can_write", rank >= org.PermissionRank["write"],
		"agents", agents,
		"hosts", hosts,
		"library", supportLibrary(graph),
		"deadlines", deadlines,
		"gpu", gpuSvc.OverviewFor(gpuScope, p.ID()),
		"windows", pyjson.NewObject("agent_hours", int64(supportAgentHours), "session_days", int64(supportSessionDays),
			"token_days", int64(supportTokenDays), "review_days", int64(supportReviewDays),
			"online_minutes", int64(supportOnlineWindow/time.Minute)),
	), nil
}

// supportAgents: the project's sessions the viewer may see and the agents
// that wrote recently.  The second result counts the running sessions.
func (c *reqCtx) supportAgents(p *project.Project, now time.Time) (*pyjson.Object, int64, error) {
	sessionsOut := []any{}
	counts := pyjson.NewObject("running", int64(0), "idle", int64(0), "error", int64(0), "approval", int64(0),
		"active", int64(0), "total", int64(0))
	inc := func(k string) {
		n, _ := counts.Value(k).(int64)
		counts.Set(k, n+1)
	}
	running := int64(0)
	if c.app.Sessions != nil {
		user, admin, err := c.sessionViewer()
		if err != nil {
			return nil, 0, err
		}
		allowed := c.app.Sessions.AccessibleIDs(p.ID(), user, admin)
		items := c.app.Sessions.BoardItems(p.ID(), allowed)
		children := map[string]int64{}
		for _, s := range items {
			if parent := strOrEmpty(s.Value("parent_id")); parent != "" {
				if supportActive[strOrEmpty(s.Value("status"))] {
					children[parent]++
				}
			}
		}
		recent := clock.Format(now.Add(-supportSessionDays * 24 * time.Hour))
		for _, s := range items {
			status := strOrEmpty(s.Value("status"))
			top := strOrEmpty(s.Value("parent_id")) == "" && strOrEmpty(s.Value("side_chat_of")) == ""
			if top {
				inc("total")
			}
			busy := s.Value("busy") != nil
			switch {
			case status == "errored" || status == "conflict":
				if top {
					inc("error")
				}
			case status == "running" || status == "starting" || status == "stopping" || (status == "idle" && busy):
				running++
				if top {
					inc("running")
				}
			case status == "idle":
				if top {
					inc("idle")
				}
			}
			if n, _ := s.Value("approvals_pending").(int64); n > 0 && top {
				inc("approval")
			}
			if supportActive[status] && top {
				inc("active")
			}
			if !top || pyjson.Truthy(s.Value("closed")) && !supportActive[status] {
				continue
			}
			if !supportActive[status] && strOrEmpty(s.Value("last_activity_at")) < recent {
				continue
			}
			if len(sessionsOut) >= supportSessionLimit {
				continue
			}
			s.Set("children", children[strOrEmpty(s.Value("id"))]).Set("device", "ark").Set("task_title", nil)
			if ref := strOrEmpty(s.Value("task_ref")); ref != "" {
				if _, _, title, err := p.Store.UnitBrief(c.ctx, ref); err == nil {
					s.Set("task_title", title)
				}
			}
			sessionsOut = append(sessionsOut, s)
		}
	}
	external := []any{}
	writers, err := p.Store.SupportAgents(c.ctx, clock.Format(now.Add(-supportAgentHours*time.Hour)), supportAgentLimit)
	if err != nil {
		return nil, 0, err
	}
	live := map[string]*pyjson.Object{}
	if c.app.Presence != nil {
		entries, _ := c.app.Presence.Snapshot(p.ID())
		for _, e := range entries {
			if !strings.HasPrefix(e.Actor, "agent:") {
				continue
			}
			if prev := live[e.Actor]; prev == nil || e.Kind == "working" {
				live[e.Actor] = pyjson.NewObject("state", e.State, "kind", e.Kind, "unit_uid", e.UID, "unit_title", e.Title,
					"note", e.Note, "at", clock.Format(e.At))
			}
		}
	}
	seen := map[string]bool{}
	byID := map[string]*pyjson.Object{}
	for _, x := range sessionsOut {
		o := x.(*pyjson.Object)
		byID[strOrEmpty(o.Value("id"))] = o
	}
	for _, w := range writers {
		actor := strOrEmpty(w.Value("actor"))
		seen[actor] = true
		w.Set("label", agentLabel(actor)).Set("live", live[actor])
		if s := byID[strOrEmpty(w.Value("session_id"))]; s != nil {
			// The session's own token, or a checked X-ARK-Session header:
			// the same agent, listed once on its session row.
			linked, _ := s.Value("linked_agents").([]any)
			s.Set("linked_agents", append(linked, w))
			continue
		}
		if w.Value("session_id") == nil {
			if s := supportLikelySession(w, sessionsOut); s != nil {
				w.Set("likely_session", pyjson.NewObject("id", s.Value("id"), "title", s.Value("title")))
			}
		}
		external = append(external, w)
	}
	for actor, l := range live { // announced or reading, no write in the window
		if seen[actor] || len(external) >= supportAgentLimit {
			continue
		}
		external = append(external, pyjson.NewObject("actor", actor, "label", agentLabel(actor), "writes", int64(0),
			"last_at", l.Value("at"), "unit_uid", orNil(l.Value("unit_uid")), "unit_title", orNil(l.Value("unit_title")), "live", l))
	}
	return pyjson.NewObject("sessions", sessionsOut, "session_counts", counts, "external", external), running, nil
}

// supportLikelySession is the one active listed session an unlinked token
// agent probably is: the same person launched it, the agent's last write
// came after the session started and names the session's task.  It is a
// guess (no session token, no X-ARK-Session header), so the panel keeps the
// agent on its own row and only says which session it may belong to.
func supportLikelySession(w *pyjson.Object, sessions []any) *pyjson.Object {
	user, _, _ := strings.Cut(strings.TrimPrefix(strOrEmpty(w.Value("actor")), "agent:"), "/")
	unit := strOrEmpty(w.Value("unit_uid"))
	last := strOrEmpty(w.Value("last_at"))
	if user == "" || unit == "" {
		return nil
	}
	var hit *pyjson.Object
	for _, x := range sessions {
		s := x.(*pyjson.Object)
		if !supportActive[strOrEmpty(s.Value("status"))] || strOrEmpty(s.Value("owner")) != user ||
			!strings.EqualFold(strOrEmpty(s.Value("task_ref")), unit) || last < strOrEmpty(s.Value("created_at")) {
			continue
		}
		if hit != nil {
			return nil // two candidates: no guess
		}
		hit = s
	}
	return hit
}

func orNil(v any) any {
	if !pyjson.Truthy(v) {
		return nil
	}
	return v
}

// supportHostKinds are the graph devices the panel lists as hosts.
var supportHostKinds = map[string]bool{"ark": true, "host": true, "gpu-server": true, "compute": true}

// supportHosts: the graph's hosts with their sessions and GPU jobs, and the
// viewer's own setup devices the graph does not place.
func (c *reqCtx) supportHosts(p *project.Project, graph *pyjson.Object, running int64, jobs []*pyjson.Object, now time.Time) []any {
	byServer := map[string][2]int64{} // GPU server name (lower) -> running, queued
	for _, j := range jobs {
		name := strings.ToLower(strOrEmpty(j.Value("server")))
		n := byServer[name]
		switch strOrEmpty(j.Value("status")) {
		case "running", "starting":
			n[0]++
		default:
			n[1]++
		}
		byServer[name] = n
	}
	queuedAnywhere := byServer[""][1]
	out := []any{}
	index := map[string]*pyjson.Object{}
	for _, d := range objectList(graph.Value("devices")) {
		kind := strOrEmpty(d.Value("kind"))
		if !supportHostKinds[kind] {
			continue
		}
		st, _ := d.Value("status").(*pyjson.Object)
		item := pyjson.NewObject("id", d.Value("id"), "kind", kind, "label", d.Value("label"),
			"scope", d.Value("scope"), "mine", pyjson.Truthy(d.Value("mine")),
			"state", "unknown", "code", nil, "last_seen", nil, "sessions_running", int64(0),
			"gpu", nil, "jobs", nil, "probed_at", nil, "current", false, "paths_set", nil, "paths_missing", nil)
		if st != nil {
			item.Set("state", st.Value("state")).Set("code", st.Value("code")).Set("last_seen", st.Value("last_seen"))
		}
		if kind == "ark" {
			item.Set("sessions_running", running)
		}
		if occ, ok := d.Value("occupancy").(*pyjson.Object); ok {
			item.Set("gpu", pyjson.NewObject("total", occ.Value("gpus_total"), "free", occ.Value("gpus_free"),
				"ours", occ.Value("gpus_ours"), "busy_others", occ.Value("gpus_busy_others")))
		}
		if pr, ok := d.Value("probe").(*pyjson.Object); ok {
			item.Set("probed_at", pr.Value("probed_at"))
		}
		if kind == "gpu-server" {
			n := byServer[strings.ToLower(strOrEmpty(d.Value("label")))]
			item.Set("jobs", pyjson.NewObject("running", n[0], "queued", n[1]))
		}
		index[strOrEmpty(d.Value("id"))] = item
		out = append(out, item)
	}
	if queuedAnywhere > 0 {
		for _, item := range out {
			if j, ok := item.(*pyjson.Object).Value("jobs").(*pyjson.Object); ok {
				j.Set("queued_any", queuedAnywhere)
			}
		}
	}
	// The viewer's own setup devices: last seen and paths.
	v, err := c.setupViewer(p, nil, false)
	if err != nil {
		return out
	}
	payload, err := c.setupPayloadFor(p, v, false)
	if err != nil || payload == nil {
		return out
	}
	server, legacy := setup.MachineFingerprint(), setup.LegacyMachineFingerprint()
	for _, x := range objectList(payload.Value("devices")) {
		id := strOrEmpty(x.Value("id"))
		if id == "" {
			continue
		}
		gid := "host:" + id
		if id == server || (legacy != "" && id == legacy) {
			gid = "ark"
		}
		item := index[gid]
		if item == nil {
			item = pyjson.NewObject("id", gid, "kind", "host", "label", x.Value("label"), "scope", "personal", "mine", true,
				"state", "unknown", "code", "no_signal", "last_seen", nil, "sessions_running", int64(0),
				"gpu", nil, "jobs", nil, "probed_at", nil, "current", false, "paths_set", nil, "paths_missing", nil)
			index[gid] = item
			out = append(out, item)
		}
		set := int64(0)
		for _, path := range objectList(x.Value("paths")) {
			if pyjson.Truthy(path.Value("path_or_ref")) {
				set++
			}
		}
		missing, _ := x.Value("missing").([]any)
		item.Set("paths_set", set).Set("paths_missing", int64(len(missing))).Set("device_id", id)
		if x.Value("current") == true && v.HostSource != "" && v.HostSource != "server" {
			item.Set("current", true)
		}
		if seen := strOrEmpty(x.Value("last_seen")); seen != "" && seen > strOrEmpty(item.Value("last_seen")) {
			item.Set("last_seen", seen)
			if t, err := time.Parse(time.RFC3339, seen); err == nil && gid != "ark" {
				if now.Sub(t) <= supportOnlineWindow {
					item.Set("state", "online").Set("code", "recent_signal")
				} else if item.Value("state") == "unknown" {
					item.Set("state", "stale").Set("code", "old_signal")
				}
			}
		}
	}
	return out
}

// supportLibrary counts the setup resources of the graph by kind (one per
// resource, not per location) and adds up their measured sizes.
func supportLibrary(graph *pyjson.Object) *pyjson.Object {
	kinds := []string{"repository", "task_space", "storage", "database", "environment", "compute"}
	seen := map[string]bool{}
	counts := map[string]int64{}
	var bytes int64
	measured := int64(0)
	worktrees := int64(0)
	for _, r := range objectList(graph.Value("resources")) {
		kind := strOrEmpty(r.Value("kind"))
		if kind == "worktree" {
			worktrees++
			continue
		}
		key := strOrEmpty(r.Value("setup_key"))
		if key == "" {
			continue
		}
		if size, ok := r.Value("size").(*pyjson.Object); ok {
			if b, ok := size.Value("bytes").(int64); ok && b > 0 {
				bytes += b
				measured++
			}
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		counts[kind]++
	}
	out := pyjson.NewObject()
	total := int64(0)
	for _, k := range kinds {
		out.Set(k, counts[k])
		total += counts[k]
	}
	out.Set("worktree", worktrees).Set("total", total).Set("measured", measured)
	if measured > 0 {
		out.Set("bytes", bytes)
	} else {
		out.Set("bytes", nil)
	}
	return out
}

// supportDeadlines: expiring tokens, GPU job time limits, long reviews.
func (c *reqCtx) supportDeadlines(p *project.Project, jobs []*pyjson.Object, now time.Time) ([]any, error) {
	out := []any{}
	if c.app.Org != nil && c.actor() != "" {
		tokens, err := c.app.Org.ExpiringTokens(c.ctx, c.actor(), p.ID(), supportTokenDays)
		if err != nil {
			return nil, err
		}
		for _, t := range tokens {
			out = append(out, pyjson.NewObject("kind", "token", "id", t.Value("id"), "label", t.Value("label"),
				"prefix", t.Value("prefix"), "at", t.Value("expires_at"), "starter", t.Value("starter")))
		}
	}
	for _, j := range jobs {
		if s := strOrEmpty(j.Value("status")); s != "running" && s != "starting" {
			continue
		}
		started, err := time.Parse("2006-01-02T15:04:05-07:00", strOrEmpty(j.Value("started_at")))
		mins, _ := j.Value("timeout_minutes").(int64)
		if err != nil || mins <= 0 {
			continue
		}
		out = append(out, pyjson.NewObject("kind", "gpu_job", "id", j.Value("id"), "label", j.Value("job"),
			"server", j.Value("server"), "task", orNil(j.Value("task")), "at", clock.Format(started.Add(time.Duration(mins)*time.Minute))))
	}
	reviews, err := p.Store.SupportReviewWaits(c.ctx, clock.Format(now.Add(-supportReviewDays*24*time.Hour)), 5)
	if err != nil {
		return nil, err
	}
	for _, r := range reviews {
		out = append(out, pyjson.NewObject("kind", "review", "uid", r.Value("uid"), "label", r.Value("title"),
			"round", r.Value("round"), "since", r.Value("since"), "at", r.Value("since")))
	}
	return out, nil
}
