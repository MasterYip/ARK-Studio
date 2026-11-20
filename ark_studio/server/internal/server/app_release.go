package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"arkstudio/go/internal/compat/pytext"
)

// Go-only: the Android APKs are downloaded from the latest GitHub release.
// The server (never the browser: GitHub rate-limits shared IPs) asks the
// GitHub API for the latest release, picks the versioned APK assets by name
// and caches the answer.  Forks set ARK_APP_RELEASE_REPO (owner/name) and,
// for a GitHub Enterprise host, ARK_APP_RELEASE_API.

const (
	defaultReleaseRepo = "MasterYip/ARK-Studio"
	defaultReleaseAPI  = "https://api.github.com"
	releaseTTL         = time.Hour       // a good answer is reused this long
	releaseRetry       = 5 * time.Minute // a failed lookup is not retried sooner
	releaseTimeout     = 5 * time.Second
)

// releaseAsset matches the versioned release asset of each app
// (ARK-Studio-<version>-android.apk, ARK-Studio-<version>-android-web.apk).
var releaseAsset = map[string]*regexp.Regexp{
	"native": regexp.MustCompile(`^ARK-Studio-.+-android\.apk$`),
	"webapp": regexp.MustCompile(`^ARK-Studio-.+-android-web\.apk$`),
}

// appRelease is the resolved latest release.  Assets maps "native"/"webapp"
// to the asset's file name and download URL; a missing key falls back to Page.
type appRelease struct {
	Version string
	Page    string
	Assets  map[string][2]string
}

// url is the download URL of an app, or the release page without one.
func (r *appRelease) url(key string) (string, string, bool) {
	if a, ok := r.Assets[key]; ok {
		return a[1], a[0], true
	}
	return r.Page, "", false
}

// releaseResolver caches the latest release in memory.
type releaseResolver struct {
	mu       sync.Mutex
	key      string
	good     *appRelease
	goodAt   time.Time
	failedAt time.Time
	client   *http.Client
}

func releaseRepo() string {
	if v := strings.Trim(pytext.Strip(os.Getenv("ARK_APP_RELEASE_REPO")), "/"); v != "" {
		return v
	}
	return defaultReleaseRepo
}

func releaseAPI() string {
	if v := strings.TrimRight(pytext.Strip(os.Getenv("ARK_APP_RELEASE_API")), "/"); v != "" {
		return v
	}
	return defaultReleaseAPI
}

// latestReleasePage is the public "latest release" page of a repo.
func latestReleasePage(repo string) string {
	return "https://github.com/" + repo + "/releases/latest"
}

// resolve returns the latest release: the cached answer while fresh, a new
// lookup when stale, the last good answer when the lookup fails, and a bare
// release-page fallback when there has never been one.  Contract replays
// (ARK_REPLAY_CLOCK=1) never touch the network unless an API is configured.
func (r *releaseResolver) resolve(ctx context.Context) *appRelease {
	repo, api := releaseRepo(), releaseAPI()
	fallback := &appRelease{Page: latestReleasePage(repo)}
	if os.Getenv("ARK_REPLAY_CLOCK") == "1" && os.Getenv("ARK_APP_RELEASE_API") == "" {
		return fallback
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if key := api + "|" + repo; key != r.key {
		r.key, r.good, r.goodAt, r.failedAt = key, nil, time.Time{}, time.Time{}
	}
	now := time.Now()
	if r.good != nil && now.Sub(r.goodAt) < releaseTTL {
		return r.good
	}
	if !r.failedAt.IsZero() && now.Sub(r.failedAt) < releaseRetry {
		if r.good != nil {
			return r.good
		}
		return fallback
	}
	got, err := r.fetch(ctx, api, repo)
	if err != nil {
		r.failedAt = now
		if r.good != nil {
			return r.good
		}
		return fallback
	}
	r.good, r.goodAt, r.failedAt = got, now, time.Time{}
	return got
}

// fetch is GET {api}/repos/{repo}/releases/latest.
func (r *releaseResolver) fetch(ctx context.Context, api, repo string) (*appRelease, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", api+"/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "ARK-Studio-server")
	client := r.client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub latest release: HTTP %d", resp.StatusCode)
	}
	var body struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return nil, err
	}
	out := &appRelease{Version: body.TagName, Page: latestReleasePage(repo),
		Assets: map[string][2]string{}}
	if strings.HasPrefix(body.HTMLURL, "https://") {
		out.Page = body.HTMLURL
	}
	for _, a := range body.Assets {
		if !strings.HasPrefix(a.URL, "https://") {
			continue
		}
		for key, re := range releaseAsset {
			if _, seen := out.Assets[key]; !seen && re.MatchString(a.Name) {
				out.Assets[key] = [2]string{a.Name, a.URL}
			}
		}
	}
	return out, nil
}
