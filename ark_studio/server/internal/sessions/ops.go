package sessions

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"arkstudio/go/internal/platform/pyerr"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pyre"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/compat/pyurl"
	"arkstudio/go/internal/providers"
	"arkstudio/go/internal/rnd"
)

var (
	driveRe     = regexp.MustCompile(`\A[A-Za-z]:`)
	contextUID  = regexp.MustCompile(`\A[A-Za-z][A-Za-z0-9_-]{1,63}\z`)
	linkedUnit  = regexp.MustCompile(`\A[A-Za-z0-9_.:-]+\z`)
	nativeIDRe  = regexp.MustCompile(`\A[A-Za-z0-9._:-]{8,120}\z`)
	codexHeader = pyrePattern(`^\s*session id:\s*([A-Za-z0-9._:-]{8,120})\s*$`, pyre.FlagIgnoreCase)
)

// RequestStop is SessionManager.request_stop.
func (m *Manager) RequestStop(id string) (*pyjson.Object, error) {
	s, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	p := s.proc
	if p == nil || !p.running() {
		active := activeStatuses[strOr(s.Meta.Value("status"))]
		m.mu.Unlock()
		if active {
			var code *int
			if p != nil {
				// poll() of an exited child; the monitor finishes it too.
				code = nil
			}
			m.finish(s, code)
		}
		return m.Public(s, false), nil
	}
	if strOr(s.Meta.Value("status")) == "stopping" {
		out := m.publicLocked(s, false)
		m.mu.Unlock()
		return out, nil
	}
	s.stopRequested = true
	s.Meta.Set("status", "stopping")
	m.saveLocked(s)
	m.mu.Unlock()
	go m.terminate(p)
	return m.Public(s, false), nil
}

func contextPublic(e *pyjson.Object) *pyjson.Object {
	return pyjson.NewObject("id", e.Value("id"), "kind", e.Value("kind"), "label", e.Value("label"),
		"uid", or(e.Value("uid"), ""), "path", or(e.Value("path"), ""), "chars", intOr(e.Value("chars"), 0))
}

func (m *Manager) guess(name string) string {
	if m.GuessType != nil {
		return m.GuessType(name)
	}
	return ""
}

