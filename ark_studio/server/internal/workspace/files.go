package workspace

import (
	"arkstudio/go/internal/platform/pyerr"
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"

	"arkstudio/go/internal/platform/pathx"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/store"
)

// listDir is Path.iterdir()/os.scandir in directory order (unsorted).
func listDir(dir string) ([]os.DirEntry, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.ReadDir(-1)
}

func lowerLess(a, b string) bool { return pytext.Lower(a) < pytext.Lower(b) }

// Info is Workspace.info.
func Info(root string) *pyjson.Object {
	rootPath := project.Resolve(root)
	pty := PTYAvailability()
	shell := "cmd.exe"
	if runtime.GOOS != "windows" {
		if shell = os.Getenv("SHELL"); shell == "" {
			shell = "sh"
		}
	}
	return pyjson.NewObject(
		"project", filepath.Base(rootPath),
		"path", rootPath,
		"git", gitContext(rootPath).object(),
		"shell", shell,
		"pty", pty.Available,
		"pty_available", pty.Available,
		"pty_backend", pty.Backend,
		"pty_version", pty.Version,
		"pty_reason", pty.Reason,
		"pty_hint", pty.Hint,
		"limits", pyjson.NewObject(
			"tree_entries", MaxTreeEntries,
			"tree_depth", MaxTreeDepth,
			"file_bytes", MaxFileBytes,
			"diff_bytes", MaxDiffBytes,
			"terminal_output_bytes", MaxTerminalOutput,
			"terminal_timeout_seconds", TerminalMaxTimeout,
			"terminal_cols", []any{int64(TerminalMinCols), int64(TerminalMaxCols)},
			"terminal_rows", []any{int64(TerminalMinRows), int64(TerminalMaxRows)},
		),
	)
}

// Tree is Workspace.tree.
func Tree(root, path string, depth int64) (*pyjson.Object, error) {
	rootPath := project.Resolve(root)
	target, rel, err := resolve(rootPath, path, true)
	if err != nil {
		return nil, err
	}
	if !isDir(target) {
		return nil, werr(400, "not a folder")
	}
	depth = max(1, min(MaxTreeDepth, depth))
	entriesLeft, nodesLeft := MaxTreeEntries, MaxTreeNodes
	ignored, symlinks, truncated := 0, 0, false
	var walk func(folder, relPath string, level int64) ([]any, error)
	walk = func(folder, relPath string, level int64) ([]any, error) {
		entries := []any{}
		if entriesLeft <= 0 || nodesLeft <= 0 {
			truncated = true
			return entries, nil
		}
		list, err := listDir(folder)
		if err != nil {
			return nil, werr(400, "cannot list folder: %s", osMessage(err))
		}
		type child struct {
			name, path string
			dir        bool
		}
		children := make([]child, len(list))
		for i, e := range list {
			p := filepath.Join(folder, e.Name())
			children[i] = child{e.Name(), p, isDir(p)}
		}
		sort.SliceStable(children, func(i, j int) bool {
			a, b := children[i], children[j]
			if a.dir != b.dir {
				return a.dir
			}
			return lowerLess(a.name, b.name)
		})
		for _, c := range children {
			if entriesLeft <= 0 || nodesLeft <= 0 {
				truncated = true
				break
			}
			if isLinkLike(c.path) {
				symlinks++
				continue
			}
			if isIgnored(c.name) || (relPath == "" && isProtected(rootPath, c.name)) {
				ignored++
				continue
			}
			dir := isDir(c.path)
			childRel := c.name
			if relPath != "" {
				childRel = relPath + "/" + c.name
			}
			entry := statPayload(c.path, childRel, c.name)
			entry.Set("has_children", dir)
			if dir && level < depth {
				kids, err := walk(c.path, childRel, level+1)
				if err != nil {
					return nil, err
				}
				entry.Set("children", kids)
			}
			entries = append(entries, entry)
			entriesLeft--
			nodesLeft--
		}
		return entries, nil
	}
	entries, err := walk(target, rel, 1)
	if err != nil {
		return nil, err
	}
	var parent any
	if rel != "" {
		parts := strings.Split(rel, "/")
		parent = strings.Join(parts[:len(parts)-1], "/")
	}
	return pyjson.NewObject("path", rel, "parent", parent, "entries", entries, "depth", depth,
		"truncated", truncated, "ignored", ignored, "symlinks_skipped", symlinks, "count", len(entries)), nil
}

