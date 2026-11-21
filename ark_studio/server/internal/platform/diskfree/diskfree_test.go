package diskfree

import "testing"

func TestAvailable(t *testing.T) {
	n, ok := Available(t.TempDir())
	if !ok || n <= 0 {
		t.Fatalf("Available(temp) = %d, %v", n, ok)
	}
	if _, ok := Available(t.TempDir() + "/missing/deeper"); ok {
		t.Error("a missing folder reports space")
	}
}
