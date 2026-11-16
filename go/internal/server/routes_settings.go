package server

// Personal and effective settings: GET/POST /api/personal/settings, GET
// /api/settings/effective, GET/POST /api/assistant/config and POST
// /api/assistant/config/test (Handler._api_get / do_POST,
// App.effective_settings_payload, Handler._assistant_config_payload).

import (
	"errors"

	"arkstudio/go/internal/assistcfg"
	"arkstudio/go/internal/chat"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pytext"
	"arkstudio/go/internal/settings"
	"arkstudio/go/internal/store"
)

var settingsPorted = []PortedRoute{
	{"exact:/api/personal/settings", "GET,HEAD"},
	{"exact:/api/personal/settings", "POST"},
	{"exact:/api/settings/effective", "GET,HEAD"},
	{"exact:/api/assistant/config", "GET,HEAD"},
	{"exact:/api/assistant/config", "POST"},
	{"exact:/api/assistant/config/test", "POST"},
}

func init() { Ported = append(Ported, settingsPorted...) }

// uncaughtOrg is an OrgError that no handler converts to ApiError: Python's
// catch-all answers 500 "Internal error: <message>" without a code.
func uncaughtOrg(err error) error {
	var oe *org.Error
	if errors.As(err, &oe) {
		return &store.InternalError{Message: oe.Message}
	}
	return orgInternal(err)
}

// effectiveSettingsPayload is App.effective_settings_payload (its
// OrgErrors are uncaught at every caller).
func (c *reqCtx) effectiveSettingsPayload() (*pyjson.Object, error) {
	out, err := c.effectiveSettings()
	return out, uncaughtOrg(err)
}

func (c *reqCtx) effectiveSettings() (*pyjson.Object, error) {
	p, err := c.currentProject()
	if err != nil {
		return nil, err
	}
	actor := c.actor()
	var role any = "owner"
	if c.app.Auth != nil {
		r, ok, err := c.app.Org.RoleFor(c.ctx, actor)
		if err != nil {
			return nil, err
		}
		role = nil
		if ok {
			role = r
		}
	}
	var projectInfo any
	if p != nil {
		perm, err := c.projectPermission()
		if err != nil {
			return nil, err
		}
		projectInfo = pyjson.NewObject("id", p.ID(), "name", p.Name(), "permission", perm)
	}
	assistant, err := c.effectiveAssistantPublic()
	if err != nil {
		return nil, err
	}
	agents, err := c.effectiveAgentsPublic()
	if err != nil {
		return nil, err
	}
	personalAssistant, err := c.app.Org.PersonalSettingsPublic(c.ctx, actor, "assistant")
	if err != nil {
		return nil, err
	}
	personalAgents, err := c.app.Org.PersonalSettingsPublic(c.ctx, actor, "agents")
	if err != nil {
		return nil, err
	}
	projectSettings := pyjson.NewObject()
	if p != nil {
		if projectSettings, err = settings.ProjectSettings(p.Manifest.Snapshot()); err != nil {
			return nil, err
		}
	}
	return pyjson.NewObject("user", actor, "role", role, "project", projectInfo,
		"assistant", assistant, "agents", agents,
		"personal", pyjson.NewObject("assistant", personalAssistant, "agents", personalAgents),
		"project_settings", projectSettings), nil
}

// assistantConfigPayload is Handler._assistant_config_payload (uncaught
// OrgErrors, like effectiveSettingsPayload).
func (c *reqCtx) assistantConfigPayload() (*pyjson.Object, error) {
	out, err := c.assistantConfig()
	return out, uncaughtOrg(err)
}

func (c *reqCtx) assistantConfig() (*pyjson.Object, error) {
	resolved, err := c.effectiveAssistant()
	if err != nil {
		return nil, err
	}
	personal, err := c.app.Org.PersonalSettingsPublic(c.ctx, c.actor(), "assistant")
	if err != nil {
		return nil, err
	}
	hasKey := pyjson.Truthy(resolved.Value("api_key"))
	sources, _ := resolved.Value("sources").(*pyjson.Object)
	if sources == nil || sources.Len() == 0 {
		sources = pyjson.NewObject()
	}
	config := pyjson.NewObject("configured", hasKey, "base_url", resolved.Value("base_url"),
		"model", resolved.Value("model"), "reasoning_effort", resolved.Value("reasoning_effort"),
		"timeout", resolved.Value("timeout"), "has_key", hasKey,
		"key_hint", orAny(resolved.Value("key_hint"), ""),
		"source", orAny(sources.Value("api_key"), "none"), "sources", sources,
		"api_key_env", orAny(resolved.Value("api_key_env"), ""),
		"project_configured", pyjson.Truthy(resolved.Value("project_configured")),
		"personal_config_required", pyjson.Truthy(resolved.Value("personal_config_required")),
		"path", assistcfg.ConfigPath())
	effective, err := c.effectiveSettingsPayload()
	if err != nil {
		return nil, err
	}
	return pyjson.NewObject("config", config, "personal", personal, "effective", effective), nil
}

