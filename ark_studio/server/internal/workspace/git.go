package workspace

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"arkstudio/go/internal/platform/procx"

	"arkstudio/go/internal/compat/pydiff"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/compat/pyurl"
	"arkstudio/go/internal/nodehist"
)

var baseRefRe = regexp.MustCompile(`\A[A-Za-z0-9][A-Za-z0-9._/~^@{}-]{0,120}\z`)

type gitResult struct {
	code           int
	stdout, stderr []byte
}

// git is _git: nil when git is missing, cannot start or times out.  It shares
// the server-wide bounded git process pool with node history.
func git(root string, args ...string) *gitResult {
	path, err := exec.LookPath("git")
	if err != nil {
		return nil
	}
	nodehist.Slots <- struct{}{}
	defer func() { <-nodehist.Slots }()
	ctx, cancel := context.WithTimeout(context.Background(), GitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, append([]string{"-C", root}, args...)...)
	procx.HideWindow(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	if ctx.Err() != nil {
		return nil
	}
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			return nil
		}
		code = ee.ExitCode()
	}
	return &gitResult{code, stdout.Bytes(), stderr.Bytes()}
}

func decode(raw []byte) string {
	if utf8.Valid(raw) {
		return string(raw)
	}
	return pyurl.DecodeReplace(raw)
}

type gitInfo struct {
	available, isRepo                  bool
	root, prefix, branch, head, reason string
}

func (g gitInfo) object() *pyjson.Object {
	return pyjson.NewObject("available", g.available, "is_repo", g.isRepo, "root", g.root,
		"prefix", g.prefix, "branch", g.branch, "head", g.head, "reason", g.reason)
}

// gitContext is _git_context: the project's own repository, never an outer one.
func gitContext(root string) gitInfo {
	var info gitInfo
	if _, err := exec.LookPath("git"); err != nil {
		info.reason = "git is not installed"
		return info
	}
	inside := git(root, "rev-parse", "--is-inside-work-tree")
	if inside == nil || inside.code != 0 || pytext.Strip(decode(inside.stdout)) != "true" {
		info.reason = "the project folder is not inside a git working tree"
		return info
	}
	top := git(root, "rev-parse", "--show-toplevel")
	if top == nil || top.code != 0 {
		info.reason = "git could not resolve the repository root"
		return info
	}
	topPath := resolvePath(pytext.Strip(decode(top.stdout)))
	rootPath := resolvePath(root)
	if !IsWithin(topPath, rootPath) {
		info.reason = "the git repository root is outside the project folder"
		return info
	}
	prefix := ""
	if rel, err := filepath.Rel(topPath, rootPath); err == nil {
		prefix = filepath.ToSlash(rel)
	}
	if prefix == "." {
		prefix = ""
	}
	info.available, info.isRepo, info.root, info.prefix = true, true, filepath.Base(topPath), prefix
	if b := git(root, "rev-parse", "--abbrev-ref", "HEAD"); b != nil && b.code == 0 {
		info.branch = pytext.Strip(decode(b.stdout))
	}
	if h := git(root, "rev-parse", "--short", "HEAD"); h != nil && h.code == 0 {
		info.head = pytext.Strip(decode(h.stdout))
	}
	return info
}

