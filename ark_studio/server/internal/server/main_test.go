package server

import (
	"os"
	"testing"
)

// TestMain turns the background size measuring off for the route tests
// (it writes into their temporary projects); locsizes_test.go turns it on
// for its own service.
func TestMain(m *testing.M) {
	os.Setenv("ARK_LOCATION_SIZES", "off")
	os.Exit(m.Run())
}
