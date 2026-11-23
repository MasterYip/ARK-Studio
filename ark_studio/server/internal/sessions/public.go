package sessions

import (
	"math"
	"sort"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/providers"
)

// Preset is SessionManager.preset(): the preset with a personal command
// override applied.
func (m *Manager) Preset(id string, overrides *pyjson.Object) (*pyjson.Object, error) {
	preset := m.presetByID(id)
	if preset == nil {
		if id == "" {
			id = "(missing)"
		}
		return nil, serr("unknown platform: "+id, 400, "")
	}
	if overrides != nil {
		if o, ok := overrides.Value(pyjson.Str(preset.Value("id"))).(*pyjson.Object); ok {
			if cmd := o.Value("command"); pyjson.Truthy(cmd) {
				return preset.Copy().Set("command", stringsOf(cmd)), nil
			}
		}
	}
	return preset, nil
}

// AgentOverrides is SessionManager.agent_overrides: the resolved "presets"
// map of one account (nil on any error, like Python).
func AgentOverrides(h *Hooks, actor string) *pyjson.Object {
	if h == nil || h.AgentConfig == nil {
		return pyjson.NewObject()
	}
	resolved, err := h.AgentConfig(actor)
	if err != nil || resolved == nil {
		return pyjson.NewObject()
	}
	if p, ok := resolved.Value("presets").(*pyjson.Object); ok {
		return p
	}
	return pyjson.NewObject()
}

func presetStrings(p *pyjson.Object, key string) []string { return stringsOf(p.Value(key)) }

// PresetsPublic is SessionManager.presets_public.
func (m *Manager) PresetsPublic(h *Hooks, actor string) []*pyjson.Object {
	overrides := AgentOverrides(h, actor)
	var out []*pyjson.Object
	for _, base := range m.Presets {
		preset, _ := m.Preset(pyjson.Str(base.Value("id")), overrides)
		available := providers.Available(preset)
		override := objOr(overrides.Value(pyjson.Str(preset.Value("id"))))
		disabled := false
		if v, ok := override.Get("enabled"); ok && v == false {
			available, disabled = false, true
		}
		modes := presetStrings(preset, "permission_modes")
		labels := pyjson.NewObject()
		for _, mode := range modes {
			labels.Set(mode, permissionLabels[mode])
		}
		var approval []string
		for _, mode := range approvalModes {
			if contains(modes, mode) {
				approval = append(approval, mode)
			}
		}
		env := objOr(override.Value("env"))
		envKeys := append([]string(nil), env.Keys()...)
		sort.Strings(envKeys)
		cmd := providers.Command(preset)
		item := pyjson.NewObject(
			"id", preset.Value("id"), "label", preset.Value("label"), "description", preset.Value("description"),
			"available", available, "prompt_mode", preset.Value("prompt_mode"), "followup", preset.Value("followup"),
			"models", preset.Value("models"), "efforts", preset.Value("efforts"),
			"access_modes", preset.Value("permission_modes"), "permission_modes", preset.Value("permission_modes"),
			"permission_labels", labels, "default_permission_mode", preset.Value("default_permission_mode"),
			"supports_approval", preset.Value("supports_approval"), "approval_modes", strList(approval),
			"unsupported_permission_modes", preset.Value("unsupported_permission_modes"),
			"approval_protocol", ApprovalProtocol, "demo", preset.Value("demo"), "program", cmd[0],
			"command_source", or(override.Value("command_source"), "global"),
			"personal_override", override.Len() > 0, "configured_env", strList(envKeys),
			"agent_options", agentOptionsPublic(h, pyjson.Str(preset.Value("id"))))
		if !available {
			if disabled {
				// "project" or "personal": where it was switched off.
				item.Set("disabled", pyjson.Str(or(override.Value("disabled_by"), "personal")))
				item.Set("unavailable_reason", "disabled in "+pyjson.Str(or(override.Value("disabled_by"), "personal"))+" settings")
			} else {
				item.Set("unavailable_reason", cmd[0]+" was not found")
			}
		}
		out = append(out, item)
	}
	if m.Catalog != nil {
		func() {
			defer func() { _ = recover() }()
			out = providers.PresetsWithProviderStatus(out, m.Catalog)
		}()
	}
	return out
}

