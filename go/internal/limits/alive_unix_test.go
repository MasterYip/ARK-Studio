//go:build !windows

package limits

import (
	"os"
	"strings"
	"syscall"
)

// processAlive reports whether pid names a running (not zombie) process.
func processAlive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	// An orphaned grandchild may linger as a zombie until init reaps it.
	stat, err := os.ReadFile("/proc/" + itoa(pid) + "/stat")
	if err == nil {
		if i := strings.LastIndexByte(string(stat), ')'); i > 0 && i+2 < len(stat) && stat[i+2] == 'Z' {
			return false
		}
	}
	return true
}
