package server

// Agent rules (Go only, T-0053): the rules an external coding agent follows
// in this project, composed from ARK's required protocol and the project's
// and the person's preference settings (internal/agentrules).
//
//   GET  /api/agent-rules            catalog, both scopes' settings, the
//                                    composed rules (Settings > Agents, the
//                                    Home starter prompts)
//   POST /api/agent-rules            {scope: "project"|"personal", settings}
//                                    project scope needs project admin
//   GET  /api/agent/v1/rules         the composed rules for the token's user
//                                    (?lang=zh for Chinese lines)

import (
	"encoding/json"

	"arkstudio/go/internal/agentrules"
	"arkstudio/go/internal/assistcfg"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/project"
)

const (
	agentRulesPath      = "/api/agent-rules"
	agentRulesAgentPath = "/api/agent/v1/rules"
)

var agentRulesGoOnly = []PortedRoute{
	{"goonly:exact:" + agentRulesPath, "GET,HEAD,POST"},
	{"goonly:exact:" + agentRulesAgentPath, "GET,HEAD"},
}

// toObject turns a Go value into the pyjson object the reply writer takes.
func toObject(v any) (*pyjson.Object, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	o, ok := pyjson.LoadsObject(string(b))
	if !ok {
		return nil, apiErr(500, "could not encode agent rules", "AGENT_RULES_ENCODE")
	}
	return o, nil
}

func (c *reqCtx) agentRulesSettings(p *project.Project) (agentrules.Settings, agentrules.Settings, error) {
	proj, err := agentrules.Load(agentrules.ProjectFile(p.Path))
	if err != nil {
		return proj, agentrules.Settings{}, err
	}
	pers, err := agentrules.Load(agentrules.PersonalFile(assistcfg.PrivateDir(), c.actor()))
	return proj, pers, err
}

type agentRulesReply struct {
	Catalog        *agentrules.Catalog `json:"catalog"`
	Project        agentrules.Settings `json:"project"`
	Personal       agentrules.Settings `json:"personal"`
	CanEditProject bool                `json:"can_edit_project"`
	Effective      agentrules.Composed `json:"effective"`
}

func (c *reqCtx) agentRulesPayload(p *project.Project) (*pyjson.Object, error) {
	cat, err := agentrules.Default()
	if err != nil {
		return nil, err
	}
	proj, pers, err := c.agentRulesSettings(p)
	if err != nil {
		return nil, err
	}
	_, adminErr := c.require("admin")
	return toObject(agentRulesReply{Catalog: cat, Project: proj, Personal: pers,
		CanEditProject: adminErr == nil, Effective: cat.Compose(proj, pers)})
}

// agentRules answers GET and POST /api/agent-rules (people in the project).
func (c *reqCtx) agentRules() error {
	if c.r.Method == "GET" || c.r.Method == "HEAD" {
		p, err := c.require("read")
		if err != nil {
			return err
		}
		out, err := c.agentRulesPayload(p)
		if err == nil {
			c.ok(out)
		}
		return err
	}
	if c.agentToken() != "" || c.token != nil {
		return apiErr(403, "Agent rules are changed by people in Settings.", "AGENT_RULES_HUMAN_ONLY")
	}
	scope := pytext.Strip(pyjson.Str(orDefault(c.body.Value("scope"), "")))
	level := "read"
	switch scope {
	case "project":
		level = "admin"
	case "personal":
	default:
		return apiErr(400, "scope must be project or personal", "AGENT_RULES_SCOPE")
	}
	p, err := c.require(level)
	if err != nil {
		return err
	}
	if c.actor() == "" {
		return apiErr(401, "Sign in with an ARK account to continue.", "AUTH_REQUIRED")
	}
	var s agentrules.Settings
	if raw := c.body.Value("settings"); raw != nil {
		if err := json.Unmarshal(pyjson.Dumps(raw), &s); err != nil {
			return apiErr(400, "settings must be {rules, custom}", "AGENT_RULES_INVALID")
		}
	}
	cat, err := agentrules.Default()
	if err != nil {
		return err
	}
	if err := cat.Validate(scope, s); err != nil {
		return apiErr(400, err.Error(), "AGENT_RULES_INVALID")
	}
	if pyjson.Truthy(c.body.Value("dry_run")) {
		// Preview: compose with the draft in place of the stored scope.
		proj, pers, err := c.agentRulesSettings(p)
		if err != nil {
			return err
		}
		if scope == "project" {
			proj = s
		} else {
			pers = s
		}
		out, err := toObject(map[string]any{"dry_run": true, "effective": cat.Compose(proj, pers)})
		if err == nil {
			c.ok(out)
		}
		return err
	}
	path := agentrules.PersonalFile(assistcfg.PrivateDir(), c.actor())
	if scope == "project" {
		path = agentrules.ProjectFile(p.Path)
	}
	if err := agentrules.Save(path, s); err != nil {
		return err
	}
	out, err := c.agentRulesPayload(p)
	if err == nil {
		c.ok(out)
	}
	return err
}

// agentRulesGet answers GET /api/agent/v1/rules for agents.
func (c *reqCtx) agentRulesGet(p *project.Project) error {
	cat, err := agentrules.Default()
	if err != nil {
		return err
	}
	proj, pers, err := c.agentRulesSettings(p)
	if err != nil {
		return err
	}
	comp := cat.Compose(proj, pers)
	lines := comp.Lines.EN
	if c.query.First("lang", "") == "zh" {
		lines = comp.Lines.ZH
	}
	rules := []any{}
	for _, e := range comp.Rules {
		if e.Marker == "" {
			continue
		}
		rules = append(rules, map[string]any{"marker": e.Marker, "id": e.ID, "kind": e.Kind, "scope": e.Scope,
			"source": e.Source, "text": e.Text})
	}
	out, err := toObject(map[string]any{"version": comp.Version, "lines": lines, "rules": rules})
	if err == nil {
		c.ok(out)
	}
	return err
}
