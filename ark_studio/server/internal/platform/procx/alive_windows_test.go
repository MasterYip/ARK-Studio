package procx

import "syscall"

// processAlive reports whether pid names a running process.
func processAlive(pid int) bool {
	const synchronize, queryLimited = 0x00100000, 0x1000
	h, err := syscall.OpenProcess(synchronize|queryLimited, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h) //nolint:errcheck
	ev, _ := syscall.WaitForSingleObject(h, 0)
	return ev != syscall.WAIT_OBJECT_0
}
