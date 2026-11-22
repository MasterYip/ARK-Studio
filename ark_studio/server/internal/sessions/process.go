package sessions

import (
	"arkstudio/go/internal/secretredact"
	"bufio"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"arkstudio/go/internal/platform/textfile"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pyos"
	"arkstudio/go/internal/compat/pyurl"
	"arkstudio/go/internal/platform/procx"
)

// process is the subprocess.Popen of one turn.  A relayed process (relay
// set, relay.go) runs under `ark-server session-relay` and outlives the
// server; cmd and the pipes are nil then.
type process struct {
	cmd            *exec.Cmd
	stdin          *os.File
	stdout, stderr *os.File
	relay          *relayClient
	childPID       int
	exited         atomic.Bool
	stdinClosed    atomic.Bool
	done           chan struct{} // closed once the monitor has finished the session
}

func (p *process) running() bool { return !p.exited.Load() }

func (p *process) pid() int {
	if p.relay != nil {
		return p.childPID
	}
	return p.cmd.Process.Pid
}

func (p *process) closeStdin() {
	if p.stdinClosed.CompareAndSwap(false, true) {
		if p.relay != nil {
			_, _ = p.relay.call(relayRequest{Op: "close"}, relayCallTimeout)
			return
		}
		_ = p.stdin.Close()
	}
}

func (p *process) writeStdin(payload []byte) error {
	if p.relay != nil {
		return p.relay.write(payload)
	}
	_, err := p.stdin.Write(payload)
	return err
}

// killTree ends the process and its descendants.
func (p *process) killTree() {
	if p.relay != nil {
		p.relay.kill(p.childPID)
		return
	}
	if err := procx.KillTree(p.cmd); err != nil {
		_ = p.cmd.Process.Kill()
	}
}

func envPath(env []string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && strings.EqualFold(k, "PATH") {
			return v
		}
	}
	return os.Getenv("PATH")
}

// startProcess is subprocess.Popen(argv, cwd, env, stdin/stdout/stderr=PIPE)
// in a new process group / session without a console window.
func startProcess(argv []string, dir string, env []string) (*process, error) {
	name, err := lookProgram(argv[0], env)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(name, argv[1:]...)
	cmd.Dir, cmd.Env = dir, env
	if cmd.Err != nil {
		return nil, cmd.Err
	}
	procx.Isolate(cmd)
	if runtime.GOOS == "windows" {
		procx.SetCmdLine(cmd, pyos.List2Cmdline(argv))
	}
	var files []*os.File
	pipe := func() (*os.File, *os.File, error) {
		r, w, err := os.Pipe()
		if err == nil {
			files = append(files, r, w)
		}
		return r, w, err
	}
	closeAll := func() {
		for _, f := range files {
			f.Close()
		}
	}
	inR, inW, err := pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := pipe()
	if err != nil {
		closeAll()
		return nil, err
	}
	errR, errW, err := pipe()
	if err != nil {
		closeAll()
		return nil, err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
	if err := cmd.Start(); err != nil {
		closeAll()
		return nil, err
	}
	inR.Close()
	outW.Close()
	errW.Close()
	return &process{cmd: cmd, stdin: inW, stdout: outR, stderr: errR, done: make(chan struct{})}, nil
}

// osErrorText is str(OSError) for a failed Popen.
func osErrorText(err error, program string) string {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, exec.ErrNotFound) {
		if runtime.GOOS == "windows" {
			return "[WinError 2] " + procx.WinErrorText(2)
		}
		return "[Errno 2] No such file or directory: " + pyjson.Repr(program)
	}
	return err.Error()
}

// decodeLine is raw.decode("utf-8"), else "mbcs" with replace (Windows),
// else utf-8 with replace.
func decodeLine(raw []byte) string {
	if utf8.Valid(raw) {
		return string(raw)
	}
	if s, ok := decodeANSI(raw); ok {
		return s
	}
	return pyurl.DecodeReplace(raw)
}

