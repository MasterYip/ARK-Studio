// Package sessions ports arkstudio/sessions.py (SessionManager and
// ManagedSession: task-agent subprocesses with an HTTP-managed lifecycle)
// and arkstudio/session_transcript.py (transcript.go).
//
// Storage is the same as Python's: <sessions-root>/<id>/meta.json,
// stdout.jsonl, stderr.jsonl and workspace/ (artifacts/, ark-context.json,
// ARK-TASK.md); archived sessions move to <sessions-root>/.archived/.
//
// Locking: one manager mutex guards the session table and every session's
// metadata (Python's RLocks nest; a single non-reentrant lock is simpler
// and the critical sections are short).  Process I/O happens outside it.
package sessions

import (
	"crypto/subtle"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/platform/textfile"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/providers"
)

const (
	maxLogLine         = 8000
	maxJSONLogLine     = 64000
	maxLogLimit        = 500
	maxPrompt          = 12000
	maxInput           = 8000
	maxTitle           = 120
	maxTextArtifact    = 256 * 1024
	maxDownload        = 8 * 1024 * 1024
	maxArtifactEntries = 400
	maxContextItems    = 12
	maxContextText     = 4000
	maxContextTotal    = 24000
	maxSteerText       = 4000
	maxSteerQueue      = 20
	maxApprovals       = 200
	maxApprovalItems   = 60
	maxApprovalMessage = 600
	maxForkContext     = 6000
	// ApprovalProtocol is APPROVAL_PROTOCOL.
	ApprovalProtocol = "ark-permission-v1"
)

var (
	permissionModes  = []string{"read_only", "plan", "ask", "accept_edits", "full_access"}
	permissionLabels = map[string]string{
		"read_only": "read-only", "plan": "plan (read-only)", "ask": "ask to write",
		"accept_edits": "accept edits", "full_access": "full access",
	}
	permissionDescriptions = map[string]string{
		"read_only":    "Read the project through the read-only task API; no write token.",
		"plan":         "Read-only planning turn: no write token, and the contract asks for a plan instead of edits.",
		"ask":          "Every mutating tool or command needs an approval from you; the adapter must request it.",
		"accept_edits": "File edits are auto-approved by ARK; commands still wait for your decision.",
		"full_access":  "No prompts: the turn receives ARK's write token and may write project units.",
	}
	approvalModes = []string{"ask", "accept_edits"}
	editKinds     = set("file_edit", "edit", "write", "patch", "file_change", "file_write")
	textSuffixes  = set(".bat", ".cfg", ".conf", ".css", ".csv", ".html", ".ini", ".js", ".json",
		".jsonl", ".log", ".md", ".mjs", ".ps1", ".py", ".sh", ".sql", ".svg",
		".toml", ".ts", ".txt", ".xml", ".yaml", ".yml")
	activeStatuses = map[string]bool{"starting": true, "running": true, "stopping": true}
	statusOrder    = map[string]int{"running": 0, "idle": 1, "starting": 2, "stopping": 3, "errored": 4, "stopped": 5}
	// NativeResumePlatforms is NATIVE_RESUME_PLATFORMS.
	NativeResumePlatforms = []string{"claude", "codex"}
)

func isReadOnlyMode(m string) bool { return m == "read_only" || m == "plan" }
func isApprovalMode(m string) bool { return m == "ask" || m == "accept_edits" }

// Session is one ManagedSession.
type Session struct {
	ID     string
	Folder string
	Meta   *pyjson.Object

	m             *Manager
	proc          *process
	stopRequested bool
	// Context entries (pasted images) for the first message of the next spawn.
	pendingEntries []*pyjson.Object
	lastSave       time.Time
	logMu          sync.Mutex
	stdinMu        sync.Mutex
	// The run's agent token (Hooks.AgentToken), kept in memory only so
	// output lines that echo it are redacted before they are written.
	agentSecret string
}

