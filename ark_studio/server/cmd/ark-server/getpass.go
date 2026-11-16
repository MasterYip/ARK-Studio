package main

import (
	"bufio"
	"os"
	"strings"
)

var stdinReader = bufio.NewReader(os.Stdin)

// readStdinLine is getpass's fallback_getpass: warn, prompt on stderr and
// read one line from stdin (echoed).
func readStdinLine(prompt string) (string, error) {
	eprintln("Warning: Password input may be echoed.")
	eprintf("%s", prompt)
	line, err := stdinReader.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
