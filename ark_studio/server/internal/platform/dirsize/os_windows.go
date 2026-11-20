package dirsize

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/windows"
)

func device(os.FileInfo) (uint64, bool) { return 0, false }

var setThreadPriority = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetThreadPriority")

// lowerThreadPriority: background mode (low IO and memory priority) for this
// thread only; the goroutine stays locked, so the thread is discarded.
func lowerThreadPriority() {
	runtime.LockOSThread()
	const threadModeBackgroundBegin = 0x00010000
	_, _, _ = setThreadPriority.Call(uintptr(windows.CurrentThread()), threadModeBackgroundBegin)
}

// IsNetworkPath: a UNC path or a mapped network drive.
func IsNetworkPath(path string) bool {
	if strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, "//") {
		return true
	}
	vol := filepath.VolumeName(path)
	if vol == "" {
		return false
	}
	p, err := windows.UTF16PtrFromString(vol + `\`)
	if err != nil {
		return false
	}
	return windows.GetDriveType(p) == windows.DRIVE_REMOTE
}
