package workspace

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
)

// The Editor's Timeline view: the git history of one project file
// (git log --follow) and the file's content at one of those commits.  Both
// stay inside the project folder (resolve) and inside the project's own
// repository (gitContext); git runs on the shared bounded pool with
// GitTimeout.

const (
	TimelineDefault = 50
	TimelineMax     = 300
)

var revRe = regexp.MustCompile(`\A[0-9a-fA-F]{4,40}\z`)

// repoRelPath joins the project's prefix inside the repository and rel.
func repoRelPath(info gitInfo, rel string) string {
	if info.prefix == "" {
		return rel
	}
	return info.prefix + "/" + rel
}

// projectRelPath maps a repository path back into the project folder ("" and
// false when the path lies outside it, e.g. before a rename into the project).
func projectRelPath(info gitInfo, repoPath string) (string, bool) {
	if info.prefix == "" {
		return repoPath, true
	}
	if rest, ok := strings.CutPrefix(repoPath, info.prefix+"/"); ok {
		return rest, true
	}
	return "", false
}

func notRepo(rel string, info gitInfo) *pyjson.Object {
	reason := info.reason
	if reason == "" {
		reason = "the project folder is not a git repository"
	}
	return pyjson.NewObject("path", rel, "is_repo", false, "reason", reason, "entries", []any{}, "truncated", false)
}

// Timeline lists the commits that touched path (following renames), newest
// first, at most limit of them.  A project outside git is not an error: the
// reply says is_repo false so the view can show its empty state.
func Timeline(root, path string, limit int64) (*pyjson.Object, error) {
	rootPath := resolvePath(root)
	target, rel, err := resolve(rootPath, path, false)
	if err != nil {
		return nil, err
	}
	if rel == "" {
		return nil, werr(400, "path is required")
	}
	if exists(target) && isDir(target) {
		return nil, werr(400, "the timeline expects a file, not a folder")
	}
	info := gitContext(rootPath)
	if !info.isRepo {
		return notRepo(rel, info), nil
	}
	if limit <= 0 {
		limit = TimelineDefault
	}
	limit = min(limit, TimelineMax)
	// One record per commit: \x1e, the fields split by \x1f, then the
	// file's (repository-relative) name at that commit from --name-only.
	proc := git(rootPath, "-c", "core.quotepath=off", "log", "--follow", "--no-color", "--no-ext-diff",
		"--format=%x1e%H%x1f%h%x1f%an%x1f%aI%x1f%s", "--name-only",
		"-n", strconv.FormatInt(limit+1, 10), "--", rel)
	if proc == nil {
		return nil, werr(504, "git did not answer in time")
	}
	if proc.code != 0 {
		detail := pytext.Strip(decode(proc.stderr))
		// A path git has never seen is an empty history, not a failure.
		if strings.Contains(detail, "unknown revision") || strings.Contains(detail, "does not have any commits") {
			return pyjson.NewObject("path", rel, "is_repo", true, "reason", "", "entries", []any{}, "truncated", false), nil
		}
		if detail == "" {
			detail = "unknown error"
		}
		return nil, werr(400, "git log failed: %s", detail)
	}
	entries := []any{}
	for _, record := range bytes.Split(proc.stdout, []byte{0x1e}) {
		lines := strings.Split(strings.TrimSpace(decode(record)), "\n")
		if len(lines) == 0 || lines[0] == "" {
			continue
		}
		fields := strings.Split(lines[0], "\x1f")
		if len(fields) < 5 || !revRe.MatchString(fields[0]) {
			continue
		}
		at := ""
		for _, line := range lines[1:] {
			if s := strings.TrimSpace(line); s != "" {
				at = s
			}
		}
		entry := pyjson.NewObject("rev", fields[0], "short", fields[1], "author", fields[2],
			"time", fields[3], "subject", fields[4])
		if at == "" {
			entry.Set("path", rel).Set("inside", true)
		} else if p, ok := projectRelPath(info, at); ok {
			entry.Set("path", p).Set("inside", true)
		} else {
			entry.Set("path", at).Set("inside", false)
		}
		entries = append(entries, entry)
	}
	truncated := int64(len(entries)) > limit
	if truncated {
		entries = entries[:limit]
	}
	return pyjson.NewObject("path", rel, "is_repo", true, "reason", "", "entries", entries,
		"truncated", truncated, "branch", info.branch), nil
}

// ShowFile returns path's text at commit rev (read-only).  rev must be a
// commit hash (4 to 40 hex digits); the file must be UTF-8 text within
// MaxFileBytes.
func ShowFile(root, path, rev string) (*pyjson.Object, error) {
	rootPath := resolvePath(root)
	rev = pytext.Strip(rev)
	if !revRe.MatchString(rev) {
		return nil, werr(400, "rev must be a commit hash")
	}
	_, rel, err := resolve(rootPath, path, false)
	if err != nil {
		return nil, err
	}
	if rel == "" {
		return nil, werr(400, "path is required")
	}
	info := gitContext(rootPath)
	if !info.isRepo {
		reason := info.reason
		if reason == "" {
			reason = "the project folder is not a git repository"
		}
		return nil, werr(400, "%s", reason)
	}
	verify := git(rootPath, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if verify == nil {
		return nil, werr(504, "git did not answer in time")
	}
	if verify.code != 0 {
		return nil, werr(404, "commit not found: %s", rev)
	}
	full := pytext.Strip(decode(verify.stdout))
	spec := full + ":" + repoRelPath(info, rel)
	size := git(rootPath, "cat-file", "-s", spec)
	if size == nil {
		return nil, werr(504, "git did not answer in time")
	}
	if size.code != 0 {
		return nil, werr(404, "%s did not exist at %s", rel, full[:min(len(full), 7)])
	}
	if n, err := strconv.ParseInt(pytext.Strip(decode(size.stdout)), 10, 64); err == nil && n > MaxFileBytes {
		return nil, werr(413, "file is too large to show (%d bytes; limit %d)", n, MaxFileBytes)
	}
	shown := git(rootPath, "cat-file", "blob", spec)
	if shown == nil {
		return nil, werr(504, "git did not answer in time")
	}
	if shown.code != 0 {
		return nil, werr(404, "%s did not exist at %s", rel, full[:min(len(full), 7)])
	}
	raw := shown.stdout
	if looksBinary(raw) {
		return nil, werr(415, "binary file; only text versions can be shown")
	}
	text, ok := decodeUTF8Sig(raw)
	if !ok {
		return nil, werr(415, "file is not UTF-8 text")
	}
	out := pyjson.NewObject("path", rel, "rev", full, "short", full[:min(len(full), 7)], "content", text,
		"size", len(raw), "lines", lineCount(text), "language", Language(rel), "hash", hashBytes(raw), "read_only", true)
	if meta := git(rootPath, "-c", "core.quotepath=off", "log", "-1", "--no-color", "--format=%an%x1f%aI%x1f%s", full); meta != nil && meta.code == 0 {
		if fields := strings.SplitN(pytext.Strip(decode(meta.stdout)), "\x1f", 3); len(fields) == 3 {
			out.Set("author", fields[0]).Set("time", fields[1]).Set("subject", fields[2])
		}
	}
	return out, nil
}
