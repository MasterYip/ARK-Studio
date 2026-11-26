package server

// Go only: the update manifest of the Android app.
//
//   GET /download/android-apps.json   (public, like the APK links)
//
// answers {apps: {native: {...}}, release: {version, page}}.  The WebView app
// (com.masteryip.arkstudio.web, key "webapp") is no longer built: an installed
// copy finds no entry here and no asset in newer releases, and reports that
// no build was found.
// Each app is {package, file, url, source, version_code, version_name, size,
// sha256, release_version}:
//   source "github"  url is the APK asset of the latest GitHub release
//                    (app_release.go); size and sha256 come from the release
//                    (asset digest, else SHA256SUMS.txt); version_code and
//                    version_name are read from the asset's manifest (a few
//                    range requests in the background, once per release),
//                    null until then, when the app compares release_version
//                    with the release it was built from
//   source "none"    the release could not be resolved
// The APK always comes from GitHub: a server does not serve its own builds.
// The app verifies sha256 after downloading and reads the downloaded APK's
// version and signature before installing it.

import (
	"arkstudio/go/internal/compat/pyjson"
)

const appManifestPath = "/download/android-apps.json"

var appManifestGoOnly = []PortedRoute{{"goonly:exact:" + appManifestPath, "GET,HEAD"}}

var appManifestApps = []struct{ key, file, path, appID string }{
	{"native", "ARK-Studio-android.apk", "/download/ARK-Studio-android.apk", "com.masteryip.arkstudio"},
}

// getAppManifest answers GET /download/android-apps.json.
func (c *reqCtx) getAppManifest() error {
	rel := c.app.appRelease.resolve(c.ctx)
	apps := pyjson.NewObject()
	for _, app := range appManifestApps {
		entry := pyjson.NewObject("package", app.appID, "file", app.file, "url", app.path, "source", "none",
			"version_code", nil, "version_name", nil, "size", nil, "sha256", nil, "release_version", nilIfBlank(rel.Version))
		if url, name, direct := rel.url(app.key); direct {
			entry.Set("source", "github").Set("url", url).Set("file", name)
			if size, ok := rel.Sizes[name]; ok && size > 0 {
				entry.Set("size", size)
			}
			if sum, ok := rel.Sums[name]; ok {
				entry.Set("sha256", sum)
			}
			if info, ok := c.app.appRelease.apkInfo(url, rel.Sizes[name]); ok && info.Package == app.appID {
				entry.Set("version_code", info.VersionCode).Set("version_name", nilIfBlank(info.VersionName))
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
