package setup

// Task folders and writable places (2026-11-25, T-0128).
//
// An evaluation of where agents put their files (T-0128, X-0007) showed
// that agents follow the task-space rule when ARK names the folder, and go
// wrong when they have to build it themselves: started on an experiment
// they named the folder after the experiment, and with nothing named they
// used the session workspace.  So ARK now names the exact folder:
//
//   TaskFolderName   <YYYYMMDD>_<task ID>-<slug>, the folder of one task
//   FindTaskFolder   an existing folder of that task in the task space
//   WritableRoots    the places registered for a device (repositories,
//                    task space, storage, artifacts) from Paths
//   ScratchPlace     a folder that must not become a task space (the OS
//                    temp dir, an ARK session workspace)

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"arkstudio/go/internal/compat/pyjson"
)

const maxSlugRunes = 40

// Slug is a folder-name form of a title: letters and digits kept (any
// script, lower-cased), every other run of characters one "-", at most 40
// characters.
func Slug(title string) string {
	var b strings.Builder
	dash := false
	n := 0
	for _, r := range strings.ToLower(title) {
		if n >= maxSlugRunes {
			break
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			dash = false
			n++
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
			n++
		}
	}
	return strings.Trim(b.String(), "-")
}

// TaskFolderName is "<YYYYMMDD>_<uid>-<slug>" ("<YYYYMMDD>_<uid>" for an
// empty slug); date is any ISO date or time ("2026-11-25T08:00:00Z").
func TaskFolderName(date, uid, title string) string {
	d := strings.ReplaceAll(date, "-", "")
	if len(d) >= 8 {
		d = d[:8]
	}
	name := d + "_" + uid
	if s := Slug(title); s != "" {
		name += "-" + s
	}
	return name
}

// FindTaskFolder is the existing folder of task uid directly in root
// ("<8 digits>_<uid>" or "<8 digits>_<uid>-..."), "" when there is none or
// root is not a folder on this machine.  The newest name wins.
func FindTaskFolder(root, uid string) string {
	if root == "" || uid == "" {
		return ""
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	re := regexp.MustCompile(`^\d{8}_` + regexp.QuoteMeta(uid) + `(?:[-_].*)?$`)
	found := ""
	for _, e := range entries {
		if e.IsDir() && re.MatchString(e.Name()) && e.Name() > found {
			found = e.Name()
		}
	}
	if found == "" {
		return ""
	}
	return JoinTaskPath(root, found)
}

// WritableRoots are the places registered for the device of a Paths
// result, each {kind, key, name, path}: the task space, then the
// repositories and storage with a path on that device, then artifacts.
func WritableRoots(paths *pyjson.Object) []any {
	out := []any{}
	seen := map[string]bool{}
	add := func(kind string, e *obj) {
		if e == nil {
			return
		}
		p, _ := e.Value("path").(string)
		if p == "" || seen[lower(p)] {
			return
		}
		seen[lower(p)] = true
		out = append(out, pyjson.NewObject("kind", kind, "key", e.Value("key"), "name", e.Value("name"), "path", p))
	}
	if paths == nil {
		return out
	}
	add("task_space", mapOf(paths.Value("task_space")))
	for _, x := range listOr(paths.Value("repositories")) {
		add("repository", mapOf(x))
	}
	for _, x := range listOr(paths.Value("storage")) {
		add("storage", mapOf(x))
	}
	if a, _ := paths.Value("artifacts").(string); a != "" {
		add("artifacts", pyjson.NewObject("key", "artifacts", "name", "artifacts", "path", a))
	}
	return out
}

// scratchRoots are the OS temp folders on every platform ARK runs on.
func scratchRoots() []string {
	roots := []string{"/tmp", "/var/tmp", "/dev/shm", "/private/tmp", "/private/var/folders"}
	if t := os.TempDir(); t != "" {
		roots = append(roots, t)
	}
	for _, k := range []string{"TMPDIR", "TEMP", "TMP"} {
		if t := os.Getenv(k); t != "" {
			roots = append(roots, t)
		}
	}
	return roots
}

func normPath(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	p = strings.TrimRight(p, "/")
	if len(p) >= 2 && p[1] == ':' { // Windows paths compare case-insensitively
		p = strings.ToLower(p)
	}
	return p
}

// Inside reports whether path is root or inside it (both absolute).
func Inside(path, root string) bool {
	p, r := normPath(path), normPath(root)
	if p == "" || r == "" {
		return false
	}
	if filepath.Separator == '\\' || (len(r) >= 2 && r[1] == ':') {
		p, r = strings.ToLower(p), strings.ToLower(r)
	}
	return p == r || strings.HasPrefix(p, r+"/")
}

// ScratchPlace explains why path must not be a task space ("" = fine): it
// is in the OS temp dir, in an ARK session workspace, or in one of avoid
// (the server's sessions root).
func ScratchPlace(path string, avoid ...string) string {
	for _, r := range scratchRoots() {
		if Inside(path, r) {
			return "it is in the OS temp dir (" + r + "), which is cleaned"
		}
	}
	for _, r := range avoid {
		if r != "" && Inside(path, r) {
			return "it is in ARK's session folders, which are not kept"
		}
	}
	if strings.Contains(normPath(path)+"/", "/agent-sessions/") {
		return "it is in ARK's session folders, which are not kept"
	}
	return ""
}
