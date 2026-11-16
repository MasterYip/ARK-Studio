// Package worktrees ports arkstudio/worktrees.py: linked Git worktrees for
// agent sessions under a safe root next to the project's repository.
// Every git call shares the server-wide git process pool (nodehist.Slots).
package worktrees

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"arkstudio/go/internal/platform/pathx"

	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/platform/procx"
	"arkstudio/go/internal/platform/textfile"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pyos"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/compat/pyurl"
	"arkstudio/go/internal/nodehist"
	"arkstudio/go/internal/project"
)

// Constants of worktrees.py.
const (
	DefaultBranchPrefix = "agent/"
	maxBranchLength     = 100
	maxNameLength       = 48
	MaxWorktrees        = 12
	gitTimeout          = 20 * time.Second
)

var (
	safeBranch    = regexp.MustCompile(`\A[A-Za-z0-9][A-Za-z0-9._/-]{0,99}\z`)
	badSegment    = regexp.MustCompile(`(^\.)|(\.\.)|(\.lock\n?\z)|(@\{)|(//)|([\\\s~^:?*\[\]])`)
	slugJunk      = regexp.MustCompile(`[^a-z0-9]+`)
	dashes        = regexp.MustCompile(`-{2,}`)
	dirJunk       = regexp.MustCompile(`[\\/:*?"<>|\s]+`)
	unicodeSpaces = regexp.MustCompile(`[\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x1c-\x1f]`)
)

// Error is WorktreeError.
type Error struct {
	Message string
	Status  int
	Code    string
}

func (e *Error) Error() string { return e.Message }

func newErr(message string, status int, code string) *Error {
	if status == 0 {
		status = 400
	}
	if code == "" {
		code = "WORKTREE_ERROR"
	}
	return &Error{message, status, code}
}

type gitResult struct {
	code           int
	stdout, stderr string
}

func decode(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	return pyurl.DecodeReplace(b)
}

// runGit is run_git_command: no shell, a 20 s timeout (124), and 127 when
// git cannot start.
func runGit(args []string, cwd string) gitResult {
	nodehist.Slots <- struct{}{}
	defer func() { <-nodehist.Slots }()
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	procx.HideWindow(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return gitResult{124, "", "git timed out"}
	}
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			return gitResult{127, "", err.Error()}
		}
		return gitResult{ee.ExitCode(), textfile.Universal(decode(stdout.Bytes())), textfile.Universal(decode(stderr.Bytes()))}
	}
	return gitResult{0, textfile.Universal(decode(stdout.Bytes())), textfile.Universal(decode(stderr.Bytes()))}
}

func inside(path, root string) bool {
	if pathx.SamePath(path, root) {
		return true
	}
	for p := filepath.Dir(path); ; p = filepath.Dir(p) {
		if pathx.SamePath(p, root) {
			return true
		}
		if filepath.Dir(p) == p {
			return false
		}
	}
}

func resolve(p string) string { return project.Resolve(pyos.ExpandUser(p)) }

// SlugifyName is slugify_name.
func SlugifyName(value any) (string, error) {
	s := ""
	if pyjson.Truthy(value) {
		s = pyjson.Str(value)
	}
	text := strings.Trim(slugJunk.ReplaceAllString(pytext.Lower(s), "-"), "-")
	text = strings.Trim(pytext.Prefix(dashes.ReplaceAllString(text, "-"), maxNameLength), "-")
	if text == "" {
		return "", newErr("a worktree name with at least one letter or digit is required", 400, "INVALID_WORKTREE_NAME")
	}
	return text, nil
}

// ValidateBranch is validate_branch.
func ValidateBranch(value any) (string, error) {
	s := ""
	if pyjson.Truthy(value) {
		s = pyjson.Str(value)
	}
	branch := pytext.Strip(s)
	if branch == "" {
		return "", newErr("branch name is required", 400, "INVALID_BRANCH")
	}
	if pytext.Len(branch) > maxBranchLength {
		return "", newErr("branch name is too long", 400, "INVALID_BRANCH")
	}
	invalid := newErr("invalid branch name: "+branch, 400, "INVALID_BRANCH")
	if strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") ||
		strings.HasSuffix(branch, ".") || branch == "HEAD" || badSegment.MatchString(branch) ||
		unicodeSpaces.MatchString(branch) || !safeBranch.MatchString(branch) {
		return "", invalid
	}
	for _, seg := range strings.Split(branch, "/") {
		if seg == "" || strings.HasPrefix(seg, ".") || strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, ".lock") {
			return "", invalid
		}
	}
	return branch, nil
}

