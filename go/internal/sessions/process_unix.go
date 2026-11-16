//go:build !windows

package sessions

import (
	"io/fs"
	"os"
	"syscall"

	"arkstudio/go/internal/pyos"
)

// exitCode is Popen.returncode: -signal for a signalled child.
func exitCode(state *os.ProcessState) int {
	if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return -int(ws.Signal())
	}
	return state.ExitCode()
}

func decodeANSI([]byte) (string, bool) { return "", false }

func winErrorText(int) string { return "" }

// lookProgram is the POSIX Popen search: a bare name on the child's PATH.
func lookProgram(name string, env []string) (string, error) {
	if pyos.HasDirPart(name) {
		return name, nil
	}
	if found := pyos.Which(name, envPath(env), true); found != "" {
		return found, nil
	}
	return "", &os.PathError{Op: "exec", Path: name, Err: fs.ErrNotExist}
}
