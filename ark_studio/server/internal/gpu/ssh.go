// Package gpu ports arkstudio/gpu_probe.py and vendor/gpu_server_probe
// (config, ssh, probe, dispatch, queue).
//
// ssh.go is vendor/gpu_server_probe/ssh.py: the OpenSSH client runner.
package gpu

import (
	"arkstudio/go/internal/platform/procx"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pyos"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/compat/pyurl"
)

// ProbeCommand is ssh.PROBE_COMMAND, the read-only nvidia-smi query.
var ProbeCommand = []string{
	"nvidia-smi",
	"--query-gpu=index,name,memory.total,memory.used,utilization.gpu," +
		"utilization.memory,temperature.gpu,power.draw",
	"--format=csv,noheader,nounits",
}

const maxCaptureBytes = 64 * 1024

// AllocationCommand is ssh.ALLOCATION_COMMAND.
var AllocationCommand = `echo __ARK_BUS__
nvidia-smi --query-gpu=index,pci.bus_id --format=csv,noheader,nounits || exit $?
echo __ARK_PROC__
apps=$(nvidia-smi --query-compute-apps=pid,gpu_bus_id,used_gpu_memory --format=csv,noheader,nounits 2>/dev/null) || apps=''
printf '%s\n' "$apps"
echo __ARK_ENV__
for pid in $(printf '%s\n' "$apps" | cut -d, -f1 | tr -d ' ' | sort -u); do
  if [ -r /proc/$pid/environ ]; then
    jid=$(tr '\0' '\n' < /proc/$pid/environ 2>/dev/null | sed -n 's/^ARK_GPU_JOB_ID=//p' | head -1)
    [ -n "$jid" ] && echo "$pid $jid"
  fi
done
echo __ARK_GPU__
` + strings.Join(ProbeCommand, " ")

// SystemCommand is ssh.SYSTEM_COMMAND plus the Go-only, read-only df
// section (StorageCommand, storage.go).
const SystemCommand = `echo __ARK_LOAD__; cut -d' ' -f1-3 /proc/loadavg
echo __ARK_NPROC__; nproc
echo __ARK_MEM__; free -m | awk 'NR==2 {print $2, $3, $4, $7}'
echo __ARK_CPU__; top -bn2 -d 0.5 2>/dev/null | grep 'Cpu(s)' | tail -1
echo __ARK_BUS__; nvidia-smi --query-gpu=index,pci.bus_id --format=csv,noheader,nounits 2>/dev/null || true
echo __ARK_PROC__; nvidia-smi --query-compute-apps=pid,gpu_bus_id,used_gpu_memory --format=csv,noheader,nounits 2>/dev/null || true
echo __ARK_PMON__; timeout 5 nvidia-smi pmon -c 1 -s u 2>/dev/null || true
echo __ARK_ENV__
for pid in $(nvidia-smi --query-compute-apps=pid --format=csv,noheader 2>/dev/null | tr -d ' '); do
  if [ -r /proc/$pid/environ ]; then
    jid=$(tr '\0' '\n' < /proc/$pid/environ 2>/dev/null | sed -n 's/^ARK_GPU_JOB_ID=//p' | head -1)
    [ -n "$jid" ] && echo "$pid $jid"
  fi
done
` + StorageCommand + `
true`

// MissingSSHHint is ssh.MISSING_SSH_HINT.
const MissingSSHHint = "OpenSSH client not found. Install the OpenSSH Client optional feature " +
	"(Windows) or the openssh-client package (Linux/macOS), make sure `ssh` is " +
	"on PATH, or set ARK_GPU_SSH to the ssh executable path."

// CommandResult is ssh.CommandResult.
type CommandResult struct {
	ReturnCode int
	Stdout     string
	Stderr     string
	ErrorKind  string // "", missing_ssh, timeout, unreachable, auth, command_failed, ssh_error
	Message    string
	DurationMS int64
}

// OK is CommandResult.ok.
func (r CommandResult) OK() bool { return r.ReturnCode == 0 && r.ErrorKind == "" }

// truncate is ssh._truncate (the text is already valid UTF-8).
func truncate(text string) string {
	if len(text) <= maxCaptureBytes {
		return text
	}
	return pyurl.DecodeReplace([]byte(text[:maxCaptureBytes])) + "\n…[truncated]"
}