// contextEntries is SessionManager._context_entries.
func (m *Manager) contextEntries(h *Hooks, s *Session, attachments any) ([]*pyjson.Object, error) {
	if attachments == nil || attachments == "" {
		return nil, nil
	}
	list, ok := attachments.([]any)
	if !ok {
		return nil, serr("context must be a list", 400, "")
	}
	if len(list) == 0 {
		return nil, nil
	}
	if len(list) > maxContextItems {
		return nil, serr("at most "+strconv.Itoa(maxContextItems)+" context attachments are allowed", 400, "")
	}
	var entries []*pyjson.Object
	seen := map[string]bool{}
	total := 0
	for _, x := range list {
		raw, ok := x.(*pyjson.Object)
		if !ok {
			return nil, serr("each context attachment must be an object", 400, "")
		}
		kind := pytext.Lower(pytext.Strip(strOr(or(raw.Value("kind"), raw.Value("type")))))
		uid, relPath, content, label, text, key := "", "", "", "", "", ""
		switch kind {
		case "unit", "task":
			kind = "unit"
			uid = pytext.Strip(strOr(or(raw.Value("uid"), raw.Value("id"))))
			if !contextUID.MatchString(uid) {
				return nil, serr("a context unit needs a valid uid", 400, "")
			}
			var unit *pyjson.Object
			if h != nil && h.Unit != nil {
				if u, err := h.Unit(uid); err == nil {
					unit = u
				}
			}
			if unit != nil {
				if inner, ok := unit.Value("unit").(*pyjson.Object); ok {
					unit = inner
				}
			}
			if unit == nil || unit.Len() == 0 {
				return nil, serr("context unit "+uid+" was not found", 404, "")
			}
			title := pyjson.Str(or(unit.Value("title"), uid))
			shownUID := pyjson.Str(or(unit.Value("uid"), uid))
			label = limit(shownUID+" · "+title, 160)
			body := pytext.Strip(strOr(or(unit.Value("body"), unit.Value("excerpt"))))
			content = limit(body, maxContextText)
			lines := []string{"### Project unit `" + shownUID + "`", "- title: " + title,
				"- type: " + pyjson.Str(or(unit.Value("type"), "note")), "- status: " + pyjson.Str(or(unit.Value("status"), "—"))}
			if content != "" {
				lines = append(lines, "", content)
			}
			text = strings.Join(lines, "\n")
			key = "unit:" + uid
		case "file", "textfile":
			kind = "file"
			rawPath := pytext.Strip(strOr(raw.Value("path")))
			if rawPath == "" {
				return nil, serr("a context file needs a path", 400, "")
			}
			target, rel, err := m.artifactTarget(s, rawPath)
			if err != nil {
				return nil, err
			}
			relPath = rel
			fi, err := os.Stat(target)
			if err != nil || !fi.Mode().IsRegular() {
				return nil, serr("context file not found: "+relPath, 404, "")
			}
			if fi.Size() > maxTextArtifact {
				return nil, serr("context file is too large to attach: "+relPath, 413, "")
			}
			blob, err := os.ReadFile(target)
			if err != nil {
				return nil, serr("cannot read context file: "+err.Error(), 400, "")
			}
			if bytes.IndexByte(blob, 0) >= 0 {
				return nil, serr("binary file; attach it as media instead", 400, "")
			}
			content = limit(pyurl.DecodeReplace(blob), maxContextText)
			label = relPath
			text = "### File `" + relPath + "`\n\n```" + LanguageFor(filepath.Base(target)) + "\n" + content + "\n```"
			key = "file:" + relPath
		case "media", "image", "video", "audio":
			kind = "media"
			rawPath := pytext.Strip(strOr(raw.Value("path")))
			if rawPath == "" {
				return nil, serr("a context media attachment needs a path", 400, "")
			}
			target, rel, err := m.artifactTarget(s, rawPath)
			if err != nil {
				return nil, err
			}
			relPath = rel
			fi, err := os.Stat(target)
			if err != nil || !fi.Mode().IsRegular() {
				return nil, serr("context media not found: "+relPath, 404, "")
			}
			label = relPath
			mime := m.guess(filepath.Base(target))
			if mime == "" {
				mime = "application/octet-stream"
			}
			size := strconv.FormatInt(fi.Size(), 10)
			content = mime + ", " + size + " bytes"
			text = "### Media `" + relPath + "`\n- mime: " + mime + "\n- size: " + size + " bytes\n" +
				"- The file lives in this session's private workspace; open it " +
				"through the ARK artifact API if the platform supports images."
			key = "media:" + relPath
		case "text", "snippet":
			kind = "text"
			content = pytext.Strip(strOr(raw.Value("text")))
			if content == "" {
				return nil, serr("a text snippet cannot be empty", 400, "")
			}
			content = limit(content, maxContextText)
			first := ""
			if lines := pytext.SplitLines(content, false); len(lines) > 0 {
				first = limit(pytext.Strip(lines[0]), 60)
			}
			label = limit(pytext.Strip(strOr(raw.Value("label"))), 80)
			if label == "" {
				label = first
			}
			if label == "" {
				label = "Text snippet"
			}
			text = "### Snippet `" + label + "`\n\n```text\n" + content + "\n```"
			key = "text:" + limit(content, 120)
		default:
			return nil, serr("context kind must be unit, file, media, or text", 400, "")
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		total += utf8.RuneCountInString(text)
		if total > maxContextTotal {
			return nil, serr("attached context is too large", 400, "")
		}
		entries = append(entries, pyjson.NewObject("id", "ctx-"+strconv.Itoa(len(entries)+1), "kind", kind,
			"label", label, "uid", uid, "path", relPath, "chars", int64(utf8.RuneCountInString(content)), "text", text))
	}
	return entries, nil
}

// ValidateContext is SessionManager.validate_context.
func (m *Manager) ValidateContext(h *Hooks, id string, attachments any) (*pyjson.Object, error) {
	s, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	entries, err := m.contextEntries(h, s, attachments)
	if err != nil {
		return nil, err
	}
	items := make([]any, len(entries))
	for i, e := range entries {
		items[i] = contextPublic(e)
	}
	return pyjson.NewObject("session", s.ID, "items", items, "count", int64(len(entries))), nil
}

func renderContext(entries []*pyjson.Object) string {
	if len(entries) == 0 {
		return ""
	}
	parts := make([]string, len(entries))
	for i, e := range entries {
		parts[i] = strOr(e.Value("text"))
	}
	return "\n\n## Attached context\n\n" + strings.Join(parts, "\n\n")
}

func contextMarker(entries []*pyjson.Object) string {
	if len(entries) == 0 {
		return ""
	}
	labels := make([]string, len(entries))
	for i, e := range entries {
		labels[i] = pyjson.Str(or(e.Value("label"), e.Value("kind")))
	}
	return "\n\n_[context attached: " + strconv.Itoa(len(entries)) + " item(s) — " + strings.Join(labels, ", ") + "]_"
}

func composeMessage(text string, entries []*pyjson.Object) string {
	body := strings.Trim(text, "\r\n")
	if body == "" && len(entries) > 0 {
		body = "Use the attached context."
	}
	return body + renderContext(entries)
}

func findApprovalLocked(s *Session, requestID string) *pyjson.Object {
	for _, r := range objects(s.Meta.Value("approvals")) {
		if pyjson.Str(r.Value("id")) == requestID {
			return r
		}
	}
	return nil
}

func matchApprovalRuleLocked(s *Session, record *pyjson.Object) *pyjson.Object {
	kind := strOr(record.Value("kind"))
	tool := pytext.Lower(pytext.Strip(strOr(record.Value("tool"))))
	for _, rule := range objects(s.Meta.Value("approval_rules")) {
		if strOr(rule.Value("kind")) != kind {
			continue
		}
		if pytext.Lower(pytext.Strip(strOr(rule.Value("tool")))) == tool {
			return rule
		}
	}
	return nil
}

func approvalPolicyLocked(s *Session, record *pyjson.Object) (string, string, string) {
	mode := pyjson.Str(or(s.Meta.Value("access_mode"), "read_only"))
	kind := pyjson.Str(or(record.Value("kind"), "other"))
	label := permissionLabels[mode]
	if label == "" {
		label = mode
	}
	if mode == "full_access" {
		return "allow", "auto", "full-access mode auto-approves mutating actions"
	}
	if isReadOnlyMode(mode) {
		return "deny", "auto", label + " mode denies mutating actions"
	}
	if isApprovalMode(mode) {
		if mode == "accept_edits" && editKinds[kind] {
			return "allow", "auto", "accept-edits mode auto-approves file edits"
		}
		if matchApprovalRuleLocked(s, record) != nil {
			return "allow", "always", "matched an always-allow rule for this tool"
		}
	}
	return "", "", ""
}

func (m *Manager) deliverPermissionResponse(s *Session, record *pyjson.Object, decision, scope, message string) bool {
	m.mu.Lock()
	if decision == "" {
		decision = strOr(record.Value("decision"))
	}
	decision = pytext.Lower(decision)
	if scope == "" {
		scope = pyjson.Str(or(record.Value("scope"), "once"))
	}
	frame := BuildPermissionResponse(deepCopy(record).(*pyjson.Object), decision, scope, message)
	m.mu.Unlock()
	if decision != "allow" && decision != "deny" {
		return false
	}
	return m.writeInput(s, string(pyjson.Dumps(frame)), false, nil) == nil
}

// observeNativeID is SessionManager.observe_native_id.
func (m *Manager) observeNativeID(s *Session, line string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if pyjson.Truthy(s.Meta.Value("native_session_id")) {
		return
	}
	if found := NativeSessionIDFromLine(line); found != "" {
		s.Meta.Set("native_session_id", found)
		m.saveLocked(s)
	}
}

func pyrePattern(p string, flags int) func(string) (string, bool) {
	re := pyre.MustCompile(p, flags)
	return func(text string) (string, bool) {
		m, s, ok := reSearch(re, text)
		if !ok {
			return "", false
		}
		return reGroup(m, s, 1), true
	}
}

// NativeSessionIDFromLine is native_session_id_from_line.
func NativeSessionIDFromLine(line string) string {
	text := pytext.Strip(line)
	if id, ok := codexHeader(text); ok {
		return id
	}
	obj, ok := jsonLineObject(text)
	if !ok {
		return ""
	}
	etype := strOr(obj.Value("type"))
	candidate := ""
	if etype == "thread.started" {
		candidate = strOr(obj.Value("thread_id"))
	} else if etype == "system" || etype == "result" || obj.Has("session_id") {
		candidate = strOr(obj.Value("session_id"))
	}
	if nativeIDRe.MatchString(candidate) {
		return candidate
	}
	return ""
}

// Resumable is SessionManager.resumable.
func (m *Manager) Resumable(s *Session) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return pyjson.Truthy(s.Meta.Value("native_session_id")) && contains(NativeResumePlatforms, strOr(s.Meta.Value("platform")))
}

// UpdateContext is SessionManager.update_context.
func (m *Manager) UpdateContext(id string, body *pyjson.Object) (*pyjson.Object, error) {
	s, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if body.Has("title") {
		if title := limit(pytext.Strip(strOr(body.Value("title"))), maxTitle); title != "" {
			s.Meta.Set("title", title)
		}
	}
	if body.Has("linked_units") {
		links := body.Value("linked_units")
		if str, ok := links.(string); ok {
			var parts []any
			for _, p := range strings.Split(str, ",") {
				parts = append(parts, pytext.Strip(p))
			}
			links = parts
		}
		list, ok := links.([]any)
		if !ok {
			m.mu.Unlock()
			return nil, serr("linked_units must be a list of node ids", 400, "")
		}
		out := []any{}
		for _, uid := range list {
			text := pytext.Strip(pyjson.Str(uid))
			probe := text
			if probe == "" {
				probe = "-"
			}
			if linkedUnit.MatchString(probe) {
				out = append(out, limit(text, 40))
			}
		}
		if len(out) > 20 {
			out = out[:20]
		}
		s.Meta.Set("linked_units", out)
	}
	if body.Has("shared") {
		b, ok := body.Value("shared").(bool)
		if !ok {
			m.mu.Unlock()
			return nil, serr("shared must be true or false", 400, "SESSION_SHARED_INVALID")
		}
		s.Meta.Set("shared", b)
	}
	m.saveLocked(s)
	m.mu.Unlock()
	m.notify(s, "update")
	return m.Public(s, false), nil
}

