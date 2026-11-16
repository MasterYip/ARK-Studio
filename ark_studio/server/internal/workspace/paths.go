// Package workspace ports arkstudio/workspace.py: the Editor's project files,
// Changes (git diff) and Terminal views.  Everything stays inside the active
// project folder: absolute paths, “..“, NUL bytes, symlinks/junctions and
// ignored trees are rejected before the filesystem is touched.
package workspace

import (
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/platform/pathx"

	"arkstudio/go/internal/blake2b"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/rnd"
)

// Limits (workspace.py constants).
const (
	MaxTreeEntries          = 300
	MaxTreeDepth            = 4
	MaxTreeNodes            = 2500
	MaxFileBytes            = 1024 * 1024
	MaxSearchResults        = 200
	MaxSearchVisits         = 6000
	MaxDiffBytes            = 400 * 1024
	MaxDiffFiles            = 500
	MaxTerminalCommand      = 2000
	MaxTerminalOutput       = 256 * 1024
	MaxTerminalEntries      = 2000
	MaxTerminals            = 8
	TerminalDefaultTimeout  = 30
	TerminalMaxTimeout      = 120
	TerminalDefaultCols     = 80
	TerminalDefaultRows     = 24
	TerminalMinCols         = 2
	TerminalMaxCols         = 1000
	TerminalMinRows         = 1
	TerminalMaxRows         = 500
	GitTimeout              = 15 * time.Second
	terminalStdinLimitChars = 8000
)

var (
	ignoredDirs = setOf(".git", ".hg", ".svn", "__pycache__", ".mypy_cache", ".pytest_cache",
		".ruff_cache", "node_modules", ".venv", "venv", "env", ".tox", "dist",
		"build", "target", ".next", ".nuxt", ".cache", ".parcel-cache",
		"coverage", "htmlcov", ".idea", ".gradle", ".pytest-tmp")
	ignoredFiles    = setOf(".DS_Store", "Thumbs.db", "desktop.ini", "ark.sqlite3", "ark.sqlite3-wal", "ark.sqlite3-shm")
	ignoredSuffixes = setOf(".pyc", ".pyo", ".pyd", ".sqlite3-journal", ".swp", ".swo")
	protectedDirs   = setOf("setup")
	driveRe         = regexp.MustCompile(`\A[A-Za-z]:`)

	// languageBySuffix is session_transcript.LANGUAGE_BY_SUFFIX.
	languageBySuffix = map[string]string{
		".bat": "bat", ".c": "c", ".cfg": "ini", ".conf": "ini", ".cpp": "cpp",
		".cs": "csharp", ".css": "css", ".csv": "csv", ".go": "go", ".h": "c",
		".hpp": "cpp", ".htm": "html", ".html": "html", ".ini": "ini", ".java": "java",
		".js": "javascript", ".json": "json", ".jsonl": "jsonl", ".jsx": "jsx",
		".log": "log", ".md": "markdown", ".markdown": "markdown", ".mdx": "markdown",
		".mjs": "javascript", ".ps1": "powershell", ".py": "python", ".rb": "ruby",
		".rs": "rust", ".rst": "rst", ".sh": "bash", ".sql": "sql", ".svg": "xml",
		".tex": "latex", ".toml": "toml", ".ts": "typescript", ".tsx": "tsx",
		".txt": "text", ".xml": "xml", ".yaml": "yaml", ".yml": "yaml",
	}
)

func setOf(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, s := range items {
		m[s] = true
	}
	return m
}

// Error is WorkspaceError: a message with its HTTP status.
type Error struct {
	Message string
	Status  int
}

func (e *Error) Error() string { return e.Message }

func werr(status int, format string, args ...any) *Error {
	return &Error{Message: fmt.Sprintf(format, args...), Status: status}
}

// suffix is pathlib.PurePath(name).suffix for one path component.
func suffix(name string) string {
	if i := strings.LastIndexAny(name, `/`); i >= 0 {
		name = name[i+1:]
	}
	i := strings.LastIndex(name, ".")
	if i <= 0 || i == len(name)-1 {
		return ""
	}
	return name[i:]
}

// Language is session_transcript.language_for.
func Language(name string) string {
	return languageBySuffix[pytext.Lower(suffix(pathName(name)))]
}

