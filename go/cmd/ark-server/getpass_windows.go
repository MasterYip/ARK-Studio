package main

import (
	"bufio"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// getpass is getpass.getpass on Windows: prompt on the console, read a line
// from the console without echo, then print a newline.
func getpass(prompt string) (string, error) {
	in, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
	if err != nil {
		return readStdinLine(prompt)
	}
	defer in.Close()
	out, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0)
	if err == nil {
		defer out.Close()
		out.WriteString(prompt) //nolint:errcheck
	}
	h := windows.Handle(in.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err == nil {
		windows.SetConsoleMode(h, mode&^windows.ENABLE_ECHO_INPUT|windows.ENABLE_LINE_INPUT) //nolint:errcheck
		defer windows.SetConsoleMode(h, mode)                                                //nolint:errcheck
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if out != nil {
		out.WriteString("\r\n") //nolint:errcheck
	}
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
