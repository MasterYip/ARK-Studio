// Package nodehist ports arkstudio/node_history.py: immutable, text-only node
// revisions in a project-local bare Git repository.
//
// Every Git call runs through a process-wide bounded pool (Slots), so a burst
// of saves cannot fork an unbounded number of git processes.
package nodehist

import (
	"arkstudio/go/internal/platform/procx"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"arkstudio/go/internal/compat/pyjson"
)

// Error is HistoryError (a ValueError).
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

// Slots bounds concurrent git processes for the whole server.
var Slots = make(chan struct{}, 8)

// CommitTime, when set and reporting ok, pins the Git author/committer date
// (differential replay only: the server sets it under ARK_REPLAY_CLOCK=1).
var CommitTime func() (time.Time, bool)

// Timeout is the subprocess timeout (Python: 20 s).
const Timeout = 20 * time.Second

// History is one project's history.git.
type History struct{ Path string }

// Available is shutil.which("git").
func Available() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

func devnull() string {
	if os.PathSeparator == '\\' {
		return "nul"
	}
	return "/dev/null"
}

func (h *History) git(ctx context.Context, data []byte, actor string, args ...string) (string, error) {
	if !Available() {
		return "", &Error{"Node history requires Git on the ARK server"}
	}
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(kv), "GIT_") {
			env = append(env, kv)
		}
	}
	var nb strings.Builder
	for _, r := range actor {
		if isPrintable(r) && r != '<' && r != '>' {
			nb.WriteRune(r)
		}
	}
	name := strings.TrimFunc(nb.String(), unicode.IsSpace)
	if utf8.RuneCountInString(name) > 120 {
		name = string([]rune(name)[:120])
	}
	if name == "" {
		name = "ARK"
	}
	null := devnull()
	if CommitTime != nil {
		if t, ok := CommitTime(); ok {
			stamp := strconv.FormatInt(t.Unix(), 10) + " +0000"
			env = append(env, "GIT_AUTHOR_DATE="+stamp, "GIT_COMMITTER_DATE="+stamp)
		}
	}
	env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+null,
		"GIT_AUTHOR_NAME="+name, "GIT_COMMITTER_NAME="+name,
		"GIT_AUTHOR_EMAIL=ark@localhost", "GIT_COMMITTER_EMAIL=ark@localhost")
	full := append([]string{"--no-pager", "-c", "core.hooksPath=" + null, "-c", "core.attributesFile=" + null,
		"-c", "core.fsmonitor=false", "--git-dir=" + h.Path}, args...)
	select {
	case Slots <- struct{}{}:
	case <-ctx.Done():
		return "", &Error{"Could not access node history: " + ctx.Err().Error()}
	}
	defer func() { <-Slots }()
	cctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", full...)
	procx.HideWindow(cmd)
	cmd.Env = env
	if data != nil {
		cmd.Stdin = bytes.NewReader(data)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if cctx.Err() != nil {
		return "", &Error{"Could not access node history: Command '" + strings.Join(append([]string{"git"}, full...), "', '") + "' timed out after 20 seconds"}
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return "", &Error{"Could not access node history: " + err.Error()}
	}
	if err != nil {
		msg := strings.TrimFunc(strings.ToValidUTF8(stderr.String(), "�"), unicode.IsSpace)
		if utf8.RuneCountInString(msg) > 500 {
			msg = string([]rune(msg)[:500])
		}
		return "", &Error{"Git node history failed: " + msg}
	}
	if !utf8.Valid(stdout.Bytes()) {
		return "", &Error{"'utf-8' codec can't decode git output"}
	}
	return stdout.String(), nil
}

func isPrintable(r rune) bool {
	return unicode.IsPrint(r) || r == ' '
}

func (h *History) init(ctx context.Context) error {
	if st, err := os.Stat(filepath.Join(h.Path, "HEAD")); err == nil && !st.IsDir() {
		return nil
	}
	_, err := h.git(ctx, nil, "ARK", "init", "--bare", "--quiet", "--template=", h.Path)
	return err
}

