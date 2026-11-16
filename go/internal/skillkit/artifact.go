package skillkit

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pyre"
	"arkstudio/go/internal/pytext"
)

// design.validate_asset_path and design.verify_artifact, with pathlib's
// string forms (Windows paths on Windows, POSIX paths elsewhere).

var assetSuffixes = []string{".css", ".html", ".jpeg", ".jpg", ".pdf", ".png", ".svg", ".webp"}

var (
	urlSchemeRe = pyre.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*://`, 0)
	externalRe  = pyre.MustCompile(`(?:href|src|xlink:href)\s*=\s*["'](https?://[^"']+)["']`, pyre.FlagIgnoreCase)
	viewportRe  = pyre.MustCompile(`<meta[^>]+name=["\']viewport["\']`, pyre.FlagIgnoreCase)
)

const windows = runtime.GOOS == "windows"

// purePosix is str(PurePosixPath(raw)).
func purePosix(raw string) string {
	if raw == "" {
		return "."
	}
	lead := ""
	if strings.HasPrefix(raw, "//") && !strings.HasPrefix(raw, "///") {
		lead = "//"
	} else if strings.HasPrefix(raw, "/") {
		lead = "/"
	}
	parts := []string{}
	for _, p := range strings.Split(raw, "/") {
		if p != "" && p != "." {
			parts = append(parts, p)
		}
	}
	s := lead + strings.Join(parts, "/")
	if s == "" {
		return "."
	}
	return s
}

func posixParts(raw string) []string {
	parts := []string{}
	if strings.HasPrefix(raw, "/") {
		parts = append(parts, "/")
	}
	for _, p := range strings.Split(raw, "/") {
		if p != "" && p != "." {
			parts = append(parts, p)
		}
	}
	return parts
}

