package chat

// POST /api/plugins/ai/chat (Go only, decision D-0003): the ai:chat
// permission of user view plugins.  The plugin's frame never sees the API
// key: the browser host forwards the plugin's messages with the user's own
// session; this route uses the user's / project's effective Assistant
// configuration (internal/chat PluginChat), returns the answer and the
// proposed changes (node:write operations the user still confirms), logs
// the call in the project's activity and stores no chat history.

import (
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	"arkstudio/go/internal/api"
	"arkstudio/go/internal/assistcfg"
	"arkstudio/go/internal/chat"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
)

// PluginChatPath is the route of the ai:chat permission.
const PluginChatPath = "/api/plugins/ai/chat"

// Limits of one request (the browser host checks the same ones first).
const (
	pluginMaxMessages     = 20
	pluginMaxMessageChars = 8000
	pluginMaxTotalChars   = 48000
	pluginMaxSystemChars  = 4000
)

var pluginIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,28}[a-z0-9]$`)

// windowLimiter allows `limit` calls per key within a sliding `window`.
type windowLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	now    func() time.Time
	hits   map[string][]time.Time
}

func newWindowLimiter(limit int, window time.Duration) *windowLimiter {
	return &windowLimiter{limit: limit, window: window, now: time.Now, hits: map[string][]time.Time{}}
}

func (l *windowLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	kept := l.hits[key][:0]
	for _, at := range l.hits[key] {
		if now.Sub(at) < l.window {
			kept = append(kept, at)
		}
	}
	if len(kept) >= l.limit {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	if len(l.hits) > 4096 { // forget idle keys
		for k, v := range l.hits {
			if len(v) == 0 || now.Sub(v[len(v)-1]) >= l.window {
				delete(l.hits, k)
			}
		}
	}
	return true
}

// pluginChatLimit: 20 calls per minute per user and plugin (the browser
// also allows at most 3 per 10 s per plugin frame).
var pluginChatLimit = newWindowLimiter(20, time.Minute)

// pluginChatRequest validates the body.
func pluginChatRequest(body *pyjson.Object) (chat.PluginRequest, error) {
	var req chat.PluginRequest
	plugin, _ := body.Value("plugin").(string)
	if !pluginIDPattern.MatchString(plugin) || strings.HasPrefix(plugin, "u-") {
		return req, api.Err(400, "plugin must be a plugin id", "")
	}
	req.Plugin = plugin
	list, ok := body.Value("messages").([]any)
	if !ok || len(list) == 0 || len(list) > pluginMaxMessages {
		return req, api.Err(400, "messages must be a list of 1-20 messages", "")
	}
	total := 0
	for i, item := range list {
		m, ok := item.(*pyjson.Object)
		if !ok {
			return req, api.Err(400, "each message needs a role and content", "")
		}
		role, _ := m.Value("role").(string)
		content, _ := m.Value("content").(string)
		content = pytext.Strip(content)
		if (role != "user" && role != "assistant") || content == "" {
			return req, api.Err(400, "each message needs a role (user or assistant) and content", "")
		}
		if i == len(list)-1 && role != "user" {
			return req, api.Err(400, "the last message must be the user's", "")
		}
		n := pytext.Len(content)
		if n > pluginMaxMessageChars {
			return req, api.Err(400, "message is too long", "")
		}
		total += n
		req.Messages = append(req.Messages, pyjson.NewObject("role", role, "content", content))
	}
	if total > pluginMaxTotalChars {
		return req, api.Err(400, "messages are too long", "")
	}
	if v := body.Value("system"); v != nil {
		system, ok := v.(string)
		if !ok || pytext.Len(system) > pluginMaxSystemChars {
			return req, api.Err(400, "system must be text of at most 4000 characters", "")
		}
		req.System = pytext.Strip(system)
	}
	req.Tools = true
	if v, ok := body.Value("tools").(bool); ok {
		req.Tools = v
	}
	return req, nil
}

// PluginChat answers POST /api/plugins/ai/chat.  actor is the request's
// write actor ("human:plugin:<id>" for the browser host).
func (h *Handlers) PluginChat(c api.Ctx, body *pyjson.Object, actor string) error {
	p, err := c.Require("write")
	if err != nil {
		return err
	}
	req, err := pluginChatRequest(body)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(actor, "agent:") {
		actor = "human:plugin:" + req.Plugin
	}
	config, err := h.Effective(c)
	if err != nil {
		return err
	}
	if pytext.Lower(assistcfg.EnvValue("ARK_CHAT_MODE")) == "local" || !pyjson.Truthy(config.Value("api_key")) {
		return api.Err(400, "No AI model is set up for the ARK Assistant (Settings › Assistant).", "AI_NOT_CONFIGURED")
	}
	// Only calls that reach the model count.
	if !pluginChatLimit.allow(c.Actor() + "\x00" + req.Plugin) {
		return api.Err(429, "Too many AI requests from this plugin; try again in a minute.", "RATE_LIMITED")
	}
	env := &chat.Env{Ctx: c.RequestContext(), Store: p.Store, ProjectID: p.ID(), ProjectName: p.Name(), ProjectPath: p.Path, Config: config,
		AgentToken: h.AgentToken(), Who: c.Who(p)}
	result, err := env.PluginChat(req)
	if err != nil {
		var pe *chat.PyError
		if errors.Is(err, chat.ErrNoModel) {
			return api.Err(400, "No AI model is set up for the ARK Assistant (Settings › Assistant).", "AI_NOT_CONFIGURED")
		}
		if errors.As(err, &pe) && pe.Type == "DeepSeekError" {
			return api.Err(502, "The AI model did not answer: "+pytext.Prefix(pe.Message, 300), "AI_FAILED")
		}
		return chatErr(err)
	}
	proposals, _ := result.Value("proposals").([]any)
	reads, _ := result.Value("reads").([]any)
	if err := p.Store.LogActivity(c.Context(), actor, "plugin.ai.chat", nil, pyjson.NewObject(
		"plugin", req.Plugin, "model", result.Value("model"), "messages", int64(len(req.Messages)),
		"proposals", int64(len(proposals)), "reads", int64(len(reads)))); err != nil {
		return err
	}
	c.OK(result)
	return nil
}
