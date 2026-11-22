package sessions

import (
	"regexp"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
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
