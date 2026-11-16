package sessionctx

import (
	"bytes"
	"os"
	"testing"
)

func TestWatcherCopy(t *testing.T) {
	want, err := os.ReadFile("../../../python/arkstudio/session_watch.py")
	if err != nil {
		t.Skip("arkstudio/session_watch.py not beside the Go module")
	}
	if !bytes.Equal(want, WatcherScript) {
		t.Fatal("go/internal/sessionctx/session_watch.py differs from arkstudio/session_watch.py; copy it again")
	}
}
