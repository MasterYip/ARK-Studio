package sessions

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/rnd"
)

// Long-lived Claude Code sessions.  `claude -p --input-format stream-json
// --output-format stream-json --verbose` keeps one process per session: ARK
// writes each user message to its stdin as a JSON line, a `result` event ends
// a turn, and a message written while a turn runs is taken into that turn.
//
//   meta.stream_input  true for such a session
//   meta.turn_state    "running" (a turn is in progress), "waiting" (the
//                      agent waits for the next message) or "interrupting"
//
// Enter while a turn runs keeps the message in ARK's queue and writes it when
// the turn ends; "Send now" writes it at once.  Stop interrupts the running
// turn (a control request), or ends the process when it waits; the process is
// killed only when it does not react.  A waiting process is closed after
// StreamIdleTimeout, and when too many run (StreamMax, StreamMaxPerUser) the
// longest-waiting one is closed first; the next message resumes the session
// with --resume.  A CLI that rejects the stream flags turns stream mode off
// for this server (one-shot turns, as before).

var (
	// StreamIdleTimeout closes a waiting process (it resumes on the next message).
	StreamIdleTimeout = envDuration("ARK_CLAUDE_STREAM_IDLE", 30*time.Minute)
	// StreamMax and StreamMaxPerUser cap live processes.
	StreamMax        = envInt("ARK_CLAUDE_STREAM_MAX", 8)
	StreamMaxPerUser = envInt("ARK_CLAUDE_STREAM_MAX_PER_USER", 4)
	// StopGrace is how long Stop waits before it kills the process.
	StopGrace = 15 * time.Second
)

const maxImageBlockBytes = 5 << 20

var imageTypes = map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp"}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name))); err == nil && v > 0 {
		return v
	}
	return def
}

func envDuration(name string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(strings.TrimSpace(os.Getenv(name))); err == nil && v > 0 {
		return v
	}
	return def
}

type streamState struct {
	once     sync.Once
	mu       sync.Mutex
	disabled bool // the CLI rejected the stream flags
}

// streamEnabled reports whether new Claude Code processes use stream input.
func (m *Manager) streamEnabled() bool {
	if os.Getenv("ARK_CLAUDE_STREAM") == "0" {
		return false
	}
	m.stream.mu.Lock()
	defer m.stream.mu.Unlock()
	return !m.stream.disabled
}

// claudeStreamArgv turns a print-mode Claude Code command into a long-lived
// stream-json one: the prompt argument is dropped (it goes to stdin) and the
// formats are added.  ok is false for a command that is not print mode or
// that chose other formats (it keeps one-shot turns).
func claudeStreamArgv(argv []string, prompt string) ([]string, bool) {
	if !contains(argv, "-p") && !contains(argv, "--print") {
		return nil, false
	}
	for i, a := range argv {
		for _, flag := range []string{"--output-format", "--input-format"} {
			if a == flag && i+1 < len(argv) && argv[i+1] != "stream-json" {
				return nil, false
			}
			if strings.HasPrefix(a, flag+"=") && a != flag+"=stream-json" {
				return nil, false
			}
		}
	}
	out := make([]string, 0, len(argv)+5)
	dropped := false
	for _, a := range argv {
		if !dropped && prompt != "" && a == prompt {
			dropped = true
			continue
		}
		out = append(out, a)
	}
	var extra []string
	if !contains(out, "--input-format") {
		extra = append(extra, "--input-format", "stream-json")
	}
	if !contains(out, "--output-format") {
		extra = append(extra, "--output-format", "stream-json")
	}
	if !contains(out, "--verbose") {
		extra = append(extra, "--verbose")
	}
	return insertAt(out, 1, extra...), true
}

func isStream(s *Session) bool {
	return pyjson.Truthy(s.Meta.Value("stream_input"))
}

// imageBlocks are base64 image content blocks for the media in `entries`.
func (m *Manager) imageBlocks(s *Session, entries []*pyjson.Object) []any {
	var out []any
	for _, e := range entries {
		if e.Value("kind") != "media" {
			continue
		}
		target, _, err := m.artifactTarget(s, strOr(e.Value("path")))
		if err != nil {
			continue
		}
		mime := imageTypes[strings.ToLower(filepath.Ext(target))]
		if mime == "" {
			continue // the API takes png, jpeg, gif and webp
		}
		blob, err := os.ReadFile(target)
		if err != nil || len(blob) == 0 || len(blob) > maxImageBlockBytes {
			continue
		}
		out = append(out, pyjson.NewObject("type", "image", "source", pyjson.NewObject(
			"type", "base64", "media_type", mime, "data", base64.StdEncoding.EncodeToString(blob))))
	}
	return out
}