// Search is Workspace.search (file names only).
func Search(root, query string, limit int64) *pyjson.Object {
	rootPath := project.Resolve(root)
	text := pytext.Lower(pytext.Strip(query))
	if text == "" {
		return pyjson.NewObject("query", "", "results", []any{}, "truncated", false, "visited", 0)
	}
	limit = max(1, min(MaxSearchResults, limit))
	results := []any{}
	visited := 0
	truncated := false
	var walk func(current string) bool // false = stop
	walk = func(current string) bool {
		list, err := listDir(current)
		if err != nil {
			return true // os.walk skips unreadable folders
		}
		var dirs, files []string
		for _, e := range list {
			if isDir(filepath.Join(current, e.Name())) {
				dirs = append(dirs, e.Name())
			} else {
				files = append(files, e.Name())
			}
		}
		keep := dirs[:0]
		for _, name := range dirs {
			if isIgnored(name) || (pathx.SamePath(current, rootPath) && isProtected(rootPath, name)) ||
				isLinkLike(filepath.Join(current, name)) {
				continue
			}
			keep = append(keep, name)
		}
		dirs = keep
		sort.SliceStable(dirs, func(i, j int) bool { return lowerLess(dirs[i], dirs[j]) })
		sort.SliceStable(files, func(i, j int) bool { return lowerLess(files[i], files[j]) })
		for _, name := range files {
			if int64(len(results)) >= limit || visited >= MaxSearchVisits {
				truncated = true
				break
			}
			child := filepath.Join(current, name)
			if isLinkLike(child) || isIgnored(name) {
				continue
			}
			visited++
			rel, _ := filepath.Rel(rootPath, child)
			rel = filepath.ToSlash(rel)
			if !strings.Contains(pytext.Lower(rel), text) {
				continue
			}
			var size int64
			if fi, err := os.Stat(child); err == nil {
				size = fi.Size()
			}
			results = append(results, pyjson.NewObject("path", rel, "name", name, "type", "file",
				"size", size, "language", Language(name)))
		}
		if truncated {
			return false
		}
		for _, name := range dirs {
			if !walk(filepath.Join(current, name)) {
				return false
			}
		}
		return true
	}
	walk(rootPath)
	return pyjson.NewObject("query", query, "results", results, "truncated", truncated, "visited", visited)
}

// ResolveFile is Workspace.resolve_file.
func ResolveFile(root, path string) (string, error) {
	if pytext.Strip(path) == "" {
		return "", werr(400, "path is required")
	}
	target, _, err := resolve(project.Resolve(root), path, true)
	if err != nil {
		return "", err
	}
	if !isFile(target) {
		return "", werr(400, "path is a folder, not a file")
	}
	return target, nil
}

// decodeUTF8Sig is bytes.decode("utf-8-sig") (strict).
func decodeUTF8Sig(raw []byte) (string, bool) {
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(raw) {
		return "", false
	}
	return string(raw), true
}

func lineCount(text string) int {
	if text == "" {
		return 0
	}
	return len(pytext.SplitLines(text, false))
}

// ReadFile is Workspace.read_file.
func ReadFile(root, path string) (*pyjson.Object, error) {
	target, rel, err := resolve(project.Resolve(root), path, true)
	if err != nil {
		return nil, err
	}
	if isDir(target) {
		return nil, werr(400, "path is a folder, not a file")
	}
	fi, err := os.Stat(target)
	if err != nil {
		return nil, werr(400, "cannot read file: %s", osMessage(err))
	}
	if fi.Size() > MaxFileBytes {
		return nil, werr(413, "file is too large to edit (%d bytes; limit %d)", fi.Size(), MaxFileBytes)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		return nil, werr(400, "cannot read file: %s", osMessage(err))
	}
	if looksBinary(raw) {
		return nil, werr(415, "binary file; the editor only opens UTF-8 text")
	}
	text, ok := decodeUTF8Sig(raw)
	if !ok {
		return nil, werr(415, "file is not UTF-8 text")
	}
	eol := "lf"
	if bytes.Contains(raw, []byte("\r\n")) {
		eol = "crlf"
	}
	name := filepath.Base(target)
	return pyjson.NewObject("path", rel, "name", name, "content", text, "size", fi.Size(),
		"lines", lineCount(text), "language", Language(name), "hash", hashBytes(raw),
		"mtime", isoTime(fi.ModTime()), "mtime_ns", fi.ModTime().UnixNano(),
		"eol", eol, "read_only", readOnly(target)), nil
}