// SanitizeDirectory is sanitize_directory.
func SanitizeDirectory(branch string) (string, error) {
	value := strings.Trim(dirJunk.ReplaceAllString(branch, "-"), "-.")
	value = strings.Trim(pytext.Prefix(dashes.ReplaceAllString(value, "-"), 80), "-")
	if value == "" {
		return "", newErr("branch name cannot be converted to a folder name", 400, "INVALID_WORKTREE_FOLDER")
	}
	return value, nil
}

// Entry is one parsed `git worktree list --porcelain` record.
type Entry struct {
	Path, Head, Branch, HeadShort  string
	Detached, Locked, Prunable     bool
	LockedReason, PrunableReason   string
	hasLocked, hasPrunable, hasRef bool
}

// ParsePorcelain is parse_worktree_porcelain.
func ParsePorcelain(text string) []Entry {
	var entries []Entry
	var cur *Entry
	ref := ""
	flush := func() {
		if cur != nil {
			e := *cur
			if strings.HasPrefix(ref, "refs/heads/") {
				e.Branch = ref[len("refs/heads/"):]
			} else {
				e.Branch = ref
			}
			e.Detached = e.Detached || e.Branch == ""
			e.HeadShort = pytext.Prefix(e.Head, 12)
			entries = append(entries, e)
		}
		cur, ref = nil, ""
	}
	lines := append(pytext.SplitLines(text, false), "")
	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		if pytext.Strip(line) == "" {
			flush()
			continue
		}
		key, value, _ := strings.Cut(line, " ")
		if cur == nil && key != "worktree" {
			cur = &Entry{}
		}
		switch key {
		case "worktree":
			flush()
			cur = &Entry{Path: pytext.Strip(value)}
		case "HEAD":
			cur.Head = pytext.Strip(value)
		case "branch":
			ref = pytext.Strip(value)
		case "detached":
			cur.Detached = true
		case "locked":
			cur.Locked, cur.LockedReason = true, pytext.Strip(value)
		case "prunable":
			cur.Prunable, cur.PrunableReason = true, pytext.Strip(value)
		}
	}
	return entries
}

// Manager is WorktreeManager.
type Manager struct {
	// ProjectPath returns the open project's folder ("" when none) or the
	// error the server's require() raises.
	ProjectPath  func() (string, error)
	BranchPrefix string
	RootOverride string
	Max          int
	mu           *sync.Mutex
}

// For returns a view of m bound to one request's project; every view
// shares m's lock (WorktreeManager._lock).
func (m *Manager) For(projectPath func() (string, error)) *Manager {
	cp := *m
	cp.ProjectPath = projectPath
	return &cp
}

// New is WorktreeManager(project_provider=...) with the environment
// defaults.
func New(projectPath func() (string, error)) *Manager {
	prefix := os.Getenv("ARK_AGENT_WORKTREE_BRANCH_PREFIX")
	if prefix == "" {
		prefix = DefaultBranchPrefix
	}
	return &Manager{ProjectPath: projectPath, BranchPrefix: prefix, Max: MaxWorktrees, mu: &sync.Mutex{}}
}

func (m *Manager) git(args []string, cwd string) gitResult { return runGit(args, cwd) }

func requireOK(r gitResult, what, code string) (string, error) {
	if r.code != 0 {
		detail := r.stderr
		if detail == "" {
			detail = r.stdout
		}
		detail = pytext.Strip(detail)
		if detail != "" {
			lines := pytext.SplitLines(detail, false)
			detail = pytext.Prefix(lines[len(lines)-1], 240)
		} else {
			detail = "exit " + strconv.Itoa(r.code)
		}
		return "", newErr(what+" failed: "+detail, 409, code)
	}
	return r.stdout, nil
}

// Repository is the repository() dict.
type Repository struct{ Root, Name, Branch, Head string }

func (r Repository) object(mainBranch *string) *pyjson.Object {
	o := pyjson.NewObject("root", r.Root, "name", r.Name, "branch", r.Branch, "head", r.Head)
	if mainBranch != nil {
		o.Set("main_branch", *mainBranch)
	}
	return o
}

