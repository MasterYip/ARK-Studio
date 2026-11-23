package server

// Splitting and merging projects (Go only; store/move.go, moveplans.go,
// clusters.go, T-0068).  An agent proposes, a person approves, then the
// agent (or the person) applies:
//
//   GET  {base}/clusters                 branches and weakly linked groups of them
//   GET  {base}/plans                    the project's plans
//   POST {base}/plans                    propose:
//        {mode: "split", roots: [uid] | clusters: [id], cross_links: keep|drop,
//         new_project: {name, description}, title?, note?, lang?}
//        {mode: "merge", target_project, title?, note?, lang?}
//   GET  {base}/plans/{id}               the plan, its state and summary
//   POST {base}/plans/{id}/approve       a person only: {comment?, apply?: true}
//   POST {base}/plans/{id}/reject        a person only: {comment?}
//   POST {base}/plans/{id}/apply         once approved (agents included)
//   POST {base}/plans/{id}/refresh       plan a stale plan again (back to proposed, same decision unit)
//   POST {base}/plans/{id}/undo          a person, or an agent once the plan was approved
//
// A plan is stale only when what it covers changed (store/moveplandeps.go);
// GET shows that as "stale" ({reasons, units}), and approve and apply answer
// 409 MOVE_PLAN_STALE with the same reasons and unit ids.
//
//   Import (a copy into this project; T-0070 rules, store/importark.go):
//   POST /api/agent/v1/import-ark/plans  {project: <source id>, units, with_children?, note?, lang?}
//   GET  /api/agent/v1/import-ark/graph?project=<id>&<filters>, .../unit?project=&uid=
//   and the same /plans/{id}[/apply|/undo] as above (also under /api/agent/v1/project-move).
//   An import needs write access here and read access to the source.
//
// {base} is /api/project-move for the web app and /api/agent/v1/project-move
// for agents.  The current project is the source; every step needs admin
// access to both projects (for an agent token: write scope and an account
// with admin access).  An agent's plan opens a decision unit in the source
// that waits in Needs you; approving or rejecting closes it.  A split
// creates the target project on apply; a merged project becomes read-only
// (project.json merged_into) until the merge is undone.

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/store"
)

const (
	projectMoveBase      = "/api/project-move"
	agentProjectMoveBase = "/api/agent/v1/project-move"
	agentImportArkBase   = "/api/agent/v1/import-ark"
)

var reMovePlanPath = regexp.MustCompile(`^/(clusters|plans)(?:/(pl_[0-9a-f]{12})(?:/(approve|reject|apply|refresh|undo))?)?$`)

// projectMoveGoOnly are the Go-only ids of this file (GoOnlyRoutes).
var projectMoveGoOnly = []PortedRoute{
	{"goonly:prefix:" + projectMoveBase + "/", "GET,HEAD,POST"},
	{"goonly:prefix:" + agentProjectMoveBase + "/", "GET,HEAD,POST"},
	{"goonly:prefix:" + agentImportArkBase + "/", "GET,HEAD,POST"},
}

// agentImportArkRoute is {agentImportArkBase}{rest}: the source graph and
// unit reads of the web import, and import plans.
func (c *reqCtx) agentImportArkRoute(rest string, post bool) error {
	if !post && (rest == "/graph" || rest == "/unit") {
		return c.importArkGet(importArkBase + strings.TrimPrefix(rest, "/"))
	}
	if !strings.HasPrefix(rest, "/plans") {
		return apiErr(404, "Not found", "")
	}
	if post && rest == "/plans" && c.body != nil {
		if mode, _ := c.body.Value("mode").(string); mode == "" {
			c.body.Set("mode", "import")
		}
	}
	return c.projectMoveRoute(rest, post)
}

func isProjectMovePath(path string) bool { return strings.HasPrefix(path, projectMoveBase+"/") }

func (c *reqCtx) isAgentCall() bool {
	return c.token != nil || strings.HasPrefix(c.actorForMove(), "agent:")
}

func (c *reqCtx) actorForMove() string {
	if c.writer != "" {
		return c.writer
	}
	return c.writeActor()
}

// moveProject is the source project with admin access checked.
func (c *reqCtx) moveProject() (*project.Project, error) {
	if c.token != nil {
		p, err := c.require("write")
		if err != nil {
			return nil, err
		}
		if c.app.Auth != nil {
			if err := c.requireProjectPermission(p.ID(), "admin"); err != nil {
				return nil, err
			}
		}
		return p, nil
	}
	return c.require("admin")
}

