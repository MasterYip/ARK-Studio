//go:build !windows

package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// shellArgv is platform_support.shell_command(text) on POSIX.
func shellArgv(text string) []string {
	shell := os.Getenv("SHELL")
	resolved := ""
	if shell != "" {
		if filepath.IsAbs(shell) && isFile(shell) {
			resolved = shell
		} else if p, err := exec.LookPath(filepath.Base(shell)); err == nil {
			resolved = p
		}
	}
	if resolved == "" {
		if p, err := exec.LookPath("sh"); err == nil {
			resolved = p
		} else {
			resolved = "/bin/sh"
		}
	}
	return []string{resolved, "-lc", text}
}

// shellCommand runs text through the login shell in a new session
// (start_new_session=True), so the whole group can be stopped.
func shellCommand(text string) *exec.Cmd {
	argv := shellArgv(text)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd
}
