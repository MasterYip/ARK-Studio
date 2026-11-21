package project

// Project storage folders (Go only).
//
// Root is the server's project folder: it also holds the agent token, the
// tutorial copies and (next to it) the organization database, and it is
// always listed.  An admin may choose another folder for NEW projects
// (Settings > Project storage location); that folder, and every folder
// chosen before, is listed as well, so existing projects stay where they
// are and stay visible.  Nothing here moves or deletes a project folder.
//
// When the chosen folder is missing (an unplugged drive, a deleted folder)
// new projects go to Root and Storage reports the fallback.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"arkstudio/go/internal/platform/diskfree"
	"arkstudio/go/internal/platform/pathx"
)

// MaxStorageFolders bounds the remembered project folders.
const MaxStorageFolders = 32

// StorageConfig is the stored organization option.
type StorageConfig struct {
	// Default is the folder for new projects ("" = Root).
	Default string `json:"default"`
	// Folders are the other folders whose projects are listed.
	Folders []string `json:"folders"`
}

// SetStorage applies cfg (paths are resolved; Root and duplicates dropped).
func (m *Manager) SetStorage(cfg StorageConfig) {
	m.fmu.Lock()
	defer m.fmu.Unlock()
	m.newDir = ""
	if d := strings.TrimSpace(cfg.Default); d != "" && filepath.IsAbs(d) && !pathx.SamePath(Resolve(d), m.Root) {
		m.newDir = Resolve(d)
	}
	m.extra = nil
	add := func(p string) {
		if p == "" || !filepath.IsAbs(p) {
			return
		}
		p = Resolve(p)
		if pathx.SamePath(p, m.Root) {
			return
		}
		for _, q := range m.extra {
			if pathx.SamePath(p, q) {
				return
			}
		}
		m.extra = append(m.extra, p)
	}
	add(m.newDir)
	for _, f := range cfg.Folders {
		add(strings.TrimSpace(f))
	}
	if len(m.extra) > MaxStorageFolders {
		m.extra = m.extra[:MaxStorageFolders]
	}
}

// Storage is the applied configuration (resolved paths).
func (m *Manager) Storage() StorageConfig {
	m.fmu.RLock()
	defer m.fmu.RUnlock()
	return StorageConfig{Default: m.newDir, Folders: append([]string(nil), m.extra...)}
}

// Folders are the listed project folders: Root first.
func (m *Manager) Folders() []string {
	m.fmu.RLock()
	defer m.fmu.RUnlock()
	return append([]string{m.Root}, m.extra...)
}

// InFolders reports whether dir is one of the listed project folders.
func (m *Manager) InFolders(dir string) bool {
	dir = Resolve(dir)
	for _, f := range m.Folders() {
		if pathx.SamePath(dir, f) {
			return true
		}
	}
	return false
}

// InsideFolders reports whether path is a listed folder or lies below one.
func (m *Manager) InsideFolders(path string) bool {
	path = Resolve(path)
	for _, f := range m.Folders() {
		if pathx.Within(path, f) {
			return true
		}
	}
	return false
}

// NewProjectFolder is where the next project is created, and whether that
// is Root because the chosen folder is missing (fallback).
func (m *Manager) NewProjectFolder() (string, bool) {
	m.fmu.RLock()
	dir := m.newDir
	m.fmu.RUnlock()
	if dir == "" {
		return m.Root, false
	}
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		return dir, false
	}
	return m.Root, true
}

// FolderCheck is the verdict on a folder an admin wants to use.
type FolderCheck struct {
	Path       string // resolved
	Exists     bool
	WillCreate bool
	Free       int64 // bytes available; -1 unknown
}

// CheckError is a rejected folder: Code is a stable error code.
type CheckError struct{ Code, Message string }

func (e *CheckError) Error() string { return e.Message }

func reject(code, format string, args ...any) error {
	return &CheckError{code, fmt.Sprintf(format, args...)}
}

var goos = runtime.GOOS

// systemFolders are folders (and everything below them) that never hold
// projects.
func systemFolders() []string {
	if goos == "windows" {
		var out []string
		for _, v := range []string{"SystemRoot", "windir", "ProgramFiles", "ProgramFiles(x86)", "ProgramW6432", "ProgramData"} {
			if p := strings.TrimSpace(os.Getenv(v)); p != "" {
				out = append(out, p)
			}
		}
		return append(out, `C:\Windows`, `C:\Program Files`, `C:\Program Files (x86)`, `C:\ProgramData`)
	}
	out := []string{"/bin", "/boot", "/dev", "/etc", "/lib", "/lib32", "/lib64", "/libx32", "/proc", "/run", "/sbin", "/sys", "/usr", "/var/lib", "/var/log", "/var/run", "/var/cache"}
	if goos == "darwin" {
		out = append(out, "/System", "/Library", "/Applications", "/private/etc", "/private/var/db", "/cores")
	}
	return out
}

// bareFolders may not be chosen themselves (folders below them may).
func bareFolders() []string {
	if goos == "windows" {
		var out []string
		if p := strings.TrimSpace(os.Getenv("USERPROFILE")); p != "" {
			out = append(out, p)
		}
		if p := strings.TrimSpace(os.Getenv("SystemDrive")); p != "" {
			out = append(out, p+`\Users`)
		}
		return out
	}
	out := []string{"/home", "/root", "/var", "/opt", "/srv", "/mnt", "/media", "/tmp", "/Users", "/Volumes", "/private"}
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, home)
	}
	return out
}

