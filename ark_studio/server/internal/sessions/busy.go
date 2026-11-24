package sessions

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/secretredact"
)

// Background work of a long-lived Claude Code session between turns
// (2026-11-22): its sub-agents and background tasks (Bash with
// run_in_background, monitors) that still run, and the ARK sessions
// started as its sub-agents.  While any runs the session is busy: the
// client shows it as running ("Running · 2 sub-agents") and the nudger
// leaves it alone.  The ARK watcher (the update feed listener) is plumbing
// and does not count.

// Busy is the running background work of one session.
type Busy struct {
	Subagents  int
	Background int
}

// Any reports whether anything runs.
func (b Busy) Any() bool { return b.Subagents+b.Background > 0 }

func (b Busy) public() *pyjson.Object {
	return pyjson.NewObject("subagents", int64(b.Subagents), "background", int64(b.Background))
}

// watcherRe matches the ARK update watcher by its command or description.
var watcherRe = regexp.MustCompile(`(?i)ark[-_ ]?watch|/api/agent/v1/updates|agentwatch|ark_wake`)

// IsWatcher reports whether a background command is the ARK watcher.
func IsWatcher(texts ...string) bool {
	for _, t := range texts {
		if t != "" && watcherRe.MatchString(t) {
			return true
		}
	}
	return false
}

var killTools = set("killshell", "killbash", "taskstop", "kill_shell", "stop_task")

func taskRunning(status string) bool {
	switch pytext.Lower(pytext.Strip(status)) {
	case "running", "started", "in_progress", "pending", "queued", "async_launched", "launched":
		return true
	}
	return false
}

// busyFromItems counts the running tasks of a transcript (its tool calls
// with a task record and the hidden {type: "task"} items).
func busyFromItems(items []*pyjson.Object) Busy {
	killed := map[string]bool{}
	for _, it := range items {
		if it.Value("type") != "tool" || it.Value("status") == "error" {
			continue
		}
		if killTools[pytext.Lower(strOr(it.Value("name")))] {
			if in, _ := it.Value("input").(*pyjson.Object); in != nil {
				if id := firstString(in, "shell_id", "bash_id", "task_id", "agent_id", "id"); id != "" {
					killed[id] = true
				}
			}
		}
	}
	var b Busy
	for _, it := range items {
		task, _ := it.Value("task").(*pyjson.Object)
		if task == nil || !taskRunning(strOr(task.Value("status"))) || killed[strOr(task.Value("id"))] {
			continue
		}
		name := pytext.Lower(strOr(it.Value("name")))
		agent := pytext.Lower(strOr(task.Value("type"))) == "agent" || activityAgentTools[name]
		if agent {
			b.Subagents++
			continue
		}
		in, _ := it.Value("input").(*pyjson.Object)
		var command, desc string
		if in != nil {
			command, desc = strOr(in.Value("command")), strOr(in.Value("description"))
		}
		if IsWatcher(command, desc, strOr(task.Value("description"))) {
			continue
		}
		b.Background++
	}
	return b
}

// waitingStreamLocked: a live Claude Code process between turns.
func waitingStreamLocked(s *Session) bool {
	return isStream(s) && s.proc != nil && s.proc.running() && s.Meta.Value("turn_state") == "waiting"
}

// transcriptInputLocked is the cached transcript's key for `s` (as in
// TranscriptPage).
func transcriptInputLocked(s *Session) TranscriptInput {
	meta := s.Meta
	return TranscriptInput{SessionID: s.ID, Prompt: strOr(or(meta.Value("prompt"), "")),
		PromptAt: or(meta.Value("started_at"), meta.Value("created_at")), Platform: strOr(meta.Value("platform"))}
}

