//go:build !windows

package gpu

import (
	"errors"
	"os/exec"
	"strconv"
	"syscall"
)

func setCmdline(*exec.Cmd, []string) {}

// osErrorText is str(OSError): "[Errno N] text".
func osErrorText(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return "[Errno " + strconv.Itoa(int(errno)) + "] " + errno.Error()
	}
	return err.Error()
}