// SSHBinary is ssh.ssh_binary(environ): the ARK_GPU_SSH override, else ssh on PATH.
func SSHBinary() string {
	override := pytext.Strip(os.Getenv("ARK_GPU_SSH"))
	if override != "" {
		if found := pyos.Which(override, "", false); found != "" {
			return found
		}
		candidate := pathStr(pyos.ExpandUser(override))
		if pyos.IsFile(candidate) {
			return candidate
		}
		return ""
	}
	return pyos.Which("ssh", "", false)
}

// pathStr is str(Path(p)): separators normalized, empty and "." parts dropped.
func pathStr(p string) string {
	sep := string(os.PathSeparator)
	if sep == `\` {
		p = strings.ReplaceAll(p, "/", `\`)
	}
	lead := ""
	if sep == `\` && len(p) >= 2 && p[1] == ':' {
		lead, p = p[:2], p[2:]
	}
	if strings.HasPrefix(p, sep) {
		lead += sep
	}
	var parts []string
	for _, part := range strings.Split(p, sep) {
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	out := lead + strings.Join(parts, sep)
	if out == "" {
		return "."
	}
	return out
}

// DependencyStatus is ssh.dependency_status.
func DependencyStatus() *pyjson.Object {
	binary := SSHBinary()
	hint := ""
	if binary == "" {
		hint = MissingSSHHint
	}
	return pyjson.NewObject("available", binary != "", "binary", binary, "hint", hint)
}

func sshTarget(server *pyjson.Object) (port int64, target string) {
	port = 22
	if v, ok := server.Value("port").(int64); ok && v != 0 {
		port = v
	}
	host := pyjson.Str(orEmpty(server.Value("host")))
	user := pyjson.Str(orEmpty(server.Value("user")))
	target = host
	if user != "" {
		target = user + "@" + host
	}
	return port, target
}

func orEmpty(v any) any {
	if !pyjson.Truthy(v) {
		return ""
	}
	return v
}

func baseArgv(server *pyjson.Object, binary string, timeout int) []string {
	port, target := sshTarget(server)
	argv := []string{binary,
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=" + strconv.Itoa(max(1, timeout)),
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ServerAliveInterval=5",
		"-o", "ServerAliveCountMax=2"}
	argv = append(argv, muxOptions()...)
	return append(argv, "-p", strconv.FormatInt(port, 10), "--", target)
}

// muxDir holds the SSH control sockets (muxOptions); "" turns sharing off.
var muxDir = sshMuxDir()

// sshMuxDir is a private folder for the control sockets, or "" on Windows
// (its OpenSSH has no ControlMaster) and when ARK_GPU_SSH_MUX=0.
func sshMuxDir() string {
	if runtime.GOOS == "windows" || os.Getenv("ARK_GPU_SSH_MUX") == "0" {
		return ""
	}
	dir := filepath.Join(os.TempDir(), "ark-gpu-ssh-"+strconv.Itoa(os.Getuid()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() || fi.Mode().Perm()&0o077 != 0 {
		return "" // not ours or shared with others: no sockets there
	}
	return dir
}

// muxOptions share one SSH connection per server between the probe, the
// system query and the next refreshes (ControlMaster): a refresh then skips
// the SSH handshake, which was most of its time.  The connection closes
// after a minute without use.  %C is a hash of host, port and user, which
// keeps the socket path short.
func muxOptions() []string {
	if muxDir == "" {
		return nil
	}
	return []string{"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + filepath.Join(muxDir, "%C"),
		"-o", "ControlPersist=60"}
}

// BuildSSHArgv is ssh.build_ssh_argv.
func BuildSSHArgv(server *pyjson.Object, binary string, timeout int) []string {
	return append(baseArgv(server, binary, timeout), ProbeCommand...)
}

// BuildCommandArgv is ssh.build_command_argv.
func BuildCommandArgv(server *pyjson.Object, binary string, timeout int, command string) []string {
	return append(baseArgv(server, binary, timeout), "bash", "-lc", ShlexQuote(command))
}

// ProbeCommandText is ssh.probe_command_text().
func ProbeCommandText() string { return strings.Join(ProbeCommand, " ") }

var unreachableMarkers = []string{
	"could not resolve", "name or service not known", "nodename nor servname",
	"connection refused", "connection timed out", "no route to host",
	"network is unreachable", "connection closed", "connection reset",
	"host key verification failed", "remote host identification has changed",
}

// classify is ssh._classify.
func classify(stderr string, code int) (string, string) {
	lowered := pytext.Lower(stderr)
	message := pytext.Prefix(pytext.Strip(stderr), 300)
	or := func(def string) string {
		if message != "" {
			return message
		}
		return def
	}
	if strings.Contains(lowered, "permission denied") || strings.Contains(lowered, "no supported authentication") {
		return "auth", or("SSH authentication failed; check your SSH agent or ~/.ssh/config")
	}
	for _, marker := range unreachableMarkers {
		if strings.Contains(lowered, marker) {
			return "unreachable", or("SSH connection failed")
		}
	}
	if code == 255 {
		return "ssh_error", or("SSH exited with status 255")
	}
	return "command_failed", or("remote command exited with status " + strconv.Itoa(code))
}

// textOut is subprocess text-mode output: UTF-8 with errors="replace" and
// universal newlines.
func textOut(b []byte) string {
	s := pyurl.DecodeReplace(b)
	if !strings.Contains(s, "\r") {
		return s
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

func timeoutOf(server *pyjson.Object, timeout int) int {
	if timeout != 0 {
		return timeout
	}
	if v, ok := server.Value("timeout_seconds").(int64); ok && v != 0 {
		return int(v)
	}
	return 8
}

// run is the shared body of ssh.run_remote / run_remote_command.
func run(server *pyjson.Object, argvOf func(binary string, limit int) []string, timeout int, binary string) CommandResult {
	limit := timeoutOf(server, timeout)
	if binary == "" {
		binary = SSHBinary()
	}
	started := time.Now()
	if binary == "" {
		return CommandResult{ReturnCode: -1, ErrorKind: "missing_ssh", Message: MissingSSHHint}
	}
	argv := argvOf(binary, limit)
	elapsed := func() int64 { return time.Since(started).Milliseconds() }
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(limit+5)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	procx.SetArgvCmdline(cmd, argv)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		name := pyjson.Str(server.Value("name"))
		if !pyjson.Truthy(server.Value("name")) {
			name = "server"
		}
		return CommandResult{ReturnCode: -1, ErrorKind: "timeout",
			Message:    "SSH to " + name + " timed out after " + strconv.Itoa(limit+5) + "s",
			DurationMS: elapsed()}
	}
	code := 0
	// A shared SSH connection (muxOptions) is a background ssh that may keep
	// the output pipes open after this command finished: WaitDelay closed
	// them, and the command's own exit status still holds.
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil {
		err = nil
		if c := cmd.ProcessState.ExitCode(); c != 0 {
			code = c
		}
	}
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			if isNotFound(err) {
				return CommandResult{ReturnCode: -1, ErrorKind: "missing_ssh", Message: MissingSSHHint}
			}
			return CommandResult{ReturnCode: -1, ErrorKind: "ssh_error",
				Message: "could not start ssh: " + procx.OSErrorText(err), DurationMS: elapsed()}
		}
		code = exitErr.ExitCode()
	}
	duration := elapsed()
	out, errText := truncate(textOut(stdout.Bytes())), truncate(textOut(stderr.Bytes()))
	if code == 0 {
		return CommandResult{Stdout: out, Stderr: errText, DurationMS: duration}
	}
	kind, message := classify(errText, code)
	return CommandResult{ReturnCode: code, Stdout: out, Stderr: errText, ErrorKind: kind, Message: message,
		DurationMS: duration}
}

func isNotFound(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == syscall.ENOENT || errno == 3 // ERROR_FILE_NOT_FOUND (2 == ENOENT), ERROR_PATH_NOT_FOUND
	}
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, exec.ErrNotFound)
}

// Runner runs one remote command for a server (probe, system or queue).
type Runner func(server *pyjson.Object, timeout int) CommandResult

// RunRemote is ssh.run_remote (the read-only GPU probe).
func RunRemote(server *pyjson.Object, timeout int) CommandResult {
	return run(server, func(binary string, limit int) []string { return BuildSSHArgv(server, binary, limit) },
		timeout, "")
}

// RunRemoteCommand is ssh.run_remote_command.
func RunRemoteCommand(server *pyjson.Object, command string, timeout int) CommandResult {
	return run(server, func(binary string, limit int) []string {
		return BuildCommandArgv(server, binary, limit, command)
	}, timeout, "")
}

// RunRemoteSystem is ssh.run_remote_system.
func RunRemoteSystem(server *pyjson.Object, timeout int) CommandResult {
	return RunRemoteCommand(server, SystemCommand, timeout)
}

// RunRemoteAllocation is ssh.run_remote_allocation.
func RunRemoteAllocation(server *pyjson.Object, timeout int) CommandResult {
	return RunRemoteCommand(server, AllocationCommand, timeout)
}