var hashRe = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func validateHash(commit string) error {
	if !hashRe.MatchString(commit) {
		return &Error{"A full revision hash is required"}
	}
	return nil
}

// Record is NodeHistory.record.
func (h *History) Record(ctx context.Context, uid, title, body, parent, actor, message string) (string, error) {
	if err := h.init(ctx); err != nil {
		return "", err
	}
	if parent != "" {
		if err := validateHash(parent); err != nil {
			return "", err
		}
	}
	var files strings.Builder
	for _, f := range [][2]string{{"body.md", body}, {"title.txt", title}} {
		blob, err := h.git(ctx, []byte(f[1]), "ARK", "hash-object", "-w", "--stdin")
		if err != nil {
			return "", err
		}
		files.WriteString("100644 blob " + strings.TrimSpace(blob) + "\t" + f[0] + "\n")
	}
	tree, err := h.git(ctx, []byte(files.String()), "ARK", "mktree")
	if err != nil {
		return "", err
	}
	tree = strings.TrimSpace(tree)
	if parent != "" {
		pt, err := h.git(ctx, nil, "ARK", "rev-parse", parent+"^{tree}")
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(pt) == tree {
			return parent, nil
		}
	}
	args := []string{"commit-tree", tree}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	commit, err := h.git(ctx, []byte(uid+": "+message+"\n"), actor, args...)
	if err != nil {
		return "", err
	}
	commit = strings.TrimSpace(commit)
	if _, err := h.git(ctx, nil, "ARK", "update-ref", "refs/ark/revisions/"+commit, commit); err != nil {
		return "", err
	}
	return commit, nil
}

// Validate is NodeHistory.validate.
func (h *History) Validate(ctx context.Context, head, commit string) (string, error) {
	if err := validateHash(commit); err != nil {
		return "", err
	}
	if err := validateHash(head); err != nil {
		return "", err
	}
	ancestor, err := h.git(ctx, nil, "ARK", "merge-base", head, commit)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(ancestor) != commit {
		return "", &Error{"Revision does not belong to this node's history"}
	}
	return commit, nil
}

// Versions is NodeHistory.versions.
func (h *History) Versions(ctx context.Context, head string, limit, offset int64) ([]any, error) {
	if err := validateHash(head); err != nil {
		return nil, err
	}
	raw, err := h.git(ctx, nil, "ARK", "log", "--format=%H%x00%aI%x00%an%x00%s", "--skip="+strconv.FormatInt(offset, 10),
		"--max-count="+strconv.FormatInt(limit, 10), head, "--")
	if err != nil {
		return nil, err
	}
	out := []any{}
	for _, line := range splitLines(raw) {
		parts := strings.SplitN(line, "\x00", 4)
		o := pyjson.NewObject()
		for i, k := range []string{"id", "created_at", "author", "message"} {
			if i < len(parts) {
				o.Set(k, parts[i])
			}
		}
		out = append(out, o)
	}
	return out, nil
}

