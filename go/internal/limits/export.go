package limits

import "os/exec"

// Isolate starts cmd in its own process group / without a console window,
// like the pool's children (long-running agent sessions use it directly).
func Isolate(cmd *exec.Cmd) { isolate(cmd) }

// KillTree ends cmd and every descendant (taskkill /T on Windows, the
// process group on POSIX).
func KillTree(cmd *exec.Cmd) error { return killTree(cmd) }

// SetCmdLine passes the exact Windows command line (no-op elsewhere).
func SetCmdLine(cmd *exec.Cmd, line string) { setCmdLine(cmd, line) }