// settingsGet answers the GET routes of this module.
func (c *reqCtx) settingsGet(path string) (bool, error) {
	switch path {
	case "/api/personal/settings":
		scope := c.query.First("scope", "assistant")
		if scope == "environment" {
			out, err := c.app.Org.PersonalEnvironmentPublic(c.ctx, c.actor())
			if err == nil {
				c.ok(pyjson.NewObject("scope", scope, "settings", out))
			}
			return true, err
		}
		out, err := c.app.Org.PersonalSettingsPublic(c.ctx, c.actor(), scope)
		if err != nil {
			return true, err
		}
		effective, err := c.effectiveSettingsPayload()
		if err == nil {
			c.ok(pyjson.NewObject("scope", scope, "settings", out, "effective", effective))
		}
		return true, err
	case "/api/settings/effective":
		out, err := c.effectiveSettingsPayload()
		if err == nil {
			c.ok(out)
		}
		return true, err
	case "/api/assistant/config":
		out, err := c.assistantConfigPayload()
		if err == nil {
			c.ok(out)
		}
		return true, err
	}
	return false, nil
}

// personalSettingsPost is POST /api/personal/settings (in do_POST's org block).
func (c *reqCtx) personalSettingsPost(path string, body *pyjson.Object) (bool, error) {
	if path != "/api/personal/settings" {
		return false, nil
	}
	scope := c.query.First("scope", "assistant")
	if v := body.Value("scope"); pyjson.Truthy(v) {
		scope = pyjson.Str(v)
	}
	scope = pytext.Lower(pytext.Strip(scope))
	var out *pyjson.Object
	var err error
	switch scope {
	case "assistant":
		out, err = c.app.Org.SavePersonalAssistant(c.ctx, c.actor(), body)
	case "agents":
		out, err = c.app.Org.SavePersonalAgents(c.ctx, c.actor(), body, settings.PresetIDs())
	case "environment":
		out, err = c.app.Org.SavePersonalEnvironment(c.ctx, c.actor(), body)
		if err == nil {
			c.ok(pyjson.NewObject("scope", scope, "settings", out))
		}
		return true, orgInternal(err)
	default:
		return true, apiErr(400, "scope must be assistant, agents or environment", "INVALID_SCOPE")
	}
	if err != nil {
		return true, orgInternal(err)
	}
	effective, err := c.effectiveSettingsPayload()
	if err == nil {
		c.ok(pyjson.NewObject("scope", scope, "settings", out, "effective", effective))
	}
	return true, err
}

// orgInternal maps a Python exception outside OrgError (org.IsInternal) to
// the generic 500 path.
func orgInternal(err error) error {
	if msg, ok := org.IsInternal(err); ok {
		return &store.InternalError{Message: msg}
	}
	return err
}

// assistantConfigPost answers POST /api/assistant/config(/test), which do_POST
// reaches after `store = self.app.require().store`.
func (c *reqCtx) assistantConfigPost(path string, body *pyjson.Object) (bool, error) {
	if path != "/api/assistant/config" && path != "/api/assistant/config/test" {
		return false, nil
	}
	if _, err := c.require("read"); err != nil {
		return true, err
	}
	if path == "/api/assistant/config" {
		if _, err := c.app.Org.SavePersonalAssistant(c.ctx, c.actor(), body); err != nil {
			return true, orgInternal(err)
		}
		out, err := c.assistantConfigPayload()
		if err == nil {
			c.ok(out)
		}
		return true, err
	}
	config, err := c.effectiveAssistant()
	if err != nil {
		return true, uncaughtOrg(err)
	}
	if !pyjson.Truthy(config.Value("api_key")) {
		return true, apiErr(409, "No ARK assistant credential is configured. Set a personal "+
			"API key in Settings → Personal → Assistant, or ask a project "+
			"admin to configure the project assistant.", "PERSONAL_CONFIG_REQUIRED")
	}
	out, err := chat.TestConnection(c.ctx, config)
	if err != nil {
		var pe *chat.PyError
		if errors.As(err, &pe) && chat.IsValueError(pe) {
			return true, apiErr(400, pe.Message, "")
		}
		return true, err
	}
	c.ok(out)
	return true, nil
}
