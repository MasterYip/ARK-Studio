package sessions

// Sessions that outlive the server (2026-11-22).  A server restart (a
// deploy, an update) used to end every agent session: the server owned the
// agent's stdin and stdout pipes, so the agent lost them when the server
// exited.  With Manager.Relay each process runs under a small relay,
// `ark-server session-relay`, started in its own session (setsid):
//
//   - the agent writes stdout/stderr straight to <session>/stdout.raw and
//     stderr.raw; the server tails them and records each line in
//     stdout.jsonl / stderr.jsonl as before, keeping how far it got in
//     stdout.pos / stderr.pos;
//   - the relay owns the agent's stdin and answers on a unix socket
//     (relay.json names it): write, close (stdin), kill, ping, and wait
//     (answered when the agent exits; the exit code is also kept in
//     exit.json).
//
// On shutdown the server only detaches (Detach): it stops taking lines in,
// so the next server continues from the saved positions without gaps or
// repeats.  The next server's Resume re-attaches every session whose relay
// still answers and finishes the others with their exit code (or "stopped"
// with a notice when the relay vanished, e.g. after a reboot).  The relay
// files are removed when the session's process has been finished.
//
// The protocol is one JSON line each way per connection; a server must
// understand relays started by older builds, so fields are only added.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	relayInfoFile    = "relay.json"
	relayExitFile    = "exit.json"
	relayLogFile     = "relay.log"
	relayCallTimeout = 10 * time.Second
	relayPoll        = 100 * time.Millisecond
	endedOffline     = "The agent process ended while ARK was not running (for example a machine restart)."
)

type relayRequest struct {
	Op   string `json:"op"` // ping, write, close, kill, wait
	Data string `json:"data,omitempty"`
}

type relayReply struct {
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	Session     string `json:"session,omitempty"`
	ChildPID    int    `json:"child_pid,omitempty"`
	StdinClosed bool   `json:"stdin_closed,omitempty"`
	Exited      bool   `json:"exited,omitempty"`
	Code        *int   `json:"code,omitempty"`
}

// relayInfo is relay.json, written by the relay once the agent runs.
type relayInfo struct {
	V         int    `json:"v"`
	Session   string `json:"session"`
	Sock      string `json:"sock"`
	RelayPID  int    `json:"relay_pid"`
	ChildPID  int    `json:"child_pid"`
	StartedAt string `json:"started_at"`
}

// relayExit is exit.json, written by the relay when the agent exits.
type relayExit struct {
	Code    *int   `json:"code"`
	EndedAt string `json:"ended_at"`
}

func hasRelay(folder string) bool {
	_, err := os.Stat(filepath.Join(folder, relayInfoFile))
	return err == nil
}

func readRelayInfo(folder string) (relayInfo, bool) {
	var info relayInfo
	raw, err := os.ReadFile(filepath.Join(folder, relayInfoFile))
	if err != nil || json.Unmarshal(raw, &info) != nil || info.Sock == "" {
		return info, false
	}
	return info, true
}

