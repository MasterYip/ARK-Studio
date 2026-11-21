// Package project ports arkstudio/projects.py: discovery of <name>.ark
// folders under the project root, project.json manifests, the agent token
// file and Project.summary().
package project

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"arkstudio/go/internal/platform/textfile"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/settings"
	"arkstudio/go/internal/store"
)

// Error is ProjectError (HTTP 400).
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

// Resolve is Path(p).resolve(): absolute, symlinks and 8.3 names expanded
// where the path exists.
func Resolve(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	// resolve(strict=False): resolve the existing prefix, keep the rest.
	dir, base := filepath.Split(abs)
	if dir != abs && dir != "" && base != "" {
		return filepath.Join(Resolve(strings.TrimRight(dir, `\/`)), base)
	}
	return abs
}

// ReadManifest is db.read_manifest.
func ReadManifest(folder string) (*pyjson.Object, bool) {
	raw, err := os.ReadFile(filepath.Join(folder, "project.json"))
	if err != nil {
		return nil, false
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	v, err := pyjson.Loads(text)
	if err != nil {
		return nil, false
	}
	o, ok := v.(*pyjson.Object)
	if !ok {
		return nil, false
	}
	if f, has := o.Get("format"); has && f != nil && f != "ark-studio-project" {
		return nil, false
	}
	return o, true
}

func getDefault(o *pyjson.Object, key string, def any) any {
	if v, ok := o.Get(key); ok {
		return v
	}
	return def
}

func stem(name string) string {
	if i := strings.LastIndex(name, "."); i > 0 {
		return name[:i]
	}
	return name
}

// Summary is one list_projects() entry.
func listEntry(folder string, m *pyjson.Object) *pyjson.Object {
	name := filepath.Base(folder)
	return pyjson.NewObject("id", getDefault(m, "id", name), "name", getDefault(m, "name", stem(name)),
		"description", getDefault(m, "description", ""), "path", folder, "folder", name,
		"created", m.Value("created"), "updated", m.Value("updated"),
		"template", getDefault(m, "template", "blank"), "demo", pyjson.Truthy(m.Value("demo")),
		"accent", getDefault(m, "accent", "#2563eb"))
}

// Project is one open project.
type Project struct {
	Path     string
	Manifest *store.Manifest
	Store    *store.Store
}

// ID is Project.id.
func (p *Project) ID() string {
	if v := p.Manifest.Get("id"); pyjson.Truthy(v) {
		return pyjson.Str(v)
	}
	return filepath.Base(p.Path)
}

// Name is Project.name.
func (p *Project) Name() string {
	if v := p.Manifest.Get("name"); pyjson.Truthy(v) {
		return pyjson.Str(v)
	}
	return stem(filepath.Base(p.Path))
}

// Summary is Project.summary().
func (p *Project) Summary() (*pyjson.Object, error) {
	m := p.Manifest.Snapshot()
	s, err := settings.ProjectSettings(m)
	if err != nil {
		return nil, err
	}
	return pyjson.NewObject("id", p.ID(), "name", p.Name(), "description", getDefault(m, "description", ""),
		"path", p.Path, "folder", filepath.Base(p.Path), "created", m.Value("created"), "updated", m.Value("updated"),
		"template", getDefault(m, "template", "blank"), "demo", pyjson.Truthy(m.Value("demo")),
		"accent", getDefault(m, "accent", "#2563eb"), "settings", s), nil
}

// Manager is ProjectManager (no creation: the Go port opens projects only).
type Manager struct {
	Root       string
	AgentToken string
	Readers    int

	mu    sync.Mutex
	cache map[string]*Project // resolved path -> project
	byRef map[string]*Project // Open(ref) argument -> project

	// Project folders besides Root and the folder for new projects
	// (storage.go; set from the organization option at start and when an
	// admin changes it).
	fmu    sync.RWMutex
	extra  []string
	newDir string
}

// NewManager is ProjectManager.__init__(root, seed_demo=False).
func NewManager(root string, readers int) (*Manager, error) {
	root = Resolve(root)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	m := &Manager{Root: root, Readers: readers, cache: map[string]*Project{}, byRef: map[string]*Project{}}
	m.AgentToken = m.ensureToken()
	return m, nil
}

func tokenURLSafe(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func (m *Manager) ensureToken() string {
	path := filepath.Join(m.Root, ".agent-token")
	if raw, err := os.ReadFile(path); err == nil {
		if t := pytext.Strip(strings.ReplaceAll(string(raw), "\r\n", "\n")); pytext.Len(t) >= 24 {
			return t
		}
	}
	token := tokenURLSafe(32)
	os.WriteFile(path, textfile.Encode(token+"\n"), 0o644) //nolint:errcheck // Python ignores OSError
	return token
}

// ListProjects is ProjectManager.list_projects.
func (m *Manager) ListProjects() []*pyjson.Object {
	type item struct {
		path  string
		mtime int64
	}
	var dirs []item
	for _, folder := range m.Folders() {
		entries, _ := os.ReadDir(folder)
		for _, e := range entries {
			info, err := e.Info()
			mt := int64(0)
			if err == nil {
				mt = info.ModTime().UnixNano()
			}
			dirs = append(dirs, item{filepath.Join(folder, e.Name()), mt})
		}
	}
	sort.SliceStable(dirs, func(i, j int) bool { return dirs[i].mtime > dirs[j].mtime })
	var out []*pyjson.Object
	for _, d := range dirs {
		name := filepath.Base(d.path)
		if fi, err := os.Stat(d.path); err != nil || !fi.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		manifest, ok := ReadManifest(d.path)
		if !ok {
			// project.json is rewritten (tmp + rename) on every write; on
			// Windows a concurrent read can hit a sharing violation.  Python
			// then skips the project for that request (and may fall back to the
			// tutorial).  An already open project keeps its in-memory manifest.
			m.mu.Lock()
			open := m.cache[d.path]
			m.mu.Unlock()
			if open == nil {
				continue
			}
			manifest = open.Manifest.Snapshot()
		}
		out = append(out, listEntry(d.path, manifest))
	}
	key := func(o *pyjson.Object) string {
		for _, k := range []string{"updated", "created"} {
			if v := o.Value(k); pyjson.Truthy(v) {
				return pyjson.Str(v)
			}
		}
		return ""
	}
	sort.SliceStable(out, func(i, j int) bool { return key(out[i]) > key(out[j]) })
	return out
}

// Cached is the already open project at path (as listed by ListProjects),
// or nil; it never opens one.
func (m *Manager) Cached(path string) *Project {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.cache[path]; ok {
		return p
	}
	return m.byRef[path]
}

// Open is ProjectManager.open(ref) with a per-path cache (App._cached_project).
func (m *Manager) Open(ref string) (*Project, error) {
	ref = pytext.Strip(ref)
	if ref == "" {
		return nil, &Error{"project path is required"}
	}
	m.mu.Lock()
	if p, ok := m.byRef[ref]; ok {
		m.mu.Unlock()
		return p, nil // resolving a path costs several syscalls on Windows
	}
	m.mu.Unlock()
	path := ref
	if !filepath.IsAbs(path) {
		path = filepath.Join(m.Root, ref)
	}
	path = Resolve(path)
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.cache[path]; ok {
		m.byRef[ref] = p
		return p, nil
	}
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		return nil, &Error{fmt.Sprintf("project folder not found: %s", path)}
	}
	manifest, ok := ReadManifest(path)
	if !ok {
		return nil, &Error{fmt.Sprintf("not an ARK project (project.json missing): %s", path)}
	}
	shared := store.NewManifest(manifest)
	st, err := store.Open(path, shared, m.Readers)
	if err != nil {
		return nil, err
	}
	p := &Project{Path: path, Manifest: shared, Store: st}
	m.cache[path] = p
	m.byRef[ref] = p
	return p, nil
}

// Close closes every cached project.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, p := range m.cache {
		p.Store.Close()
		delete(m.cache, k)
	}
	m.byRef = map[string]*Project{}
}
