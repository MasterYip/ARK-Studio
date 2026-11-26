package sessionstart

// Where a session's files go (2026-11-25, T-0128; 2026-11-26, T-0132).
// Folders follow the unit of work, not the session: task-related files go
// in the folder of the task they belong to, or, with no task above a unit,
// in that unit's own folder; the Start section names the starting unit's
// folder and how to get the folder of any other unit.  The agent's own
// scratch may stay in its session workspace or the OS temp dir.

import (
	"fmt"
	"strings"
)

// Kinds of folder.
const (
	FolderTask = "task" // the folder of the task the work belongs to
	FolderUnit = "unit" // the unit's own folder: no task above it
)

// Folder is the work folder of the session on its device.
type Folder struct {
	HostID, HostLabel string
	Kind              string   // FolderTask, FolderUnit ("" = none)
	Owner             string   // the uid the folder is named after
	TaskSpace         string   // this device's task space; "" = none registered
	Unit, UnitType    string   // the session's unit ("" = none)
	Task              string   // the task the folder belongs to ("" = none found)
	Path              string   // the absolute task folder ("" = no task or no task space)
	Exists            bool     // the folder exists on this machine
	Writable          []string // every registered place on this device
}

func (f *Folder) device() string {
	if f.HostLabel != "" && f.HostLabel != f.HostID {
		return f.HostLabel + ", " + f.HostID
	}
	return f.HostID
}

func article(kind string) string {
	if kind != "" && strings.ContainsRune("aeiou", rune(kind[0])) {
		return "an " + kind
	}
	return "a " + kind
}

// OtherUnitLine says how to get the folder of any other unit.
const OtherUnitLine = "When you work on another node or task, get its folder with GET /api/agent/v1/setup/paths?unit=<UID> " +
	"(task_folder.path) or `ark-server agent folder <UID>`, and put that work's files there."

const scratchLine = "Your own scratch and temporary downloads may stay in this session's workspace or the OS temp dir; anything a task " +
	"needs later (inputs, scripts, results) goes in its folder. Write or change files only in the places registered for this device " +
	"(GET /api/agent/v1/setup/paths \"writable\"; the session workspace for scratch); anything else needs my decision first."

// FolderNote is the "Files:" line of the Start section ("" = nothing to say).
func FolderNote(f *Folder) string {
	if f == nil {
		return ""
	}
	if f.TaskSpace == "" {
		return fmt.Sprintf("Files: this device (%s) has no task space in setup. Before you write any file or download anything, "+
			"POST /api/agent/v1/setup/task-space-decision with 2-3 options (path, free space, why; never inside this session's "+
			"workspace or the OS temp dir) and your recommendation, then wait for my answer. Do not start the work before that.", f.device())
	}
	var head string
	switch {
	case f.Path != "" && f.Kind == FolderTask && f.Unit != "" && f.Unit != f.Task:
		head = fmt.Sprintf("Files: %s is %s, not a task; it belongs to task %s. The folder for %s is %s's folder %s (device %s).",
			f.Unit, article(f.UnitType), f.Task, f.Unit, f.Task, f.Path, f.device())
	case f.Path != "" && f.Kind == FolderUnit:
		head = fmt.Sprintf("Files: %s is %s with no task above it; the folder for %s is its own folder %s (device %s). "+
			"A task you create for this same work keeps this folder: put its path on the task.",
			f.Unit, article(f.UnitType), f.Unit, f.Path, f.device())
	case f.Path != "":
		head = fmt.Sprintf("Files: the folder for %s is %s (device %s).", f.Task, f.Path, f.device())
	default:
		head = fmt.Sprintf("Files: this session has no node yet, so no folder: once you pick or create the node or task for the work, "+
			"its folder is in the task space %s (device %s).", f.TaskSpace, f.device())
	}
	lines := []string{head}
	if f.Path != "" {
		lines = append(lines, "Inputs in downloads/, scripts and results in artifacts/, logs in logs/ of that folder.")
	}
	lines = append(lines, OtherUnitLine, scratchLine)
	return strings.Join(lines, "\n")
}

func joinFolder(root, rel string) string {
	sep := "/"
	if strings.Contains(root, "\\") && !strings.Contains(root, "/") {
		sep = "\\"
	}
	return strings.TrimRight(root, "/\\") + sep + rel + sep
}

// withFolder adds the Files lines to a start that leads to work.
func withFolder(st Start, f *Folder) Start {
	if f == nil {
		return st
	}
	switch st.Mode {
	case ModeSetup, ModeDeviceSetup:
		return st // no work yet: setup first
	}
	if note := FolderNote(f); note != "" {
		st.Text = strings.TrimRight(st.Text, "\n") + "\n" + note
	}
	return st
}
