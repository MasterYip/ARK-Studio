package main

// Which agent a watcher belongs to (T-0109, 2026-11-23).  An agent often
// passes the wrong --owner-pid: PowerShell's $PID is the short-lived shell
// that ran the command, not Codex.  When the given owner is gone, or is a
// process between the watcher and its agent (a shell), the watcher walks up
// its own parent chain to the nearest Codex or Claude Code process (or the
// node/bun process of a CLI wrapper).  Only parent ids and process names
// are read, never another process's command line or environment.

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// procInfo is one process as the owner search sees it.
type procInfo struct {
	PPID int
	Name string
}

// procTable maps process ids to their parent and name.
type procTable map[int]procInfo

// watchProcTable lists the processes (tests replace it).
var watchProcTable = listProcesses

// agentKind is "codex", "claude", "node" (a CLI wrapper's runtime) or "".
func agentKind(name string) string {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(name)))
	base = strings.TrimSuffix(base, ".exe")
	switch {
	case isCodexProcess(base):
		return "codex"
	case base == "claude":
		return "claude"
	case base == "node" || base == "bun":
		return "node"
	}
	return ""
}

// maxOwnerDepth bounds the walk (and any parent-id loop).
const maxOwnerDepth = 64

// nearestAgent walks up from self's parent: the first Codex or Claude Code
// process, else the first node/bun process, else 0.
func (t procTable) nearestAgent(self int) (int, string) {
	seen := map[int]bool{self: true}
	wrapper := 0
	p := t[self].PPID
	for i := 0; i < maxOwnerDepth && p >= 1 && !seen[p]; i++ {
		info, ok := t[p]
		if !ok {
			break
		}
		seen[p] = true
		switch agentKind(info.Name) {
		case "codex", "claude":
			return p, agentKind(info.Name)
		case "node":
			if wrapper == 0 {
				wrapper = p
			}
		}
		p = info.PPID
	}
	if wrapper != 0 {
		return wrapper, "node"
	}
	return 0, ""
}

// isAncestor reports whether pid is on self's parent chain.
func (t procTable) isAncestor(self, pid int) bool {
	seen := map[int]bool{self: true}
	p := t[self].PPID
	for i := 0; i < maxOwnerDepth && p >= 1 && !seen[p]; i++ {
		if p == pid {
			return true
		}
		seen[p] = true
		info, ok := t[p]
		if !ok {
			return false
		}
		p = info.PPID
	}
	return false
}

// resolveOwner picks the agent process a watcher (pid self) belongs to.
// given is --owner-pid (0 = none).  A given Codex or Claude Code process is
// kept, and so is a live process the agent named that is not on the
// watcher's own parent chain.  Otherwise (the given process is gone, or is
// a shell between the watcher and its agent, or none was given) the nearest
// agent above the watcher is used; note says so when it replaces a given pid.
func resolveOwner(t procTable, self, given int) (owner int, kind, note string) {
	if given > 1 {
		if info, ok := t[given]; ok {
			k := agentKind(info.Name)
			if k == "codex" || k == "claude" || !t.isAncestor(self, given) {
				return given, k, ""
			}
		}
	}
	if pid, k := t.nearestAgent(self); pid != 0 {
		if given > 1 {
			why := "is gone"
			if _, ok := t[given]; ok {
				why = "is not your agent (" + t[given].Name + ")"
			}
			note = fmt.Sprintf("pid %d %s; the watcher belongs to %s pid %d, the nearest agent above it", given, why, t[pid].Name, pid)
		}
		return pid, k, note
	}
	if _, ok := t[given]; given > 1 && ok {
		return given, agentKind(t[given].Name), ""
	}
	return 0, "", ""
}

// parseProcStat reads the name and parent id from a Linux /proc/<pid>/stat
// line: "pid (comm) state ppid ...", where comm may hold spaces and ")".
func parseProcStat(line string) (procInfo, bool) {
	lp, rp := strings.IndexByte(line, '('), strings.LastIndexByte(line, ')')
	if lp < 0 || rp < lp {
		return procInfo{}, false
	}
	fields := strings.Fields(line[rp+1:])
	if len(fields) < 2 {
		return procInfo{}, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return procInfo{}, false
	}
	return procInfo{PPID: ppid, Name: line[lp+1 : rp]}, true
}

// parsePS reads `ps -A -o pid=,ppid=,comm=` (comm may be a path with spaces).
func parsePS(out string) procTable {
	t := procTable{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		name := strings.TrimSpace(line)
		name = strings.TrimSpace(strings.TrimPrefix(name, f[0]))
		name = strings.TrimSpace(strings.TrimPrefix(name, f[1]))
		t[pid] = procInfo{PPID: ppid, Name: filepath.Base(name)}
	}
	return t
}
