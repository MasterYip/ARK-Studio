//go:build windows

package procx

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"unicode/utf16"

	"golang.org/x/sys/windows"

	"arkstudio/go/internal/compat/pyos"
)

// isolate starts the child in its own process group without a console
// window (CREATE_NO_WINDOW, like node_history.py).
func isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000 | syscall.CREATE_NEW_PROCESS_GROUP}
}

// killTree ends the child and every descendant (taskkill /T).
func killTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	kill.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	if err := kill.Run(); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}

func setCmdLine(cmd *exec.Cmd, line string) {
	if line != "" {
		cmd.SysProcAttr.CmdLine = line
	}
}

const createNoWindow = 0x08000000

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
}

func setArgvCmdline(cmd *exec.Cmd, argv []string) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: pyos.List2Cmdline(argv)}
}

func errnoText(errno syscall.Errno) string {
	return "[WinError " + itoa(int(errno)) + "] " + WinErrorText(int(errno))
}

// WinErrorText is the text CPython puts after "[WinError n]": FormatMessageW
// (system message, LANG_NEUTRAL) with trailing whitespace, control
// characters and dots removed, or "Windows Error 0x<hex>" when there is no
// message.
func WinErrorText(code int) string {
	buf := make([]uint16, 512)
	n, err := windows.FormatMessage(windows.FORMAT_MESSAGE_FROM_SYSTEM|windows.FORMAT_MESSAGE_IGNORE_INSERTS,
		0, uint32(code), 0, buf, nil)
	if err != nil || n == 0 {
		return "Windows Error 0x" + strconv.FormatUint(uint64(uint32(code)), 16)
	}
	text := []rune(string(utf16.Decode(buf[:n])))
	for len(text) > 0 && (text[len(text)-1] <= ' ' || text[len(text)-1] == '.') {
		text = text[:len(text)-1]
	}
	return string(text)
}

func exitCode(state *os.ProcessState) int { return state.ExitCode() }

func killProcessTree(pid int) {
	taskkill, err := exec.LookPath("taskkill")
	if err != nil {
		taskkill = "taskkill"
	}
	cmd := exec.Command(taskkill, "/PID", strconv.Itoa(pid), "/T", "/F")
	hideWindow(cmd)
	cmd.Run() //nolint:errcheck
}
