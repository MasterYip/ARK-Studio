//go:build linux

package resources

import (
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// diskUsage is shutil.disk_usage (statvfs: f_blocks, f_bfree, f_bavail, f_frsize).
func diskUsage(path string) (total, used, free int64, ok bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0, 0, false
	}
	size := int64(st.Frsize)
	if size == 0 {
		size = int64(st.Bsize)
	}
	return int64(st.Blocks) * size, int64(st.Blocks-st.Bfree) * size, int64(st.Bavail) * size, true
}

// loadAvg is os.getloadavg()[0] (glibc reads /proc/loadavg).
func loadAvg() (float64, bool) {
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	return v, err == nil
}
