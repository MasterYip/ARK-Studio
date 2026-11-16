package workspace

import (
	"io"
	"math"
	"math/big"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"arkstudio/go/internal/platform/procx"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/compat/pyurl"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/rnd"
)

// PTYInfo is pty_availability().
type PTYInfo struct {
	Available                      bool
	Backend, Version, Reason, Hint string
}

// handle is a running child: a stdio process or a pseudo-terminal.
type handle interface {
	pid() int
	write(data []byte) error
	resize(cols, rows int) error
	close()
}

type entry struct {
	seq    int64
	stream string
	text   string
	t      string
}

// Terminal is TerminalSession.
type Terminal struct {
	ID, Command, Cwd                string
	ProjectID, ProjectPath, Creator string
	timeout                         int64
	pty, ptyRequested               bool
	ptyBackend, ptyFallback         string

	mu          sync.Mutex
	cols, rows  int64
	startedAt   string
	startedMono time.Time
	finishedAt  string
	exitCode    *int64
	status      string
	stopReason  string
	entries     []entry
	nextSeq     int64
	totalBytes  int
	truncated   bool
	lineCount   int

	cmd     *exec.Cmd
	h       handle
	stdin   io.WriteCloser
	exited  chan struct{}
	exitVal int64
	pumps   sync.WaitGroup
	killing atomic.Bool
}

// append is TerminalSession.append.
func (t *Terminal) append(stream, text string) {
	if text == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.truncated {
		return
	}
	data := text
	if !t.pty {
		data = strings.ReplaceAll(text, "\r\n", "\n")
	}
	remaining := MaxTerminalOutput - t.totalBytes
	if remaining <= 0 {
		t.markTruncatedLocked()
		return
	}
	if len(data) > remaining {
		keep := []byte(data)[:remaining]
		for len(keep) > 0 && !validUTF8(keep) {
			keep = keep[:len(keep)-1]
		}
		if len(keep) > 0 {
			t.pushLocked(stream, string(keep))
		}
		t.markTruncatedLocked()
		return
	}
	t.pushLocked(stream, data)
}

func (t *Terminal) pushLocked(stream, text string) {
	t.entries = append(t.entries, entry{t.nextSeq, stream, text, now()})
	t.nextSeq++
	t.totalBytes += len(text)
	t.lineCount += strings.Count(text, "\n")
	if n := len(t.entries) - MaxTerminalEntries; n > 0 {
		t.entries = append([]entry(nil), t.entries[n:]...)
	}
}

func (t *Terminal) markTruncatedLocked() {
	if t.truncated {
		return
	}
	t.truncated = true
	if t.stopReason == "" {
		t.stopReason = "output_limit"
	}
	t.pushLocked("system", "\n…[output limit reached; command stopped by ARK]\n")
}

// Snapshot is TerminalSession.snapshot.
func (t *Terminal) Snapshot(offset, limit int64) *pyjson.Object {
	t.mu.Lock()
	defer t.mu.Unlock()
	limit = max(0, min(1000, limit))
	start := 0
	if offset > 0 {
		start = sort.Search(len(t.entries), func(i int) bool { return t.entries[i].seq > offset })
	}
	end := min(len(t.entries), start+int(limit))
	items := []any{}
	next := offset
	for _, e := range t.entries[start:end] {
		items = append(items, pyjson.NewObject("seq", e.seq, "stream", e.stream, "text", e.text, "t", e.t))
		next = e.seq
	}
	first := int64(1)
	if len(t.entries) > 0 {
		first = t.entries[0].seq
	}
	var exit any
	if t.exitCode != nil {
		exit = *t.exitCode
	}
	return pyjson.NewObject(
		"id", t.ID, "command", t.Command, "cwd", t.Cwd, "project_id", t.ProjectID,
		"project_path", t.ProjectPath, "creator", t.Creator, "status", t.status,
		"exit_code", exit, "stop_reason", t.stopReason, "entries", items, "offset", next,
		"dropped_before", max(0, first-1), "truncated", t.truncated, "started_at", t.startedAt,
		"finished_at", t.finishedAt, "timeout", t.timeout, "total_bytes", t.totalBytes,
		"lines", t.lineCount, "pty", t.pty, "pty_requested", t.ptyRequested,
		"pty_backend", t.ptyBackend, "pty_fallback", t.ptyFallback, "cols", t.cols, "rows", t.rows)
}

// Public is TerminalSession.public().
func (t *Terminal) Public() *pyjson.Object { return t.Snapshot(0, 0) }