func readRelayExit(folder string) (*int, bool) {
	var ex relayExit
	raw, err := os.ReadFile(filepath.Join(folder, relayExitFile))
	if err != nil || json.Unmarshal(raw, &ex) != nil {
		return nil, false
	}
	return ex.Code, true
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func rawPath(folder, stream string) string { return filepath.Join(folder, stream+".raw") }
func posPath(folder, stream string) string { return filepath.Join(folder, stream+".pos") }

func readPos(folder, stream string) int64 {
	raw, err := os.ReadFile(posPath(folder, stream))
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// resetRelayFiles prepares a session folder for a new relayed process.
func resetRelayFiles(folder string) error {
	for _, name := range []string{relayInfoFile, relayExitFile, "stdout.pos", "stderr.pos"} {
		if err := os.Remove(filepath.Join(folder, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, stream := range []string{"stdout", "stderr"} {
		if err := os.WriteFile(rawPath(folder, stream), nil, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// cleanupRelay removes the relay files of a finished process.
func cleanupRelay(folder, sock string) {
	for _, name := range []string{relayInfoFile, relayExitFile, "stdout.pos", "stderr.pos", "stdout.raw", "stderr.raw"} {
		_ = os.Remove(filepath.Join(folder, name))
	}
	if fi, err := os.Stat(filepath.Join(folder, relayLogFile)); err == nil && fi.Size() == 0 {
		_ = os.Remove(filepath.Join(folder, relayLogFile))
	}
	if sock != "" {
		if fi, err := os.Lstat(sock); err == nil && fi.Mode()&os.ModeSocket != 0 {
			_ = os.Remove(sock)
		}
	}
}

// relayClient talks to one relay; every call is its own connection, so a
// write blocked on a full pipe never holds up a kill.
type relayClient struct{ sock string }

func (c *relayClient) call(req relayRequest, timeout time.Duration) (relayReply, error) {
	var reply relayReply
	conn, err := net.DialTimeout("unix", c.sock, 3*time.Second)
	if err != nil {
		return reply, err
	}
	defer conn.Close()
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	line, _ := json.Marshal(req)
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return reply, err
	}
	raw, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return reply, err
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return reply, err
	}
	return reply, nil
}

func (c *relayClient) write(payload []byte) error {
	r, err := c.call(relayRequest{Op: "write", Data: string(payload)}, 0)
	if err != nil {
		return err
	}
	if !r.OK {
		return errors.New(r.Error)
	}
	return nil
}

// kill ends the agent's process group; when the relay does not answer the
// group is signalled directly.
func (c *relayClient) kill(childPID int) {
	if r, err := c.call(relayRequest{Op: "kill"}, relayCallTimeout); err == nil && r.OK {
		return
	}
	killGroup(childPID)
}

// wait blocks until the agent exits: its exit code, and false when the
// relay vanished without one (killed, or the machine restarted).
func (c *relayClient) wait(folder string) (*int, bool) {
	if r, err := c.call(relayRequest{Op: "wait"}, 0); err == nil && r.OK && r.Exited {
		return r.Code, true
	}
	return readRelayExit(folder)
}

// tailStream records the lines of <stream>.raw from the saved position
// until exited() reports the process gone and the file is read to its end.
func (m *Manager) tailStream(s *Session, stream string, exited func() bool) {
	f, err := os.Open(rawPath(s.Folder, stream))
	if err != nil {
		return
	}
	defer f.Close()
	offset := readPos(s.Folder, stream)
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return
	}
	buf := make([]byte, 64*1024)
	var pending []byte
	final := false
	for {
		if m.detaching.Load() {
			return
		}
		n, err := f.Read(buf)
		if n > 0 {
			pending = append(pending, buf[:n]...)
			for {
				i := bytes.IndexByte(pending, '\n')
				if i < 0 {
					break
				}
				if !m.ingest(s, stream, pending[:i+1], offset+int64(i+1)) {
					return
				}
				offset += int64(i + 1)
				pending = pending[i+1:]
			}
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return
		}
		if final {
			if len(pending) > 0 {
				m.ingest(s, stream, pending, offset+int64(len(pending)))
			}
			return
		}
		if exited() {
			final = true // read once more to the end: the last lines
			continue
		}
		time.Sleep(relayPoll)
	}
}

// ingest records one line and saves the position after it; false once the
// manager detaches (the next server takes the line).
func (m *Manager) ingest(s *Session, stream string, raw []byte, end int64) bool {
	s.ingestMu.Lock()
	defer s.ingestMu.Unlock()
	if m.detaching.Load() {
		return false
	}
	m.handleLine(s, stream, raw)
	_ = os.WriteFile(posPath(s.Folder, stream), []byte(strconv.FormatInt(end, 10)), 0o600)
	return true
}

// monitorRelay is monitor for a relayed process.
func (m *Manager) monitorRelay(s *Session, p *process, wg *sync.WaitGroup) {
	defer close(p.done)
	code, known := p.relay.wait(s.Folder)
	p.exited.Store(true)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
	if m.detaching.Load() {
		return // the next server finishes it
	}
	m.finishRelayed(s, p.relay.sock, code, known, "The agent process ended without an exit code (its relay was stopped).")
}

// finishRelayed removes the relay files, then finishes the session (before:
// a process started right after finish reuses the folder).
func (m *Manager) finishRelayed(s *Session, sock string, code *int, known bool, notice string) {
	m.mu.Lock()
	if strOr(s.Meta.Value("status")) == "stopping" {
		s.stopRequested = true
	}
	if !known {
		s.stopRequested = true
		s.Meta.Set("notice", notice)
	}
	m.mu.Unlock()
	cleanupRelay(s.Folder, sock)
	m.finish(s, code)
}

// Detach lets go of relayed processes before the server exits: no more
// output lines are taken in (the next server continues where this one
// stopped) and every session's metadata is saved.  The processes keep
// running.  Processes with pipes (no relay) end with the server as before.
func (m *Manager) Detach() {
	m.detaching.Store(true)
	for _, s := range m.all() {
		s.ingestMu.Lock()
		s.ingestMu.Unlock() //nolint:staticcheck // waits for a line in progress
	}
	m.mu.Lock()
	for _, id := range m.order {
		if s := m.sessions[id]; s.proc != nil {
			m.saveLocked(s)
		}
	}
	m.mu.Unlock()
}

// Resume re-attaches the relayed processes a previous server left running
// and finishes the sessions whose process ended meanwhile.  The server calls
// it once, after OnChange is set.
func (m *Manager) Resume() (attached, ended int) {
	for _, s := range m.all() {
		m.mu.Lock()
		active := activeStatuses[strOr(s.Meta.Value("status"))] && s.proc == nil
		m.mu.Unlock()
		if !active {
			continue
		}
		info, ok := readRelayInfo(s.Folder)
		if !ok {
			if hasRelay(s.Folder) { // unreadable: the relay never got going
				m.finishRelayed(s, "", nil, false, endedOffline)
				ended++
			}
			continue
		}
		c := &relayClient{sock: info.Sock}
		if r, err := c.call(relayRequest{Op: "ping"}, 3*time.Second); err == nil && r.OK && r.Session == s.ID && !r.Exited {
			m.reattach(s, c, r)
			attached++
			continue
		}
		// The process ended while no server ran: take in its last output.
		gone := func() bool { return true }
		m.tailStream(s, "stdout", gone)
		m.tailStream(s, "stderr", gone)
		code, known := readRelayExit(s.Folder)
		m.finishRelayed(s, info.Sock, code, known, endedOffline)
		ended++
	}
	return attached, ended
}

func (m *Manager) reattach(s *Session, c *relayClient, r relayReply) {
	p := &process{relay: c, childPID: r.ChildPID, done: make(chan struct{})}
	p.stdinClosed.Store(r.StdinClosed)
	m.mu.Lock()
	s.proc = p
	stopping := strOr(s.Meta.Value("status")) == "stopping"
	if stopping {
		s.stopRequested = true
	}
	stream := isStream(s)
	interrupting := stream && s.Meta.Value("turn_state") == "interrupting"
	if pid := int64(r.ChildPID); pid > 0 {
		s.Meta.Set("pid", pid)
	}
	m.saveLocked(s)
	m.mu.Unlock()
	m.startReaders(s, p)
	if stream {
		m.startStreamReaper()
	}
	// A Stop that was waiting for the process when the server restarted.
	switch {
	case stopping:
		go m.killIfStuck(s, p, p.running)
	case interrupting:
		go m.killIfStuck(s, p, func() bool {
			m.mu.Lock()
			defer m.mu.Unlock()
			return s.Meta.Value("turn_state") == "interrupting"
		})
	}
}
