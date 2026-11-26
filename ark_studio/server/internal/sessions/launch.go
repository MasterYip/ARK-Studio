package sessions

import (
	"arkstudio/go/internal/sessionstart"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/platform/textfile"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/linuxacct"
	"arkstudio/go/internal/providers"
	"arkstudio/go/internal/rnd"
)

func bodyStr(body *pyjson.Object, key string) string { return strOr(body.Value(key)) }

// safeRelative is _safe_relative.
func safeRelative(v any, field string) (string, error) {
	raw := strings.Trim(pytext.Strip(strings.ReplaceAll(strOr(v), "\\", "/")), "/")
	if raw == "" {
		return "", nil
	}
	if driveRe.MatchString(raw) {
		return "", serr(field+" must be relative to the session workspace", 400, "")
	}
	var parts []string
	for _, p := range strings.Split(raw, "/") {
		if p == "" || p == "." {
			continue
		}
		if p == ".." {
			return "", serr(field+" may not contain '..'", 400, "")
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, "/"), nil
}

func (m *Manager) newID() string {
	for {
		candidate := "s_" + clock.Now().UTC().Format("20060102150405") + rnd.Hex("sessions", 3)
		if _, err := os.Stat(filepath.Join(m.Root, candidate)); err != nil {
			return candidate
		}
	}
}

func (m *Manager) validatePermissionMode(v any, preset *pyjson.Object) (string, error) {
	requested := modeToken(or(or(v, preset.Value("default_permission_mode")), "read_only"))
	if isApprovalMode(requested) && !pyjson.Truthy(preset.Value("supports_approval")) {
		return "", serr(pyjson.Str(preset.Value("label"))+" cannot request approvals; ask/accept-edits modes "+
			"need an interactive adapter that reads decisions on stdin", 400, "")
	}
	return normalizeAccessMode(requested, presetStrings(preset, "permission_modes"))
}

func normalizeAccessMode(v any, allowed []string) (string, error) {
	mode := modeToken(or(v, "read_only"))
	if !contains(permissionModes, mode) {
		labels := make([]string, len(permissionModes))
		for i, m := range permissionModes {
			labels[i] = permissionLabels[m]
		}
		return "", serr("access_mode must be one of: "+strings.Join(labels, ", "), 400, "")
	}
	if len(allowed) > 0 && !contains(allowed, mode) {
		return "", serr("this platform does not allow "+permissionLabels[mode]+" access", 400, "")
	}
	return mode, nil
}

func (m *Manager) resolveWorktree(h *Hooks, ref string, allowShared bool, excludeID string) (*pyjson.Object, error) {
	if h == nil || h.Worktree == nil {
		return nil, serr("worktree sessions are not available on this server", 400, "")
	}
	target, err := h.Worktree(ref)
	if err != nil {
		var se *Error
		if errors.As(err, &se) {
			return nil, se
		}
		status, code := 400, ""
		if sc, ok := err.(interface {
			StatusCode() int
			ErrorCode() string
		}); ok {
			status, code = sc.StatusCode(), sc.ErrorCode()
		}
		return nil, serr(err.Error(), status, code)
	}
	if target == nil || !pyjson.Truthy(target.Value("path")) {
		return nil, serr("the worktree manager returned an invalid target", 400, "")
	}
	m.mu.Lock()
	conflicts := m.activeWorktreeSessionsLocked(pyjson.Str(target.Value("path")), excludeID)
	m.mu.Unlock()
	if len(conflicts) > 0 && !allowShared {
		return nil, serr("another active session already uses this worktree ("+strings.Join(conflicts, ", ")+
			"); stop it or explicitly allow a shared worktree", 409, "")
	}
	return target, nil
}

var promptRequired = set("dsh", "codex", "claude", "opencode", "cursor", "gemini")

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func apiObject(apiBase string) *pyjson.Object {
	return pyjson.NewObject("base", apiBase, "token_env", "ARK_READ_TOKEN", "header", "X-ARK-Read-Token",
		"methods", []any{"GET"})
}

func permissionContext(preset *pyjson.Object, access string) *pyjson.Object {
	return pyjson.NewObject("mode", access, "label", permissionLabels[access],
		"description", permissionDescriptions[access], "supports_approval", pyjson.Truthy(preset.Value("supports_approval")),
		"approval_protocol", ApprovalProtocol, "ark_write_token", access == "full_access")
}

// Start is SessionManager.start.
func (m *Manager) Start(h *Hooks, body *pyjson.Object, baseURL, actor string) (*Session, error) {
	overrides := AgentOverrides(h, actor)
	preset, err := m.Preset(bodyStr(body, "platform"), overrides)
	if err != nil {
		return nil, err
	}
	label := pyjson.Str(preset.Value("label"))
	if !providers.Available(preset) {
		return nil, serr(label+" is not installed or configured", 400, "")
	}
	title := limit(pytext.Strip(bodyStr(body, "title")), maxTitle)
	prompt := WithoutPastedTokens(pytext.Strip(bodyStr(body, "prompt")))
	if err := checkPrompt("the prompt", prompt); err != nil {
		return nil, err
	}
	if title == "" && prompt != "" {
		title = label + " session"
	}
	presetID := pyjson.Str(preset.Value("id"))
	// Pasted images are saved once the session has a workspace (below).
	contextIn, startImages := splitImageContext(body.Value("context"))
	startCtx, err := m.startContextEntries(h, contextIn)
	if err != nil {
		return nil, err
	}
	if len(startCtx) > 0 {
		prompt = composeMessage(prompt, startCtx)
		if err := checkPrompt("the prompt with its start context", prompt); err != nil {
			return nil, err
		}
	}
	// A session may start without a first message: a long-lived process
	// waits for it; a one-shot agent starts with it (awaiting_first).
	awaitFirst := prompt == "" && len(startImages) == 0 && promptRequired[presetID]
	chosen := startChoice(body.Value("start"))
	if chosen != nil {
		if err := checkPrompt("the start prompt", chosen.Text); err != nil {
			return nil, err
		}
	}
	model := limit(pytext.Strip(bodyStr(body, "model")), 80)
	agent := limit(pytext.Strip(bodyStr(body, "agent")), 120)
	effort := limit(pytext.Strip(bodyStr(body, "effort")), 40)
	access, err := m.validatePermissionMode(body.Value("access_mode"), preset)
	if err != nil {
		return nil, err
	}
	if err := m.checkEffort(preset, model, effort); err != nil {
		return nil, err
	}
	taskRef := limit(pytext.Strip(strOr(or(body.Value("task"), body.Value("task_ref")))), 40)
	// What the agent does first (preamble.go).  A start that has work to
	// do runs without a typed first message.
	// Where the work goes (taskfolder.go): resolved before the start, which
	// names it, and created now so the agent has it from its first file.
	sessionID := m.newID()
	var folder *sessionstart.Folder
	if h != nil && h.TaskFolder != nil && !pyjson.Truthy(preset.Value("demo")) {
		folder = h.TaskFolder(FolderInput{Actor: actor, TaskRef: taskRef, SessionID: sessionID, Title: title, Created: NowISO()})
		prepareFolder(folder)
	}
	var start *sessionstart.Start
	if h != nil && h.StartPoint != nil && !pyjson.Truthy(preset.Value("demo")) {
		start = h.StartPoint(StartInput{Actor: actor, TaskRef: taskRef, Prompt: prompt, Chosen: chosen, Folder: folder})
		if start != nil && prompt == "" && len(startImages) == 0 && start.Mode != sessionstart.ModeWait {
			prompt = KickoffMessage
			awaitFirst = false
		}
	}
	cwd, err := safeRelative(or(body.Value("cwd"), ""), "cwd")
	if err != nil {
		return nil, err
	}
	worktreeRef := pytext.Strip(bodyStr(body, "worktree"))
	var worktree *pyjson.Object
	allowShared := pyjson.Truthy(body.Value("allow_shared_worktree"))
	if worktreeRef != "" {
		if cwd != "" {
			return nil, serr("a worktree session starts at the worktree root; leave cwd empty", 400, "")
		}
		if worktree, err = m.resolveWorktree(h, worktreeRef, allowShared, ""); err != nil {
			return nil, err
		}
		if !isDir(pyjson.Str(worktree.Value("path"))) {
			return nil, serr("the selected worktree folder is missing", 409, "")
		}
	}
	parentID := limit(pytext.Strip(bodyStr(body, "parent_id")), 40)
	var parent *Session
	if parentID != "" {
		if parent, err = m.Get(parentID); err != nil {
			return nil, err
		}
		m.mu.Lock()
		pstatus, pdepth := parent.Meta.Value("status"), intOr(parent.Meta.Value("depth"), 0)
		ptask, pcwd := strOr(parent.Meta.Value("task_ref")), parent.Meta.Value("cwd")
		m.mu.Unlock()
		if pstatus == "archived" {
			return nil, serr("parent session is archived", 400, "")
		}
		if pdepth >= 3 {
			return nil, serr("subagent nesting is limited to 3 levels", 400, "")
		}
		if taskRef == "" {
			taskRef = limit(ptask, 40)
		}
		if cwd == "" && pcwd != nil && pcwd != "" && pcwd != "." {
			if cwd, err = safeRelative(pcwd, "cwd"); err != nil {
				return nil, err
			}
		}
	}
	command := providers.Command(preset)
	if preset.Value("prompt_mode") == "arg" {
		has := false
		for _, part := range command {
			has = has || strings.Contains(part, "{prompt}")
		}
		if !has {
			return nil, serr("this platform's command has no {prompt} placeholder; use a stdin platform", 400, "")
		}
	}

	sessionFolder := filepath.Join(m.Root, sessionID)
	workspace := filepath.Join(sessionFolder, "workspace")
	artifacts := filepath.Join(workspace, "artifacts")
	if err := os.MkdirAll(artifacts, 0o755); err != nil {
		return nil, err
	}
	var cwdDir string
	if worktree != nil {
		cwdDir = pyjson.Str(worktree.Value("path"))
		if !isDir(cwdDir) {
			return nil, serr("the selected worktree folder is missing", 409, "")
		}
	} else {
		cwdDir = workspace
		if cwd != "" {
			cwdDir = filepath.Join(workspace, filepath.FromSlash(cwd))
		}
		if err := os.MkdirAll(cwdDir, 0o755); err != nil {
			return nil, err
		}
		rw, rc := resolvePath(workspace), resolvePath(cwdDir)
		if rc != rw && !strings.HasPrefix(rc, rw+string(filepath.Separator)) {
			return nil, serr("cwd must stay inside the session workspace", 400, "")
		}
	}
	cwdLabel := "."
	if worktree == nil && cwd != "" {
		cwdLabel = cwd
	}
	project := pyjson.NewObject()
	if h != nil && h.Project != nil {
		p, err := h.Project()
		if err != nil {
			return nil, err
		}
		if p != nil {
			project = p
		}
	}
	projectRef := pyjson.NewObject("id", project.Value("id"), "name", project.Value("name"), "path", project.Value("path"))
	readToken := rnd.URLSafe("sessions", 24)
	apiBase := "/api/task-agent/v1"
	if baseURL != "" {
		apiBase = strings.TrimRight(baseURL, "/") + "/api/task-agent/v1"
	}
	context := pyjson.NewObject()
	if h != nil && h.Context != nil {
		c, err := h.Context()
		if err != nil {
			return nil, err
		}
		if c != nil {
			context = c.Copy()
		}
	}
	if !context.Has("format") {
		context.Set("format", "ark-task-agent-context")
	}
	if !context.Has("version") {
		context.Set("version", int64(1))
	}
	context.Set("generated_at", NowISO()).Set("read_only", true).Set("project", projectRef)
	sessionInfo := pyjson.NewObject("id", sessionID, "platform", presetID, "task_ref", taskRef, "model", model,
		"agent", agent, "effort", effort, "access_mode", access, "permission_mode", access,
		"supports_approval", pyjson.Truthy(preset.Value("supports_approval")))
	context.Set("session", sessionInfo)
	context.Set("permission", permissionContext(preset, access))
	if worktree != nil {
		sessionInfo.Set("worktree", pyjson.NewObject("path", worktree.Value("path"), "branch", or(worktree.Value("branch"), "")))
	}
	context.Set("api", apiObject(apiBase))
	if err := writeJSON(filepath.Join(workspace, "ark-context.json"), context); err != nil {
		return nil, err
	}
	contract := m.contract(sessionID, preset, projectRef, taskRef, cwdLabel, apiBase, access, worktree, workspace, folder)
	if err := os.WriteFile(filepath.Join(workspace, "ARK-TASK.md"), textfile.Encode(contract), 0o644); err != nil {
		return nil, err
	}
	var depth int64
	if parent != nil {
		m.mu.Lock()
		depth = intOr(parent.Meta.Value("depth"), 0) + 1
		m.mu.Unlock()
	}
	var wtCopy any
	allow := false
	if worktree != nil {
		wtCopy = worktree.Copy()
		allow = allowShared
	}
	now := NowISO()
	meta := pyjson.NewObject(
		"id", sessionID, "title", title, "platform", presetID, "platform_label", label, "model", model,
		"effort", effort, "access_mode", access, "permission_mode", access,
		"permission_history", []any{pyjson.NewObject("mode", access, "at", now, "turn", int64(1), "source", "start")},
		"turn", int64(1), "approvals", []any{}, "approval_rules", []any{}, "agent", agent, "task_ref", taskRef,
		"parent_id", parentID, "relation", "", "side_chat_of", "", "forked_from", "", "depth", depth,
		"status", "starting", "cwd", cwdLabel, "worktree", wtCopy, "allow_shared_worktree", allow,
		"created_at", now, "started_at", nil, "last_activity_at", now, "ended_at", nil, "exit_code", nil,
		"pid", nil, "error", "", "notice", "", "project", projectRef,
		"followup", pyjson.Truthy(preset.Value("followup")), "prompt", prompt, "read_token", readToken,
		"api", apiObject(apiBase),
		"read_env", pyjson.NewObject("ARK_PROJECT_PATH", projectRef.Value("path"), "ARK_TASK_PROJECT", projectRef.Value("path"),
			"ARK_TASK_CONTEXT", "workspace/ark-context.json", "ARK_READ_TOKEN", "read-only session token",
			"ARK_TASK_API_BASE", apiBase),
		"counts", pyjson.NewObject("stdout", int64(0), "stderr", int64(0), "bytes", int64(0)), "actor", actor)
	if folder != nil {
		meta.Set("task_folder", folderObject(folder))
	}
	s := &Session{ID: sessionID, Folder: sessionFolder, Meta: meta, m: m, start: start}
	if start != nil {
		meta.Set("start", startObject(start))
	}
	if native := pytext.Strip(bodyStr(body, "fork_native_id")); native != "" {
		meta.Set("fork_native_id", native)
	}
	if len(startImages) > 0 {
		entries, err := m.contextEntries(h, s, startImages)
		if err != nil {
			return nil, err
		}
		s.pendingEntries = entries
	}
	m.mu.Lock()
	m.sessions[sessionID] = s
	m.order = append(m.order, sessionID)
	m.saveLocked(s)
	m.mu.Unlock()
	startEntries := s.pendingEntries
	if awaitFirst && !(presetID == "claude" && m.streamEnabled()) {
		// Nothing to run yet: the first message starts the agent (Continue).
		m.mu.Lock()
		meta.Set("status", "stopped").Set("awaiting_first", true).Set("notice", "")
		m.saveLocked(s)
		m.mu.Unlock()
		return s, nil
	}
	if err := m.spawn(h, s, preset, prompt, cwdDir, workspace, artifacts, projectRef, apiBase, readToken, false, actor); err != nil {
		return s, err
	}
	m.recordMedia(s, startEntries) // the first turn shows its images
	return s, nil
}

func expand(token string, values [][2]string) string {
	for _, kv := range values {
		token = strings.ReplaceAll(token, "{"+kv[0]+"}", kv[1])
	}
	return token
}

func preview(argv []string, prompt string) string {
	parts := append([]string(nil), argv...)
	if prompt != "" {
		for i, p := range parts {
			parts[i] = strings.ReplaceAll(p, prompt, "…")
		}
	}
	n := len(parts)
	if n > 4 {
		n = 4
	}
	out := strings.Join(parts[:n], " ")
	if len(parts) > 4 {
		out += " …"
	}
	return limit(out, 300)
}

func indexOf(list []string, s string) int {
	for i, x := range list {
		if x == s {
			return i
		}
	}
	return -1
}

func insertAt(list []string, at int, items ...string) []string {
	out := append([]string(nil), list[:at]...)
	out = append(out, items...)
	return append(out, list[at:]...)
}

// resumeArgv is _resume_argv.
func resumeArgv(argv []string, platform, nativeID, prompt string) []string {
	argv = append([]string(nil), argv...)
	if platform == "claude" {
		if !contains(argv, "--resume") && !contains(argv, "-r") && len(argv) >= 1 {
			argv = insertAt(argv, 1, "--resume", nativeID)
		}
		return argv
	}
	if platform == "codex" && contains(argv, "exec") && !contains(argv, "resume") {
		at := len(argv)
		if prompt != "" && contains(argv, prompt) {
			at = indexOf(argv, prompt)
		}
		argv = insertAt(argv, at, "resume", nativeID)
	}
	return argv
}

func (m *Manager) failSpawn(s *Session, message string) {
	m.mu.Lock()
	s.Meta.Set("status", "errored").Set("error", message).Set("ended_at", NowISO())
	setTurnEndLocked(s, "error", message)
	m.saveLocked(s)
	m.mu.Unlock()
}

// spawn is SessionManager._spawn.
func (m *Manager) spawn(h *Hooks, s *Session, preset *pyjson.Object, prompt, cwdDir, workspace, artifacts string,
	projectRef *pyjson.Object, apiBase, readToken string, recordPrompt bool, actor string) error {
	m.mu.Lock()
	meta := s.Meta
	model := strOr(meta.Value("model"))
	if actor == "" {
		actor = strOr(meta.Value("actor"))
	}
	effort := strOr(meta.Value("effort"))
	agent := strOr(meta.Value("agent"))
	resumeID := strOr(meta.Delete("resume_native_id"))
	forkID := strOr(meta.Delete("fork_native_id"))
	pending := s.pendingEntries
	s.pendingEntries = nil
	taskRefNow := strOr(meta.Value("task_ref"))
	kept := folderFromMeta(meta.Value("task_folder"))
	titleNow, createdNow := strOr(meta.Value("title")), strOr(meta.Value("created_at"))
	m.mu.Unlock()
	// Where the work goes.  A folder the session already has stays its
	// folder (a task created later records it; files are never moved);
	// otherwise it is resolved again, e.g. after a task-space decision.
	if h != nil && h.TaskFolder != nil && !pyjson.Truthy(preset.Value("demo")) && (kept == nil || kept.Path == "") {
		if f := h.TaskFolder(FolderInput{Actor: actor, TaskRef: taskRefNow, SessionID: s.ID, Title: titleNow, Created: createdNow}); f != nil {
			prepareFolder(f)
			m.mu.Lock()
			meta.Set("task_folder", folderObject(f))
			m.mu.Unlock()
		}
	}
	m.mu.Lock()
	taskFolder := folderFromMeta(meta.Value("task_folder"))
	m.mu.Unlock()
	env := m.buildEnv(h, s, projectRef, apiBase, readToken, workspace, artifacts, actor)
	presetID := pyjson.Str(preset.Value("id"))
	// What the agent is told about ARK (preamble.go): Claude Code takes it
	// as an appended system prompt on every run, other agents ahead of
	// their first message.
	userPrompt := prompt
	m.mu.Lock()
	firstTurn := intOr(meta.Value("turn"), 1) <= 1
	start := s.start
	s.start = nil
	m.mu.Unlock()
	if start == nil && (!firstTurn || resumeID != "") {
		start = m.resumeStart(h, s, actor)
	}
	preamble := m.sessionPreamble(h, s, preset, projectRef, env, actor, start)
	if preamble != "" && presetID != "claude" && firstTurn && resumeID == "" && forkID == "" && prompt != "" {
		prompt = preamble + "\n\n---\n\n" + prompt
	}
	values := [][2]string{{"prompt", prompt}, {"model", model}, {"effort", effort}, {"workspace", cwdDir},
		{"project", strOr(projectRef.Value("path"))}, {"context", filepath.Join(workspace, "ark-context.json")},
		{"artifacts", artifacts}, {"api_base", apiBase}, {"session", s.ID}}
	var argv []string
	for _, part := range providers.Command(preset) {
		argv = append(argv, expand(part, values))
	}
	if resumeID != "" {
		argv = resumeArgv(argv, presetID, resumeID, prompt)
	} else if forkID != "" {
		if next, ok := forkArgv(argv, presetID, forkID, prompt); ok {
			argv = next
			m.mu.Lock()
			meta.Set("forked_native_from", forkID)
			m.mu.Unlock()
		}
	}
	if presetID == "codex" && model != "" && !contains(argv, "-m") && !contains(argv, "--model") {
		at := 1
		if i := indexOf(argv, "exec"); i >= 0 {
			at = i + 1
		}
		argv = insertAt(argv, at, "-m", model)
	}
	if presetID == "codex" && effort != "" {
		has := false
		for _, p := range argv {
			has = has || strings.Contains(p, "model_reasoning_effort")
		}
		if !has {
			at := 1
			if i := indexOf(argv, "exec"); i >= 0 {
				at = i + 1
			}
			argv = insertAt(argv, at, "-c", "model_reasoning_effort="+effort)
		}
	}
	if presetID == "codex" {
		m.mu.Lock()
		access := pyjson.Str(or(meta.Value("access_mode"), "read_only"))
		m.mu.Unlock()
		argv = withCodexSandbox(argv, access)
		if access == "full_access" || access == "accept_edits" || access == "ask" {
			argv = withCodexDirs(argv, codexWritableDirs(taskFolder)) // the registered places, writable too
		}
		argv = codexJSONArgv(argv)
	}
	agentFlag := m.agentFlag(h, presetID, agent)
	if presetID == "codex" && len(agentFlag) > 0 {
		at := 1
		if i := indexOf(argv, "exec"); i >= 0 {
			at = i + 1
		}
		argv = insertAt(argv, at, agentFlag...)
	}
	if presetID == "claude" && len(argv) > 1 {
		var extra []string
		if model != "" && !contains(argv, "--model") {
			extra = append(extra, "--model", model)
		}
		if effort != "" && !contains(argv, "--effort") {
			extra = append(extra, "--effort", effort)
		}
		extra = append(extra, agentFlag...)
		argv = insertAt(argv, 1, extra...)
		if preamble != "" && !contains(argv, "--append-system-prompt") && !contains(argv, "--append-system-prompt-file") {
			argv = append(argv, systemPromptArgs(preamble, workspace)...)
		}
	}
	// A long-lived Claude Code process takes every message on stdin.
	stream := false
	if presetID == "claude" && len(argv) > 1 && m.streamEnabled() {
		if next, ok := claudeStreamArgv(argv, prompt); ok {
			if m.makeRoom(actor, s) {
				argv, stream = next, true
			}
		}
	}
	if presetID == "claude" && len(argv) > 1 && !stream {
		argv = insertAt(argv, 1, claudeStreamArgs(argv)...)
	}
	if !stream && len(pending) > 0 {
		// One-shot turns cannot take image blocks: the agent gets the paths.
		prompt = composeMessage(prompt, pending)
		for i, part := range argv {
			if part != "" && (part == strOr(meta.Value("prompt")) || part == userPrompt) {
				argv[i] = prompt
			}
		}
	}
	// A long prompt never travels as an argument (longargs.go).
	stdinPrompt := ""
	if !stream {
		next, viaStdin, err := longPromptArgv(presetID, preset, argv, prompt, workspace)
		if err != nil {
			m.failSpawn(s, err.Error())
			return err
		}
		argv, stdinPrompt = next, viaStdin
	}
	m.mu.Lock()
	previous := strOr(meta.Value("linux_account"))
	m.mu.Unlock()
	selected := ""
	if h != nil && h.LinuxAccount != nil {
		selected = pytext.Strip(h.LinuxAccount(actor))
	}
	if previous != "" && selected != previous {
		msg := "the assigned Linux account '" + previous + "' is no longer selected or assigned; relaunch after an admin reassigns it"
		m.failSpawn(s, msg)
		return serr(msg, 409, "LINUX_ACCOUNT_REVOKED")
	}
	if selected != "" {
		info, err := linuxacct.Preflight(selected, cwdDir)
		if err != nil {
			var le *linuxacct.Error
			if errors.As(err, &le) {
				m.failSpawn(s, le.Message)
				return serr(le.Message, le.Status, le.Code)
			}
			return err
		}
		if argv, err = linuxacct.Wrap(argv, selected, info.Home); err != nil {
			return err
		}
		m.mu.Lock()
		meta.Set("linux_account", selected).Set("linux_account_home", info.Home)
		m.mu.Unlock()
	}
	m.mu.Lock()
	meta.Set("command_preview", preview(argv, prompt))
	m.mu.Unlock()
	m.markTurnStart(s)
	proc, err := m.startProcess(s, argv, cwdDir, env)
	if err != nil {
		text := osErrorText(err, argv[0])
		m.failSpawn(s, "could not start process: "+text)
		return serr("Could not start "+pyjson.Str(preset.Value("label"))+": "+text, 400, "")
	}
	m.mu.Lock()
	s.proc = proc
	now := NowISO()
	meta.Set("status", "running").Set("pid", int64(proc.pid())).Set("started_at", now).Set("last_activity_at", now)
	meta.Delete("bg_pending") // a new process: the old one's background work is gone
	if stream {
		meta.Set("stream_input", true).Set("followup", true).Set("turn_state", "running")
	} else if meta.Has("stream_input") {
		meta.Delete("stream_input")
		meta.Delete("turn_state")
		meta.Set("followup", pyjson.Truthy(preset.Value("followup")))
	}
	m.saveLocked(s)
	m.mu.Unlock()
	m.startReaders(s, proc)
	if stream {
		m.startStreamReaper()
		if prompt != "" || len(pending) > 0 {
			if err := m.sendUserMessage(s, prompt, pending, prompt, recordPrompt); err != nil {
				return serr("could not send the first message: "+err.Error(), 409, "")
			}
		} else {
			m.mu.Lock()
			meta.Set("turn_state", "waiting")
			m.saveLocked(s)
			m.mu.Unlock()
		}
		return nil
	}
	if stdinPrompt != "" {
		// The agent reads the prompt to EOF; a goroutine, so a slow reader
		// never holds up the request.
		go func() {
			_ = proc.writeStdin([]byte(stdinPrompt))
			proc.closeStdin()
		}()
		return nil
	}
	if prompt != "" && preset.Value("prompt_mode") == "stdin" {
		_ = m.writeInput(s, prompt, recordPrompt, nil)
	}
	if !pyjson.Truthy(preset.Value("followup")) {
		proc.closeStdin()
	}
	return nil
}

// splitImageContext separates pasted images (saved after the session has a
// workspace) from the rest of a new session's context.
func splitImageContext(raw any) (any, []any) {
	list, ok := raw.([]any)
	if !ok {
		return raw, nil
	}
	var rest, images []any
	for _, x := range list {
		if o, ok := x.(*pyjson.Object); ok && pytext.Lower(strOr(o.Value("kind"))) == "image_data" {
			images = append(images, x)
			continue
		}
		rest = append(rest, x)
	}
	if rest == nil {
		return nil, images
	}
	return rest, images
}

func isWriteCredential(name string) bool {
	upper := strings.ToUpper(name)
	switch upper {
	case "ARK_AGENT_TOKEN", "ARK_TOKEN", "ARK_WRITE_TOKEN", "ARK_API_TOKEN", "ARK_TASK_WRITE_TOKEN", "ARKT":
		return true
	}
	if !strings.HasPrefix(upper, "ARK_") {
		return false
	}
	for _, marker := range []string{"TOKEN", "SECRET", "PASSWORD", "CREDENTIAL"} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}

func isCredentialEnv(name string) bool {
	upper := strings.ToUpper(name)
	if isWriteCredential(upper) {
		return true
	}
	if strings.Contains(upper, "BASE_URL") || strings.Contains(upper, "ENDPOINT") {
		return false
	}
	for _, marker := range []string{"API_KEY", "_TOKEN", "TOKEN_", "SECRET", "PASSWORD", "CREDENTIAL", "OAUTH"} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}

// inheritedWakeEnv are not passed on from the server's own environment:
// they name the session to wake, and a stale value would wake another one.
var inheritedWakeEnv = map[string]bool{"ARK_WAKE": true, "ARK_SESSION_ID": true, "ARK_CODEX_THREAD": true,
	"CODEX_THREAD_ID": true, "ARK_OWNER_PID": true, "ARK_WATCH_DETACHED_OWNER": true}

type envMap struct {
	keys []string
	vals map[string]string
}

func (e *envMap) set(k, v string) {
	key := k
	if runtime.GOOS == "windows" {
		key = strings.ToUpper(k)
	}
	if _, ok := e.vals[key]; !ok {
		e.keys = append(e.keys, key)
	}
	e.vals[key] = v
}

func (e *envMap) get(k string) string {
	if runtime.GOOS == "windows" {
		k = strings.ToUpper(k)
	}
	return e.vals[k]
}

func (e *envMap) list() []string {
	out := make([]string, 0, len(e.keys))
	for _, k := range e.keys {
		out = append(out, k+"="+e.vals[k])
	}
	return out
}

// buildEnv is SessionManager._build_env.
func (m *Manager) buildEnv(h *Hooks, s *Session, projectRef *pyjson.Object, apiBase, readToken, workspace, artifacts, actor string) []string {
	env := &envMap{vals: map[string]string{}}
	for _, kv := range os.Environ() {
		i := strings.Index(kv, "=")
		if i <= 0 { // Windows "=C:" entries are not in os.environ
			continue
		}
		key, value := kv[:i], kv[i+1:]
		if isWriteCredential(key) || isCredentialEnv(key) || inheritedWakeEnv[strings.ToUpper(key)] {
			continue
		}
		skip := false
		for _, secret := range m.StripEnvValues {
			skip = skip || (secret != "" && strings.Contains(value, secret))
		}
		if !skip {
			env.set(key, value)
		}
	}
	m.mu.Lock()
	meta := s.Meta
	access := pyjson.Str(or(meta.Value("access_mode"), "read_only"))
	model, effort := strOr(meta.Value("model")), strOr(meta.Value("effort"))
	platform := strOr(meta.Value("platform"))
	worktree, _ := meta.Value("worktree").(*pyjson.Object)
	m.mu.Unlock()
	projectPath := strOr(projectRef.Value("path"))
	m.mu.Lock()
	taskFolder := folderFromMeta(meta.Value("task_folder"))
	m.mu.Unlock()
	if a := folderArtifacts(taskFolder); a != "" && !isReadOnlyMode(access) {
		artifacts = a // the task folder, not the session workspace
	}
	for _, kv := range [][2]string{{"ARK_TASK_SESSION", s.ID}, {"ARK_TASK_PROJECT", projectPath},
		{"ARK_PROJECT_PATH", projectPath}, {"ARK_TASK_CONTEXT", filepath.Join(workspace, "ark-context.json")},
		{"ARK_TASK_ARTIFACTS", artifacts}, {"ARK_TASK_API_BASE", apiBase}, {"ARK_READ_TOKEN", readToken},
		{"ARK_TASK_ACCESS_MODE", access}, {"ARK_TASK_PERMISSION_MODE", access},
		{"ARK_TASK_APPROVAL_PROTOCOL", ApprovalProtocol}, {"ARK_TASK_MODEL", model}, {"ARK_TASK_EFFORT", effort},
		{"PYTHONUNBUFFERED", "1"}, {"PYTHONIOENCODING", "utf-8"}, {"PYTHONUTF8", "1"}} {
		env.set(kv[0], kv[1])
	}
	if taskFolder != nil && taskFolder.Path != "" {
		env.set("ARK_TASK_FOLDER", taskFolder.Path)
	}
	if platform == "codex" {
		// The transcript reads Codex's JSON events on stdout; its tracing
		// log on stderr is noise there (the raw log keeps what is left).
		if env.get("RUST_LOG") == "" {
			env.set("RUST_LOG", "error")
		}
		env.set("NO_COLOR", "1")
	}
	// ark-server's own folder first, so `ark-server agent watch` and
	// `ark-server agent host-id` work inside every session (T-0053).
	exeDir := ""
	if exe, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exe)
	}
	if runtime.GOOS == "windows" && exeDir != "" {
		current := env.get("PATH")
		if current == "" {
			current = os.Getenv("PATH")
		}
		env.set("PATH", exeDir+string(os.PathListSeparator)+current)
	}
	if runtime.GOOS != "windows" {
		home, _ := os.UserHomeDir()
		var parts []string
		if exeDir != "" {
			parts = append(parts, exeDir)
		}
		for _, p := range []string{filepath.Join(home, ".local", "bin"), filepath.Join(home, "bin"), "/usr/local/bin"} {
			if isDir(p) {
				parts = append(parts, p)
			}
		}
		current := env.get("PATH")
		if current == "" {
			current = os.Getenv("PATH")
		}
		for _, p := range strings.Split(current, string(os.PathListSeparator)) {
			if p != "" {
				parts = append(parts, p)
			}
		}
		seen := map[string]bool{}
		var ordered []string
		for _, p := range parts {
			if !seen[p] {
				seen[p] = true
				ordered = append(ordered, p)
			}
		}
		env.set("PATH", strings.Join(ordered, string(os.PathListSeparator)))
	}
	token := ""
	if h != nil && h.AgentToken != nil {
		if secret, id, err := h.AgentToken(actor, s.ID, access); err == nil && secret != "" {
			token = secret
			m.mu.Lock()
			s.agentSecret = secret
			if id != "" {
				ids := []any{}
				if old, ok := s.Meta.Value("agent_token_ids").([]any); ok {
					ids = append(ids, old...)
				}
				s.Meta.Set("agent_token_ids", append(ids, id))
			}
			m.mu.Unlock()
		}
	} else if access == "full_access" {
		token = m.WriteToken
	}
	// One wake-up mechanism per session (T-0053): where ARK nudges idle
	// sessions of this project and can continue this platform's runs, the
	// agent is told so (ARK_WAKE=ark) and starts no watcher.
	env.set("ARK_SESSION_ID", s.ID)
	if h != nil && h.WakesIdle != nil && contains(NativeResumePlatforms, platform) &&
		h.WakesIdle(strOr(projectRef.Value("id"))) {
		env.set("ARK_WAKE", "ark")
	}
	if token != "" {
		env.set("ARK_AGENT_TOKEN", token)
		env.set("ARK_TOKEN", token) // the name the ARK watcher scripts read
		env.set("ARKT", token)      // the short name the session block uses (preamble.go)
	}
	// The server root and project for the agent API (/api/agent/v1/...);
	// ARK_TASK_API_BASE is the task API, not the root.
	if root := strings.TrimSuffix(apiBase, "/api/task-agent/v1"); root != apiBase && root != "" {
		env.set("ARK_URL", root)
	}
	if pid := strOr(projectRef.Value("id")); pid != "" {
		env.set("ARK_PROJECT", pid)
	}
	if access == "full_access" {
		env.set("ARK_TASK_FULL_ACCESS", "1")
	} else {
		ro := "0"
		if isReadOnlyMode(access) {
			ro = "1"
		}
		env.set("ARK_TASK_READ_ONLY", ro)
		if access == "plan" {
			env.set("ARK_TASK_PLAN_MODE", "1")
		}
		if isApprovalMode(access) {
			env.set("ARK_TASK_APPROVALS", "1")
			mode := "all"
			if access == "accept_edits" {
				mode = "edits"
			}
			env.set("ARK_TASK_APPROVAL_MODE", mode)
		}
	}
	if worktree != nil && pyjson.Truthy(worktree.Value("path")) {
		env.set("ARK_TASK_WORKTREE", pyjson.Str(worktree.Value("path")))
		env.set("ARK_TASK_WORKTREE_BRANCH", strOr(worktree.Value("branch")))
	}
	if platform == "dsh" {
		mode := "workspace-write"
		if access == "full_access" {
			mode = "danger-full-access"
		} else if isReadOnlyMode(access) {
			mode = "read-only"
		}
		env.set("DSH_PERMISSION_MODE", mode)
		if h != nil && h.AssistantKey != nil {
			if key, err := h.AssistantKey(actor); err == nil && key != "" {
				env.set("TENCENTHUB_API_KEY", key)
			}
		}
	}
	override := objOr(AgentOverrides(h, actor).Value(platform))
	values := objOr(override.Value("env"))
	for _, name := range values.Keys() {
		if v, ok := values.Value(name).(string); ok && name != "" {
			env.set(name, v)
		}
	}
	return env.list()
}

