package server

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"

	"arkstudio/go/internal/project"
	"arkstudio/go/internal/pytext"
	"arkstudio/go/internal/pyurl"
	"arkstudio/go/internal/store"
)

// ZIP exports of GET /api/export: format=bundle (Handler._send_markdown_bundle)
// and format=project (Handler._send_project_archive).

// streamZip is Handler._stream_zip.
func (c *reqCtx) streamZip(path, name string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	h := c.w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	securityHeaders(h, headerList{
		{"Content-Disposition", `attachment; filename="` + name + `"; filename*=UTF-8''` + pyurl.Quote(name, "/")},
		{"Content-Security-Policy", FileCSP},
		{"Cache-Control", "no-store"},
	})
	c.w.WriteHeader(http.StatusOK)
	if c.r.Method != http.MethodHead {
		io.Copy(c.w, f) //nolint:errcheck // client went away
	}
	return nil
}

func (c *reqCtx) sendMarkdownBundle(p *project.Project) error {
	refs, err := store.ParseRefs(c.query.First("units", ""))
	if err != nil {
		return bundleErr(err)
	}
	depth := int64(1)
	if raw := c.query.First("depth", "1"); raw != "" {
		n, err := pytext.Int(raw)
		if err != nil {
			var ie *pytext.IntError
			if errors.As(err, &ie) {
				return apiErr(400, err.Error(), "")
			}
			n = 1 << 40
		}
		depth = n
	}
	bundle := p.Store.NewBundle(p.Path, p.Name(), p.Store.LocalMediaResolver(p.Path, c.app.Cfg.MediaRoots))
	if err := bundle.Collect(c.ctx, refs, depth); err != nil {
		return bundleErr(err)
	}
	if check := c.query.First("check", ""); check == "1" || check == "true" {
		summary, err := bundle.Summary()
		if err != nil {
			return err
		}
		c.ok(summary)
		return nil
	}
	tmp, err := os.CreateTemp("", "ark-bundle-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	manifest, err := bundle.WriteZip(tmp)
	tmp.Close()
	if err != nil {
		return bundleErr(err)
	}
	return c.streamZip(tmp.Name(), manifest.Value("root").(string)+".zip")
}

func bundleErr(err error) error {
	var be *store.BundleError
	var ve *store.ValueError
	switch {
	case errors.As(err, &be):
		return apiErr(400, be.Message, "")
	case errors.As(err, &ve):
		return apiErr(400, ve.Message, "")
	}
	return err
}

func (c *reqCtx) sendProjectArchive(p *project.Project, name string) error {
	include := false
	if s := c.query.First("sessions", ""); s == "1" || s == "true" || s == "yes" {
		include = true
		c.syncSessionMirrors(p)
	}
	scratch, err := os.MkdirTemp("", "ark-project-export-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	archive := scratch + string(os.PathSeparator) + "project.zip"
	f, err := os.Create(archive)
	if err != nil {
		return err
	}
	snapshot, err := os.MkdirTemp("", "ark-project-snapshot-")
	if err != nil {
		f.Close()
		return err
	}
	defer os.RemoveAll(snapshot)
	_, err = project.WriteArchiveSessions(c.ctx, p, f, snapshot, include)
	f.Close()
	if err != nil {
		return apiErr(400, "Could not export project: "+err.Error(), "")
	}
	return c.streamZip(archive, name+".ark.zip")
}
