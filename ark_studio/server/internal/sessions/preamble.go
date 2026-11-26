package sessions

// What an ARK-launched agent is told about ARK itself (2026-11-24).  A
// session ARK launches gets its session facts and its own agent token in
// the environment (buildEnv), but the agent learns of them only if it is
// told.  Without that, people pasted the Home starter prompt with their
// personal token into the session, and the agent used that token (and
// saved it to ~/.ark/tokens) instead of the session's own: its writes then
// showed up as a second, unrelated "agent token" agent.  So ARK adds a
// session block and the person's composed core rules: Claude Code as an
// appended system prompt on every run, other agents ahead of the first
// message.  Pasted personal tokens are taken out of messages to the agent.

import (
	"regexp"
	"strconv"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/sessionstart"
)

// PastedTokenRe is a personal ARK agent token in a message.
var PastedTokenRe = regexp.MustCompile(`\barka_[A-Za-z0-9_-]{16,}`)

// PastedTokenNote replaces a pasted token in a message to a launched agent.
const PastedTokenNote = "[token removed: in this session use $ARKT]"

// WithoutPastedTokens takes personal tokens out of a message to an agent
// ARK launched: the session has its own token.
func WithoutPastedTokens(text string) string {
	if !strings.Contains(text, "arka_") {
		return text
	}
	return PastedTokenRe.ReplaceAllLiteralString(text, PastedTokenNote)
}

func envHas(env []string, key, value string) bool {
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if ok && strings.EqualFold(k, key) && (value == "" && v != "" || value != "" && v == value) {
			return true
		}
	}
	return false
}

// sessionPreamble is the session block plus the composed core rules for
// one run ("" for demo presets and without the CoreRules hook).
func (m *Manager) sessionPreamble(h *Hooks, s *Session, preset, projectRef *pyjson.Object, env []string, actor string,
	start *sessionstart.Start) string {
	if h == nil || h.CoreRules == nil || pyjson.Truthy(preset.Value("demo")) {
		return ""
	}
	m.mu.Lock()
	access := pyjson.Str(or(s.Meta.Value("access_mode"), "read_only"))
	m.mu.Unlock()
	pid := strOr(projectRef.Value("id"))
	name := strOr(projectRef.Value("name"))
	lines := []string{
		"ARK SESSION (added by ARK, not typed by the person)",
		"- ARK launched you: project " + name + " (" + pid + "), session " + s.ID + " (ARK_SESSION_ID).",
	}
	if envHas(env, "ARK_WAKE", "ark") {
		lines = append(lines, "- ARK_WAKE=ark: ARK wakes you when there is news; start no watcher and save no token file.")
	}
	if envHas(env, "ARKT", "") {
		scope := "read-only scope"
		if access == "full_access" {
			scope = "write scope"
		}
		lines = append(lines,
			"- In an ARK-launched session use $ARKT (your session token, "+scope+"; also ARK_AGENT_TOKEN and ARK_TOKEN); ignore other tokens.",
			"  Every agent API call: $ARK_URL/api/agent/v1/... with `Authorization: Bearer $ARKT`, `X-ARK-Project: $ARK_PROJECT` and `X-ARK-Session: $ARK_SESSION_ID`.",
			"  Do not use a token pasted into this chat, files in ~/.ark/tokens or any other token, and do not save $ARKT to a file; it ends with the session.",
			"  Read it from the environment in each command (\"$ARKT\"); never print it.")
	} else {
		lines = append(lines, "- This session has no ARK agent token: read through the task API with ARK_READ_TOKEN (see ARK-TASK.md); do not use any other token.")
	}
	lines = append(lines, "- Permission mode and workspace: ARK-TASK.md in your workspace.")
	if start != nil {
		lines = append(lines, "- Your first step is the Start section at the end of this block; do not ask for a pasted prompt.")
	}
	out := strings.Join(lines, "\n")
	if rules := strings.TrimSpace(h.CoreRules(pid, actor)); rules != "" {
		out += "\n\n" + rules
	}
	if start != nil && start.Text != "" {
		out += "\n\n" + start.Text
	}
	return out
}

// KickoffMessage is the first message of a session whose start has work to
// do and that was launched without one.
const KickoffMessage = "Begin with the Start section of the ARK session block."

// maxStartText is no working limit either: Start refuses a longer chosen
// prompt with 413 (checkPrompt) instead of cutting it.
const maxStartText = maxPrompt + 1

// startChoice is the "start" of a launch request: the prompt chosen in the
// Run dialog ({id, title, text}); nil when none.
func startChoice(v any) *sessionstart.Chosen {
	o, ok := v.(*pyjson.Object)
	if !ok {
		return nil
	}
	c := &sessionstart.Chosen{ID: limit(pytext.Strip(strOr(o.Value("id"))), 80), Title: limit(pytext.Strip(strOr(o.Value("title"))), 160),
		Text: limit(WithoutPastedTokens(pytext.Strip(strOr(o.Value("text")))), maxStartText)}
	if c.ID == "" {
		return nil
	}
	if c.Title == "" {
		c.Title = c.ID
	}
	return c
}

func startObject(st *sessionstart.Start) *pyjson.Object {
	return pyjson.NewObject("mode", st.Mode, "title", st.Title, "summary", st.Summary, "task", st.Task, "text", st.Text, "at", NowISO())
}

// resumeStart is the start of a later run: what the session was doing.
func (m *Manager) resumeStart(h *Hooks, s *Session, actor string) *sessionstart.Start {
	if h == nil || h.StartPoint == nil {
		return nil
	}
	m.mu.Lock()
	turn := intOr(s.Meta.Value("turn"), 1)
	taskRef := strOr(s.Meta.Value("task_ref"))
	folder := folderFromMeta(s.Meta.Value("task_folder"))
	notice := strOr(s.Meta.Value("error"))
	if notice == "" {
		notice = strOr(s.Meta.Value("notice"))
	}
	status := ""
	if code, ok := s.Meta.Value("exit_code").(int64); ok {
		status = "finished"
		if code != 0 {
			status = "stopped (exit code " + strconv.FormatInt(code, 10) + ")"
		}
	}
	m.mu.Unlock()
	st := h.StartPoint(StartInput{Actor: actor, TaskRef: taskRef, Folder: folder, Resume: &sessionstart.Resume{Turn: turn, Status: status, Notice: notice}})
	if st != nil {
		m.mu.Lock()
		s.Meta.Set("start", startObject(st))
		m.mu.Unlock()
	}
	return st
}
