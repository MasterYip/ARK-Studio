//go:build !windows && !linux && !darwin

package diskfree

func available(string) (int64, bool) { return 0, false }
