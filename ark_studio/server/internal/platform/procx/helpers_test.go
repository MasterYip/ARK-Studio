package procx

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func TestOSErrorText(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		prefix string
	}{
		{"plain", errors.New("boom"), "boom"},
		{"errno", syscall.Errno(2), map[bool]string{true: "[WinError 2] ", false: "[Errno 2] "}[runtime.GOOS == "windows"]},
		{"wrapped", fmt.Errorf("dial: %w", syscall.Errno(2)), map[bool]string{true: "[WinError 2] ", false: "[Errno 2] "}[runtime.GOOS == "windows"]},
	}
	for _, c := range cases {
		got := OSErrorText(c.err)
		if !strings.HasPrefix(got, c.prefix) {
			t.Errorf("%s: OSErrorText = %q, want prefix %q", c.name, got, c.prefix)
		}
		if c.name != "plain" && (strings.HasSuffix(got, ".") || strings.HasSuffix(got, " ")) {
			t.Errorf("%s: trailing dot/space not stripped: %q", c.name, got)
		}
	}
}

func TestWinErrorText(t *testing.T) {
	if runtime.GOOS != "windows" {
		if WinErrorText(2) != "" {
			t.Fatal("WinErrorText off Windows must be empty")
		}
		return
	}
	if got := WinErrorText(2); got == "" || strings.HasSuffix(got, ".") {
		t.Errorf("WinErrorText(2) = %q", got)
	}
	if got := WinErrorText(0x7ffffff0); !strings.HasPrefix(got, "Windows Error 0x") {
		t.Errorf("unknown code: %q", got)
	}
}

func TestExitCode(t *testing.T) {
	if ExitCode(nil) != -1 {
		t.Fatal("nil state must be -1")
	}
	name, args := "sh", []string{"-c", "exit 3"}
	if runtime.GOOS == "windows" {
		name, args = "cmd", []string{"/c", "exit 3"}
	}
	cmd := exec.Command(name, args...)
	HideWindow(cmd)
	_ = cmd.Run()
	if got := ExitCode(cmd.ProcessState); got != 3 {
		t.Fatalf("ExitCode = %d, want 3", got)
	}
}
