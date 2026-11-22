package server

// Home prompt library (Go only, 2026-11-22): custom prompts and Home pins
// (internal/promptlib).  The built-in prompts are in the web app; every
// copied prompt is composed with the project's agent rules there.
//
//   GET  /api/prompts          this person's and the project's custom prompts,
//                              this person's Home pins for the project (null:
//                              the defaults), the rules version
//   POST /api/prompts          {action: "save", scope, prompt: {id?, title, description, body}}
//                              {action: "delete", scope, id}
//                              {action: "import", scope, prompts: [...]}
//                              {action: "pins", pins: [ids] | null}
//                              project scope needs project admin; people only
//   GET  /api/agent/v1/prompts the token user's and the project's custom prompts
//   POST /api/agent/v1/prompts {scope?: "personal", id?, title, description, body}
//                              or {delete: id, scope?}: an agent writes a
//                              prompt for the person (project scope: admins)

import (
	"encoding/json"
	"errors"
	"strconv"

	"arkstudio/go/internal/agentrules"
	"arkstudio/go/internal/assistcfg"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/promptlib"
)

const (
	promptsPath      = "/api/prompts"
	promptsAgentPath = "/api/agent/v1/prompts"
)

var promptsGoOnly = []PortedRoute{
	{"goonly:exact:" + promptsPath, "GET,HEAD,POST"},
	{"goonly:exact:" + promptsAgentPath, "GET,HEAD,POST"},
}

func (c *reqCtx) promptFile(p *project.Project, scope string) string {
	if scope == promptlib.ScopeProj {
		return promptlib.ProjectFile(p.Path)
	}
	return promptlib.PersonalFile(assistcfg.PrivateDir(), c.actor())
}

func promptScope(v any) (string, error) {
	scope := pytext.Strip(pyjson.Str(orDefault(v, promptlib.ScopeUser)))
	if scope == "" {
		scope = promptlib.ScopeUser
	}
	if scope != promptlib.ScopeUser && scope != promptlib.ScopeProj {
		return "", apiErr(400, "scope must be personal or project", "PROMPT_SCOPE")
	}
	return scope, nil
}

// promptsRequire checks the access a scope needs to write and that a person
// is known; it returns the project.  A person's own prompts need only read
// access (they are theirs, not the project's); an agent needs a token that
// may write; the project's prompts need a project admin.
func (c *reqCtx) promptsRequire(scope string, agent bool) (*project.Project, error) {
	level := "read"
	if agent {
		level = "write"
	}
	if scope == promptlib.ScopeProj && !agent {
		level = "admin"
	}
	p, err := c.require(level)
	if err == nil && agent && scope == promptlib.ScopeProj && c.app.Auth != nil {
		// Agent tokens go up to write; the person behind it must be a
		// project admin.
		err = c.checkProjectAccess(p, "admin")
	}
	if err != nil {
		if scope == promptlib.ScopeProj {
			return nil, apiErr(403, "Only a project admin can change the project's prompts.", "PROMPT_PROJECT_ADMIN")
		}
		return nil, err
	}
	if c.actor() == "" {
		return nil, apiErr(401, "Sign in with an ARK account to continue.", "AUTH_REQUIRED")
	}
	return p, nil
}

func (c *reqCtx) rulesVersion(p *project.Project) string {
	cat, err := agentrules.Default()
	if err != nil {
		return ""
	}
	proj, pers, err := c.agentRulesSettings(p)
	if err != nil {
		return ""
	}
	return cat.Compose(proj, pers).Version
}

type promptsReply struct {
	Personal       []promptlib.Prompt `json:"personal"`
	Project        []promptlib.Prompt `json:"project"`
	Pins           []string           `json:"pins"` // null: the defaults
	CanEditProject bool               `json:"can_edit_project"`
	RulesVersion   string             `json:"rules_version"`
	Version        string             `json:"version"`
	Placeholders   []string           `json:"placeholders"`
	Limits         map[string]int     `json:"limits"`
}

