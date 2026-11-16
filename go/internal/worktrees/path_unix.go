//go:build !windows

package worktrees

import (
	"os/exec"
	"path/filepath"
)

func samePath(a, b string) bool { return filepath.Clean(a) == filepath.Clean(b) }

func hideWindow(*exec.Cmd) {}
