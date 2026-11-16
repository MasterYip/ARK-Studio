package chat

import (
	"os/exec"
	"syscall"

	"arkstudio/go/internal/pyos"
)

// setCmdline passes the command line subprocess.list2cmdline builds.
func setCmdline(cmd *exec.Cmd, parts []string) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: pyos.List2Cmdline(parts)}
}
