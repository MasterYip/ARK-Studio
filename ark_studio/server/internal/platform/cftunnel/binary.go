// Package cftunnel runs cloudflared so that a phone can reach a loopback ARK
// server through a Cloudflare Tunnel: a free "quick tunnel" on
// *.trycloudflare.com (no account) or the user's own named tunnel (token).
// Cloudflare carries the traffic; ARK runs no relay. The binary comes from
// PATH, a path the user set, or the official GitHub release, pinned to one
// version and checked against its SHA-256 before it is ever run.
package cftunnel

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Version is the cloudflared release ARK downloads when none is installed.
const Version = "2026.11.1"

// asset is one release file and its published SHA-256 (GitHub release
// asset digest of cloudflare/cloudflared 2026.11.1).
type asset struct {
	name, sha256 string
	tgz          bool // darwin: the binary is inside a .tgz
}

var assets = map[string]asset{
	"windows/amd64": {"cloudflared-windows-amd64.exe", "341deda3db5ec498a0482254d6c38c0b7cf01a30c37bb5dd95d2216d08955e35", false},
	"windows/386":   {"cloudflared-windows-386.exe", "060868e138e638237e33329315c3e374e0156a2fb18879e571dbc0d506060df3", false},
	"linux/amd64":   {"cloudflared-linux-amd64", "97bb3b02d23a5980fc9c15a1a270e692da1923f103f920d5b16d4931d5ab55e7", false},
	"linux/arm64":   {"cloudflared-linux-arm64", "b155cec3e48e489cfb8e5c602769fb8c771083018d8881d68b4d18a791567c5a", false},
	"linux/arm":     {"cloudflared-linux-arm", "0196274a8237660991a27211d7423d1c2fdcb6be56fcae473dd330479d4692fb", false},
	"linux/386":     {"cloudflared-linux-386", "757c510745a9884f39e1a99d19c1e18bd5e0012b3368455a958471c71d875530", false},
	"darwin/amd64":  {"cloudflared-darwin-amd64.tgz", "5e0403b188dfee71fbd9b995e7dc9910d61b661c52371935ab6475d41185632f", true},
	"darwin/arm64":  {"cloudflared-darwin-arm64.tgz", "02fe4bcf08f2c09699cff30658228a739a0c47fb75f3653d1b8772a27ade4462", true},
}

// ReleaseBase is where the pinned assets are downloaded from (tests point
// it at a local server).
var ReleaseBase = "https://github.com/cloudflare/cloudflared/releases/download/" + Version + "/"

func exeName() string {
	if runtime.GOOS == "windows" {
		return "cloudflared.exe"
	}
	return "cloudflared"
}

// Binary says where cloudflared is and how ARK found it.
type Binary struct {
	Path   string `json:"path"`
	Source string `json:"source"` // custom, path, download
}

// lookPath is exec.LookPath (tests replace it).
var lookPath = exec.LookPath

// Find returns a usable cloudflared without downloading: the custom path
// when set, else one on PATH, else an earlier verified download in dir.
func Find(custom, dir string) (Binary, bool) {
	if custom = strings.TrimSpace(custom); custom != "" {
		if fi, err := os.Stat(custom); err == nil && fi.Mode().IsRegular() {
			return Binary{custom, "custom"}, true
		}
		return Binary{}, false
	}
	if p, err := lookPath("cloudflared"); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		return Binary{p, "path"}, true
	}
	if dir != "" {
		p := filepath.Join(dir, exeName())
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return Binary{p, "download"}, true
		}
	}
	return Binary{}, false
}

// ErrUnsupported means there is no pinned build for this OS/architecture.
var ErrUnsupported = errors.New("no cloudflared download for " + runtime.GOOS + "/" + runtime.GOARCH +
	"; install cloudflared and put it on PATH")

// Progress reports downloaded and total bytes (total 0 when unknown).
type Progress func(done, total int64)

// Download fetches the pinned release for this platform into dir, checks
// its SHA-256 and installs it as dir/cloudflared[.exe]. Proxy settings come
// from the environment (HTTPS_PROXY).
func Download(ctx context.Context, dir string, progress Progress) (Binary, error) {
	a, ok := assets[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return Binary{}, ErrUnsupported
	}
	return download(ctx, dir, a, progress)
}

type countingWriter struct {
	done, total int64
	report      Progress
	last        time.Time
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.done += int64(len(p))
	if w.report != nil && time.Since(w.last) > 500*time.Millisecond {
		w.last = time.Now()
		w.report(w.done, w.total)
	}
	return len(p), nil
}

func download(ctx context.Context, dir string, a asset, progress Progress) (Binary, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Binary{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", ReleaseBase+a.name, nil)
	if err != nil {
		return Binary{}, err
	}
	req.Header.Set("User-Agent", "ARK-Studio cloudflared-download")
	client := &http.Client{Timeout: 10 * time.Minute, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}
	resp, err := client.Do(req)
	if err != nil {
		return Binary{}, fmt.Errorf("download cloudflared: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Binary{}, fmt.Errorf("download cloudflared: HTTP %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp(dir, ".cloudflared-*.part")
	if err != nil {
		return Binary{}, err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	counter := &countingWriter{total: max(resp.ContentLength, 0), report: progress}
	_, err = io.Copy(io.MultiWriter(tmp, h, counter), io.LimitReader(resp.Body, 200<<20))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Binary{}, fmt.Errorf("download cloudflared: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != a.sha256 {
		return Binary{}, fmt.Errorf("cloudflared download failed its checksum (got %s, want %s); not installed", got, a.sha256)
	}
	target := filepath.Join(dir, exeName())
	if a.tgz {
		if err := extractTgz(tmp.Name(), target); err != nil {
			return Binary{}, err
		}
	} else {
		os.Remove(target)
		if err := os.Rename(tmp.Name(), target); err != nil {
			return Binary{}, err
		}
	}
	if err := os.Chmod(target, 0o755); err != nil {
		return Binary{}, err
	}
	return Binary{target, "download"}, nil
}

// extractTgz copies the cloudflared entry of a verified .tgz to target.
func extractTgz(archive, target string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err != nil {
			return fmt.Errorf("cloudflared archive: %w", err)
		}
		if hdr.Typeflag == tar.TypeReg && filepath.Base(hdr.Name) == "cloudflared" {
			out, err := os.OpenFile(target+".part", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, io.LimitReader(tr, 200<<20))
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
			os.Remove(target)
			return os.Rename(target+".part", target)
		}
	}
}