func (m *Manager) projectPath() (string, error) {
	raw, err := m.ProjectPath()
	if err != nil {
		return "", err
	}
	raw = pytext.Strip(raw)
	if raw == "" {
		return "", newErr("no ARK project is open", 409, "NO_PROJECT")
	}
	return resolve(raw), nil
}

// RepositoryInfo is repository().
func (m *Manager) RepositoryInfo() (Repository, error) {
	proj, err := m.projectPath()
	if err != nil {
		return Repository{}, err
	}
	r := m.git([]string{"rev-parse", "--show-toplevel"}, proj)
	if r.code != 0 {
		return Repository{}, newErr("The current ARK project is not inside a Git repository. "+
			"Open a project that lives in a checkout to create agent worktrees.", 409, "NOT_A_GIT_REPOSITORY")
	}
	root := resolve(pytext.Strip(r.stdout))
	if !pyos.IsDir(root) {
		return Repository{}, newErr("git reported a repository root that does not exist", 409, "NOT_A_GIT_REPOSITORY")
	}
	branch, head := "", ""
	if c := m.git([]string{"rev-parse", "--abbrev-ref", "HEAD"}, root); c.code == 0 {
		branch = pytext.Strip(c.stdout)
	}
	if h := m.git([]string{"rev-parse", "--short", "HEAD"}, root); h.code == 0 {
		head = pytext.Strip(h.stdout)
	}
	if branch == "HEAD" {
		branch = ""
	}
	return Repository{root, filepath.Base(root), branch, head}, nil
}

// SafeRoot is safe_root().
func (m *Manager) SafeRoot(repo Repository) (string, error) {
	root := m.RootOverride
	if root == "" {
		if envRoot := pytext.Strip(os.Getenv("ARK_AGENT_WORKTREE_ROOT")); envRoot != "" {
			root = pyos.ExpandUser(envRoot)
			if !filepath.IsAbs(root) {
				root = filepath.Join(repo.Root, root)
			}
		} else {
			root = filepath.Join(filepath.Dir(repo.Root), filepath.Base(repo.Root)+"-worktrees")
		}
	}
	resolved := resolve(root)
	if pathx.SamePath(resolved, resolve(repo.Root)) {
		return "", newErr("the worktree root cannot be the repository root", 409, "UNSAFE_ROOT")
	}
	return resolved, nil
}

func (m *Manager) entries(repoRoot string) ([]Entry, error) {
	out, err := requireOK(m.git([]string{"worktree", "list", "--porcelain"}, repoRoot), "listing worktrees", "GIT_WORKTREE_LIST")
	if err != nil {
		return nil, err
	}
	return ParsePorcelain(out), nil
}

// statusCount is _status_count; ok false is None.
func (m *Manager) statusCount(path string) (int, bool) {
	if !pyos.Exists(path) {
		return 0, false
	}
	r := m.git([]string{"status", "--porcelain=v1", "-z"}, path)
	if r.code != 0 {
		return 0, false
	}
	n := 0
	for _, e := range strings.Split(r.stdout, "\x00") {
		if pytext.Strip(e) != "" {
			n++
		}
	}
	return n, true
}

func (m *Manager) describe(e Entry, repoRoot, root, mainBranch string) *pyjson.Object {
	path := resolve(e.Path)
	dirty, known := m.statusCount(path)
	var dirtyV any
	if known {
		dirtyV = int64(dirty)
	}
	main := pathx.SamePath(path, repoRoot)
	managed := inside(path, root) && !main
	exists := pyos.Exists(path)
	branch, mb := e.Branch, ""
	if main {
		branch, mb = "", mainBranch
	}
	status := "main"
	if !main {
		switch {
		case !exists:
			status = "missing"
		case known && dirty > 0:
			status = "dirty"
		default:
			status = "clean"
		}
	}
	return pyjson.NewObject(
		"path", path,
		"name", filepath.Base(path),
		"branch", branch,
		"main_branch", mb,
		"head", e.HeadShort,
		"main", main,
		"managed", managed,
		"detached", e.Detached,
		"locked", e.Locked,
		"prunable", e.Prunable,
		"missing", !exists,
		"dirty", dirtyV,
		"clean", known && dirty == 0,
		"active_sessions", []any{},
		"status", status,
	)
}

func unavailable(err *Error, repo *pyjson.Object) *pyjson.Object {
	var r any
	if repo != nil {
		r = repo
	}
	return pyjson.NewObject("available", false, "reason", err.Message, "code", err.Code,
		"repository", r, "root", "", "worktrees", []any{}, "total", int64(0), "generated_at", clock.UTCNow())
}

