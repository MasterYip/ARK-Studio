package project

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pytext"
	"arkstudio/go/internal/settings"
	"arkstudio/go/internal/store"
)

// Project creation and seeding: ProjectManager.create, seed.py and the
// tutorial seed (demo._seed), replayed from tests/python/contract/gen_seeds.py.

// DemoAssetsDir is the repository's demo-assets folder (seed.py and demo.py
// read it beside the arkstudio package).  The server sets it from the web
// root; "" means the assets are unavailable and the seeds skip them.
var DemoAssetsDir string

var (
	slugRe   = regexp.MustCompile(`[^a-z0-9]+`)
	dashesRe = regexp.MustCompile(`-{2,}`)
)

// Slugify is projects._slugify.
func Slugify(name string) string {
	slug := strings.Trim(slugRe.ReplaceAllString(pytext.Lower(name), "-"), "-")
	slug = strings.Trim(pytext.Prefix(dashesRe.ReplaceAllString(slug, "-"), 48), "-")
	if slug == "" {
		return "project"
	}
	return slug
}

// ReplayIDs makes NewID deterministic (differential replay only, see
// tests/python/contract/replayclock.py).
var ReplayIDs atomic.Bool

var replaySeq atomic.Int64

// NewID is projects._new_id.
func NewID() string {
	if ReplayIDs.Load() {
		return fmt.Sprintf("prj_r%09d", replaySeq.Add(1))
	}
	b := make([]byte, 5)
	rand.Read(b) //nolint:errcheck
	return "prj_" + hex.EncodeToString(b)
}

func readme(name string) string {
	return "# " + name + "\n\n" +
		"This is an ARK Studio project folder. Open it from the workbench's " +
		"project switcher, or run `python -m arkstudio serve --root <parent-folder>`.\n\n" +
		"- `project.json` — identity and display metadata\n" +
		"- `ark.sqlite3` — units, tags, links, media references, activity\n" +
		"- `assets/` — local media (optional)\n" +
		"- `exports/` — generated exports\n" +
		"- `logs/activity.jsonl` — append-only activity mirror\n\n" +
		"Copy the whole folder to back up or move the project.\n"
}

// Create is ProjectManager.create.
func (m *Manager) Create(ctx context.Context, name, description, template string, demo bool, accent string) (*Project, error) {
	name = pytext.Strip(name)
	if name == "" {
		return nil, &Error{"project name is required"}
	}
	if pytext.Len(name) > 80 {
		return nil, &Error{"project name is too long"}
	}
	if template != "blank" && template != "paper" && template != "demo" {
		template = "blank"
	}
	if accent == "" {
		accent = "#2563eb"
	}
	base := Slugify(name)
	folder := filepath.Join(m.Root, base+".ark")
	for n := 2; ; n++ {
		if _, err := os.Lstat(folder); os.IsNotExist(err) {
			break
		}
		folder = filepath.Join(m.Root, fmt.Sprintf("%s-%d.ark", base, n))
	}
	if err := os.MkdirAll(m.Root, 0o755); err != nil {
		return nil, err
	}
	if err := os.Mkdir(folder, 0o755); err != nil {
		return nil, err
	}
	for _, sub := range []string{"assets", "exports", "logs"} {
		if err := os.MkdirAll(filepath.Join(folder, sub), 0o755); err != nil {
			return nil, err
		}
	}
	now := store.UTCNow()
	manifest := pyjson.NewObject("format", "ark-studio-project", "version", int64(1), "id", NewID(),
		"name", name, "description", description, "created", now, "updated", now, "template", template,
		"demo", demo, "accent", accent, "settings", pyjson.NewObject("graph", settings.NormalizeGraph(nil)))
	if err := store.WriteManifest(folder, pyjson.DumpsIndent(manifest, 2)); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(folder, "README.md"), store.TextFile(readme(name)), 0o644); err != nil {
		return nil, err
	}
	shared := store.NewManifest(manifest)
	st, err := store.Open(folder, shared, m.Readers)
	if err != nil {
		return nil, err
	}
	p := &Project{Path: Resolve(folder), Manifest: shared, Store: st}
	if err := st.SetMeta(ctx, "name", name); err != nil {
		return nil, err
	}
	if err := st.SetMeta(ctx, "created", now); err != nil {
		return nil, err
	}
	if err := st.LogActivity(ctx, "system", "project.create", nil,
		pyjson.NewObject("name", name, "template", template, "demo", demo)); err != nil {
		return nil, err
	}
	switch {
	case template == "demo" || demo:
		copyDemoAssets(folder)
		err = ApplySeed(ctx, st, "demo")
	case template == "paper":
		err = ApplySeed(ctx, st, "paper")
	default:
		err = ApplySeed(ctx, st, "blank")
	}
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.cache[p.Path] = p
	m.mu.Unlock()
	return p, nil
}

