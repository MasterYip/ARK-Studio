package server

import (
	"database/sql"
	"errors"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/skillbrief"
	"arkstudio/go/internal/skillkit"
	"arkstudio/go/internal/sqlitedb"
)

// POST /api/skills/<skill>/<action> (Handler._skills_post): render,
// validate, tasks, brief, round, history, leakage, verify, asset and the
// setup lifecycle actions under /api/skills/setup/.

var skillsPorted = []PortedRoute{
	{"prefix:/api/skills/", "POST"},
}

var reSkillsPost = pyRe(`^/api/skills/([^/]+)/([a-z][a-z-]*)$`, false)

// skillsError maps skillkit errors like the do_POST except chain:
// SkillError -> 404, other ValueErrors -> 400, anything else -> 500.
func skillsError(err error) error {
	var se *skillkit.SkillError
	var ve *skillkit.Error
	var ue *skillbrief.Uncaught
	switch {
	case errors.As(err, &se):
		return apiErr(404, se.Msg, "")
	case errors.As(err, &ve):
		return apiErr(400, ve.Msg, "")
	case errors.As(err, &ue):
		return apiErr(500, "Internal error: "+ue.Message, "")
	}
	return err
}

func (c *reqCtx) skillsPost(path string, body *pyjson.Object, actor string) error {
	m := reSkillsPost.FindStringSubmatch(path)
	if m == nil {
		return apiErr(404, "Unknown skills endpoint: "+path, "")
	}
	skillID, action := m[1], m[2]
	p, err := c.require("read")
	if err != nil {
		return err
	}
	summary, err := p.Summary()
	if err != nil {
		return err
	}
	if skillID == "setup" && setupActions[action] {
		return c.setupAction(action, body, actor)
	}
	switch action {
	case "render":
		template := pytext.Strip(pyjson.Str(orDefault(body.Value("template"), "")))
		if template == "" {
			return apiErr(400, "template is required", "")
		}
		data, err := skillkit.RenderTemplate(skillID, template, orDefault(body.Value("context"), pyjson.NewObject()), summary)
		if err != nil {
			return skillsError(err)
		}
		c.ok(data)
		return nil
	case "validate":
		def, ok := map[string]string{"agent-team": "task", "rrsi": "round", "design": "theme", "setup": "environment"}[skillID]
		if !ok {
			def = "task"
		}
		kind := pytext.Lower(pytext.Strip(pyjson.Str(orDefault(body.Value("kind"), def))))
		payload, ok := body.Value("payload").(*pyjson.Object)
		if !ok {
			payload = body.Copy()
			payload.Delete("kind")
		}
		root := pyjson.Str(summary.Value("path"))
		result, err := skillkit.ValidatePayload(skillID, kind, payload, root, true, summary)
		if err != nil {
			return skillsError(err)
		}
		if skillID == "agent-team" && (kind == "task" || kind == "request") {
			if err := c.addSetupWarnings(p, result, payload); err != nil {
				return err
			}
		}
		c.ok(result)
		return nil
	case "tasks":
		result, err := c.createAgentTask(p, body, summary, actor)
		if err != nil {
			return skillsError(err)
		}
		if err := c.addSetupWarnings(p, result, body); err != nil {
			return err
		}
		c.ok(result)
		return nil
	case "brief":
		result, err := skillbrief.ValidateBrief(body)
		if err != nil {
			return skillsError(err)
		}
		c.ok(result)
		return nil
	case "round":
		result, err := skillkit.ValidateRound(body)
		if err != nil {
			return skillsError(err)
		}
		c.ok(pyjson.NewObject("result", result))
		return nil
	case "history":
		result, err := skillkit.HistorySnapshot(body)
		if err != nil {
			return skillsError(err)
		}
		c.ok(pyjson.NewObject("result", result))
		return nil
	case "leakage":
		var patterns []any
		if l, ok := body.Value("patterns").([]any); ok {
			patterns = l
		}
		text := pyjson.Str(orDefault(body.Value("text"), orDefault(body.Value("diff"), "")))
		findings, err := skillkit.ScanLeakage(text, patterns)
		if err != nil {
			return skillsError(err)
		}
		c.ok(pyjson.NewObject("ok", len(findings) == 0, "findings", findings))
		return nil
	case "verify":
		kind := pyjson.Str(orDefault(body.Value("artifact_kind"), orDefault(body.Value("kind"), "auto")))
		result, err := skillkit.VerifyArtifact(orDefault(body.Value("path"), ""), pyjson.Str(summary.Value("path")), true, kind)
		if err != nil {
			return skillsError(err)
		}
		c.ok(result)
		return nil
	case "asset":
		c.ok(skillkit.ValidateAssetPath(orDefault(body.Value("path"), ""), pyjson.Str(summary.Value("path")), true))
		return nil
	}
	return apiErr(404, "Unknown skills action: "+action, "")
}

