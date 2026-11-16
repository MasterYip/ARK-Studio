package server

import (
	"sort"

	"arkstudio/go/internal/platform/clock"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/settings"
	"arkstudio/go/internal/store"
)

// PATCH /api/project and /api/project/settings (App.update_project,
// App.update_project_settings).

func (c *reqCtx) projectPayload(p *project.Project) (*pyjson.Object, error) {
	payload, err := p.Store.ProjectPayload(c.ctx)
	if err != nil {
		return nil, err
	}
	summary, err := p.Summary()
	if err != nil {
		return nil, err
	}
	return payload.Set("summary", summary), nil
}

func (c *reqCtx) writeManifest(p *project.Project, encoded []byte) error {
	return store.WriteManifest(p.Path, encoded)
}

// updateProject is App.update_project.
func (c *reqCtx) updateProject(body *pyjson.Object) error {
	p, err := c.require("admin")
	if err != nil {
		return err
	}
	changed := false
	var updated string
	encoded, err := p.Manifest.Mutate(func(m *pyjson.Object) error {
		for _, key := range []string{"name", "description", "accent"} {
			v, ok := body.Get(key)
			if !ok || v == nil {
				continue
			}
			value := pytext.Strip(pyjson.Str(v))
			if key == "name" && value == "" {
				return apiErr(400, "Project name cannot be empty", "")
			}
			m.Set(key, pytext.Prefix(value, 600))
			changed = true
		}
		if changed {
			updated = clock.UTCNow()
			m.Set("updated", updated)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if changed {
		if err := c.writeManifest(p, encoded); err != nil {
			return err
		}
		fields := make([]any, 0, body.Len())
		for _, k := range body.Keys() {
			fields = append(fields, k)
		}
		if err := p.Store.LogActivity(c.ctx, "human:ui", "project.update", nil, pyjson.NewObject("fields", fields)); err != nil {
			return err
		}
		if err := c.app.Org.TouchProject(c.ctx, p.ID(), p.Name(), updated); err != nil {
			return err
		}
	}
	payload, err := c.projectPayload(p)
	if err != nil {
		return err
	}
	c.ok(payload)
	return nil
}

func sortedKeys(o *pyjson.Object) []any {
	keys := append([]string(nil), o.Keys()...)
	sort.Strings(keys)
	out := make([]any, len(keys))
	for i, k := range keys {
		out[i] = k
	}
	return out
}

// updateProjectSettings is App.update_project_settings.
func (c *reqCtx) updateProjectSettings(body *pyjson.Object) error {
	p, err := c.require("admin")
	if err != nil {
		return err
	}
	var next *pyjson.Object
	var updated string
	encoded, err := p.Manifest.Mutate(func(m *pyjson.Object) error {
		s, err := settings.SettingsPatch(m, body, settings.PresetIDs())
		if err != nil {
			if se, ok := err.(*settings.Error); ok {
				return apiErr(400, se.Message, "")
			}
			return err
		}
		next = s
		updated = clock.UTCNow()
		m.Set("settings", s).Set("updated", updated)
		return nil
	})
	if err != nil {
		return err
	}
	if err := c.writeManifest(p, encoded); err != nil {
		return err
	}
	if err := p.Store.LogActivity(c.ctx, "human:ui", "project.settings", nil,
		pyjson.NewObject("sections", sortedKeys(next))); err != nil {
		return err
	}
	userID, err := c.app.Org.UserIDFor(c.ctx, c.actor())
	if err != nil {
		return err
	}
	if err := c.app.Org.Log(c.ctx, userID, "project.settings", p.ID(), pyjson.NewObject("sections", sortedKeys(body))); err != nil {
		return err
	}
	current, err := settings.ProjectSettings(p.Manifest.Snapshot())
	if err != nil {
		return err
	}
	summary, err := p.Summary()
	if err != nil {
		return err
	}
	c.ok(pyjson.NewObject("settings", current, "summary", summary, "updated_at", updated))
	return nil
}