// pathName is Path(s).name.
func pathName(s string) string {
	if runtime.GOOS == "windows" {
		s = strings.ReplaceAll(s, `\`, "/")
	}
	s = strings.TrimRight(s, "/")
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	if s == "." {
		return ""
	}
	return s
}

func isIgnored(name string) bool {
	if ignoredDirs[name] || ignoredFiles[name] {
		return true
	}
	return ignoredSuffixes[pytext.Lower(suffix(name))]
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func isFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

func isProtected(root, rel string) bool {
	first := strings.SplitN(strings.ReplaceAll(rel, `\`, "/"), "/", 2)[0]
	if !protectedDirs[pytext.Lower(first)] {
		return false
	}
	return exists(filepath.Join(root, "ark.sqlite3")) || exists(filepath.Join(root, "project.json"))
}

func normcase(p string) string {
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" {
		return strings.ToLower(strings.ReplaceAll(p, "/", `\`))
	}
	return p
}

// IsWithin is platform_support.is_within.
func IsWithin(root, path string) bool {
	r := normcase(project.Resolve(root))
	p := normcase(project.Resolve(path))
	if r == p {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(p, strings.TrimSuffix(r, sep)+sep)
}

// relParts is _rel_parts.
func relParts(value any) ([]string, error) {
	raw := ""
	if pyjson.Truthy(value) {
		raw = pyjson.Str(value)
	}
	raw = pytext.Strip(strings.ReplaceAll(raw, `\`, "/"))
	if raw == "" || raw == "." || raw == "./" {
		return nil, nil
	}
	if strings.Contains(raw, "\x00") {
		return nil, werr(400, "invalid path")
	}
	if strings.HasPrefix(raw, "/") || driveRe.MatchString(raw) {
		return nil, werr(400, "absolute paths are not allowed; use a project-relative path")
	}
	var parts []string
	for _, p := range strings.Split(raw, "/") {
		if p != "" && p != "." {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return nil, nil
	}
	for _, p := range parts {
		if p == ".." {
			return nil, werr(400, "path traversal is not allowed")
		}
	}
	for _, p := range parts {
		if isIgnored(p) {
			return nil, werr(403, "%s is ignored by the workspace and cannot be opened", p)
		}
	}
	return parts, nil
}

// resolve is _resolve: a project-relative path with no symlinked component.
func resolve(root string, value any, mustExist bool) (string, string, error) {
	rootPath := project.Resolve(root)
	parts, err := relParts(value)
	if err != nil {
		return "", "", err
	}
	if len(parts) > 0 && isProtected(rootPath, parts[0]) {
		return "", "", werr(403, "%s is ignored by the workspace and cannot be opened", parts[0])
	}
	current := rootPath
	for _, part := range parts {
		current = filepath.Join(current, part)
		if isLinkLike(current) {
			return "", "", werr(403, "symlinked paths are not allowed")
		}
	}
	target := project.Resolve(filepath.Join(append([]string{rootPath}, parts...)...))
	rel := strings.Join(parts, "/")
	if !pathx.SamePath(target, rootPath) && !IsWithin(rootPath, target) {
		return "", "", werr(403, "path is outside the project folder")
	}
	if len(parts) > 0 && pathx.SamePath(target, rootPath) {
		return "", "", werr(403, "path is outside the project folder")
	}
	if mustExist && !exists(target) {
		name := rel
		if name == "" {
			name = filepath.Base(target)
		}
		return "", "", werr(404, "file not found: %s", name)
	}
	return target, rel, nil
}

// isoTime is _iso_time(st_mtime) for a modification time.
func isoTime(t time.Time) string {
	ns := t.UnixNano()
	sec := ns / 1e9
	if ns%1e9 >= 999_999_500 { // fromtimestamp rounds to microseconds
		sec++
	}
	return time.Unix(sec, 0).UTC().Format("2006-01-02T15:04:05") + "+00:00"
}

func hashBytes(raw []byte) string {
	return hex.EncodeToString(blake2b.Sum(raw, 16))
}

func looksBinary(raw []byte) bool {
	if len(raw) > 8192 {
		raw = raw[:8192]
	}
	return strings.IndexByte(string(raw), 0) >= 0
}

// statPayload is _stat_payload.
func statPayload(path, rel, name string) *pyjson.Object {
	fi, err := os.Stat(path)
	dir := err == nil && fi.IsDir()
	out := pyjson.NewObject("path", rel, "name", name)
	if dir {
		out.Set("type", "dir")
	} else {
		out.Set("type", "file")
	}
	var size, mtimeNS int64
	mtime := ""
	if err == nil {
		if !dir {
			size = fi.Size()
		}
		mtime = isoTime(fi.ModTime())
		mtimeNS = fi.ModTime().UnixNano()
	}
	lang := ""
	if !dir {
		lang = Language(name)
	}
	return out.Set("size", size).Set("mtime", mtime).Set("mtime_ns", mtimeNS).Set("language", lang)
}

// atomicWrite is _atomic_write.
func atomicWrite(target string, data []byte) error {
	temp := filepath.Join(filepath.Dir(target), "."+filepath.Base(target)+".ark-tmp-"+rnd.UUID4Hex()[:10])
	defer os.Remove(temp) //nolint:errcheck
	fail := func(err error) error { return werr(400, "cannot write file: %s", osMessage(err)) }
	var mode os.FileMode
	keep := false
	if fi, err := os.Stat(target); err == nil {
		mode, keep = fi.Mode().Perm(), true
	}
	f, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return fail(err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fail(err)
	}
	if err := f.Close(); err != nil {
		return fail(err)
	}
	if keep {
		if err := os.Chmod(temp, mode); err != nil {
			return fail(err)
		}
	}
	if err := os.Rename(temp, target); err != nil {
		return fail(err)
	}
	return nil
}

// osMessage approximates str(OSError): the operation detail without Go's
// "op path:" prefix.
func osMessage(err error) string {
	if pe, ok := err.(*os.PathError); ok {
		return pe.Err.Error() + ": '" + pe.Path + "'"
	}
	if le, ok := err.(*os.LinkError); ok {
		return le.Err.Error() + ": '" + le.Old + "' -> '" + le.New + "'"
	}
	return err.Error()
}

func compareDigest(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// now is workspace.utc_now (the replay clock under ARK_REPLAY_CLOCK=1).
func now() string { return clock.UTCNow() }

// Exported for Workbench Search (internal/search), which follows the
// Explorer's rules.
func IsIgnored(name string) bool                { return isIgnored(name) }
func IsProtected(root, rel string) bool         { return isProtected(root, rel) }
func IsLinkLike(path string) bool               { return isLinkLike(path) }
func LooksBinary(raw []byte) bool               { return looksBinary(raw) }
func ListDir(dir string) ([]os.DirEntry, error) { return listDir(dir) }
func IsDir(path string) bool                    { return isDir(path) }

// IgnoredDirNames is workspace.IGNORED_DIRS.
func IgnoredDirNames() []string {
	out := make([]string, 0, len(ignoredDirs))
	for name := range ignoredDirs {
		out = append(out, name)
	}
	return out
}
