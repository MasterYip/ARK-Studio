package setup

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pyos"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/compat/pyurl"
	"arkstudio/go/internal/nodehist"
)

// GitTimeout is setup.GIT_TIMEOUT... run_git_command uses worktrees.GIT_TIMEOUT.
const GitTimeout = 20 * time.Second

const windows = runtime.GOOS == "windows"

var nonID = regexp.MustCompile(`[^a-z0-9._-]+`)

// ------------------------------------------------------------- pathlib

// pathStr is str(Path(p)): separators normalized, empty and "." parts dropped.
func pathStr(p string) string {
	sep := string(os.PathSeparator)
	if windows {
		p = strings.ReplaceAll(p, "/", `\`)
	}
	lead := ""
	if windows && len(p) >= 2 && p[1] == ':' {
		lead, p = p[:2], p[2:]
	}
	if windows && lead == "" && strings.HasPrefix(p, `\\`) && !strings.HasPrefix(p, `\\\`) {
		// UNC: \\server\share keeps both parts as the anchor.
		parts := strings.SplitN(p[2:], `\`, 3)
		if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
			lead = `\\` + parts[0] + `\` + parts[1]
			p = `\`
			if len(parts) == 3 {
				p += parts[2]
			}
		}
	}
	if strings.HasPrefix(p, sep) {
		lead += sep
		if !windows && strings.HasPrefix(p, "//") && !strings.HasPrefix(p, "///") {
			lead += sep
		}
	}
	var parts []string
	for _, part := range strings.Split(p, sep) {
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	out := lead + strings.Join(parts, sep)
	if out == "" {
		return "."
	}
	return out
}

// expandUser is Path(p).expanduser() (str of the result).
func expandUser(p string) string {
	s := pathStr(p)
	sep := string(os.PathSeparator)
	if s == "~" || strings.HasPrefix(s, "~"+sep) {
		return pathStr(pyos.Home() + s[1:])
	}
	return s
}

// pathName is Path(p).name.
func pathName(p string) string {
	s := pathStr(p)
	if windows && len(s) >= 2 && s[1] == ':' {
		s = s[2:]
	}
	i := strings.LastIndex(s, string(os.PathSeparator))
	name := s[i+1:]
	if name == "." {
		return ""
	}
	return name
}

// pathParent is str(Path(p).parent).
func pathParent(p string) string {
	s := pathStr(p)
	lead := ""
	if windows && len(s) >= 2 && s[1] == ':' {
		lead, s = s[:2], s[2:]
	}
	i := strings.LastIndex(s, string(os.PathSeparator))
	switch {
	case i < 0:
		if lead != "" {
			return lead
		}
		return "."
	case i == 0:
		return lead + s[:1]
	}
	return lead + s[:i]
}

func join(base, name string) string { return pathStr(base + string(os.PathSeparator) + name) }

// ------------------------------------------------------------- git

type gitResult struct {
	code   int
	stdout string
}

// git is _git / run_git_command: never fails, returncode 127 when git cannot start.
func git(args []string, cwd string) gitResult {
	path, err := exec.LookPath("git")
	if err != nil {
		return gitResult{127, ""}
	}
	nodehist.Slots <- struct{}{}
	defer func() { <-nodehist.Slots }()
	ctx, cancel := context.WithTimeout(context.Background(), GitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = cwd
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err = cmd.Run()
	if ctx.Err() != nil {
		return gitResult{124, ""}
	}
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			return gitResult{127, ""}
		}
		code = ee.ExitCode()
	}
	raw := stdout.Bytes()
	out := string(raw)
	if !utf8.Valid(raw) {
		out = pyurl.DecodeReplace(raw)
	}
	out = strings.ReplaceAll(strings.ReplaceAll(out, "\r\n", "\n"), "\r", "\n")
	return gitResult{code, out}
}

func gitOut(args []string, cwd string) string {
	r := git(args, cwd)
	if r.code != 0 {
		return ""
	}
	return pytext.Strip(r.stdout)
}

func readText(p string) (string, bool) {
	raw, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	s := string(raw)
	if !utf8.Valid(raw) {
		s = pyurl.DecodeReplace(raw)
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n"), true
}

func markers(dir string) []any {
	out := []any{}
	for _, name := range projectMarkers {
		if pyos.Exists(join(dir, name)) {
			out = append(out, name)
		}
	}
	return out
}

// InspectRepository is inspect_repository(path).
func InspectRepository(path string) *obj {
	target := expandUser(path)
	info := pyjson.NewObject("path", target, "exists", pyos.IsDir(target), "git", false)
	if !pyos.IsDir(target) {
		return info
	}
	top := gitOut([]string{"rev-parse", "--show-toplevel"}, target)
	info.Set("markers", markers(target)).Set("has_tests", pyos.IsDir(join(target, "tests")))
	if top == "" {
		return info
	}
	root := pathStr(top)
	status := gitOut([]string{"status", "--porcelain"}, root)
	worktrees := gitOut([]string{"worktree", "list", "--porcelain"}, root)
	excluded := false
	if t, ok := readText(join(join(join(root, ".git"), "info"), "exclude")); ok {
		excluded = strings.Contains(t, ".worktrees")
	}
	remote := gitOut([]string{"remote", "get-url", "origin"}, root)
	def := gitOut([]string{"symbolic-ref", "--short", "refs/remotes/origin/HEAD"}, root)
	if def != "" {
		if i := strings.Index(def, "/"); i >= 0 {
			def = def[i+1:]
		}
	}
	dirty := int64(0)
	for _, line := range splitLines(status) {
		if pytext.Strip(line) != "" {
			dirty++
		}
	}
	info.Set("git", true)
	info.Set("root", root)
	info.Set("name", pathName(root))
	info.Set("branch", gitOut([]string{"rev-parse", "--abbrev-ref", "HEAD"}, root))
	info.Set("head", gitOut([]string{"rev-parse", "--short", "HEAD"}, root))
	info.Set("default_branch", def)
	info.Set("dirty_files", dirty)
	info.Set("worktrees", int64(strings.Count(worktrees, "worktree ")))
	info.Set("worktrees_excluded", excluded)
	info.Set("remote", SanitizeRemote(remote))
	info.Set("markers", markers(root))
	info.Set("has_tests", pyos.IsDir(join(root, "tests")))
	return info
}

// splitLines is str.splitlines() for git output (already newline-translated).
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	out := []string{}
	cur := []rune{}
	for _, r := range s {
		if r == '\n' || r == '\v' || r == '\f' || r == 0x1c || r == 0x1d || r == 0x1e || r == 0x85 || r == 0x2028 || r == 0x2029 {
			out = append(out, string(cur))
			cur = cur[:0]
			continue
		}
		cur = append(cur, r)
	}
	if len(cur) > 0 {
		out = append(out, string(cur))
	}
	return out
}

func command(id, run, repo, gate string) *obj {
	return pyjson.NewObject("id", id, "run", run, "repository", repo, "gate", gate, "proposed", true)
}

// SuggestCommands is suggest_commands(info, repo_id).
func SuggestCommands(info *obj, repoID string) []any {
	root := pathStr(orStr(or(get(info, "root"), or(get(info, "path"), ""))))
	ms := map[string]bool{}
	for _, m := range listOr(get(info, "markers")) {
		ms[str(m)] = true
	}
	out := []any{}
	hasUnit := func() bool {
		for _, c := range out {
			if c.(*obj).Value("gate") == "unit" {
				return true
			}
		}
		return false
	}
	if ms["pyproject.toml"] || ms["setup.py"] || ms["requirements.txt"] {
		out = append(out, command(repoID+"-compile", "python -m compileall -q .", repoID, "static"))
		if pyjson.Truthy(get(info, "has_tests")) {
			pytest := false
			if t, ok := readText(join(root, "pyproject.toml")); ok {
				pytest = strings.Contains(t, "pytest")
			}
			run := "python -m unittest discover -s tests"
			if pytest {
				run = "python -m pytest -q"
			}
			out = append(out, command(repoID+"-unit", run, repoID, "unit"))
		}
	}
	if ms["package.json"] {
		scripts := pyjson.NewObject()
		if raw, err := os.ReadFile(join(root, "package.json")); err == nil && utf8.Valid(raw) {
			if v, err := pyjson.Loads(string(raw)); err == nil {
				// json.loads(...).get("scripts") or {} (a non-dict document raises
				// AttributeError in Python; treated as no scripts here).
				if m := mapOf(v); m != nil {
					if s := mapOf(m.Value("scripts")); s != nil {
						scripts = s
					}
				}
			}
		}
		for _, pair := range [][2]string{{"lint", "static"}, {"test", "unit"}, {"build", "component"}} {
			if scripts.Has(pair[0]) {
				out = append(out, command(repoID+"-"+pair[0], "npm run "+pair[0], repoID, pair[1]))
			}
		}
	}
	if ms["Makefile"] {
		makefile, _ := readText(join(root, "Makefile"))
		if makeTestRe.MatchString(makefile) && !hasUnit() {
			out = append(out, command(repoID+"-unit", "make test", repoID, "unit"))
		}
	}
	if ms["Cargo.toml"] {
		out = append(out, command(repoID+"-unit", "cargo test", repoID, "unit"))
	}
	return out
}

var makeTestRe = regexp.MustCompile(`(?m)^test:`)

// FindTaskSpaces is find_task_spaces(path, depth=3).
func FindTaskSpaces(path string) []string {
	root := expandUser(path)
	found := []string{}
	if !pyos.IsDir(root) {
		return found
	}
	skip := map[string]bool{".git": true, "node_modules": true, ".worktrees": true, "__pycache__": true, ".venv": true, "venv": true}
	var walk func(folder string, level int)
	walk = func(folder string, level int) {
		if level > 3 || len(found) >= 20 {
			return
		}
		ents, err := os.ReadDir(folder)
		if err != nil {
			return
		}
		names := make([]string, 0, len(ents))
		set := map[string]bool{}
		for _, e := range ents {
			names = append(names, e.Name())
			set[e.Name()] = true
		}
		for _, m := range taskSpaceMarkers {
			if set[m] {
				found = append(found, folder)
				return
			}
		}
		sort.Strings(names)
		for _, name := range names {
			child := join(folder, name)
			if !skip[name] && !strings.HasPrefix(name, ".") && pyos.IsDir(child) {
				walk(child, level+1)
			}
		}
	}
	walk(root, 0)
	return found
}

// Discover is discover(paths, gpu_servers=..., providers=..., project=...).
// installed is the provider ids whose CLI is installed (nil: no summary).
func Discover(paths []any, gpuServers *obj, installed []string, projectPath string) *obj {
	observations, repositories, taskSpaces, commands := []any{}, []any{}, []any{}, []any{}
	seenRoots := map[string]bool{}
	candidates := []string{}
	for _, p := range paths {
		if pytextStrip(orStr(p)) != "" {
			candidates = append(candidates, str(p))
		}
	}
	if len(candidates) > 20 {
		candidates = candidates[:20]
	}
	if projectPath != "" && len(candidates) == 0 {
		candidates = []string{pathParent(projectPath)}
	}
	for _, raw := range candidates {
		info := InspectRepository(raw)
		obs := pyjson.NewObject("kind", "path")
		obs.Update(info)
		observations = append(observations, obs)
		if pyjson.Truthy(info.Value("git")) {
			root := str(info.Value("root"))
			if !seenRoots[root] {
				seenRoots[root] = true
				repoID := trimDash(nonID.ReplaceAllString(lower(str(info.Value("name"))), "-"))
				repoID = prefix(repoID, 48)
				if repoID == "" {
					repoID = fmt.Sprintf("repo-%d", len(repositories)+1)
				}
				role := "code"
				if len(repositories) == 0 {
					role = "primary"
				}
				instr := []any{}
				ms := listOr(info.Value("markers"))
				for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
					for _, m := range ms {
						if m == name {
							instr = append(instr, name)
							break
						}
					}
				}
				repositories = append(repositories, pyjson.NewObject("id", repoID, "path", root, "role", role,
					"access", "read-write", "default_branch", or(info.Value("default_branch"), or(info.Value("branch"), "")),
					"remote", info.Value("remote"), "instructions", instr))
				commands = append(commands, SuggestCommands(info, repoID)...)
				if pyjson.Truthy(info.Value("dirty_files")) {
					observations = append(observations, pyjson.NewObject("kind", "warning", "detail",
						fmt.Sprintf("%s has %s uncommitted file(s); do not branch task worktrees from it yet", repoID, str(info.Value("dirty_files")))))
				}
				if n, _ := info.Value("worktrees").(int64); n > 1 && !pyjson.Truthy(info.Value("worktrees_excluded")) {
					observations = append(observations, pyjson.NewObject("kind", "warning", "detail",
						repoID+": .worktrees/ is not in .git/info/exclude"))
				}
			}
		}
		for _, folder := range FindTaskSpaces(raw) {
			dup := false
			for _, t := range taskSpaces {
				if t.(*obj).Value("location") == folder {
					dup = true
				}
			}
			if !dup {
				taskSpaces = append(taskSpaces, pyjson.NewObject("id", fmt.Sprintf("space-%d", len(taskSpaces)+1),
					"kind", "agent-team-folder", "location", folder, "access", "read-only", "relation", "import-history"))
			}
		}
	}
	compute := []any{pyjson.NewObject("id", "local", "kind", "local", "use", "any", "note", PlatformSystem()+" host running ARK")}
	for _, x := range listOr(get(gpuServers, "servers")) {
		server := mapOf(x)
		name := pytextStrip(orStr(get(server, "name")))
		if name != "" {
			compute = append(compute, pyjson.NewObject("id", prefix(nonID.ReplaceAllString(lower(name), "-"), 48),
				"kind", "ssh", "gpu_server", name, "use", "ask", "note", joinStrs(get(server, "tags"))))
		}
	}
	if gpuServers != nil && len(listOr(gpuServers.Value("servers"))) == 0 {
		note := or(gpuServers.Value("hint"), "no GPU servers configured")
		observations = append(observations, pyjson.NewObject("kind", "gpu", "detail", note))
	}
	providers := []any{}
	for _, id := range installed {
		providers = append(providers, id)
	}
	draft := pyjson.NewObject("repositories", repositories, "task_spaces", taskSpaces, "compute", compute,
		"commands", commands, "agents", pyjson.NewObject("providers", providers), "unknowns", []any{})
	normalized, errs, warns := NormalizeProfile(draft)
	return pyjson.NewObject("ok", len(errs) == 0, "errors", strList(errs), "warnings", strList(warns),
		"observations", observations, "draft", normalized, "questions", Questions(normalized),
		"note", "Discovery is read-only and heuristic. Commands are marked proposed; "+
			"a human must confirm the profile before task work starts.")
}

func strList(l []string) []any {
	out := make([]any, len(l))
	for i, s := range l {
		out[i] = s
	}
	return out
}

func trimDash(s string) string { return strings.Trim(s, "-") }

func prefix(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// Verify is verify(profile, gpu_servers=...) with os.environ.
func Verify(profile *obj, gpuServers *obj) *obj {
	checks := []any{}
	check := func(id, status, detail string, mandatory bool) {
		checks = append(checks, pyjson.NewObject("id", id, "status", status, "detail", detail, "mandatory", mandatory))
	}
	for _, x := range listOr(get(profile, "repositories")) {
		repo := mapOf(x)
		cid := "repository:" + str(get(repo, "id"))
		info := InspectRepository(str(get(repo, "path")))
		if !pyjson.Truthy(info.Value("exists")) {
			check(cid, "fail", "path does not exist: "+str(get(repo, "path")), true)
			continue
		}
		rw := get(repo, "access") == "read-write"
		if !pyjson.Truthy(info.Value("git")) {
			status := "warn"
			if rw {
				status = "fail"
			}
			check(cid, status, "not a Git checkout", rw)
			continue
		}
		check(cid, "pass", str(info.Value("name"))+" @ "+str(info.Value("branch"))+" "+str(info.Value("head")), true)
		if branch := get(repo, "default_branch"); pyjson.Truthy(branch) {
			ok := git([]string{"rev-parse", "--verify", "--quiet", str(branch)}, str(info.Value("root"))).code == 0
			status, suffix := "fail", " not found"
			if ok {
				status, suffix = "pass", ""
			}
			check(cid+":branch", status, "base branch '"+str(branch)+"'"+suffix, true)
		}
		if rw {
			if pyjson.Truthy(info.Value("worktrees_excluded")) {
				check(cid+":worktrees-excluded", "pass", ".worktrees/ in .git/info/exclude", false)
			} else {
				check(cid+":worktrees-excluded", "warn", "add '.worktrees/' to .git/info/exclude before the first task worktree", false)
			}
			if pyjson.Truthy(info.Value("dirty_files")) {
				check(cid+":clean", "warn", str(info.Value("dirty_files"))+" uncommitted file(s) in the primary checkout", false)
			}
		}
	}
	for _, x := range listOr(get(profile, "task_spaces")) {
		space := mapOf(x)
		location := orStr(get(space, "location"))
		if IsLocalPath(location) {
			exists := pyos.Exists(expandUser(location))
			detail := "missing: " + location
			status := "warn"
			if exists {
				detail, status = location, "pass"
			}
			check("task_space:"+str(get(space, "id")), status, detail, false)
		}
	}
	names := map[string]bool{}
	for _, x := range listOr(get(gpuServers, "servers")) {
		names[str(get(mapOf(x), "name"))] = true
	}
	for _, x := range listOr(get(profile, "compute")) {
		item := mapOf(x)
		if get(item, "kind") != "ssh" {
			continue
		}
		id := "compute:" + str(get(item, "id"))
		gs := get(item, "gpu_server")
		switch {
		case gpuServers == nil:
			check(id, "skip", "GPU server list not consulted", false)
		case isStr(gs) && names[gs.(string)]:
			check(id, "pass", "'"+str(gs)+"' is in the private server list (not probed)", true)
		default:
			check(id, "fail", "'"+str(gs)+"' is not in the private GPU server list", true)
		}
	}
	for _, x := range listOr(get(profile, "storage")) {
		item := mapOf(x)
		location := orStr(get(item, "location"))
		if IsLocalPath(location) {
			exists := pyos.IsDir(expandUser(location))
			detail, status := "missing directory: "+location, "warn"
			if exists {
				detail, status = location, "pass"
			}
			check("storage:"+str(get(item, "id")), status, detail, false)
		}
	}
	for _, x := range listOr(get(profile, "secrets")) {
		item := mapOf(x)
		ref := orStr(get(item, "ref"))
		if strings.HasPrefix(ref, "env:") {
			present := os.Getenv(ref[4:]) != ""
			word, status := "not set", "warn"
			if present {
				word, status = "set", "pass"
			}
			check("secret:"+str(get(item, "id")), status, ref+" is "+word+" (value not read)", false)
		}
	}
	check("commands", "skip", "verify never executes project commands; run the smoke "+
		"or unit gate inside a task", false)
	ok := true
	for _, c := range checks {
		o := c.(*obj)
		if o.Value("status") == "fail" && o.Value("mandatory") == true {
			ok = false
		}
	}
	return pyjson.NewObject("ok", ok, "checks", checks, "machine", MachineFingerprint(), "verified_at", utcNow())
}

func isStr(v any) bool { _, ok := v.(string); return ok }

func pytextStrip(s string) string { return pytext.Strip(s) }
