//go:build !windows

package procx

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// isolate puts the child in its own process group.
func isolate(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// killTree signals the whole process group.
func killTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}

func setCmdLine(*exec.Cmd, string) {}

func hideWindow(*exec.Cmd) {}

func setArgvCmdline(*exec.Cmd, []string) {}

func errnoText(errno syscall.Errno) string {
	return "[Errno " + itoa(int(errno)) + "] " + errno.Error()
}

// WinErrorText is only meaningful on Windows.
func WinErrorText(int) string { return "" }

func exitCode(state *os.ProcessState) int {
	if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return -int(ws.Signal())
	}
	return state.ExitCode()
}

func killProcessTree(pid int) {
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