// Busy is the background work still running in `s` between turns (zero
// while a turn runs or the process is gone).  It reads only the log lines
// appended since the last look.
func (m *Manager) Busy(s *Session) Busy {
	m.mu.Lock()
	waiting := waitingStreamLocked(s)
	in := transcriptInputLocked(s)
	m.mu.Unlock()
	var b Busy
	if waiting {
		c := m.transcriptCacheFor(s.ID)
		c.mu.Lock()
		m.syncCacheLocked(c, s, in, "")
		if c.b != nil {
			b = busyFromItems(c.b.items)
		}
		c.mu.Unlock()
		for _, child := range m.all() {
			if child == s {
				continue
			}
			m.mu.Lock()
			if strOr(child.Meta.Value("parent_id")) == s.ID && m.isActiveLocked(child) {
				b.Subagents++
			}
			m.mu.Unlock()
		}
	}
	m.mu.Lock()
	s.busy = b
	m.mu.Unlock()
	return b
}

// ------------------------------------------------- finished background work
//
// 2026-11-23: a long-lived Claude Code process reports a background command
// or sub-agent that finished (a task_notification event) and, while it
// waits, usually takes it up itself with a new turn (observeStreamLine).
// A notification that arrives while the process waits is kept in
// meta.bg_pending until a turn starts; when none started within a grace
// period, the session nudger tells the agent (internal/sessionnudge,
// Deps.Background).  The order of the stream decides, not time stamps.

// BackgroundDone is background work of a resting session that finished
// while it waited.
type BackgroundDone struct {
	ID, Name, Kind, Status string
	ExitCode               *int64
	Tail                   string // the last lines of its output file ("" when unknown)
}

const (
	bgMaxAge  = 2 * time.Hour
	bgTailMax = 4096
)

var exitCodeRe = regexp.MustCompile(`(?i)exit(?:ed with)?(?: code)?[ :]+(-?\d+)`)

// noteTaskNotificationLocked records a task_notification that arrived while
// the process waits (caller holds m.mu).
func noteTaskNotificationLocked(s *Session, taskID string) {
	if taskID == "" || !waitingStreamLocked(s) {
		return
	}
	pending, _ := s.Meta.Value("bg_pending").(*pyjson.Object)
	if pending == nil {
		pending = pyjson.NewObject()
	}
	if !pending.Has(taskID) {
		pending.Set(taskID, time.Now().UTC().Format(time.RFC3339Nano))
	}
	s.Meta.Set("bg_pending", pending)
}

// taskNotificationID is the task id of a task_notification stream line of
// the main conversation ("" for any other line).
func taskNotificationID(text string) string {
	if !strings.Contains(text, "task_notification") && !strings.Contains(text, "task-notification") {
		return ""
	}
	obj, ok := jsonLineObject(text)
	if !ok || pyjson.Truthy(obj.Value("parent_tool_use_id")) {
		return ""
	}
	switch strOr(obj.Value("type")) {
	case "system":
		if strOr(obj.Value("subtype")) == "task_notification" {
			return firstString(obj, "task_id", "task-id", "shell_id", "agent_id", "tool_use_id")
		}
	case "user":
		message, _ := obj.Value("message").(*pyjson.Object)
		if message == nil {
			return ""
		}
		var texts []string
		if t, ok := message.Value("content").(string); ok {
			texts = append(texts, t)
		}
		for _, blk := range blocks(message.Value("content")) {
			if strOr(blk.Value("type")) == "text" {
				texts = append(texts, strOr(blk.Value("text")))
			}
		}
		for _, t := range texts {
			if m := taskNotificationRe.FindStringSubmatch(t); m != nil {
				for _, tag := range taskTagRe.FindAllStringSubmatch(m[1], -1) {
					if tag[1] == "task-id" || tag[1] == "task_id" {
						return strings.TrimSpace(tag[2])
					}
				}
			}
		}
	}
	return ""
}

