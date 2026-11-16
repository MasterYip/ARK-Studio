package agentdocs

import (
	"embed"
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:generate go run ../../cmd/docsgen

//go:embed all:dist
var embedded embed.FS

// ManifestFile inside dist records each embedded doc's last-change time.
const ManifestFile = ".ark-docs.json"

// Manifest is the dist/.ark-docs.json written by cmd/docsgen.
type Manifest struct {
	Updated map[string]string `json:"updated"` // docs/-relative path -> RFC 3339 UTC
}

// Doc is one served Markdown file.
type Doc struct {
	Path    string // docs/-relative slash path, e.g. "guide/topics.md"
	Section string // first path component
	Title   string
	Summary string
	Updated string // RFC 3339 UTC ("" if unknown)
	Text    string // LF line endings
}

// Bytes is the size of the served Markdown.
func (d *Doc) Bytes() int { return len(d.Text) }

// Set is the documentation agents can read.
type Set struct {
	Source string // "embedded", "disk" or "none"
	Docs   []*Doc // index order: section, then path
	byPath map[string]*Doc
}

func normalizeText(b []byte) string {
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	return strings.TrimPrefix(s, "\uFEFF")
}

// Load reads every included doc of fsys (a docs/ root).  updated maps a path
// to its change time; nil or a missing entry falls back to the file mtime.
func Load(fsys fs.FS, source string, updated map[string]string) (*Set, error) {
	s := &Set{Source: source, byPath: map[string]*Doc{}}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !Included(p) {
			return nil
		}
		raw, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		text := normalizeText(raw)
		doc := &Doc{Path: p, Section: p[:strings.IndexByte(p, '/')], Title: Title(text, p),
			Summary: Summary(text), Updated: updated[p], Text: text}
		if doc.Updated == "" {
			if info, err := d.Info(); err == nil && !info.ModTime().IsZero() {
				doc.Updated = info.ModTime().UTC().Format(time.RFC3339)
			}
		}
		s.Docs = append(s.Docs, doc)
		s.byPath[p] = doc
		return nil
	})
	if err != nil {
		return nil, err
	}
	rank := map[string]int{}
	for i, sec := range Sections {
		rank[sec] = i
	}
	sort.Slice(s.Docs, func(i, j int) bool {
		a, b := s.Docs[i], s.Docs[j]
		if a.Section != b.Section {
			return rank[a.Section] < rank[b.Section]
		}
		return a.Path < b.Path
	})
	return s, nil
}

// Embedded returns the docs compiled into the binary (false when the build
// skipped `go generate`).
func Embedded() (*Set, bool) {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		return nil, false
	}
	var m Manifest
	raw, err := fs.ReadFile(sub, ManifestFile)
	if err != nil || json.Unmarshal(raw, &m) != nil {
		return nil, false
	}
	s, err := Load(sub, "embedded", m.Updated)
	if err != nil || len(s.Docs) == 0 {
		return nil, false
	}
	return s, true
}

// FromDir reads a docs/ folder on disk.
func FromDir(dir string) (*Set, error) {
	return Load(os.DirFS(dir), "disk", nil)
}

// Get finds a doc by a requested path after Normalize.
func (s *Set) Get(requested string) (*Doc, bool) {
	rel, ok := Normalize(requested)
	if !ok {
		return nil, false
	}
	d, ok := s.byPath[rel]
	return d, ok
}

// Has reports whether a normalised path is served.
func (s *Set) Has(rel string) bool { _, ok := s.byPath[rel]; return ok }

// Normalize maps a requested doc path to a docs/-relative key: a leading
// "/" or "docs/" is dropped and ".md" added when there is no extension.
// Anything with "..", ".", empty segments, backslashes or control
// characters is rejected (the caller answers 404), so a request can only
// ever name a key of the served set.
func Normalize(requested string) (string, bool) {
	p := strings.TrimPrefix(requested, "/")
	p = strings.TrimPrefix(p, "docs/")
	if p == "" || len(p) > 200 || strings.ContainsAny(p, "\\:") {
		return "", false
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", false
		}
	}
	if path.Ext(p) == "" {
		p += ".md"
	}
	if path.Clean(p) != p {
		return "", false
	}
	return p, true
}

// --- locating the docs ------------------------------------------------------

const diskTTL = 2 * time.Second

var (
	locateMu    sync.Mutex
	locateCache = map[string]*cachedSet{}
)

type cachedSet struct {
	set      *Set
	dir      string // disk root ("" = embedded / none)
	loadedAt time.Time
}

// Locate returns the docs for a server whose web/ assets come from webDir
// ("" = embedded web).  A disk web root reads the docs/ folder beside it
// (development: edits show within two seconds); otherwise the embedded docs,
// else a docs/ folder beside the executable or in a parent of the working
// directory; else an empty set.
func Locate(webDir string) *Set {
	locateMu.Lock()
	defer locateMu.Unlock()
	c := locateCache[webDir]
	if c != nil && (c.dir == "" || time.Since(c.loadedAt) < diskTTL) {
		return c.set
	}
	if c != nil && c.dir != "" {
		if s, err := FromDir(c.dir); err == nil {
			c.set, c.loadedAt = s, time.Now()
		}
		return c.set
	}
	c = &cachedSet{loadedAt: time.Now()}
	if dir := docsBeside(webDir); dir != "" {
		c.dir = dir
	} else if s, ok := Embedded(); ok {
		c.set = s
	} else {
		c.dir = searchDocs()
	}
	if c.dir != "" {
		if s, err := FromDir(c.dir); err == nil {
			c.set = s
		}
	}
	if c.set == nil {
		c.set = &Set{Source: "none", byPath: map[string]*Doc{}}
		c.dir = ""
	}
	locateCache[webDir] = c
	return c.set
}

func isDocsDir(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "agents", "agent-interface.md"))
	return err == nil
}

func docsBeside(webDir string) string {
	if webDir == "" {
		return ""
	}
	if dir := filepath.Join(filepath.Dir(webDir), "docs"); isDocsDir(dir) {
		return dir
	}
	return ""
}

func searchDocs() string {
	var starts []string
	if exe, err := os.Executable(); err == nil {
		starts = append(starts, filepath.Dir(exe))
	}
	if wd, err := os.Getwd(); err == nil {
		starts = append(starts, wd)
	}
	for _, start := range starts {
		for dir := start; ; dir = filepath.Dir(dir) {
			if d := filepath.Join(dir, "docs"); isDocsDir(d) {
				return d
			}
			if filepath.Dir(dir) == dir {
				break
			}
		}
	}
	return ""
}