func (c *reqCtx) moveTarget(id string) (*project.Project, error) {
	if c.app.Auth != nil {
		if err := c.requireProjectPermission(id, "admin"); err != nil {
			return nil, err
		}
	}
	dst, err := c.projectByID(id)
	if err != nil {
		return nil, err
	}
	if dst == nil {
		return nil, apiErr(404, "target project not found", "PROJECT_NOT_ACCESSIBLE")
	}
	return dst, nil
}

// projectMoveRoute answers {base}/... (rest is the path after the base).
func (c *reqCtx) projectMoveRoute(rest string, post bool) error {
	m := reMovePlanPath.FindStringSubmatch(rest)
	if m == nil {
		return apiErr(404, "Not found", "")
	}
	if m[1] == "clusters" {
		if post || m[2] != "" {
			return apiErr(405, "Method not allowed", "")
		}
		p, err := c.require("read")
		if err != nil {
			return err
		}
		out, err := p.Store.SplitClusters(c.ctx)
		if err != nil {
			return err
		}
		c.ok(out)
		return nil
	}
	src, err := c.require("write")
	if err != nil {
		return err
	}
	if m[2] == "" {
		if !post {
			list := []any{}
			for _, p := range store.ListMovePlans(src.Path) {
				list = append(list, p.Public())
			}
			c.ok(pyjson.NewObject("plans", list))
			return nil
		}
		return c.proposeMove(src)
	}
	plan, err := store.LoadMovePlan(src.Path, m[2])
	if err != nil {
		return err
	}
	if m[3] != "" {
		if err := c.planAccess(src, plan); err != nil {
			return err
		}
	}
	switch {
	case m[3] == "" && !post:
		c.ok(pyjson.NewObject("plan", c.planView(src, plan)))
		return nil
	case m[3] == "refresh" && post:
		return c.refreshMove(src, plan)
	case !post:
		return apiErr(405, "Method not allowed", "")
	case m[3] == "approve" || m[3] == "reject":
		return c.decideMove(src, plan, m[3] == "approve")
	case m[3] == "apply":
		return c.applyMove(src, plan)
	case m[3] == "undo":
		return c.undoMove(src, plan)
	}
	return apiErr(404, "Not found", "")
}

// planAccess checks the access a plan needs: admin on both projects for a
// split or merge, write here and read on the source for an import.
func (c *reqCtx) planAccess(src *project.Project, plan *store.MovePlan) error {
	if plan.Mode == "import" {
		_, err := c.importSource(plan.Request.SourceID, src)
		return err
	}
	if _, err := c.moveProject(); err != nil {
		return err
	}
	if plan.Request.TargetID != "" {
		_, err := c.moveTarget(plan.Request.TargetID)
		return err
	}
	return nil
}

func (c *reqCtx) planView(src *project.Project, plan *store.MovePlan) *pyjson.Object {
	out := plan.Public()
	if plan.MoveID != "" {
		if j, err := store.LoadMoveJournal(src.Path, plan.MoveID); err == nil {
			out.Set("move_state", j.State)
		}
	}
	if plan.State == "proposed" || plan.State == "approved" {
		var stale any
		if other, err := c.planOther(plan); err == nil {
			var se *store.MoveStaleError
			if errors.As(src.Store.CheckPlan(c.ctx, other, plan), &se) {
				stale = staleObject(se)
			}
		}
		out.Set("stale", stale)
	}
	return out
}

// planOther is the other store a plan reads: the source of an import, the
// target of a merge, nil for a split (its project is made on apply).
func (c *reqCtx) planOther(plan *store.MovePlan) (*store.Store, error) {
	switch {
	case plan.Mode == "import":
		p, err := c.projectByID(plan.Request.SourceID)
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, apiErr(404, "source project not found", "PROJECT_NOT_ACCESSIBLE")
		}
		return p.Store, nil
	case plan.Mode == "merge" || plan.Request.All:
		p, err := c.projectByID(plan.Request.TargetID)
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, apiErr(404, "target project not found", "PROJECT_NOT_ACCESSIBLE")
		}
		return p.Store, nil
	}
	return nil, nil
}

func staleObject(se *store.MoveStaleError) *pyjson.Object {
	reasons := []any{}
	for _, r := range se.Reasons {
		reasons = append(reasons, r.Object())
	}
	units := []any{}
	for _, u := range se.Units() {
		units = append(units, u)
	}
	return pyjson.NewObject("reasons", reasons, "units", units)
}

// refreshMove is POST {base}/plans/{id}/refresh: plan again from the
// projects as they are now; the plan waits for approval again.
func (c *reqCtx) refreshMove(src *project.Project, plan *store.MovePlan) error {
	other, err := c.planOther(plan)
	if err != nil {
		return err
	}
	if err := src.Store.RefreshPlan(c.ctx, other, plan, c.actorForMove()); err != nil {
		return err
	}
	c.ok(pyjson.NewObject("plan", c.planView(src, plan)))
	return nil
}

