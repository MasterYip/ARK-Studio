//go:build !windows && !linux

package resources

// diskUsage and loadAvg are not implemented on this platform (None).
func diskUsage(string) (total, used, free int64, ok bool) { return 0, 0, 0, false }

func loadAvg() (float64, bool) { return 0, false }
