//go:build windows

package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

const createNoWindow = 0x08000000

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