// WriteFile is Workspace.write_file (optimistic concurrency).
func WriteFile(root string, body *pyjson.Object) (*pyjson.Object, error) {
	rootPath := project.Resolve(root)
	content, ok := body.Value("content").(string)
	if !ok {
		return nil, werr(400, "content must be a string")
	}
	data := []byte(content)
	if len(data) > MaxFileBytes {
		return nil, werr(413, "file is too large to save (%d bytes; limit %d)", len(data), MaxFileBytes)
	}
	target, rel, err := resolve(rootPath, body.Value("path"), false)
	if err != nil {
		return nil, err
	}
	if exists(target) && isDir(target) {
		return nil, werr(400, "path is a folder, not a file")
	}
	if !IsWithin(rootPath, filepath.Dir(target)) {
		return nil, werr(403, "path is outside the project folder")
	}
	if !isDir(filepath.Dir(target)) {
		return nil, werr(404, "parent folder does not exist")
	}
	baseHash := ""
	if v := body.Value("base_hash"); pyjson.Truthy(v) {
		baseHash = pytext.Strip(pyjson.Str(v))
	}
	baseMtime := body.Value("base_mtime")
	mtimeMissing := baseMtime == nil || baseMtime == ""
	create := pyjson.Truthy(body.Value("create"))
	existed := exists(target)
	if existed {
		if baseHash == "" && mtimeMissing {
			return nil, werr(409, "base_hash or base_mtime is required to overwrite a file")
		}
		fi, err := os.Stat(target)
		if err != nil {
			return nil, werr(400, "cannot read file: %s", osMessage(err))
		}
		if fi.Size() > MaxFileBytes {
			return nil, werr(413, "file is too large to save (%d bytes; limit %d)", fi.Size(), MaxFileBytes)
		}
		current, err := os.ReadFile(target)
		if err != nil {
			return nil, werr(400, "cannot read file: %s", osMessage(err))
		}
		changed := werr(409, "the file changed on disk since it was opened; reload it before saving")
		if baseHash != "" {
			for _, r := range baseHash {
				if r > 0x7f {
					return nil, &pyerr.InternalError{Message: "comparing strings with non-ASCII characters is not supported"}
				}
			}
			if !compareDigest(baseHash, hashBytes(current)) {
				return nil, changed
			}
		} else if !mtimeMissing {
			if _, big := baseMtime.(interface{ BitLen() int }); big {
				return nil, changed
			}
			expected, err := store.PyInt(baseMtime)
			if err != nil {
				return nil, werr(400, "base_mtime must be an integer nanosecond timestamp")
			}
			if expected != fi.ModTime().UnixNano() {
				return nil, changed
			}
		}
	} else if !create {
		return nil, werr(404, "file does not exist; create it explicitly")
	}
	if err := atomicWrite(target, data); err != nil {
		return nil, err
	}
	out := pyjson.NewObject("path", rel, "created", !existed)
	out.Update(statPayload(target, rel, filepath.Base(target)))
	out.Set("size", len(data)).Set("lines", lineCount(content)).Set("hash", hashBytes(data))
	if fi, err := os.Stat(target); err == nil {
		out.Set("mtime", isoTime(fi.ModTime())).Set("mtime_ns", fi.ModTime().UnixNano())
	}
	return out, nil
}

