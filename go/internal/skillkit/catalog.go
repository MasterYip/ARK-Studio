package skillkit

import (
	"strings"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pytext"
	"arkstudio/go/internal/setup"
	"arkstudio/go/internal/skillbrief"
)

// catalog.render_template / validate_payload.

type template struct{ id, kind string }

var skillTemplates = []struct {
	id        string
	templates []template
}{
	{"setup", []template{{"environment.json", "json"}, {"setup_summary.md", "markdown"}}},
	{"agent-team", []template{{"goal_manifest.md", "markdown"}, {"inspection_report.md", "markdown"}, {"agent_brief.md", "markdown"}}},
	{"rrsi", []template{{"proposer.md", "markdown"}, {"analyst.md", "markdown"}, {"critic.md", "markdown"}, {"domain.json", "json"}}},
	{"design", []template{{"theme.json", "json"}, {"design_brief.md", "markdown"}, {"style_review.md", "markdown"}}},
}

func templatesOf(id string) ([]template, bool) {
	for _, s := range skillTemplates {
		if s.id == id {
			return s.templates, true
		}
	}
	return nil, false
}

// IsSkill reports whether id (already stripped and lowered) is a skill.
func IsSkill(id string) bool { _, ok := templatesOf(id); return ok }

func defaultContext(skill, tmpl string, project *obj) *obj {
	now := UTCNow()
	name := any(nil)
	if project != nil {
		name = project.Value("name")
	}
	switch skill {
	case "setup":
		return setup.ExampleProfile()
	case "agent-team":
		ex := ExampleTask(project)
		ex.Set("created_at", now)
		ex.Set("ark_unit_uid", ex.Value("task_id"))
		ex.Set("project_name", or(ex.Value("project_name"), "ARK project"))
		ex.Set("interface_version", skillbrief.InterfaceVersion)
		ex.Set("context_plan", ex.Value("context"))
		if tmpl == "agent_brief.md" {
			ex.Set("parent_task", ex.Value("task_id"))
		}
		return ex
	case "rrsi":
		return pyjson.NewObject(
			"task_title", or(name, "Improve the ARK coding-agent harness"),
			"incumbent_score", "0.6200", "incumbent_cost", "120000",
			"s_star", "0.6620", "delta", "0.017", "round_index", "0", "budget", "2",
			"components", strings.Join(Components, ", "),
			"structural_components", strings.Join(StructuralComponents, ", "),
			"prune_set", "_none_", "untried_components", "prompt, control_flow, config",
			"reserved_component", "_none_", "accepted_counts", `{"skill": 1}`,
			"round_evidence", "candidate-a 0.668 / 122000 tokens; candidate-b 0.640 / 110000 tokens",
			"diff", "+ prompt = render_ark_context(unit)\n- prompt = system_prompt",
		)
	case "design":
		return pyjson.NewObject(
			"project_name", or(name, "ARK project"),
			"audience", "researchers and maintainers",
			"venue", "internal report",
			"mediums", []any{"paper figure", "project website"},
			"qualities", []any{"precise", "calm", "technical"},
			"anti_goals", []any{"decorative gradients", "low-contrast gray text"},
			"theme_name", "academic-clean", "theme_version", "1",
			"artifact", "assets/figure.svg", "mode", "paper",
			"shared_checks", strList(gateShared),
			"mode_checks", strList(gatePaper),
			"verification", "verify_artifact('assets/figure.svg', project_root=...)",
		)
	}
	return pyjson.NewObject()
}

// RenderTemplate is catalog.render_template(skill_id, template, context,
// project=project); context nil means None.
func RenderTemplate(skillID, tmpl string, context any, project *obj) (*obj, error) {
	id := pytext.Lower(pytext.Strip(skillID))
	templates, ok := templatesOf(id)
	if !ok {
		return nil, &SkillError{"unknown skill: " + skillID}
	}
	tmpl = pytext.Strip(tmpl)
	kind := ""
	for _, t := range templates {
		if t.id == tmpl {
			kind = t.kind
		}
	}
	if kind == "" {
		return nil, &SkillError{"unknown template for " + id + ": " + tmpl}
	}
	var ctx *obj
	if context != nil {
		c, ok := context.(*obj)
		if !ok {
			return nil, &SkillError{"context must be an object"}
		}
		ctx = c
	}
	merged := defaultContext(id, tmpl, project)
	if ctx != nil && ctx.Len() > 0 {
		for _, k := range ctx.Keys() {
			merged.Set(k, ctx.Value(k))
		}
	}
	var text string
	var err error
	switch id {
	case "setup":
		profile, _, _ := setup.NormalizeProfile(merged)
		merged = profile
		if tmpl == "environment.json" {
			text = string(pyjson.DumpsIndent(profile, 2))
		} else {
			text = setup.RenderSummary(pyjson.NewObject("state", "draft", "profile", profile, "digest", setup.Digest(profile)))
		}
	case "agent-team":
		switch tmpl {
		case "goal_manifest.md":
			text, err = RenderGoalManifest(merged)
		case "agent_brief.md":
			text, err = RenderAgentBrief(merged)
		default:
			text, err = RenderInspectionReport(merged)
		}
	case "rrsi":
		if tmpl == "domain.json" {
			domain := ExampleDomain()
			if ctx != nil {
				for _, k := range ctx.Keys() {
					domain.Set(k, ctx.Value(k))
				}
			}
			text = string(pyjson.DumpsIndent(domain, 2))
		} else {
			var briefs *obj
			if briefs, err = BuildBriefs(merged); err == nil {
				text = briefs.Value(strings.TrimSuffix(tmpl, ".md")).(string)
			}
		}
	default:
		switch tmpl {
		case "theme.json":
			theme := ExampleTheme()
			if ctx != nil && ctx.Len() > 0 {
				theme = DeepMerge(theme, ctx)
			}
			text = string(pyjson.DumpsIndent(theme, 2))
		case "design_brief.md":
			text, err = RenderDesignBrief(merged)
		default:
			text, err = RenderStyleReview(merged)
		}
	}
	if err != nil {
		return nil, err
	}
	return pyjson.NewObject("skill", id, "template", tmpl, "kind", kind, "text", text, "context", merged), nil
}