func (c *reqCtx) promptsPayload(p *project.Project) (*pyjson.Object, error) {
	pers, err := promptlib.Load(c.promptFile(p, promptlib.ScopeUser))
	if err != nil {
		return nil, err
	}
	proj, err := promptlib.Load(c.promptFile(p, promptlib.ScopeProj))
	if err != nil {
		return nil, err
	}
	var pins []string
	if list, ok := pers.Pins[p.ID()]; ok {
		pins = append([]string{}, list...)
	}
	_, adminErr := c.require("admin")
	if adminErr != nil && c.token != nil && c.app.Auth != nil {
		// Agent tokens go up to write; what counts is the person's role.
		adminErr = c.checkProjectAccess(p, "admin")
	}
	return toObject(promptsReply{
		Personal: promptlib.Listed(pers.Prompts, promptlib.ScopeUser), Project: promptlib.Listed(proj.Prompts, promptlib.ScopeProj),
		Pins: pins, CanEditProject: adminErr == nil, RulesVersion: c.rulesVersion(p),
		Version: promptlib.LibraryVersion(pers.Prompts, proj.Prompts), Placeholders: promptlib.Placeholders,
		Limits: map[string]int{"prompts": promptlib.MaxPrompts, "pins": promptlib.MaxPins},
	})
}

func decodeInto(v any, out any) error {
	if v == nil {
		return nil
	}
	return json.Unmarshal(pyjson.Dumps(v), out)
}

func promptErr(err error) error {
	if errors.Is(err, promptlib.ErrNotFound) {
		return apiErr(404, err.Error(), "PROMPT_NOT_FOUND")
	}
	var ae *ApiError
	if errors.As(err, &ae) {
		return err
	}
	return apiErr(400, err.Error(), "PROMPT_INVALID")
}

// prompts answers GET and POST /api/prompts (people in the project).
func (c *reqCtx) prompts() error {
	if c.r.Method == "GET" || c.r.Method == "HEAD" {
		p, err := c.require("read")
		if err != nil {
			return err
		}
		out, err := c.promptsPayload(p)
		if err == nil {
			c.ok(out)
		}
		return err
	}
	if c.agentToken() != "" || c.token != nil {
		return apiErr(403, "Agents write prompts through POST /api/agent/v1/prompts.", "PROMPTS_HUMAN_ONLY")
	}
	action := pytext.Strip(pyjson.Str(orDefault(c.body.Value("action"), "")))
	var warnings []string
	var p *project.Project
	var err error
	switch action {
	case "pins":
		if p, err = c.require("read"); err != nil {
			return err
		}
		if c.actor() == "" {
			return apiErr(401, "Sign in with an ARK account to continue.", "AUTH_REQUIRED")
		}
		if !promptlib.ProjectKey(p.ID()) {
			return apiErr(400, "this project has no id to keep pins for", "PROMPT_PINS")
		}
		var pins []string
		reset := c.body.Value("pins") == nil
		if !reset {
			if err := decodeInto(c.body.Value("pins"), &pins); err != nil {
				return apiErr(400, "pins must be a list of prompt ids", "PROMPT_PINS")
			}
			if pins, err = promptlib.CleanPins(pins); err != nil {
				return apiErr(400, err.Error(), "PROMPT_PINS")
			}
		}
		_, err = promptlib.Update(c.promptFile(p, promptlib.ScopeUser), func(f *promptlib.File) error {
			if reset {
				delete(f.Pins, p.ID())
				return nil
			}
			if f.Pins == nil {
				f.Pins = map[string][]string{}
			}
			f.Pins[p.ID()] = pins
			return nil
		})
	case "save", "delete", "import":
		scope, serr := promptScope(c.body.Value("scope"))
		if serr != nil {
			return serr
		}
		if p, err = c.promptsRequire(scope, false); err != nil {
			return err
		}
		_, err = promptlib.Update(c.promptFile(p, scope), func(f *promptlib.File) error {
			switch action {
			case "save":
				var in promptlib.Input
				if err := decodeInto(c.body.Value("prompt"), &in); err != nil {
					return errors.New("prompt must be {id?, title, description, body}")
				}
				clean, warn, err := promptlib.Clean(in)
				if err != nil {
					return err
				}
				warnings = warn
				_, err = promptlib.Put(f, clean, c.actor())
				return err
			case "delete":
				return promptlib.Delete(f, pytext.Strip(pyjson.Str(orDefault(c.body.Value("id"), ""))))
			default:
				var list []promptlib.Input
				if err := decodeInto(c.body.Value("prompts"), &list); err != nil {
					return errors.New("prompts must be a list of {title, description, body}")
				}
				added, skipped, err := promptlib.Import(f, list, c.actor())
				if err == nil {
					warnings = append(warnings, strconv.Itoa(added)+" added, "+strconv.Itoa(skipped)+" already there")
				}
				return err
			}
		})
	default:
		return apiErr(400, "action must be save, delete, import or pins", "PROMPT_ACTION")
	}
	if err != nil {
		return promptErr(err)
	}
	out, err := c.promptsPayload(p)
	if err == nil {
		if warnings == nil {
			warnings = []string{}
		}
		out.Set("warnings", toAnyList(warnings))
		c.ok(out)
	}
	return err
}

