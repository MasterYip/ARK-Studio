package search

import (
	"bytes"
	"container/list"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"arkstudio/go/internal/nodehist"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pyurl"
	"arkstudio/go/internal/workspace"
)

// Emit receives one result; returning false stops the scan (client gone).
type Emit func(item *pyjson.Object) bool

// ------------------------------------------------------------ text cache

// cacheBytes is ARK_SEARCH_CACHE_MB (default 192) in bytes.
var cacheBytes = func() int64 {
	mb := int64(192)
	if v, ok := os.LookupEnv("ARK_SEARCH_CACHE_MB"); ok {
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			n = 192
		}
		mb = max(0, n)
	}
	return mb * 1024 * 1024
}()

type cached struct {
	path    string
	mtimeNS int64
	size    int64
	text    *string // nil = binary
	scan    int64
	elem    *list.Element
}

var (
	textMu    sync.Mutex
	textLRU   = list.New() // front = oldest
	textIndex = map[string]*cached{}
	textBytes int64
	scanSeq   int64
)

func cost(size int64) int64 { return 2*size + 200 }

// cachedText is _cached_text: (text, true) on a hit (text nil = binary).
func cachedText(path string, mtimeNS, size, scan int64) (*string, bool) {
	textMu.Lock()
	defer textMu.Unlock()
	hit := textIndex[path]
	if hit == nil {
		return nil, false
	}
	if hit.mtimeNS != mtimeNS || hit.size != size {
		textLRU.Remove(hit.elem)
		delete(textIndex, path)
		textBytes -= cost(hit.size)
		return nil, false
	}
	hit.scan = scan
	textLRU.MoveToBack(hit.elem)
	return hit.text, true
}

// rememberText is _remember_text.
func rememberText(path string, mtimeNS, size int64, text *string, scan int64) {
	c := cost(size)
	if c > cacheBytes/8 {
		return
	}
	textMu.Lock()
	defer textMu.Unlock()
	if old := textIndex[path]; old != nil {
		textLRU.Remove(old.elem)
		delete(textIndex, path)
		textBytes -= cost(old.size)
	}
	for textBytes+c > cacheBytes && textLRU.Len() > 0 {
		oldest := textLRU.Front().Value.(*cached)
		if scan != 0 && oldest.scan == scan {
			return
		}
		textLRU.Remove(oldest.elem)
		delete(textIndex, oldest.path)
		textBytes -= cost(oldest.size)
	}
	e := &cached{path: path, mtimeNS: mtimeNS, size: size, text: text, scan: scan}
	e.elem = textLRU.PushBack(e)
	textIndex[path] = e
	textBytes += c
}

// ClearTextCache is clear_text_cache.
func ClearTextCache() {
	textMu.Lock()
	textLRU.Init()
	textIndex = map[string]*cached{}
	textBytes = 0
	textMu.Unlock()
}

