//go:build !windows

package sessions

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"arkstudio/go/internal/platform/procx"
)

// relaySupported: relays run by default on Linux; ARK_SESSION_RELAY=0
// turns them off, =1 turns them on elsewhere (macOS).
func relaySupported() bool {
	switch os.Getenv("ARK_SESSION_RELAY") {
	case "0":
		return false
	case "1":
		return true
	}
	return runtime.GOOS == "linux"
}

// relayExe is the running server binary; /proc/self/exe keeps working after
// a deploy replaced the file.
func relayExe() string {
	if runtime.GOOS == "linux" {
		return "/proc/self/exe"
	}
	exe, _ := os.Executable()
	return exe
}

// relaySocketPath is <session>/relay.sock, or a short path under the temp
// folder when that is too long for a unix socket.
func relaySocketPath(folder, id string) (string, error) {
	p := filepath.Join(folder, "relay.sock")
	if len(p) <= 100 {
		return p, nil
	}
	dir := filepath.Join(os.TempDir(), "ark-relay-"+strconv.Itoa(os.Getuid()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, id+".sock"), nil
}

func killGroup(pid int) {
	if pid <= 1 {
		return
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// startRelay starts argv under a relay in its own session and waits until
// the agent runs (or failed to start).
func startRelay(folder, id string, argv []string, dir string, env []string) (*process, error) {
	name, err := lookProgram(argv[0], env)
	if err != nil {
		return nil, err
	}
	if err := resetRelayFiles(folder); err != nil {
		return nil, err
	}
	sock, err := relaySocketPath(folder, id)
	if err != nil {
		return nil, err
	}
	logf, err := os.OpenFile(filepath.Join(folder, relayLogFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer logf.Close()
	args := append([]string{"session-relay", "--id", id, "--dir", folder, "--sock", sock, "--", name}, argv[1:]...)
	cmd := exec.Command(relayExe(), args...)
	cmd.Dir, cmd.Env = dir, env
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	hs, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(hs).ReadString('\n')
		lines <- strings.TrimRight(line, "\r\n")
	}()
	var line string
	select {
	case line = <-lines:
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		line = <-lines
	}
	go func() { _ = cmd.Wait() }() // reaps the relay while this server runs
	if rest, ok := strings.CutPrefix(line, "ok "); ok {
		pid, _ := strconv.Atoi(rest)
		return &process{relay: &relayClient{sock: sock}, childPID: pid, done: make(chan struct{})}, nil
	}
	if rest, ok := strings.CutPrefix(line, "err "); ok {
		return nil, errors.New(rest)
	}
	return nil, errors.New("the session relay did not start (see " + filepath.Join(folder, relayLogFile) + ")")
}

// RelayMain is `ark-server session-relay`:
//
//	session-relay --id ID --dir FOLDER --sock PATH -- PROGRAM ARGS...
//	session-relay stop-all SESSIONS-ROOT
func RelayMain(args []string) int {
	if len(args) > 0 && args[0] == "stop-all" {
		return relayStopAll(args[1:])
	}
	fs := flag.NewFlagSet("session-relay", flag.ContinueOnError)
	id := fs.String("id", "", "session id")
	dir := fs.String("dir", "", "session folder")
	sock := fs.String("sock", "", "unix socket path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	argv := fs.Args()
	if *id == "" || *dir == "" || *sock == "" || len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "usage: ark-server session-relay --id ID --dir FOLDER --sock PATH -- PROGRAM [ARGS...]")
		return 2
	}
	// A write to a closed handshake pipe must not kill the relay.
	signal.Notify(make(chan os.Signal, 1), syscall.SIGPIPE, syscall.SIGHUP)
	r := &relay{id: *id, folder: *dir, exited: make(chan struct{})}
	return r.run(*sock, argv)
}

type relay struct {
	id, folder  string
	mu          sync.Mutex
	stdin       *os.File
	stdinClosed bool
	cmd         *exec.Cmd
	code        *int
	exited      chan struct{}
	replies     sync.WaitGroup
}

func (r *relay) run(sock string, argv []string) int {
	handshake := func(line string) {
		fmt.Fprintln(os.Stdout, line)
		_ = os.Stdout.Close()
	}
	open := func(stream string) (*os.File, error) {
		return os.OpenFile(rawPath(r.folder, stream), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	}
	out, err := open("stdout")
	if err != nil {
		handshake("err " + err.Error())
		return 1
	}
	errf, err := open("stderr")
	if err != nil {
		handshake("err " + err.Error())
		return 1
	}
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		handshake("err session relay: " + err.Error())
		return 1
	}
	_ = os.Chmod(sock, 0o600)
	defer func() {
		ln.Close()
		_ = os.Remove(sock)
	}()
	inR, inW, err := os.Pipe()
	if err != nil {
		handshake("err " + err.Error())
		return 1
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, out, errf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		handshake("err " + osErrorText(err, argv[0]))
		return 1
	}
	inR.Close()
	out.Close()
	errf.Close()
	r.cmd, r.stdin = cmd, inW
	info := relayInfo{V: 1, Session: r.id, Sock: sock, RelayPID: os.Getpid(), ChildPID: cmd.Process.Pid, StartedAt: NowISO()}
	raw, _ := json.Marshal(info)
	if err := writeFileAtomic(filepath.Join(r.folder, relayInfoFile), raw); err != nil {
		fmt.Fprintln(os.Stderr, "session relay: relay.json:", err)
	}
	handshake("ok " + strconv.Itoa(cmd.Process.Pid))
	go r.serve(ln)
	_ = cmd.Wait()
	code := procx.ExitCode(cmd.ProcessState)
	r.mu.Lock()
	r.code = &code
	r.closeStdinLocked()
	r.mu.Unlock()
	raw, _ = json.Marshal(relayExit{Code: &code, EndedAt: NowISO()})
	if err := writeFileAtomic(filepath.Join(r.folder, relayExitFile), raw); err != nil {
		fmt.Fprintln(os.Stderr, "session relay: exit.json:", err)
	}
	close(r.exited)
	// Let the waiting servers read the answer.
	done := make(chan struct{})
	go func() { r.replies.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
	return 0
}

func (r *relay) closeStdinLocked() {
	if !r.stdinClosed {
		r.stdinClosed = true
		_ = r.stdin.Close()
	}
}

func (r *relay) serve(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go r.handle(conn)
	}
}

func (r *relay) isExited() bool {
	select {
	case <-r.exited:
		return true
	default:
		return false
	}
}

func (r *relay) handle(conn net.Conn) {
	defer conn.Close()
	raw, err := bufio.NewReaderSize(conn, 64*1024).ReadBytes('\n')
	if err != nil && !(errors.Is(err, io.EOF) && len(raw) > 0) {
		return
	}
	var req relayRequest
	if json.Unmarshal(raw, &req) != nil {
		return
	}
	reply := relayReply{OK: true, Session: r.id}
	switch req.Op {
	case "ping":
	case "write":
		r.mu.Lock()
		closed := r.stdinClosed
		r.mu.Unlock()
		if closed {
			reply = relayReply{Error: "session stdin is closed"}
			break
		}
		// Outside the lock: a full pipe must not block close or kill.
		if _, err := r.stdin.Write([]byte(req.Data)); err != nil {
			reply = relayReply{Error: "session stdin is closed"}
		}
	case "close":
		r.mu.Lock()
		r.closeStdinLocked()
		r.mu.Unlock()
	case "kill":
		killGroup(r.cmd.Process.Pid)
	case "wait":
		r.replies.Add(1)
		defer r.replies.Done()
		<-r.exited
	default:
		reply = relayReply{Error: "unknown op"}
	}
	r.mu.Lock()
	reply.ChildPID = r.cmd.Process.Pid
	reply.StdinClosed = r.stdinClosed
	reply.Code = r.code
	r.mu.Unlock()
	reply.Exited = r.isExited()
	line, _ := json.Marshal(reply)
	_, _ = conn.Write(append(line, '\n'))
}

// relayStopAll ends every relayed agent under a sessions root: stdin is
// closed first (an agent waiting for input ends cleanly), processes still
// running after 15 s are killed.  A running server records the endings.
func relayStopAll(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: ark-server session-relay stop-all SESSIONS-ROOT")
		return 2
	}
	entries, err := os.ReadDir(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var live []*relayClient
	for _, e := range entries {
		info, ok := readRelayInfo(filepath.Join(args[0], e.Name()))
		if !ok {
			continue
		}
		c := &relayClient{sock: info.Sock}
		if r, err := c.call(relayRequest{Op: "close"}, 3*time.Second); err == nil && r.OK && !r.Exited {
			live = append(live, c)
		}
	}
	deadline := time.Now().Add(15 * time.Second)
	killed := 0
	for _, c := range live {
		for time.Now().Before(deadline) {
			if r, err := c.call(relayRequest{Op: "ping"}, 3*time.Second); err != nil || r.Exited {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if r, err := c.call(relayRequest{Op: "ping"}, 3*time.Second); err == nil && !r.Exited {
			c.kill(r.ChildPID)
			killed++
		}
	}
	fmt.Printf("stopped %d session(s), %d killed after the grace period\n", len(live), killed)
	return 0
}
