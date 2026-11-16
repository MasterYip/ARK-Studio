// Command docsgen copies the docs/ Markdown that agents may read (the list
// is agentdocs.Included) into internal/agentdocs/dist for go:embed, with a
// manifest of each file's last-change time: the last commit touching it
// when git is available, else the file mtime.  Line endings become LF.
//
//	cd go && go generate ./...   (runs: go run ../../cmd/docsgen in internal/agentdocs)
package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"arkstudio/go/internal/agentdocs"
)

func main() {
	src := filepath.Join("..", "..", "..", "docs")
	if len(os.Args) > 1 {
		src = os.Args[1]
	}
	dst := "dist"
	if _, err := os.Stat(filepath.Join(src, "agents", "agent-interface.md")); err != nil {
		fmt.Fprintln(os.Stderr, "docsgen: no agents/agent-interface.md in", src)
		os.Exit(1)
	}
	entries, _ := os.ReadDir(dst)
	for _, e := range entries {
		if e.Name() != ".keep" {
			os.RemoveAll(filepath.Join(dst, e.Name()))
		}
	}
	manifest := agentdocs.Manifest{Updated: map[string]string{}}
	total := 0
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() || !agentdocs.Included(rel) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		data = []byte(strings.ReplaceAll(string(data), "\r\n", "\n"))
		target := filepath.Join(dst, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return err
		}
		manifest.Updated[rel] = changed(path)
		total += len(data)
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "docsgen:", err)
		os.Exit(1)
	}
	raw, _ := json.MarshalIndent(manifest, "", " ")
	if err := os.WriteFile(filepath.Join(dst, agentdocs.ManifestFile), append(raw, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "docsgen:", err)
		os.Exit(1)
	}
	fmt.Printf("docsgen: %d docs, %d bytes from %s\n", len(manifest.Updated), total, src)
}

// changed is the file's last commit time (UTC RFC 3339), else its mtime.
func changed(path string) string {
	abs, _ := filepath.Abs(path)
	out, err := exec.Command("git", "-C", filepath.Dir(abs), "log", "-1", "--format=%cI", "--", filepath.Base(abs)).Output()
	if err == nil {
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(out))); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	if info, err := os.Stat(path); err == nil {
		return info.ModTime().UTC().Format(time.RFC3339)
	}
	return ""
}
