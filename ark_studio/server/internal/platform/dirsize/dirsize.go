// Package dirsize measures how much a registered resource folder (or a
// single file, such as a SQLite database) holds on this host: bytes, files,
// and for a git checkout the share of its top-level .git folder.
//
// The walk is read-only and bounded: a time cap and an entry budget end it
// early (the result is then a lower bound, Capped), symbolic links are
// counted but never followed, other file systems mounted inside the tree
// are not entered, unreadable folders are skipped and counted.  Sizes are
// apparent sizes (the files' lengths).  Measure runs on the caller's
// goroutine; Background runs it on its own OS thread at idle IO priority
// where the OS allows it.
package dirsize

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Defaults for one measured path.
const (
	DefaultMaxDuration = 20 * time.Second
	DefaultMaxEntries  = 2_000_000
)

// Kinds of result.
const (
	KindDir     = "dir"
	KindFile    = "file"
	KindMissing = "missing" // the path does not exist on this host
	KindNetwork = "network" // a network mount: not measured unless asked
	KindError   = "error"   // the root could not be read
)

// Reasons a walk stopped early.
const (
	CappedTime    = "time"
	CappedEntries = "entries"
)

// Options bound one walk.  Zero values take the defaults.
type Options struct {
	MaxDuration time.Duration
	MaxEntries  int64
	// MeasureNetwork measures a path on a network mount too (with the
	// same caps); by default such a path is reported as KindNetwork.
	MeasureNetwork bool
	// Pause is slept every PauseEvery entries so a long walk leaves the
	// disk to others (0 = no pause).
	Pause      time.Duration
	PauseEvery int64
}

// Result is one measured path.
type Result struct {
	Path     string        `json:"path"`
	Kind     string        `json:"kind"`
	Bytes    int64         `json:"bytes"`
	Files    int64         `json:"files"`
	Dirs     int64         `json:"dirs"`
	GitBytes int64         `json:"git_bytes"` // inside the top-level .git folder
	GitFiles int64         `json:"git_files"`
	Git      bool          `json:"git"` // the root has a .git folder
	Capped   string        `json:"capped,omitempty"`
	Skipped  int64         `json:"skipped"`  // entries that could not be read
	Symlinks int64         `json:"symlinks"` // links counted, never followed
	Mounts   int64         `json:"mounts"`   // other file systems not entered
	Error    string        `json:"error,omitempty"`
	Took     time.Duration `json:"-"`
}

type frame struct {
	path string
	git  bool
}

// Measure walks root (made absolute; a link at root itself is resolved
// once) within the options' caps.  ctx cancels it like the time cap.
func Measure(ctx context.Context, root string, opt Options) Result {
	start := time.Now()
	if opt.MaxDuration <= 0 {
		opt.MaxDuration = DefaultMaxDuration
	}
	if opt.MaxEntries <= 0 {
		opt.MaxEntries = DefaultMaxEntries
	}
	res := Result{Path: root}
	defer func() { res.Took = time.Since(start) }()
	if !filepath.IsAbs(root) {
		res.Kind, res.Error = KindError, "not an absolute path"
		return res
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			res.Kind = KindMissing
		} else {
			res.Kind, res.Error = KindError, errText(err)
		}
		return res
	}
	info, err := os.Lstat(real)
	if err != nil {
		res.Kind, res.Error = KindError, errText(err)
		return res
	}
	if !opt.MeasureNetwork && IsNetworkPath(real) {
		res.Kind = KindNetwork
		return res
	}
	if !info.IsDir() {
		res.Kind = KindFile
		if info.Mode().IsRegular() {
			res.Bytes, res.Files = info.Size(), 1
		}
		return res
	}
	res.Kind = KindDir
	ctx, cancel := context.WithTimeout(ctx, opt.MaxDuration)
	defer cancel()
	rootDev, haveDev := device(info)
	if gi, err := os.Lstat(filepath.Join(real, ".git")); err == nil && gi.IsDir() {
		res.Git = true
	}
	stack := []frame{{path: real}}
	var entries int64
	for len(stack) > 0 {
		top := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		res.Dirs++
		f, err := os.Open(top.path)
		if err != nil {
			res.Skipped++
			continue
		}
		for {
			batch, err := f.ReadDir(256)
			for _, e := range batch {
				entries++
				if entries%256 == 0 && ctx.Err() != nil {
					res.Capped = CappedTime
					f.Close()
					return res
				}
				if entries > opt.MaxEntries {
					res.Capped = CappedEntries
					f.Close()
					return res
				}
				if opt.Pause > 0 && opt.PauseEvery > 0 && entries%opt.PauseEvery == 0 {
					time.Sleep(opt.Pause)
				}
				child := filepath.Join(top.path, e.Name())
				git := top.git || (top.path == real && e.Name() == ".git")
				t := e.Type()
				switch {
				case t&fs.ModeSymlink != 0:
					res.Symlinks++
				case t.IsDir():
					ci, err := e.Info()
					if err != nil {
						res.Skipped++
						continue
					}
					if d, ok := device(ci); haveDev && ok && d != rootDev {
						res.Mounts++
						continue
					}
					stack = append(stack, frame{path: child, git: git})
				case t.IsRegular():
					ci, err := e.Info()
					if err != nil {
						res.Skipped++
						continue
					}
					res.Files++
					res.Bytes += ci.Size()
					if git {
						res.GitFiles++
						res.GitBytes += ci.Size()
					}
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					res.Skipped++
				}
				break
			}
		}
		f.Close()
	}
	return res
}

// Background is Measure on a fresh OS thread at the lowest IO and CPU
// priority this OS allows; the thread is not reused afterwards.
func Background(ctx context.Context, root string, opt Options) Result {
	out := make(chan Result, 1)
	go func() {
		lowerThreadPriority() // locks the goroutine to its thread for good
		out <- Measure(ctx, root, opt)
	}()
	return <-out
}

func errText(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	if errors.Is(err, fs.ErrPermission) {
		return "permission denied"
	}
	return err.Error()
}
