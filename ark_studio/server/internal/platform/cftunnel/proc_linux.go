//go:build linux

package cftunnel

import (
	"os/exec"
	"syscall"
)

// prepare asks the kernel to end cloudflared when ark-server dies.
func prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}

func adopt(*exec.Cmd) {}