// addSetupWarnings is Handler._add_setup_warnings(result, task).
func (c *reqCtx) addSetupWarnings(p *project.Project, result *pyjson.Object, task *pyjson.Object) error {
	if result == nil {
		return nil
	}
	v, err := c.setupViewer(p, nil, false)
	if err != nil {
		return err
	}
	if task == nil {
		task = pyjson.NewObject()
	}
	warnings, err := c.setupEnv(p).CheckTask(v, task)
	if err != nil {
		return err
	}
	if len(warnings) > 0 {
		result.Set("setup_warnings", warnings)
		if l, ok := result.Value("warnings").([]any); ok {
			result.Set("warnings", append(l, warnings...))
		}
	}
	return nil
}

// createAgentTask is agent_team.create_task(store, body, project=..., actor=...).
func (c *reqCtx) createAgentTask(p *project.Project, body, summary *pyjson.Object, actor string) (*pyjson.Object, error) {
	request := body.Copy()
	create := pyjson.Truthy(request.Delete("create"))
	existing := []any{}
	err := p.Store.ReadQuery(c.ctx, func(q sqlitedb.Querier) error {
		rows, err := q.QueryContext(c.ctx, "SELECT uid FROM units WHERE type='task'")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var uid sql.NullString
			if err := rows.Scan(&uid); err != nil {
				return err
			}
			if uid.Valid {
				existing = append(existing, uid.String)
			} else {
				existing = append(existing, "None")
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	preview, err := skillkit.PrepareTask(request, existing, summary)
	if err != nil {
		return nil, err
	}
	if !pyjson.Truthy(preview.Value("ok")) {
		msgs := []string{}
		for _, e := range preview.Value("errors").([]any) {
			msgs = append(msgs, e.(string))
		}
		return nil, &skillkit.Error{Msg: strings.Join(msgs, "; ")}
	}
	preview.Set("created", false)
	preview.Set("ark_unit", nil)
	if !create {
		return preview, nil
	}
	manifest := preview.Value("manifest").(*pyjson.Object)
	unitDraft := preview.Value("unit").(*pyjson.Object)
	taskID := manifest.Value("task_id").(string)
	for _, e := range existing {
		if e == taskID {
			return nil, &skillkit.Error{Msg: "task ID already exists in the ARK project: " + taskID}
		}
	}
	data := pyjson.NewObject(
		"title", taskID+" · "+pyjson.Str(manifest.Value("title")),
		"type", "task",
		"status", unitDraft.Value("status"),
		"importance", unitDraft.Value("importance"),
		"body", preview.Value("markdown"),
		"tags", unitDraft.Value("tags"),
	)
	if pyjson.Truthy(manifest.Value("assignee")) {
		data.Set("assignee", manifest.Value("assignee"))
	}
	unit, err := p.Store.CreateUnit(c.ctx, data, actor, c.who(p))
	if err != nil {
		return nil, err
	}
	uid := pyjson.Str(unit.Value("uid"))
	manifest.Set("ark_unit_uid", uid)
	if uid != taskID {
		preview.Set("warnings", append(preview.Value("warnings").([]any),
			"ARK unit "+uid+" differs from protocol task ID "+taskID+"; both are recorded"))
		preview.Set("markdown", pytext.RStrip(preview.Value("markdown").(string))+"\n\n> ARK unit: `"+uid+"`\n")
	}
	updated, err := p.Store.UpdateUnit(c.ctx, uid, pyjson.NewObject(
		"body", preview.Value("markdown"), "tags", unitDraft.Value("tags")), actor, c.who(p))
	if err != nil {
		return nil, err
	}
	preview.Set("created", true)
	preview.Set("ark_unit", updated)
	manifest.Set("ark_unit_uid", uid)
	preview.Value("record_paths").(*pyjson.Object).Set("ark_unit", uid)
	return preview, nil
}