// readText is _read: (text, ok); text nil = binary, ok=false = read error.
func readText(path string, mtimeNS, size, scan int64) (*string, bool) {
	if t, hit := cachedText(path, mtimeNS, size, scan); hit {
		return t, true
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var text *string
	if !workspace.LooksBinary(raw) {
		s := string(raw)
		if !utf8.Valid(raw) {
			s = pyurl.DecodeReplace(raw)
		}
		text = &s
	}
	rememberText(path, mtimeNS, size, text, scan)
	return text, true
}

// --------------------------------------------------------------- walking

type entry struct {
	rel, name, path string
}

type dirListing struct {
	files []string
	dirs  []string // kept subfolders, sorted
}

// listDirFor is _list_dir + the folder filter of _walk.
func listDirFor(root, path, relDir string) dirListing {
	list, err := workspace.ListDir(path)
	if err != nil {
		return dirListing{}
	}
	var out dirListing
	var dirs []string
	for _, e := range list {
		if workspace.IsDir(filepath.Join(path, e.Name())) {
			dirs = append(dirs, e.Name())
		} else {
			out.files = append(out.files, e.Name())
		}
	}
	sort.SliceStable(out.files, func(i, j int) bool { return lowerLess(out.files[i], out.files[j]) })
	for _, name := range dirs {
		if workspace.IsIgnored(name) || (relDir == "" && workspace.IsProtected(root, name)) ||
			workspace.IsLinkLike(filepath.Join(path, name)) {
			continue
		}
		out.dirs = append(out.dirs, name)
	}
	sort.SliceStable(out.dirs, func(i, j int) bool { return lowerLess(out.dirs[i], out.dirs[j]) })
	return out
}

// walk is _walk: every file in plain-scan order (a folder's files before its
// subfolders), listed level by level in parallel, and whether the listing
// finished before the deadline.
func walk(root string, deadline time.Time) ([]entry, bool) {
	type folder struct{ path, rel string }
	listed := map[string]dirListing{}
	level := []folder{{root, ""}}
	complete := true
	for len(level) > 0 {
		if !time.Now().Before(deadline) {
			complete = false
			for _, f := range level {
				listed[f.rel] = dirListing{}
			}
			break
		}
		results := make([]dirListing, len(level))
		var wg sync.WaitGroup
		sem := make(chan struct{}, readThreads)
		for i, f := range level {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				results[i] = listDirFor(root, f.path, f.rel)
				<-sem
			}()
		}
		wg.Wait()
		var next []folder
		for i, f := range level {
			listed[f.rel] = results[i]
			for _, d := range results[i].dirs {
				rel := d
				if f.rel != "" {
					rel = f.rel + "/" + d
				}
				next = append(next, folder{filepath.Join(f.path, d), rel})
			}
		}
		level = next
	}
	var out []entry
	stack := []string{""}
	for len(stack) > 0 {
		relDir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		l := listed[relDir]
		base := root
		if relDir != "" {
			base = filepath.Join(root, filepath.FromSlash(relDir))
		}
		for _, name := range l.files {
			rel := name
			if relDir != "" {
				rel = relDir + "/" + name
			}
			out = append(out, entry{rel, name, filepath.Join(base, name)})
		}
		for i := len(l.dirs) - 1; i >= 0; i-- {
			rel := l.dirs[i]
			if relDir != "" {
				rel = relDir + "/" + rel
			}
			stack = append(stack, rel)
		}
	}
	return out, complete
}

const readThreads = 8

// ------------------------------------------------------------------- git

type gitRootHit struct {
	at     time.Time
	inside bool
}

var (
	gitRootsMu sync.Mutex
	gitRoots   = map[string]gitRootHit{}
)

func runGit(root string, stdin []byte, timeout time.Duration, args ...string) ([]byte, int, bool) {
	path, err := exec.LookPath("git")
	if err != nil {
		return nil, 0, false
	}
	nodehist.Slots <- struct{}{}
	defer func() { <-nodehist.Slots }()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = root
	hideWindow(cmd)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err = cmd.Run()
	if ctx.Err() != nil {
		return nil, 0, false
	}
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			return nil, 0, false
		}
		return stdout.Bytes(), ee.ExitCode(), true
	}
	return stdout.Bytes(), 0, true
}

// inGitWorkTree is _in_git_work_tree (cached for 30 s per root).
func inGitWorkTree(root string) bool {
	gitRootsMu.Lock()
	hit, ok := gitRoots[root]
	gitRootsMu.Unlock()
	if ok && time.Since(hit.at) < 30*time.Second {
		return hit.inside
	}
	out, code, ran := runGit(root, nil, 5*time.Second, "rev-parse", "--is-inside-work-tree")
	inside := ran && code == 0 && string(bytes.TrimSpace(out)) == "true"
	gitRootsMu.Lock()
	gitRoots[root] = gitRootHit{time.Now(), inside}
	gitRootsMu.Unlock()
	return inside
}

// gitIgnored is _git_ignored: paths .gitignore (and git's other exclude
// sources) ignore, when the root is inside a git work tree.
func gitIgnored(root string, rels []string) map[string]bool {
	out := map[string]bool{}
	if len(rels) == 0 || !inGitWorkTree(root) {
		return out
	}
	raw, code, ran := runGit(root, []byte(strings.Join(rels, "\x00")), 15*time.Second,
		"-c", "core.quotepath=off", "check-ignore", "--stdin", "-z")
	if !ran || (code != 0 && code != 1) {
		return out
	}
	for _, item := range strings.Split(pyurl.DecodeReplace(raw), "\x00") {
		if item != "" {
			out[item] = true
		}
	}
	return out
}

// -------------------------------------------------------------- ripgrep

type rgHit struct {
	at   time.Time
	path string
}

var (
	rgMu     sync.Mutex
	rgLookup = map[string]rgHit{}
)

