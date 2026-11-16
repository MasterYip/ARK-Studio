//go:build !windows

package hostopen

import "errors"

// shellOpen exists on Windows only (os.startfile).
func shellOpen(string) error { return errors.New("os.startfile is not available") }
