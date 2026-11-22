package server

import (
	"os"
	"testing"

	"arkstudio/go/internal/platform/testpath"
)

// TestMain turns the background size measuring off for the route tests
// (it writes into their temporary projects); locsizes_test.go turns it on
// for its own service.  PATH keeps only the tools the tests run (see testpath).
func TestMain(m *testing.M) {
	os.Setenv("ARK_LOCATION_SIZES", "off")
	testpath.Use()
	os.Exit(m.Run())
}
