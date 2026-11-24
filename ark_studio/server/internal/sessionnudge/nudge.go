// Package sessionnudge reminds an agent that ARK launched (Run panel,
// session sidebar) of people's replies in ARK while the agent sits idle
// (T-0053, 2026-11-19).  An external agent watches the update feed itself
// (GET /api/agent/v1/updates, the watcher scripts); a session ARK started
// is woken from here: a running, idle session that takes follow-up input
// gets the people's updates since the last look as one short input turn,
// and a finished one-shot run (claude -p, codex exec) that can be resumed
// natively is continued with it.  These are people's replies, not idle
// reminders, so no project setting turns them off (T-0109): the session was
// told at launch (ARK_WAKE=ark) that ARK wakes it and starts no watcher.
//
// The package knows nothing about the server: the caller supplies the
// sessions, the update feed and the way to send input (Deps).
package sessionnudge

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"arkstudio/go/internal/compat/pyjson"
)

// Session is one ARK-launched session as the nudger sees it.
type Session struct {
	ID        string
	ProjectID string
	User      string // the account it acts for
	Idle      bool   // running and quiet for a while (between turns, even while its sub-agents or background tasks run)
	Accepts   bool   // takes follow-up input
	Stopped   bool   // finished recently and can be resumed with a new turn
	Watching  bool   // a watcher of its own is waiting on the update feed
}

// Batch is the people's updates after a cursor.
type Batch struct {
	Items []*pyjson.Object
	Epoch string
	Top   int64
}

// Deps connects the nudger to the server.
type Deps struct {
	Sessions func() []Session
	Top      func(ctx context.Context, projectID string) (epoch string, top int64, err error)
	Since    func(ctx context.Context, s Session, after int64) (Batch, error)
	Send     func(s Session, text string) error
	// ProjectName names the project in the message (optional).
	ProjectName func(projectID string) string
	// Load and Save keep a session's cursor across server restarts
	// (optional): updates held back before a restart are still delivered.
	Load func(sessionID string) (epoch string, after int64, ok bool)
	Save func(sessionID, epoch string, after int64)
	// Background is the session's own background work (background shell
	// commands, sub-agents) that finished while it rested and that the CLI
	// did not take up itself (optional).  ack marks them told; it is called
	// only after the message was sent.
	Background func(s Session) (notices []Notice, ack func())
}

// Notice is one finished piece of a session's background work.
type Notice struct {
	Line string // "Background task sh_1 \"Train\" finished (exit 0)"
	Tail string // the last lines of its output ("" when unknown)
}

// bgEvery spaces background notices of one session.
const bgEvery = time.Minute

type cursor struct {
	epoch string
	after int64
}

// Nudger keeps one feed cursor per session.
type Nudger struct {
	D      Deps
	mu     sync.Mutex
	cursor map[string]cursor
	lastBg map[string]time.Time
}

// Tick looks once at every session; it returns the sessions nudged.
func (n *Nudger) Tick(ctx context.Context) []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.cursor == nil {
		n.cursor = map[string]cursor{}
	}
	if n.lastBg == nil {
		n.lastBg = map[string]time.Time{}
	}
	var sent []string
	live := map[string]bool{}
	for _, s := range n.D.Sessions() {
		live[s.ID] = true
		if s.ProjectID == "" {
			continue
		}
		c, ok := n.cursor[s.ID]
		if !ok && n.D.Load != nil { // seen before a restart: go on from there
			if epoch, after, saved := n.D.Load(s.ID); saved {
				c, ok = cursor{epoch, after}, true
				n.cursor[s.ID] = c
			}
		}
		if !ok { // first sight: the feed starts now
			if epoch, top, err := n.D.Top(ctx, s.ProjectID); err == nil {
				n.set(s.ID, cursor{epoch, top})
			}
			continue
		}
		// Between turns a session takes input even while its sub-agents or
		// background tasks run (they can run for hours): people's answers
		// reach it then, as a person's message would.
		resting := (s.Idle && s.Accepts) || s.Stopped
		next, fetched := c, false
		var items []*pyjson.Object
		switch {
		case s.Watching: // its own watcher wakes it: one wake-up per update
			if epoch, top, err := n.D.Top(ctx, s.ProjectID); err == nil {
				n.set(s.ID, cursor{epoch, top})
			}
		case resting:
			b, err := n.D.Since(ctx, s, c.after)
			if err != nil {
				break
			}
			if b.Epoch != c.epoch { // history replaced: start again at now
				n.set(s.ID, cursor{b.Epoch, b.Top})
				break
			}
			items, next, fetched = b.Items, cursor{c.epoch, b.Top}, true
		}
		// Background work of the session that finished while it rests
		// (the CLI did not wake itself): one message, at most every bgEvery.
		var notices []Notice
		var ack func()
		if s.Idle && s.Accepts && n.D.Background != nil && time.Since(n.lastBg[s.ID]) >= bgEvery {
			notices, ack = n.D.Background(s)
		}
		if len(items) == 0 && len(notices) == 0 {
			if fetched {
				n.set(s.ID, next)
			}
			continue // a turn runs or nothing new: keep the cursor
		}
		name := ""
		if n.D.ProjectName != nil {
			name = n.D.ProjectName(s.ProjectID)
		}
		if err := n.D.Send(s, Compose(s, name, items, notices)); err != nil {
			continue // try again next tick
		}
		if fetched {
			n.set(s.ID, next)
		}
		if len(notices) > 0 {
			n.lastBg[s.ID] = time.Now()
			if ack != nil {
				ack()
			}
		}
		sent = append(sent, s.ID)
	}
	for id := range n.cursor {
		if !live[id] {
			delete(n.cursor, id)
		}
	}
	for id := range n.lastBg {
		if !live[id] {
			delete(n.lastBg, id)
		}
	}
	return sent
}

