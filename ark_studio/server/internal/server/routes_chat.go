package server

// Assistant chat (App.chat_state / chat_send / chat_stream, chat.py).

import (
	"context"

	"arkstudio/go/internal/api"
	chatapi "arkstudio/go/internal/api/chat"
	"arkstudio/go/internal/assistcfg"
	"arkstudio/go/internal/chat"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/project"
)

// adoptLegacyChat gives a project's pre-v10 Assistant rows (empty user) to
// its owner once (T-0028, store.AdoptLegacyChat; owner from
// org.ProjectOwner).  Local mode keeps them as the local user's thread and
// adopts nothing.
func (a *App) adoptLegacyChat(ctx context.Context, p *project.Project) error {
	if a.Auth == nil || a.Org == nil || p == nil {
		return nil
	}
	_, err := p.Store.AdoptLegacyChat(ctx, func() (string, error) { return a.Org.ProjectOwner(ctx, p.ID()) })
	return err
}

var chatPorted = []PortedRoute{
	{"exact:/api/chat", "GET,HEAD"},
	{"exact:/api/chat", "POST"},
	{"exact:/api/chat/status", "GET,HEAD"},
	{"exact:/api/chat/stream", "POST"},
}

// effectiveAssistant is App.effective_assistant().
func (c *reqCtx) effectiveAssistant() (*pyjson.Object, error) {
	summary := pyjson.NewObject()
	p, err := c.currentProject()
	if err != nil {
		return nil, err
	}
	if p != nil {
		s, err := p.Summary()
		if err != nil {
			return nil, err
		}
		summary = s
	}
	global, err := assistcfg.Load()
	if err != nil {
		return nil, err
	}
	return c.app.Org.ResolveAssistant(c.ctx, summary, c.actor(), global)
}

// effectiveAssistantPublic is App.effective_assistant_public().
func (c *reqCtx) effectiveAssistantPublic() (*pyjson.Object, error) {
	resolved, err := c.effectiveAssistant()
	if err != nil {
		return nil, err
	}
	key := pyjson.Str(resolved.Value("api_key"))
	resolved.Delete("api_key")
	resolved.Set("configured", key != "")
	resolved.Set("has_key", key != "")
	hint := resolved.Value("key_hint")
	if !pyjson.Truthy(hint) {
		hint = ""
	}
	resolved.Set("key_hint", hint)
	return resolved, nil
}

func (c *reqCtx) chatHandlers() *chatapi.Handlers {
	return &chatapi.Handlers{
		Effective:       func(api.Ctx) (*pyjson.Object, error) { return c.effectiveAssistant() },
		EffectivePublic: func(api.Ctx) (*pyjson.Object, error) { return c.effectiveAssistantPublic() },
		AgentToken:      c.app.AgentToken,
		LocalMode:       c.app.Auth == nil,
		Scope:           func(_ api.Ctx, p *project.Project) *chat.Scope { return c.assistantScope(p) },
	}
}

// chatStream is POST /api/chat/stream (internal/api/chat).
func (c *reqCtx) chatStream() error { return c.chatHandlers().Stream(c) }
