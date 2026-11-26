package sessionstart

import (
	"strings"
	"testing"
)

func TestFolderNote(t *testing.T) {
	base := Folder{HostID: "709270ec09fc", HostLabel: "lab", TaskSpace: "/srv/tasks", Writable: []string{"/srv/tasks", "/srv/code"}}
	task := base
	task.Unit, task.UnitType, task.Task, task.Path, task.Kind = "T-0003", "task", "T-0003", "/srv/tasks/20261125_T-0003-drift", FolderTask
	child := task
	child.Unit, child.UnitType = "X-0001", "experiment"
	orphan := base
	orphan.Unit, orphan.UnitType, orphan.Kind, orphan.Owner, orphan.Path = "I-0001", "idea", FolderUnit, "I-0001", "/srv/tasks/20261126_I-0001-drift"
	none := base
	noSpace := Folder{HostID: "709270ec09fc"}
	for _, c := range []struct {
		name string
		f    Folder
		want []string
		not  []string
	}{
		{"task", task, []string{"the folder for T-0003 is /srv/tasks/20261125_T-0003-drift (device lab, 709270ec09fc)",
			"setup/paths?unit=<UID>", "ark-server agent folder <UID>", "may stay in this session's workspace"}, []string{"not a task"}},
		{"child", child, []string{"X-0001 is an experiment, not a task; it belongs to task T-0003", "The folder for X-0001 is T-0003's folder /srv/tasks/20261125_T-0003-drift"}, []string{"keeps this folder"}},
		{"orphan", orphan, []string{"I-0001 is an idea with no task above it", "its own folder /srv/tasks/20261126_I-0001-drift", "A task you create for this same work keeps this folder"}, nil},
		{"none", none, []string{"this session has no node yet, so no folder", "task space /srv/tasks", "setup/paths?unit=<UID>"}, []string{"session-"}},
		{"no task space", noSpace, []string{"has no task space", "task-space-decision", "Do not start the work before that", "never inside this session's workspace or the OS temp dir"}, []string{"<task ID>"}},
	} {
		got := FolderNote(&c.f)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: lacks %q:\n%s", c.name, w, got)
			}
		}
		for _, w := range c.not {
			if strings.Contains(got, w) {
				t.Errorf("%s: has %q:\n%s", c.name, w, got)
			}
		}
	}
}

func TestComposeAddsFolder(t *testing.T) {
	f := &Folder{HostID: "h1", TaskSpace: "/srv/tasks", Unit: "X-0001", UnitType: "experiment", Task: "T-0003", Path: "/srv/tasks/20261125_T-0003", Kind: FolderTask}
	ready := Setup{Known: true, ProjectState: "confirmed"}
	st := Compose(Facts{Setup: ready, Task: &Task{UID: "X-0001", Type: "experiment", Title: "Drift", Status: "active"}, Folder: f})
	if st.Mode != ModeContinue || !strings.Contains(st.Text, "Start: continue the experiment X-0001") ||
		!strings.Contains(st.Text, "Files: X-0001 is an experiment") {
		t.Fatalf("continue:\n%s", st.Text)
	}
	st = Compose(Facts{Setup: ready, Prompt: "do it", Folder: f})
	if st.Mode != ModeFollow || !strings.Contains(st.Text, "Files:") {
		t.Fatalf("follow:\n%s", st.Text)
	}
	st = Compose(Facts{Setup: Setup{Known: true, ProjectState: "draft"}, Folder: f})
	if st.Mode != ModeSetup || strings.Contains(st.Text, "Files:") {
		t.Fatalf("setup has a Files line:\n%s", st.Text)
	}
	st = Compose(Facts{Setup: ready, Task: &Task{UID: "T-0003", Type: "task", Title: "Drift", Status: "active"}})
	if !strings.Contains(st.Text, "Start: continue T-0003 ") || strings.Contains(st.Text, "Files:") {
		t.Fatalf("no folder facts:\n%s", st.Text)
	}
}