// proposeMove is POST {base}/plans.
func (c *reqCtx) proposeMove(src *project.Project) error {
	b := c.body
	plan := &store.MovePlan{CreatedBy: c.actorForMove()}
	plan.Title, _ = b.Value("title").(string)
	plan.Note, _ = b.Value("note").(string)
	plan.Lang, _ = b.Value("lang").(string)
	plan.Request.SourceID, plan.Request.SourceName = src.ID(), src.Name()
	plan.Request.CrossLinks, _ = b.Value("cross_links").(string)
	var dst *store.Store
	mode, _ := b.Value("mode").(string)
	if mode == "import" {
		return c.proposeImport(src, plan)
	}
	if _, err := c.moveProject(); err != nil {
		return err
	}
	switch mode {
	case "", "split":
		roots := append(nonEmpty(stringList(b.Value("roots"))), nonEmpty(stringList(b.Value("units")))...)
		if ids := nonEmpty(stringList(b.Value("clusters"))); len(ids) > 0 {
			data, err := src.Store.SplitClusters(c.ctx)
			if err != nil {
				return err
			}
			byID := map[string][]string{}
			for _, x := range data.Value("clusters").([]any) {
				o := x.(*pyjson.Object)
				for _, r := range o.Value("roots").([]any) {
					byID[pyjson.Str(o.Value("id"))] = append(byID[pyjson.Str(o.Value("id"))], pyjson.Str(r.(*pyjson.Object).Value("uid")))
				}
			}
			for _, id := range ids {
				if _, ok := byID[id]; !ok {
					return apiErr(400, "unknown cluster: "+id+" (read the clusters again)", "MOVE_CLUSTER_UNKNOWN")
				}
				roots = append(roots, byID[id]...)
			}
		}
		if len(roots) == 0 {
			return apiErr(400, "roots or clusters is required", "MOVE_UNITS_REQUIRED")
		}
		plan.Request.Units = roots
		np, _ := b.Value("new_project").(*pyjson.Object)
		if np != nil {
			plan.NewName, _ = np.Value("name").(string)
			plan.NewDesc, _ = np.Value("description").(string)
		}
		plan.NewName = strings.TrimSpace(plan.NewName)
		if plan.NewName == "" {
			return apiErr(400, "new_project.name is required", "MOVE_TARGET_REQUIRED")
		}
		plan.Request.TargetName = plan.NewName
	case "merge":
		target, _ := b.Value("target_project").(string)
		if target == "" {
			return apiErr(400, "target_project is required", "MOVE_TARGET_REQUIRED")
		}
		if target == src.ID() {
			return apiErr(400, "the target is the current project", "MOVE_SAME_PROJECT")
		}
		p, err := c.moveTarget(target)
		if err != nil {
			return err
		}
		dst = p.Store
		plan.Request.All = true
		plan.Request.TargetID, plan.Request.TargetName = p.ID(), p.Name()
	default:
		return apiErr(400, "mode must be split, merge or import", "MOVE_MODE")
	}
	if err := src.Store.ProposeMove(c.ctx, dst, plan, c.isAgentCall()); err != nil {
		return err
	}
	c.ok(pyjson.NewObject("plan", plan.Public()))
	return nil
}

// proposeImport is POST {base}/plans with mode "import": the current
// project is the target, project the source.
func (c *reqCtx) proposeImport(target *project.Project, plan *store.MovePlan) error {
	b := c.body
	id, _ := b.Value("project").(string)
	src, err := c.importSource(id, target)
	if err != nil {
		return err
	}
	plan.Request.SourceID, plan.Request.SourceName = src.ID(), src.Name()
	plan.Request.TargetID, plan.Request.TargetName = target.ID(), target.Name()
	plan.Request.Units = nonEmpty(stringList(b.Value("units")))
	plan.Request.WithChildren = b.Value("with_children") == true
	plan.Request.CrossLinks = ""
	if len(plan.Request.Units) == 0 {
		return apiErr(400, "units is required", "IMPORT_UNITS_REQUIRED")
	}
	if len(plan.Request.Units) > 2000 {
		return apiErr(400, "at most 2000 units per import", "IMPORT_TOO_MANY")
	}
	if err := target.Store.ProposeImport(c.ctx, src.Store, plan, c.isAgentCall()); err != nil {
		return err
	}
	c.ok(pyjson.NewObject("plan", plan.Public()))
	return nil
}

