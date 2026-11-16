//go:build !windows

package search

import "os/exec"

func hideWindow(*exec.Cmd) {}
