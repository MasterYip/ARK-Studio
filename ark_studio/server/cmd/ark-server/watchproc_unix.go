//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// watchProcessAlive reports whether one process (the watcher's owner) runs.
func watchProcessAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// watchProcessName is the name of one process: /proc on Linux, ps elsewhere.
func watchProcessName(pid int) string {
	if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm"); err == nil {
		return strings.TrimSpace(string(b))
	}
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// listProcesses reads every process's parent id and name: /proc/<pid>/stat
// on Linux, `ps -A -o pid=,ppid=,comm=` elsewhere.  Nothing else is read.
func listProcesses() (procTable, error) {
	if entries, err := os.ReadDir("/proc"); err == nil {
		t := procTable{}
		for _, e := range entries {
			pid, err := strconv.Atoi(e.Name())
			if err != nil {
				continue
			}
			b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
			if err != nil {
				continue
			}
			if info, ok := parseProcStat(string(b)); ok {
				t[pid] = info
			}
		}
		if len(t) > 0 {
			return t, nil
		}
	}
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=,comm=").Output()
	if err != nil {
		return nil, err
	}
	return parsePS(string(out)), nil
}

// detachProcess puts the child in a session of its own, without a terminal.
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
