// Command webgen copies the repository's web/ folder into
// internal/webui/dist for go:embed and records the asset version (the
// newest file mtime in hex, the same value the Python server computes).
//
//	cd go && go generate ./...   (runs: go run ../../cmd/webgen in internal/webui)
package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"arkstudio/go/internal/webui"
)

func main() {
	src := filepath.Join("..", "..", "..", "web")
	if len(os.Args) > 1 {
		src = os.Args[1]
	}
	dst := "dist"
	if _, err := os.Stat(filepath.Join(src, "index.html")); err != nil {
		fmt.Fprintln(os.Stderr, "webgen: no index.html in", src)
		os.Exit(1)
	}
	entries, _ := os.ReadDir(dst)
	for _, e := range entries {
		if e.Name() != ".keep" {
			os.RemoveAll(filepath.Join(dst, e.Name()))
		}
	}
	count := 0
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		count++
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "webgen:", err)
		os.Exit(1)
	}
	version := fmt.Sprintf("%x", webui.MaxMtimeNS(src))
	if err := os.WriteFile(filepath.Join(dst, webui.VersionFile), []byte(version+"\n"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "webgen:", err)
		os.Exit(1)
	}
	fmt.Printf("webgen: %d files from %s, asset version %s\n", count, src, version)
	demo := filepath.Join(filepath.Dir(src), "demo")
	n, err := mirror(demo, "demo")
	if err != nil {
		fmt.Fprintln(os.Stderr, "webgen:", err)
		os.Exit(1)
	}
	fmt.Printf("webgen: %d demo files from %s\n", n, demo)
}

// mirror replaces dst (keeping .keep) with a copy of src (the demo project's
// assets, embedded for packaged builds); a missing src leaves dst empty.
func mirror(src, dst string) (int, error) {
	entries, _ := os.ReadDir(dst)
	for _, e := range entries {
		if e.Name() != ".keep" {
			os.RemoveAll(filepath.Join(dst, e.Name()))
		}
	}
	if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
		return 0, nil
	}
	count := 0
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		count++
		return os.WriteFile(target, data, 0o644)
	})
	return count, err
}
