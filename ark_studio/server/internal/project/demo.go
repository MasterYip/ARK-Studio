package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/platform/textfile"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/store"
)

// The per-user tutorial project (arkstudio/demo.py).

const (
	DemoID   = "ark-demo"
	DemoName = "ARK Studio Demo"
	// DemoDescription is demo.DEMO_DESCRIPTION.
	DemoDescription = "A disposable, hands-on tutorial: one illustrated chapter per workspace " +
		"(Home, Research, Map, Execute, Output, Editor) plus agents, skills, " +
		"settings and mobile, all built around one small example study. " +
		"Edits are local to this visit and reset when you switch projects."
)

var demoMu sync.Mutex

// DemoTemplatePath is demo.template_path.
func DemoTemplatePath(root string) string { return filepath.Join(Resolve(root), ".demo-template") }

// DemoUserPath is demo.user_path.
func DemoUserPath(root, username string) string {
	if username == "" {
		username = "local"
	}
	sum := sha256.Sum256([]byte(username))
	return filepath.Join(Resolve(root), ".demo", hex.EncodeToString(sum[:])[:16])
}

// DemoFolderVersion is demo.folder_version (0, false = None).
func DemoFolderVersion(path string) (int64, bool) {
	raw, err := os.ReadFile(filepath.Join(path, "project.json"))
	if err != nil {
		return 0, false
	}
	v, err := pyjson.Loads(strings.ReplaceAll(string(raw), "\r\n", "\n"))
	if err != nil {
		return 0, false
	}
	o, ok := v.(*pyjson.Object)
	if !ok {
		return 0, false
	}
	n, ok := o.Value("demo_version").(int64)
	return n, ok
}

func demoIsCurrent(path string) bool {
	n, ok := DemoFolderVersion(path)
	return ok && n == DemoVersion
}

func demoManifest() *pyjson.Object {
	now := clock.UTCNow()
	return pyjson.NewObject("format", "ark-studio-project", "version", int64(1), "id", DemoID,
		"name", DemoName, "description", DemoDescription, "created", now, "updated", now,
		"accent", "#2563eb", "demo", true, "demo_version", int64(DemoVersion))
}

// templateBuiltToday reports a tutorial template seeded today (Go-only):
// its dates are relative to the day it was built (seedAnchors), so a
// template from an earlier day is rebuilt and the tutorial never shows a
// stale deadline.  With the recorded dates (SeedDatesFixed) any day will do.
func templateBuiltToday(path string) bool {
	if SeedDatesFixed.Load() {
		return true
	}
	raw, err := os.ReadFile(filepath.Join(path, "project.json"))
	if err != nil {
		return false
	}
	v, err := pyjson.Loads(strings.ReplaceAll(string(raw), "\r\n", "\n"))
	if err != nil {
		return false
	}
	o, ok := v.(*pyjson.Object)
	if !ok {
		return false
	}
	created, _ := o.Value("created").(string)
	return len(created) >= 10 && created[:10] == clock.Now().UTC().Format("2006-01-02")
}

