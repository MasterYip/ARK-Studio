//go:build windows

package nodehist

import (
	"os/exec"
	"syscall"
)

// hideWindow is creationflags=CREATE_NO_WINDOW.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
}