// decideMove is POST {base}/plans/{id}/approve|reject: a person only.
func (c *reqCtx) decideMove(src *project.Project, plan *store.MovePlan, approve bool) error {
	if c.isAgentCall() {
		return apiErr(403, "only a person approves or rejects a move plan", "MOVE_NEEDS_PERSON")
	}
	comment, _ := c.body.Value("comment").(string)
	if approve && plan.State == "proposed" {
		// Approve what the plan covers now, not what it covered once.
		other, err := c.planOther(plan)
		if err != nil {
			return err
		}
		if err := src.Store.CheckPlan(c.ctx, other, plan); err != nil {
			return c.moveErr(err)
		}
	}
	if err := src.Store.DecidePlan(c.ctx, plan, approve, c.actorForMove(), comment); err != nil {
		return err
	}
	if approve && c.body.Value("apply") == true {
		return c.applyMove(src, plan)
	}
	c.ok(pyjson.NewObject("plan", c.planView(src, plan)))
	return nil
}

// applyMove is POST {base}/plans/{id}/apply.
func (c *reqCtx) applyMove(src *project.Project, plan *store.MovePlan) error {
	if plan.State != "approved" {
		return apiErr(409, "move plan "+plan.ID+" is "+plan.State+"; a person with admin access to both projects must approve it first",
			"MOVE_NOT_APPROVED")
	}
	if plan.Mode == "import" {
		return c.applyImport(src, plan)
	}
	req := plan.Request
	req.PlanID = plan.ID
	var dst *project.Project
	staleErr := func(err error) error {
		plan.Error = err.Error()
		store.SaveMovePlan(src.Path, plan) //nolint:errcheck
		return c.moveErr(err)
	}
	if plan.Mode == "split" {
		// Check before the new project is made; the uids are numbered for
		// it when the move runs.
		if err := src.Store.CheckPlan(c.ctx, nil, plan); err != nil {
			return staleErr(err)
		}
		var err error
		dst, err = c.createProjectRecord(pyjson.NewObject("name", plan.NewName, "description", plan.NewDesc))
		if err != nil {
			return err
		}
		req.TargetID, req.TargetName = dst.ID(), dst.Name()
	} else {
		var err error
		if dst, err = c.moveTarget(req.TargetID); err != nil {
			return err
		}
		if err := src.Store.CheckPlan(c.ctx, dst.Store, plan); err != nil {
			return staleErr(err)
		}
	}
	hash := plan.Hash
	// The move is the approver's act: its activity rows (and so the update
	// feed of agents working on the moved nodes) name the person.
	actor := plan.ApprovedBy
	if actor == "" {
		actor = c.actorForMove()
	}
	out, err := src.Store.ApplyMove(c.ctx, dst.Store, req, hash, actor)
	if err != nil {
		plan.Error = err.Error()
		store.SaveMovePlan(src.Path, plan) //nolint:errcheck
		return c.moveErr(err)
	}
	plan.State, plan.MoveID, plan.AppliedBy, plan.Error = "applied", pyjson.Str(out.Value("move_id")), c.actorForMove(), ""
	plan.Request.TargetID, plan.Request.TargetName = req.TargetID, req.TargetName
	if err := store.SaveMovePlan(src.Path, plan); err != nil {
		return err
	}
	if req.All {
		if err := src.Store.SetMerged(pyjson.NewObject("project", req.TargetID, "project_name", req.TargetName,
			"move_id", plan.MoveID)); err != nil {
			return err
		}
	}
	if c.app.Org != nil {
		detail := pyjson.NewObject("move_id", plan.MoveID, "plan", plan.ID, "source", req.SourceID, "target", req.TargetID,
			"units", out.Value("unit_count"), "approved_by", plan.ApprovedBy, "applied_by", plan.AppliedBy)
		uid, _ := c.app.Org.UserIDFor(c.ctx, c.actor())
		c.app.Org.Log(c.ctx, uid, "project."+plan.Mode, req.SourceID, detail) //nolint:errcheck // audit is best effort
	}
	out.Set("source_name", req.SourceName).Set("target_name", req.TargetName)
	c.ok(pyjson.NewObject("plan", c.planView(src, plan), "move", out))
	return nil
}

// applyImport carries out an approved import plan (src is the target).
func (c *reqCtx) applyImport(target *project.Project, plan *store.MovePlan) error {
	source, err := c.importSource(plan.Request.SourceID, target)
	if err != nil {
		return err
	}
	actor := plan.ApprovedBy
	if actor == "" {
		actor = c.actorForMove()
	}
	out, err := target.Store.ApplyImport(c.ctx, source.Store, plan, actor)
	if err != nil {
		plan.Error = err.Error()
		store.SaveMovePlan(target.Path, plan) //nolint:errcheck
		return c.moveErr(err)
	}
	plan.State, plan.AppliedBy, plan.Error = "applied", c.actorForMove(), ""
	if err := store.SaveMovePlan(target.Path, plan); err != nil {
		return err
	}
	c.ok(pyjson.NewObject("plan", plan.Public(), "import", out))
	return nil
}

