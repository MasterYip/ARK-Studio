package gpu

// storage.go (Go-only, tests/python/contract/go_only.py AUGMENTED
// "/api/gpu/probe"): the GPU server's storage usage.  SystemCommand ends with
// a read-only `df -PB1` section (__ARK_DF__); ParseStorage keeps the real
// filesystems and ProbeServer adds them to the probe result as
//
//	"storage": [{"mount", "used_bytes", "total_bytes", "free_bytes", "percent", "data"}]
//
// "data" marks the mount holding a configured data path (queue.path_map
// "to" for this server).  A missing df, an empty section or output that does
// not parse just leaves "storage" out; it never fails the probe.

import (
	"sort"
	"strconv"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
)

const (
	storageMarker   = "__ARK_DF__"
	maxStorageItems = 4
)

// StorageCommand is the df section appended to SystemCommand: POSIX output
// in bytes, pseudo filesystems excluded where df supports -x (BusyBox and
// older df fall back to the plain call; ParseStorage filters again).
const StorageCommand = "echo " + storageMarker + "; df -PB1 -x tmpfs -x devtmpfs -x overlay -x squashfs 2>/dev/null " +
	"|| df -PB1 2>/dev/null || true"

// Mount is one parsed df row.
type Mount struct {
	Source string
	Mount  string
	Total  int64
	Used   int64
	Free   int64
}

// Percent is df's capacity (used / (used + available)) with one decimal.
func (m Mount) Percent() float64 {
	base := m.Used + m.Free
	if base <= 0 {
		base = m.Total
	}
	if base <= 0 {
		return 0
	}
	return round1(pyMin(100, 100*float64(m.Used)/float64(base)))
}

var pseudoFS = map[string]bool{
	"tmpfs": true, "devtmpfs": true, "overlay": true, "squashfs": true, "none": true, "udev": true,
	"shm": true, "proc": true, "sysfs": true, "cgroup": true, "cgroup2": true, "efivarfs": true,
	"devpts": true, "mqueue": true, "hugetlbfs": true, "debugfs": true, "tracefs": true,
	"securityfs": true, "pstore": true, "bpf": true, "configfs": true, "fusectl": true,
	"nsfs": true, "ramfs": true, "rootfs": true, "autofs": true, "binfmt_misc": true,
}

var pseudoMountPrefixes = []string{"/proc", "/sys", "/dev", "/run", "/snap", "/boot", "/var/lib/docker",
	"/var/lib/containerd", "/var/lib/kubelet", "/var/snap"}

func pseudoMount(source, mount string) bool {
	if pseudoFS[pytext.Lower(source)] || strings.HasPrefix(source, "/dev/loop") {
		return true
	}
	if !strings.HasPrefix(mount, "/") {
		return true
	}
	for _, prefix := range pseudoMountPrefixes {
		if mount == prefix || strings.HasPrefix(mount, prefix+"/") {
			return true
		}
	}
	return false
}

// ParseDF parses `df -P -B1` output into the real, deduplicated mounts (in
// output order).  Header, malformed and pseudo rows are skipped; a device
// mounted twice (bind mounts) keeps its shortest mount point.
func ParseDF(text string) []Mount {
	var out []Mount
	bySource := map[string]int{}
	byMount := map[string]bool{}
	for _, line := range pytext.SplitLines(text, false) {
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[0] == "Filesystem" {
			continue
		}
		nums := [3]int64{}
		ok := true
		for i := 0; i < 3; i++ {
			n, err := strconv.ParseInt(fields[1+i], 10, 64)
			if err != nil || n < 0 {
				ok = false
				break
			}
			nums[i] = n
		}
		if !ok || !strings.HasSuffix(fields[4], "%") {
			continue
		}
		m := Mount{Source: fields[0], Mount: strings.Join(fields[5:], " "), Total: nums[0], Used: nums[1], Free: nums[2]}
		if m.Total <= 0 || pseudoMount(m.Source, m.Mount) || byMount[m.Mount] {
			continue
		}
		if i, seen := bySource[m.Source]; seen && strings.HasPrefix(m.Source, "/") {
			if len(m.Mount) < len(out[i].Mount) {
				delete(byMount, out[i].Mount)
				byMount[m.Mount] = true
				out[i] = m
			}
			continue
		}
		bySource[m.Source] = len(out)
		byMount[m.Mount] = true
		out = append(out, m)
	}
	return out
}

func mountContains(mount, path string) bool {
	return mount == "/" || path == mount || strings.HasPrefix(path, mount+"/")
}

// SelectStorage picks what the panel shows: the mount holding each data path
// first (longest matching mount point), then the largest others, at most
// maxStorageItems.
func SelectStorage(mounts []Mount, dataPaths []string) []any {
	if len(mounts) == 0 {
		return nil
	}
	data := map[int]bool{}
	var order []int
	for _, p := range dataPaths {
		best := -1
		for i, m := range mounts {
			if mountContains(m.Mount, p) && (best < 0 || len(m.Mount) > len(mounts[best].Mount)) {
				best = i
			}
		}
		if best >= 0 && !data[best] {
			data[best] = true
			order = append(order, best)
		}
	}
	rest := make([]int, 0, len(mounts))
	for i := range mounts {
		if !data[i] {
			rest = append(rest, i)
		}
	}
	sort.SliceStable(rest, func(a, b int) bool { return mounts[rest[a]].Total > mounts[rest[b]].Total })
	order = append(order, rest...)
	if len(order) > maxStorageItems {
		order = order[:maxStorageItems]
	}
	out := make([]any, 0, len(order))
	for _, i := range order {
		m := mounts[i]
		out = append(out, pyjson.NewObject("mount", m.Mount, "used_bytes", m.Used, "total_bytes", m.Total,
			"free_bytes", m.Free, "percent", m.Percent(), "data", data[i]))
	}
	return out
}

// ParseStorage reads the __ARK_DF__ section of the system query output (nil
// when it is missing or holds no real filesystem).
func ParseStorage(text string, dataPaths []string) []any {
	i := strings.Index(text, storageMarker+"\n")
	if i < 0 {
		return nil
	}
	section := text[i+len(storageMarker)+1:]
	if j := strings.Index(section, "\n__ARK_"); j >= 0 {
		section = section[:j]
	}
	return SelectStorage(ParseDF(section), dataPaths)
}

// DataPaths are the remote data folders configured for a server: the "to"
// side of queue.path_map entries that apply to it (no servers list = all).
func DataPaths(cfg *pyjson.Object, server string) []string {
	q := queueConfig(cfg)
	if q == nil {
		return nil
	}
	var out []string
	for _, m := range pathMaps(q) {
		applies := len(m.servers) == 0
		for _, s := range m.servers {
			applies = applies || s == server
		}
		if applies && m.to != "" {
			out = append(out, m.to)
		}
	}
	return out
}