// RestingBackgroundDone lists the background tasks and sub-agents of `s`
// whose end was reported while it waited, at least `grace` ago, with no
// turn since.
func (m *Manager) RestingBackgroundDone(s *Session, grace time.Duration) []BackgroundDone {
	m.mu.Lock()
	waiting := waitingStreamLocked(s)
	due := map[string]bool{}
	var order []string
	if pending, ok := s.Meta.Value("bg_pending").(*pyjson.Object); ok && waiting {
		now := time.Now()
		for _, id := range pending.Keys() {
			at, err := time.Parse(time.RFC3339Nano, strOr(pending.Value(id)))
			if err != nil || now.Sub(at) > bgMaxAge {
				pending.Delete(id)
				continue
			}
			if now.Sub(at) >= grace {
				due[id] = true
				order = append(order, id)
			}
		}
	}
	in := transcriptInputLocked(s)
	m.mu.Unlock()
	if len(due) == 0 {
		return nil
	}
	tasks := map[string]*pyjson.Object{} // task id -> its transcript item
	c := m.transcriptCacheFor(s.ID)
	c.mu.Lock()
	m.syncCacheLocked(c, s, in, "")
	if c.b != nil {
		for _, it := range c.b.items {
			if task, _ := it.Value("task").(*pyjson.Object); task != nil {
				tasks[strOr(task.Value("id"))] = it
			}
		}
	}
	c.mu.Unlock()
	var out []BackgroundDone
	var watchers []string
	for _, id := range order {
		d := BackgroundDone{ID: id, Kind: "background task"}
		if it := tasks[id]; it != nil {
			task, _ := it.Value("task").(*pyjson.Object)
			in, _ := it.Value("input").(*pyjson.Object)
			var command, desc string
			if in != nil {
				command, desc = strOr(in.Value("command")), strOr(in.Value("description"))
			}
			if IsWatcher(command, desc, strOr(task.Value("description"))) {
				watchers = append(watchers, id)
				continue
			}
			if pytext.Lower(strOr(task.Value("type"))) == "agent" || activityAgentTools[pytext.Lower(strOr(it.Value("name")))] {
				d.Kind = "sub-agent"
			}
			for _, name := range []string{strOr(task.Value("description")), desc, command} {
				if name != "" {
					d.Name = truncate(strings.TrimSpace(name), 80)
					break
				}
			}
			d.Status = strOr(task.Value("status"))
			if n, isInt := pyIntValue(task.Value("exit_code")); isInt {
				d.ExitCode = &n
			} else if mm := exitCodeRe.FindStringSubmatch(strOr(task.Value("summary"))); mm != nil {
				if n, err := strconv.ParseInt(mm[1], 10, 64); err == nil {
					d.ExitCode = &n
				}
			}
			d.Tail = outputTail(strOr(task.Value("output_file")))
		}
		out = append(out, d)
	}
	m.MarkBackgroundTold(s, watchers) // the ARK watcher is plumbing: never told
	return out
}

// MarkBackgroundTold forgets reported tasks the session was told about.
func (m *Manager) MarkBackgroundTold(s *Session, ids []string) {
	if len(ids) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	pending, _ := s.Meta.Value("bg_pending").(*pyjson.Object)
	if pending == nil {
		return
	}
	for _, id := range ids {
		pending.Delete(id)
	}
	m.saveLocked(s)
}

// clearBackgroundPendingLocked: a turn starts, and the agent sees every
// notification in it (caller holds m.mu).
func clearBackgroundPendingLocked(s *Session) {
	if s.Meta.Has("bg_pending") {
		s.Meta.Delete("bg_pending")
	}
}

// Line is the notice line: `Background task sh_1 "Train" completed (exit 0)`.
func (d BackgroundDone) Line() string {
	var b strings.Builder
	b.WriteString(strings.ToUpper(d.Kind[:1]) + d.Kind[1:] + " " + d.ID)
	if d.Name != "" {
		b.WriteString(" " + strconv.Quote(d.Name))
	}
	status := d.Status
	if status == "" || taskRunning(status) {
		status = "finished"
	}
	b.WriteString(" " + status)
	if d.ExitCode != nil {
		b.WriteString(" (exit " + strconv.FormatInt(*d.ExitCode, 10) + ")")
	}
	return b.String()
}

// outputTail is the last lines of a task's output file (Claude Code writes
// one per background command), secrets redacted; "" when unreadable.
func outputTail(path string) string {
	if path == "" || !filepath.IsAbs(path) {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return ""
	}
	off := max(st.Size()-bgTailMax, 0)
	buf := make([]byte, st.Size()-off)
	if n, err := f.ReadAt(buf, off); err != nil && n < len(buf) {
		return ""
	}
	text := strings.ToValidUTF8(string(buf), "")
	if off > 0 {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:] // drop the cut first line
		}
	}
	text, _ = secretredact.Text(text, nil)
	return text
}
