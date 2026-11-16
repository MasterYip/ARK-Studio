// Package pathx holds path rules Python applies through pathlib.
package pathx

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var goos = runtime.GOOS

// SamePath is pathlib equality: case-insensitive on Windows.
func SamePath(a, b string) bool {
	if goos == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// ExpandUser is Path(p).expanduser() for "~", "~/..." and "~\...": the
// home folder joined with the rest (other forms are returned unchanged).
func ExpandUser(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// Within is `path == root or root in path.parents` (pathlib, no resolve).
func Within(path, root string) bool {
	for p := filepath.Clean(path); ; {
		if SamePath(p, root) {
			return true
		}
		parent := filepath.Dir(p)
		if parent == p {
			return false
		}
		p = parent
	}
}