func failed(err error) (*obj, error) {
	if e, ok := err.(*Error); ok {
		return pyjson.NewObject("ok", false, "errors", []any{e.Msg}, "warnings", []any{}), nil
	}
	return nil, err
}

// ValidatePayload is catalog.validate_payload(skill_id, kind, payload,
// project_root=..., project=...); project nil means None.
func ValidatePayload(skillID, kind string, payload *obj, projectRoot string, hasRoot bool, project *obj) (*obj, error) {
	id := pytext.Lower(pytext.Strip(skillID))
	kind = pytext.Lower(pytext.Strip(kind))
	payload = copyObj(payload)
	if !IsSkill(id) {
		return nil, &SkillError{"unknown skill: " + id}
	}
	in := func(opts ...string) bool { return inList(kind, opts) }
	switch id {
	case "setup":
		if in("environment", "profile") {
			profile, ok := payload.Value("profile").(*obj)
			if !ok {
				profile = payload
			}
			return setup.ValidateProfile(profile), nil
		}
		return failed(errf("unknown setup validation kind: " + kind))
	case "agent-team":
		switch {
		case in("task", "request"):
			p := project
			if p == nil {
				p = pyjson.NewObject("id", payload.Value("project_id"), "name", payload.Value("project_name"))
			}
			r, err := PrepareTask(payload, or(payload.Value("existing_ids"), []any{}), p)
			if err != nil {
				return failed(err)
			}
			return r, nil
		case in("manifest", "protocol"):
			ids, err := iterate(or(payload.Value("existing_ids"), []any{}))
			if err != nil {
				return failed(err)
			}
			own := str(payload.Value("task_id"))
			existing := []any{}
			for _, v := range ids {
				if str(v) != own {
					existing = append(existing, v)
				}
			}
			r, err := ValidateManifest(payload, existing)
			if err != nil {
				return failed(err)
			}
			return r, nil
		case in("brief", "delegation"):
			return skillbrief.ValidateBrief(payload)
		}
		return failed(errf("unknown agent-team validation kind: " + kind))
	case "rrsi":
		switch {
		case in("round", "selection"):
			r, err := ValidateRound(payload)
			if err != nil {
				return failed(err)
			}
			return pyjson.NewObject("ok", true, "result", r), nil
		case in("history", "edits"):
			r, err := HistorySnapshot(payload)
			if err != nil {
				return failed(err)
			}
			return pyjson.NewObject("ok", true, "result", r), nil
		case in("edit"):
			return pyjson.NewObject("ok", true, "result", ValidateEdit(payload)), nil
		case in("leakage", "leak"):
			findings, err := ScanLeakage(str(or(payload.Value("text"), or(payload.Value("diff"), ""))), nil)
			if err != nil {
				return failed(err)
			}
			return pyjson.NewObject("ok", len(findings) == 0, "findings", findings), nil
		case in("domain"):
			r, err := ValidateDomain(payload)
			if err != nil {
				return failed(err)
			}
			return r, nil
		}
		return failed(errf("unknown rrsi validation kind: " + kind))
	}
	switch {
	case in("theme", "theme.json"):
		theme := any(payload)
		if t, ok := payload.Value("theme").(*obj); ok {
			theme = t
		}
		r, err := ValidateTheme(theme, payload.Value("variant"))
		if err != nil {
			return failed(err)
		}
		return r, nil
	case kind == "artifact":
		r, err := VerifyArtifact(or(payload.Value("path"), ""), projectRoot, hasRoot, str(or(payload.Value("artifact_kind"), "auto")))
		if err != nil {
			return failed(err)
		}
		return r, nil
	case kind == "asset":
		return ValidateAssetPath(or(payload.Value("path"), ""), projectRoot, hasRoot), nil
	}
	return failed(errf("unknown design validation kind: " + kind))
}