// isVolumeRoot: "/" or "C:\" (or a UNC share root).
func isVolumeRoot(p string) bool {
	return filepath.Dir(p) == p
}

// projectAbove is the nearest folder at or above p that is an ARK project.
func projectAbove(p string) string {
	for cur := p; ; {
		if fi, err := os.Stat(filepath.Join(cur, "project.json")); err == nil && !fi.IsDir() {
			if _, ok := ReadManifest(cur); ok {
				return cur
			}
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}
		cur = parent
	}
}

// nearestExisting is p or its nearest existing parent.
func nearestExisting(p string) (string, os.FileInfo) {
	for cur := p; ; {
		if fi, err := os.Stat(cur); err == nil {
			return cur, fi
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", nil
		}
		cur = parent
	}
}

// Writable probes dir by creating and removing a file.
func Writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".ark-write-test-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	return os.Remove(name) == nil
}

// CheckFolder validates raw as a folder for new projects: absolute, not a
// system folder or volume root, not inside a project, not above ARK's own
// data (Root), an existing writable folder or one that can be created
// below a writable one.  Nothing is created.
func (m *Manager) CheckFolder(raw string) (FolderCheck, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return FolderCheck{}, reject("STORAGE_PATH_REQUIRED", "Enter a folder path.")
	}
	if strings.ContainsRune(text, 0) {
		return FolderCheck{}, reject("STORAGE_PATH_INVALID", "The path contains an invalid character.")
	}
	if !filepath.IsAbs(text) {
		return FolderCheck{}, reject("STORAGE_PATH_RELATIVE", "Enter a full (absolute) folder path, for example %s.", examplePath())
	}
	path := Resolve(filepath.Clean(text))
	if isVolumeRoot(path) {
		return FolderCheck{}, reject("STORAGE_PATH_SYSTEM", "Choose a folder, not the top of a drive (%s).", path)
	}
	for _, sys := range systemFolders() {
		if pathx.Within(path, sys) || pathx.Within(path, Resolve(sys)) {
			return FolderCheck{}, reject("STORAGE_PATH_SYSTEM", "%s is a system folder. Choose a folder of your own.", path)
		}
	}
	for _, bare := range bareFolders() {
		if pathx.SamePath(path, bare) || pathx.SamePath(path, Resolve(bare)) {
			return FolderCheck{}, reject("STORAGE_PATH_SYSTEM", "Choose a folder inside %s rather than %s itself.", path, path)
		}
	}
	if pathx.Within(m.Root, path) && !pathx.SamePath(m.Root, path) {
		return FolderCheck{}, reject("STORAGE_PATH_DATA", "%s contains ARK's own data folder. Choose a separate folder.", path)
	}
	if p := projectAbove(path); p != "" {
		return FolderCheck{}, reject("STORAGE_PATH_IN_PROJECT", "%s is inside the project folder %s. Choose a folder outside any project.", path, p)
	}
	out := FolderCheck{Path: path, Free: -1}
	existing, fi := nearestExisting(path)
	if fi == nil {
		return FolderCheck{}, reject("STORAGE_PATH_MISSING", "The drive or folder for %s does not exist.", path)
	}
	if !fi.IsDir() {
		return FolderCheck{}, reject("STORAGE_PATH_NOT_FOLDER", "%s is a file, not a folder.", existing)
	}
	out.Exists = pathx.SamePath(existing, path)
	out.WillCreate = !out.Exists
	if !Writable(existing) {
		if out.Exists {
			return FolderCheck{}, reject("STORAGE_PATH_READONLY", "ARK cannot write to %s.", path)
		}
		return FolderCheck{}, reject("STORAGE_PATH_READONLY", "ARK cannot create %s: %s is not writable.", path, existing)
	}
	if n, ok := diskfree.Available(existing); ok {
		out.Free = n
	}
	return out, nil
}

// PrepareFolder creates a checked folder (and parents) and probes it again.
func PrepareFolder(c FolderCheck) error {
	if err := os.MkdirAll(c.Path, 0o755); err != nil {
		return reject("STORAGE_PATH_CREATE", "Cannot create %s: %v", c.Path, errors.Unwrap(err))
	}
	if !Writable(c.Path) {
		return reject("STORAGE_PATH_READONLY", "ARK cannot write to %s.", c.Path)
	}
	return nil
}

// foldKey compares paths the way the file system does.
func foldKey(p string) string {
	if goos == "windows" {
		return strings.ToLower(filepath.Clean(p))
	}
	return filepath.Clean(p)
}

func examplePath() string {
	if goos == "windows" {
		return `C:\ARK Projects`
	}
	return "/srv/ark/projects"
}

// FolderInfo describes one listed folder.
type FolderInfo struct {
	Path     string
	Builtin  bool // Root
	Default  bool // where new projects go (configured)
	Exists   bool
	Projects int
}

// FolderInfos lists the folders with their project counts.
func (m *Manager) FolderInfos() []FolderInfo {
	counts := map[string]int{}
	for _, s := range m.ListProjects() {
		if p, ok := s.Value("path").(string); ok {
			counts[foldKey(Resolve(filepath.Dir(p)))]++
		}
	}
	cfg := m.Storage()
	var out []FolderInfo
	for i, f := range m.Folders() {
		fi, err := os.Stat(f)
		info := FolderInfo{Path: f, Builtin: i == 0, Exists: err == nil && fi.IsDir(), Projects: counts[foldKey(f)]}
		info.Default = cfg.Default == "" && i == 0 || cfg.Default != "" && pathx.SamePath(f, cfg.Default)
		out = append(out, info)
	}
	return out
}