// RipgrepPath is ripgrep_path: ARK_RG (a path, or none/off/0/false) or rg
// on PATH, looked up at most once a minute.
func RipgrepPath() string {
	configured := strings.TrimSpace(os.Getenv("ARK_RG"))
	switch strings.ToLower(configured) {
	case "none", "off", "0", "false":
		return ""
	}
	rgMu.Lock()
	hit, ok := rgLookup[configured]
	rgMu.Unlock()
	if ok && time.Since(hit.at) < 60*time.Second {
		return hit.path
	}
	found := ""
	if configured != "" {
		if fi, err := os.Stat(configured); err == nil && fi.Mode().IsRegular() {
			found = configured
		}
	} else if p, err := exec.LookPath("rg"); err == nil {
		found = p
	}
	rgMu.Lock()
	rgLookup[configured] = rgHit{time.Now(), found}
	rgMu.Unlock()
	return found
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// rgCandidates is _rg_candidates: files holding the literal query (a
// superset of the files with a matching line), or nil when ripgrep cannot
// answer.
func rgCandidates(root, query string, caseSensitive bool, cancelled func() bool) map[string]bool {
	rg := RipgrepPath()
	if rg == "" || strings.ContainsAny(query, "\r\n") {
		return nil
	}
	if !caseSensitive && !isASCII(query) {
		return nil
	}
	mode := "--ignore-case"
	if caseSensitive {
		mode = "--case-sensitive"
	}
	args := []string{"--files-with-matches", "--no-messages", "--no-config", "--no-ignore", "--hidden",
		"--text", "--fixed-strings", mode, "--max-filesize", strconv.Itoa(MaxFileBytes)}
	names := workspace.IgnoredDirNames()
	sort.Strings(names)
	for _, name := range names {
		args = append(args, "--glob", "!"+name+"/")
	}
	args = append(args, "--regexp", query)
	if !caseSensitive && strings.Contains(strings.ToLower(query), "i") {
		args = append(args, "--regexp", "İ", "--regexp", "ı")
	}
	args = append(args, "--", ".")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, rg, args...)
	cmd.Dir = root
	hideWindow(cmd)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Start(); err != nil {
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
wait:
	for {
		select {
		case err = <-done:
			break wait
		case <-time.After(50 * time.Millisecond):
			if (cancelled != nil && cancelled()) || ctx.Err() != nil {
				cmd.Process.Kill() //nolint:errcheck
				<-done
				return nil
			}
		}
	}
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			return nil
		}
		code = ee.ExitCode()
	}
	if code != 0 && code != 1 {
		return nil
	}
	found := map[string]bool{}
	for _, raw := range bytes.Split(stdout.Bytes(), []byte("\n")) {
		rel := strings.ReplaceAll(strings.TrimSpace(string(raw)), `\`, "/")
		rel = strings.TrimPrefix(rel, "./")
		if rel != "" {
			found[rel] = true
		}
	}
	return found
}

var warming sync.Mutex

type warmItem struct {
	path          string
	mtimeNS, size int64
}

// warmLater is _warm_later: read files ripgrep let the scan skip into the
// text cache in the background, one warm-up at a time.
func warmLater(items []warmItem) {
	if len(items) == 0 || !warming.TryLock() {
		return
	}
	go func() {
		defer warming.Unlock()
		for _, it := range items {
			if _, hit := cachedText(it.path, it.mtimeNS, it.size, 0); !hit {
				readText(it.path, it.mtimeNS, it.size, 0)
			}
		}
	}()
}

// ----------------------------------------------------------------- scan

type step struct {
	kind          string // "", "big", "error", "miss", "read"
	path          string
	mtimeNS, size int64
}

// IterFiles is iter_search_files: emits each matching file in Explorer
// order and returns the summary.  cancelled (may be nil) and a false Emit
// stop the scan.
func IterFiles(root, query string, o Options, include, exclude string, cancelled func() bool, emit Emit) (*pyjson.Object, error) {
	re, err := BuildMatcher(query, o)
	if err != nil {
		return nil, err
	}
	accept := pathFilter(include, exclude)
	rootPath := project.Resolve(root)
	started := time.Now()
	textMu.Lock()
	scanSeq++
	scan := scanSeq
	textMu.Unlock()
	budget := stepBudget
	total, visited, skipped, found := 0, 0, 0, 0
	truncated, timedOut := false, false
	backend := "python"

	plan, complete := walk(rootPath, started.Add(TimeLimit))
	if len(plan) > 0 {
		rels := make([]string, len(plan))
		for i, e := range plan {
			rels[i] = e.rel
		}
		if ignored := gitIgnored(rootPath, rels); len(ignored) > 0 {
			kept := plan[:0]
			for _, e := range plan {
				if !ignored[e.rel] {
					kept = append(kept, e)
				}
			}
			plan = kept
		}
	}
	work := make([]step, len(plan))
	var reads []int
	for i, e := range plan {
		if workspace.IsLinkLike(e.path) || workspace.IsIgnored(e.name) || !accept(e.rel) {
			continue
		}
		fi, err := os.Lstat(e.path)
		switch {
		case err != nil:
			work[i] = step{kind: "error"}
		case fi.Size() > MaxFileBytes:
			work[i] = step{kind: "big"}
		default:
			work[i] = step{kind: "read", path: e.path, mtimeNS: fi.ModTime().UnixNano(), size: fi.Size()}
			reads = append(reads, i)
		}
	}
	if !o.Regex && len(reads) > 0 {
		cold := 0
		for _, i := range reads {
			if _, hit := cachedText(work[i].path, work[i].mtimeNS, work[i].size, scan); !hit {
				cold++
			}
		}
		if cold*2 > len(reads) {
			if candidates := rgCandidates(rootPath, query, o.Case, cancelled); candidates != nil {
				backend = "rg"
				var skippedReads []warmItem
				for _, i := range reads {
					if !candidates[plan[i].rel] {
						skippedReads = append(skippedReads, warmItem{work[i].path, work[i].mtimeNS, work[i].size})
						work[i].kind = "miss"
					}
				}
				warmLater(skippedReads)
			}
		}
	}
	// Read ahead on a small pool, in order.
	type result struct {
		text *string
		ok   bool
	}
	pending := map[int]chan result{}
	sem := make(chan struct{}, readThreads)
	submitted := 0
	ahead := readThreads * 8
	stop := make(chan struct{})
	defer close(stop)
	submit := func(upto int) {
		for submitted < len(work) && submitted <= upto {
			s := work[submitted]
			if s.kind == "read" {
				ch := make(chan result, 1)
				pending[submitted] = ch
				if t, hit := cachedText(s.path, s.mtimeNS, s.size, scan); hit {
					ch <- result{t, true}
				} else {
					go func() {
						select {
						case sem <- struct{}{}:
						case <-stop:
							ch <- result{nil, false}
							return
						}
						t, ok := readText(s.path, s.mtimeNS, s.size, scan)
						<-sem
						ch <- result{t, ok}
					}()
				}
			}
			submitted++
		}
	}
	finished := true
	for index, s := range work {
		if cancelled != nil && cancelled() {
			truncated, finished = true, false
			break
		}
		if time.Since(started) >= TimeLimit || budget <= 0 {
			timedOut, truncated, finished = true, true, false
			break
		}
		if visited >= MaxFilesVisited || found >= MaxItems || total >= MaxResults {
			truncated, finished = true, false
			break
		}
		if s.kind == "" {
			continue
		}
		visited++
		if s.kind == "big" || s.kind == "error" {
			skipped++
			continue
		}
		if s.kind == "miss" {
			continue
		}
		submit(index + ahead)
		r := <-pending[index]
		delete(pending, index)
		if !r.ok {
			skipped++
			continue
		}
		if r.text == nil {
			continue
		}
		hits, capped, err := lineMatches(*r.text, re, min(MaxPerItem, MaxResults-total), &budget)
		if err != nil {
			timedOut, truncated, finished = true, true, false
			break
		}
		if len(hits) == 0 {
			continue
		}
		total += len(hits)
		found++
		matches := make([]any, len(hits))
		for i, h := range hits {
			matches[i] = h.object()
		}
		if !emit(pyjson.NewObject("path", plan[index].rel, "name", plan[index].name, "matches", matches, "capped", capped)) {
			return nil, nil
		}
	}
	if finished && !complete {
		timedOut, truncated = true, true
	}
	return pyjson.NewObject("query", query, "total", total, "visited", visited, "skipped", skipped,
		"truncated", truncated, "timed_out", timedOut, "backend", backend, "elapsed_ms", elapsedMS(started)), nil
}

// Files is search_files: the plain (non-streamed) reply.
func Files(root, query string, o Options, include, exclude string) (*pyjson.Object, error) {
	files := []any{}
	summary, err := IterFiles(root, query, o, include, exclude, nil, func(item *pyjson.Object) bool {
		files = append(files, item)
		return true
	})
	if err != nil {
		return nil, err
	}
	out := pyjson.NewObject("query", summary.Value("query"), "files", files)
	for _, k := range []string{"total", "visited", "skipped", "truncated", "timed_out", "backend", "elapsed_ms"} {
		out.Set(k, summary.Value(k))
	}
	return out, nil
}
