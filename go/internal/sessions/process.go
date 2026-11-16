package sessions

import (
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

	"arkstudio/go/internal/limits"
	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pyos"
	"arkstudio/go/internal/pyurl"
	"arkstudio/go/internal/store"
)

// process is the subprocess.Popen of one turn.
type process struct {
	cmd            *exec.Cmd
	stdin          *os.File
	stdout, stderr *os.File
	exited         atomic.Bool
	stdinClosed    atomic.Bool
}

func (p *process) running() bool { return !p.exited.Load() }
func (p *process) pid() int      { return p.cmd.Process.Pid }

func (p *process) closeStdin() {
	if p.stdinClosed.CompareAndSwap(false, true) {
		_ = p.stdin.Close()
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
	limits.Isolate(cmd)
	if runtime.GOOS == "windows" {
		limits.SetCmdLine(cmd, pyos.List2Cmdline(argv))
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
	return &process{cmd: cmd, stdin: inW, stdout: outR, stderr: errR}, nil
}

// osErrorText is str(OSError) for a failed Popen.
func osErrorText(err error, program string) string {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, exec.ErrNotFound) {
		if runtime.GOOS == "windows" {
			return "[WinError 2] " + winErrorText(2)
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

func (m *Manager) startReaders(s *Session, p *process) {
	var wg sync.WaitGroup
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
			}
		}
		if err != nil {
			return
		}
	}
}

func (m *Manager) monitor(s *Session, p *process, wg *sync.WaitGroup) {
	err := p.cmd.Wait()
	var code *int
	if p.cmd.ProcessState != nil {
		c := exitCode(p.cmd.ProcessState)
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
}

// appendEvent is ManagedSession.append_event.
func (m *Manager) appendEvent(s *Session, stream, line, kind string) {
	line = strings.TrimRight(line, "\r\n")
	if utf8.RuneCountInString(line) > maxLogLine {
		line = limit(line, maxLogLine) + " …[truncated]"
	}
	entry := pyjson.NewObject("t", NowISO(), "line", line)
	if kind != "" {
		entry.Set("kind", kind)
	}
	s.logMu.Lock()
	if fh, err := os.OpenFile(filepath.Join(s.Folder, stream+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		_, _ = fh.Write(store.TextFile(string(pyjson.Dumps(entry)) + "\n"))
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
	_, err := p.stdin.Write(payload)
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
	stderrCount := intOr(objOr(s.Meta.Value("counts")).Value("stderr"), 0)
	transcript := strings.ToLower(strOr(s.Meta.Value("platform"))) == "codex"
	if !stopped && stderrCount > 0 && !transcript && !pyjson.Truthy(s.Meta.Value("error")) {
		s.Meta.Set("error", "process exited with errors")
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
	if err := limits.KillTree(p.cmd); err != nil {
		_ = p.cmd.Process.Kill()
	}
	deadline := time.Now().Add(6 * time.Second)
	for p.running() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if p.running() {
		_ = p.cmd.Process.Kill()
	}
}
