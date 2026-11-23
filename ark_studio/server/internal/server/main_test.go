package server

import (
	"os"
	"testing"

	"arkstudio/go/internal/platform/testpath"
	"arkstudio/go/internal/sessions"
)

// TestMain turns the background size measuring off for the route tests
// (it writes into their temporary projects); locsizes_test.go turns it on
// for its own service.  PATH keeps only the tools the tests run (see testpath).
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "session-relay" { // the relay of a relayed session
		os.Exit(sessions.RelayMain(os.Args[2:]))
	}
	if os.Getenv("ARK_FAKE_CLAUDE_STREAM") == "1" { // a session's agent (fakeclaude_test.go)
		fakeClaudeStream()
		return
	}
	os.Setenv("ARK_LOCATION_SIZES", "off")
	testpath.Use()
	os.Exit(m.Run())
}
