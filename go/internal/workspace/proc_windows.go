//go:build windows

package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

const createNoWindow = 0x08000000

// hideWindow is creationflags=CREATE_NO_WINDOW.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
}

func cmdExe() string {
	if p, err := exec.LookPath("cmd.exe"); err == nil {
		return p
	}
	return filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
}

// shellCommand is platform_support.run_command_args: one quoted command
// line for `cmd.exe /d /s /c`, in a new process group without a console.
func shellCommand(text string) *exec.Cmd {
	cmd := exec.Command(cmdExe())
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine:       `cmd.exe /d /s /c "` + text + `"`,
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | createNoWindow,
	}
	return cmd
}

// killTree is platform_support.kill_process_tree: taskkill /T /F.
func killTree(pid int) {
	taskkill, err := exec.LookPath("taskkill")
	if err != nil {
		taskkill = "taskkill"
	}
	cmd := exec.Command(taskkill, "/PID", strconv.Itoa(pid), "/T", "/F")
	hideWindow(cmd)
	cmd.Run() //nolint:errcheck
}

func exitCode(state *os.ProcessState) int64 {
	if state == nil {
		return -1
	}
	return int64(state.ExitCode())
}
