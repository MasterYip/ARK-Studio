package main

import (
	"os"
	"testing"

	"arkstudio/go/internal/platform/testpath"
)

// TestMain keeps PATH to the tools the tests run (see testpath).
func TestMain(m *testing.M) {
	testpath.Use()
	os.Exit(m.Run())
}