// streamUserLine is one user message for Claude Code's stream-json input.
func streamUserLine(text string, images []any) string {
	content := []any{}
	if pytext.Strip(text) != "" {
		content = append(content, pyjson.NewObject("type", "text", "text", text))
	}
	content = append(content, images...)
	return string(pyjson.Dumps(pyjson.NewObject("type", "user",
		"message", pyjson.NewObject("role", "user", "content", content))))
}

// sendUserMessage writes a user turn to a running session: a stream-json
// line (with image blocks) for a stream session, plain text otherwise.  The
// turn is recorded as `display` (and its media) when record is set.
func (m *Manager) sendUserMessage(s *Session, text string, entries []*pyjson.Object, display string, record bool) error {
	m.autoTitle(s, text)
	m.mu.Lock()
	stream := isStream(s)
	m.mu.Unlock()
	payload := composeMessage(text, entries)
	if !stream {
		if record {
			defer m.recordMedia(s, entries)
		}
		return m.writeInput(s, payload, record, &display)
	}
	if err := m.writeInput(s, streamUserLine(payload, m.imageBlocks(s, entries)), false, nil); err != nil {
		return err
	}
	m.mu.Lock()
	s.Meta.Set("turn_state", "running")
	m.saveLocked(s)
	m.mu.Unlock()
	if record {
		m.appendEvent(s, "stdout", "› "+strings.TrimRight(display, "\r\n"), "input")
		m.recordMedia(s, entries)
	}
	return nil
}

// streamTurnState is the turn state of a stream session ("" otherwise).
func (m *Manager) streamTurnState(s *Session) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !isStream(s) {
		return ""
	}
	return pyjson.Str(or(s.Meta.Value("turn_state"), "running"))
}

// observeStreamLine follows turn boundaries of a stream session: a `result`
// of the main conversation ends the turn, and the first queued message (if
// any) starts the next one.
func (m *Manager) observeStreamLine(s *Session, line string) {
	text := pytext.Strip(line)
	if strings.HasPrefix(text, "{") && (strings.Contains(text, `"type":"assistant"`) || strings.Contains(text, `"type": "assistant"`)) &&
		!strings.Contains(text, `"parent_tool_use_id":"`) && !strings.Contains(text, `"parent_tool_use_id": "`) {
		// The agent speaks while it waits (a background task's notification
		// woke it): a turn without a user message.
		m.mu.Lock()
		if isStream(s) && s.Meta.Value("turn_state") == "waiting" {
			s.Meta.Set("turn_state", "running")
			m.saveLocked(s)
		}
		m.mu.Unlock()
		return
	}
	if !strings.HasPrefix(text, "{") || (!strings.Contains(text, `"type":"result"`) && !strings.Contains(text, `"type": "result"`)) {
		return
	}
	obj, ok := jsonLineObject(text)
	if !ok || obj.Value("type") != "result" || pyjson.Truthy(obj.Value("parent_tool_use_id")) {
		return
	}
	m.mu.Lock()
	if !isStream(s) {
		m.mu.Unlock()
		return
	}
	s.Meta.Set("turn_state", "waiting").Set("last_activity_at", NowISO())
	var next *pyjson.Object
	for _, e := range objects(s.Meta.Value("steer_queue")) {
		if e.Value("status") == "queued" {
			next = e
			break
		}
	}
	if next != nil {
		next.Set("status", "delivered").Set("delivered_at", NowISO()).Set("channel", "stdin")
	}
	m.saveLocked(s)
	m.mu.Unlock()
	if next != nil {
		text := strOr(next.Value("text"))
		if err := m.sendUserMessage(s, text, nil, text, true); err != nil {
			m.mu.Lock()
			next.Set("status", "failed").Set("error", err.Error())
			m.saveLocked(s)
			m.mu.Unlock()
		}
	}
	m.notify(s, "turn")
}