func toAnyList(list []string) []any {
	out := make([]any, len(list))
	for i, s := range list {
		out[i] = s
	}
	return out
}

// agentPromptsGet answers GET /api/agent/v1/prompts.
func (c *reqCtx) agentPromptsGet(p *project.Project) error {
	out, err := c.promptsPayload(p)
	if err != nil {
		return err
	}
	all := []any{}
	for _, key := range []string{"personal", "project"} {
		if list, ok := out.Value(key).([]any); ok {
			all = append(all, list...)
		}
	}
	reply := pyjson.NewObject("prompts", all, "placeholders", out.Value("placeholders"),
		"rules_version", out.Value("rules_version"), "version", out.Value("version"),
		"can_edit_project", out.Value("can_edit_project"))
	c.ok(reply)
	return nil
}

// agentPromptsPost answers POST /api/agent/v1/prompts.
func (c *reqCtx) agentPromptsPost(path string, body *pyjson.Object) (bool, error) {
	if path != promptsAgentPath {
		return false, nil
	}
	scope, err := promptScope(body.Value("scope"))
	if err != nil {
		return true, err
	}
	p, err := c.promptsRequire(scope, true)
	if err != nil {
		return true, err
	}
	var stored promptlib.Prompt
	var warnings []string
	del := pytext.Strip(pyjson.Str(orDefault(body.Value("delete"), "")))
	_, err = promptlib.Update(c.promptFile(p, scope), func(f *promptlib.File) error {
		if del != "" {
			return promptlib.Delete(f, del)
		}
		var in promptlib.Input
		if err := decodeInto(body, &in); err != nil {
			return errors.New("send {title, description, body} (and id to replace a prompt)")
		}
		clean, warn, err := promptlib.Clean(in)
		if err != nil {
			return err
		}
		warnings = warn
		stored, err = promptlib.Put(f, clean, agentActor(c.writeActor())+" for "+c.actor())
		return err
	})
	if err != nil {
		return true, promptErr(err)
	}
	if warnings == nil {
		warnings = []string{}
	}
	if del != "" {
		c.ok(pyjson.NewObject("deleted", del, "scope", scope))
		return true, nil
	}
	stored.Scope, stored.Version = scope, promptlib.ContentVersion(stored)
	obj, err := toObject(stored)
	if err != nil {
		return true, err
	}
	c.ok(pyjson.NewObject("prompt", obj, "warnings", toAnyList(warnings), "rules_version", c.rulesVersion(p),
		"hint", "The prompt is in ARK → Home → More prompts; the person pins it to show it on Home."))
	return true, nil
}
