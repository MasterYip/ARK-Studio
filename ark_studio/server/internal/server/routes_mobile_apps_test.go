package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// apkApp is a local-mode app whose web root sits in a temp checkout, so its
// dist/ folder (webui.Beside) is under the test's control.
func apkApp(t *testing.T) (*App, string) {
	t.Helper()
	root := t.TempDir()
	web := filepath.Join(root, "web")
	if err := os.MkdirAll(web, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<!doctype html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	app, err := NewApp(Config{Root: filepath.Join(root, "projects"), NoDemo: true,
		WebRoot: web, SessionsRoot: filepath.Join(root, "sessions")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	app.Bind("127.0.0.1", 8765)
	return app, filepath.Join(root, "dist")
}

const fakeRelease = `{"tag_name":"v2.1.0","html_url":"https://github.com/fork/ark/releases/tag/v2.1.0","assets":[
 {"name":"SHA256SUMS","browser_download_url":"https://github.com/fork/ark/releases/download/v2.1.0/SHA256SUMS"},
 {"name":"ARK-Studio-v2.1.0-windows-x64.zip","browser_download_url":"https://github.com/fork/ark/releases/download/v2.1.0/ARK-Studio-v2.1.0-windows-x64.zip"},
 {"name":"ARK-Studio-v2.1.0-android-web.apk","browser_download_url":"https://github.com/fork/ark/releases/download/v2.1.0/ARK-Studio-v2.1.0-android-web.apk"},
 {"name":"ARK-Studio-v2.1.0-android.apk","browser_download_url":"https://github.com/fork/ark/releases/download/v2.1.0/ARK-Studio-v2.1.0-android.apk"}]}`

// fakeGitHub serves /repos/fork/ark/releases/latest; status is read per request.
func fakeGitHub(t *testing.T, status *atomic.Int32, hits *atomic.Int32) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/repos/fork/ark/releases/latest" || r.Header.Get("User-Agent") == "" {
			http.NotFound(w, r)
			return
		}
		if code := int(status.Load()); code != 200 {
			w.WriteHeader(code)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fakeRelease)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ARK_REPLAY_CLOCK", "")
	t.Setenv("ARK_APP_RELEASE_API", srv.URL)
	t.Setenv("ARK_APP_RELEASE_REPO", "fork/ark")
}

type mobileAppsBody struct {
	Version    string `json:"version"`
	ReleaseURL string `json:"release_url"`
	Apps       map[string]struct {
		Available bool   `json:"available"`
		URL       string `json:"url"`
		File      string `json:"file"`
		Direct    bool   `json:"direct"`
		QR        string `json:"qr_svg"`
		Local     bool   `json:"local"`
		LocalURL  string `json:"local_url"`
		Size      int64  `json:"size"`
	} `json:"apps"`
}

func getMobileApps(t *testing.T, app *App) mobileAppsBody {
	t.Helper()
	w := docsGet(app, "GET", "/api/mobile/apps", nil)
	if w.Code != 200 {
		t.Fatalf("apps: %d %s", w.Code, w.Body)
	}
	var out mobileAppsBody
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMobileAppsResolveTheLatestRelease(t *testing.T) {
	var status, hits atomic.Int32
	status.Store(200)
	fakeGitHub(t, &status, &hits)
	app, dist := apkApp(t)

	out := getMobileApps(t, app)
	if out.Version != "v2.1.0" || out.ReleaseURL != "https://github.com/fork/ark/releases/tag/v2.1.0" {
		t.Fatalf("release: %q %q", out.Version, out.ReleaseURL)
	}
	base := "https://github.com/fork/ark/releases/download/v2.1.0/"
	file := "ARK-Studio-v2.1.0-android.apk"
	if got := out.Apps["native"]; len(out.Apps) != 1 || !got.Available || !got.Direct || got.URL != base+file ||
		got.File != file || !strings.HasPrefix(got.QR, "<svg") || got.Local || got.LocalURL == "" {
		t.Fatalf("apps = %+v", out.Apps)
	}

	// Cached: the next calls and the /download redirect do not ask GitHub again.
	getMobileApps(t, app)
	w := docsGet(app, "GET", "/download/ark.apk", nil)
	if w.Code != 302 || w.Header().Get("Location") != base+"ARK-Studio-v2.1.0-android.apk" {
		t.Fatalf("redirect: %d %q", w.Code, w.Header().Get("Location"))
	}
	w = docsGet(app, "HEAD", "/download/ARK-Studio-webapp.apk", nil)
	if w.Code != 302 || w.Header().Get("Location") != base+"ARK-Studio-v2.1.0-android.apk" {
		t.Fatalf("redirect web: %d %q", w.Code, w.Header().Get("Location"))
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("GitHub asked %d times, want 1", n)
	}

	// Stale cache + failing lookup: the last good answer stays.
	status.Store(500)
	app.appRelease.mu.Lock()
	app.appRelease.goodAt = time.Now().Add(-2 * releaseTTL)
	app.appRelease.mu.Unlock()
	if out = getMobileApps(t, app); out.Version != "v2.1.0" || out.Apps["native"].URL != base+"ARK-Studio-v2.1.0-android.apk" {
		t.Fatalf("last good: %+v", out)
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("GitHub asked %d times, want 2", n)
	}

	// A file in dist/ is not served: the APK always comes from GitHub.
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "ARK-Studio-android.apk"), []byte("NATIVE"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARK_DOWNLOAD_DIR", dist)
	if w = docsGet(app, "GET", "/download/ARK-Studio-android.apk", nil); w.Code != 302 {
		t.Fatalf("dist: %d", w.Code)
	}
	if out = getMobileApps(t, app); out.Apps["native"].Local || out.Apps["native"].Size != 0 {
		t.Fatalf("local flags: %+v", out.Apps)
	}
}

func TestMobileAppsFallBackToTheReleasePage(t *testing.T) {
	var status, hits atomic.Int32
	status.Store(403) // rate limited
	fakeGitHub(t, &status, &hits)
	app, _ := apkApp(t)

	page := "https://github.com/fork/ark/releases/latest"
	out := getMobileApps(t, app)
	if out.ReleaseURL != page || out.Version != "" {
		t.Fatalf("release: %q %q", out.ReleaseURL, out.Version)
	}
	if got := out.Apps["native"]; !got.Available || got.Direct || got.URL != page || got.File != "" {
		t.Fatalf("native = %+v", got)
	}
	w := docsGet(app, "GET", "/download/ARK-Studio-android.apk", nil)
	if w.Code != 302 || w.Header().Get("Location") != page {
		t.Fatalf("redirect: %d %q", w.Code, w.Header().Get("Location"))
	}
	// A failed lookup is not retried at once.
	if n := hits.Load(); n != 1 {
		t.Fatalf("GitHub asked %d times, want 1", n)
	}
}

func TestMobileAppsReplayNeverAsksGitHub(t *testing.T) {
	t.Setenv("ARK_REPLAY_CLOCK", "1")
	t.Setenv("ARK_APP_RELEASE_API", "")
	t.Setenv("ARK_APP_RELEASE_REPO", "")
	app, _ := apkApp(t)
	out := getMobileApps(t, app)
	page := "https://github.com/MasterYip/ARK-Studio/releases/latest"
	if out.ReleaseURL != page || out.Apps["native"].URL != page {
		t.Fatalf("replay: %+v", out)
	}
}