// buildDemoTemplate is demo._build_template.
func buildDemoTemplate(ctx context.Context, root string) (string, error) {
	template := DemoTemplatePath(root)
	if demoIsCurrent(template) && templateBuiltToday(template) {
		return template, nil
	}
	root = Resolve(root)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	temp, err := os.MkdirTemp(root, ".demo-build-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(temp)
	for _, sub := range []string{"assets", "exports", "logs"} {
		if err := os.MkdirAll(filepath.Join(temp, sub), 0o755); err != nil {
			return "", err
		}
	}
	manifest := demoManifest()
	if err := os.WriteFile(filepath.Join(temp, "project.json"), textfile.Encode(string(pyjson.DumpsIndent(manifest, 2))+"\n"), 0o644); err != nil {
		return "", err
	}
	st, err := store.Open(temp, store.NewManifest(manifest), 2)
	if err != nil {
		return "", err
	}
	err = func() error {
		if err := st.SetMeta(ctx, "name", DemoName); err != nil {
			return err
		}
		if err := st.SetMeta(ctx, "created", pyjson.Str(manifest.Value("created"))); err != nil {
			return err
		}
		if err := CopyTutorialAssets(temp); err != nil {
			return err
		}
		return ApplySeed(ctx, st, "tutorial")
	}()
	st.Close()
	if err != nil {
		return "", err
	}
	stampDemoVersion(temp)
	if _, err := os.Stat(template); err == nil {
		os.RemoveAll(template) //nolint:errcheck
	}
	if err := os.Rename(temp, template); err != nil {
		return "", err
	}
	return template, nil
}

// stampDemoVersion is demo._stamp_version.
func stampDemoVersion(path string) {
	file := filepath.Join(path, "project.json")
	raw, err := os.ReadFile(file)
	if err != nil {
		return
	}
	v, err := pyjson.Loads(strings.ReplaceAll(string(raw), "\r\n", "\n"))
	if err != nil {
		return
	}
	o, ok := v.(*pyjson.Object)
	if !ok {
		return
	}
	if n, ok := o.Value("demo_version").(int64); ok && n == DemoVersion {
		return
	}
	o.Set("demo_version", int64(DemoVersion))
	os.WriteFile(file, textfile.Encode(string(pyjson.DumpsIndent(o, 2))+"\n"), 0o644) //nolint:errcheck
}

// EnsureDemo is demo.ensure_demo: the user's tutorial folder, copied from a
// current template when missing, outdated or reset.
func EnsureDemo(ctx context.Context, root, username string, reset bool) (string, error) {
	demoMu.Lock()
	template, err := buildDemoTemplate(ctx, root)
	demoMu.Unlock()
	if err != nil {
		return "", err
	}
	target := DemoUserPath(root, username)
	demoMu.Lock()
	defer demoMu.Unlock()
	if _, err := os.Stat(target); err == nil && (reset || !demoIsCurrent(target)) {
		os.RemoveAll(target) //nolint:errcheck
	}
	if fi, err := os.Stat(filepath.Join(target, "project.json")); err != nil || !fi.Mode().IsRegular() {
		if _, err := os.Stat(target); err == nil {
			os.RemoveAll(target) //nolint:errcheck
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		if err := copyTree(template, target); err != nil {
			return "", err
		}
	}
	return target, nil
}

// copyTree is shutil.copytree (copy2: data and modification times).
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if err := copyFile(path, target); err != nil {
			return err
		}
		if info, err := d.Info(); err == nil {
			os.Chtimes(target, info.ModTime(), info.ModTime()) //nolint:errcheck
		}
		return nil
	})
}

// Drop closes and forgets a cached project (before its folder is replaced
// or deleted; Windows keeps SQLite files open otherwise).
func (m *Manager) Drop(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := Resolve(path)
	if p, ok := m.cache[key]; ok {
		p.Store.Close()
		delete(m.cache, key)
		for ref, q := range m.byRef {
			if q == p {
				delete(m.byRef, ref)
			}
		}
	}
}

// Delete is ProjectManager.delete(project_id).
func (m *Manager) Delete(projectID string) error {
	for _, summary := range m.ListProjects() {
		if summary.Value("id") != projectID {
			continue
		}
		path := pyjson.Str(summary.Value("path"))
		if Resolve(filepath.Dir(path)) != Resolve(m.Root) {
			return &Error{"refusing to delete outside the project root"}
		}
		m.Drop(path)
		return os.RemoveAll(path)
	}
	return &Error{"project not found: " + projectID}
}

// Reopen closes p's store, runs replace (which swaps database files), and
// opens a fresh store on the same manifest (blackbox restore).
func (m *Manager) Reopen(p *Project, replace func() error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p.Store.Close()
	err := replace()
	st, oerr := store.Open(p.Path, p.Manifest, m.Readers)
	if oerr != nil {
		return oerr
	}
	p.Store = st
	return err
}
