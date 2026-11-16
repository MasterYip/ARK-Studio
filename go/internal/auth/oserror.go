package auth

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// pyOSError is str(OSError) for a failed open()/read_text() of path, as
// Python prints it: "[Errno 2] No such file or directory: 'C:\x\pw.txt'"
// (the path is str(Path(path)), shown with repr()).
func pyOSError(path string, err error) string {
	shown := filepath.Clean(path)
	errno, text := 0, ""
	fi, statErr := os.Stat(path)
	switch {
	case statErr == nil && fi.IsDir():
		if runtime.GOOS == "windows" {
			errno, text = 13, "Permission denied"
		} else {
			errno, text = 21, "Is a directory"
		}
	case errors.Is(err, fs.ErrNotExist):
		errno, text = 2, "No such file or directory"
	case errors.Is(err, fs.ErrPermission):
		errno, text = 13, "Permission denied"
	default:
		return err.Error()
	}
	return "[Errno " + itoa(errno) + "] " + text + ": " + reprPath(shown)
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

// reprPath is repr() of a str holding a path (printable text assumed).
func reprPath(s string) string {
	quote := "'"
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		quote = `"`
	}
	s = strings.ReplaceAll(s, `\`, `\\`)
	if quote == "'" {
		s = strings.ReplaceAll(s, "'", `\'`)
	}
	return quote + s + quote
}

// expandUserPath is Path(path).expanduser() for "~" and "~/...".
func expandUserPath(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}
