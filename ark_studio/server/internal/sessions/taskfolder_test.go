package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/sessionstart"
)

// A writing Codex session gets the registered places that exist here as
// --add-dir roots right after `exec`; nothing else is widened.
func TestCodexWritableRoots(t *testing.T) {
	root := t.TempDir()
	ts, repo := filepath.Join(root, "tasks"), filepath.Join(root, "repo")
	for _, d := range []string{ts, repo} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f := &sessionstart.Folder{TaskSpace: ts, Path: filepath.Join(ts, "20261125_T-0001-x"), Writable: []string{ts, repo, filepath.Join(root, "gone")}}
	if dirs := codexWritableDirs(f); len(dirs) != 2 || dirs[0] != ts || dirs[1] != repo {
		t.Fatalf("dirs before the folder exists: %v", dirs)
	}
	prepareFolder(f)
	if !f.Exists || !isDir(f.Path) || folderArtifacts(f) != filepath.Join(f.Path, "artifacts") {
		t.Fatalf("folder not prepared: %+v", f)
	}
	argv := []string{"codex", "exec", "-c", `default_permissions="ark_session"`, "--json", "go"}
	got := withCodexDirs(argv, codexWritableDirs(f))
	want := []string{"codex", "exec", "--add-dir", ts, "--add-dir", repo, "--add-dir", f.Path, "-c", `default_permissions="ark_session"`, "--json", "go"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("argv:\n%v\nwant\n%v", got, want)
	}
	if out := withCodexDirs([]string{"claude", "-p"}, []string{ts}); len(out) != 2 {
		t.Fatalf("non-codex argv changed: %v", out)
	}
	// The folder round-trips through the session meta.
	back := folderFromMeta(folderObject(f))
	if back.Path != f.Path || back.TaskSpace != ts || len(back.Writable) != 3 || !back.Exists {
		t.Fatalf("meta round trip: %+v", back)
	}
}

// ARK-TASK.md names the task folder instead of the workspace artifacts.
func TestContractNamesTaskFolder(t *testing.T) {
	m := &Manager{}
	preset := pyjson.NewObject("id", "codex", "label", "Codex CLI")
	project := pyjson.NewObject("id", "prj_1", "name", "P", "path", "/p")
	f := &sessionstart.Folder{HostID: "h1", TaskSpace: "/srv/tasks", Path: "/srv/tasks/20261125_T-0003-x", Task: "T-0003", Kind: sessionstart.FolderTask,
		Unit: "X-0001", UnitType: "experiment", Writable: []string{"/srv/tasks", "/srv/code"}}
	text := m.contract("s_1", preset, project, "X-0001", ".", "http://x/api/task-agent/v1", "full_access", nil, "/w", f)
	for _, w := range []string{"The folder for X-0001 (device h1): `/srv/tasks/20261125_T-0003-x` (task T-0003, which X-0001 belongs to)",
		"ark-server agent folder <UID>", "for your own scratch and temporary downloads", "`/srv/tasks`, `/srv/code`"} {
		if !strings.Contains(text, w) {
			t.Fatalf("contract lacks %q:\n%s", w, text)
		}
	}
	if strings.Contains(text, "Session workspace artifacts") {
		t.Fatalf("contract still names the workspace artifacts:\n%s", text)
	}
	own := &sessionstart.Folder{HostID: "h1", TaskSpace: "/srv/tasks", Path: "/srv/tasks/20261126_I-0003-idea", Kind: sessionstart.FolderUnit,
		Unit: "I-0003", UnitType: "idea", Owner: "I-0003", Writable: []string{"/srv/tasks"}}
	if text := m.contract("s_1", preset, project, "I-0003", ".", "http://x", "full_access", nil, "/w", own); !strings.Contains(text,
		"The folder for I-0003 (device h1): `/srv/tasks/20261126_I-0003-idea` — its own folder (no task above it)") {
		t.Fatalf("own folder contract:\n%s", text)
	}
	if old := m.contract("s_1", preset, project, "", ".", "http://x", "full_access", nil, "/w", nil); !strings.Contains(old, "Session workspace artifacts") {
		t.Fatalf("no folder facts: %s", old)
	}
}

// The guard leaves scratch alone (session workspace, OS temp) and names
// new top-level entries of home outside the registered places, and
// workspace files that ARK units name as results.
func TestGuardLook(t *testing.T) {
	root := t.TempDir()
	ws, home, ts := filepath.Join(root, "ws"), filepath.Join(root, "home"), filepath.Join(root, "home", "tasks")
	for _, d := range []string{ws, home, ts, filepath.Join(home, "old")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(ws, "ARK-TASK.md"))
	before := map[string]map[string]bool{home: topNames(home)}
	since := time.Now().Add(-time.Second)
	scratch, result := filepath.Join(ws, "dl", "a.csv"), filepath.Join(ws, "out", "drift.png")
	write(scratch)
	write(result)
	write(filepath.Join(ws, "ARK-TASK.md"))
	write(filepath.Join(home, "scratch", "b.py"))
	write(filepath.Join(home, ".cache", "c"))
	write(filepath.Join(home, "old", "d"))
	write(filepath.Join(ts, "20261125_T-0001", "e.png"))
	f := &sessionstart.Folder{TaskSpace: ts, Writable: []string{ts}}
	var asked []string
	referenced := func(c []string) []string {
		asked = c
		var out []string
		for _, p := range c {
			if p == result { // a unit body names it as a result
				out = append(out, p)
			}
		}
		return out
	}
	g := GuardLook(f, ws, home, since, before, referenced)
	if len(g.Outside) != 1 || g.Outside[0] != filepath.Join(home, "scratch")+string(filepath.Separator) {
		t.Fatalf("outside: %v", g.Outside)
	}
	if len(g.Referenced) != 1 || g.Referenced[0] != result || len(asked) != 2 {
		t.Fatalf("referenced: %v (asked %v)", g.Referenced, asked)
	}
	text := guardText(g)
	if !strings.Contains(text, "outside the places registered") || !strings.Contains(text, "results recorded in ARK but kept in the session workspace") ||
		strings.Contains(text, "a.csv") {
		t.Fatalf("text: %s", text)
	}
	// Scratch only: nothing to say.
	if g := GuardLook(f, ws, home, since, map[string]map[string]bool{home: topNames(home)}, func([]string) []string { return nil }); !g.empty() {
		t.Fatalf("scratch flagged: %+v", g)
	}
	if GuardLook(nil, ws, home, since, before, nil).Outside != nil {
		t.Fatal("no folder facts should find nothing")
	}
}
