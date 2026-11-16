//go:build windows

package sessions

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

func exitCode(state *os.ProcessState) int { return state.ExitCode() }

// decodeANSI is bytes.decode("mbcs", "replace") (the ANSI code page).
func decodeANSI(raw []byte) (string, bool) {
	if len(raw) == 0 {
		return "", true
	}
	n, err := windows.MultiByteToWideChar(0, 0, &raw[0], int32(len(raw)), nil, 0)
	if err != nil || n <= 0 {
		return "", false
	}
	buf := make([]uint16, n)
	n, err = windows.MultiByteToWideChar(0, 0, &raw[0], int32(len(raw)), &buf[0], n)
	if err != nil {
		return "", false
	}
	return string(utf16.Decode(buf[:n])), true
}

// winErrorText is the text CPython puts after "[WinError n]": FormatMessageW
// in the user's language with trailing whitespace and dots removed.
func winErrorText(code int) string {
	buf := make([]uint16, 512)
	n, err := windows.FormatMessage(windows.FORMAT_MESSAGE_FROM_SYSTEM|windows.FORMAT_MESSAGE_IGNORE_INSERTS,
		0, uint32(code), 0, buf, nil)
	if err != nil || n == 0 {
		return "Windows Error 0x" + strings.ToUpper(strings.TrimLeft(hex(code), "0"))
	}
	text := []rune(string(utf16.Decode(buf[:n])))
	for len(text) > 0 && (text[len(text)-1] <= ' ' || text[len(text)-1] == '.') {
		text = text[:len(text)-1]
	}
	return string(text)
}

func hex(n int) string {
	const digits = "0123456789abcdef"
	out := ""
	for n > 0 {
		out = string(digits[n%16]) + out
		n /= 16
	}
	return "0" + out
}

// lookProgram resolves a bare program name the way CreateProcess does for
// subprocess.Popen on Windows (lpApplicationName=NULL): ".exe" is appended
// when the name has no extension and PATHEXT is NOT applied, so a bare
// "claude" never runs claude.cmd.  Search order: the server's own folder,
// the current folder, System32, System, the Windows folder, then PATH.
func lookProgram(name string, _ []string) (string, error) {
	if strings.ContainsAny(name, `/\:`) {
		return name, nil
	}
	file := name
	if filepath.Ext(file) == "" {
		file += ".exe"
	}
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, wd)
	}
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	dirs = append(dirs, filepath.Join(root, "System32"), filepath.Join(root, "System"), root)
	dirs = append(dirs, filepath.SplitList(os.Getenv("PATH"))...)
	for _, d := range dirs {
		if d == "" {
			continue
		}
		p := filepath.Join(d, file)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
	}
	return "", &os.PathError{Op: "exec", Path: name, Err: fs.ErrNotExist}
}