// startProcess starts one turn's process: relayed (it survives a server
// restart) when the manager runs relays, else with pipes owned by the server.
func (m *Manager) startProcess(s *Session, argv []string, dir string, env []string) (*process, error) {
	if m.Relay {
		return startRelay(s.Folder, s.ID, argv, dir, env)
	}
	return startProcess(argv, dir, env)
}

func (m *Manager) startReaders(s *Session, p *process) {
	var wg sync.WaitGroup
	if p.relay != nil {
		for _, name := range []string{"stdout", "stderr"} {
			wg.Add(1)
			go func(name string) {
				defer wg.Done()
				m.tailStream(s, name, func() bool { return !p.running() })
			}(name)
		}
		go m.monitorRelay(s, p, &wg)
		return
	}
	for _, item := range []struct {
		name string
		f    *os.File
	}{{"stdout", p.stdout}, {"stderr", p.stderr}} {
		wg.Add(1)
		go func(name string, f *os.File) {
			defer wg.Done()
			m.readStream(s, name, f)
		}(item.name, item.f)
	}
	go m.monitor(s, p, &wg)
}

func (m *Manager) readStream(s *Session, name string, f *os.File) {
	r := bufio.NewReaderSize(f, 64*1024)
	for {
		raw, err := r.ReadBytes('\n')
		if len(raw) > 0 {
			m.handleLine(s, name, raw)
		}
		if err != nil {
			return
		}
	}
}

// handleLine records one output line and follows what it reports.
func (m *Manager) handleLine(s *Session, name string, raw []byte) {
	text := decodeLine(raw)
	m.appendEvent(s, name, text, "")
	m.touch(s, len(raw))
	func() {
		defer func() { _ = recover() }()
		m.observeNativeID(s, text)
	}()
	if name == "stdout" {
		func() {
			defer func() { _ = recover() }()
			m.observeOutputLine(s, text)
		}()
		func() {
			defer func() { _ = recover() }()
			m.observeStreamLine(s, text)
		}()
	}
}

func (m *Manager) monitor(s *Session, p *process, wg *sync.WaitGroup) {
	err := p.cmd.Wait()
	var code *int
	if p.cmd.ProcessState != nil {
		c := procx.ExitCode(p.cmd.ProcessState)
		code = &c
	} else {
		_ = err
	}
	p.exited.Store(true)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(4 * time.Second): // two readers joined with timeout=2 each
	}
	p.closeStdin()
	_ = p.stdout.Close()
	_ = p.stderr.Close()
	m.finish(s, code)
	close(p.done)
}

