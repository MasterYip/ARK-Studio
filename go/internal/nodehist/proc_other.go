//go:build !windows

package nodehist

import "os/exec"

func hideWindow(*exec.Cmd) {}
