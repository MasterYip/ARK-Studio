package dirsize

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", n)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMeasureTreeWithGit(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.txt"), 10)
	write(t, filepath.Join(root, "src", "b.go"), 20)
	write(t, filepath.Join(root, ".git", "objects", "pack", "p"), 100)
	write(t, filepath.Join(root, ".git", "HEAD"), 5)
	write(t, filepath.Join(root, "sub", ".git", "x"), 7) // nested .git is working tree
	r := Measure(context.Background(), root, Options{})
	if r.Kind != KindDir || r.Capped != "" {
		t.Fatalf("kind %q capped %q", r.Kind, r.Capped)
	}
	if r.Bytes != 142 || r.Files != 5 {
		t.Fatalf("bytes %d files %d", r.Bytes, r.Files)
	}
	if !r.Git || r.GitBytes != 105 || r.GitFiles != 2 {
		t.Fatalf("git %v %d %d", r.Git, r.GitBytes, r.GitFiles)
	}
}

func TestMeasureFileMissingRelative(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(root, "x.sqlite3")
	write(t, db, 33)
	if r := Measure(context.Background(), db, Options{}); r.Kind != KindFile || r.Bytes != 33 || r.Files != 1 {
		t.Fatalf("file: %+v", r)
	}
	if r := Measure(context.Background(), filepath.Join(root, "nope"), Options{}); r.Kind != KindMissing {
		t.Fatalf("missing: %+v", r)
	}
	if r := Measure(context.Background(), "rel/path", Options{}); r.Kind != KindError {
		t.Fatalf("relative: %+v", r)
	}
}

func TestMeasureEntryCapIsLowerBound(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 40; i++ {
		write(t, filepath.Join(root, "d", strings.Repeat("f", 1+i%5)+string(rune('a'+i%26))+itoa(i)), 1)
	}
	r := Measure(context.Background(), root, Options{MaxEntries: 10})
	if r.Capped != CappedEntries || r.Files > 10 || r.Files == 0 {
		t.Fatalf("capped: %+v", r)
	}
}

func TestMeasureTimeCap(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 300; i++ {
		write(t, filepath.Join(root, "f"+itoa(i)), 0)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r := Measure(ctx, root, Options{}); r.Capped != CappedTime {
		t.Fatalf("expected time cap: %+v", r)
	}
}

func TestMeasureDoesNotFollowLinks(t *testing.T) {
	outside := t.TempDir()
	write(t, filepath.Join(outside, "big"), 1000)
	root := t.TempDir()
	write(t, filepath.Join(root, "small"), 3)
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if err := os.Symlink(filepath.Join(outside, "big"), filepath.Join(root, "filelink")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	r := Measure(context.Background(), root, Options{})
	if r.Bytes != 3 || r.Files != 1 || r.Symlinks != 2 {
		t.Fatalf("links followed: %+v", r)
	}
	// A link given as the root itself is resolved once.
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skip(err)
	}
	if r := Measure(context.Background(), alias, Options{}); r.Bytes != 3 {
		t.Fatalf("root link: %+v", r)
	}
}

func TestBackgroundMatchesMeasure(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a"), 4)
	if r := Background(context.Background(), root, Options{}); r.Bytes != 4 {
		t.Fatalf("%+v", r)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for ; i > 0; i /= 10 {
		s = string(rune('0'+i%10)) + s
	}
	return s
}