// CreatePath is Workspace.create_path.
func CreatePath(root string, body *pyjson.Object) (*pyjson.Object, error) {
	rootPath := project.Resolve(root)
	kind := "file"
	if v := body.Value("type"); pyjson.Truthy(v) {
		kind = pytext.Lower(pytext.Strip(pyjson.Str(v)))
	}
	if kind != "file" && kind != "dir" {
		return nil, werr(400, "type must be file or dir")
	}
	target, rel, err := resolve(rootPath, body.Value("path"), false)
	if err != nil {
		return nil, err
	}
	if exists(target) {
		return nil, werr(409, "path already exists")
	}
	if !isDir(filepath.Dir(target)) {
		return nil, werr(404, "parent folder does not exist")
	}
	var data []byte
	if kind == "dir" {
		if err := os.Mkdir(target, 0o777); err != nil {
			return nil, werr(400, "cannot create path: %s", osMessage(err))
		}
	} else {
		content := body.Value("content")
		if content == nil {
			content = ""
		}
		text, ok := content.(string)
		if !ok {
			return nil, werr(400, "content must be a string")
		}
		data = []byte(text)
		if len(data) > MaxFileBytes {
			return nil, werr(413, "file is too large")
		}
		f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
		if err != nil {
			return nil, werr(400, "cannot create path: %s", osMessage(err))
		}
		_, err = f.Write(data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, werr(400, "cannot create path: %s", osMessage(err))
		}
	}
	payload := statPayload(target, rel, filepath.Base(target))
	if kind == "file" {
		raw, _ := os.ReadFile(target)
		payload.Set("hash", hashBytes(raw))
	}
	return payload, nil
}

// RenamePath is Workspace.rename_path.
func RenamePath(root string, body *pyjson.Object) (*pyjson.Object, error) {
	rootPath := project.Resolve(root)
	source, sourceRel, err := resolve(rootPath, body.Value("from"), true)
	if err != nil {
		return nil, err
	}
	target, targetRel, err := resolve(rootPath, body.Value("to"), false)
	if err != nil {
		return nil, err
	}
	if pathx.SamePath(source, rootPath) {
		return nil, werr(400, "the project root cannot be moved")
	}
	if exists(target) {
		return nil, werr(409, "destination already exists")
	}
	if !isDir(filepath.Dir(target)) {
		return nil, werr(404, "destination folder does not exist")
	}
	if isDir(source) && (pathx.SamePath(target, source) || isParent(source, target)) {
		return nil, werr(400, "a folder cannot be moved inside itself")
	}
	if err := os.Rename(source, target); err != nil {
		return nil, werr(400, "cannot rename path: %s", osMessage(err))
	}
	kind := "file"
	if isDir(target) {
		kind = "dir"
	}
	return pyjson.NewObject("from", sourceRel, "to", targetRel, "type", kind), nil
}

// isParent is `parent in child.parents`.
func isParent(parent, child string) bool {
	for p := filepath.Dir(filepath.Clean(child)); ; p = filepath.Dir(p) {
		if pathx.SamePath(p, parent) {
			return true
		}
		if filepath.Dir(p) == p {
			return false
		}
	}
}

// deleteTree is _delete_tree: bottom-up, never following links.
func deleteTree(target string) (int, error) {
	removed := 0
	var visit func(folder string) error
	visit = func(folder string) error {
		entries, err := listDir(folder)
		if err != nil {
			return err
		}
		for _, e := range entries {
			child := filepath.Join(folder, e.Name())
			switch {
			case isLinkLike(child):
				if err := os.Remove(child); err != nil {
					return err
				}
				removed++
			case e.IsDir():
				if err := visit(child); err != nil {
					return err
				}
			default:
				if err := os.Remove(child); err != nil {
					return err
				}
				removed++
			}
		}
		if err := os.Remove(folder); err != nil {
			return err
		}
		removed++
		return nil
	}
	err := visit(target)
	return removed, err
}

// DeletePath is Workspace.delete_path.
func DeletePath(root string, body *pyjson.Object) (*pyjson.Object, error) {
	rootPath := project.Resolve(root)
	target, rel, err := resolve(rootPath, body.Value("path"), true)
	if err != nil {
		return nil, err
	}
	if pathx.SamePath(target, rootPath) {
		return nil, werr(400, "the project root cannot be deleted")
	}
	recursive := pyjson.Truthy(body.Value("recursive"))
	removed := 1
	if isDir(target) {
		children, err := listDir(target)
		if err != nil {
			return nil, werr(400, "cannot read folder: %s", osMessage(err))
		}
		if len(children) > 0 && !recursive {
			return nil, werr(409, "folder is not empty; pass recursive to delete it")
		}
		if recursive {
			if removed, err = deleteTree(target); err != nil {
				return nil, werr(400, "cannot delete folder: %s", osMessage(err))
			}
		} else if err := os.Remove(target); err != nil {
			return nil, err
		}
	} else if err := os.Remove(target); err != nil {
		return nil, werr(400, "cannot delete file: %s", osMessage(err))
	}
	return pyjson.NewObject("path", rel, "removed", removed, "recursive", recursive), nil
}
