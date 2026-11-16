//go:build windows

package main

import (
	"syscall"
	"time"
	"unsafe"
)

var (
	psapi                = syscall.NewLazyDLL("psapi.dll")
	getProcessMemoryInfo = psapi.NewProc("GetProcessMemoryInfo")
)

type processMemoryCounters struct {
	cb                         uint32
	pageFaultCount             uint32
	peakWorkingSetSize         uintptr
	workingSetSize             uintptr
	quotaPeakPagedPoolUsage    uintptr
	quotaPagedPoolUsage        uintptr
	quotaPeakNonPagedPoolUsage uintptr
	quotaNonPagedPoolUsage     uintptr
	pagefileUsage              uintptr
	peakPagefileUsage          uintptr
}

// sampleTree sums CPU time and memory of pid and all its descendants (a
// Windows venv python.exe is a launcher that runs the real interpreter as a
// child process).
func sampleTree(pid int) (procUsage, bool) {
	snap, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return procUsage{}, false
	}
	defer syscall.CloseHandle(snap)
	parent := map[uint32]uint32{}
	threads := map[uint32]int{}
	var e syscall.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = syscall.Process32First(snap, &e); err == nil; err = syscall.Process32Next(snap, &e) {
		parent[e.ProcessID] = e.ParentProcessID
		threads[e.ProcessID] = int(e.Threads)
	}
	tree := map[uint32]bool{uint32(pid): true}
	for changed := true; changed; {
		changed = false
		for child, p := range parent {
			if tree[p] && !tree[child] && child != 0 {
				tree[child] = true
				changed = true
			}
		}
	}
	var u procUsage
	found := false
	for id := range tree {
		h, err := syscall.OpenProcess(0x1000, false, id) // PROCESS_QUERY_LIMITED_INFORMATION
		if err != nil {
			continue
		}
		var c, x, k, us syscall.Filetime
		if syscall.GetProcessTimes(h, &c, &x, &k, &us) == nil {
			u.cpu += time.Duration((int64(k.HighDateTime)<<32|int64(k.LowDateTime))+(int64(us.HighDateTime)<<32|int64(us.LowDateTime))) * 100
		}
		var m processMemoryCounters
		m.cb = uint32(unsafe.Sizeof(m))
		if r, _, _ := getProcessMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&m)), uintptr(m.cb)); r != 0 {
			u.rss += uint64(m.workingSetSize)
			u.private += uint64(m.pagefileUsage)
		}
		syscall.CloseHandle(h)
		u.threads += threads[id]
		u.procs++
		found = true
	}
	return u, found
}
