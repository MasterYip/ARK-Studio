package server

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	wsapi "arkstudio/go/internal/api/workspace"
	"arkstudio/go/internal/workspace"
)

// GET /api/workspace/preview/<token>/<path> (Go only): the files of an
// interactive HTML preview (workspace/preview.go).  It runs before the
// session checks and never looks at cookies, session or agent tokens: the
// path-scoped, expiring token is the only authorisation, and it only reads
// files under its folder.  Every reply carries a CSP that sandboxes the
// document (opaque origin, so no ARK cookies, storage or DOM even when the
// page is opened in its own tab) and limits its own requests to the token's
// folder, so a script cannot reach /api/* or other projects.

// previewGoOnly is the Go-only id of this file (GoOnlyRoutes).
var previewGoOnly = []PortedRoute{{"goonly:prefix:" + wsapi.PreviewPrefix, "GET,HEAD"}}

// previewSandbox matches the Editor's <iframe sandbox>.
const previewSandbox = "sandbox allow-scripts allow-popups allow-forms allow-modals allow-pointer-lock allow-downloads"

func isPreviewPath(path string) bool { return strings.HasPrefix(path, wsapi.PreviewPrefix) }

// previewCSP: scripts, styles, images, media and fonts from the token's
// folder (any port of this host, http or https: proxies may hide the port)
// or https CDNs; fetch/XHR/WebSocket, workers, frames and forms only to the
// token's folder.
func (c *reqCtx) previewCSP(token string) string {
	host := c.r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if strings.Contains(host, ":") { // IPv6 literal
		host = "[" + strings.Trim(host, "[]") + "]"
	}
	base := strings.TrimSuffix(c.externalPrefix(), "/") + wsapi.PreviewPrefix + token + "/"
	own := "https://" + host + ":*" + base + " http://" + host + ":*" + base
	return previewSandbox + "; default-src 'none'; " +
		"script-src " + own + " 'unsafe-inline' 'unsafe-eval' blob: https:; " +
		"style-src " + own + " 'unsafe-inline' https:; " +
		"img-src " + own + " data: blob: https:; media-src " + own + " data: blob: https:; " +
		"font-src " + own + " data: https:; connect-src " + own + " data: blob:; " +
		"worker-src " + own + " blob:; frame-src " + own + "; " +
		"form-action " + own + "; base-uri " + own + "; frame-ancestors 'self'"
}

func (c *reqCtx) workspacePreview() error {
	if c.r.Method != http.MethodGet && c.r.Method != http.MethodHead {
		return apiErr(405, "Method not allowed", "")
	}
	rest := strings.TrimPrefix(c.path, wsapi.PreviewPrefix)
	token, file, _ := strings.Cut(rest, "/")
	p, ok := workspace.LookupPreview(token)
	if !ok {
		return apiErr(403, "The preview has expired. Reload it in the Editor.", "PREVIEW_EXPIRED")
	}
	target, err := workspace.PreviewFile(p, file)
	if err != nil {
		return err
	}
	f, err := os.Open(target)
	if err != nil {
		return apiErr(404, "file not found", "")
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return apiErr(404, "file not found", "")
	}
	name := filepath.Base(target)
	ctype := guessType(name)
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mjs":
		ctype = "text/javascript"
	case ".wasm":
		ctype = "application/wasm"
	}
	if ctype == "" {
		ctype = "application/octet-stream"
	} else if strings.HasPrefix(ctype, "text/") || ctype == "application/javascript" || ctype == "application/json" {
		ctype += "; charset=utf-8"
	}
	h := c.w.Header()
	h.Set("Content-Type", ctype)
	securityHeaders(h, headerList{
		{"Content-Security-Policy", c.previewCSP(token)},
		{"X-Frame-Options", "SAMEORIGIN"},
		{"Cache-Control", "private, no-cache"},
		// The page's origin is opaque, so its own fetch() of a file next to it
		// is cross-origin: allow reading (the token is the only key; no
		// credentials are involved).
		{"Access-Control-Allow-Origin", "*"},
	})
	http.ServeContent(c.w, c.r, name, fi.ModTime(), f)
	return nil
}
