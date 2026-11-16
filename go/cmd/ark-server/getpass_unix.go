//go:build linux

package main

import (
	"bufio"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// getpass is getpass.unix_getpass: prompt and read on /dev/tty with echo
// off; without a terminal it warns and reads stdin.
func getpass(prompt string) (string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return readStdinLine(prompt)
	}
	defer tty.Close()
	fd := int(tty.Fd())
	old, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return readStdinLine(prompt)
	}
	noecho := *old
	noecho.Lflag &^= unix.ECHO
	unix.IoctlSetTermios(fd, unix.TCSETSF, &noecho)   //nolint:errcheck
	defer unix.IoctlSetTermios(fd, unix.TCSETSF, old) //nolint:errcheck
	tty.WriteString(prompt)                           //nolint:errcheck
	line, err := bufio.NewReader(tty).ReadString('\n')
	tty.WriteString("\n") //nolint:errcheck
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\n"), nil
}
