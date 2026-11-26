package sessions

// The task folder of a launched session (2026-11-25, T-0128).  At launch
// the server resolves where the session's work goes on this device
// (Hooks.TaskFolder: the exact task folder, the task space and every
// registered place).  The session keeps it in its meta; ARK-TASK.md names
// the folder instead of the session workspace, ARK_TASK_ARTIFACTS points
// into it, and Codex gets the registered places as writable roots.

import (
	"os"
	"path/filepath"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/sessionstart"
)

// folderObject is the meta form of a folder (nil for none).
func folderObject(f *sessionstart.Folder) any {
	if f == nil {
		return nil
	}
	w := make([]any, 0, len(f.Writable))
	for _, p := range f.Writable {
		w = append(w, p)
	}
	return pyjson.NewObject("path", f.Path, "kind", f.Kind, "owner", f.Owner, "task", f.Task, "unit", f.Unit, "unit_type", f.UnitType,
		"task_space", f.TaskSpace, "host_id", f.HostID, "host_label", f.HostLabel, "exists", f.Exists, "writable", w)
}

// FolderFromMeta is the folder a session keeps in its meta ("task_folder").
func FolderFromMeta(v any) *sessionstart.Folder { return folderFromMeta(v) }

// folderFromMeta reads it back (nil when the session has none).
func folderFromMeta(v any) *sessionstart.Folder {
	o, ok := v.(*pyjson.Object)
	if !ok {
		return nil
	}
	f := &sessionstart.Folder{Path: strOr(o.Value("path")), Kind: strOr(o.Value("kind")), Owner: strOr(o.Value("owner")),
		Task: strOr(o.Value("task")), Unit: strOr(o.Value("unit")),
		UnitType: strOr(o.Value("unit_type")), TaskSpace: strOr(o.Value("task_space")), HostID: strOr(o.Value("host_id")),
		HostLabel: strOr(o.Value("host_label")), Exists: pyjson.Truthy(o.Value("exists"))}
	if list, ok := o.Value("writable").([]any); ok {
		for _, x := range list {
			if s := strOr(x); s != "" {
				f.Writable = append(f.Writable, s)
			}
		}
	}
	return f
}

// prepareFolder creates the task folder when its task space exists on this
// machine (sessions run here), so the path ARK names is real.
func prepareFolder(f *sessionstart.Folder) {
	if f == nil || f.Path == "" || f.TaskSpace == "" || !isDir(f.TaskSpace) {
		return
	}
	if err := os.MkdirAll(f.Path, 0o755); err == nil {
		f.Exists = true
	}
}

// folderArtifacts is ARK_TASK_ARTIFACTS for a session with a task folder.
func folderArtifacts(f *sessionstart.Folder) string {
	if f == nil || f.Path == "" || !f.Exists {
		return ""
	}
	return filepath.Join(f.Path, "artifacts")
}

// contractFolderLines replace the session-workspace artifacts hint of
// ARK-TASK.md (nil: keep the old hint, no folder facts).
func contractFolderLines(f *sessionstart.Folder) []string {
	if f == nil {
		return nil
	}
	dev := f.HostID
	if f.HostLabel != "" && f.HostLabel != f.HostID {
		dev = f.HostLabel + " / " + f.HostID
	}
	var lines []string
	switch {
	case f.TaskSpace == "":
		lines = append(lines, "- This device ("+dev+") has no task space: raise the task-space decision before any file "+
			"(POST /api/agent/v1/setup/task-space-decision; see the Start section).")
	case f.Path != "" && f.Kind == sessionstart.FolderUnit:
		lines = append(lines, "- The folder for "+f.Unit+" (device "+dev+"): `"+f.Path+"` — its own folder (no task above it); "+
			"files of this work go there: downloads/, artifacts/, logs/. A task you create for this same work keeps it: put its path on the task.")
	case f.Path != "":
		lines = append(lines, "- The folder for "+f.Unit+" (device "+dev+"): `"+f.Path+"`"+taskOfNote(f)+
			" — files of this work go there: downloads/, artifacts/, logs/.")
	default:
		lines = append(lines, "- No folder yet: this session has no node. Once you pick or create the node or task for the work, get its folder "+
			"(below) and put that work's files there.")
	}
	lines = append(lines, "- "+sessionstart.OtherUnitLine,
		"- This session workspace and the OS temp dir are for your own scratch and temporary downloads; anything a task needs later goes in its folder.")
	if len(f.Writable) > 0 {
		lines = append(lines, "- Registered places on this device (write only here): `"+strings.Join(f.Writable, "`, `")+"`.")
	}
	return lines
}

func taskOfNote(f *sessionstart.Folder) string {
	if f.Unit != "" && f.Task != "" && f.Unit != f.Task {
		return " (task " + f.Task + ", which " + f.Unit + " belongs to)"
	}
	return ""
}

// codexWritableDirs are the --add-dir folders of a writing Codex session:
// the registered places that exist on this machine.
func codexWritableDirs(f *sessionstart.Folder) []string {
	if f == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, p := range append(append([]string{}, f.Writable...), f.Path) {
		if p == "" || seen[p] || !isDir(p) {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// withCodexDirs adds --add-dir for each folder right after `exec`.
func withCodexDirs(argv []string, dirs []string) []string {
	i := indexOf(argv, "exec")
	if i < 0 || len(dirs) == 0 {
		return argv
	}
	var extra []string
	for _, d := range dirs {
		extra = append(extra, "--add-dir", d)
	}
	return insertAt(argv, i+1, extra...)
}