// contract is SessionManager._contract (ARK-TASK.md).
func (m *Manager) contract(sessionID string, preset, project *pyjson.Object, taskRef, cwd, apiBase, access string,
	worktree *pyjson.Object, workspace string, folder *sessionstart.Folder) string {
	label := permissionLabels[access]
	artifactsHint := "`./artifacts/`"
	if workspace != "" {
		artifactsHint = "`" + filepath.Join(workspace, "artifacts") + "`"
	}
	hasWorktree := worktree != nil
	writeScope := "this session workspace"
	if hasWorktree {
		writeScope = "this session workspace or the selected Git worktree"
	}
	name := pyjson.Str(or(project.Value("name"), "(unnamed)"))
	ppath := pyjson.Str(project.Value("path"))
	lines := []string{
		"# ARK task session `" + sessionID + "`\n",
		"You are a task agent managed by ARK Studio (" + pyjson.Str(preset.Value("label")) + ").",
		"Permission mode: **" + label + "** — " + permissionDescriptions[access] + "\n",
	}
	switch {
	case isReadOnlyMode(access):
		lines = append(lines,
			"## This ARK project is read-only\n",
			"- Project: **"+name+"** (`"+ppath+"`)",
			"- Do **not** modify `project.json`, `ark.sqlite3`, `assets/`, or any other file in the project folder.",
			"- The inner ARK assistant is the only writer of ARK project data. If a change is needed, propose it in `./artifacts/`.",
			"- Read-only API base: `"+apiBase+"`",
			"- Read token: environment variable `ARK_READ_TOKEN`; header `X-ARK-Read-Token: <token>`.",
			"- The ARK write token is never provided to this process.\n")
		if hasWorktree {
			lines = append(lines, "- Editing files inside the selected Git worktree is part of this task; "+
				"the ARK project folder above remains read-only.\n")
		}
		if access == "plan" {
			last := "This is read-only enforcement plus a planning contract, not a separate execution sandbox.\n"
			if hasWorktree {
				last = "Do not edit the selected Git worktree in plan mode either.\n"
			}
			lines = append(lines, "## Plan mode\n",
				"Produce a concrete plan (steps, files, risks, acceptance checks) in your reply.",
				"Do not perform the edits or commands the plan describes; ARK withholds the write token.", last)
		}
	case isApprovalMode(access):
		lines = append(lines,
			"## Mutating actions need approval\n",
			"ARK cannot intercept your filesystem calls. You must ask before every mutating tool",
			"or command by emitting an approval request and waiting for the matching response.",
			"File edits are auto-approved in accept-edits mode; commands still wait for a decision.",
			"In ask mode every request waits for the user.",
			"The ARK project write token is **not** provided in this mode; ARK project data remains read-only.",
			"Write only inside "+writeScope+".\n",
			"### Approval protocol (`"+ApprovalProtocol+"`)\n",
			"Request line (stdout):",
			"```text",
			`[[ark-event]] {"type":"permission_request","id":"req-1","kind":"command",`,
			`               "tool":"bash","command":"pytest -q","input":{"command":"pytest -q"}}`,
			"```",
			"Decision line (stdin):",
			"```json",
			`{"type":"permission_response","protocol":"ark-permission-v1","id":"req-1",`,
			` "request_id":"req-1","decision":"allow","scope":"once","message":""}`,
			"```",
			"Wait for the response before acting. `decision` is `allow` or `deny`; `scope` is",
			"`once`, `always` (a user rule for this tool) or `auto` (mode policy).",
			"Claude Code stream-json `control_request`/`can_use_tool` frames are also recognised",
			"and answered with a matching `control_response`.\n")
	default:
		lines = append(lines,
			"## Full access\n",
			"- Project: **"+name+"** (`"+ppath+"`)",
			"- ARK's write token is provided as `ARK_AGENT_TOKEN` (also `ARK_TOKEN`) for `$ARK_URL/api/agent/v1/...`, with `X-ARK-Project: $ARK_PROJECT`.",
			"- Writes are logged as activity; use them deliberately and only for this project.",
			"### GPU queue",
			"Do not choose a GPU with `nvidia-smi` and do not SSH to a GPU host yourself.",
			"Describe the job and let the queue allocate it:",
			"```http",
			"POST /api/task-agent/v1/gpu/jobs",
			"X-ARK-Read-Token: $ARK_READ_TOKEN",
			"Content-Type: application/json",
			"```",
			"Example body: `{\"job\":\"train\",\"project\":\"ark\",\"gpu_count\":1,`",
			"`\"min_free_memory_mb\":12000,\"command\":\"python train.py\",`",
			"`\"work_dir\":\"/absolute/path/on/the/gpu/host\"}`.",
			"Poll `GET /api/task-agent/v1/gpu/jobs/<id>` and read its logs.",
			"The queue handles GPU selection, path mapping, environment setup and SSH launch.\n")
	}
	if hasWorktree {
		edit := "- Editing this worktree is expected."
		if access == "plan" {
			edit = "- Editing this worktree is expected except in plan mode."
		}
		repo := objOr(worktree.Value("repository"))
		lines = append(lines,
			"## Git worktree\n",
			"- Working tree: `"+pyjson.Str(worktree.Value("path"))+"`",
			"- Branch: `"+pyjson.Str(or(worktree.Value("branch"), "(detached)"))+"`",
			"- Repository: `"+strOr(repo.Value("root"))+"`",
			edit,
			"- Commit only in this worktree, and never push, merge, or delete the worktree.\n")
	}
	where := " (inside the private session workspace)"
	if hasWorktree {
		where = " (Git worktree root)"
	}
	task := taskRef
	if task == "" {
		task = "(unassigned)"
	}
	lines = append(lines,
		"## ARK context\n",
		"- `./ark-context.json` — generated snapshot (project summary, units, links, permission mode).",
		"- Fresh API: `GET /context`, `GET /project`, `GET /units`, `GET /links`, `GET /snapshot`.",
		"- Read token: `ARK_READ_TOKEN`; header `X-ARK-Read-Token: <token>`; API base `"+apiBase+"`.\n",
		"## Workspace\n",
		"- cwd: `"+cwd+"`"+where)
	if fl := contractFolderLines(folder); fl != nil && !isReadOnlyMode(access) {
		lines = append(lines, fl...)
	} else {
		lines = append(lines, "- Session workspace artifacts: "+artifactsHint)
	}
	lines = append(lines, "- Task reference: "+task+"\n")
	return strings.Join(lines, "\n")
}