// observeOutputLine is SessionManager.observe_output_line.
func (m *Manager) observeOutputLine(s *Session, line string) {
	if request := PermissionRequestFromLine(line); request != nil {
		m.registerPermissionRequest(s, request)
		return
	}
	resolution := PermissionResolutionFromLine(line)
	if resolution == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	record := findApprovalLocked(s, strOr(resolution.Value("request_id")))
	if record != nil && record.Value("status") == "pending" {
		record.Set("status", pyjson.Str(or(resolution.Value("status"), "resolved"))).
			Set("message", strOr(resolution.Value("message"))).Set("decided_at", NowISO()).Set("decided_by", "adapter")
		m.saveLocked(s)
	}
}

func (m *Manager) registerPermissionRequest(s *Session, request *pyjson.Object) {
	request = request.Copy()
	requestID := pytext.Strip(strOr(request.Value("request_id")))
	m.mu.Lock()
	if requestID != "" {
		if findApprovalLocked(s, requestID) != nil {
			m.mu.Unlock()
			return
		}
	}
	if requestID == "" {
		requestID = "ap_" + rnd.Hex("sessions", 6)
		request.Set("request_id", requestID)
	}
	input := request.Value("input")
	encoded := string(pyjson.Dumps(input))
	if n := utf8.RuneCountInString(encoded); n > 8000 {
		input = pyjson.NewObject("truncated", limit(encoded, 8000), "original_chars", int64(n))
	}
	now := NowISO()
	record := pyjson.NewObject("id", requestID, "kind", pyjson.Str(or(request.Value("kind"), "other")),
		"tool", strOr(request.Value("tool")), "command", strOr(request.Value("command")),
		"path", strOr(request.Value("path")), "description", strOr(request.Value("description")),
		"input", input, "protocol", pyjson.Str(or(request.Value("protocol"), ApprovalProtocol)),
		"native_id", strOr(request.Value("native_id")), "status", "pending", "decision", "", "scope", "",
		"auto", false, "message", "", "mode", pyjson.Str(or(s.Meta.Value("access_mode"), "read_only")),
		"requested_at", now, "decided_at", nil, "decided_by", "")
	approvals, ok := s.Meta.Value("approvals").([]any)
	if !ok {
		approvals = []any{}
	}
	if len(approvals) >= maxApprovals {
		removed := false
		for i, old := range approvals {
			if o, ok := old.(*pyjson.Object); !ok || o.Value("status") != "pending" {
				approvals = append(approvals[:i:i], approvals[i+1:]...)
				removed = true
				break
			}
		}
		if !removed {
			approvals = approvals[1:]
		}
	}
	approvals = append(approvals, record)
	s.Meta.Set("approvals", approvals)
	s.Meta.Set("last_activity_at", NowISO())
	decision, scope, reason := approvalPolicyLocked(s, record)
	if decision != "" {
		status := "denied"
		if decision == "allow" {
			status = "allowed"
		}
		record.Set("status", status).Set("decision", decision).Set("scope", scope).Set("auto", true).
			Set("message", reason).Set("decided_at", NowISO()).Set("decided_by", "policy")
	}
	m.saveLocked(s)
	m.mu.Unlock()
	if decision != "" {
		if !m.deliverPermissionResponse(s, record, decision, scope, reason) {
			m.mu.Lock()
			record.Set("status", "failed").Set("message",
				strings.Trim(reason+"; the response could not be written to adapter stdin", "; "))
			m.saveLocked(s)
			m.mu.Unlock()
		}
	}
}

// Approvals is SessionManager.approvals.
func (m *Manager) Approvals(id string) (*pyjson.Object, error) {
	s, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	items, pending, rules := approvalState(deepCopy(s.Meta).(*pyjson.Object))
	status := m.publicLocked(s, false).Value("status")
	return pyjson.NewObject("session", s.ID, "status", status, "items", items, "pending", pending,
		"rules", rules, "protocol", ApprovalProtocol), nil
}