// List is list().
func (m *Manager) List() (*pyjson.Object, error) {
	repo, err := m.RepositoryInfo()
	if err != nil {
		var we *Error
		if errors.As(err, &we) {
			return unavailable(we, nil), nil
		}
		return nil, err
	}
	repoRoot := resolve(repo.Root)
	root, err := m.SafeRoot(repo)
	if err != nil {
		return unavailable(err.(*Error), repo.object(nil)), nil
	}
	entries, err := m.entries(repoRoot)
	if err != nil {
		return nil, err
	}
	mainBranch := ""
	for _, e := range entries {
		if pathx.SamePath(resolve(e.Path), repoRoot) {
			mainBranch = e.Branch
			break
		}
	}
	var worktrees, managed []any
	dirtyTotal := int64(0)
	for _, e := range entries {
		item := m.describe(e, repoRoot, root, mainBranch)
		worktrees = append(worktrees, item)
		if item.Value("managed") == true {
			managed = append(managed, item)
			if pyjson.Truthy(item.Value("dirty")) {
				dirtyTotal++
			}
		}
	}
	if worktrees == nil {
		worktrees = []any{}
	}
	if managed == nil {
		managed = []any{}
	}
	r := repo
	r.Root = repoRoot
	return pyjson.NewObject(
		"available", true,
		"reason", "",
		"repository", r.object(&mainBranch),
		"root", root,
		"root_exists", pyos.Exists(root),
		"worktrees", worktrees,
		"managed", managed,
		"total", int64(len(worktrees)),
		"managed_total", int64(len(managed)),
		"dirty_total", dirtyTotal,
		"generated_at", clock.UTCNow(),
	), nil
}

func objects(v any) []*pyjson.Object {
	l, _ := v.([]any)
	out := make([]*pyjson.Object, 0, len(l))
	for _, x := range l {
		if o, ok := x.(*pyjson.Object); ok {
			out = append(out, o)
		}
	}
	return out
}

func refMatches(item *pyjson.Object, raw string) bool {
	return raw == pyjson.Str(item.Value("path")) || raw == pyjson.Str(item.Value("name")) ||
		raw == pyjson.Str(item.Value("branch"))
}

func unavailableErr(listing *pyjson.Object) error {
	reason := pyjson.Str(listing.Value("reason"))
	if reason == "" {
		reason = "worktrees are unavailable"
	}
	code := pyjson.Str(listing.Value("code"))
	if code == "" {
		code = "WORKTREES_UNAVAILABLE"
	}
	return newErr(reason, 409, code)
}

// ResolveSessionTarget is resolve_session_target.
func (m *Manager) ResolveSessionTarget(ref any) (*pyjson.Object, error) {
	raw := ""
	if pyjson.Truthy(ref) {
		raw = pyjson.Str(ref)
	}
	raw = pytext.Strip(raw)
	if raw == "" {
		return nil, newErr("a worktree reference is required", 400, "WORKTREE_REQUIRED")
	}
	listing, err := m.List()
	if err != nil {
		return nil, err
	}
	if listing.Value("available") != true {
		return nil, unavailableErr(listing)
	}
	var candidates []*pyjson.Object
	for _, item := range objects(listing.Value("worktrees")) {
		if item.Value("main") == true || !pyjson.Truthy(item.Value("managed")) {
			continue
		}
		if refMatches(item, raw) {
			candidates = append(candidates, item)
		} else if filepath.IsAbs(raw) && pathx.SamePath(resolve(pyjson.Str(item.Value("path"))), resolve(raw)) {
			candidates = append(candidates, item)
		}
	}
	if len(candidates) == 0 {
		return nil, newErr("worktree not found under the managed root: "+raw, 404, "WORKTREE_NOT_FOUND")
	}
	item := candidates[0].Copy()
	if pyjson.Truthy(item.Value("missing")) {
		return nil, newErr("worktree folder is missing: "+pyjson.Str(item.Value("path")), 409, "WORKTREE_MISSING")
	}
	repo := listing.Value("repository")
	if !pyjson.Truthy(repo) {
		repo = pyjson.NewObject()
	}
	item.Set("repository", repo)
	item.Set("root", listing.Value("root"))
	return item, nil
}

func strField(body *pyjson.Object, key string) string {
	v := body.Value(key)
	if !pyjson.Truthy(v) {
		return ""
	}
	return pytext.Strip(pyjson.Str(v))
}