// Workspace is ManagedSession.workspace.
func (s *Session) Workspace() string { return filepath.Join(s.Folder, "workspace") }

// Hooks are the request-bound providers of SessionManager.__init__
// (project_provider, context_provider, unit_provider, worktree_provider,
// agent_config_provider, assistant_config_provider,
// linux_account_provider).  The server builds one per request.
type Hooks struct {
	Project      func() (*pyjson.Object, error)
	Context      func() (*pyjson.Object, error)
	Unit         func(uid string) (*pyjson.Object, error)
	Worktree     func(ref string) (*pyjson.Object, error)
	AgentConfig  func(actor string) (*pyjson.Object, error)
	AssistantKey func(actor string) (string, error)
	LinuxAccount func(actor string) string
	// AgentToken issues the agent token for one session run (T-0053): the
	// secret for ARK_AGENT_TOKEN and its id for revocation; "" = none.
	AgentToken func(actor, sessionID, access string) (secret, id string, err error)
}

// Manager is SessionManager.
type Manager struct {
	tcache    transcriptCaches
	stream    streamState
	Root      string
	IdleAfter time.Duration
	// Set by the server after Open.
	Presets        []*pyjson.Object
	StripEnvValues []string
	WriteToken     string
	Catalog        *providers.Catalog
	OnChange       func(s *Session, event string)
	GuessType      func(name string) string

	mu       sync.Mutex
	sessions map[string]*Session
	order    []string
}

func readJSON(path string) (*pyjson.Object, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return pyjson.LoadsObject(strings.ReplaceAll(string(raw), "\r\n", "\n"))
}

// writeJSON is sessions._write_json (indent 2, tmp + replace).
func writeJSON(path string, data *pyjson.Object) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, textfile.Encode(string(pyjson.DumpsIndent(data, 2))+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func resolvePath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

// Open is SessionManager.__init__ + _load_existing: sessions left active by
// a previous process are marked stopped, exactly like Python does at start.
func Open(root string) (*Manager, error) {
	root = resolvePath(root)
	if err := os.MkdirAll(filepath.Join(root, ".archived"), 0o755); err != nil {
		return nil, err
	}
	m := &Manager{Root: root, IdleAfter: 90 * time.Second, sessions: map[string]*Session{}}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		folder := filepath.Join(root, name)
		if fi, err := os.Stat(folder); err != nil || !fi.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		meta, ok := readJSON(filepath.Join(folder, "meta.json"))
		if !ok || !IDRe.MatchString(strOr(meta.Value("id"))) {
			continue
		}
		if activeStatuses[strOr(meta.Value("status"))] {
			now := NowISO()
			meta.Set("status", "stopped").Set("ended_at", now).Set("last_activity_at", now).
				Set("notice", "ARK restarted; the previous process is no longer attached.")
			if err := writeJSON(filepath.Join(folder, "meta.json"), meta); err != nil {
				return nil, err
			}
		}
		id := pyjson.Str(meta.Value("id"))
		if _, dup := m.sessions[id]; !dup {
			m.order = append(m.order, id)
		}
		m.sessions[id] = &Session{ID: id, Folder: folder, Meta: meta, m: m}
	}
	return m, nil
}

func (m *Manager) presetByID(id string) *pyjson.Object {
	for _, p := range m.Presets {
		if pyjson.Str(p.Value("id")) == id {
			return p
		}
	}
	return nil
}

func (m *Manager) all() []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Session, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, m.sessions[id])
	}
	return out
}

// saveLocked writes meta.json (caller holds m.mu).
func (m *Manager) saveLocked(s *Session) {
	_ = writeJSON(filepath.Join(s.Folder, "meta.json"), s.Meta)
}

// Save is SessionManager.save.
func (m *Manager) Save(s *Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saveLocked(s)
}

func (m *Manager) notify(s *Session, event string) {
	if m.OnChange != nil {
		func() {
			defer func() { _ = recover() }()
			m.OnChange(s, event)
		}()
	}
}

