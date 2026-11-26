package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

// gitRepo makes a repository with a history for one file:
//
//	c1 add notes.md, c2 edit it, c3 rename it to docs/guide.md, c4 edit it.
func gitRepo(t *testing.T, dir string) []string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Ana Tester", "-c", "user.email=ana@example.com",
			"-c", "commit.gpgsign=false", "-c", "core.autocrlf=false"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(rel, text string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q")
	var revs []string
	commit := func(msg string) {
		run("add", "-A")
		run("commit", "-q", "-m", msg)
		revs = append(revs, run("rev-parse", "HEAD"))
	}
	write("notes.md", "# Notes\n\nfirst\n")
	commit("Add notes")
	write("notes.md", "# Notes\n\nfirst\nsecond\n")
	commit("Extend notes")
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	run("mv", "notes.md", "docs/guide.md")
	commit("Move notes to the guide")
	write("docs/guide.md", "# Guide\n\nfirst\nsecond\nthird\n")
	commit("Rename the heading")
	write("other.txt", "unrelated\n")
	commit("Unrelated change")
	return revs
}

func entries(t *testing.T, out *pyjson.Object) []*pyjson.Object {
	t.Helper()
	list, _ := out.Value("entries").([]any)
	res := make([]*pyjson.Object, len(list))
	for i, item := range list {
		res[i] = item.(*pyjson.Object)
	}
	return res
}

func TestTimelineFollowsRenames(t *testing.T) {
	root := t.TempDir()
	revs := gitRepo(t, root)
	out, err := Timeline(root, "docs/guide.md", 0)
	if err != nil {
		t.Fatal(err)
	}
	got := entries(t, out)
	if len(got) != 4 || out.Value("is_repo") != true || out.Value("truncated") != false {
		t.Fatalf("timeline: %d entries, %v", len(got), pyjson.Dumps(out))
	}
	want := []struct{ subject, path, rev string }{
		{"Rename the heading", "docs/guide.md", revs[3]},
		{"Move notes to the guide", "docs/guide.md", revs[2]},
		{"Extend notes", "notes.md", revs[1]},
		{"Add notes", "notes.md", revs[0]},
	}
	for i, w := range want {
		e := got[i]
		if e.Value("subject") != w.subject || e.Value("path") != w.path || e.Value("rev") != w.rev || e.Value("author") != "Ana Tester" {
			t.Errorf("entry %d: %s", i, pyjson.Dumps(e))
		}
		if !strings.HasPrefix(pyjson.Str(e.Value("time")), "20") || len(pyjson.Str(e.Value("short"))) < 4 {
			t.Errorf("entry %d time/short: %s", i, pyjson.Dumps(e))
		}
	}
	// The limit caps the list and says so.
	out, err = Timeline(root, "docs/guide.md", 2)
	if err != nil || len(entries(t, out)) != 2 || out.Value("truncated") != true {
		t.Fatalf("limit: %v %v", err, pyjson.Dumps(out))
	}
	// A file git never saw has an empty history.
	if err := os.WriteFile(filepath.Join(root, "new.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = Timeline(root, "new.md", 0)
	if err != nil || len(entries(t, out)) != 0 || out.Value("is_repo") != true {
		t.Fatalf("untracked: %v %v", err, out)
	}
}

func TestTimelineInsideSubfolderProject(t *testing.T) {
	repo := t.TempDir()
	gitRepo(t, repo)
	// The project is the docs/ folder of a larger repository: paths are
	// project-relative, and commits from before the file moved into the
	// project keep their repository path and say they are outside.
	out, err := Timeline(filepath.Join(repo, "docs"), "guide.md", 0)
	if err != nil || out.Value("is_repo") != true {
		t.Fatalf("subfolder: %v %v", err, out)
	}
	got := entries(t, out)
	if len(got) != 4 {
		t.Fatalf("subfolder: %d entries", len(got))
	}
	for i, want := range []struct {
		path   string
		inside bool
	}{{"guide.md", true}, {"guide.md", true}, {"notes.md", false}, {"notes.md", false}} {
		if got[i].Value("path") != want.path || got[i].Value("inside") != want.inside {
			t.Errorf("entry %d: %s", i, pyjson.Dumps(got[i]))
		}
	}
	// Showing an old version uses the project-relative path.
	shown, err := ShowFile(filepath.Join(repo, "docs"), "guide.md", pyjson.Str(got[1].Value("rev")))
	if err != nil || !strings.HasPrefix(pyjson.Str(shown.Value("content")), "# Notes") {
		t.Fatalf("show in subfolder: %v %v", err, shown)
	}
}

func TestTimelineRefusesPathsOutsideTheProject(t *testing.T) {
	root := t.TempDir()
	gitRepo(t, root)
	for path, status := range map[string]int{
		"../x.md": 400, "/etc/passwd": 400, "C:/Windows/win.ini": 400, ".git/config": 403, "": 400, "docs": 400,
	} {
		_, err := Timeline(root, path, 0)
		we, ok := err.(*Error)
		if !ok || we.Status != status {
			t.Errorf("timeline %q: %v, want %d", path, err, status)
		}
		_, err = ShowFile(root, path, "HEAD")
		if err == nil {
			t.Errorf("show %q accepted", path)
		}
	}
}

func TestTimelineWithoutRepository(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("# A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := Timeline(root, "a.md", 0)
	if err != nil || out.Value("is_repo") != false || len(entries(t, out)) != 0 {
		t.Fatalf("no repo: %v %v", err, out)
	}
	if _, err := ShowFile(root, "a.md", "abcdef1"); err == nil || err.(*Error).Status != 400 {
		t.Fatalf("show without repo: %v", err)
	}
}

func TestShowFile(t *testing.T) {
	root := t.TempDir()
	revs := gitRepo(t, root)
	out, err := ShowFile(root, "notes.md", revs[0])
	if err != nil {
		t.Fatal(err)
	}
	if out.Value("content") != "# Notes\n\nfirst\n" || out.Value("subject") != "Add notes" || out.Value("read_only") != true {
		t.Fatalf("show: %s", pyjson.Dumps(out))
	}
	// Short hashes work; the reply carries the full one.
	out, err = ShowFile(root, "docs/guide.md", revs[3][:8])
	if err != nil || out.Value("rev") != revs[3] || !strings.Contains(pyjson.Str(out.Value("content")), "# Guide") {
		t.Fatalf("short: %v %v", err, out)
	}
	for _, tc := range []struct {
		path, rev string
		status    int
	}{
		{"notes.md", "HEAD", 400},       // names are not accepted, only hashes
		{"notes.md", "--all", 400},      // nor options
		{"notes.md", "abc", 400},        // too short
		{"notes.md", "deadbeef00", 404}, // no such commit
		{"docs/guide.md", revs[0], 404}, // not there yet
	} {
		_, err := ShowFile(root, tc.path, tc.rev)
		we, ok := err.(*Error)
		if !ok || we.Status != tc.status {
			t.Errorf("show %s@%s: %v, want %d", tc.path, tc.rev, err, tc.status)
		}
	}
}