// undoMove is POST {base}/plans/{id}/undo.
func (c *reqCtx) undoMove(src *project.Project, plan *store.MovePlan) error {
	if plan.Mode == "import" && plan.State == "applied" {
		out, err := src.Store.UndoImport(c.ctx, plan, c.body.Value("force") == true, c.actorForMove())
		if err != nil {
			return err
		}
		plan.State = "undone"
		if err := store.SaveMovePlan(src.Path, plan); err != nil {
			return err
		}
		c.ok(pyjson.NewObject("plan", plan.Public(), "import", out))
		return nil
	}
	if plan.State != "applied" || plan.MoveID == "" {
		return apiErr(409, "move plan "+plan.ID+" is "+plan.State+"; only an applied plan can be undone", "MOVE_NOT_APPLIED")
	}
	dst, err := c.moveTarget(plan.Request.TargetID)
	if err != nil {
		return err
	}
	out, err := src.Store.UndoMove(c.ctx, dst.Store, plan.MoveID, c.body.Value("force") == true, c.actorForMove())
	if err != nil {
		return err
	}
	if m, ok := src.Manifest.Get("merged_into").(*pyjson.Object); ok && pyjson.Str(m.Value("move_id")) == plan.MoveID {
		if err := src.Store.SetMerged(nil); err != nil {
			return err
		}
	}
	plan.State = "undone"
	if err := store.SaveMovePlan(src.Path, plan); err != nil {
		return err
	}
	c.ok(pyjson.NewObject("plan", c.planView(src, plan), "move", out))
	return nil
}

// moveErr answers a stale plan with 409 MOVE_PLAN_STALE, the reasons and
// the unit ids concerned (refresh or propose again).
func (c *reqCtx) moveErr(err error) error {
	var stale *store.MoveStaleError
	if errors.As(err, &stale) {
		e := pyjson.NewObject("status", int64(409), "code", "MOVE_PLAN_STALE",
			"message", stale.Error()+" (refresh the plan, or propose a new one)")
		so := staleObject(stale)
		e.Set("reasons", so.Value("reasons")).Set("units", so.Value("units"))
		c.drainUnreadBody()
		c.sendJSON(409, pyjson.NewObject("ok", false, "error", e), headerList{{"Connection", "close"}})
		return nil
	}
	return err
}

// nonEmpty drops empty strings.
func nonEmpty(list []string) []string {
	out := []string{}
	for _, s := range list {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// recoverMoves runs at start-up: moves a restart interrupted are rolled
// back, a merge whose last step (read-only mark, plan state) was lost is
// finished, and backups older than the undo window are removed.
func (a *App) recoverMoves() {
	ctx := context.Background()
	for _, entry := range a.Projects.ListProjects() {
		path := pyjson.Str(entry.Value("path"))
		if path == "" {
			continue
		}
		journals := store.MoveJournals(path)
		if len(journals) == 0 {
			continue
		}
		store.CleanMoveBackups(path, time.Now())
		var src *project.Project
		for _, j := range journals {
			plan, _ := store.LoadMovePlan(path, j.PlanID)
			if !j.Interrupted() && (plan == nil || plan.State != "approved" || j.State != "done") {
				continue
			}
			if src == nil {
				p, err := a.Projects.Open(path)
				if err != nil {
					break
				}
				src = p
			}
			var dst *store.Store
			if p, err := a.Projects.Open(j.TargetPath); err == nil {
				dst = p.Store
			}
			if j.Interrupted() {
				if err := src.Store.RecoverMove(ctx, dst, j); err != nil {
					continue
				}
				if plan != nil {
					plan.Error = "interrupted by a server restart; rolled back"
					store.SaveMovePlan(path, plan) //nolint:errcheck
				}
				continue
			}
			// Done, but the plan was not marked applied: finish its last step.
			plan.State, plan.MoveID = "applied", j.ID
			plan.Request.TargetID, plan.Request.TargetName = j.Target, j.TargetName
			if plan.Mode == "merge" {
				src.Store.SetMerged(pyjson.NewObject("project", j.Target, "project_name", j.TargetName, "move_id", j.ID)) //nolint:errcheck
			}
			store.SaveMovePlan(path, plan) //nolint:errcheck
		}
	}
}