// ApplySeed replays one recorded seed (gen_seeds.py) through the Go store.
func ApplySeed(ctx context.Context, st *store.Store, key string) error {
	v, err := pyjson.Loads(seedOps[key])
	if err != nil {
		return err
	}
	for i, raw := range v.([]any) {
		op := raw.(*pyjson.Object)
		args, _ := op.Value("args").(*pyjson.Object)
		arg := func(name string, def any) any {
			if v, ok := args.Get(name); ok {
				return v
			}
			return def
		}
		actor := pyjson.Str(arg("actor", "human:ui"))
		var err error
		switch op.Value("op") {
		case "create_unit":
			data, _ := arg("data", nil).(*pyjson.Object)
			_, err = st.CreateUnit(ctx, data, actor, store.Who{})
		case "create_link":
			_, err = st.CreateLink(ctx, arg("source", nil), arg("target", nil), arg("relation", nil), arg("note", ""), actor)
		case "add_media":
			_, err = st.AddMedia(ctx, arg("ref", nil), arg("url", nil), arg("kind", "link"), arg("caption", ""), actor)
		case "design_topic":
			body, _ := arg("body", nil).(*pyjson.Object)
			_, err = st.DesignTopic(ctx, arg("ref", nil), body, actor)
		case "update_unit":
			patch, _ := arg("patch", nil).(*pyjson.Object)
			_, err = st.UpdateUnit(ctx, arg("ref", nil), patch, actor, store.Who{})
		case "set_meta":
			err = st.SetMeta(ctx, pyjson.Str(arg("key", "")), pyjson.Str(arg("value", "")))
		default:
			return fmt.Errorf("seed %s: unknown op %v", key, op.Value("op"))
		}
		raises := op.Has("raises")
		if err != nil && !raises {
			return fmt.Errorf("seed %s op %d (%v): %w", key, i, op.Value("op"), err)
		}
		if err == nil && raises {
			return fmt.Errorf("seed %s op %d (%v): Python raised %v, Go did not", key, i, op.Value("op"), op.Value("raises"))
		}
	}
	return nil
}

// copyDemoAssets is seed._copy_demo_assets (shutil.copy2: data and mtime).
func copyDemoAssets(folder string) {
	if DemoAssetsDir == "" {
		return
	}
	assets := filepath.Join(folder, "assets")
	os.MkdirAll(assets, 0o755) //nolint:errcheck
	for _, name := range []string{"workbench-overview.png", "mind-graph.png"} {
		source := filepath.Join(DemoAssetsDir, name)
		info, err := os.Stat(source)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		target := filepath.Join(assets, name)
		if _, err := os.Stat(target); err == nil {
			continue
		}
		if copyFile(source, target) == nil {
			os.Chtimes(target, info.ModTime(), info.ModTime()) //nolint:errcheck
		}
	}
}

// CopyTutorialAssets is demo._copy_assets: demo-assets/tutorial into
// <project>/assets/tutorial (shutil.copyfile: data only).
func CopyTutorialAssets(project string) error {
	if DemoAssetsDir == "" {
		return nil
	}
	src := filepath.Join(DemoAssetsDir, "tutorial")
	if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
		return nil
	}
	var files []string
	filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error { //nolint:errcheck
		if err == nil && d.Type().IsRegular() {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	for _, f := range files {
		rel, _ := filepath.Rel(src, f)
		target := filepath.Join(project, "assets", "tutorial", rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := copyFile(f, target); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(target)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
