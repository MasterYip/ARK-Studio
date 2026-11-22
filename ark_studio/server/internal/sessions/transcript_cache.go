package sessions

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/compat/pyurl"
)

// The session transcript is rebuilt from the logs on every poll; a long
// stream-json session has megabytes of log, so each session keeps its
// builder and feeds it only the lines appended since the last request.  A
// log that shrank (or a different prompt) starts over.  Unlike a one-off
// BuildTranscript, the cached builder keeps every line (no MaxSourceLines
// window), so sub-agents and background tasks started early stay known.

const (
	transcriptCacheIdle = 15 * time.Minute
	transcriptCacheMax  = 24
	// Tool output sent with a transcript page; ?item=<id> returns the item
	// with its output as kept by the builder (maxToolOutputChars).
	transcriptPreviewChars = 4000
	untailedItems          = 1500
)

type transcriptCache struct {
	mu             sync.Mutex
	key            string
	b              *transcriptBuilder
	outOff, errOff int64
	outN, errN     int
	usedAt         time.Time
	lastKey        string
	last           *pyjson.Object
}

type transcriptCaches struct {
	mu sync.Mutex
	m  map[string]*transcriptCache
}

func (m *Manager) transcriptCacheFor(id string) *transcriptCache {
	m.tcache.mu.Lock()
	defer m.tcache.mu.Unlock()
	if m.tcache.m == nil {
		m.tcache.m = map[string]*transcriptCache{}
	}
	now := time.Now()
	c := m.tcache.m[id]
	if c == nil {
		if len(m.tcache.m) >= transcriptCacheMax {
			var oldest string
			for k, v := range m.tcache.m {
				if now.Sub(v.usedAt) > transcriptCacheIdle || oldest == "" || v.usedAt.Before(m.tcache.m[oldest].usedAt) {
					oldest = k
				}
			}
			delete(m.tcache.m, oldest)
		}
		c = &transcriptCache{}
		m.tcache.m[id] = c
	}
	c.usedAt = now
	return c
}

// readNewEntries returns the log entries appended since `off` (complete
// lines only) and the new offset.  ok is false when the log shrank.
func readNewEntries(s *Session, stream string, off int64) ([]*pyjson.Object, int64, bool) {
	path := filepath.Join(s.Folder, stream+".jsonl")
	s.logMu.Lock()
	defer s.logMu.Unlock()
	fh, err := os.Open(path)
	if err != nil {
		return nil, off, off == 0
	}
	defer fh.Close()
	fi, err := fh.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, off, off == 0
	}
	if fi.Size() < off {
		return nil, 0, false
	}
	if fi.Size() == off {
		return nil, off, true
	}
	if _, err := fh.Seek(off, io.SeekStart); err != nil {
		return nil, off, false
	}
	raw, err := io.ReadAll(fh)
	if err != nil {
		return nil, off, false
	}
	end := bytes.LastIndexByte(raw, '\n')
	if end < 0 {
		return nil, off, true
	}
	raw = raw[:end+1]
	text := strings.ReplaceAll(strings.ReplaceAll(pyurl.DecodeReplace(raw), "\r\n", "\n"), "\r", "\n")
	var out []*pyjson.Object
	for _, line := range pytext.SplitLines(text, false) {
		if o, ok := parseObject(line); ok {
			out = append(out, o)
		}
	}
	return out, off + int64(len(raw)), true
}

// cachedTranscript is the session transcript through its cached builder.
// `item` (optional) is a tool item id to return as kept by the builder.
func (m *Manager) cachedTranscript(s *Session, in TranscriptInput, item string) (*pyjson.Object, *pyjson.Object) {
	return m.cachedTranscriptFor(s, in, item, "")
}

// cachedTranscriptFor is cachedTranscript, or with `agent` (a Task/Agent
// call id) the transcript of that sub-agent: its own messages and tools.
func (m *Manager) cachedTranscriptFor(s *Session, in TranscriptInput, item, agent string) (*pyjson.Object, *pyjson.Object) {
	cacheID := s.ID
	if agent != "" {
		cacheID += "#" + agent
	}
	c := m.transcriptCacheFor(cacheID)
	c.mu.Lock()
	defer c.mu.Unlock()
	m.syncCacheLocked(c, s, in, agent)
	finalKey := strings.Join([]string{in.Status, in.Error, in.Notice, pyjson.Str(in.Waiting)}, "\x00")
	if c.last == nil || c.lastKey != finalKey {
		c.last, c.lastKey = c.b.finalize(in), finalKey
	}
	var found *pyjson.Object
	if item != "" && c.b.tools[item] != nil {
		found = c.b.tools[item].Copy()
		if urls := c.b.toolImages[item]; len(urls) > 0 {
			list := make([]any, len(urls))
			for i, u := range urls {
				list[i] = u
			}
			found.Set("image_data", list)
		}
	}
	return c.last, found
}

