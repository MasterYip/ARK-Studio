package server

import (
	"fmt"
	"testing"
)

// A newer run with the same sid supersedes the older one; at most 500 ids
// are kept, the oldest inserted id first out (Python dict order).
func TestSearchRunsSupersedeAndBound(t *testing.T) {
	first := searchRunSet("panel:files")
	second := searchRunSet("panel:files")
	if searchRunCurrent("panel:files", first) || !searchRunCurrent("panel:files", second) {
		t.Fatal("newer run must supersede the older one")
	}
	searchRunDone("panel:files", first) // the stale run must not drop the newer one
	if !searchRunCurrent("panel:files", second) {
		t.Fatal("stale run removed the current one")
	}
	searchRunDone("panel:files", second)
	for i := 0; i < 501; i++ {
		searchRunSet(fmt.Sprintf("sid-%d", i))
	}
	searchRuns.Lock()
	n, has0 := len(searchRuns.tokens), searchRuns.tokens["sid-0"] != 0
	searchRuns.Unlock()
	if n != 500 || has0 {
		t.Fatalf("bounded map: %d ids, oldest kept=%v", n, has0)
	}
}
