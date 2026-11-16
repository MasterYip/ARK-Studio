package server

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"arkstudio/go/internal/platform/pyerr"

	"arkstudio/go/internal/platform/pathx"

	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/compat/pyurl"
	"arkstudio/go/internal/project"
)

const goos = runtime.GOOS

// FileCSP and PDFCSP are Handler.FILE_CSP / PDF_CSP.
const (
	FileCSP = "sandbox; default-src 'none'; img-src 'self' data:; media-src 'self'; " +
		"style-src 'unsafe-inline'; frame-ancestors 'self'"
	PDFCSP = "default-src 'none'; object-src 'self'; style-src 'unsafe-inline'; " +
		"img-src 'self' data:; frame-ancestors 'self'"
)

func lookPath(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

var (
	httpURLRe   = regexp.MustCompile(`(?i)\Ahttps?://`)
	driveRe     = regexp.MustCompile(`\A/[A-Za-z]:/`)
	safeNameRe  = regexp.MustCompile(`[^A-Za-z0-9._ -]`)
	rangeRe     = pytext.MustCompile(`\Abytes=(\d*)-(\d*)\z`)
	previewOnly = map[string]bool{"application/pdf": true, "text/plain": true, "text/markdown": true,
		"text/csv": true, "application/json": true}
)

// within is pathx.Within.
func within(path, root string) bool { return pathx.Within(path, root) }

// resolveMedia is App.resolve_media.
func (c *reqCtx) resolveMedia(p *project.Project, idText string) (string, string, error) {
	id, err := pytext.Int(idText)
	if err != nil {
		return "", "", &pyerr.ValueError{Message: err.Error()}
	}
	row, ok, err := p.Store.MediaRow(c.ctx, id)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", "", apiErr(404, "Media not found", "")
	}
	url := row.Str("url")
	if httpURLRe.MatchString(url) {
		return "", "", apiErr(302, url, "")
	}
	var path string
	if strings.HasPrefix(strings.ToLower(url), "file://") {
		raw := pyurl.Unquote(pyurl.Parse(url).Path)
		if driveRe.MatchString(raw) {
			raw = raw[1:]
		}
		path = raw
	} else {
		path = url
		if !filepath.IsAbs(path) {
			path = filepath.Join(p.Path, path)
		}
	}
	path = project.Resolve(expandHome(path))
	allowed := append([]string{p.Path}, c.app.mediaRoots()...)
	inside := false
	for _, root := range allowed {
		inside = inside || within(path, root)
	}
	if !inside {
		return "", "", apiErr(403, "Local media outside the project folder is not served. "+
			"Start the server with --media-root to allow it.", "")
	}
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return "", "", apiErr(404, "Media file not found: "+filepath.Base(path), "")
	}
	return path, guessType(filepath.Base(path)), nil
}

func (a *App) mediaRoots() []string {
	out := make([]string, len(a.Cfg.MediaRoots))
	for i, r := range a.Cfg.MediaRoots {
		out[i] = project.Resolve(r)
	}
	return out
}

func (c *reqCtx) mediaRaw(idText string) error {
	p, err := c.require("read")
	if err != nil {
		return err
	}
	path, ctype, err := c.resolveMedia(p, idText)
	if err != nil {
		return err
	}
	d := c.query.First("download", "")
	return c.sendFile(path, ctype, d == "1" || d == "true")
}

// fileHeaders is Handler._file_headers.
func fileHeaders(name, ctype string, download bool) (string, headerList) {
	safe := safeNameRe.ReplaceAllString(name, "_")
	if safe == "" {
		safe = "file"
	}
	inline := !download && (strings.HasPrefix(ctype, "image/") || strings.HasPrefix(ctype, "video/") ||
		strings.HasPrefix(ctype, "audio/") || previewOnly[ctype])
	if !inline {
		ctype = "application/octet-stream"
	} else if ctype == "text/plain" || ctype == "text/markdown" || ctype == "text/csv" {
		ctype += "; charset=utf-8"
	}
	disposition := "attachment"
	if inline {
		disposition = "inline"
	}
	headers := headerList{{"Content-Disposition", disposition + `; filename="` + safe + `"`}, {"Accept-Ranges", "bytes"}}
	if ctype == "application/pdf" {
		headers = append(headers, [2]string{"Content-Security-Policy", PDFCSP}, [2]string{"X-Frame-Options", "SAMEORIGIN"})
	} else {
		headers = append(headers, [2]string{"Content-Security-Policy", FileCSP})
	}
	return ctype, headers
}

// sendFile is Handler._send_file: one byte range at most.
func (c *reqCtx) sendFile(path, ctype string, download bool) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	size := fi.Size()
	ctype, headers := fileHeaders(filepath.Base(path), ctype, download)
	// Validators: the browser (or the service worker's media cache) keeps the
	// bytes and revalidates; authorization already ran for this request.
	etag := `"f` + strconv.FormatInt(fi.ModTime().UnixNano(), 16) + "-" + strconv.FormatInt(size, 16) + `"`
	headers = append(headers, [2]string{"ETag", etag},
		[2]string{"Last-Modified", fi.ModTime().UTC().Format("Mon, 02 Jan 2006 15:04:05") + " GMT"})
	if !download {
		headers = append(headers, [2]string{"Cache-Control", "private, no-cache"})
	}
	if c.header("Range") == "" && etagMatches(c.header("If-None-Match"), etag) {
		c.notModified(headers)
		return nil
	}
	start, end, status := int64(0), size-1, 200
	rng := strings.ReplaceAll(c.header("Range"), " ", "")
	if m := rangeRe.FindStringSubmatch(rng); rng != "" && m != nil && (m[1] != "" || m[2] != "") {
		var first, last int64
		if m[1] != "" {
			first, _ = pytext.Int(m[1])
			last = size - 1
			if m[2] != "" {
				n, _ := pytext.Int(m[2])
				last = min(n, size-1)
			}
		} else {
			suffix, _ := pytext.Int(m[2])
			if suffix != 0 {
				first, last = max(0, size-suffix), size-1
			} else {
				first, last = size, size-1
			}
		}
		if first >= size || first > last {
			h := c.w.Header()
			h.Set("Content-Type", "text/plain; charset=utf-8")
			h.Set("Content-Length", "0")
			h.Set("Content-Range", "bytes */"+strconv.FormatInt(size, 10))
			securityHeaders(h, headerList{{"Accept-Ranges", "bytes"}, {"Content-Security-Policy", FileCSP}})
			c.w.WriteHeader(416)
			return nil
		}
		start, end, status = first, last, 206
	}
	length := max(0, end-start+1)
	h := c.w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Length", strconv.FormatInt(length, 10))
	if status == 206 {
		h.Set("Content-Range", "bytes "+strconv.FormatInt(start, 10)+"-"+strconv.FormatInt(end, 10)+"/"+strconv.FormatInt(size, 10))
	}
	securityHeaders(h, headers)
	c.w.WriteHeader(status)
	if c.r.Method == "HEAD" || length == 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil // headers are out; nothing useful left to report
	}
	defer f.Close()
	f.Seek(start, io.SeekStart) //nolint:errcheck
	io.CopyN(c.w, f, length)    //nolint:errcheck // client may abort a seek
	return nil
}
