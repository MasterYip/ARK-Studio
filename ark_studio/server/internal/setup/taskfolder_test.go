package setup

import (
	"os"
	"path/filepath"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

func TestTaskFolderNames(t *testing.T) {
	for _, c := range []struct{ date, uid, title, want string }{
		{"2026-11-25T08:00:00Z", "T-0003", "Line-3 drift: analysis (v2)", "20261125_T-0003-line-3-drift-analysis-v2"},
		{"2026-11-25", "T-0004", "时间重排 v2", "20261125_T-0004-时间重排-v2"},
		{"2026-11-25", "T-0005", "!!!", "20261125_T-0005"},
	} {
		if got := TaskFolderName(c.date, c.uid, c.title); got != c.want {
			t.Errorf("TaskFolderName(%q) = %q, want %q", c.title, got, c.want)
		}
	}
	if n := []rune(Slug("a very long title that goes on and on and on beyond forty characters")); len(n) > maxSlugRunes {
		t.Fatalf("slug too long: %d", len(n))
	}
}

func TestFindTaskFolder(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"20261120_T-0012-old", "20261121_T-00120-other", "20261124_T-0012", "notes"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := FindTaskFolder(root, "T-0012")
	if filepath.Base(got) != "20261124_T-0012" {
		t.Fatalf("found %q", got)
	}
	if FindTaskFolder(root, "T-0099") != "" || FindTaskFolder(filepath.Join(root, "missing"), "T-0012") != "" {
		t.Fatal("found a folder that is not there")
	}
}

func TestWritableRoots(t *testing.T) {
	paths := pyjson.NewObject("task_space", pyjson.NewObject("key", "tasks", "name", "Tasks", "path", "/srv/tasks"),
		"repositories", []any{pyjson.NewObject("key", "code", "path", "/srv/code"), pyjson.NewObject("key", "far", "path", nil)},
		"storage", []any{pyjson.NewObject("key", "data", "path", "/srv/data")}, "artifacts", "/srv/tasks")
	got := WritableRoots(paths)
	if len(got) != 3 {
		t.Fatalf("roots: %v", got)
	}
	want := []string{"task_space:/srv/tasks", "repository:/srv/code", "storage:/srv/data"}
	for i, x := range got {
		o := x.(*pyjson.Object)
		if pyjson.Str(o.Value("kind"))+":"+pyjson.Str(o.Value("path")) != want[i] {
			t.Fatalf("root %d: %v", i, o)
		}
	}
}

func TestScratchPlace(t *testing.T) {
	for _, p := range []string{"/tmp/tasks", "/var/tmp/x", filepath.Join(os.TempDir(), "x"), "/data/ark/agent-sessions/s_1/workspace/tasks"} {
		if ScratchPlace(p) == "" {
			t.Errorf("%s accepted", p)
		}
	}
	if ScratchPlace("/srv/sessions/s_1/tasks", "/srv/sessions") == "" {
		t.Error("session root accepted")
	}
	for _, p := range []string{"/srv/ark-tasks", "D:/ARK-tasks", "/tmpfiles/x"} {
		if why := ScratchPlace(p); why != "" {
			t.Errorf("%s refused: %s", p, why)
		}
	}
}
