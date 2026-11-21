//go:build windows

package diskfree

import "golang.org/x/sys/windows"

func available(path string) (int64, bool) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, false
	}
	var avail, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &free); err != nil {
		return 0, false
	}
	return int64(avail), true
}