// approvalPublic is SessionManager.approval_public.
func approvalPublic(r *pyjson.Object) *pyjson.Object {
	status := pyjson.Str(or(r.Value("status"), "pending"))
	return pyjson.NewObject("id", r.Value("id"), "kind", or(r.Value("kind"), "other"), "tool", or(r.Value("tool"), ""),
		"command", or(r.Value("command"), ""), "path", or(r.Value("path"), ""),
		"description", or(r.Value("description"), ""), "input", r.Value("input"),
		"protocol", or(r.Value("protocol"), ApprovalProtocol), "status", status,
		"decision", or(r.Value("decision"), ""), "scope", or(r.Value("scope"), ""),
		"auto", pyjson.Truthy(r.Value("auto")), "message", or(r.Value("message"), ""),
		"mode", or(r.Value("mode"), ""), "requested_at", r.Value("requested_at"),
		"decided_at", r.Value("decided_at"), "decided_by", or(r.Value("decided_by"), ""),
		"pending", status == "pending", "active", status == "pending")
}

func objects(v any) []*pyjson.Object {
	list, _ := v.([]any)
	out := make([]*pyjson.Object, 0, len(list))
	for _, x := range list {
		if o, ok := x.(*pyjson.Object); ok {
			out = append(out, o)
		}
	}
	return out
}

// approvalState is SessionManager._approval_state.
func approvalState(meta *pyjson.Object) (items []any, pending int64, rules []any) {
	raw := objects(meta.Value("approvals"))
	var pend []*pyjson.Object
	for _, r := range raw {
		if r.Value("status") == "pending" {
			pend = append(pend, r)
		}
	}
	list := make([]*pyjson.Object, 0, len(pend))
	seen := map[string]bool{}
	for _, r := range pend {
		list = append(list, approvalPublic(r))
		seen[strOr(r.Value("id"))] = true
	}
	budget := maxApprovalItems - len(list)
	if budget < 0 {
		budget = 0
	}
	if budget > 0 {
		tail := raw
		if len(tail) > budget {
			tail = tail[len(tail)-budget:]
		}
		for _, r := range tail {
			key := strOr(r.Value("id"))
			if r.Value("status") == "pending" || (key != "" && seen[key]) {
				continue
			}
			seen[key] = true
			list = append(list, approvalPublic(r))
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		return strOr(list[i].Value("requested_at")) < strOr(list[j].Value("requested_at"))
	})
	items = make([]any, len(list))
	for i, x := range list {
		items[i] = x
	}
	rules = []any{}
	for _, r := range objects(meta.Value("approval_rules")) {
		rules = append(rules, r.Copy())
	}
	return items, int64(len(pend)), rules
}

// Public is SessionManager.public(session, detail).
func (m *Manager) Public(s *Session, detail bool) *pyjson.Object {
	m.Busy(s)
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.publicLocked(s, detail)
}