// DecideApproval is SessionManager.decide_approval.
func (m *Manager) DecideApproval(id, requestID, decision, message, actor string) (*pyjson.Object, error) {
	s, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	var allow bool
	var scope string
	switch modeToken(decision) {
	case "allow", "allow_once", "once", "approve":
		allow, scope = true, "once"
	case "always", "allow_always", "always_allow", "allow_for_session":
		allow, scope = true, "always"
	case "deny", "deny_once", "reject", "block":
		allow, scope = false, "once"
	default:
		return nil, serr("decision must be allow, always or deny", 400, "")
	}
	message = limit(pytext.Strip(message), maxApprovalMessage)
	m.mu.Lock()
	record := findApprovalLocked(s, requestID)
	if record == nil {
		m.mu.Unlock()
		return nil, serr("approval request not found", 404, "")
	}
	status := pyjson.Str(or(record.Value("status"), "pending"))
	if status != "pending" {
		m.mu.Unlock()
		return nil, serr("approval request was already "+status, 409, "")
	}
	if !m.isActiveLocked(s) {
		record.Set("status", "expired").Set("message", "the session is not running").
			Set("decided_at", NowISO()).Set("decided_by", actor)
		m.saveLocked(s)
		m.mu.Unlock()
		return nil, serr("the session is not running; the approval request expired", 409, "")
	}
	d := "deny"
	if allow {
		d = "allow"
	}
	msg := message
	if msg == "" {
		msg = "denied by user"
		if allow {
			msg = "approved by user"
		}
	}
	record.Set("status", "deciding").Set("decision", d).Set("scope", scope).Set("auto", false).
		Set("message", msg).Set("decided_at", NowISO()).Set("decided_by", actor)
	if scope == "always" && allow {
		rules, _ := s.Meta.Value("approval_rules").([]any)
		if !s.Meta.Has("approval_rules") {
			rules = []any{}
		}
		kind := pyjson.Str(or(record.Value("kind"), "other"))
		tool := strOr(record.Value("tool"))
		dup := false
		for _, r := range objects(rules) {
			dup = dup || (r.Value("kind") == kind && pytext.Lower(strOr(r.Value("tool"))) == pytext.Lower(tool))
		}
		if !dup {
			rules = append(rules, pyjson.NewObject("kind", kind, "tool", tool, "created_at", NowISO(), "created_by", actor))
			s.Meta.Set("approval_rules", rules)
		}
	}
	m.saveLocked(s)
	m.mu.Unlock()
	delivered := m.deliverPermissionResponse(s, record, d, scope, message)
	m.mu.Lock()
	if delivered {
		st := "denied"
		if allow {
			st = "allowed"
		}
		record.Set("status", st)
	} else {
		record.Set("status", "failed").Set("message", pytext.Strip(message+" — the decision could not be written to the adapter stdin"))
	}
	m.saveLocked(s)
	m.mu.Unlock()
	if !delivered {
		return nil, serr("the decision was recorded but could not be written to the adapter stdin", 409, "")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return pyjson.NewObject("session", m.publicLocked(s, true), "approval", approvalPublic(record),
		"decision", d, "scope", scope), nil
}

func (m *Manager) steerQueueLocked(s *Session) []any {
	q, _ := s.Meta.Value("steer_queue").([]any)
	return q
}

// Steer is SessionManager.steer.
func (m *Manager) Steer(h *Hooks, id, text string, context any) (*pyjson.Object, error) {
	s, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	text = pytext.Strip(text)
	entries, err := m.contextEntries(h, s, context)
	if err != nil {
		return nil, err
	}
	if text == "" && len(entries) == 0 {
		return nil, serr("steer text or attached context is required", 400, "")
	}
	if utf8.RuneCountInString(text) > maxSteerText {
		return nil, serr("steer text is too long", 400, "")
	}
	if !m.IsActive(s) {
		return nil, serr("session is not running; send a continue message", 409, "")
	}
	payload := composeMessage(text, entries)
	if utf8.RuneCountInString(payload) > maxPrompt {
		return nil, serr("steer text plus attached context is too long", 400, "")
	}
	m.mu.Lock()
	followup := pyjson.Truthy(s.Meta.Value("followup"))
	channel := "next_turn"
	if followup {
		channel = "stdin"
	}
	shown := text
	if shown == "" {
		shown = "Use the attached context."
	}
	item := pyjson.NewObject("id", "st_"+rnd.Hex("sessions", 4), "text", shown, "status", "queued",
		"channel", channel, "created_at", NowISO(), "delivered_at", nil, "error", "")
	queue := m.steerQueueLocked(s)
	if !s.Meta.Has("steer_queue") || queue == nil {
		queue = []any{}
	}
	if len(queue) >= maxSteerQueue {
		var kept []any
		for _, e := range queue {
			if o, ok := e.(*pyjson.Object); !ok || o.Value("status") != "delivered" {
				kept = append(kept, e)
			}
		}
		queue = kept
		if queue == nil {
			queue = []any{}
		}
	}
	if len(queue) >= maxSteerQueue {
		s.Meta.Set("steer_queue", queue)
		m.mu.Unlock()
		return nil, serr("the steer queue is full", 409, "")
	}
	queue = append(queue, item)
	s.Meta.Set("steer_queue", queue).Set("last_activity_at", NowISO())
	m.saveLocked(s)
	m.mu.Unlock()
	if followup {
		display := shown + contextMarker(entries)
		if err := m.writeInput(s, payload, true, &display); err != nil {
			m.mu.Lock()
			item.Set("status", "failed").Set("error", err.Error())
			m.saveLocked(s)
			m.mu.Unlock()
			return nil, serr("steer queued but delivery failed: "+err.Error(), 409, "")
		}
		m.mu.Lock()
		item.Set("status", "delivered").Set("delivered_at", NowISO())
		m.saveLocked(s)
		m.mu.Unlock()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return pyjson.NewObject("session", m.publicLocked(s, true), "steer", steerPublic(item)), nil
}

// RemoveSteer is SessionManager.remove_steer.
func (m *Manager) RemoveSteer(id, steerID string) (*pyjson.Object, error) {
	s, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	queue := m.steerQueueLocked(s)
	kept := []any{}
	for _, e := range queue {
		if o, ok := e.(*pyjson.Object); ok && store_eq(o.Value("id"), steerID) {
			continue
		}
		kept = append(kept, e)
	}
	if len(kept) == len(queue) {
		return nil, serr("steer message not found", 404, "")
	}
	s.Meta.Set("steer_queue", kept)
	m.saveLocked(s)
	return m.publicLocked(s, true), nil
}

// UpdateSteer is SessionManager.update_steer.
func (m *Manager) UpdateSteer(id, steerID, text string) (*pyjson.Object, error) {
	s, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	text = pytext.Strip(text)
	if text == "" {
		return nil, serr("steer text is required", 400, "")
	}
	if utf8.RuneCountInString(text) > maxSteerText {
		return nil, serr("steer text is too long", 400, "")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var item *pyjson.Object
	for _, e := range objects(s.Meta.Value("steer_queue")) {
		if store_eq(e.Value("id"), steerID) {
			item = e
			break
		}
	}
	if item == nil {
		return nil, serr("steer message not found", 404, "")
	}
	if item.Value("status") == "delivered" {
		return nil, serr("this steer was already delivered to the process stdin; it cannot be edited", 409, "")
	}
	item.Set("text", text).Set("edited_at", NowISO()).Set("status", "queued").Set("error", "")
	m.saveLocked(s)
	return m.publicLocked(s, true), nil
}

// ReorderSteer is SessionManager.reorder_steer.
func (m *Manager) ReorderSteer(id string, order any) (*pyjson.Object, error) {
	s, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	if str, ok := order.(string); ok {
		var parts []any
		for _, p := range strings.Split(str, ",") {
			if p = pytext.Strip(p); p != "" {
				parts = append(parts, p)
			}
		}
		if parts == nil {
			parts = []any{}
		}
		order = parts
	}
	list, ok := order.([]any)
	if !ok {
		return nil, serr("order must be a list of steer ids", 400, "")
	}
	ids := make([]string, 0, len(list))
	for i, x := range list {
		if i == maxSteerQueue {
			break
		}
		ids = append(ids, pyjson.Str(x))
	}
	set := map[string]bool{}
	for _, x := range ids {
		set[x] = true
	}
	if len(set) != len(ids) {
		return nil, serr("order contains duplicate steer ids", 400, "")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	queue := objects(m.steerQueueLocked(s))
	queued := map[string]bool{}
	for _, e := range queue {
		if e.Value("status") != "delivered" {
			queued[pyjson.Str(e.Value("id"))] = true
		}
	}
	same := len(queued) == len(set)
	for k := range set {
		same = same && queued[k]
	}
	if !same {
		return nil, serr("order must list every queued steer message exactly once", 400, "")
	}
	byID := map[string]*pyjson.Object{}
	for _, e := range queue {
		byID[pyjson.Str(e.Value("id"))] = e
	}
	next := 0
	rebuilt := make([]any, 0, len(queue))
	for _, e := range queue {
		if e.Value("status") == "delivered" {
			rebuilt = append(rebuilt, e)
		} else {
			rebuilt = append(rebuilt, byID[ids[next]])
			next++
		}
	}
	s.Meta.Set("steer_queue", rebuilt)
	m.saveLocked(s)
	return m.publicLocked(s, true), nil
}

func (m *Manager) drainSteerLocked(s *Session) []string {
	var out []string
	now := NowISO()
	for _, e := range objects(s.Meta.Value("steer_queue")) {
		if e.Value("status") == "delivered" {
			continue
		}
		text := strOr(e.Value("text"))
		e.Set("status", "delivered").Set("delivered_at", now).Set("channel", "continue").Set("error", "")
		if pytext.Strip(text) != "" {
			out = append(out, text)
		}
	}
	return out
}

// SendInput is SessionManager.send_input.
func (m *Manager) SendInput(h *Hooks, id, text string, context any) (*pyjson.Object, error) {
	s, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	text = pytext.Strip(text)
	entries, err := m.contextEntries(h, s, context)
	if err != nil {
		return nil, err
	}
	if text == "" && len(entries) == 0 {
		return nil, serr("input is required", 400, "")
	}
	if utf8.RuneCountInString(text) > maxInput {
		return nil, serr("input is too long", 400, "")
	}
	if !m.IsActive(s) {
		return nil, serr("session is not running", 409, "")
	}
	m.mu.Lock()
	followup := pyjson.Truthy(s.Meta.Value("followup"))
	m.mu.Unlock()
	if !followup {
		return nil, serr("this platform does not accept follow-up input", 409, "")
	}
	payload := composeMessage(text, entries)
	if utf8.RuneCountInString(payload) > maxPrompt {
		return nil, serr("input plus attached context is too long", 400, "")
	}
	shown := text
	if shown == "" {
		shown = "Use the attached context."
	}
	display := shown + contextMarker(entries)
	if err := m.writeInput(s, payload, true, &display); err != nil {
		return nil, err
	}
	return m.Public(s, true), nil
}

// readLogLines is path.read_text(errors="replace").splitlines().
func readLogLines(path string) ([]string, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	text := strings.ReplaceAll(strings.ReplaceAll(pyurl.DecodeReplace(raw), "\r\n", "\n"), "\r", "\n")
	return pytext.SplitLines(text, false), true
}

// Logs is SessionManager.logs.
func (m *Manager) Logs(id, stream string, offset, lim int) (*pyjson.Object, error) {
	s, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	if stream != "stdout" && stream != "stderr" {
		return nil, serr("stream must be stdout or stderr", 400, "")
	}
	path := filepath.Join(s.Folder, stream+".jsonl")
	entries := []any{}
	total := 0
	if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() {
		s.logMu.Lock()
		lines, _ := readLogLines(path)
		s.logMu.Unlock()
		total = len(lines)
		offset = max(0, min(offset, total))
		lim = max(1, min(lim, maxLogLimit))
		end := min(offset+lim, total)
		for _, raw := range lines[offset:end] {
			if o, ok := parseObject(raw); ok {
				entries = append(entries, o)
			}
		}
	}
	return pyjson.NewObject("session", id, "stream", stream, "entries", entries,
		"offset", int64(offset+len(entries)), "total", int64(total), "status", m.Public(s, false).Value("status")), nil
}

func (m *Manager) allLogEntries(s *Session, stream string) []*pyjson.Object {
	path := filepath.Join(s.Folder, stream+".jsonl")
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	s.logMu.Lock()
	lines, ok := readLogLines(path)
	s.logMu.Unlock()
	if !ok {
		return nil
	}
	if len(lines) > MaxSourceLines {
		lines = lines[len(lines)-MaxSourceLines:]
	}
	var out []*pyjson.Object
	for _, raw := range lines {
		if o, ok := parseObject(raw); ok {
			out = append(out, o)
		}
	}
	return out
}

// Transcript is SessionManager.transcript.
func (m *Manager) Transcript(id string) (*pyjson.Object, error) {
	s, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	payload := m.Public(s, true)
	m.mu.Lock()
	platform := strOr(s.Meta.Value("platform"))
	m.mu.Unlock()
	data := BuildTranscript(TranscriptInput{SessionID: s.ID, Status: pyjson.Str(payload.Value("status")),
		Prompt: strOr(payload.Value("prompt")), PromptAt: or(payload.Value("started_at"), payload.Value("created_at")),
		Stdout: m.allLogEntries(s, "stdout"), Stderr: m.allLogEntries(s, "stderr"),
		Error: strOr(payload.Value("error")), Platform: platform, Notice: strOr(payload.Value("notice"))})
	approvals := objOr(payload.Value("approvals"))
	recorded := map[string]*pyjson.Object{}
	for _, item := range objects(approvals.Value("items")) {
		recorded[pyjson.Str(item.Value("id"))] = item
	}
	items := objects(data.Value("items"))
	for _, item := range items {
		if item.Value("type") != "permission" {
			continue
		}
		record := recorded[strOr(item.Value("request_id"))]
		if record == nil {
			if !pyjson.Truthy(data.Value("active")) {
				item.Set("status", "expired")
			}
			continue
		}
		item.Set("status", or(record.Value("status"), item.Value("status")))
		item.Set("decision", or(record.Value("decision"), "")).Set("scope", or(record.Value("scope"), "")).
			Set("auto", pyjson.Truthy(record.Value("auto"))).Set("message", or(record.Value("message"), "")).
			Set("mode", or(record.Value("mode"), "")).Set("decided_at", record.Value("decided_at")).
			Set("decided_by", or(record.Value("decided_by"), ""))
		for _, key := range []string{"kind", "tool", "command", "path", "description"} {
			if v := record.Value(key); pyjson.Truthy(v) {
				item.Set(key, v)
			}
		}
	}
	pending := int64(0)
	for _, item := range items {
		if item.Value("type") == "permission" && item.Value("status") == "pending" {
			pending++
		}
	}
	data.Value("stats").(*pyjson.Object).Set("pending_permissions", pending)
	data.Set("approvals", approvals)
	data.Set("permission_mode", or(payload.Value("permission_mode"), payload.Value("access_mode")))
	data.Set("permission_label", or(payload.Value("permission_label"), payload.Value("access_label")))
	data.Set("permission_history", or(payload.Value("permission_history"), []any{}))
	data.Set("permission_modes", or(payload.Value("permission_modes"), []any{}))
	data.Set("supports_approval", pyjson.Truthy(approvals.Value("supported")))
	data.Set("turn", or(payload.Value("turn"), int64(1)))
	data.Set("signature", strings.Join([]string{pyjson.Str(data.Value("signature")), pyjson.Str(data.Value("permission_mode")),
		pyjson.Str(pending), permissionSignature(items)}, "|"))
	data.Set("counts", objOr(payload.Value("counts")))
	data.Set("raw_status", or(payload.Value("raw_status"), payload.Value("status")))
	return data, nil
}

// WorkspaceIndex is SessionManager.workspace_index.
func (m *Manager) WorkspaceIndex(id string) (*pyjson.Object, error) {
	s, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	index := IndexWorkspace(s.Workspace(), m.guess)
	out := pyjson.NewObject("session", s.ID)
	for _, k := range index.Keys() {
		out.Set(k, index.Value(k))
	}
	return out, nil
}

// artifactTarget is SessionManager._artifact_target.
func (m *Manager) artifactTarget(s *Session, rel string) (string, string, error) {
	raw := pytext.Strip(strings.ReplaceAll(rel, "\\", "/"))
	if strings.HasPrefix(raw, "/") || driveRe.MatchString(raw) {
		return "", "", serr("absolute paths are not allowed", 400, "")
	}
	var parts []string
	for _, p := range strings.Split(raw, "/") {
		if p == "" || p == "." {
			continue
		}
		if p == ".." {
			return "", "", serr("path traversal is not allowed", 400, "")
		}
		parts = append(parts, p)
	}
	root := resolvePath(s.Workspace())
	target := root
	if len(parts) > 0 {
		target = resolvePath(filepath.Join(append([]string{root}, parts...)...))
	}
	if target != root && !strings.HasPrefix(target, root+string(filepath.Separator)) {
		return "", "", serr("path is outside the session workspace", 403, "")
	}
	if isSymlink(target) {
		return "", "", serr("symlinks are not served", 400, "")
	}
	return target, strings.Join(parts, "/"), nil
}

// ListArtifacts is SessionManager.list_artifacts.
func (m *Manager) ListArtifacts(id, rel string) (*pyjson.Object, error) {
	s, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	target, relPath, err := m.artifactTarget(s, rel)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(target)
	if err != nil {
		return nil, serr("folder not found", 404, "")
	}
	if !fi.IsDir() {
		return nil, serr("not a folder", 400, "")
	}
	children, err := sortedChildren(target)
	if err != nil {
		return nil, serr("cannot list folder: "+err.Error(), 400, "")
	}
	entries := []any{}
	truncated := false
	for _, child := range children {
		if len(entries) >= maxArtifactEntries {
			truncated = true
			break
		}
		path := filepath.Join(target, child.Name())
		if isSymlink(path) {
			continue
		}
		st, err := os.Stat(path)
		if err != nil {
			continue
		}
		dir := st.IsDir()
		p := child.Name()
		if relPath != "" {
			p = relPath + "/" + child.Name()
		}
		kind, size, language := "file", st.Size(), LanguageFor(child.Name())
		if dir {
			kind, size, language = "dir", 0, ""
		}
		entries = append(entries, pyjson.NewObject("name", child.Name(), "path", p, "type", kind, "size", size,
			"mtime", isoMtime(st.ModTime()), "text", !dir && textSuffixes[pytext.Lower(pySuffix(child.Name()))],
			"language", language))
	}
	var parent any
	if relPath != "" {
		parts := strings.Split(relPath, "/")
		parent = strings.Join(parts[:len(parts)-1], "/")
	}
	return pyjson.NewObject("session", id, "path", relPath, "parent", parent, "entries", entries,
		"truncated", truncated, "count", int64(len(entries))), nil
}

// ReadArtifact is SessionManager.read_artifact.
func (m *Manager) ReadArtifact(id, rel string) (*pyjson.Object, error) {
	s, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	target, relPath, err := m.artifactTarget(s, rel)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(target)
	if err != nil || !fi.Mode().IsRegular() {
		return nil, serr("file not found", 404, "")
	}
	if fi.Size() > maxTextArtifact {
		return nil, serr("file is too large to preview; download it instead", 413, "")
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		return nil, serr("cannot read file: "+err.Error(), 400, "")
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		return nil, serr("binary file; download it instead", 415, "")
	}
	text := strings.TrimLeft(pyurl.DecodeReplace(raw), string(rune(0xFEFF)))
	lines := 0
	if text != "" {
		lines = len(pytext.SplitLines(text, false))
	}
	name := filepath.Base(target)
	mime := m.guess(name)
	if mime == "" {
		mime = "text/plain"
	}
	return pyjson.NewObject("session", id, "path", relPath, "name", name, "content", text, "size", fi.Size(),
		"mime", mime, "language", LanguageFor(name), "lines", int64(lines), "truncated", false), nil
}

// ArtifactDownload is SessionManager.artifact_download: (path, type, name).
func (m *Manager) ArtifactDownload(id, rel string) (string, string, string, error) {
	s, err := m.Get(id)
	if err != nil {
		return "", "", "", err
	}
	target, _, err := m.artifactTarget(s, rel)
	if err != nil {
		return "", "", "", err
	}
	fi, err := os.Stat(target)
	if err != nil || !fi.Mode().IsRegular() {
		return "", "", "", serr("file not found", 404, "")
	}
	if fi.Size() > maxDownload {
		return "", "", "", serr("file is too large to download", 413, "")
	}
	name := filepath.Base(target)
	ctype := m.guess(name)
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	return target, ctype, name, nil
}

// ArtifactFile is SessionManager.artifact_file.
func (m *Manager) ArtifactFile(id, rel string) (string, error) {
	s, err := m.Get(id)
	if err != nil {
		return "", err
	}
	target, _, err := m.artifactTarget(s, rel)
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(target); err != nil || !fi.Mode().IsRegular() {
		return "", serr("file not found", 404, "")
	}
	return target, nil
}

// outputTail is SessionManager._output_tail.
//
// A log line that is valid JSON but not an object raises AttributeError in
// Python (entry.get), which reaches the caller as a 500; it is returned as
// an error here.
func (m *Manager) outputTail(s *Session, lim int) (string, error) {
	path := filepath.Join(s.Folder, "stdout.jsonl")
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return "", nil
	}
	lines, ok := readLogLines(path)
	if !ok {
		return "", nil
	}
	if len(lines) > 80 {
		lines = lines[len(lines)-80:]
	}
	var chunks []string
	size := 0
	for i := len(lines) - 1; i >= 0; i-- {
		v, err := pyjson.Loads(lines[i])
		if err != nil {
			continue
		}
		entry, ok := v.(*pyjson.Object)
		if !ok {
			return "", errors.New("'" + pyerr.TypeName(v) + "' object has no attribute 'get'")
		}
		if entry.Value("kind") == "input" {
			continue
		}
		line := pytext.RStrip(strOr(entry.Value("line")))
		if line == "" {
			continue
		}
		chunks = append(chunks, line)
		size += utf8.RuneCountInString(line) + 1
		if size >= lim {
			break
		}
	}
	for i, j := 0, len(chunks)-1; i < j; i, j = i+1, j-1 {
		chunks[i], chunks[j] = chunks[j], chunks[i]
	}
	text := []rune(strings.Join(chunks, "\n"))
	if len(text) > lim {
		text = text[len(text)-lim:]
	}
	return string(text), nil
}

// ContinueOptions are the optional overrides of continue_session (nil =
// None, i.e. keep the session's value).
type ContinueOptions struct {
	Platform, Model, Effort, AccessMode any
	Context                             any
	Actor                               string
	Resume                              bool
}

func isNoneOrEmpty(v any) bool { return v == nil || v == "" }

// ContinueSession is SessionManager.continue_session.
func (m *Manager) ContinueSession(h *Hooks, id, text string, o ContinueOptions) (*pyjson.Object, error) {
	s, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	actor := o.Actor
	m.mu.Lock()
	if actor == "" {
		actor = strOr(s.Meta.Value("actor"))
	}
	platformNow := s.Meta.Value("platform")
	m.mu.Unlock()
	if o.Resume {
		if !m.Resumable(s) {
			return nil, serr("this session has no provider session id to resume", 409, "SESSION_NOT_RESUMABLE")
		}
		if !isNoneOrEmpty(o.Platform) && !pyEq(o.Platform, platformNow) {
			return nil, serr("a provider-native resume keeps the original platform", 400, "SESSION_RESUME_PLATFORM")
		}
	}
	text = limit(pytext.Strip(text), maxPrompt)
	overrides := !isNoneOrEmpty(o.Platform) || !isNoneOrEmpty(o.Model) || !isNoneOrEmpty(o.Effort) || !isNoneOrEmpty(o.AccessMode)
	if m.IsActive(s) {
		if overrides {
			return nil, serr("stop the running session before changing provider or access", 409, "")
		}
		m.mu.Lock()
		followup := pyjson.Truthy(s.Meta.Value("followup"))
		m.mu.Unlock()
		if followup {
			return m.SendInput(h, id, text, o.Context)
		}
		return nil, serr("stop the running one-shot session before correcting it", 409, "")
	}
	entries, err := m.contextEntries(h, s, o.Context)
	if err != nil {
		return nil, err
	}
	if text == "" && len(entries) == 0 {
		return nil, serr("correction text or attached context is required", 400, "")
	}
	newPlatform := pytext.Strip(strOr(or(o.Platform, platformNow)))
	preset, err := m.Preset(newPlatform, AgentOverrides(h, actor))
	if err != nil {
		return nil, err
	}
	if !providers.Available(preset) {
		return nil, serr(pyjson.Str(preset.Value("label"))+" is not installed or configured", 400, "")
	}
	m.mu.Lock()
	accessIn := o.AccessMode
	if isNoneOrEmpty(accessIn) {
		accessIn = s.Meta.Value("access_mode")
	}
	newEffort := strOr(s.Meta.Value("effort"))
	if o.Effort != nil {
		newEffort = limit(pytext.Strip(strOr(o.Effort)), 40)
	}
	modelIn := o.Model
	if modelIn == nil {
		modelIn = or(s.Meta.Value("model"), "")
	}
	newModel := limit(pytext.Strip(pyjson.Str(modelIn)), 80)
	original := pytext.Strip(strOr(s.Meta.Value("prompt")))
	m.mu.Unlock()
	newAccess, err := m.validatePermissionMode(accessIn, preset)
	if err != nil {
		return nil, err
	}
	if err := m.checkEffort(preset, newModel, newEffort); err != nil {
		return nil, err
	}
	tail, err := m.outputTail(s, 4000)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	queued := m.drainSteerLocked(s)
	m.mu.Unlock()
	var prompt string
	if o.Resume {
		prompt = text
		if len(queued) > 0 {
			prompt += "\n\n## Queued interventions\n" + strings.Join(queued, "\n\n")
		}
		if len(entries) > 0 {
			prompt += renderContext(entries)
		}
		prompt = limit(pytext.Strip(prompt), maxPrompt)
	} else {
		prompt = original
		if tail != "" {
			prompt += "\n\n## Previous output\n" + tail
		}
		if len(queued) > 0 {
			prompt += "\n\n## Queued interventions\n" + strings.Join(queued, "\n\n")
		}
		if len(entries) > 0 {
			prompt += renderContext(entries)
		}
		if text != "" {
			prompt += "\n\n## User correction\n" + text
		}
		prompt = limit(prompt, maxPrompt)
	}
	if text != "" {
		m.appendEvent(s, "stdout", "› "+text, "input")
	}
	m.mu.Lock()
	if m.expirePendingLocked(s, "a new turn started before a decision") > 0 {
		m.saveLocked(s)
	}
	worktreeMeta, _ := s.Meta.Value("worktree").(*pyjson.Object)
	allowShared := pyjson.Truthy(s.Meta.Value("allow_shared_worktree"))
	cwdMeta := or(s.Meta.Value("cwd"), "")
	m.mu.Unlock()
	var worktree *pyjson.Object
	cwd := ""
	if worktreeMeta != nil && pyjson.Truthy(worktreeMeta.Value("path")) {
		if worktree, err = m.resolveWorktree(h, pyjson.Str(worktreeMeta.Value("path")), allowShared, s.ID); err != nil {
			return nil, err
		}
		cwd = "."
	} else if cwd, err = safeRelative(cwdMeta, "cwd"); err != nil {
		return nil, err
	}
	m.mu.Lock()
	meta := s.Meta
	turn := intOr(meta.Value("turn"), 1) + 1
	meta.Set("platform", preset.Value("id")).Set("platform_label", preset.Value("label")).Set("actor", actor).
		Set("model", newModel).Set("effort", newEffort).Set("access_mode", newAccess).Set("permission_mode", newAccess).
		Set("turn", turn)
	history, ok := meta.Value("permission_history").([]any)
	if !ok {
		history = []any{}
	}
	meta.Set("permission_history", append(history, pyjson.NewObject("mode", newAccess, "at", NowISO(), "turn", turn, "source", "continue")))
	meta.Set("followup", pyjson.Truthy(preset.Value("followup")))
	if worktree != nil {
		meta.Set("worktree", worktree.Copy())
	} else {
		meta.Set("worktree", meta.Value("worktree"))
	}
	meta.Set("status", "starting").Set("error", "").Set("notice", "").Set("ended_at", nil).Set("exit_code", nil).
		Set("pid", nil).Set("started_at", nil).Set("last_activity_at", NowISO())
	if o.Resume {
		meta.Set("resume_native_id", strOr(meta.Value("native_session_id"))).Set("resumed_natively_at", NowISO())
	}
	s.stopRequested = false
	s.proc = nil
	projectRef := objOr(deepCopy(meta.Value("project")))
	apiBase := strOr(objOr(meta.Value("api")).Value("base"))
	readToken := strOr(meta.Value("read_token"))
	m.mu.Unlock()
	workspace := s.Workspace()
	artifacts := filepath.Join(workspace, "artifacts")
	_ = os.MkdirAll(artifacts, 0o755)
	var cwdDir string
	if worktree != nil {
		cwdDir = pyjson.Str(worktree.Value("path"))
	} else {
		cwdDir = workspace
		if cwd != "" {
			cwdDir = filepath.Join(workspace, filepath.FromSlash(cwd))
		}
		_ = os.MkdirAll(cwdDir, 0o755)
	}
	label := cwd
	if label == "" {
		label = "."
	}
	m.refreshWorkspacePermission(s, preset, workspace, projectRef, apiBase, label)
	if err := m.spawn(h, s, preset, prompt, cwdDir, workspace, artifacts, projectRef, apiBase, readToken, false, actor); err != nil {
		return nil, err
	}
	m.mu.Lock()
	s.Meta.Set("continued_at", NowISO())
	m.saveLocked(s)
	m.mu.Unlock()
	return m.Public(s, true), nil
}

func pyEq(a, b any) bool { return pyjson.Str(a) == pyjson.Str(b) && sameKind(a, b) }

func sameKind(a, b any) bool {
	_, as := a.(string)
	_, bs := b.(string)
	return as == bs
}

func (m *Manager) forkContext(parent *Session) (string, error) {
	m.mu.Lock()
	title := pyjson.Str(or(parent.Meta.Value("title"), "untitled"))
	status := pyjson.Str(or(parent.Meta.Value("status"), "unknown"))
	mode := permissionLabels[pyjson.Str(or(parent.Meta.Value("access_mode"), "read_only"))]
	m.mu.Unlock()
	if mode == "" {
		mode = "read-only"
	}
	lines := []string{
		"## Side chat context",
		"You are a side chat branched from ARK session `" + parent.ID + "` (" + title + ").",
		"- The parent process keeps running independently; this side chat is a new process " +
			"with its own private workspace. Files are not shared with the parent.",
		"- Parent status: " + status,
		"- Parent permission mode: " + mode,
		"- Use the parent context below to answer; do not assume you can see the parent's files.",
	}
	if data, err := m.Transcript(parent.ID); err == nil {
		var messages []*pyjson.Object
		for _, item := range objects(data.Value("items")) {
			if item.Value("type") == "message" && pytext.Strip(strOr(item.Value("text"))) != "" {
				messages = append(messages, item)
			}
		}
		if len(messages) > 0 {
			lines = append(lines, "### Recent parent conversation")
			if len(messages) > 6 {
				messages = messages[len(messages)-6:]
			}
			for _, item := range messages {
				text := pytext.Strip(strOr(item.Value("text")))
				lines = append(lines, "**"+pyjson.Str(or(item.Value("role"), "assistant"))+":** "+limit(text, 900))
			}
		}
	}
	tail, err := m.outputTail(parent, 1200)
	if err != nil {
		return "", err
	}
	if tail != "" {
		lines = append(lines, "### Latest parent output\n```\n"+tail+"\n```")
	}
	return "\n\n" + limit(strings.Join(lines, "\n\n"), maxForkContext), nil
}

// ForkSession is SessionManager.fork_session.
func (m *Manager) ForkSession(h *Hooks, id string, body *pyjson.Object, baseURL, actor string) (*Session, error) {
	parent, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	pm := deepCopy(parent.Meta).(*pyjson.Object)
	m.mu.Unlock()
	if actor == "" {
		actor = strOr(pm.Value("actor"))
	}
	if pm.Value("status") == "archived" {
		return nil, serr("the parent session is archived", 400, "")
	}
	prompt := limit(pytext.Strip(strOr(or(body.Value("prompt"), body.Value("text")))), maxPrompt)
	if prompt == "" {
		return nil, serr("a side-chat prompt is required", 400, "")
	}
	relation := strings.ReplaceAll(pytext.Lower(pytext.Strip(pyjson.Str(or(body.Value("mode"), "side_chat")))), "-", "_")
	if relation != "side_chat" && relation != "fork" {
		return nil, serr("mode must be side_chat or fork", 400, "")
	}
	platform := strOr(or(body.Value("platform"), pm.Value("platform")))
	preset, err := m.Preset(platform, AgentOverrides(h, actor))
	if err != nil {
		return nil, err
	}
	accessIn := body.Value("access_mode")
	if isNoneOrEmpty(accessIn) {
		accessIn = pm.Value("access_mode")
	}
	access, err := m.validatePermissionMode(accessIn, preset)
	if err != nil {
		return nil, err
	}
	samePlatform := platform == strOr(pm.Value("platform"))
	pick := func(key string, n int) string {
		var v any
		if bv, ok := body.Get(key); ok && bv != nil {
			v = bv
		} else if samePlatform {
			v = pm.Value(key)
		}
		return limit(pytext.Strip(strOr(v)), n)
	}
	model, effort := pick("model", 80), pick("effort", 40)
	if err := m.checkEffort(preset, model, effort); err != nil {
		return nil, err
	}
	title := limit(pyjson.Str(or(body.Value("title"), "Side chat · "+pyjson.Str(or(pm.Value("title"), parent.ID)))), maxTitle)
	forkCtx, err := m.forkContext(parent)
	if err != nil {
		return nil, err
	}
	composed := limit(prompt+forkCtx, maxPrompt)
	var cwd any = ""
	if c := pm.Value("cwd"); c != nil && c != "" && c != "." {
		cwd = c
	}
	startBody := pyjson.NewObject("platform", platform, "title", title, "prompt", composed, "model", model,
		"effort", effort, "access_mode", access, "agent", strOr(or(body.Value("agent"), pm.Value("agent"))),
		"task", strOr(or(body.Value("task"), pm.Value("task_ref"))), "cwd", cwd, "parent_id", parent.ID)
	s, err := m.Start(h, startBody, baseURL, actor)
	if err != nil {
		return nil, err
	}
	m.SetMeta(s, "relation", relation, "side_chat_of", parent.ID, "forked_from", parent.ID,
		"forked_at", NowISO(), "fork_prompt", prompt)
	return s, nil
}

// Relaunch is SessionManager.relaunch.
func (m *Manager) Relaunch(h *Hooks, id, baseURL, actor string) (*Session, error) {
	old, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	if m.IsActive(old) {
		return nil, serr("stop the session before relaunching it", 409, "")
	}
	m.mu.Lock()
	meta := deepCopy(old.Meta).(*pyjson.Object)
	m.mu.Unlock()
	if actor == "" {
		actor = strOr(meta.Value("actor"))
	}
	prompt := pytext.Strip(strOr(meta.Value("prompt")))
	if prompt == "" {
		return nil, serr("this session has no original prompt; launch a new session", 400, "")
	}
	worktree, _ := meta.Value("worktree").(*pyjson.Object)
	if worktree == nil {
		worktree = pyjson.NewObject()
	}
	var cwd any = ""
	if c := meta.Value("cwd"); c != nil && c != "" && c != "." {
		cwd = c
	}
	body := pyjson.NewObject("platform", meta.Value("platform"), "title", meta.Value("title"), "model", meta.Value("model"),
		"effort", or(meta.Value("effort"), ""), "access_mode", or(meta.Value("access_mode"), "read_only"),
		"agent", meta.Value("agent"), "task", meta.Value("task_ref"), "cwd", cwd,
		"worktree", strOr(worktree.Value("path")), "allow_shared_worktree", pyjson.Truthy(meta.Value("allow_shared_worktree")),
		"prompt", prompt, "parent_id", or(meta.Value("parent_id"), ""))
	s, err := m.Start(h, body, baseURL, actor)
	if err != nil {
		return nil, err
	}
	m.SetMeta(s, "relaunched_from", id)
	_, _ = m.Archive(id)
	return s, nil
}

// Archive is SessionManager.archive.
func (m *Manager) Archive(id string) (*pyjson.Object, error) {
	s, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	if m.IsActive(s) {
		return nil, serr("stop the session before archiving it", 409, "")
	}
	m.mu.Lock()
	payload := m.publicLocked(s, true)
	archiveRoot := filepath.Join(m.Root, ".archived")
	dest := filepath.Join(archiveRoot, s.ID)
	if _, err := os.Stat(dest); err == nil {
		dest = filepath.Join(archiveRoot, s.ID+"-"+strconv.FormatInt(time.Now().Unix(), 10))
	}
	s.Meta.Set("status", "archived").Set("archived_at", NowISO()).Set("archived_path", dest)
	payload.Set("status", "archived").Set("raw_status", "archived")
	m.saveLocked(s)
	m.mu.Unlock()
	if err := os.Rename(s.Folder, dest); err != nil {
		return nil, serr("could not archive session: "+err.Error(), 400, "")
	}
	m.mu.Lock()
	m.removeLocked(s.ID)
	m.mu.Unlock()
	return payload, nil
}

func (m *Manager) removeLocked(id string) {
	delete(m.sessions, id)
	for i, x := range m.order {
		if x == id {
			m.order = append(m.order[:i:i], m.order[i+1:]...)
			break
		}
	}
}

// Delete is SessionManager.delete.
func (m *Manager) Delete(id string) (*pyjson.Object, error) {
	s, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	if m.IsActive(s) {
		return nil, serr("stop the session before deleting it", 409, "")
	}
	folder := resolvePath(s.Folder)
	if filepath.Dir(folder) != m.Root || !IDRe.MatchString(filepath.Base(folder)) {
		return nil, serr("refusing to delete outside the session root", 400, "")
	}
	if err := os.RemoveAll(folder); err != nil {
		return nil, serr("could not delete session: "+err.Error(), 400, "")
	}
	m.mu.Lock()
	m.removeLocked(s.ID)
	m.mu.Unlock()
	m.notify(s, "delete")
	return pyjson.NewObject("deleted", s.ID), nil
}
