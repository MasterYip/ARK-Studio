//go:build !windows

package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

func hideWindow(*exec.Cmd) {}

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

// killTree is platform_support.kill_process_tree: SIGTERM to the process
// group, then SIGKILL after 4 s.  ARK's own group is never signalled.
func killTree(pid int) {
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		return
	}
	if pgid <= 1 || pgid == syscall.Getpgrp() {
		syscall.Kill(pid, syscall.SIGTERM) //nolint:errcheck // Python falls back to proc.terminate()
		return
	}
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil {
		if err == syscall.EPERM {
			syscall.Kill(pid, syscall.SIGTERM) //nolint:errcheck
			if !waitGone(func() error { return syscall.Kill(pid, 0) }) {
				syscall.Kill(pid, syscall.SIGKILL) //nolint:errcheck
			}
		}
		return
	}
	if !waitGone(func() error { return syscall.Kill(-pgid, 0) }) {
		syscall.Kill(-pgid, syscall.SIGKILL) //nolint:errcheck
	}
}

func waitGone(probe func() error) bool {
	deadline := time.Now().Add(4 * time.Second)
	for {
		if err := probe(); err == syscall.ESRCH {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// exitCode is Popen.returncode: -signal for a signalled child.
func exitCode(state *os.ProcessState) int64 {
	if state == nil {
		return -1
	}
	if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return -int64(ws.Signal())
	}
	return int64(state.ExitCode())
}