// Create is create(body).
func (m *Manager) Create(body *pyjson.Object) (*pyjson.Object, error) {
	if body == nil {
		body = pyjson.NewObject()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	repo, err := m.RepositoryInfo()
	if err != nil {
		return nil, err
	}
	repoRoot := resolve(repo.Root)
	root, err := m.SafeRoot(repo)
	if err != nil {
		return nil, err
	}
	entries, err := m.entries(repoRoot)
	if err != nil {
		return nil, err
	}
	var mainEntry *Entry
	for i := range entries {
		if pathx.SamePath(resolve(entries[i].Path), repoRoot) {
			mainEntry = &entries[i]
			break
		}
	}
	if mainEntry == nil {
		return nil, newErr("cannot locate the main worktree", 409, "GIT_WORKTREE_LIST")
	}
	mainBranch := mainEntry.Branch
	if mainBranch == "" {
		mainBranch = repo.Branch
	}
	base := strField(body, "base")
	if base != "" {
		if base, err = ValidateBranch(base); err != nil {
			return nil, err
		}
	} else {
		base = mainBranch
		if base == "" {
			base = mainEntry.Head
			if base == "" {
				base = "HEAD"
			}
		}
	}
	name := strField(body, "name")
	branchInput := strField(body, "branch")
	var branch string
	if branchInput != "" {
		if branch, err = ValidateBranch(branchInput); err != nil {
			return nil, err
		}
	} else {
		slug, err := SlugifyName(name)
		if err != nil {
			return nil, err
		}
		if branch, err = ValidateBranch(m.BranchPrefix + slug); err != nil {
			return nil, err
		}
	}
	if branch == mainBranch {
		return nil, newErr("a worktree cannot check out the main branch", 409, "BRANCH_IS_MAIN")
	}
	managed := 0
	for _, e := range entries {
		p := resolve(e.Path)
		if inside(p, root) && !pathx.SamePath(p, repoRoot) {
			managed++
		}
	}
	if managed >= m.Max {
		return nil, newErr(fmt.Sprintf("the managed root already has %d worktrees (limit %d)", managed, m.Max), 409, "WORKTREE_LIMIT")
	}
	for _, e := range entries {
		if !e.Detached && e.Branch == branch {
			where := e.Path
			if where == "" {
				where = "another worktree"
			}
			return nil, newErr("branch "+branch+" is already checked out at "+where, 409, "BRANCH_ALREADY_CHECKED_OUT")
		}
	}
	folder, err := SanitizeDirectory(branch)
	if err != nil {
		return nil, err
	}
	target := resolve(filepath.Join(root, folder))
	if !inside(target, root) {
		return nil, newErr("resolved worktree escaped the safe root", 409, "UNSAFE_PATH")
	}
	if pyos.Exists(target) {
		return nil, newErr("folder already exists: "+target, 409, "WORKTREE_FOLDER_EXISTS")
	}
	dirty, known := m.statusCount(repoRoot)
	if !known {
		return nil, newErr("could not check whether the repository is clean", 409, "GIT_STATUS")
	}
	if dirty > 0 {
		s := "s"
		if dirty == 1 {
			s = ""
		}
		return nil, newErr(fmt.Sprintf("the base checkout has %d uncommitted change%s; "+
			"commit or stash them before creating an agent worktree", dirty, s), 409, "WORKTREE_BASE_DIRTY")
	}
	if r := m.git([]string{"rev-parse", "--verify", base + "^{commit}"}, repoRoot); r.code != 0 {
		return nil, newErr("base ref not found: "+base, 400, "BASE_NOT_FOUND")
	}
	listed, err := requireOK(m.git([]string{"branch", "--list", branch, "--format=%(refname:short)"}, repoRoot), "checking the branch", "GIT_BRANCH_LIST")
	if err != nil {
		return nil, err
	}
	exists := false
	for _, line := range pytext.SplitLines(listed, false) {
		if pytext.Strip(line) == branch {
			exists = true
		}
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	var r gitResult
	if exists {
		r = m.git([]string{"worktree", "add", target, branch}, repoRoot)
	} else {
		r = m.git([]string{"worktree", "add", target, "-b", branch, base}, repoRoot)
	}
	if _, err := requireOK(r, "creating worktree "+branch, "GIT_WORKTREE_ADD"); err != nil {
		return nil, err
	}
	if !pyos.IsDir(target) {
		return nil, newErr("git reported success but the worktree folder is missing", 500, "WORKTREE_VERIFY")
	}
	listing, err := m.List()
	if err != nil {
		return nil, err
	}
	var created *pyjson.Object
	for _, item := range objects(listing.Value("worktrees")) {
		if pathx.SamePath(resolve(pyjson.Str(item.Value("path"))), target) {
			created = item
			break
		}
	}
	if created == nil {
		created = pyjson.NewObject("path", target, "name", filepath.Base(target), "branch", branch,
			"head", "", "main", false, "managed", true, "detached", false,
			"locked", false, "prunable", false, "missing", false,
			"dirty", int64(0), "clean", true, "status", "clean", "active_sessions", []any{})
	}
	repoOut := listing.Value("repository")
	if !pyjson.Truthy(repoOut) {
		repoOut = repo.object(nil)
	}
	return pyjson.NewObject(
		"created", true,
		"created_branch", !exists,
		"reused_branch", exists,
		"base", base,
		"repository", repoOut,
		"root", root,
		"worktree", created,
	), nil
}

// Remove is remove(body).
func (m *Manager) Remove(body *pyjson.Object) (*pyjson.Object, error) {
	if body == nil {
		body = pyjson.NewObject()
	}
	if body.Value("confirm") != true {
		return nil, newErr("removing a worktree requires explicit confirmation", 400, "CONFIRM_REQUIRED")
	}
	raw := ""
	for _, k := range []string{"path", "name", "branch"} {
		if v := body.Value(k); pyjson.Truthy(v) {
			raw = pyjson.Str(v)
			break
		}
	}
	raw = pytext.Strip(raw)
	if raw == "" {
		return nil, newErr("worktree path is required", 400, "WORKTREE_REQUIRED")
	}
	force := pyjson.Truthy(body.Value("force"))
	deleteBranch := pyjson.Truthy(body.Value("delete_branch"))
	m.mu.Lock()
	defer m.mu.Unlock()
	listing, err := m.List()
	if err != nil {
		return nil, err
	}
	if listing.Value("available") != true {
		return nil, unavailableErr(listing)
	}
	repoRoot := resolve(pyjson.Str(listing.Value("repository").(*pyjson.Object).Value("root")))
	var target *pyjson.Object
	for _, item := range objects(listing.Value("worktrees")) {
		if item.Value("main") == true || !pyjson.Truthy(item.Value("managed")) {
			continue
		}
		if refMatches(item, raw) {
			target = item
			break
		}
	}
	if target == nil {
		return nil, newErr("managed worktree not found: "+raw, 404, "WORKTREE_NOT_FOUND")
	}
	tpath := pyjson.Str(target.Value("path"))
	if pathx.SamePath(resolve(tpath), repoRoot) {
		return nil, newErr("the main worktree cannot be removed", 400, "WORKTREE_MAIN")
	}
	if !inside(resolve(tpath), resolve(pyjson.Str(listing.Value("root")))) {
		return nil, newErr("refusing to remove a path outside the managed root", 409, "UNSAFE_PATH")
	}
	if pyjson.Truthy(target.Value("missing")) {
		return nil, newErr("the worktree folder is already missing; use Git to prune it", 409, "WORKTREE_MISSING")
	}
	dirty, known := m.statusCount(resolve(tpath))
	if !known {
		return nil, newErr("could not check the worktree status", 409, "GIT_STATUS")
	}
	if dirty > 0 && !force {
		s := "s"
		if dirty == 1 {
			s = ""
		}
		return nil, newErr(fmt.Sprintf("worktree has %d uncommitted change%s; "+
			"commit or discard them, or confirm a forced removal", dirty, s), 409, "WORKTREE_DIRTY")
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, tpath)
	if _, err := requireOK(m.git(args, repoRoot), "removing the worktree", "GIT_WORKTREE_REMOVE"); err != nil {
		return nil, err
	}
	branchDeleted := false
	branch := pyjson.Str(target.Value("branch"))
	if deleteBranch && branch != "" {
		branchDeleted = m.git([]string{"branch", "-D", branch}, repoRoot).code == 0
	}
	return pyjson.NewObject(
		"removed", true,
		"forced", force,
		"path", tpath,
		"branch", branch,
		"branch_deleted", branchDeleted,
		"dirty_at_removal", int64(dirty),
		"generated_at", clock.UTCNow(),
	), nil
}