// refreshWorkspacePermission is SessionManager._refresh_workspace_permission.
func (m *Manager) refreshWorkspacePermission(s *Session, preset *pyjson.Object, workspace string,
	projectRef *pyjson.Object, apiBase, cwd string) {
	m.mu.Lock()
	meta := s.Meta
	access := pyjson.Str(or(meta.Value("access_mode"), "read_only"))
	taskRef := strOr(meta.Value("task_ref"))
	taskFolder := folderFromMeta(meta.Value("task_folder"))
	info := pyjson.NewObject("id", s.ID, "platform", preset.Value("id"), "task_ref", taskRef,
		"model", or(meta.Value("model"), ""), "agent", or(meta.Value("agent"), ""), "effort", or(meta.Value("effort"), ""),
		"access_mode", access, "permission_mode", access, "supports_approval", pyjson.Truthy(preset.Value("supports_approval")))
	worktree, _ := meta.Value("worktree").(*pyjson.Object)
	if worktree != nil && !pyjson.Truthy(worktree.Value("path")) {
		worktree = nil
	}
	if worktree != nil {
		worktree = deepCopy(worktree).(*pyjson.Object)
	}
	m.mu.Unlock()
	context, ok := readJSON(filepath.Join(workspace, "ark-context.json"))
	if !ok {
		context = pyjson.NewObject()
	}
	if !context.Has("format") {
		context.Set("format", "ark-task-agent-context")
	}
	if !context.Has("version") {
		context.Set("version", int64(1))
	}
	if !context.Has("project") {
		context.Set("project", projectRef)
	}
	if !context.Has("api") {
		context.Set("api", apiObject(apiBase))
	}
	context.Set("generated_at", NowISO()).Set("read_only", isReadOnlyMode(access)).Set("session", info).
		Set("permission", permissionContext(preset, access))
	if worktree != nil {
		info.Set("worktree", pyjson.NewObject("path", worktree.Value("path"), "branch", or(worktree.Value("branch"), "")))
	}
	_ = writeJSON(filepath.Join(workspace, "ark-context.json"), context)
	_ = os.WriteFile(filepath.Join(workspace, "ARK-TASK.md"),
		textfile.Encode(m.contract(s.ID, preset, projectRef, taskRef, cwd, apiBase, access, worktree, workspace, taskFolder)), 0o644)
}