// splitLines is str.splitlines() for git output.
func splitLines(s string) []string {
	var out []string
	start := 0
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			out = append(out, string(rs[start:i]))
			if rs[i] == '\r' && i+1 < len(rs) && rs[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	if start < len(rs) {
		out = append(out, string(rs[start:]))
	}
	return out
}

// Revision is NodeHistory.revision.
func (h *History) Revision(ctx context.Context, head, commit string) (*pyjson.Object, error) {
	if _, err := h.Validate(ctx, head, commit); err != nil {
		return nil, err
	}
	title, err := h.git(ctx, nil, "ARK", "show", commit+":title.txt")
	if err != nil {
		return nil, err
	}
	body, err := h.git(ctx, nil, "ARK", "show", commit+":body.md")
	if err != nil {
		return nil, err
	}
	return pyjson.NewObject("id", commit, "title", title, "body", body), nil
}

// Diff is NodeHistory.diff ("" = None).
func (h *History) Diff(ctx context.Context, head, before, after string) (*pyjson.Object, error) {
	if after == "" {
		after = head
	}
	after, err := h.Validate(ctx, head, after)
	if err != nil {
		return nil, err
	}
	if before != "" {
		if before, err = h.Validate(ctx, head, before); err != nil {
			return nil, err
		}
	} else {
		raw, err := h.git(ctx, nil, "ARK", "rev-list", "--parents", "--max-count=1", after)
		if err != nil {
			return nil, err
		}
		parents := strings.Fields(raw)
		if len(parents) > 1 {
			before = parents[1]
		}
	}
	empty, err := h.git(ctx, []byte{}, "ARK", "hash-object", "-t", "tree", "--stdin")
	if err != nil {
		return nil, err
	}
	from := before
	if from == "" {
		from = strings.TrimSpace(empty)
	}
	diff, err := h.git(ctx, nil, "ARK", "diff", "--no-ext-diff", "--no-textconv", "--no-color", "--unified=3",
		from, after, "--", "title.txt", "body.md")
	if err != nil {
		return nil, err
	}
	changes, err := ChangeBlocks(diff)
	if err != nil {
		return nil, err
	}
	var fromV any
	if before != "" {
		fromV = before
	}
	return pyjson.NewObject("from", fromV, "to", after, "diff", diff, "changes", changes), nil
}

var hunkRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// ChangeBlocks is NodeHistory.change_blocks.
func ChangeBlocks(diff string) ([]any, error) {
	changes := []any{}
	var file any
	inHunk := false
	var block *pyjson.Object
	var before, after, index int64
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			i := strings.LastIndex(line, " b/")
			if i >= 0 {
				file = line[i+3:]
			} else {
				file = line
			}
			index, block, inHunk = 0, nil, false
		case strings.HasPrefix(line, "@@ "):
			m := hunkRe.FindStringSubmatch(line)
			if m == nil {
				return nil, &Error{"Invalid Git diff hunk"}
			}
			old, _ := strconv.ParseInt(m[1], 10, 64)
			nw, _ := strconv.ParseInt(m[3], 10, 64)
			before, after = old, nw
			if m[2] != "0" {
				before--
			}
			if m[4] != "0" {
				after--
			}
			block, inHunk = nil, true
		case inHunk && (strings.HasPrefix(line, "-") || strings.HasPrefix(line, "+")):
			if block == nil {
				block = pyjson.NewObject("file", file, "index", index, "before_start", before,
					"after_start", after, "before_count", int64(0), "after_count", int64(0))
				changes = append(changes, block)
				index++
			}
			if strings.HasPrefix(line, "-") {
				block.Set("before_count", block.Value("before_count").(int64)+1)
				before++
			} else {
				block.Set("after_count", block.Value("after_count").(int64)+1)
				after++
			}
		case inHunk && strings.HasPrefix(line, " "):
			before++
			after++
			block = nil
		}
	}
	return changes, nil
}

// ReapplyBlock is NodeHistory.reapply_block (indices are str positions of
// lines; counts come from ChangeBlocks).
func ReapplyBlock(old, new string, block *pyjson.Object) string {
	lines := func(text string) []string {
		parts := strings.Split(text, "\n")
		out := make([]string, 0, len(parts))
		for _, p := range parts[:len(parts)-1] {
			out = append(out, p+"\n")
		}
		if last := parts[len(parts)-1]; last != "" {
			out = append(out, last)
		}
		return out
	}
	source, target := lines(old), lines(new)
	get := func(k string) int { v, _ := block.Value(k).(int64); return int(v) }
	start, count := get("after_start"), get("after_count")
	ss, sc := get("before_start"), get("before_count")
	slice := func(xs []string, a, n int) []string {
		if a < 0 {
			a = max(0, len(xs)+a)
		}
		a = min(a, len(xs))
		b := min(a+n, len(xs))
		if b < a {
			b = a
		}
		return xs[a:b]
	}
	if start < 0 {
		start = max(0, len(target)+start)
	}
	start = min(start, len(target))
	end := min(start+count, len(target))
	if end < start {
		end = start
	}
	repl := slice(source, ss, sc)
	out := append(append(append([]string{}, target[:start]...), repl...), target[end:]...)
	return strings.Join(out, "")
}
