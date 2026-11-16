package skillkit

import (
	"regexp"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/skillbrief"
)

// agent_team.py

var requiredText = []string{"title", "objective", "owner", "reviewer", "repository", "base_revision"}

var driveRe = regexp.MustCompile(`\A[A-Za-z]:`)

// listOfText is _list_of_text(value, limit, item_limit).
func listOfText(v any, limit, itemLimit int) []any {
	out := []any{}
	if v == nil {
		return out
	}
	items, ok := v.([]any)
	if !ok {
		items = []any{v}
	}
	if len(items) > limit {
		items = items[:limit]
	}
	for _, item := range items {
		if t := text(item, itemLimit); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func lot(v any) []any { return listOfText(v, 200, 500) }

// acceptanceItems is _acceptance(value).
func acceptanceItems(v any) []*obj {
	items, ok := v.([]any)
	if !ok {
		items = []any{}
		if pyjson.Truthy(v) {
			items = []any{v}
		}
	}
	if len(items) > 50 {
		items = items[:50]
	}
	out := []*obj{}
	for _, raw := range items {
		var command, threshold, evidence string
		if o, ok := raw.(*obj); ok {
			command = text(o.Value("command"), 1000)
			threshold = text(or(o.Value("threshold"), o.Value("expected")), 600)
			evidence = text(o.Value("evidence"), 600)
		} else {
			command = text(raw, 1000)
		}
		out = append(out, pyjson.NewObject("command", command, "threshold", threshold, "evidence", evidence))
	}
	return out
}

func evidenceBlock(v any) *obj {
	var retain, external []any
	if o, ok := v.(*obj); ok {
		retain = lot(or(o.Value("retain_in_git"), o.Value("retain")))
		external = lot(or(o.Value("external"), o.Value("outside_git")))
	} else {
		retain, external = lot(v), []any{}
	}
	return pyjson.NewObject("retain_in_git", retain, "external", external)
}

// hasDotDot is `".." in PurePosixPath(raw).parts`.
func hasDotDot(raw string) bool {
	for _, p := range strings.Split(raw, "/") {
		if p == ".." {
			return true
		}
	}
	return false
}

// validateWorktree is validate_worktree(worktree, task_id).
func validateWorktree(worktree, taskID string) error {
	raw := pytext.Strip(worktree)
	if raw == "" {
		return errf("worktree is required")
	}
	if strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, `\`) || driveRe.MatchString(raw) || hasDotDot(raw) {
		return errf("worktree must be a repository-relative path")
	}
	normalized := strings.TrimRight(strings.ReplaceAll(raw, `\`, "/"), "/")
	expected := ".worktrees/" + taskID
	if normalized != expected {
		return errf("worktree must be '" + expected + "' (task worktrees live under .worktrees/)")
	}
	return nil
}

func lowerEq(a, b string) bool { return pytext.Lower(a) == pytext.Lower(b) }

// PrepareTask is agent_team.prepare_task(task, existing_ids=..., project=...).
func PrepareTask(task *obj, existingIDs any, project *obj) (*obj, error) {
	task = copyObj(task)
	existing, err := iterate(existingIDs)
	if err != nil {
		return nil, err
	}
	d := skillbrief.NewDiag()

	taskID := pytext.Upper(text(or(task.Value("task_id"), task.Value("id")), 80))
	if taskID != "" {
		if err := checkUniqueTaskID(taskID, existing); err != nil {
			d.Err(err.Error())
		}
	} else {
		prefix := pytext.Upper(text(or(task.Value("prefix"), or(task.Value("task_prefix"), "T")), 40))
		id, err := nextTaskID(existing, prefix)
		if err != nil {
			msg := err.Error()
			if msg == "" {
				msg = "could not allocate a task ID"
			}
			d.Err(msg)
			p := prefix
			if p == "" {
				p = "T"
			}
			id = p + "-001"
		}
		taskID = id
	}

	project = copyObj(project)
	projectID := text(or(task.Value("project_id"), or(project.Value("id"), "ark")), 120)
	projectName := text(or(task.Value("project_name"), or(project.Value("name"), projectID)), 200)

	phase := pytext.Lower(text(or(task.Value("phase"), "proposed"), 40))
	status := pytext.Lower(text(or(task.Value("status"), "waiting"), 40))
	if !inList(phase, Phases) {
		d.Err("phase must be one of: " + strings.Join(Phases, ", "))
	}
	if !inList(status, Statuses) {
		d.Err("status must be one of: " + strings.Join(Statuses, ", "))
	}
	ark := "open"
	if inList(phase, Phases) && inList(status, Statuses) {
		ark = arkStatus[status]
	}

	for _, key := range requiredText {
		if text(task.Value(key), 4000) == "" {
			d.Err(key + " is required")
		}
	}

	title := text(task.Value("title"), 240)
	objective := text(or(task.Value("objective"), task.Value("goal")), 6000)
	owner := text(task.Value("owner"), 200)
	reviewer := text(task.Value("reviewer"), 200)
	var assignee any
	if a := text(task.Value("assignee"), 64); a != "" {
		assignee = a
	}
	repository := text(task.Value("repository"), 240)
	baseRevision := text(or(task.Value("base_revision"), task.Value("base")), 200)
	if owner != "" && reviewer != "" && lowerEq(owner, reviewer) && !pyjson.Truthy(task.Value("acceptance_exception")) {
		d.Err("owner and reviewer must differ unless acceptance_exception is declared")
	}

	dependencies := []any{}
	for _, v := range lot(task.Value("dependencies")) {
		if s := pytext.Strip(v.(string)); s != "" {
			dependencies = append(dependencies, pytext.Upper(s))
		}
	}
	for _, dep := range dependencies {
		if err := parseTaskID(dep.(string)); err != nil {
			d.Err("dependency " + pyjson.Repr(dep) + ": " + err.Error())
		}
	}

	worktree := text(or(task.Value("worktree"), ".worktrees/"+taskID), 300)
	worktreeException := pyjson.Truthy(task.Value("worktree_exception"))
	if !worktreeException {
		if err := validateWorktree(worktree, taskID); err != nil {
			d.Err(err.Error())
		}
	} else {
		d.Warn("worktree_exception recorded; the default .worktrees/<TASK-ID> layout is bypassed")
	}

	deliverables := lot(task.Value("deliverables"))
	if len(deliverables) == 0 {
		d.Err("deliverables must declare at least one measurable output")
	}
	nonGoals := lot(task.Value("non_goals"))
	if len(nonGoals) == 0 {
		d.Warn("non_goals is empty; declare what this task will not do")
	}
	ownedPaths := lot(task.Value("owned_paths"))
	if len(ownedPaths) == 0 {
		d.Err("owned_paths must declare at least one file or glob")
	}
	acceptance := acceptanceItems(task.Value("acceptance"))
	anyCommand := false
	for _, a := range acceptance {
		anyCommand = anyCommand || a.Value("command") != ""
	}
	if len(acceptance) == 0 || !anyCommand {
		d.Err("acceptance must include at least one command or check")
	}
	gateList := lot(task.Value("gates"))
	if len(gateList) == 0 {
		gateList = []any{"scope", "static", "unit"}
		d.Warn("gates omitted; defaulted to scope, static, unit")
	}
	ordered, err := orderedGates(gateList)
	if err != nil {
		d.Err(err.Error())
		ordered = []string{}
	}
	gatesAny := strList(ordered)
	stopConditions := lot(task.Value("stop_conditions"))
	if len(stopConditions) == 0 {
		d.Warn("stop_conditions is empty; declare when to stop and escalate")
	}
	risks := lot(task.Value("risks"))
	background := text(or(task.Value("background"), task.Value("hypothesis")), 6000)
	evidence := evidenceBlock(task.Value("evidence"))
	importance := pytext.Lower(text(or(task.Value("importance"), "p2"), 10))
	if !inList(importance, []string{"p0", "p1", "p2", "p3"}) {
		d.Err("importance must be p0, p1, p2 or p3")
	}
	var budgetText string
	if b, ok := task.Value("budget").(*obj); ok {
		parts := []string{}
		for _, k := range b.Keys() {
			parts = append(parts, k+"="+str(b.Value(k)))
		}
		budgetText = strings.Join(parts, ", ")
	} else {
		budgetText = text(task.Value("budget"), 600)
	}
	acceptanceException := pyjson.Truthy(task.Value("acceptance_exception"))

	structure := pytext.Lower(text(or(task.Value("task_structure"), "sequential"), 40))
	if !inList(structure, skillbrief.TaskStructures) {
		d.Err("task_structure must be one of: " + strings.Join(skillbrief.TaskStructures, ", "))
		structure = "sequential"
	}
	team, err := skillbrief.NormalizeTeam(d, task.Value("team"), structure, reviewer)
	if err != nil {
		return nil, err
	}
	contextPlan, err := skillbrief.NormalizeContext(d, task.Value("context"))
	if err != nil {
		return nil, err
	}
	modelPolicy := skillbrief.NormalizeModelPolicy(d, task.Value("model_policy"), team.Value("size"))
	verification := skillbrief.NormalizeVerification(d, task.Value("verification"), gatesAny, acceptance, acceptanceException)
	taxonomy := skillbrief.NormalizeFailureTaxonomy(d, task.Value("failure_taxonomy"))
	handoff, err := skillbrief.NormalizeHandoff(d, task.Value("handoff"), objective, deliverables, dependencies)
	if err != nil {
		return nil, err
	}

	gateEv := pyjson.NewObject()
	if len(ordered) > 0 {
		gateEv = gateEvidence(ordered)
	}
	accAny := make([]any, len(acceptance))
	for i, a := range acceptance {
		accAny[i] = a
	}
	arkUID := any(text(task.Value("ark_unit_uid"), 40))
	if arkUID == "" {
		arkUID = taskID
	}
	manifest := pyjson.NewObject(
		"format", "ark-agent-team-task",
		"protocol_version", int64(ProtocolVersion),
		"interface_version", skillbrief.InterfaceVersion,
		"task_id", taskID,
		"title", title,
		"project_id", projectID,
		"project_name", projectName,
		"ark_unit_uid", arkUID,
		"phase", phase,
		"status", status,
		"ark_status", ark,
		"owner", owner,
		"reviewer", reviewer,
		"assignee", assignee,
		"repository", repository,
		"worktree", worktree,
		"base_revision", baseRevision,
		"dependencies", dependencies,
		"importance", importance,
		"budget", budgetText,
		"background", background,
		"objective", objective,
		"deliverables", deliverables,
		"non_goals", nonGoals,
		"owned_paths", ownedPaths,
		"acceptance", accAny,
		"gates", gatesAny,
		"gate_evidence", gateEv,
		"evidence", evidence,
		"risks", risks,
		"stop_conditions", stopConditions,
		"task_structure", structure,
		"team", team,
		"context", contextPlan,
		"model_policy", modelPolicy,
		"verification", verification,
		"failure_taxonomy", taxonomy,
		"handoff", handoff,
		"created_at", UTCNow(),
	)

	tags := []any{"skills:agent-team", "task:" + taskID, "project:" + projectID}
	if role := text(task.Value("role"), 40); role != "" {
		tags = append(tags, "skill-role:"+role)
	}
	tags = append(tags, "topology:"+str(team.Value("mode")))
	if phase == "review" || phase == "evaluation" {
		tags = append(tags, "phase:"+phase)
	}
	unitTitle := taskID
	if title != "" {
		unitTitle = taskID + " · " + title
	}
	unit := pyjson.NewObject("title", unitTitle, "type", "task", "status", ark,
		"importance", importance, "body", "", "tags", tags)

	ctx := copyObj(manifest)
	if budgetText == "" {
		ctx.Set("budget", "_No explicit budget declared._")
	}
	ctx.Set("context_plan", contextPlan)

	markdown := ""
	if len(d.Errors) == 0 {
		md, err := RenderGoalManifest(ctx)
		if err != nil {
			if _, ok := err.(*Error); !ok {
				return nil, err
			}
			d.Err(err.Error())
		} else {
			markdown = md
			unit.Set("body", markdown)
		}
	}
	recordPaths := pyjson.NewObject(
		"canonical_record", "docs/agent-team/tasks/"+taskID+"/goal_manifest.md",
		"inspection_report", "docs/agent-team/tasks/"+taskID+"/inspection_report.md",
		"worktree", worktree,
	)
	links := []any{}
	for _, dep := range dependencies {
		links = append(links, pyjson.NewObject("target_task_id", dep, "relation", "depends_on"))
	}
	return pyjson.NewObject("ok", len(d.Errors) == 0, "errors", d.Errors, "warnings", d.Warnings,
		"manifest", manifest, "markdown", markdown, "unit", unit, "links", links,
		"record_paths", recordPaths), nil
}

// ValidateManifest is agent_team.validate_manifest(document, existing_ids=...).
func ValidateManifest(document *obj, existing []any) (*obj, error) {
	payload := copyObj(document)
	for _, k := range []string{"format", "protocol_version", "ark_status", "gate_evidence", "created_at"} {
		payload.Delete(k)
	}
	ownID := pytext.Upper(pytext.Strip(str(or(payload.Value("task_id"), ""))))
	if pyjson.Truthy(payload.Value("project_id")) && !pyjson.Truthy(payload.Value("project_name")) {
		payload.Set("project_name", payload.Value("project_id"))
	}
	kept := []any{}
	for _, v := range existing {
		if pytext.Upper(str(v)) != ownID {
			kept = append(kept, v)
		}
	}
	return PrepareTask(payload, kept, pyjson.NewObject("id", payload.Value("project_id"), "name", payload.Value("project_name")))
}

func soloTeam() *obj {
	return pyjson.NewObject("mode", "solo", "size", int64(1), "heterogeneous", false, "rationale", "", "roles", []any{})
}

func defaultPlan() *obj {
	return pyjson.NewObject("budget_tokens", int64(120000), "inline", []any{}, "spill", []any{}, "artifacts", []any{})
}

// withDefaults is {**defaults, **context} (defaults keep their positions).
func withDefaults(defaults, context *obj) *obj {
	for _, k := range context.Keys() {
		defaults.Set(k, context.Value(k))
	}
	return defaults
}

func RenderGoalManifest(context *obj) (string, error) {
	return substitute(assets["agent-team/templates/goal_manifest.md"], context)
}

func RenderInspectionReport(context *obj) (string, error) {
	merged := withDefaults(pyjson.NewObject(
		"task_id", "T-000", "owner", "", "reviewer", "", "phase", "review", "status", "waiting",
		"base_revision", "", "candidate_revision", "", "integration_revision", "",
		"deliverables", []any{}, "changed_files", []any{}, "commands", []any{}, "results", "",
		"deviations", []any{}, "residual_risks", []any{}, "artifact_inventory", []any{}, "handoff", []any{},
		"team", soloTeam(), "context_plan", defaultPlan(),
		"failure_taxonomy", strList(skillbrief.FailureClasses),
	), context)
	return substitute(assets["agent-team/templates/inspection_report.md"], merged)
}

func RenderAgentBrief(context *obj) (string, error) {
	merged := withDefaults(pyjson.NewObject(
		"task_id", "T-000", "parent_task", "_standalone_", "owner", "", "reviewer", "",
		"task_structure", "sequential", "team", soloTeam(), "objective", "", "owned_paths", []any{},
		"context_plan", defaultPlan(), "acceptance", []any{},
		"verification", pyjson.NewObject("independent", true, "method", "command", "evidence", []any{}, "reviewer_reproduces", []any{}),
		"handoff", pyjson.NewObject("contract", "", "inputs", []any{}, "outputs", []any{}),
		"stop_conditions", []any{},
		"failure_taxonomy", strList(skillbrief.FailureClasses),
	), context)
	return substitute(assets["agent-team/templates/agent_brief.md"], merged)
}

// ExampleTask is agent_team.example_task(project).
func ExampleTask(project *obj) *obj {
	project = copyObj(project)
	taskID := str(or(project.Value("next_task_id"), "T-0100"))
	return pyjson.NewObject(
		"create", false,
		"task_id", taskID,
		"title", "Ship the skills/protocol layer",
		"project_id", or(project.Value("id"), "ark-demo"),
		"project_name", or(project.Value("name"), "ARK project"),
		"phase", "ready",
		"status", "waiting",
		"owner", "agent:codex",
		"reviewer", "human:maintainer",
		"repository", "ARK-Studio",
		"worktree", ".worktrees/"+taskID,
		"base_revision", "main@0000000",
		"dependencies", []any{},
		"importance", "p1",
		"budget", "one worktree, no new runtime dependency",
		"background", "The team protocol currently lives in a standalone checkout.",
		"objective", "Expose an ARK-native, tested skills layer and keep it discoverable in Settings.",
		"deliverables", []any{
			"arkstudio/skills package with catalog, templates and validators",
			"Settings > Skills panel and read-only JSON API",
			"tests and protocol documentation",
		},
		"non_goals", []any{
			"replacing the ARK UI or building a second standalone app",
			"copying vendor benchmark harnesses or model credentials",
		},
		"owned_paths", []any{"arkstudio/skills/**", "tests/python/agents/test_skills.py", "docs/agents/skills.md"},
		"acceptance", []any{
			pyjson.NewObject("command", "python -B -m unittest tests.python.agents.test_skills -v",
				"threshold", "all tests pass", "evidence", "test log"),
			pyjson.NewObject("command", "node --check web/js/views/settings.js",
				"threshold", "syntax check passes", "evidence", "shell output"),
		},
		"gates", strList(GateIDs[:3]),
		"task_structure", "long-horizon",
		"team", pyjson.NewObject("mode", "router-workers", "size", int64(3), "heterogeneous", false,
			"rationale", "three independent evidence routes share one task graph",
			"roles", []any{"router", "worker", "reviewer"}),
		"context", pyjson.NewObject("budget_tokens", int64(120000),
			"inline", []any{"task manifest", "current unit", "owned paths"},
			"spill", []any{"full logs", "datasets", "prior reports"},
			"artifacts", []any{"assets/"}),
		"model_policy", pyjson.NewObject("routing", "tiered", "planner", "inherit", "worker", "inherit", "verifier", "independent"),
		"verification", pyjson.NewObject("independent", true, "method", "command",
			"evidence", []any{"test log", "diff"}, "reviewer_reproduces", []any{"unit"}),
		"failure_taxonomy", strList(skillbrief.FailureClasses),
		"handoff", pyjson.NewObject("contract", "Reviewer receives the diff, test log and residual risks.",
			"inputs", []any{"main@0000000"}, "outputs", []any{"green focused tests", "skills documentation"}),
		"evidence", pyjson.NewObject("retain_in_git", []any{"goal manifest", "test log", "source mapping"}, "external", []any{}),
		"risks", []any{"Settings tab regressions from a parallel GUI task"},
		"stop_conditions", []any{"a test or a required file is owned by another active task"},
	)
}
