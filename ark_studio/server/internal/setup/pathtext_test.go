package setup

import (
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

func TestParseLogicalAndJoin(t *testing.T) {
	for _, c := range []struct {
		ref, rel, space string
		ok              bool
	}{
		{"task:20261124_T-0117/artifacts/", "20261124_T-0117/artifacts/", "", true},
		{"task:a%20b/c.png?space=hoi-tasks", "a b/c.png", "hoi-tasks", true},
		{"task:./", "", "", true},
		{"task:../etc", "", "", false},
		{"task:/abs", "", "", false},
		{"task:C:/x", "", "", false},
		{"file:///E:/x", "", "", false},
	} {
		rel, space, ok := ParseLogical(c.ref)
		if rel != c.rel || space != c.space || ok != c.ok {
			t.Errorf("ParseLogical(%q) = %q %q %v", c.ref, rel, space, ok)
		}
	}
	if got := JoinTaskPath("E:\\work\\tasks", "T-1/artifacts/a.png"); got != "E:\\work\\tasks\\T-1\\artifacts\\a.png" {
		t.Errorf("windows join: %s", got)
	}
	if got := JoinTaskPath("/data/tasks/", "T-1/"); got != "/data/tasks/T-1/" {
		t.Errorf("posix join: %s", got)
	}
	if got := JoinTaskPath("D:/tasks", ""); got != "D:/tasks" {
		t.Errorf("root: %s", got)
	}
}

func TestRewritePrefixWindowsAndPosix(t *testing.T) {
	body := strings.Join([]string{
		"## Resource paths",
		"- [Task folder](file:///E:/Work/Tasks/20261124_T-1/)",
		"- E:\\Work\\Tasks\\20261124_T-1\\artifacts\\a.png and e:/work/tasks/x.txt",
		"- untouched: E:/Work/Tasks-old/y and XE:/Work/Tasks/z",
		"- inline `E:/Work/Tasks/inline.md`",
		"```",
		"cp E:/Work/Tasks/in-block.txt .",
		"```",
		"after E:/Work/Tasks",
	}, "\n")
	got, n := RewritePrefix(body, "E:/Work/Tasks/", "D:/ARK-tasks")
	want := strings.Join([]string{
		"## Resource paths",
		"- [Task folder](file:///D:/ARK-tasks/20261124_T-1/)",
		"- D:/ARK-tasks/20261124_T-1/artifacts/a.png and D:/ARK-tasks/x.txt",
		"- untouched: E:/Work/Tasks-old/y and XE:/Work/Tasks/z",
		"- inline `D:/ARK-tasks/inline.md`",
		"```",
		"cp E:/Work/Tasks/in-block.txt .",
		"```",
		"after D:/ARK-tasks",
	}, "\n")
	if got != want || n != 5 {
		t.Fatalf("n=%d\n%s", n, got)
	}
	// Idempotent: nothing left to move.
	if again, n2 := RewritePrefix(got, "E:/Work/Tasks", "D:/ARK-tasks"); n2 != 0 || again != got {
		t.Fatalf("second pass changed %d", n2)
	}
	posix := "see /data/me/tasks/T-1/log.txt, file:///data/me/tasks/T-2 and /data/me/tasks2/no"
	got, n = RewritePrefix(posix, "/data/me/tasks", "/mnt/big/tasks")
	if got != "see /mnt/big/tasks/T-1/log.txt, file:///mnt/big/tasks/T-2 and /data/me/tasks2/no" || n != 2 {
		t.Fatalf("posix n=%d %s", n, got)
	}
	// POSIX paths are case-sensitive.
	if _, n := RewritePrefix("/Data/Me/Tasks/x", "/data/me/tasks", "/x"); n != 0 {
		t.Fatal("posix match ignored case")
	}
	// A whole drive or "/" is not a prefix.
	for _, bad := range []string{"E:/", "/", "relative/path", ""} {
		if _, n := RewritePrefix("E:/a /b", bad, "/x/y"); n != 0 {
			t.Fatalf("prefix %q accepted", bad)
		}
	}
	// Windows -> POSIX: separators of the rest follow the new prefix.
	if got, _ := RewritePrefix(`E:\Work\Tasks\T-1\a.png`, "E:/Work/Tasks", "/data/tasks"); got != "/data/tasks/T-1/a.png" {
		t.Fatalf("cross style: %s", got)
	}
	// Spaces in file: URLs.
	if got, n := RewritePrefix("[x](file:///E:/My%20Tasks/T-1/)", "E:/My Tasks", "D:/T"); n != 1 || got != "[x](file:///D:/T/T-1/)" {
		t.Fatalf("encoded: %d %s", n, got)
	}
}

func TestToLogical(t *testing.T) {
	roots := []LogicalRoot{{Path: "E:/Work/Tasks"}, {Path: "/data/me/tasks"}, {Path: "/data/me/tasks/legacy", Space: "old"}}
	body := strings.Join([]string{
		"[Task folder](file:///E:/Work/Tasks/20261124_T-1/)",
		"render: /data/me/tasks/20261124_T-1/artifacts/out.mp4",
		"old: /data/me/tasks/legacy/T-9/notes.md",
		"root: E:\\Work\\Tasks",
		"other: E:/x stays",
		"```sh",
		"ls /data/me/tasks/in-block",
		"```",
	}, "\n")
	got, n := ToLogical(body, roots)
	want := strings.Join([]string{
		"[Task folder](task:20261124_T-1/)",
		"render: task:20261124_T-1/artifacts/out.mp4",
		"old: task:T-9/notes.md?space=old",
		"root: task:./",
		"other: E:/x stays",
		"```sh",
		"ls /data/me/tasks/in-block",
		"```",
	}, "\n")
	if got != want || n != 4 {
		t.Fatalf("n=%d\n%s", n, got)
	}
	if again, n2 := ToLogical(got, roots); n2 != 0 || again != got {
		t.Fatal("ToLogical is not idempotent")
	}
	// Every logical path resolves back on each device.
	rel, _, ok := ParseLogical("task:20261124_T-1/artifacts/out.mp4")
	if !ok || JoinTaskPath("/data/me/tasks", rel) != "/data/me/tasks/20261124_T-1/artifacts/out.mp4" ||
		JoinTaskPath("E:\\Work\\Tasks", rel) != "E:\\Work\\Tasks\\20261124_T-1\\artifacts\\out.mp4" {
		t.Fatal("resolution per device")
	}
}

func TestChooseOption(t *testing.T) {
	opts := []any{
		pyjson.NewObject("path", "D:/ARK-tasks"), pyjson.NewObject("path", "E:/Work/ARK-tasks"),
	}
	for answer, want := range map[string]string{
		"`D:/ARK-tasks` — 812 GB free (recommended)": "D:/ARK-tasks",
		"2":                                "E:/Work/ARK-tasks",
		"use e:\\work\\ark-tasks":          "E:/Work/ARK-tasks",
		"Other: F:/elsewhere/tasks please": "F:/elsewhere/tasks",
	} {
		if got, ok := chooseOption(answer, opts); !ok || got != want {
			t.Errorf("%q -> %q %v", answer, got, ok)
		}
	}
	if _, ok := chooseOption("not sure yet", opts); ok {
		t.Error("an answer without a path chose something")
	}
}
