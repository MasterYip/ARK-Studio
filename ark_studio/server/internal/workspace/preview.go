package workspace

import (
	"crypto/rand"
	"encoding/base64"
	"path"
	"strings"
	"sync"
	"time"

	"arkstudio/go/internal/project"
)

// Interactive HTML previews.  The Editor shows a project .html file in an
// <iframe sandbox="allow-scripts …"> without allow-same-origin, so the page
// runs in an opaque origin: no ARK cookies, storage or DOM.  Its files come
// from GET /api/workspace/preview/<token>/<path>, which ignores cookies and
// session tokens: the random token alone authorises reading files under one
// folder of one project, read-only, until it expires.  The Editor renews it
// while the preview is open.

const (
	PreviewTTL  = 30 * time.Minute
	MaxPreviews = 2000
)

// Preview is one issued token.
type Preview struct {
	Token     string
	ProjectID string
	Root      string // the project folder
	Dir       string // project-relative folder the token may read ("" = the whole project)
	User      string
	Expires   time.Time
}

var previews = struct {
	sync.Mutex
	m map[string]*Preview
}{m: map[string]*Preview{}}

// previewNow is time.Now (tests move it).
var previewNow = time.Now

func newPreviewToken() string {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func prunePreviews(now time.Time) {
	for token, p := range previews.m {
		if !now.Before(p.Expires) {
			delete(previews.m, token)
		}
	}
	// Still too many: drop the ones expiring first.
	for len(previews.m) >= MaxPreviews {
		var oldest *Preview
		for _, p := range previews.m {
			if oldest == nil || p.Expires.Before(oldest.Expires) {
				oldest = p
			}
		}
		delete(previews.m, oldest.Token)
	}
}

// IssuePreview checks that path is an HTML file of the project and returns a
// token for its folder.
func IssuePreview(root, projectID, user, file string) (*Preview, string, error) {
	target, rel, err := resolve(project.Resolve(root), file, true)
	if err != nil {
		return nil, "", err
	}
	if !isFile(target) {
		return nil, "", werr(400, "path is a folder, not a file")
	}
	if ext := strings.ToLower(path.Ext(rel)); ext != ".html" && ext != ".htm" {
		return nil, "", werr(400, "only .html files have an interactive preview")
	}
	dir := path.Dir(rel)
	if dir == "." {
		dir = ""
	}
	now := previewNow()
	p := &Preview{Token: newPreviewToken(), ProjectID: projectID, Root: project.Resolve(root), Dir: dir, User: user,
		Expires: now.Add(PreviewTTL)}
	previews.Lock()
	prunePreviews(now)
	previews.m[p.Token] = p
	previews.Unlock()
	name := rel
	if dir != "" {
		name = strings.TrimPrefix(rel, dir+"/")
	}
	return p, name, nil
}

// RenewPreview extends a live token of the same project and person.
func RenewPreview(token, projectID, user string) (*Preview, bool) {
	now := previewNow()
	previews.Lock()
	defer previews.Unlock()
	p, ok := previews.m[token]
	if !ok || !now.Before(p.Expires) || p.ProjectID != projectID || p.User != user {
		return nil, false
	}
	p.Expires = now.Add(PreviewTTL)
	return p, true
}

// LookupPreview returns a live token.
func LookupPreview(token string) (*Preview, bool) {
	now := previewNow()
	previews.Lock()
	defer previews.Unlock()
	p, ok := previews.m[token]
	if !ok {
		return nil, false
	}
	if !now.Before(p.Expires) {
		delete(previews.m, token)
		return nil, false
	}
	copy := *p
	return &copy, true
}

// PreviewFile resolves a file the token may read: rel is relative to the
// token's folder and goes through the workspace path rules (no "..",
// absolute paths, links, ignored or protected folders).  A folder serves its
// index.html.
func PreviewFile(p *Preview, rel string) (string, error) {
	rel = strings.TrimPrefix(strings.ReplaceAll(rel, `\`, "/"), "/")
	if rel == "" {
		return "", werr(404, "file not found")
	}
	full := rel
	if p.Dir != "" {
		full = p.Dir + "/" + rel // not cleaned: ".." parts are refused by resolve
	}
	target, _, err := resolve(p.Root, full, true)
	if err != nil {
		return "", err
	}
	if isDir(target) {
		index := full + "/index.html"
		if target, _, err = resolve(p.Root, index, true); err != nil {
			return "", err
		}
	}
	if !isFile(target) {
		return "", werr(404, "file not found")
	}
	return target, nil
}
