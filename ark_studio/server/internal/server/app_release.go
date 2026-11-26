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

	"arkstudio/go/internal/apkinfo"
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
	apkInfoTimeout     = 3 * time.Minute // reading an APK's version from its release asset (in the background)
)

// releaseAsset matches the versioned release asset of each app
// (ARK-Studio-<version>-android.apk).
var releaseAsset = map[string]*regexp.Regexp{
	"native": regexp.MustCompile(`^ARK-Studio-.+-android\.apk$`),
}

// appRelease is the resolved latest release.  Assets maps "native"
// to the asset's file name and download URL; a missing key falls back to Page.
// Sizes and Sums (from the release's SHA256SUMS.txt, best effort) are by
// asset file name, for the update manifest (app_manifest.go).
type appRelease struct {
	Version string
	Page    string
	Assets  map[string][2]string
	Sizes   map[string]int64
	Sums    map[string]string
}

// releaseSumsAsset is the checksum file of a release (.github/workflows/release.yml).
const releaseSumsAsset = "SHA256SUMS.txt"

// parseSums reads "<sha256>  <file>" lines (sha256sum output).
func parseSums(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 64 {
			continue
		}
		out[strings.TrimPrefix(fields[1], "*")] = strings.ToLower(fields[0])
	}
	return out
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
	infos    map[string]apkinfo.Info // by asset URL: read once per release (apkInfo)
	tried    map[string]time.Time    // by asset URL: when a read started
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
			Size int64  `json:"size"`
			// "sha256:<hex>" (GitHub computes it for release assets).
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return nil, err
	}
	out := &appRelease{Version: body.TagName, Page: latestReleasePage(repo),
		Assets: map[string][2]string{}, Sizes: map[string]int64{}, Sums: map[string]string{}}
	sumsURL := ""
	if strings.HasPrefix(body.HTMLURL, "https://") {
		out.Page = body.HTMLURL
	}
	for _, a := range body.Assets {
		if !strings.HasPrefix(a.URL, "https://") {
			continue
		}
		out.Sizes[a.Name] = a.Size
		if hex, ok := strings.CutPrefix(a.Digest, "sha256:"); ok && len(hex) == 64 {
			out.Sums[a.Name] = strings.ToLower(hex)
		}
		if a.Name == releaseSumsAsset {
			sumsURL = a.URL
		}
		for key, re := range releaseAsset {
			if _, seen := out.Assets[key]; !seen && re.MatchString(a.Name) {
				out.Assets[key] = [2]string{a.Name, a.URL}
			}
		}
	}
	// The checksum file only for assets without a digest (the download host
	// may be unreachable where the API is not).
	missing := false
	for key := range releaseAsset {
		if a, ok := out.Assets[key]; ok && out.Sums[a[0]] == "" {
			missing = true
		}
	}
	if sumsURL != "" && missing {
		if text, err := r.fetchText(ctx, client, sumsURL); err == nil {
			for name, sum := range parseSums(text) {
				if out.Sums[name] == "" {
					out.Sums[name] = sum
				}
			}
		}
	}
	return out, nil
}

// fetchText downloads a small release asset (the checksum file).
func (r *releaseResolver) fetchText(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "ARK-Studio-server")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("release asset: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	return string(data), err
}

// apkInfo is the package and version of an APK asset, read from its
// AndroidManifest.xml with HTTP range requests (the end of the zip and the
// manifest entry, not the whole APK).  The read runs in the background (the
// download host can be slow); until it is done, and after a failure until
// the next try (releaseRetry), ok is false.
func (r *releaseResolver) apkInfo(url string, size int64) (apkinfo.Info, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if info, ok := r.infos[url]; ok {
		return info, true
	}
	if size <= 0 || !strings.HasPrefix(url, "https://") {
		return apkinfo.Info{}, false
	}
	if at, ok := r.tried[url]; ok && time.Since(at) < releaseRetry+apkInfoTimeout {
		return apkinfo.Info{}, false
	}
	if r.tried == nil {
		r.tried = map[string]time.Time{}
	}
	r.tried[url] = time.Now()
	client := r.client
	if client == nil {
		client = http.DefaultClient
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), apkInfoTimeout)
		defer cancel()
		info, err := apkinfo.Read(&rangeReader{ctx: ctx, client: client, url: url, size: size}, size)
		r.mu.Lock()
		defer r.mu.Unlock()
		if err != nil {
			r.tried[url] = time.Now().Add(-apkInfoTimeout) // try again after releaseRetry
			return
		}
		if r.infos == nil {
			r.infos = map[string]apkinfo.Info{}
		}
		r.infos[url] = info
	}()
	return apkinfo.Info{}, false
}

// rangeReader is an io.ReaderAt over HTTP range requests, in 64 KiB blocks
// (a zip reader reads small pieces near each other).
type rangeReader struct {
	ctx    context.Context
	client *http.Client
	url    string
	size   int64
	blocks map[int64][]byte
}

const rangeBlock = 64 << 10

func (rr *rangeReader) ReadAt(p []byte, off int64) (int, error) {
	if off >= rr.size {
		return 0, io.EOF
	}
	n := 0
	for n < len(p) && off+int64(n) < rr.size {
		pos := off + int64(n)
		start := pos - pos%rangeBlock
		block, err := rr.block(start)
		if err != nil {
			return n, err
		}
		c := copy(p[n:], block[pos-start:])
		if c == 0 {
			return n, io.ErrUnexpectedEOF
		}
		n += c
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (rr *rangeReader) block(start int64) ([]byte, error) {
	if b, ok := rr.blocks[start]; ok {
		return b, nil
	}
	if len(rr.blocks) >= 64 {
		return nil, fmt.Errorf("apk asset: too many reads")
	}
	end := min(start+rangeBlock, rr.size) - 1
	req, err := http.NewRequestWithContext(rr.ctx, "GET", rr.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ARK-Studio-server")
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	resp, err := rr.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("apk asset: HTTP %d to a range request", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, rangeBlock+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != end-start+1 {
		return nil, fmt.Errorf("apk asset: short range")
	}
	if rr.blocks == nil {
		rr.blocks = map[int64][]byte{}
	}
	rr.blocks[start] = data
	return data, nil
}