// syncCacheLocked feeds the cached builder the log lines appended since
// the last look (c.mu held).
func (m *Manager) syncCacheLocked(c *transcriptCache, s *Session, in TranscriptInput, agent string) {
	key := strings.Join([]string{in.SessionID, in.Prompt, pyjson.Str(in.PromptAt), in.Platform}, "\x00")
	for attempt := 0; attempt < 2; attempt++ {
		if c.b == nil || c.key != key {
			c.b, c.key, c.outOff, c.errOff, c.outN, c.errN, c.last = newTranscriptBuilder(in), key, 0, 0, 0, 0, nil
			c.b.agent = agent
		}
		out, outOff, ok1 := readNewEntries(s, "stdout", c.outOff)
		errs, errOff, ok2 := readNewEntries(s, "stderr", c.errOff)
		if !ok1 || !ok2 {
			c.b = nil // a log was replaced: start over
			continue
		}
		n, nErr := len(out), len(errs)
		if agent != "" {
			out, errs = agentLines(out, agent), nil
		}
		if len(out)+len(errs) > 0 {
			c.b.feed(transcriptRows(out, errs, c.outN, c.errN))
			c.last = nil
		}
		c.outN += n
		c.errN += nErr
		c.outOff, c.errOff = outOff, errOff
		break
	}
	if c.b == nil { // both attempts saw a replaced log
		c.b, c.key = newTranscriptBuilder(in), key
		c.b.agent = agent
	}
}

// TranscriptOptions shape a transcript reply for the web client.
type TranscriptOptions struct {
	Tail  int    // only the last Tail items (plus `activity`); 0 = all
	Known string // the signature the client has: an unchanged transcript replies {unchanged: true}
	Item  string // one tool item with its full kept output
	Image int    // with Item: only its n-th image (1-based) as a data URL
}

var (
	activityAgentTools  = set("task", "agent", "spawn_agent", "subagent", "dispatch_agent")
	activityCommandTool = set("bash", "shell", "command", "exec", "execute", "terminal", "powershell")
	activityOtherTools  = set("killshell", "killbash", "taskstop", "kill_shell", "stop_task", "bashoutput", "taskoutput", "bash_output", "task_output")
)

// activityItems are the items the sub-agent / background task model needs,
// without their outputs (a page of the transcript does not hold them all).
func activityItems(items []*pyjson.Object) []any {
	out := []any{}
	for _, it := range items {
		switch it.Value("type") {
		case "task":
			out = append(out, it)
			continue
		case "tool":
		default:
			continue
		}
		name := pytext.Lower(strOr(it.Value("name")))
		_, hasTask := it.Value("task").(*pyjson.Object)
		input, _ := it.Value("input").(*pyjson.Object)
		background := input != nil && pyjson.Truthy(input.Value("run_in_background"))
		if !activityAgentTools[name] && !activityOtherTools[name] && !hasTask && !background {
			continue
		}
		if activityCommandTool[name] && !background {
			if tk, _ := it.Value("task").(*pyjson.Object); tk == nil || !pyjson.Truthy(tk.Value("background")) {
				continue // a foreground command
			}
		}
		c := pyjson.NewObject()
		for _, k := range []string{"id", "type", "name", "status", "at", "completed_at", "task", "meta", "children", "child_tool",
			"child_tokens", "last_output", "last_at"} {
			if v, ok := it.Get(k); ok {
				c.Set(k, v)
			}
		}
		if input != nil {
			slim := pyjson.NewObject()
			for _, k := range []string{"description", "subagent_type", "agent_type", "model", "run_in_background", "command",
				"shell_id", "bash_id", "task_id", "agent_id", "prompt"} {
				if v, ok := input.Get(k); ok {
					if str, isStr := v.(string); isStr {
						v = truncate(str, 300)
					}
					slim.Set(k, v)
				}
			}
			c.Set("input", slim)
		}
		if out, ok := it.Value("output").(string); ok {
			c.Set("output", lastChars(out, 600))
		}
		out = append(out, c)
	}
	return out
}

