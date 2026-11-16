//go:build !windows

package pyos

import "syscall"

func executable(name string) bool { return syscall.Access(name, 1) == nil }
