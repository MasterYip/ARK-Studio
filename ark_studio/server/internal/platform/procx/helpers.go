package procx

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// HideWindow is creationflags=CREATE_NO_WINDOW (no-op off Windows).  It
// replaces cmd.SysProcAttr.
func HideWindow(cmd *exec.Cmd) { hideWindow(cmd) }

// SetArgvCmdline passes the command line subprocess.list2cmdline builds
// from argv (Windows; no-op elsewhere).  It replaces cmd.SysProcAttr.
func SetArgvCmdline(cmd *exec.Cmd, argv []string) { setArgvCmdline(cmd, argv) }

// KillProcessTree is platform_support.kill_process_tree(pid): taskkill /T
// /F on Windows; on POSIX SIGTERM to the process group, then SIGKILL after
// 4 s (ARK's own group is never signalled).
func KillProcessTree(pid int) { killProcessTree(pid) }

// ExitCode is Popen.returncode: -signal for a signalled child on POSIX, -1
// for a child that has not exited.
func ExitCode(state *os.ProcessState) int {
	if state == nil {
		return -1
	}
	return exitCode(state)
}

// OSErrorText is str(OSError) of a system error: "[WinError N] <message>"
// on Windows (FormatMessageW as CPython calls it), "[Errno N] <strerror>"
// elsewhere; other errors keep their own text.
func OSErrorText(err error) string {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return err.Error()
	}
	return errnoText(errno)
}

func itoa(n int) string { return strconv.Itoa(n) }