// stopStream stops a stream session: interrupt a running turn, or end a
// waiting process; kill it if it does not react within StopGrace.  handled
// is false when the session is not a live stream session.
func (m *Manager) stopStream(s *Session) (handled bool) {
	m.mu.Lock()
	p := s.proc
	if !isStream(s) || p == nil || !p.running() {
		m.mu.Unlock()
		return false
	}
	state := pyjson.Str(or(s.Meta.Value("turn_state"), "running"))
	if state == "running" {
		s.Meta.Set("turn_state", "interrupting")
		m.saveLocked(s)
		m.mu.Unlock()
		frame := pyjson.NewObject("type", "control_request", "request_id", "int_"+rnd.Hex("sessions", 4),
			"request", pyjson.NewObject("subtype", "interrupt"))
		if err := m.writeInput(s, string(pyjson.Dumps(frame)), false, nil); err == nil {
			go m.killIfStuck(s, p, func() bool {
				m.mu.Lock()
				defer m.mu.Unlock()
				return s.Meta.Value("turn_state") == "interrupting"
			})
			return true
		}
		m.mu.Lock()
	}
	s.stopRequested = true
	s.Meta.Set("status", "stopping")
	m.saveLocked(s)
	m.mu.Unlock()
	p.closeStdin()
	go m.killIfStuck(s, p, func() bool { return p.running() })
	return true
}

func (m *Manager) killIfStuck(s *Session, p *process, stuck func() bool) {
	deadline := time.Now().Add(StopGrace)
	for time.Now().Before(deadline) {
		if !p.running() || !stuck() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	if p.running() && stuck() {
		m.mu.Lock()
		s.stopRequested = true
		s.Meta.Set("status", "stopping")
		m.saveLocked(s)
		m.mu.Unlock()
		m.terminate(p)
	}
}

// closeIdle ends a waiting stream process; the next message resumes it.
func (m *Manager) closeIdle(s *Session, reason string) {
	m.mu.Lock()
	p := s.proc
	if p == nil || !p.running() {
		m.mu.Unlock()
		return
	}
	s.stopRequested = true
	s.Meta.Set("notice", reason)
	m.saveLocked(s)
	m.mu.Unlock()
	p.closeStdin()
	go m.killIfStuck(s, p, func() bool { return p.running() })
}

type liveStream struct {
	s       *Session
	actor   string
	waiting bool
	since   time.Time
}

func (m *Manager) liveStreams() []liveStream {
	var out []liveStream
	for _, s := range m.all() {
		m.mu.Lock()
		if isStream(s) && s.proc != nil && s.proc.running() && !s.stopRequested {
			since, _ := parseISO(s.Meta.Value("last_activity_at"))
			out = append(out, liveStream{s: s, actor: strOr(s.Meta.Value("actor")),
				waiting: s.Meta.Value("turn_state") == "waiting", since: since})
		}
		m.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].since.Before(out[j].since) })
	return out
}

// makeRoom closes waiting processes so a new one for `actor` fits the
// limits.  False when the limits are reached by running turns.
func (m *Manager) makeRoom(actor string, self *Session) bool {
	live := m.liveStreams()
	total, mine := 0, 0
	for _, l := range live {
		if l.s == self {
			continue
		}
		total++
		if l.actor == actor {
			mine++
		}
	}
	for _, l := range live {
		if total < StreamMax && mine < StreamMaxPerUser {
			return true
		}
		if l.s == self || !l.waiting || (mine >= StreamMaxPerUser && total < StreamMax && l.actor != actor) {
			continue
		}
		m.closeIdle(l.s, "Closed to make room for another session; the next message resumes it.")
		total--
		if l.actor == actor {
			mine--
		}
	}
	return total < StreamMax && mine < StreamMaxPerUser
}

// startStreamReaper closes waiting processes after StreamIdleTimeout.
func (m *Manager) startStreamReaper() {
	m.stream.once.Do(func() {
		go func() {
			for range time.Tick(30 * time.Second) {
				for _, l := range m.liveStreams() {
					if l.waiting && !l.since.IsZero() && time.Since(l.since) > StreamIdleTimeout {
						m.closeIdle(l.s, "Closed after "+StreamIdleTimeout.String()+" without messages; the next message resumes it.")
					}
				}
			}
		}()
	})
}

// stderrRejectsStream reports a CLI that does not know the stream flags
// (its stderr).  finish then turns stream mode off for this server.
func (m *Manager) stderrRejectsStream(s *Session) bool {
	for _, e := range m.allLogEntries(s, "stderr") {
		low := strings.ToLower(strOr(e.Value("line")))
		if strings.Contains(low, "input-format") || strings.Contains(low, "unknown option") || strings.Contains(low, "stream-json") {
			return true
		}
	}
	return false
}

// disableStream turns stream input off for new Claude Code processes.
func (m *Manager) disableStream() {
	m.stream.mu.Lock()
	m.stream.disabled = true
	m.stream.mu.Unlock()
}