// Get is SessionManager.get.
func (m *Manager) Get(id string) (*Session, error) {
	if !IDRe.MatchString(id) {
		return nil, serr("invalid session id", 404, "")
	}
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil {
		return nil, serr("session not found", 404, "")
	}
	return s, nil
}

// IDRe is SESSION_ID_RE.
var IDRe = regexp.MustCompile(`\As_[a-z0-9]{8,32}\z`)

// ForReadToken is SessionManager.session_for_read_token.
func (m *Manager) ForReadToken(token string) *Session {
	if token == "" {
		return nil
	}
	for _, s := range m.all() {
		m.mu.Lock()
		stored := strOr(s.Meta.Value("read_token"))
		m.mu.Unlock()
		if stored != "" && subtle.ConstantTimeCompare([]byte(stored), []byte(token)) == 1 {
			return s
		}
	}
	return nil
}

// Count is the number of known sessions.
func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

func or(v any, def any) any {
	if pyjson.Truthy(v) {
		return v
	}
	return def
}

// IsActive is ManagedSession.is_active.
func (m *Manager) IsActive(s *Session) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.isActiveLocked(s)
}

func (m *Manager) isActiveLocked(s *Session) bool {
	if s.proc != nil && s.proc.running() {
		return true
	}
	return activeStatuses[strOr(s.Meta.Value("status"))]
}

// displayStatus is SessionManager._display_status -> (status, raw).
func (m *Manager) displayStatus(meta *pyjson.Object) (string, string) {
	raw := pyjson.Str(or(meta.Value("status"), "stopped"))
	status := raw
	if raw == "running" {
		if pyjson.Truthy(meta.Value("stream_input")) && meta.Value("turn_state") == "waiting" {
			status = "idle" // a long-lived Claude Code process waits for the next message
		} else if last, ok := parseISO(meta.Value("last_activity_at")); ok && clock.Now().Sub(last) >= m.IdleAfter {
			status = "idle"
		}
	}
	return status, raw
}

// Brief is SessionManager.brief().
func (m *Manager) Brief() []*pyjson.Object {
	var out []*pyjson.Object
	for _, s := range m.all() {
		m.mu.Lock()
		meta := deepCopy(s.Meta).(*pyjson.Object)
		m.mu.Unlock()
		status, _ := m.displayStatus(meta)
		out = append(out, pyjson.NewObject("id", or(meta.Value("id"), s.ID), "title", or(meta.Value("title"), ""),
			"status", status, "agent", or(meta.Value("agent"), ""), "platform", or(meta.Value("platform"), ""),
			"task_ref", or(meta.Value("task_ref"), ""), "created_at", meta.Value("created_at"),
			"last_activity_at", meta.Value("last_activity_at"), "project", objOr(meta.Value("project"))))
	}
	return out
}

// RunsSummary is overview.runs_summary for the sessions of one project.
func (m *Manager) RunsSummary(projectID string) *pyjson.Object {
	var mine []*pyjson.Object
	counts := pyjson.NewObject()
	for _, item := range m.Brief() {
		project, _ := item.Value("project").(*pyjson.Object)
		if pyjson.Str(or(project.Value("id"), "")) != projectID {
			continue
		}
		mine = append(mine, item)
		status := pyjson.Str(or(item.Value("status"), ""))
		n, _ := counts.Value(status).(int64)
		counts.Set(status, n+1)
	}
	var active []*pyjson.Object
	for _, s := range mine {
		if activeStatuses[pyjson.Str(or(s.Value("status"), ""))] {
			active = append(active, s)
		}
	}
	recent := append([]*pyjson.Object(nil), mine...)
	key := func(s *pyjson.Object) string {
		return pyjson.Str(or(or(s.Value("last_activity_at"), s.Value("created_at")), ""))
	}
	sort.SliceStable(recent, func(i, j int) bool { return key(recent[i]) > key(recent[j]) })
	pick := append([]*pyjson.Object(nil), active...)
	for _, s := range recent {
		isActive := false
		for _, a := range active {
			isActive = isActive || a == s
		}
		if !isActive {
			pick = append(pick, s)
		}
	}
	items := []any{}
	for i, s := range pick {
		if i == 5 {
			break
		}
		items = append(items, pyjson.NewObject("id", s.Value("id"), "title", or(s.Value("title"), ""),
			"status", or(s.Value("status"), ""), "agent", or(or(s.Value("agent"), s.Value("platform")), ""),
			"task_ref", or(s.Value("task_ref"), ""), "last_activity_at", or(s.Value("last_activity_at"), s.Value("created_at"))))
	}
	return pyjson.NewObject("total", int64(len(mine)), "active", int64(len(active)), "counts", counts, "recent", items)
}

