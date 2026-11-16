//go:build !windows

package chat

import "os/exec"

func setCmdline(*exec.Cmd, []string) {}
