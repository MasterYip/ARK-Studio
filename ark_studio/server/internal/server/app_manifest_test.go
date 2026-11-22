package server

// Go only: GET /download/android-apps.json (app_manifest.go).

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type manifestApp struct {
	Package     string  `json:"package"`
	URL         string  `json:"url"`
	Source      string  `json:"source"`
	VersionCode *int64  `json:"version_code"`
	VersionName *string `json:"version_name"`
	Size        *int64  `json:"size"`
	SHA256      *string `json:"sha256"`
	Release     *string `json:"release_version"`
}

func readAppManifest(t *testing.T, app *App, headers map[string]string) map[string]manifestApp {
	t.Helper()
	r := gpuCall(t, app, "GET", appManifestPath, nil, headers)
	if r.code != 200 {
		t.Fatalf("manifest: %d %s", r.code, r.raw)
	}
	var body struct {
		Apps map[string]manifestApp `json:"apps"`
	}
	if err := json.Unmarshal([]byte(r.raw), &body); err != nil {
		t.Fatal(err)
	}
	return body.Apps
}

// fakeAPK is a zip with the native app's compiled manifest (apkinfo testdata).
func fakeAPK(t *testing.T) []byte {
	t.Helper()
	manifest, err := os.ReadFile(filepath.Join("..", "apkinfo", "testdata", "AndroidManifest.xml"))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	w, _ := z.Create("AndroidManifest.xml")
	_, _ = w.Write(manifest)
	_ = z.Close()
	return buf.Bytes()
}

func TestAppManifest(t *testing.T) {
	app, dist := apkApp(t)
	webSum := strings.Repeat("c", 64)
	var gh *httptest.Server
	gh = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/fork/ark/releases/latest":
			w.Write([]byte(`{"tag_name":"v2.2.0","assets":[` + //nolint:errcheck
				`{"name":"ARK-Studio-2.2.0-android.apk","size":4000,"browser_download_url":"` + gh.URL + `/a.apk",` +
				`"digest":"sha256:` + strings.Repeat("D", 64) + `"},` +
				`{"name":"ARK-Studio-2.2.0-android-web.apk","size":3000,"browser_download_url":"` + gh.URL + `/w.apk"},` +
				`{"name":"SHA256SUMS.txt","size":200,"browser_download_url":"` + gh.URL + `/sums"}]}`))
		case "/sums":
			w.Write([]byte(webSum + "  ARK-Studio-2.2.0-android-web.apk\n")) //nolint:errcheck
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(gh.Close)
	t.Setenv("ARK_REPLAY_CLOCK", "")
	t.Setenv("ARK_APP_RELEASE_API", gh.URL)
	t.Setenv("ARK_APP_RELEASE_REPO", "fork/ark")
	app.appRelease.client = gh.Client()

	// No local build: the app comes from the release (size; SHA-256 from the
	// asset digest, else from SHA256SUMS.txt).  The WebView app is not listed,
	// even when an older release still has its APK.
	apps := readAppManifest(t, app, nil)
	native := apps["native"]
	if native.Source != "release" || native.VersionCode != nil || *native.Size != 4000 || native.SHA256 == nil ||
		*native.SHA256 != strings.Repeat("d", 64) ||
		*native.Release != "v2.2.0" || native.URL != "/download/ARK-Studio-android.apk" {
		t.Errorf("native from the release: %+v", native)
	}
	if _, ok := apps["webapp"]; ok || len(apps) != 1 {
		t.Errorf("apps: %+v", apps)
	}

	// A build in dist/: version and checksum read from the file.
	apk := fakeAPK(t)
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "ARK-Studio-android.apk"), apk, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(apk)
	apps = readAppManifest(t, app, nil)
	native = apps["native"]
	if native.Source != "server" || native.VersionCode == nil || *native.VersionCode != 16 || *native.VersionName != "3.0" ||
		*native.Size != int64(len(apk)) || *native.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("native from dist: %+v", native)
	}
	// The served file is the one described; the WebView app's old path serves it too.
	for _, path := range []string{"/download/ARK-Studio-android.apk", "/download/ARK-Studio-webapp.apk"} {
		if r := gpuCall(t, app, "GET", path, nil, nil); r.code != 200 || len(r.raw) != len(apk) {
			t.Errorf("download %s: %d %d bytes", path, r.code, len(r.raw))
		}
	}
}

func TestAppManifestIsPublic(t *testing.T) {
	// No GitHub lookup: a release API that has nothing (ARK_REPLAY_CLOCK=1
	// would also fix the seed dates for later tests).
	none := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(none.Close)
	t.Setenv("ARK_REPLAY_CLOCK", "")
	t.Setenv("ARK_APP_RELEASE_API", none.URL)
	app, _ := authDesktopApp(t)
	apps := readAppManifest(t, app, nil)
	if apps["native"].Package != "com.masteryip.arkstudio" || len(apps) != 1 {
		t.Fatalf("signed-out manifest: %+v", apps)
	}
}

// ARK_DOWNLOAD_DIR names the APK folder of a server whose web UI is embedded
// (no web root on disk, so no dist/ beside it).
func TestAppManifestDownloadDir(t *testing.T) {
	app, _ := apkApp(t)
	app.Assets = nil
	t.Setenv("ARK_REPLAY_CLOCK", "1")
	t.Setenv("ARK_APP_RELEASE_API", "")
	if dir := app.apkDir(); dir != "" {
		t.Fatalf("embedded without ARK_DOWNLOAD_DIR: %q", dir)
	}
	dir := t.TempDir()
	t.Setenv("ARK_DOWNLOAD_DIR", dir)
	apk := fakeAPK(t)
	if err := os.WriteFile(filepath.Join(dir, "ARK-Studio-android.apk"), apk, 0o644); err != nil {
		t.Fatal(err)
	}
	native := readAppManifest(t, app, nil)["native"]
	if native.Source != "server" || native.VersionCode == nil || *native.VersionCode != 16 {
		t.Errorf("native from ARK_DOWNLOAD_DIR: %+v", native)
	}
	if r := gpuCall(t, app, "GET", "/download/ARK-Studio-android.apk", nil, nil); r.code != 200 || len(r.raw) != len(apk) {
		t.Errorf("download: %d %d bytes", r.code, len(r.raw))
	}
}
