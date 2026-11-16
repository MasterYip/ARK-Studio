//go:build !linux

package workspace

import (
	"errors"
	"os/exec"
)

// PTYAvailability reports no PTY backend: the Go server implements the POSIX
// PTY on Linux only (Python uses pywinpty on Windows when it is installed).
func PTYAvailability() PTYInfo {
	return PTYInfo{
		Reason: "the Go server has no PTY backend on this platform",
		Hint:   "The stdio terminal keeps working. Interactive PTY terminals need ark-server on Linux.",
	}
}

type ptyProcess interface {
	handle
	read(buf []byte) (int, error)
	command() *exec.Cmd
}

func startPTY(string, string, []string, int, int) (ptyProcess, error) {
	return nil, errors.New("no PTY backend")
}
