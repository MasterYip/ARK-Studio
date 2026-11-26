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
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
	app, _ := apkApp(t)
	apk := fakeAPK(t)
	sum := sha256.Sum256(apk)
	var gh *httptest.Server
	var ranges atomic.Int32
	gh = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/fork/ark/releases/latest":
			w.Write([]byte(`{"tag_name":"v2.2.0","assets":[` + //nolint:errcheck
				`{"name":"ARK-Studio-2.2.0-android.apk","size":` + strconv.Itoa(len(apk)) + `,"browser_download_url":"` + gh.URL + `/a.apk",` +
				`"digest":"sha256:` + strings.ToUpper(hex.EncodeToString(sum[:])) + `"},` +
				`{"name":"ARK-Studio-2.2.0-android-web.apk","size":3000,"browser_download_url":"` + gh.URL + `/w.apk"}]}`))
		case "/a.apk":
			if r.Header.Get("Range") == "" {
				t.Errorf("whole APK downloaded")
			}
			ranges.Add(1)
			http.ServeContent(w, r, "a.apk", time.Time{}, bytes.NewReader(apk))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(gh.Close)
	t.Setenv("ARK_REPLAY_CLOCK", "")
	t.Setenv("ARK_APP_RELEASE_API", gh.URL)
	t.Setenv("ARK_APP_RELEASE_REPO", "fork/ark")
	app.appRelease.client = gh.Client()

	// The APK of the latest release: URL, size and SHA-256 from the release,
	// version code and name read from the asset with range requests.  The
	// WebView app is not listed, even when an older release still has its APK.
	apps := readAppManifest(t, app, nil)
	native := apps["native"]
	// The version is read in the background: null at first, then known.
	for i := 0; i < 100 && native.VersionCode == nil; i++ {
		time.Sleep(50 * time.Millisecond)
		apps = readAppManifest(t, app, nil)
		native = apps["native"]
	}
	if native.Source != "github" || native.URL != gh.URL+"/a.apk" || native.VersionCode == nil || *native.VersionCode != 16 ||
		native.VersionName == nil || *native.VersionName != "3.0" || *native.Size != int64(len(apk)) ||
		native.SHA256 == nil || *native.SHA256 != hex.EncodeToString(sum[:]) || *native.Release != "v2.2.0" {
		t.Errorf("native from the release: %+v", native)
	}
	if _, ok := apps["webapp"]; ok || len(apps) != 1 {
		t.Errorf("apps: %+v", apps)
	}
	// Read once: a stale lookup of the same release reuses the version.
	n := ranges.Load()
	app.appRelease.mu.Lock()
	app.appRelease.goodAt = time.Now().Add(-2 * releaseTTL)
	app.appRelease.mu.Unlock()
	if native = readAppManifest(t, app, nil)["native"]; native.VersionCode == nil || ranges.Load() != n {
		t.Errorf("version read again: %+v (%d range requests, was %d)", native, ranges.Load(), n)
	}
	// The old download paths redirect to the asset; the server never serves an APK.
	for _, path := range []string{"/download/ARK-Studio-android.apk", "/download/ARK-Studio-webapp.apk"} {
		if r := gpuCall(t, app, "GET", path, nil, nil); r.code != 302 {
			t.Errorf("download %s: %d", path, r.code)
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
