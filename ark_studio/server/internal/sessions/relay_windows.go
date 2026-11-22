//go:build windows

package sessions

import (
	"errors"
	"fmt"
	"os"
)

// Windows (the desktop app) keeps processes with pipes owned by the
// server: they end when the server exits, as before.
func relaySupported() bool { return false }

func killGroup(int) {}

func startRelay(string, string, []string, string, []string) (*process, error) {
	return nil, errors.New("session relays are not supported on Windows")
}

// RelayMain is `ark-server session-relay` (not supported on Windows).
func RelayMain([]string) int {
	fmt.Fprintln(os.Stderr, "ark-server session-relay is not supported on Windows")
	return 2
}
