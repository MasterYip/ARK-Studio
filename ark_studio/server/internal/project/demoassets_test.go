package project

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// Packaged builds have no demo folder on disk; the embedded copy must still
// seed the demo images and the tutorial assets.
func TestDemoAssetsFromEmbeddedFS(t *testing.T) {
	oldDir, oldFS := DemoAssetsDir, DemoAssetsFS
	t.Cleanup(func() { DemoAssetsDir, DemoAssetsFS = oldDir, oldFS })
	DemoAssetsDir = ""
	DemoAssetsFS = fstest.MapFS{
		"workbench-overview.png":    {Data: []byte("png-a")},
		"mind-graph.png":            {Data: []byte("png-b")},
		"tutorial/research/note.md": {Data: []byte("# note")},
		"tutorial/execute/run.log":  {Data: []byte("ok")},
	}
	dir := t.TempDir()
	copyDemoAssets(dir)
	for name, want := range map[string]string{"workbench-overview.png": "png-a", "mind-graph.png": "png-b"} {
		got, err := os.ReadFile(filepath.Join(dir, "assets", name))
		if err != nil || string(got) != want {
			t.Fatalf("%s: %q %v", name, got, err)
		}
	}
	if err := CopyTutorialAssets(dir); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{"research/note.md": "# note", "execute/run.log": "ok"} {
		got, err := os.ReadFile(filepath.Join(dir, "assets", "tutorial", filepath.FromSlash(rel)))
		if err != nil || string(got) != want {
			t.Fatalf("tutorial %s: %q %v", rel, got, err)
		}
	}
}

// A folder on disk still wins over the embedded copy (development checkouts).
func TestDemoAssetsDiskWins(t *testing.T) {
	oldDir, oldFS := DemoAssetsDir, DemoAssetsFS
	t.Cleanup(func() { DemoAssetsDir, DemoAssetsFS = oldDir, oldFS })
	disk := t.TempDir()
	os.WriteFile(filepath.Join(disk, "mind-graph.png"), []byte("disk"), 0o644) //nolint:errcheck
	DemoAssetsDir = disk
	DemoAssetsFS = fstest.MapFS{"mind-graph.png": {Data: []byte("embedded")}}
	dir := t.TempDir()
	copyDemoAssets(dir)
	if got, _ := os.ReadFile(filepath.Join(dir, "assets", "mind-graph.png")); string(got) != "disk" {
		t.Fatalf("got %q", got)
	}
}