func resolvePath(p string) string {
	if abs, err := filepath.Abs(filepath.FromSlash(p)); err == nil {
		p = abs
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return p
}

// checkBase is _check_base.
func checkBase(root, value string) (string, error) {
	base := pytext.Strip(value)
	if base == "" {
		base = "HEAD"
	}
	if !baseRefRe.MatchString(base) || strings.HasPrefix(base, "-") {
		return "", werr(400, "base must be a git ref such as HEAD, a branch, a tag or a commit hash")
	}
	verify := git(root, "rev-parse", "--verify", "--quiet", base+"^{commit}")
	if verify == nil {
		return "", werr(400, "git is not available")
	}
	if verify.code != 0 {
		return "", werr(400, "git base not found: %s", base)
	}
	return base, nil
}

// capText is _cap_text.  The cut compares a code-point index with a byte
// limit, exactly like the Python original.
func capText(text string, limit int) (string, bool) {
	if len(text) <= limit {
		return text, false
	}
	raw := []byte(text)[:limit]
	for len(raw) > 0 && !utf8.Valid(raw) { // decode("utf-8", "ignore") of a cut tail
		raw = raw[:len(raw)-1]
	}
	runes := []rune(string(raw))
	cut := -1
	for i := len(runes) - 1; i >= 0; i-- {
		if runes[i] == '\n' {
			cut = i
			break
		}
	}
	if cut > limit/2 {
		runes = runes[:cut+1]
	}
	return string(runes) + "\n…[diff truncated by ARK]\n", true
}

type numstat struct {
	adds, dels *int64
	binary     bool
}

// numstatEntries parses `git diff --numstat -z`.
func numstatEntries(raw []byte) map[string]numstat {
	out := map[string]numstat{}
	fields := bytes.Split(raw, []byte{0})
	for i := 0; i < len(fields); {
		record := fields[i]
		i++
		if len(record) == 0 {
			continue
		}
		parts := bytes.Split(record, []byte("\t"))
		if len(parts) < 3 {
			continue
		}
		addsRaw, delsRaw, name := parts[0], parts[1], parts[2]
		if len(name) == 0 && i+1 < len(fields) {
			name = fields[i+1]
			i += 2
		}
		binary := string(addsRaw) == "-" && string(delsRaw) == "-"
		var st numstat
		st.binary = binary
		if !binary {
			a, errA := pytext.Int(string(addsRaw))
			d, errD := pytext.Int(string(delsRaw))
			if errA == nil && errD == nil {
				st.adds, st.dels = &a, &d
			}
		}
		out[decode(name)] = st
	}
	return out
}

type nameStatus struct {
	path, status string
}

// nameStatusEntries parses `git diff --name-status -z` (insertion order kept).
func nameStatusEntries(raw []byte) []nameStatus {
	var out []nameStatus
	seen := map[string]int{}
	fields := bytes.Split(raw, []byte{0})
	for i := 0; i < len(fields); {
		record := fields[i]
		i++
		if len(record) == 0 {
			continue
		}
		parts := bytes.Split(record, []byte("\t"))
		status := "M"
		if len(parts[0]) > 0 {
			if parts[0][0] < 0x80 {
				status = string(parts[0][:1])
			} else {
				status = "�"
			}
		}
		var path []byte
		switch {
		case len(parts) >= 3:
			path = parts[2]
		case len(parts) == 2:
			path = parts[1]
		case (status == "R" || status == "C") && i+1 < len(fields):
			path = fields[i+1]
			i += 2
		case i < len(fields):
			path = fields[i]
			i++
		default:
			continue
		}
		key := decode(path)
		if k, ok := seen[key]; ok {
			out[k].status = status
			continue
		}
		seen[key] = len(out)
		out = append(out, nameStatus{key, status})
	}
	return out
}

// countLines is _count_lines for an untracked file.
func countLines(target string) (*int64, bool) {
	fi, err := os.Stat(target)
	if err != nil || fi.Size() > MaxFileBytes {
		return nil, false
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		return nil, false
	}
	if looksBinary(raw) {
		return nil, true
	}
	if !utf8.Valid(raw) {
		return nil, true
	}
	n := int64(lineCount(string(raw)))
	return &n, false
}

func gitPathSkipped(rootPath, path string) bool {
	if isIgnored(path) || isProtected(rootPath, path) {
		return true
	}
	for _, part := range strings.Split(path, "/") {
		if isIgnored(part) {
			return true
		}
	}
	return false
}

func intOrNone(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// Changes is Workspace.changes.
func Changes(root, base string) (*pyjson.Object, error) {
	rootPath := resolvePath(root)
	info := gitContext(rootPath)
	if !info.isRepo {
		b := base
		if b == "" {
			b = "HEAD"
		}
		reason := info.reason
		if reason == "" {
			reason = "not a git repository"
		}
		return pyjson.NewObject("base", b, "git", false, "files", []any{}, "totals", pyjson.NewObject(),
			"truncated", false, "reason", reason), nil
	}
	base, err := checkBase(rootPath, base)
	if err != nil {
		return nil, err
	}
	num := git(rootPath, "diff", "--relative", "--numstat", "-z", base, "--", ".")
	status := git(rootPath, "diff", "--relative", "--name-status", "-z", base, "--", ".")
	if num == nil || status == nil {
		return nil, werr(400, "git diff failed")
	}
	if num.code != 0 || status.code != 0 {
		raw := num.stderr
		if len(raw) == 0 {
			raw = status.stderr
		}
		detail := pytext.Strip(decode(raw))
		if detail == "" {
			detail = "unknown error"
		}
		return nil, werr(400, "git diff failed: %s", detail)
	}
	stats := numstatEntries(num.stdout)
	type file struct {
		path string
		obj  *pyjson.Object
	}
	var files []file
	index := map[string]bool{}
	for _, ns := range nameStatusEntries(status.stdout) {
		if gitPathSkipped(rootPath, ns.path) {
			continue
		}
		st := stats[ns.path]
		index[ns.path] = true
		files = append(files, file{ns.path, pyjson.NewObject("path", ns.path, "status", ns.status,
			"additions", intOrNone(st.adds), "deletions", intOrNone(st.dels), "binary", st.binary)})
	}
	if untracked := git(rootPath, "ls-files", "--others", "--exclude-standard", "-z", "--", "."); untracked != nil && untracked.code == 0 {
		var candidates []string
		for _, name := range bytes.Split(untracked.stdout, []byte{0}) {
			if len(name) == 0 {
				continue
			}
			path := decode(name)
			if gitPathSkipped(rootPath, path) {
				continue
			}
			candidates = append(candidates, path)
		}
		sort.SliceStable(candidates, func(i, j int) bool { return lowerLess(candidates[i], candidates[j]) })
		if len(candidates) > MaxDiffFiles {
			candidates = candidates[:MaxDiffFiles]
		}
		for _, path := range candidates {
			if index[path] {
				continue
			}
			index[path] = true
			adds, binary := countLines(filepath.Join(rootPath, filepath.FromSlash(path)))
			var dels any
			if adds != nil {
				dels = int64(0)
			}
			files = append(files, file{path, pyjson.NewObject("path", path, "status", "A",
				"additions", intOrNone(adds), "deletions", dels, "binary", binary, "untracked", true)})
		}
	}
	sort.SliceStable(files, func(i, j int) bool { return lowerLess(files[i].path, files[j].path) })
	truncated := len(files) > MaxDiffFiles
	if truncated {
		files = files[:MaxDiffFiles]
	}
	ordered := make([]any, len(files))
	var added, deleted int64
	for i, f := range files {
		ordered[i] = f.obj
		if n, ok := f.obj.Value("additions").(int64); ok {
			added += n
		}
		if n, ok := f.obj.Value("deletions").(int64); ok {
			deleted += n
		}
	}
	return pyjson.NewObject("base", base, "git", true, "branch", info.branch, "head", info.head,
		"files", ordered, "totals", pyjson.NewObject("files", len(ordered), "additions", added, "deletions", deleted),
		"truncated", truncated), nil
}

func countPrefixed(text string, add, skip string) int64 {
	var n int64
	for _, line := range pytext.SplitLines(text, false) {
		if strings.HasPrefix(line, add) && !strings.HasPrefix(line, skip) {
			n++
		}
	}
	return n
}

func binaryDiff(rel, base string, deleted bool, message string) *pyjson.Object {
	return pyjson.NewObject("path", rel, "base", base, "changed", true, "binary", true,
		"diff", "", "additions", nil, "deletions", nil, "truncated", false, "deleted", deleted,
		"message", message)
}

// DiffFile is Workspace.diff_file.
func DiffFile(root, path, base string) (*pyjson.Object, error) {
	rootPath := resolvePath(root)
	info := gitContext(rootPath)
	if !info.isRepo {
		reason := info.reason
		if reason == "" {
			reason = "the project folder is not a git repository"
		}
		return nil, werr(400, "%s", reason)
	}
	base, err := checkBase(rootPath, base)
	if err != nil {
		return nil, err
	}
	target, rel, err := resolve(rootPath, path, false)
	if err != nil {
		return nil, err
	}
	if isDir(target) {
		return nil, werr(400, "diff expects a file, not a folder")
	}
	repoRel := rel
	if info.prefix != "" {
		repoRel = info.prefix + "/" + rel
	}
	tracked := git(rootPath, "ls-files", "--error-unmatch", "--", rel)
	isTracked := tracked != nil && tracked.code == 0
	var working []byte
	deleted := !exists(target)
	if !deleted {
		fi, err := os.Stat(target)
		if err != nil {
			return nil, werr(400, "cannot read file: %s", osMessage(err))
		}
		if fi.Size() > MaxFileBytes {
			return nil, werr(413, "file is too large to diff (%d bytes; limit %d)", fi.Size(), MaxFileBytes)
		}
		if working, err = os.ReadFile(target); err != nil {
			return nil, werr(400, "cannot read file: %s", osMessage(err))
		}
	}
	const binaryMsg = "Binary file — the editor shows a text diff only."
	if !isTracked && len(working) > 0 && looksBinary(working) {
		return binaryDiff(rel, base, deleted, binaryMsg), nil
	}
	if isTracked {
		proc := git(rootPath, "diff", "--relative", "--no-color", "--no-ext-diff", "--unified=3", base, "--", rel)
		if proc == nil || proc.code != 0 {
			detail := ""
			if proc != nil {
				detail = pytext.Strip(decode(proc.stderr))
			}
			if detail == "" {
				detail = "unknown error"
			}
			return nil, werr(400, "git diff failed: %s", detail)
		}
		raw := decode(proc.stdout)
		if strings.Contains(raw, "Binary files") || strings.Contains(raw, "GIT binary patch") {
			return binaryDiff(rel, base, deleted, binaryMsg), nil
		}
		changed := pytext.Strip(raw) != ""
		additions := countPrefixed(raw, "+", "+++")
		deletions := countPrefixed(raw, "-", "---")
		if raw == "" && !deleted {
			return pyjson.NewObject("path", rel, "base", base, "changed", false, "binary", false,
				"diff", "", "additions", 0, "deletions", 0, "truncated", false, "deleted", false,
				"message", "No changes against "+base+"."), nil
		}
		text, truncated := capText(raw, MaxDiffBytes)
		return pyjson.NewObject("path", rel, "base", base, "changed", changed, "binary", false,
			"diff", text, "additions", additions, "deletions", deletions,
			"truncated", truncated, "deleted", deleted), nil
	}
	var baseBytes []byte
	if shown := git(rootPath, "show", base+":"+repoRel); shown != nil && shown.code == 0 {
		baseBytes = shown.stdout
	}
	if deleted && len(baseBytes) == 0 {
		return nil, werr(404, "file not found: %s", rel)
	}
	if len(baseBytes) > 0 && looksBinary(baseBytes) {
		return binaryDiff(rel, base, deleted, binaryMsg), nil
	}
	workingText, ok := decodeUTF8Sig(working)
	if !ok {
		return binaryDiff(rel, base, deleted, "Binary or non-UTF-8 file — the editor shows a text diff only."), nil
	}
	if !deleted && workingText == "" && len(baseBytes) == 0 {
		return pyjson.NewObject("path", rel, "base", base, "changed", true, "binary", false,
			"diff", "", "additions", 0, "deletions", 0, "truncated", false,
			"deleted", false, "message", "Empty untracked file."), nil
	}
	baseText, ok := decodeUTF8Sig(baseBytes)
	if !ok {
		baseText = decode(baseBytes)
	}
	tofile := "b/" + rel
	if deleted {
		tofile = "/dev/null"
	}
	diff := pydiff.UnifiedDiff(pytext.SplitLines(baseText, true), pytext.SplitLines(workingText, true), "a/"+rel, tofile, 3)
	text, truncated := capText(diff, MaxDiffBytes)
	return pyjson.NewObject("path", rel, "base", base, "changed", pytext.Strip(diff) != "" || deleted,
		"binary", false, "diff", text, "additions", countPrefixed(diff, "+", "+++"),
		"deletions", countPrefixed(diff, "-", "---"), "truncated", truncated, "deleted", deleted,
		"untracked", true), nil
}