func (m *Manager) publicLocked(s *Session, detail bool) *pyjson.Object {
	meta := deepCopy(s.Meta).(*pyjson.Object)
	status, rawStatus := m.displayStatus(meta)
	access := pyjson.Str(or(meta.Value("access_mode"), "read_only"))
	if !contains(permissionModes, access) {
		access = "read_only"
	}
	allowed := append([]string(nil), permissionModes...)
	supports := false
	if preset := m.presetByID(strOr(meta.Value("platform"))); preset != nil {
		allowed = append([]string(nil), presetStrings(preset, "permission_modes")...)
		supports = pyjson.Truthy(preset.Value("supports_approval"))
	}
	items, pending, rules := approvalState(meta)
	history := []any{}
	for _, e := range objects(meta.Value("permission_history")) {
		mode := strOr(e.Value("mode"))
		if !contains(permissionModes, mode) {
			continue
		}
		history = append(history, pyjson.NewObject("mode", mode, "label", permissionLabels[mode], "at", e.Value("at"),
			"turn", intOr(e.Value("turn"), 0), "source", or(e.Value("source"), "")))
	}
	steer := []any{}
	for _, r := range objects(meta.Value("steer_queue")) {
		steer = append(steer, steerPublic(r))
	}
	var worktree any
	if wt, ok := meta.Value("worktree").(*pyjson.Object); ok && pyjson.Truthy(wt.Value("path")) {
		pub := wt.Copy()
		others := m.activeWorktreeSessionsLocked(pyjson.Str(wt.Value("path")), s.ID)
		pub.Set("other_active_session_ids", strList(others)).Set("active_count", int64(len(others)+1)).
			Set("conflict", len(others) > 0).Set("shared", len(others) > 0)
		if len(others) > 0 {
			pub.Set("status", "conflict")
		}
		worktree = pub
	}
	var approvalAllowed []string
	for _, mode := range approvalModes {
		if contains(allowed, mode) {
			approvalAllowed = append(approvalAllowed, mode)
		}
	}
	followup := pyjson.Truthy(meta.Value("followup"))
	channel := "next_turn"
	if followup {
		channel = "stdin"
	}
	counts := meta.Value("counts")
	if !pyjson.Truthy(counts) {
		counts = pyjson.NewObject("stdout", int64(0), "stderr", int64(0))
	}
	linked := meta.Value("linked_units")
	if !pyjson.Truthy(linked) {
		linked = []any{}
	}
	id := meta.Value("id")
	ids := pyjson.Str(id)
	payload := pyjson.NewObject(
		"id", id, "title", meta.Value("title"), "platform", meta.Value("platform"),
		"platform_label", meta.Value("platform_label"), "model", or(meta.Value("model"), ""),
		"effort", or(meta.Value("effort"), ""), "access_mode", access, "access_label", permissionLabels[access],
		"permission_mode", access, "permission_label", permissionLabels[access],
		"permission_description", permissionDescriptions[access], "permission_modes", strList(allowed),
		"permission_history", history, "turn", intOr(meta.Value("turn"), 1),
		"approvals", pyjson.NewObject("items", items, "pending", pending, "rules", rules, "supported", supports,
			"protocol", ApprovalProtocol),
		"agent", or(meta.Value("agent"), ""), "native_session_id", or(meta.Value("native_session_id"), ""),
		"linked_units", linked, "owner", or(meta.Value("actor"), ""), "shared", pyjson.Truthy(meta.Value("shared")),
		"task_ref", or(meta.Value("task_ref"), ""), "parent_id", or(meta.Value("parent_id"), ""),
		"relation", or(meta.Value("relation"), ""), "side_chat_of", or(meta.Value("side_chat_of"), ""),
		"forked_from", or(meta.Value("forked_from"), ""), "depth", intOr(meta.Value("depth"), 0),
		"status", status, "raw_status", rawStatus, "cwd", or(meta.Value("cwd"), "."),
		"linux_account", or(meta.Value("linux_account"), ""), "worktree", worktree,
		"allow_shared_worktree", pyjson.Truthy(meta.Value("allow_shared_worktree")),
		"created_at", meta.Value("created_at"), "started_at", meta.Value("started_at"),
		"last_activity_at", meta.Value("last_activity_at"), "ended_at", meta.Value("ended_at"),
		"exit_code", meta.Value("exit_code"), "pid", meta.Value("pid"), "error", or(meta.Value("error"), ""),
		"notice", or(meta.Value("notice"), ""), "project", objOr(meta.Value("project")), "steer_queue", steer,
		"capabilities", pyjson.NewObject("followup", followup, "stdin", followup, "steer", true,
			"steer_channel", channel, "context", true, "access_modes", strList(allowed),
			"permission_modes", strList(allowed), "supports_approval", supports,
			"approval_modes", strList(approvalAllowed), "approval_protocol", ApprovalProtocol,
			"side_chat", true, "full_access", contains(allowed, "full_access"), "read_only_project", true,
			"streams", []any{"stdout", "stderr"}),
		"counts", counts,
		"logs", pyjson.NewObject("stdout", "/api/agent-sessions/"+ids+"/logs?stream=stdout",
			"stderr", "/api/agent-sessions/"+ids+"/logs?stream=stderr"),
		"artifacts", pyjson.NewObject("endpoint", "/api/agent-sessions/"+ids+"/artifacts",
			"download", "/api/agent-sessions/"+ids+"/artifacts/download"),
		"context", pyjson.NewObject("api", objOr(meta.Value("api")), "file", "ark-context.json", "read_only", true))
	if detail {
		payload.Set("prompt", or(meta.Value("prompt"), "")).Set("read_token", or(meta.Value("read_token"), "")).
			Set("command_preview", or(meta.Value("command_preview"), "")).Set("workspace", s.Workspace()).
			Set("read_env", objOr(meta.Value("read_env")))
	}
	if pyjson.Truthy(meta.Value("closed")) {
		payload.Set("closed", true)
	}
	if m := meta.Value("fork_method"); m != nil {
		payload.Set("fork_method", m)
	}
	if waitingStreamLocked(s) && s.busy.Any() {
		payload.Set("busy", s.busy.public()) // between turns, sub-agents or background tasks still run
	}
	if pyjson.Truthy(meta.Value("stream_input")) {
		// A long-lived Claude Code process: messages go to its stdin at once.
		payload.Value("capabilities").(*pyjson.Object).Set("stream_input", true).
			Set("turn_state", or(meta.Value("turn_state"), ""))
	}
	return payload
}

