package worktrees

import (
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// samePath is pathlib equality on Windows: case-insensitive.
func samePath(a, b string) bool { return strings.EqualFold(filepath.Clean(a), filepath.Clean(b)) }

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
}