// ContextSnapshot is SessionManager.context_snapshot (false when missing).
func ContextSnapshot(s *Session) (*pyjson.Object, bool) {
	return readJSON(filepath.Join(s.Folder, "workspace", "ark-context.json"))
}

// ForWorktree is SessionManager.sessions_for_worktree.
func (m *Manager) ForWorktree(path string, activeOnly bool) []string {
	target := pytext.Strip(path)
	out := []string{}
	if target == "" {
		return out
	}
	resolved := resolvePath(target)
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range m.order {
		s := m.sessions[id]
		if activeOnly && !activeStatuses[strOr(s.Meta.Value("status"))] {
			continue
		}
		wt, ok := s.Meta.Value("worktree").(*pyjson.Object)
		if !ok {
			continue
		}
		if p := strOr(wt.Value("path")); p != "" && resolvePath(p) == resolved {
			out = append(out, s.ID)
		}
	}
	return out
}

// activeWorktreeSessionsLocked is _active_worktree_sessions.
func (m *Manager) activeWorktreeSessionsLocked(path, excludeID string) []string {
	target := pytext.Strip(path)
	out := []string{}
	if target == "" {
		return out
	}
	resolved := resolvePath(target)
	for _, id := range m.order {
		s := m.sessions[id]
		if s.ID == excludeID || !activeStatuses[strOr(s.Meta.Value("status"))] {
			continue
		}
		wt, ok := s.Meta.Value("worktree").(*pyjson.Object)
		if !ok {
			continue
		}
		if other := strOr(wt.Value("path")); other != "" && resolvePath(other) == resolved {
			out = append(out, s.ID)
		}
	}
	return out
}

