package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"arkstudio/go/internal/platform/pyerr"

	"arkstudio/go/internal/blackbox"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/project"
)

// Black box snapshots: App.__init__ wiring and the /api/blackbox routes.

var reBlackboxRestore = pyRe(`^/api/blackbox/([^/]+)/restore$`, false)

// bbProject adapts a project to blackbox.Project.
type bbProject struct {
	a *App
	p *project.Project
}

func (b bbProject) ID() string   { return b.p.ID() }
func (b bbProject) Name() string { return b.p.Name() }
func (b bbProject) Folder() string {
	b.p.Store.FlushManifest() // the snapshot copies project.json from disk
	return b.p.Path
}
func (b bbProject) Revision(ctx context.Context) (string, error) {
	return b.p.Store.Revision(ctx)
}
func (b bbProject) Reopen(replace func() error) error {
	if err := b.a.Projects.Reopen(b.p, replace); err != nil {
		return err
	}
	// Cached client snapshots of the replaced database are not deltas of
	// this one (GET /api/changes resets them).
	_, err := b.p.Store.ResetChangeLogID(context.Background())
	return err
}

// setupBlackbox mirrors App.__init__: ARK_BLACKBOX, ARK_BLACKBOX_INTERVAL,
// ARK_BLACKBOX_DIR, the startup snapshot and the periodic loop.
func (a *App) setupBlackbox(ctx context.Context) {
	v := strings.ToLower(pytext.Strip(os.Getenv("ARK_BLACKBOX")))
	if v == "" {
		v = "1"
	}
	enabled := v != "0" && v != "false" && v != "no" && v != "off"
	interval := int64(900)
	if raw := os.Getenv("ARK_BLACKBOX_INTERVAL"); raw != "" {
		if n, err := pytext.Int(raw); err == nil {
			interval = n
		}
	}
	dir := os.Getenv("ARK_BLACKBOX_DIR")
	if dir == "" {
		dir = filepath.Join(filepath.Dir(a.Root), "blackbox")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		dir = filepath.Join(a.Root, ".blackbox")
	}
	a.Blackbox = blackbox.New(project.Resolve(expandHome(dir)), interval, enabled, func() blackbox.Project {
		a.mu.Lock()
		p := a.project
		a.mu.Unlock()
		if p == nil {
			return nil
		}
		return bbProject{a, p}
	})
	if enabled && a.project != nil {
		bp := bbProject{a, a.project}
		if len(a.Blackbox.List(bp)) == 0 {
			a.Blackbox.Snapshot(ctx, bp, "startup", true) //nolint:errcheck // never blocks startup
		}
		a.Blackbox.Start()
	}
}

func blackboxErr(err error) error {
	var ve *blackbox.ValueError
	var oe *blackbox.OSError
	switch e := err.(type) {
	case *blackbox.ValueError:
		ve = e
		return &pyerr.ValueError{Message: ve.Message}
	case *blackbox.OSError:
		oe = e
		return &pyerr.InternalError{Message: oe.Message}
	}
	return err
}

// blackboxGet is GET /api/blackbox.
func (c *reqCtx) blackboxGet() error {
	p, err := c.require("read")
	if err != nil {
		return err
	}
	c.ok(pyjson.NewObject("enabled", c.app.Blackbox.Enabled, "snapshots", c.app.Blackbox.List(bbProject{c.app, p})))
	return nil
}

// blackboxPost handles POST /api/blackbox and /api/blackbox/<id>/restore.
func (c *reqCtx) blackboxPost(path string, body *pyjson.Object) (bool, error) {
	if path == "/api/blackbox" {
		p, err := c.require("admin")
		if err != nil {
			return true, err
		}
		reason := pytext.Prefix(pytext.Strip(strOrFalsy(body.Value("reason"), "manual")), 40)
		if reason == "" {
			reason = "manual"
		}
		snap, err := c.app.Blackbox.Snapshot(c.ctx, bbProject{c.app, p}, reason, true)
		if err != nil {
			return true, blackboxErr(err)
		}
		c.ok(pyjson.NewObject("snapshot", snap))
		return true, nil
	}
	m := reBlackboxRestore.FindStringSubmatch(path)
	if m == nil {
		return false, nil
	}
	p, err := c.require("admin")
	if err != nil {
		return true, err
	}
	c.app.restoreMu.Lock()
	result, err := c.app.Blackbox.Restore(c.ctx, bbProject{c.app, p}, m[1])
	c.app.restoreMu.Unlock()
	if err != nil {
		return true, blackboxErr(err)
	}
	payload, err := c.projectPayload(p)
	if err != nil {
		return true, err
	}
	c.ok(pyjson.NewObject("restored", result, "project", payload))
	return true, nil
}

// strOrFalsy is str(value or default).
func strOrFalsy(v any, def string) string {
	if !pyjson.Truthy(v) {
		return def
	}
	return pyjson.Str(v)
}