func steerPublic(r *pyjson.Object) *pyjson.Object {
	return pyjson.NewObject("id", r.Value("id"), "text", or(r.Value("text"), ""), "status", or(r.Value("status"), "queued"),
		"channel", or(r.Value("channel"), "next_turn"), "created_at", r.Value("created_at"),
		"delivered_at", r.Value("delivered_at"), "edited_at", r.Value("edited_at"), "error", or(r.Value("error"), ""))
}

// invertTime is _invert_time.
func invertTime(v any) float64 {
	if t, ok := parseISO(v); ok {
		return -float64(t.UnixNano()) / 1e9
	}
	return math.Inf(1)
}

// List is SessionManager.list().
func (m *Manager) List() *pyjson.Object {
	sessions := m.all()
	items := make([]*pyjson.Object, 0, len(sessions))
	for _, s := range sessions {
		items = append(items, m.Public(s, false))
	}
	order := func(o *pyjson.Object) int {
		if n, ok := statusOrder[pyjson.Str(o.Value("status"))]; ok {
			return n
		}
		return 9
	}
	sort.SliceStable(items, func(i, j int) bool {
		oi, oj := order(items[i]), order(items[j])
		if oi != oj {
			return oi < oj
		}
		return invertTime(items[i].Value("last_activity_at")) < invertTime(items[j].Value("last_activity_at"))
	})
	counts := pyjson.NewObject()
	list := make([]any, len(items))
	for i, item := range items {
		st := pyjson.Str(item.Value("status"))
		n, _ := counts.Value(st).(int64)
		counts.Set(st, n+1)
		list[i] = item
	}
	groups := m.Groups()
	return pyjson.NewObject("sessions", list, "counts", counts, "total", int64(len(items)),
		"groups", groups.Value("groups"), "group_counts", groups.Value("counts"))
}

// effortChoices is SessionManager.effort_choices.
func (m *Manager) effortChoices(preset *pyjson.Object, model string) []string {
	if e := presetStrings(preset, "efforts"); len(e) > 0 {
		return e
	}
	if m.Catalog == nil {
		return nil
	}
	var caps *pyjson.Object
	func() {
		defer func() { _ = recover() }()
		caps = objOr(m.Catalog.MetadataFor(pyjson.Str(preset.Value("id"))).Value("capabilities"))
	}()
	if caps == nil {
		return nil
	}
	options := objects(caps.Value("model_options"))
	var match *pyjson.Object
	for _, item := range options {
		if (model != "" && store_eq(item.Value("id"), model)) || (model == "" && pyjson.Truthy(item.Value("default"))) {
			match = item
			break
		}
	}
	if match != nil && pyjson.Truthy(match.Value("efforts")) {
		return stringsOf(match.Value("efforts"))
	}
	return stringsOf(caps.Value("efforts"))
}

func store_eq(a any, s string) bool { x, ok := a.(string); return ok && x == s }

func (m *Manager) checkEffort(preset *pyjson.Object, model, effort string) error {
	if effort == "" {
		return nil
	}
	choices := m.effortChoices(preset, model)
	if len(choices) == 0 {
		return serr("this platform does not expose a reasoning level", 400, "")
	}
	if !contains(choices, effort) {
		return serr("unknown reasoning level '"+effort+"'; expected one of "+joinComma(choices), 400, "")
	}
	return nil
}

func joinComma(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