// appendEvent is ManagedSession.append_event.
func (m *Manager) appendEvent(s *Session, stream, line, kind string) {
	line = strings.TrimRight(line, "\r\n")
	// JSON event lines (stream-json tool results) get a larger limit: cut
	// short they cannot be parsed any more.
	max := maxLogLine
	if strings.HasPrefix(line, "{") {
		max = maxJSONLogLine
	}
	if utf8.RuneCountInString(line) > max {
		line = limit(line, max) + " …[truncated]"
	}
	if strings.Contains(line, "arka_") || (s.agentSecret != "" && strings.Contains(line, s.agentSecret)) {
		var extra []string
		if s.agentSecret != "" {
			extra = []string{s.agentSecret}
		}
		line, _ = secretredact.Text(line, extra)
	}
	entry := pyjson.NewObject("t", NowISO(), "line", line)
	if kind != "" {
		entry.Set("kind", kind)
	}
	s.logMu.Lock()
	if fh, err := os.OpenFile(filepath.Join(s.Folder, stream+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		_, _ = fh.Write(textfile.Encode(string(pyjson.Dumps(entry)) + "\n"))
		_ = fh.Close()
	}
	s.logMu.Unlock()
	m.mu.Lock()
	counts := m.countsLocked(s)
	counts.Set(stream, intOr(counts.Value(stream), 0)+1)
	m.mu.Unlock()
}

func (m *Manager) countsLocked(s *Session) *pyjson.Object {
	counts, ok := s.Meta.Value("counts").(*pyjson.Object)
	if !ok {
		counts = pyjson.NewObject("stdout", int64(0), "stderr", int64(0))
		s.Meta.Set("counts", counts)
	}
	return counts
}

// touch is ManagedSession.touch.
func (m *Manager) touch(s *Session, nbytes int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s.Meta.Set("last_activity_at", NowISO())
	counts := m.countsLocked(s)
	counts.Set("bytes", intOr(counts.Value("bytes"), 0)+int64(nbytes))
	if now := time.Now(); now.Sub(s.lastSave) >= 1500*time.Millisecond {
		s.lastSave = now
		m.saveLocked(s)
	}
}

// writeInput is ManagedSession.write_input (display nil = the text).
func (m *Manager) writeInput(s *Session, text string, record bool, display *string) error {
	m.mu.Lock()
	p := s.proc
	m.mu.Unlock()
	if p == nil || !p.running() {
		return serr("session is not running", 409, "")
	}
	if p.stdinClosed.Load() {
		return serr("session stdin is closed", 409, "")
	}
	payload := []byte(strings.TrimRight(text, "\r\n") + "\n")
	s.stdinMu.Lock()
	err := p.writeStdin(payload)
	s.stdinMu.Unlock()
	if err != nil {
		return serr("session stdin is closed", 409, "")
	}
	if record {
		shown := text
		if display != nil {
			shown = *display
		}
		m.appendEvent(s, "stdout", "› "+strings.TrimRight(shown, "\r\n"), "input")
	}
	m.touch(s, len(payload))
	return nil
}

// expirePendingLocked is expire_pending_approvals (caller holds m.mu).
func (m *Manager) expirePendingLocked(s *Session, reason string) int {
	count := 0
	now := NowISO()
	for _, r := range objects(s.Meta.Value("approvals")) {
		if r.Value("status") != "pending" {
			continue
		}
		r.Set("status", "expired").Set("message", reason).Set("decided_at", now).Set("decided_by", "system")
		count++
	}
	if count > 0 {
		s.Meta.Set("last_activity_at", now)
	}
	return count
}

// finish is ManagedSession.finish.
func (m *Manager) finish(s *Session, code *int) {
	// Read stderr before the status changes: the session must not show as
	// ended while stream mode is still on and its error still the generic
	// one (a new session started in that window would use the rejected
	// flags again).
	rejected := (code == nil || *code != 0) && m.stderrRejectsStream(s)
	m.mu.Lock()
	if !activeStatuses[strOr(s.Meta.Value("status"))] {
		m.mu.Unlock()
		return
	}
	m.expirePendingLocked(s, "the process ended before a decision")
	stopped := s.stopRequested || (code != nil && *code == 0)
	if stopped {
		s.Meta.Set("status", "stopped")
	} else {
		s.Meta.Set("status", "errored")
	}
	if s.stopRequested || code == nil {
		s.Meta.Set("exit_code", nil)
	} else {
		s.Meta.Set("exit_code", int64(*code))
	}
	now := NowISO()
	s.Meta.Set("ended_at", now).Set("last_activity_at", now)
	if code != nil && *code != 0 && !s.stopRequested {
		s.Meta.Set("error", "process exited with code "+pyjson.Str(int64(*code)))
	}
	if isStream(s) {
		s.Meta.Set("turn_state", "")
	}
	stderrCount := intOr(objOr(s.Meta.Value("counts")).Value("stderr"), 0)
	transcript := strings.ToLower(strOr(s.Meta.Value("platform"))) == "codex"
	if !stopped && stderrCount > 0 && !transcript && !pyjson.Truthy(s.Meta.Value("error")) {
		s.Meta.Set("error", "process exited with errors")
	}
	if !stopped && rejected && isStream(s) {
		m.disableStream()
		s.Meta.Set("error", "This Claude Code version does not take stream input; send the message again to run it as a one-shot turn.")
	}
	m.saveLocked(s)
	m.mu.Unlock()
	m.notify(s, "finish")
}

// terminate is SessionManager._terminate.
func (m *Manager) terminate(p *process) {
	if !p.running() {
		return
	}
	p.killTree()
	deadline := time.Now().Add(6 * time.Second)
	for p.running() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if p.running() {
		if p.relay != nil {
			p.relay.kill(p.childPID)
		} else {
			_ = p.cmd.Process.Kill()
		}
	}
}