// Status is the current status.
func (t *Terminal) Status() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.status
}

func (t *Terminal) running() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.status == "running" || t.status == "stopping"
}

func (t *Terminal) hasExited() bool {
	select {
	case <-t.exited:
		return true
	default:
		return false
	}
}

// Manager is TerminalManager: a bounded registry owned by one server.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Terminal
	order    []string
}

// NewManager returns an empty registry.
func NewManager() *Manager { return &Manager{sessions: map[string]*Terminal{}} }

// pyFloatInt is int(float(value)) for a decoded JSON value; ok=false where
// Python raises TypeError/ValueError.
func pyFloatInt(v any) (int64, bool, error) {
	var f float64
	switch x := v.(type) {
	case bool:
		if x {
			f = 1
		}
	case int64:
		f = float64(x)
	case *big.Int:
		f, _ = new(big.Float).SetInt(x).Float64()
	case float64:
		f = x
	case string:
		parsed, err := strconv.ParseFloat(strings.ReplaceAll(pytext.Strip(x), "_", ""), 64)
		if err != nil && !strings.Contains(err.Error(), "range") {
			return 0, false, nil
		}
		f = parsed
	default:
		return 0, false, nil
	}
	if math.IsNaN(f) {
		return 0, false, nil
	}
	if math.IsInf(f, 0) {
		return 0, false, &OverflowError{"cannot convert float infinity to integer"}
	}
	if f > math.MaxInt64 {
		return math.MaxInt64, true, nil
	}
	if f < math.MinInt64 {
		return math.MinInt64, true, nil
	}
	return int64(f), true, nil
}

// OverflowError is a Python OverflowError (an unhandled 500 in Python).
type OverflowError struct{ Message string }

func (e *OverflowError) Error() string { return e.Message }

// pyIntValue is int(value) for a decoded JSON value.
func pyIntValue(v any) (int64, string, bool) {
	switch x := v.(type) {
	case bool:
		if x {
			return 1, "1", true
		}
		return 0, "0", true
	case int64:
		return x, strconv.FormatInt(x, 10), true
	case *big.Int:
		if x.Sign() > 0 {
			return math.MaxInt64, x.String(), true
		}
		return math.MinInt64, x.String(), true
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, "", false
		}
		n := int64(x)
		return n, strconv.FormatInt(n, 10), true
	case string:
		n, err := pytext.Int(x)
		if err != nil {
			return 0, "", false
		}
		return n, strconv.FormatInt(n, 10), true
	}
	return 0, "", false
}

// terminalSize is _terminal_size.
func terminalSize(cols, rows any, strict bool) (int64, int64, error) {
	type dim struct {
		label     string
		value     any
		low, high int64
		def       int64
	}
	var out [2]int64
	for i, d := range []dim{{"cols", cols, TerminalMinCols, TerminalMaxCols, TerminalDefaultCols},
		{"rows", rows, TerminalMinRows, TerminalMaxRows, TerminalDefaultRows}} {
		if d.value == nil || d.value == "" {
			out[i] = d.def
			continue
		}
		n, text, ok := pyIntValue(d.value)
		if !ok {
			if strict {
				return 0, 0, werr(400, "%s must be an integer", d.label)
			}
			out[i] = d.def
			continue
		}
		if n < d.low || n > d.high {
			if strict {
				return 0, 0, werr(400, "%s must be between %d and %d (got %s)", d.label, d.low, d.high, text)
			}
			n = max(d.low, min(d.high, n))
		}
		out[i] = n
	}
	return out[0], out[1], nil
}

