// Package hostopen ports the open half of arkstudio/platform_support.py:
// scoped path resolution (safe_path, ensure_within, resolve_media_path,
// path_from_file_url) and opening a file, folder or URL in the operating
// system's default application.
//
// Under ARK_REPLAY_CLOCK=1 (the differential replay) nothing is launched:
// every opener succeeds without starting a process, exactly like the
// replay shim does for Python (tests/python/contract/replayclock.py).
package hostopen

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pyurl"
)

// Error is platform_support.PlatformError.
type Error struct {
	Message string
	Status  int
}

func (e *Error) Error() string { return e.Message }

func perr(message string, status int) error { return &Error{message, status} }

// Fake turns every launch into a no-op success (replay only).
var Fake = os.Getenv("ARK_REPLAY_CLOCK") == "1"

// PlatformID is platform_support.platform_id().
func PlatformID() string {
	switch runtime.GOOS {
	case "windows":
		return "windows"
	case "darwin":
		return "macos"
	case "linux", "freebsd", "openbsd", "netbsd":
		return "linux"
	}
	return runtime.GOOS
}

var (
	driveRe   = regexp.MustCompile(`^[A-Za-z]:`)
	urlLikeRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://`)
	httpRe    = regexp.MustCompile(`(?i)^https?://`)
	schemeRe  = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*:`)
	fileDrive = regexp.MustCompile(`^/[A-Za-z]:`)
)

// Resolve is Path(p).expanduser().resolve() (non-strict).
func Resolve(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			p = home + p[1:]
		}
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		if r2, err := filepath.Abs(real); err == nil {
			return r2
		}
	}
	return abs
}

func normalized(p string) string {
	n := Resolve(p)
	if runtime.GOOS == "windows" {
		n = strings.ToLower(n)
	}
	return n
}

// IsWithin is platform_support.is_within(root, path).
func IsWithin(root, path string) bool {
	r, p := normalized(root), normalized(path)
	if r == p {
		return true
	}
	rel, err := filepath.Rel(r, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func isSymlink(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.Mode()&os.ModeSymlink != 0
}

// EnsureWithin is platform_support.ensure_within(root, candidate, must_exist).
func EnsureWithin(root, candidate string, mustExist bool) (string, error) {
	rootPath := Resolve(root)
	raw := candidate
	if !filepath.IsAbs(raw) {
		if runtime.GOOS == "windows" && (strings.HasPrefix(raw, `\`) || strings.HasPrefix(raw, "/")) {
			// root / "\x" keeps only the drive of root (PureWindowsPath).
			raw = filepath.VolumeName(rootPath) + raw
		} else {
			raw = filepath.Join(rootPath, raw)
		}
	}
	if isSymlink(raw) {
		return "", perr("symlinked paths are not opened", 403)
	}
	target := Resolve(raw)
	if !IsWithin(rootPath, target) {
		return "", perr("path is outside the project or session workspace", 403)
	}
	if mustExist && !exists(target) {
		return "", perr("file not found: "+filepath.Base(target), 404)
	}
	return target, nil
}

// SafePath is platform_support.safe_path(root, value).
func SafePath(root, value string, mustExist bool) (string, error) {
	raw := strings.TrimSpace(strings.ReplaceAll(value, `\`, "/"))
	raw = strings.TrimFunc(raw, isPySpace)
	if raw == "" {
		return "", perr("path is required", 400)
	}
	if strings.Contains(raw, "\x00") {
		return "", perr("invalid path", 400)
	}
	if strings.HasPrefix(raw, "/") || driveRe.MatchString(raw) {
		return "", perr("absolute paths are not allowed; use a project-relative path", 400)
	}
	var parts []string
	for _, part := range strings.Split(raw, "/") {
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return "", perr("path is required", 400)
	}
	for _, part := range parts {
		if part == ".." {
			return "", perr("path traversal is not allowed", 400)
		}
	}
	return EnsureWithin(root, filepath.Join(append([]string{root}, parts...)...), mustExist)
}

func isPySpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x1f, 0x85, 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// PathFromFileURL is platform_support.path_from_file_url(value).
func PathFromFileURL(value string) (string, error) {
	u := pyurl.Parse(value)
	if strings.ToLower(u.Scheme) != "file" {
		return "", perr("not a file:// URL", 400)
	}
	if u.Netloc != "" && u.Netloc != "localhost" {
		return "", perr("network file URLs are not opened", 403)
	}
	raw := pyurl.Unquote(u.Path)
	if fileDrive.MatchString(raw) {
		raw = raw[1:]
	}
	if raw == "" {
		return "", perr("file URL has no path", 400)
	}
	return filepath.FromSlash(raw), nil
}

func isFile(p string) bool { st, err := os.Stat(p); return err == nil && st.Mode().IsRegular() }

// ResolveMediaPath is platform_support.resolve_media_path(root, value).
func ResolveMediaPath(root, value string) (string, error) {
	text := strings.TrimFunc(value, isPySpace)
	var target string
	var err error
	switch {
	case text == "":
		return "", perr("media URL is empty", 400)
	case strings.HasPrefix(strings.ToLower(text), "file://"):
		var p string
		if p, err = PathFromFileURL(text); err == nil {
			target, err = EnsureWithin(root, p, true)
		}
	case httpRe.MatchString(text):
		return "", perr("remote media is not a local file", 400)
	case strings.HasPrefix(text, "/") || driveRe.MatchString(text) || strings.HasPrefix(text, `\\`):
		target, err = EnsureWithin(root, filepath.FromSlash(text), true)
	case schemeRe.MatchString(text):
		return "", perr("unsupported media URL scheme", 400)
	default:
		target, err = SafePath(root, text, true)
	}
	if err != nil {
		return "", err
	}
	if !isFile(target) {
		return "", perr("not a file: "+filepath.Base(target), 404)
	}
	return target, nil
}

func localPath(value string) (string, error) {
	if urlLikeRe.MatchString(value) {
		return "", perr("open_external expects a local file; use default_browser_open for URLs", 400)
	}
	target := Resolve(value)
	st, err := os.Stat(target)
	if err != nil {
		return "", perr("file not found: "+filepath.Base(target), 404)
	}
	if !st.Mode().IsRegular() && !st.IsDir() {
		return "", perr("not a file or folder: "+filepath.Base(target), 400)
	}
	return target, nil
}

func run(argv ...string) (int, error) {
	if Fake {
		return 0, nil
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	if err := cmd.Start(); err != nil {
		var ee *exec.Error
		if errors.As(err, &ee) || errors.Is(err, os.ErrNotExist) {
			return 0, perr("application opener not found: "+argv[0], 400)
		}
		return 0, perr("could not launch "+argv[0]+": "+err.Error(), 400)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), nil
		}
		return 0, nil
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		return 0, perr("could not launch "+argv[0]+": timed out after 10 seconds", 400)
	}
}

func startfile(target string) error {
	if Fake {
		return nil
	}
	return shellOpen(target)
}

func which(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}

// OpenExternal is platform_support.open_external(value).
func OpenExternal(value string) (*pyjson.Object, error) {
	target, err := localPath(value)
	if err != nil {
		return nil, err
	}
	family := PlatformID()
	result := pyjson.NewObject("opened", true, "kind", "file", "target", target, "platform", family)
	switch family {
	case "windows":
		if err := startfile(target); err == nil {
			return result.Set("opener", "os.startfile"), nil
		}
		explorer := which("explorer")
		if explorer == "" {
			explorer = "explorer"
		}
		code, err := run(explorer, target)
		if err != nil {
			return nil, err
		}
		if code != 0 && code != 1 {
			return nil, perr("Windows could not open this file with its default application", 400)
		}
		return result.Set("opener", "explorer"), nil
	case "macos":
		opener := which("open")
		if opener == "" {
			opener = "/usr/bin/open"
		}
		code, err := run(opener, target)
		if err != nil {
			return nil, err
		}
		if code != 0 {
			return nil, perr("macOS 'open' could not launch the default application", 400)
		}
		return result.Set("opener", "open"), nil
	}
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return nil, perr("no graphical session is available; start ARK in a desktop session "+
			"or open the file manually", 400)
	}
	var attempts []string
	for _, name := range []string{"xdg-open", "gio"} {
		exe := which(name)
		if exe == "" {
			continue
		}
		argv := []string{exe, target}
		if name == "gio" {
			argv = []string{exe, "open", target}
		}
		code, err := run(argv...)
		if err == nil && code == 0 {
			if name == "gio" {
				return result.Set("opener", "gio open"), nil
			}
			return result.Set("opener", name), nil
		}
		attempts = append(attempts, name)
	}
	detail := ""
	if len(attempts) > 0 {
		detail = " (tried " + strings.Join(attempts, ", ") + ")"
	}
	return nil, perr("no default-application opener is available; install xdg-utils (xdg-open) or gio"+detail, 400)
}

// OpenURL is platform_support.open_url(value).
func OpenURL(value string) (*pyjson.Object, error) {
	url := strings.TrimFunc(value, isPySpace)
	scheme := strings.ToLower(pyurl.Parse(url).Scheme)
	if scheme == "" || scheme == "javascript" || scheme == "data" || scheme == "vbscript" {
		return nil, perr("unsupported URL scheme", 400)
	}
	family := PlatformID()
	result := func(opener string) *pyjson.Object {
		return pyjson.NewObject("opened", true, "kind", "url", "target", url, "platform", family, "opener", opener)
	}
	switch family {
	case "windows":
		if err := startfile(url); err != nil {
			return nil, perr("Windows could not open this URL: "+err.Error(), 400)
		}
		return result("os.startfile"), nil
	case "macos":
		opener := which("open")
		if opener == "" {
			opener = "/usr/bin/open"
		}
		if code, err := run(opener, url); err != nil {
			return nil, err
		} else if code == 0 {
			return result("open"), nil
		}
	default:
		opener := which("xdg-open")
		if opener == "" {
			opener = which("gio")
		}
		if opener != "" {
			argv := []string{opener, url}
			if !strings.HasSuffix(opener, "xdg-open") {
				argv = []string{opener, "open", url}
			}
			if code, err := run(argv...); err != nil {
				return nil, err
			} else if code == 0 {
				return result(filepath.Base(opener)), nil
			}
		}
	}
	if scheme == "http" || scheme == "https" {
		return DefaultBrowserOpen(url)
	}
	return nil, perr("no handler could open the "+scheme+":// URL", 400)
}

// DefaultBrowserOpen is platform_support.default_browser_open(url).
func DefaultBrowserOpen(raw string) (*pyjson.Object, error) {
	value := strings.TrimFunc(raw, isPySpace)
	u := pyurl.Parse(value)
	if s := strings.ToLower(u.Scheme); (s != "http" && s != "https") || u.Netloc == "" {
		return nil, perr("only http(s) URLs can be opened in the default browser", 400)
	}
	if !Fake {
		var err error
		switch PlatformID() {
		case "windows":
			err = shellOpen(value)
		case "macos":
			_, err = run("open", value)
		default:
			_, err = run("xdg-open", value)
		}
		if err != nil {
			return nil, perr("no default browser is available in this session", 400)
		}
	}
	return pyjson.NewObject("opened", true, "kind", "url", "target", value, "platform", PlatformID(), "opener", "webbrowser"), nil
}
