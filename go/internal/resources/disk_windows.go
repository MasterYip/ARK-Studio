//go:build windows

package resources

import "golang.org/x/sys/windows"

// diskUsage is shutil.disk_usage on Windows (nt._getdiskusage: total and
// TotalNumberOfFreeBytes; used = total - free).
func diskUsage(path string) (total, used, free int64, ok bool) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, 0, false
	}
	var avail, t, f uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &t, &f); err != nil {
		return 0, 0, 0, false
	}
	return int64(t), int64(t - f), int64(f), true
}

// loadAvg: os.getloadavg does not exist on Windows (None).
func loadAvg() (float64, bool) { return 0, false }