// terminalEnv is _terminal_env: the host env minus ARK secrets.
func terminalEnv(pty bool) []string {
	var keys []string
	env := map[string]string{}
	set := func(k, v string) {
		if _, ok := env[k]; !ok {
			keys = append(keys, k)
		}
		env[k] = v
	}
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			continue
		}
		if runtime.GOOS == "windows" {
			k = strings.ToUpper(k)
		}
		upper := strings.ToUpper(k)
		if strings.HasPrefix(upper, "ARK_") && (strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") ||
			strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "CREDENTIAL")) {
			continue
		}
		set(k, v)
	}
	set("PYTHONUNBUFFERED", "1")
	set("PYTHONIOENCODING", "utf-8")
	set("ARK_TERMINAL", "1")
	if pty {
		set("TERM", "xterm-256color")
		set("COLORTERM", "truecolor")
		set("TERM_PROGRAM", "ARK")
		for _, k := range []string{"NO_COLOR", "CLICOLOR", "FORCE_COLOR"} {
			delete(env, k)
		}
	} else {
		set("TERM", "dumb")
		set("NO_COLOR", "1")
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if v, ok := env[k]; ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// StartOptions are the TerminalManager.start keyword arguments.
type StartOptions struct {
	Command                         any
	Cwd                             any
	Timeout                         any
	PTY                             bool
	Cols, Rows                      any
	ProjectID, ProjectPath, Creator string
}

// Start is TerminalManager.start.
func (m *Manager) Start(root string, o StartOptions) (*Terminal, PTYInfo, error) {
	var none PTYInfo
	rootPath := project.Resolve(root)
	text := ""
	if pyjson.Truthy(o.Command) {
		text = pytext.Strip(pyjson.Str(o.Command))
	}
	if text == "" {
		return nil, none, werr(400, "command is required")
	}
	if pytext.Len(text) > MaxTerminalCommand {
		return nil, none, werr(400, "command is too long (limit %d characters)", MaxTerminalCommand)
	}
	if strings.Contains(text, "\x00") {
		return nil, none, werr(400, "command contains a NUL byte")
	}
	if strings.ContainsAny(text, "\r\n") {
		return nil, none, werr(400, "command must be a single line")
	}
	target, cwdRel, err := resolve(rootPath, o.Cwd, true)
	if err != nil {
		return nil, none, err
	}
	if !isDir(target) {
		return nil, none, werr(400, "cwd must be a folder inside the project")
	}
	timeout, ok, err := pyFloatInt(o.Timeout)
	if err != nil {
		return nil, none, err
	}
	if !ok {
		timeout = TerminalDefaultTimeout
	}
	timeout = max(1, min(TerminalMaxTimeout, timeout))
	cols, rows, err := terminalSize(o.Cols, o.Rows, o.PTY)
	if err != nil {
		return nil, none, err
	}
	m.Sweep()
	m.mu.Lock()
	active := 0
	for _, t := range m.sessions {
		if t.running() {
			active++
		}
	}
	m.mu.Unlock()
	if active >= MaxTerminals {
		return nil, none, werr(409, "too many running terminals (limit %d); stop one first", MaxTerminals)
	}
	availability := PTYInfo{}
	if o.PTY {
		availability = PTYAvailability()
	}
	t := &Terminal{Command: text, Cwd: cwdRel, ProjectID: o.ProjectID, ProjectPath: o.ProjectPath,
		Creator: o.Creator, timeout: timeout, ptyRequested: o.PTY, cols: cols, rows: rows,
		status: "running", nextSeq: 1, exited: make(chan struct{})}
	if o.PTY && !availability.Available {
		t.ptyFallback = availability.Reason
		if t.ptyFallback == "" {
			t.ptyFallback = "PTY support is unavailable"
		}
	}
	var ptyHandle ptyProcess
	if o.PTY && availability.Available {
		ptyHandle, err = startPTY(text, target, terminalEnv(true), int(cols), int(rows))
		if err != nil {
			ptyHandle = nil
			t.ptyFallback = "PTY could not start (" + err.Error() + "); using stdio"
		}
	}
	if ptyHandle != nil {
		t.pty, t.ptyBackend, t.h, t.cmd = true, availability.Backend, ptyHandle, ptyHandle.command()
	} else if err := t.startStdio(text, target); err != nil {
		return nil, none, werr(400, "could not start command: %s", osMessage(err))
	}
	t.ID = "term_" + rnd.UUID4Hex()[:12]
	t.startedAt = now()
	t.startedMono = time.Now()
	m.mu.Lock()
	m.sessions[t.ID] = t
	m.order = append(m.order, t.ID)
	m.mu.Unlock()
	go func() {
		state, _ := t.cmd.Process.Wait()
		t.exitVal = int64(procx.ExitCode(state))
		close(t.exited)
	}()
	if t.pty {
		t.pumps.Add(1)
		go t.pumpPTY(ptyHandle)
	}
	go m.watch(t)
	return t, PTYAvailability(), nil
}

func (t *Terminal) startStdio(text, dir string) error {
	cmd := shellCommand(text)
	cmd.Dir = dir
	cmd.Env = terminalEnv(false)
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		return err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdinR, outW, errW
	err = cmd.Start()
	stdinR.Close()
	outW.Close()
	errW.Close()
	if err != nil {
		stdinW.Close()
		outR.Close()
		errR.Close()
		return err
	}
	t.cmd, t.stdin = cmd, stdinW
	t.h = &stdioHandle{cmd: cmd}
	t.pumps.Add(2)
	go t.pump("stdout", outR)
	go t.pump("stderr", errR)
	return nil
}

type stdioHandle struct{ cmd *exec.Cmd }

func (s *stdioHandle) pid() int              { return s.cmd.Process.Pid }
func (s *stdioHandle) write([]byte) error    { return io.ErrClosedPipe }
func (s *stdioHandle) resize(int, int) error { return nil }
func (s *stdioHandle) close()                {}
func validUTF8(b []byte) bool                { return pyurl.DecodeReplace(b) == string(b) }
func (m *Manager) lookup(id string) *Terminal {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

// pump is TerminalManager._pump.
func (t *Terminal) pump(stream string, r io.ReadCloser) {
	defer t.pumps.Done()
	defer r.Close()
	var dec pyurl.Decoder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			t.append(stream, dec.Decode(buf[:n], false))
		}
		if err != nil {
			break
		}
	}
	t.append(stream, dec.Decode(nil, true))
}

