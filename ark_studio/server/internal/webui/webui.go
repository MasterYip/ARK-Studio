// Package webui serves the unchanged web/ frontend, embedded or from disk.
//
// Release builds embed web/ through go:embed: run `go generate ./...` first
// (cmd/webgen copies ../web into internal/webui/dist and records the asset
// version), then `go build ./cmd/ark-server`.  Without that step only
// dist/.keep is embedded and the server serves web/ from disk (found next to
// the binary, in a parent of the working directory, or via --web-root /
// ARK_WEB_ROOT), which is also what development wants: edits show up without
// a rebuild, like the Python server.
package webui

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

//go:generate go run ../../cmd/webgen

//go:embed all:dist
var embedded embed.FS

// The demo project's assets (ark_studio/demo), copied by cmd/webgen so that
// packaged builds without a checkout can still seed demo images.
//
//go:embed all:demo
var embeddedDemo embed.FS

// EmbeddedDemo returns the embedded demo assets, or nil when the build has
// none (go generate was not run).
func EmbeddedDemo() fs.FS {
	sub, err := fs.Sub(embeddedDemo, "demo")
	if err != nil {
		return nil
	}
	if _, err := fs.Stat(sub, "workbench-overview.png"); err != nil {
		return nil
	}
	return sub
}

// VersionFile inside dist holds the asset version cmd/webgen computed.
const VersionFile = ".ark-build"

// Assets is one web root.
type Assets struct {
	// Dir is the disk root ("" when embedded).
	Dir string
	fs  fs.FS

	mu        sync.Mutex
	version   string
	checkedAt time.Time
	fixed     string
}

// Embedded returns the embedded assets when the build contains them.
func Embedded() (*Assets, bool) {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, false
	}
	version, _ := fs.ReadFile(sub, VersionFile)
	return &Assets{fs: sub, fixed: strings.TrimSpace(string(version))}, true
}

// FromDir serves a web/ folder on disk.
func FromDir(dir string) (*Assets, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	if _, err := os.Stat(filepath.Join(abs, "index.html")); err != nil {
		return nil, fmt.Errorf("web root %s has no index.html", abs)
	}
	return &Assets{Dir: abs, fs: os.DirFS(abs)}, nil
}

// Checkout layouts: the web root is <checkout>/ark_studio/web (earlier
// checkouts: <checkout>/web); a release puts web/ beside the binary.
const productDir = "ark_studio"

// Beside is the folder name next to a web root on disk: <web>/../name, or
// in the ark_studio/ checkout layout, where only the product folders sit
// beside web/, <web>/../../name (the checkout root: dist/, docs/).  The
// first that exists wins; without either the plain sibling path.
func Beside(webDir, name string) string {
	near := filepath.Join(filepath.Dir(webDir), name)
	if fi, err := os.Stat(near); err == nil && fi.IsDir() {
		return near
	}
	if parent := filepath.Dir(webDir); filepath.Base(parent) == productDir {
		far := filepath.Join(filepath.Dir(parent), name)
		if fi, err := os.Stat(far); err == nil && fi.IsDir() {
			return far
		}
	}
	return near
}

// Locate finds the web root: explicit dir, then embedded, then a web/ (or
// ark_studio/web/) folder beside the executable or in the working directory
// and its parents.
func Locate(explicit string) (*Assets, error) {
	if explicit != "" {
		return FromDir(explicit)
	}
	if a, ok := Embedded(); ok {
		return a, nil
	}
	var starts []string
	if exe, err := os.Executable(); err == nil {
		starts = append(starts, filepath.Dir(exe))
	}
	if wd, err := os.Getwd(); err == nil {
		starts = append(starts, wd)
	}
	for _, start := range starts {
		for dir := start; ; dir = filepath.Dir(dir) {
			if a, err := FromDir(filepath.Join(dir, "web")); err == nil {
				return a, nil
			}
			if a, err := FromDir(filepath.Join(dir, productDir, "web")); err == nil {
				return a, nil
			}
			if filepath.Dir(dir) == dir {
				break
			}
		}
	}
	return nil, errors.New("web/ assets not found: build with `go generate ./...` or pass --web-root")
}

// FS is the asset file system.
func (a *Assets) FS() fs.FS { return a.fs }

var versionClean = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// Version is _asset_version(): ARK_ASSET_VERSION, the build's recorded
// version, or the newest file mtime in hex (re-scanned at most every 0.5 s).
func (a *Assets) Version() string {
	if override := strings.TrimSpace(os.Getenv("ARK_ASSET_VERSION")); override != "" {
		v := versionClean.ReplaceAllString(override, "")
		if len(v) > 40 {
			v = v[:40]
		}
		return v
	}
	if a.fixed != "" {
		return a.fixed
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.version != "" && time.Since(a.checkedAt) < 500*time.Millisecond {
		return a.version
	}
	a.version = fmt.Sprintf("%x", MaxMtimeNS(a.Dir))
	a.checkedAt = time.Now()
	return a.version
}

// MaxMtimeNS is max(st_mtime_ns) over every file below dir.
func MaxMtimeNS(dir string) int64 {
	var latest int64
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error { //nolint:errcheck
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().UnixNano() > latest {
			latest = info.ModTime().UnixNano()
		}
		return nil
	})
	return latest
}

// Stat describes one asset (for cache fingerprints).
func (a *Assets) Stat(rel string) (fs.FileInfo, error) { return fs.Stat(a.fs, rel) }

// Read returns the bytes of one asset.
func (a *Assets) Read(rel string) ([]byte, error) { return fs.ReadFile(a.fs, rel) }