// pathStr is str(Path(raw)) on this platform.
func pathStr(raw string) string {
	if !windows {
		return purePosix(raw)
	}
	s := strings.ReplaceAll(raw, "/", `\`)
	drive := ""
	if len(s) >= 2 && s[1] == ':' && ((s[0]|0x20) >= 'a' && (s[0]|0x20) <= 'z') {
		drive, s = s[:2], s[2:]
	}
	root := ""
	if strings.HasPrefix(s, `\`) {
		root = `\`
	}
	parts := []string{}
	for _, p := range strings.Split(s, `\`) {
		if p != "" && p != "." {
			parts = append(parts, p)
		}
	}
	out := drive + root + strings.Join(parts, `\`)
	if out == "" {
		return "."
	}
	return out
}

func isAbs(p string) bool {
	if windows {
		s := strings.ReplaceAll(p, "/", `\`)
		return len(s) >= 3 && s[1] == ':' && s[2] == '\\' || strings.HasPrefix(s, `\\`)
	}
	return strings.HasPrefix(p, "/")
}

// joinPath is str(Path(root) / raw).
func joinPath(root, raw string) string {
	if raw == "" {
		return pathStr(root)
	}
	if windows {
		s := strings.ReplaceAll(raw, "/", `\`)
		if len(s) >= 2 && s[1] == ':' {
			return pathStr(raw)
		}
		if strings.HasPrefix(s, `\`) {
			return pathStr(filepath.VolumeName(root) + s)
		}
	} else if strings.HasPrefix(raw, "/") {
		return pathStr(raw)
	}
	return pathStr(root + string(filepath.Separator) + raw)
}

// resolve is Path(p).resolve() (non-strict): absolute, symlinks resolved
// for the existing part, ".." collapsed.
func resolve(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = filepath.Clean(p)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	// resolve the longest existing prefix
	dir, rest := abs, ""
	for {
		parent := filepath.Dir(dir)
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(real, rest)
		}
		if parent == filepath.Dir(parent) {
			return abs
		}
	}
}

func splitParts(p string) []string {
	out := []string{}
	for _, s := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		out = append(out, s)
	}
	return out
}

// relativeTo reports whether Path(child).relative_to(Path(root)) succeeds.
func relativeTo(child, root string) bool {
	if windows {
		cv, rv := filepath.VolumeName(child), filepath.VolumeName(root)
		if !strings.EqualFold(cv, rv) {
			return false
		}
		child, root = child[len(cv):], root[len(rv):]
	}
	c, r := splitParts(child), splitParts(root)
	if len(c) < len(r) {
		return false
	}
	for i := range r {
		if windows && !strings.EqualFold(c[i], r[i]) || !windows && c[i] != r[i] {
			return false
		}
	}
	return true
}

// suffix is PurePath(raw).suffix.
func suffix(raw string) string {
	name := raw
	if windows {
		name = strings.ReplaceAll(name, `\`, "/")
		if len(name) >= 2 && name[1] == ':' {
			name = name[2:]
		}
	}
	parts := posixParts(name)
	if len(parts) == 0 || parts[len(parts)-1] == "/" {
		return ""
	}
	last := parts[len(parts)-1]
	i := strings.LastIndex(last, ".")
	if 0 < i && i < len(last)-1 {
		return last[i:]
	}
	return ""
}

// ValidateAssetPath is design.validate_asset_path(path, project_root=...).
func ValidateAssetPath(path any, projectRoot string, hasRoot bool) *obj {
	raw := strings.ReplaceAll(pytext.Strip(str(or(path, ""))), `\`, "/")
	if raw == "" {
		return pyjson.NewObject("ok", false, "errors", []any{"asset path is required"}, "path", "")
	}
	errs := []any{}
	if reMatch(urlSchemeRe, raw) || strings.HasPrefix(raw, "data:") {
		errs = append(errs, "asset must be a local project file, not a URL or data URI")
	}
	if strings.HasPrefix(raw, "/") || driveRe.MatchString(raw) {
		errs = append(errs, "asset path must be project-relative")
	}
	parts := posixParts(raw)
	if inList("..", parts) {
		errs = append(errs, "asset path must not traverse outside the project")
	}
	if len(parts) == 0 || parts[0] != "assets" {
		errs = append(errs, "design artifacts must live under the project's assets/ folder")
	}
	suf := suffix(raw)
	if !inList(pytext.Lower(suf), assetSuffixes) {
		shown := suf
		if shown == "" {
			shown = "(none)"
		}
		errs = append(errs, "unsupported asset type: "+shown)
	}
	if hasRoot {
		root := resolve(pathStr(projectRoot))
		if !relativeTo(resolve(joinPath(root, raw)), root) {
			errs = append(errs, "asset path resolves outside the project folder")
		}
	}
	return pyjson.NewObject("ok", len(errs) == 0, "errors", errs, "path", purePosix(raw),
		"rules", pyjson.NewObject("project_relative", true, "assets_folder", true,
			"allowed_suffixes", strList(assetSuffixes)))
}

func check(id string, ok bool, detail string) *obj {
	return pyjson.NewObject("id", id, "ok", ok, "detail", detail)
}

// readText is Path.read_text(encoding="utf-8", errors="replace").
func readText(data []byte) string {
	var b strings.Builder
	for len(data) > 0 {
		r, n := utf8.DecodeRune(data)
		if r == utf8.RuneError && n <= 1 {
			b.WriteRune(utf8.RuneError)
			data = data[1:]
			continue
		}
		b.WriteRune(r)
		data = data[n:]
	}
	s := strings.ReplaceAll(b.String(), "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

func externalRefs(text string) []string {
	s := []rune(text)
	set := map[string]bool{}
	pos := 0
	for pos <= len(s) {
		budget := searchBudget
		m, ok, _ := externalRe.SearchFrom(s, pos, -1, &budget)
		if !ok {
			break
		}
		gs, ge, _ := m.Group(1)
		set[string(s[gs:ge])] = true
		pos = m.End
		if m.End == m.Start {
			pos++
		}
	}
	out := []string{}
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// VerifyArtifact is design.verify_artifact(path, project_root=..., kind=...).
func VerifyArtifact(path any, projectRoot string, hasRoot bool, kind string) (*obj, error) {
	raw, ok := path.(string)
	if !ok {
		return nil, uncaught("TypeError", "expected str, bytes or os.PathLike object, not "+typeName(path))
	}
	checks, errs, warns := []any{}, []any{}, []any{}
	target := pathStr(raw)
	if hasRoot {
		root := resolve(pathStr(projectRoot))
		candidate := pathStr(raw)
		if !isAbs(raw) {
			candidate = joinPath(root, raw)
		}
		if relativeTo(resolve(candidate), root) {
			checks = append(checks, check("in_project", true, candidate))
		} else {
			errs = append(errs, "artifact resolves outside the ARK project folder")
			checks = append(checks, check("in_project", false, candidate))
		}
		target = candidate
	}
	st, err := os.Stat(target)
	if err != nil || !st.Mode().IsRegular() {
		errs = append(errs, "artifact not found: "+target)
		return pyjson.NewObject("ok", false, "path", target, "checks", checks, "errors", errs,
			"warnings", warns, "kind", kind), nil
	}
	checks = append(checks, check("exists", true, target))
	size := st.Size()
	if size <= 0 {
		errs = append(errs, "artifact is empty")
	}
	checks = append(checks, check("non_empty", size > 0, strconv.FormatInt(size, 10)+" bytes"))
	if kind == "auto" {
		kind = map[string]string{".svg": "svg", ".pdf": "pdf", ".png": "png", ".jpg": "jpeg", ".jpeg": "jpeg",
			".webp": "webp", ".html": "html", ".css": "css"}[pytext.Lower(suffix(target))]
		if kind == "" {
			kind = "unknown"
		}
	}
	checks = append(checks, check("kind", kind != "unknown", kind))
	data, err := os.ReadFile(target)
	if err != nil {
		return nil, uncaught("PermissionError", err.Error())
	}
	switch kind {
	case "svg":
		tag, perr := xmlRoot(data)
		if perr != nil {
			errs = append(errs, "SVG is not well-formed XML: "+perr.Error())
			checks = append(checks, check("svg_xml", false, perr.Error()))
			break
		}
		local := tag[strings.LastIndex(tag, "}")+1:]
		valid := local == "svg"
		checks = append(checks, check("svg_xml", valid, tag))
		if !valid {
			errs = append(errs, "SVG root element is not <svg>")
		}
		ext := externalRefs(readText(data))
		detail := strings.Join(ext, ", ")
		if detail == "" {
			detail = "no external references"
		}
		checks = append(checks, check("self_contained", len(ext) == 0, detail))
		if len(ext) > 0 {
			errs = append(errs, "SVG references external assets: "+strings.Join(ext, ", "))
		}
	case "png", "jpeg", "pdf":
		magic := data
		if len(magic) > 8 {
			magic = magic[:8]
		}
		var ok bool
		switch kind {
		case "png":
			ok = bytes.HasPrefix(magic, []byte("\x89PNG\r\n\x1a\n"))
		case "jpeg":
			ok = bytes.HasPrefix(magic, []byte("\xff\xd8\xff"))
		default:
			ok = bytes.HasPrefix(magic, []byte("%PDF-"))
		}
		checks = append(checks, check("magic", ok, hex.EncodeToString(magic)))
		if !ok {
			errs = append(errs, "file does not look like a valid "+kind)
		}
	case "html":
		text := readText(data)
		_, has := search(viewportRe, text)
		ext := externalRefs(text)
		vd := "missing"
		if has {
			vd = "meta viewport present"
		}
		checks = append(checks, check("viewport", has, vd))
		detail := strings.Join(ext, ", ")
		if detail == "" {
			detail = "no external references"
		}
		checks = append(checks, check("self_contained", len(ext) == 0, detail))
		if !has {
			warns = append(warns, "HTML artifact has no viewport meta tag")
		}
		if len(ext) > 0 {
			warns = append(warns, "HTML references external assets; disclose and pin them")
		}
	case "css":
		has := strings.Contains(readText(data), "--")
		checks = append(checks, check("tokens", has, "CSS custom properties"))
		if !has {
			warns = append(warns, "CSS artifact declares no custom-property tokens")
		}
	}
	return pyjson.NewObject("ok", len(errs) == 0, "path", target, "kind", kind, "size", size,
		"checks", checks, "errors", errs, "warnings", warns, "quality_gates", QualityGates()), nil
}
