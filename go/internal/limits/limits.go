// Package limits holds the bounded pools that keep one ARK server from
// exhausting its host under hundreds of concurrent users.
//
//   - Pool: a counting semaphore whose Acquire honors the request context, so
//     a queued request that times out or disconnects leaves the queue instead
//     of holding a goroutine forever.
//   - Run: start a child process (git, an agent CLI, ssh) only while holding
//     a Pool slot, with a deadline, killing the whole process tree on cancel.
package limits

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"sync/atomic"
	"time"
)

// Pool is a context-aware counting semaphore.
type Pool struct {
	slots   chan struct{}
	waiting atomic.Int64
}

// NewPool returns a pool with n slots (n >= 1).
func NewPool(n int) *Pool { return &Pool{slots: make(chan struct{}, max(1, n))} }

// Acquire waits for a slot or for ctx to end.
func (p *Pool) Acquire(ctx context.Context) error {
	select {
	case p.slots <- struct{}{}:
		return nil
	default:
	}
	p.waiting.Add(1)
	defer p.waiting.Add(-1)
	select {
	case p.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release frees a slot taken by Acquire.
func (p *Pool) Release() { <-p.slots }

// InUse is the number of held slots.
func (p *Pool) InUse() int { return len(p.slots) }

// Waiting is the number of callers queued in Acquire.
func (p *Pool) Waiting() int64 { return p.waiting.Load() }

// Cap is the pool size.
func (p *Pool) Cap() int { return cap(p.slots) }

// ErrOutputLimit is returned when a child writes more than the limit.
var ErrOutputLimit = errors.New("limits: child output exceeds the limit")

type capped struct {
	buf   bytes.Buffer
	limit int
	over  bool
}

func (c *capped) Write(p []byte) (int, error) {
	if c.buf.Len()+len(p) > c.limit {
		c.over = true
		return 0, ErrOutputLimit
	}
	return c.buf.Write(p)
}

// Result of Run.
type Result struct {
	Stdout, Stderr []byte
	ExitCode       int
}

// Run executes name args under a pool slot with a timeout (Python's
// subprocess.run(timeout=...)), stdin data, and capped output.  The child
// gets its own process group / job so cancellation kills its descendants.
func (p *Pool) Run(ctx context.Context, timeout time.Duration, stdin []byte, maxOutput int, env []string, name string, args ...string) (Result, error) {
	return p.RunSpec(ctx, Spec{Timeout: timeout, Stdin: stdin, MaxOutput: maxOutput, Env: env, Name: name, Args: args})
}

// Spec describes one child for RunSpec.
type Spec struct {
	Timeout   time.Duration
	Stdin     []byte
	MaxOutput int
	Env       []string // nil inherits the server environment
	Dir       string
	Name      string
	Args      []string
	// CmdLine (Windows only) is the exact command line passed to
	// CreateProcess, for callers that reproduce subprocess.list2cmdline.
	CmdLine string
}

// RunSpec is Run with a working directory and an optional raw command line.
func (p *Pool) RunSpec(ctx context.Context, s Spec) (Result, error) {
	if err := p.Acquire(ctx); err != nil {
		return Result{}, err
	}
	defer p.Release()
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.Name, s.Args...)
	cmd.Env = s.Env
	cmd.Dir = s.Dir
	if s.Stdin != nil {
		cmd.Stdin = bytes.NewReader(s.Stdin)
	}
	maxOutput := s.MaxOutput
	out, errOut := &capped{limit: maxOutput}, &capped{limit: maxOutput}
	cmd.Stdout, cmd.Stderr = out, errOut
	isolate(cmd)
	setCmdLine(cmd, s.CmdLine)
	cmd.Cancel = func() error { return killTree(cmd) }
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	res := Result{Stdout: out.buf.Bytes(), Stderr: errOut.buf.Bytes(), ExitCode: cmd.ProcessState.ExitCode()}
	if out.over || errOut.over {
		return res, ErrOutputLimit
	}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return res, nil // non-zero exit is reported through ExitCode
	}
	return res, err
}