func lastChars(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

// previewItem is a copy of a tool item whose output fits a transcript page.
func previewItem(it *pyjson.Object) *pyjson.Object {
	if it.Value("type") != "tool" {
		return it
	}
	out, ok := it.Value("output").(string)
	if !ok || pytext.Len(out) <= transcriptPreviewChars {
		return it
	}
	c := it.Copy()
	c.Set("output", pytext.Prefix(out, transcriptPreviewChars)).Set("output_preview", true)
	if !c.Has("output_chars") {
		c.Set("output_chars", int64(pytext.Len(out)))
	}
	return c
}

// shapeTranscript applies TranscriptOptions to a full transcript reply.
func shapeTranscript(data, item *pyjson.Object, o TranscriptOptions) *pyjson.Object {
	if o.Item != "" {
		var found any
		if item != nil {
			if o.Image > 0 {
				urls, _ := item.Value("image_data").([]any)
				if o.Image > len(urls) {
					return pyjson.NewObject("session", data.Value("session"), "image", nil)
				}
				return pyjson.NewObject("session", data.Value("session"), "image", urls[o.Image-1])
			}
			item.Delete("image_data")
			found = item
		}
		return pyjson.NewObject("session", data.Value("session"), "item", found)
	}
	if o.Known != "" && o.Known == strOr(data.Value("signature")) {
		return pyjson.NewObject("session", data.Value("session"), "status", data.Value("status"),
			"signature", data.Value("signature"), "unchanged", true)
	}
	if o.Tail <= 0 {
		// Clients that do not page get the last untailedItems items (the
		// cached builder keeps a session's whole history).
		items := objects(data.Value("items"))
		if len(items) <= untailedItems {
			return data
		}
		out := data.Copy()
		page := make([]any, 0, untailedItems)
		for _, it := range items[len(items)-untailedItems:] {
			page = append(page, it)
		}
		return out.Set("items", page).Set("offset", int64(len(items)-untailedItems)).Set("total_items", int64(len(items)))
	}
	items := objects(data.Value("items"))
	start := 0
	if len(items) > o.Tail {
		start = len(items) - o.Tail
	}
	page := make([]any, 0, len(items)-start)
	for _, it := range items[start:] {
		page = append(page, previewItem(it))
	}
	out := data.Copy()
	out.Set("items", page).Set("offset", int64(start)).Set("total_items", int64(len(items))).Set("activity", activityItems(items))
	return out
}

// agentLines keeps the stream lines of one sub-agent (and of the agents it
// starts, which fold into their own calls).
func agentLines(entries []*pyjson.Object, agent string) []*pyjson.Object {
	var out []*pyjson.Object
	for _, e := range entries {
		line := strOr(e.Value("line"))
		if strings.Contains(line, `"parent_tool_use_id":"`+agent+`"`) || strings.Contains(line, `"parent_tool_use_id": "`+agent+`"`) {
			out = append(out, e)
		}
	}
	return out
}

// AgentTranscript is the transcript of one sub-agent of session `id`: the
// messages and tools of the Task/Agent call `agent`, its prompt first.
func (m *Manager) AgentTranscript(id, agent string, o TranscriptOptions) (*pyjson.Object, error) {
	s, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	payload := m.Public(s, true)
	m.mu.Lock()
	platform := strOr(s.Meta.Value("platform"))
	m.mu.Unlock()
	main, call := m.cachedTranscript(s, TranscriptInput{SessionID: s.ID, Status: pyjson.Str(payload.Value("status")),
		Prompt: strOr(payload.Value("prompt")), PromptAt: or(payload.Value("started_at"), payload.Value("created_at")),
		Error: strOr(payload.Value("error")), Platform: platform, Notice: strOr(payload.Value("notice"))}, agent)
	if call == nil {
		return nil, serr("sub-agent not found in this session", 404, "")
	}
	_ = main
	input := objOr(call.Value("input"))
	status := "stopped"
	if call.Value("status") == "running" && activeStatuses[pyjson.Str(payload.Value("status"))] {
		status = "running"
	}
	if task, ok := call.Value("task").(*pyjson.Object); ok {
		if ts := pytext.Lower(strOr(task.Value("status"))); ts == "running" && activeStatuses[pyjson.Str(payload.Value("status"))] {
			status = "running"
		}
	}
	data, _ := m.cachedTranscriptFor(s, TranscriptInput{SessionID: s.ID, Status: status, Platform: platform,
		PromptAt: call.Value("at")}, "", agent) // the stream starts with the sub-agent's prompt
	out := data.Copy()
	out.Set("agent", pyjson.NewObject("id", agent, "name", call.Value("name"), "description", input.Value("description"),
		"subagent_type", input.Value("subagent_type"), "model", input.Value("model"), "status", call.Value("status"),
		"task", call.Value("task"), "output", lastChars(strOr(call.Value("output")), 4000)))
	return shapeTranscript(out, nil, TranscriptOptions{Tail: o.Tail, Known: o.Known}), nil
}