// pumpPTY is TerminalManager._pump_pty.
func (t *Terminal) pumpPTY(p ptyProcess) {
	defer t.pumps.Done()
	var dec pyurl.Decoder
	buf := make([]byte, 4096)
	for {
		n, err := p.read(buf)
		if n > 0 {
			t.append("stdout", dec.Decode(buf[:n], false))
		}
		if err != nil {
			break
		}
	}
	t.append("stdout", dec.Decode(nil, true))
}

// watch is TerminalManager._watch.
func (m *Manager) watch(t *Terminal) {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for !t.hasExited() {
		t.mu.Lock()
		reason := t.stopReason
		t.mu.Unlock()
		if reason != "" && !t.killing.Load() {
			t.killing.Store(true)
			m.killProcess(t)
		} else if reason == "" && time.Since(t.startedMono) >= time.Duration(t.timeout)*time.Second {
			t.mu.Lock()
			t.status, t.stopReason = "stopping", "timeout"
			t.mu.Unlock()
			t.killing.Store(true)
			m.killProcess(t)
		}
		select {
		case <-t.exited:
		case <-tick.C:
		}
	}
	drained := make(chan struct{})
	go func() { t.pumps.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(1500 * time.Millisecond):
		if t.pty {
			t.h.close()
			select {
			case <-drained:
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	t.mu.Lock()
	code := t.exitVal
	t.exitCode = &code
	t.finishedAt = now()
	switch t.stopReason {
	case "cancelled", "server_shutdown":
		t.status = "cancelled"
	case "timeout":
		t.status = "timeout"
	case "output_limit":
		t.status = "output_limit"
	default:
		t.status = "exited"
	}
	if t.stopReason == "output_limit" && !t.truncated {
		t.markTruncatedLocked()
	}
	t.mu.Unlock()
	if t.pty {
		t.h.close()
	} else if t.stdin != nil {
		t.stdin.Close()
	}
}

// killProcess is TerminalManager._kill_process.
func (m *Manager) killProcess(t *Terminal) {
	if t.hasExited() {
		return
	}
	procx.KillProcessTree(t.h.pid())
	select {
	case <-t.exited:
	case <-time.After(5 * time.Second):
		t.cmd.Process.Kill() //nolint:errcheck
	}
}

func (m *Manager) kill(t *Terminal) {
	if t.killing.Swap(true) {
		return
	}
	m.killProcess(t)
}

// Get is TerminalManager.get; projectID nil skips the project check.
func (m *Manager) Get(id string, projectID *string) (*Terminal, error) {
	t := m.lookup(id)
	if t == nil || (projectID != nil && t.ProjectID != *projectID) {
		return nil, werr(404, "terminal not found")
	}
	return t, nil
}

// List is TerminalManager.list.
func (m *Manager) List(projectID string) *pyjson.Object {
	m.mu.Lock()
	var items []*Terminal
	for _, id := range m.order {
		if t := m.sessions[id]; t != nil && t.ProjectID == projectID {
			items = append(items, t)
		}
	}
	m.mu.Unlock()
	sort.SliceStable(items, func(i, j int) bool { return items[i].startedAt > items[j].startedAt })
	out := make([]any, len(items))
	running := 0
	for i, t := range items {
		out[i] = t.Public()
		if t.running() {
			running++
		}
	}
	return pyjson.NewObject("terminals", out, "count", len(items), "running", running, "limit", MaxTerminals)
}

// Read is TerminalManager.read.
func (m *Manager) Read(id string, offset, limit int64) (*pyjson.Object, error) {
	t, err := m.Get(id, nil)
	if err != nil {
		return nil, err
	}
	return t.Snapshot(max(0, offset), max(1, min(1000, limit))), nil
}

// Write is TerminalManager.write (stdin).
func (m *Manager) Write(id string, value string) (*pyjson.Object, error) {
	t, err := m.Get(id, nil)
	if err != nil {
		return nil, err
	}
	if value == "" {
		return nil, werr(400, "stdin text is required")
	}
	if pytext.Len(value) > terminalStdinLimitChars {
		return nil, werr(400, "stdin text is too long (limit 8000 characters)")
	}
	if !t.running() {
		return nil, werr(409, "terminal is not running")
	}
	if t.pty {
		payload := value
		if !strings.HasSuffix(value, "\r") && !strings.HasSuffix(value, "\n") {
			payload += "\r"
		}
		if err := t.h.write([]byte(payload)); err != nil {
			return nil, werr(409, "terminal stdin is closed")
		}
		return t.Snapshot(0, 0), nil
	}
	if t.stdin == nil {
		return nil, werr(409, "terminal stdin is closed")
	}
	payload := value
	if !strings.HasSuffix(value, "\n") {
		payload += "\n"
	}
	if _, err := t.stdin.Write([]byte(payload)); err != nil {
		return nil, werr(409, "terminal stdin is closed")
	}
	t.append("stdin", "› "+strings.TrimRight(value, "\r\n")+"\n")
	return t.Snapshot(0, 0), nil
}

// Resize is TerminalManager.resize.
func (m *Manager) Resize(id string, cols, rows any) (*pyjson.Object, error) {
	t, err := m.Get(id, nil)
	if err != nil {
		return nil, err
	}
	c, r, err := terminalSize(cols, rows, true)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	if t.status != "running" && t.status != "stopping" {
		t.mu.Unlock()
		return nil, werr(409, "terminal is not running")
	}
	t.cols, t.rows = c, r
	t.mu.Unlock()
	if !t.pty {
		return pyjson.NewObject("terminal", t.Snapshot(0, 0), "resized", false,
			"reason", "terminal is not a PTY; stdio pipes have no window size"), nil
	}
	if err := t.h.resize(int(c), int(r)); err != nil {
		return nil, werr(409, "could not resize terminal: %s", err.Error())
	}
	return pyjson.NewObject("terminal", t.Snapshot(0, 0), "resized", true, "reason", ""), nil
}

// Cancel is TerminalManager.cancel.
func (m *Manager) Cancel(id string) (*pyjson.Object, error) {
	t, err := m.Get(id, nil)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	if t.status != "running" && t.status != "stopping" {
		t.mu.Unlock()
		return t.Snapshot(0, 0), nil
	}
	t.status = "stopping"
	if t.stopReason == "" {
		t.stopReason = "cancelled"
	}
	t.mu.Unlock()
	go m.kill(t)
	return t.Snapshot(0, 0), nil
}

// Close is TerminalManager.close.
func (m *Manager) Close(id string) (*pyjson.Object, error) {
	t, err := m.Get(id, nil)
	if err != nil {
		return nil, err
	}
	if t.running() {
		return nil, werr(409, "stop the terminal before closing it")
	}
	m.mu.Lock()
	m.remove(t.ID)
	m.mu.Unlock()
	return pyjson.NewObject("closed", t.ID), nil
}

func (m *Manager) remove(id string) {
	delete(m.sessions, id)
	for i, x := range m.order {
		if x == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
}

// Sweep drops finished terminals older than ten minutes.
func (m *Manager) Sweep() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	removed := 0
	for _, id := range append([]string(nil), m.order...) {
		t := m.sessions[id]
		if !t.running() && time.Since(t.startedMono) > 600*time.Second {
			m.remove(id)
			removed++
		}
	}
	return removed
}

// Shutdown stops every running terminal (server exit).
func (m *Manager) Shutdown() {
	m.mu.Lock()
	var items []*Terminal
	for _, t := range m.sessions {
		items = append(items, t)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, t := range items {
		if !t.running() {
			continue
		}
		t.mu.Lock()
		t.status = "stopping"
		if t.stopReason == "" {
			t.stopReason = "server_shutdown"
		}
		t.mu.Unlock()
		wg.Add(1)
		go func() { defer wg.Done(); m.kill(t) }()
	}
	wg.Wait()
}