// set moves a session's cursor and saves it when it changed (caller holds n.mu).
func (n *Nudger) set(id string, c cursor) {
	if old, ok := n.cursor[id]; ok && old == c {
		return
	}
	n.cursor[id] = c
	if n.D.Save != nil {
		n.D.Save(id, c.epoch, c.after)
	}
}

// Run ticks until ctx ends.
func (n *Nudger) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n.Tick(ctx)
		}
	}
}

const (
	maxListed   = 5
	maxTailRows = 12
)

// Text is the input turn for people's updates (Compose without notices).
func Text(s Session, projectName string, items []*pyjson.Object) string {
	return Compose(s, projectName, items, nil)
}

// Compose is the input turn: what happened, then what to do.  It opens like
// the watcher's messages: "ARK UPDATE for <agent>, ARK project <name> (<id>)".
// Feed items of kind gpu_job_done (a GPU job the session queued ended) and
// background notices are listed apart from people's updates.
func Compose(s Session, projectName string, items []*pyjson.Object, notices []Notice) string {
	var people, jobs []*pyjson.Object
	for _, it := range items {
		if pyjson.Str(it.Value("kind")) == "gpu_job_done" {
			jobs = append(jobs, it)
		} else {
			people = append(people, it)
		}
	}
	var b strings.Builder
	project := s.ProjectID
	if projectName != "" {
		project = projectName + " (" + s.ProjectID + ")"
	}
	var parts []string
	if len(people) > 0 {
		parts = append(parts, fmt.Sprintf("%d new update(s) from people while you were idle", len(people)))
	}
	if len(jobs) > 0 {
		parts = append(parts, fmt.Sprintf("%d GPU job(s) you queued ended", len(jobs)))
	}
	if len(notices) > 0 {
		parts = append(parts, fmt.Sprintf("%d background task(s) of this session finished", len(notices)))
	}
	fmt.Fprintf(&b, "ARK UPDATE for ARK session %s, ARK project %s: %s:\n", s.ID, project, strings.Join(parts, "; "))
	for i, it := range people {
		if i == maxListed {
			fmt.Fprintf(&b, "- and %d more\n", len(people)-maxListed)
			break
		}
		fmt.Fprintf(&b, "- %s\n", pyjson.Str(it.Value("summary")))
	}
	for i, it := range jobs {
		if i == maxListed {
			fmt.Fprintf(&b, "- and %d more GPU job(s): GET /api/agent/v1/gpu/jobs\n", len(jobs)-maxListed)
			break
		}
		fmt.Fprintf(&b, "- %s\n", pyjson.Str(it.Value("summary")))
		writeTail(&b, strValue(it.Value("log_tail")))
		if link := strValue(it.Value("link")); link != "" {
			fmt.Fprintf(&b, "  full log: GET %s\n", link)
		}
	}
	for i, nt := range notices {
		if i == maxListed {
			fmt.Fprintf(&b, "- and %d more\n", len(notices)-maxListed)
			break
		}
		fmt.Fprintf(&b, "- %s\n", nt.Line)
		writeTail(&b, nt.Tail)
	}
	if len(people) > 0 {
		b.WriteString("Read GET /api/agent/v1/updates, act on them under the core rules (reply with reply_to, resolve, " +
			"record decisions, set review for what I must look at), then go on. If you already handled them, say so in one line. " +
			"If you are not working on ARK project " + project + " in this session, ignore this message.")
	}
	if len(jobs)+len(notices) > 0 {
		if len(people) > 0 {
			b.WriteString(" ")
		}
		b.WriteString("Check the result of the work that ended and go on with what waited for it. " +
			"ARK tells you when such work ends: do not poll for it.")
	}
	return b.String()
}

func strValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// writeTail adds the last lines of an output, indented.
func writeTail(b *strings.Builder, tail string) {
	tail = strings.TrimRight(strings.ReplaceAll(tail, "\r\n", "\n"), "\n ")
	if strings.TrimSpace(tail) == "" {
		return
	}
	lines := strings.Split(tail, "\n")
	if len(lines) > maxTailRows {
		lines = lines[len(lines)-maxTailRows:]
	}
	fmt.Fprintf(b, "  last %d line(s) of output:\n", len(lines))
	for _, l := range lines {
		if len(l) > 300 {
			l = l[:300] + "..."
		}
		fmt.Fprintf(b, "    %s\n", l)
	}
}
