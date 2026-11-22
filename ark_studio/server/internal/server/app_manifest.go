package server

// Go only: the update manifest of the Android apps.
//
//   GET /download/android-apps.json   (public, like the APKs themselves)
//
// answers {apps: {native: {...}}, release: {version, page}}.  The WebView app
// (com.masteryip.arkstudio.web, key "webapp") is no longer built: an installed
// copy finds no entry here and no asset in newer releases, and reports that
// no build was found.
// Each app is {package, file, url, source, version_code, version_name, size,
// sha256, release_version}:
//   source "server"   this server has a build in dist/ (sendAPK serves it):
//                     version_code / version_name are read from the APK,
//                     size and sha256 from the file
//   source "release"  no local build: url (the same /download path)
//                     redirects to the latest GitHub release asset; size and
//                     sha256 come from the release (SHA256SUMS.txt) and
//                     version_code is null, so the app compares
//                     release_version with the release it was built from
//   source "none"     neither is known
// The app verifies sha256 after downloading and reads the downloaded APK's
// version and signature before installing it.

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"arkstudio/go/internal/apkinfo"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/webui"
)

const appManifestPath = "/download/android-apps.json"

var appManifestGoOnly = []PortedRoute{{"goonly:exact:" + appManifestPath, "GET,HEAD"}}

type localAPK struct {
	size    int64
	mod     time.Time
	sha     string
	info    apkinfo.Info
	infoErr error
}

// apkCache keeps the hash and manifest of each dist/ APK until the file changes.
type apkCache struct {
	mu    sync.Mutex
	files map[string]localAPK
}

func (a *apkCache) get(path string) (localAPK, bool) {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return localAPK{}, false
	}
	a.mu.Lock()
	cached, ok := a.files[path]
	a.mu.Unlock()
	if ok && cached.size == fi.Size() && cached.mod.Equal(fi.ModTime()) {
		return cached, true
	}
	f, err := os.Open(path)
	if err != nil {
		return localAPK{}, false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return localAPK{}, false
	}
	out := localAPK{size: fi.Size(), mod: fi.ModTime(), sha: hex.EncodeToString(h.Sum(nil))}
	out.info, out.infoErr = apkinfo.Read(f, fi.Size())
	a.mu.Lock()
	if a.files == nil {
		a.files = map[string]localAPK{}
	}
	a.files[path] = out
	a.mu.Unlock()
	return out, true
}

var appManifestApps = []struct{ key, file, path, appID string }{
	{"native", "ARK-Studio-android.apk", "/download/ARK-Studio-android.apk", "com.masteryip.arkstudio"},
}

// apkDir is the folder of this server's own APK builds: ARK_DOWNLOAD_DIR
// when set, else dist/ beside a web root on disk.  A binary with the web UI
// embedded has no such folder unless ARK_DOWNLOAD_DIR names one ("").
func (a *App) apkDir() string {
	if dir := strings.TrimSpace(os.Getenv("ARK_DOWNLOAD_DIR")); dir != "" {
		return dir
	}
	if a.Assets != nil && a.Assets.Dir != "" {
		return webui.Beside(a.Assets.Dir, "dist")
	}
	return ""
}

// getAppManifest answers GET /download/android-apps.json.
func (c *reqCtx) getAppManifest() error {
	dist := c.app.apkDir()
	rel := c.app.appRelease.resolve(c.ctx)
	apps := pyjson.NewObject()
	for _, app := range appManifestApps {
		entry := pyjson.NewObject("package", app.appID, "file", app.file, "url", app.path, "source", "none",
			"version_code", nil, "version_name", nil, "size", nil, "sha256", nil, "release_version", nilIfBlank(rel.Version))
		local, ok := localAPK{}, false
		if dist != "" {
			local, ok = c.app.apks.get(filepath.Join(dist, app.file))
		}
		if ok && local.infoErr == nil && local.info.Package == app.appID {
			entry.Set("source", "server").Set("version_code", local.info.VersionCode).
				Set("version_name", local.info.VersionName).Set("size", local.size).Set("sha256", local.sha)
		} else if _, name, direct := rel.url(app.key); direct {
			entry.Set("source", "release")
			if size, ok := rel.Sizes[name]; ok && size > 0 {
				entry.Set("size", size)
			}
			if sum, ok := rel.Sums[name]; ok {
				entry.Set("sha256", sum)
			}
		}
		apps.Set(app.key, entry)
	}
	c.sendJSON(200, pyjson.NewObject("apps", apps, "release", pyjson.NewObject("version", nilIfBlank(rel.Version),
		"page", rel.Page)), headerList{{"Cache-Control", "no-store"}})
	return nil
}

func nilIfBlank(s string) any {
	if s == "" {
		return nil
	}
	return s
}
