//go:build windows

package limits

import (
	"os/exec"
	"strconv"
	"syscall"
)

// isolate starts the child in its own process group without a console
// window (CREATE_NO_WINDOW, like node_history.py).
func isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000 | syscall.CREATE_NEW_PROCESS_GROUP}
}

// killTree ends the child and every descendant (taskkill /T).
func killTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	kill.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	if err := kill.Run(); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}

func setCmdLine(cmd *exec.Cmd, line string) {
	if line != "" {
		cmd.SysProcAttr.CmdLine = line
	}
}
