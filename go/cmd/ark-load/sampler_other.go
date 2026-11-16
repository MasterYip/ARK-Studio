//go:build !windows

package main

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// sampleTree reads /proc for pid and its children (Linux).
func sampleTree(pid int) (procUsage, bool) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return procUsage{}, false
	}
	parent := map[int]int{}
	for _, e := range entries {
		id, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if stat, err := os.ReadFile("/proc/" + e.Name() + "/stat"); err == nil {
			fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+2:]))
			if len(fields) > 1 {
				parent[id], _ = strconv.Atoi(fields[1])
			}
		}
	}
	tree := map[int]bool{pid: true}
	for changed := true; changed; {
		changed = false
		for c, p := range parent {
			if tree[p] && !tree[c] {
				tree[c], changed = true, true
			}
		}
	}
	var u procUsage
	tick := 100.0 // USER_HZ
	for id := range tree {
		stat, err := os.ReadFile("/proc/" + strconv.Itoa(id) + "/stat")
		if err != nil {
			continue
		}
		f := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+2:]))
		if len(f) < 22 {
			continue
		}
		ut, _ := strconv.ParseFloat(f[11], 64)
		st, _ := strconv.ParseFloat(f[12], 64)
		th, _ := strconv.Atoi(f[17])
		rss, _ := strconv.ParseUint(f[21], 10, 64)
		u.cpu += time.Duration((ut + st) / tick * float64(time.Second))
		u.rss += rss * uint64(os.Getpagesize())
		u.private += rss * uint64(os.Getpagesize())
		u.threads += th
		u.procs++
	}
	return u, u.procs > 0
}