// startContextEntries renders the context attached to a new session's first
// prompt (the Sessions sidebar's "add context").  Only units and text
// snippets: files and media live in a session workspace that does not exist
// yet.
func (m *Manager) startContextEntries(h *Hooks, raw any) ([]*pyjson.Object, error) {
	if !pyjson.Truthy(raw) {
		return nil, nil
	}
	if list, ok := raw.([]any); ok {
		for _, x := range list {
			o, ok := x.(*pyjson.Object)
			if !ok {
				continue
			}
			switch pytext.Lower(pytext.Strip(strOr(or(o.Value("kind"), o.Value("type"))))) {
			case "unit", "task", "text", "snippet":
			default:
				return nil, serr("a new session can start with units and text snippets only", 400, "")
			}
		}
	}
	return m.contextEntries(h, nil, raw)
}

// claudeStreamArgs asks Claude Code's print mode for stream-json, so the
// transcript sees tool calls, sub-agents and background tasks (not only the
// final text).  Commands that already pick an output format keep it.
func claudeStreamArgs(argv []string) []string {
	if !contains(argv, "-p") && !contains(argv, "--print") {
		return nil
	}
	for _, a := range argv {
		if a == "--output-format" || strings.HasPrefix(a, "--output-format=") {
			return nil
		}
	}
	out := []string{"--output-format", "stream-json"}
	if !contains(argv, "--verbose") {
		out = append(out, "--verbose")
	}
	return out
}
