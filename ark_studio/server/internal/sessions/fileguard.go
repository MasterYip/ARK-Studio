package sessions

// The file guard of a launched session (2026-11-25, T-0128; 2026-11-26,
// T-0132).  After each turn ARK warns in the session about new top-level
// files and folders in the home folder of the account the agent runs as
// (outside the places registered for this device), and about files of the
// session workspace that units written this turn name as results.  Scratch
// in the session workspace or the OS temp dir is the agent's own and is
// not reported.  It is a cheap look, not a global scan: the top level of
// home compared with its entries when the turn began, and the session's
// own workspace (bounded depth and entries).  Nothing is moved or deleted.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/sessionstart"
)

const (
	guardMaxEntries = 4000 // per folder looked at
	guardMaxListed  = 12   // paths named in the warning
)

// guardToolState are names in a home or temp folder that belong to tools
// (agent CLIs, caches, editors), not to the agent's work.
var guardToolState = regexp.MustCompile(`^(\..*|claude-[^/]*|codex[^/]*|node-compile-cache|v8-compile-cache[^/]*|jiti|tsx-[^/]*|` +
	`dsh[^/]*|tmp-[0-9]+-[^/]*|matplotlib-[^/]*|pip-[^/]*|pymp-[^/]*|go-build[^/]*|gopls-[^/]*|hsperfdata[^/]*|` +
	`node-addon-native-custom-loader[^/]*|snap-private-tmp|systemd-private-[^/]*|ark-watch[^/]*|__pycache__)$`)

// arkWorkspaceFiles are ARK's own files in a session workspace.
var arkWorkspaceFiles = map[string]bool{"ARK-TASK.md": true, "ARK-SESSION-BLOCK.md": true, "ark-context.json": true,
	"ARK-PROMPT.md": true, "ARK-MESSAGE.md": true}

// GuardFinding is what one look found.
type GuardFinding struct {
	Outside    []string // created outside every registered place (new top-level entries of home)
	Referenced []string // session-workspace files a unit of this turn names as results
}

func (g GuardFinding) empty() bool { return len(g.Outside) == 0 && len(g.Referenced) == 0 }