// Groups is SessionManager.groups(): active agents by project / worktree.
func (m *Manager) Groups() *pyjson.Object {
	m.mu.Lock()
	type bucket struct {
		obj      *pyjson.Object
		sessions []any
	}
	var keys []string
	buckets := map[string]*bucket{}
	for _, id := range m.order {
		s := m.sessions[id]
		meta := s.Meta
		if !activeStatuses[strOr(meta.Value("status"))] {
			continue
		}
		project := objOr(meta.Value("project"))
		projectID := pyjson.Str(or(or(project.Value("id"), project.Value("path")), "project"))
		worktree, _ := meta.Value("worktree").(*pyjson.Object)
		path := ""
		if worktree != nil {
			path = strOr(worktree.Value("path"))
		} else {
			worktree = pyjson.NewObject()
		}
		key := "project:" + projectID
		if path != "" {
			key = "worktree:" + path
		}
		b := buckets[key]
		if b == nil {
			kind, label := "project", pyjson.Str(or(project.Value("name"), "Project"))
			var wt any
			if path != "" {
				kind = "worktree"
				label = pyjson.Str(or(worktree.Value("branch"), filepath.Base(path)))
				wt = pyjson.NewObject("path", path, "branch", or(worktree.Value("branch"), ""))
			}
			b = &bucket{obj: pyjson.NewObject("id", key, "kind", kind, "label", label,
				"project", pyjson.NewObject("id", project.Value("id"), "name", project.Value("name"), "path", project.Value("path")),
				"worktree", wt)}
			buckets[key] = b
			keys = append(keys, key)
		}
		b.sessions = append(b.sessions, pyjson.NewObject("id", s.ID, "title", or(meta.Value("title"), ""),
			"platform", or(meta.Value("platform"), ""), "status", or(meta.Value("status"), "")))
	}
	m.mu.Unlock()
	groups := make([]*pyjson.Object, 0, len(keys))
	active, conflicts := int64(0), int64(0)
	for _, k := range keys {
		b := buckets[k]
		n := int64(len(b.sessions))
		conflict := b.obj.Value("kind") == "worktree" && n > 1
		status := "busy"
		if conflict {
			status = "conflict"
			conflicts++
		}
		ids := []any{}
		for _, s := range b.sessions {
			ids = append(ids, s.(*pyjson.Object).Value("id"))
		}
		b.obj.Set("sessions", b.sessions).Set("active", n).Set("conflict", conflict).Set("status", status).Set("session_ids", ids)
		active += n
		groups = append(groups, b.obj)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		ci, cj := groups[i].Value("conflict") == true, groups[j].Value("conflict") == true
		if ci != cj {
			return ci
		}
		return pytext.Lower(pyjson.Str(groups[i].Value("label"))) < pytext.Lower(pyjson.Str(groups[j].Value("label")))
	})
	list := make([]any, len(groups))
	for i, g := range groups {
		list[i] = g
	}
	return pyjson.NewObject("groups", list, "counts", pyjson.NewObject("groups", int64(len(groups)), "active", active, "conflicts", conflicts))
}

// CanView is session_context.can_view.
func CanView(owner string, shared bool, user string, admin bool) bool {
	if admin || shared {
		return true
	}
	return owner != "" && pytext.Lower(owner) == pytext.Lower(user)
}

// CanManage is session_context.can_manage.
func CanManage(owner string, user string, admin bool) bool {
	if admin {
		return true
	}
	return owner != "" && pytext.Lower(owner) == pytext.Lower(user)
}

// Owner returns (actor, shared) of a session for the privacy checks.
func (m *Manager) Owner(s *Session) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return strOr(s.Meta.Value("actor")), pyjson.Truthy(s.Meta.Value("shared"))
}

// ProjectID is session.meta["project"]["id"] as a string.
func (m *Manager) ProjectID(s *Session) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return strOr(objOr(s.Meta.Value("project")).Value("id"))
}

// MetaValue reads one metadata key under the lock (a deep copy).
func (m *Manager) MetaValue(s *Session, key string) any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return deepCopy(s.Meta.Value(key))
}

// SetMeta assigns metadata keys and saves.
func (m *Manager) SetMeta(s *Session, kv ...any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := 0; i+1 < len(kv); i += 2 {
		s.Meta.Set(kv[i].(string), kv[i+1])
	}
	m.saveLocked(s)
}

// AccessibleIDs is Handler._accessible_session_ids for one project.
func (m *Manager) AccessibleIDs(projectID, user string, admin bool) map[string]bool {
	out := map[string]bool{}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range m.order {
		meta := m.sessions[id].Meta
		pid := strOr(objOr(meta.Value("project")).Value("id"))
		if pid == projectID && CanView(strOr(meta.Value("actor")), pyjson.Truthy(meta.Value("shared")), user, admin) {
			out[id] = true
		}
	}
	return out
}

// TakeAgentTokenIDs returns the ids of the agent tokens issued for the
// session's runs (Hooks.AgentToken) and forgets them: the caller revokes
// them when the run ends.
func (m *Manager) TakeAgentTokenIDs(s *Session) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	if ids, ok := s.Meta.Value("agent_token_ids").([]any); ok {
		for _, id := range ids {
			if v := strOr(id); v != "" {
				out = append(out, v)
			}
		}
	}
	if len(out) > 0 {
		s.Meta.Delete("agent_token_ids")
		m.saveLocked(s)
	}
	s.agentSecret = ""
	return out
}
