package gpu

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf16"

	"golang.org/x/sys/windows"

	"arkstudio/go/internal/pyos"
)

// setCmdline passes the command line subprocess.list2cmdline builds.
func setCmdline(cmd *exec.Cmd, argv []string) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: pyos.List2Cmdline(argv)}
}

// osErrorText is str(OSError) of a Windows error: "[WinError N] " plus
// FormatMessage (LANG_NEUTRAL), trailing whitespace and dots removed.
func osErrorText(err error) string {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return err.Error()
	}
	buf := make([]uint16, 512)
	n, ferr := windows.FormatMessage(windows.FORMAT_MESSAGE_FROM_SYSTEM|windows.FORMAT_MESSAGE_IGNORE_INSERTS,
		0, uint32(errno), 0, buf, nil)
	msg := ""
	if ferr == nil {
		msg = strings.TrimRight(string(utf16.Decode(buf[:n])), " \t\r\n.")
	}
	return "[WinError " + strconv.Itoa(int(errno)) + "] " + msg
}