// guardScan lists regular files changed at or after since below root (to
// depth), skipping tool state at the top and anything inside allowed.
func guardScan(root string, depth int, since time.Time, allowed []string, skip func(rel string, top bool) bool) []string {
	if root == "" || !isDir(root) {
		return nil
	}
	var out []string
	n := 0
	base := strings.Count(filepath.Clean(root), string(filepath.Separator))
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		n++
		if n > guardMaxEntries {
			return filepath.SkipAll
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		top := !strings.Contains(rel, "/")
		if skip != nil && skip(rel, top) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		for _, a := range allowed {
			if insideDir(p, a) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if d.IsDir() {
			if strings.Count(filepath.Clean(p), string(filepath.Separator))-base >= depth {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if info, err := d.Info(); err == nil && !info.ModTime().Before(since) {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func insideDir(p, root string) bool {
	if root == "" {
		return false
	}
	a, b := filepath.Clean(p), filepath.Clean(root)
	if filepath.Separator == '\\' {
		a, b = strings.ToLower(a), strings.ToLower(b)
	}
	return a == b || strings.HasPrefix(a, b+string(filepath.Separator))
}

// topNames are the entries directly in dir (nil when it cannot be read).
func topNames(dir string) map[string]bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make(map[string]bool, len(entries))
	for _, e := range entries {
		out[e.Name()] = true
	}
	return out
}

// newTop are the entries of dir that were not there before (before[dir]),
// except tool state and registered places: new top-level files and folders
// of a home or temp folder, the places an agent invents.
func newTop(dir string, before map[string]map[string]bool, allowed []string, noise *regexp.Regexp) []string {
	old, ok := before[dir]
	if !ok || old == nil {
		return nil
	}
	var out []string
	for name := range topNames(dir) {
		if old[name] || guardToolState.MatchString(name) || (noise != nil && noise.MatchString(name)) {
			continue
		}
		p := filepath.Join(dir, name)
		in := false
		for _, a := range allowed {
			in = in || insideDir(p, a) || insideDir(a, p)
		}
		if !in {
			if isDir(p) {
				p += string(filepath.Separator)
			}
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// GuardLook is one look after a turn.  Scratch in the session workspace or
// the OS temp dir is the agent's own and is not reported (the user,
// 2026-11-26); reported are new top-level entries of the home folder
// outside the registered places (before: its entries when the turn
// began), and workspace files changed this turn that ARK units written
// this turn name (referenced: the subset of candidates mentioned in ARK,
// nil = not checked) - results must live in a task's folder.
func GuardLook(f *sessionstart.Folder, workspace, home string, since time.Time, before map[string]map[string]bool,
	referenced func(candidates []string) []string) GuardFinding {
	if f == nil {
		return GuardFinding{}
	}
	allowed := append(append([]string{}, f.Writable...), f.Path)
	var g GuardFinding
	g.Outside = newTop(home, before, append(allowed, workspace), nil)
	if referenced != nil {
		cands := guardScan(workspace, 4, since, nil, func(rel string, top bool) bool {
			return top && (arkWorkspaceFiles[rel] || strings.HasPrefix(rel, "."))
		})
		if len(cands) > 0 {
			g.Referenced = referenced(cands)
		}
	}
	return g
}

// guardText is the warning shown in the session.
func guardText(g GuardFinding) string {
	list := func(ps []string) string {
		if len(ps) > guardMaxListed {
			return strings.Join(ps[:guardMaxListed], ", ") + ", … (" + itoa(len(ps)) + " in all)"
		}
		return strings.Join(ps, ", ")
	}
	var parts []string
	if len(g.Outside) > 0 {
		parts = append(parts, "files outside the places registered for this device: "+list(g.Outside)+
			". Move them into the folder of the task they belong to (or ask in a decision).")
	}
	if len(g.Referenced) > 0 {
		parts = append(parts, "results recorded in ARK but kept in the session workspace: "+list(g.Referenced)+
			". Move them into the folder of their task (GET /api/agent/v1/setup/paths?unit=<UID>) and update the paths in ARK.")
	}
	return "ARK file check after this turn: " + strings.Join(parts, " ")
}

func itoa(n int) string { return pyjson.Str(int64(n)) }

// guardHome is the home folder of the account the session's agent runs as.
func guardHome(meta *pyjson.Object) string {
	if h := strOr(meta.Value("linux_account_home")); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

// markTurnStart remembers when a turn began and what the home folder held
// then (the guard's baseline).  Only for sessions with a
// task space: without one there is nothing registered to compare with.
func (m *Manager) markTurnStart(s *Session) {
	m.mu.Lock()
	f := folderFromMeta(s.Meta.Value("task_folder"))
	home := guardHome(s.Meta)
	m.mu.Unlock()
	if f == nil || f.TaskSpace == "" {
		return
	}
	before := map[string]map[string]bool{home: topNames(home)}
	m.mu.Lock()
	s.guardSince = time.Now().Add(-2 * time.Second)
	s.guardBefore = before
	m.mu.Unlock()
}

// guardAfterTurn runs the look for a finished turn and reports findings in
// the session (a warning notice in its transcript, meta "file_check").
func (m *Manager) guardAfterTurn(s *Session) {
	m.mu.Lock()
	since, before := s.guardSince, s.guardBefore
	f := folderFromMeta(s.Meta.Value("task_folder"))
	home := guardHome(s.Meta)
	pid := strOr(objOr(s.Meta.Value("project")).Value("id"))
	m.mu.Unlock()
	if since.IsZero() || f == nil || f.TaskSpace == "" {
		return
	}
	var referenced func([]string) []string
	if m.ReferencedFiles != nil && pid != "" {
		referenced = func(c []string) []string { return m.ReferencedFiles(pid, since, c) }
	}
	g := GuardLook(f, s.Workspace(), home, since, before, referenced)
	m.mu.Lock()
	s.guardSince, s.guardBefore = time.Time{}, nil
	if g.empty() {
		s.Meta.Delete("file_check")
		m.saveLocked(s)
		m.mu.Unlock()
		return
	}
	outside, refs := make([]any, 0, len(g.Outside)), make([]any, 0, len(g.Referenced))
	for _, p := range g.Outside {
		outside = append(outside, p)
	}
	for _, p := range g.Referenced {
		refs = append(refs, p)
	}
	s.Meta.Set("file_check", pyjson.NewObject("at", NowISO(), "outside", outside, "referenced", refs))
	m.saveLocked(s)
	m.mu.Unlock()
	line := arkEventPrefix + " " + string(pyjson.Dumps(pyjson.NewObject("type", "notice", "level", "warning", "text", guardText(g))))
	m.appendEvent(s, "stdout", line, "")
}
