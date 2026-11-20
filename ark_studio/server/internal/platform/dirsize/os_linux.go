package dirsize

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func device(fi os.FileInfo) (uint64, bool) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev), true
	}
	return 0, false
}

// lowerThreadPriority: idle IO class and nice 19 for this thread only.  The
// goroutine stays locked, so Go discards the thread when it ends.
func lowerThreadPriority() {
	runtime.LockOSThread()
	const whoProcess, classIdle, classShift = 1, 3, 13
	_, _, _ = unix.Syscall(unix.SYS_IOPRIO_SET, whoProcess, uintptr(unix.Gettid()), classIdle<<classShift)
	_ = unix.Setpriority(unix.PRIO_PROCESS, unix.Gettid(), 19)
}

var networkFS = map[string]bool{"nfs": true, "nfs4": true, "cifs": true, "smb3": true, "smbfs": true,
	"fuse.sshfs": true, "sshfs": true, "9p": true, "afs": true, "ceph": true, "glusterfs": true,
	"fuse.glusterfs": true, "lustre": true, "davfs": true, "fuse.davfs2": true, "fuse.rclone": true,
	"fuse.gcsfuse": true, "fuse.s3fs": true, "fuse.cephfs": true, "beegfs": true, "gpfs": true}

// IsNetworkPath: the longest mount point of /proc/self/mountinfo that
// contains path has a network file system type.
func IsNetworkPath(path string) bool {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	defer f.Close()
	path = filepath.Clean(path)
	best, bestType := "", ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		// id parent major:minor root mountpoint options [optional...] - fstype source superopts
		left, right, ok := strings.Cut(sc.Text(), " - ")
		if !ok {
			continue
		}
		lf, rf := strings.Fields(left), strings.Fields(right)
		if len(lf) < 5 || len(rf) < 1 {
			continue
		}
		mp := unescapeMount(lf[4])
		if mp != "/" && path != mp && !strings.HasPrefix(path, mp+"/") {
			continue
		}
		if len(mp) >= len(best) {
			best, bestType = mp, rf[0]
		}
	}
	return networkFS[bestType]
}

func unescapeMount(s string) string {
	r := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return r.Replace(s)
}
