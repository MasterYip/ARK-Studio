// Package sessionnudge reminds an agent that ARK launched (Run panel,
// session sidebar) of people's replies in ARK while the agent sits idle
// (T-0053, 2026-11-19).  An external agent watches the update feed itself
// (GET /api/agent/v1/updates, the watcher scripts); a session ARK started
// can also be woken from here when its project allows it: a running, idle
// session that takes follow-up input gets the people's updates since the
// last look as one short input turn, and a finished one-shot run (claude
// -p, codex exec) that can be resumed natively is continued with it.
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
	Enabled  func(projectID string) bool
	Top      func(ctx context.Context, projectID string) (epoch string, top int64, err error)
	Since    func(ctx context.Context, s Session, after int64) (Batch, error)
	Send     func(s Session, text string) error
	// ProjectName names the project in the message (optional).
	ProjectName func(projectID string) string
	// Load and Save keep a session's cursor across server restarts
	// (optional): updates held back before a restart are still delivered.
	Load func(sessionID string) (epoch string, after int64, ok bool)
	Save func(sessionID, epoch string, after int64)
}

type cursor struct {
	epoch string
	after int64
}

// Nudger keeps one feed cursor per session.
type Nudger struct {
	D      Deps
	mu     sync.Mutex
	cursor map[string]cursor
}

// Tick looks once at every session; it returns the sessions nudged.
func (n *Nudger) Tick(ctx context.Context) []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.cursor == nil {
		n.cursor = map[string]cursor{}
	}
	var sent []string
	live := map[string]bool{}
	for _, s := range n.D.Sessions() {
		live[s.ID] = true
		if s.ProjectID == "" || (n.D.Enabled != nil && !n.D.Enabled(s.ProjectID)) {
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
		if s.Watching { // its own watcher wakes it: one wake-up per update
			if epoch, top, err := n.D.Top(ctx, s.ProjectID); err == nil {
				n.set(s.ID, cursor{epoch, top})
			}
			continue
		}
		// Between turns a session takes input even while its sub-agents or
		// background tasks run (they can run for hours): people's answers
		// reach it then, as a person's message would.
		if !(s.Idle && s.Accepts) && !s.Stopped {
			continue // a turn runs: keep the cursor, nudge when it rests
		}
		b, err := n.D.Since(ctx, s, c.after)
		if err != nil {
			continue
		}
		if b.Epoch != c.epoch { // history replaced: start again at now
			n.set(s.ID, cursor{b.Epoch, b.Top})
			continue
		}
		if len(b.Items) == 0 {
			n.set(s.ID, cursor{c.epoch, b.Top})
			continue
		}
		name := ""
		if n.D.ProjectName != nil {
			name = n.D.ProjectName(s.ProjectID)
		}
		if err := n.D.Send(s, Text(s, name, b.Items)); err != nil {
			continue // try again next tick
		}
		n.set(s.ID, cursor{c.epoch, b.Top})
		sent = append(sent, s.ID)
	}
	for id := range n.cursor {
		if !live[id] {
			delete(n.cursor, id)
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

const maxListed = 5

// Text is the input turn: what happened, then what to do.  It opens like
// the watcher's messages: "ARK UPDATE for <agent>, ARK project <name> (<id>)".
func Text(s Session, projectName string, items []*pyjson.Object) string {
	var b strings.Builder
	project := s.ProjectID
	if projectName != "" {
		project = projectName + " (" + s.ProjectID + ")"
	}
	fmt.Fprintf(&b, "ARK UPDATE for ARK session %s, ARK project %s: %d new update(s) from people while you were idle:\n",
		s.ID, project, len(items))
	for i, it := range items {
		if i == maxListed {
			fmt.Fprintf(&b, "- and %d more\n", len(items)-maxListed)
			break
		}
		fmt.Fprintf(&b, "- %s\n", pyjson.Str(it.Value("summary")))
	}
	b.WriteString("Read GET /api/agent/v1/updates, act on them under the core rules (reply with reply_to, resolve, " +
		"record decisions, set review for what I must look at), then go on. If you already handled them, say so in one line. " +
		"If you are not working on ARK project " + project + " in this session, ignore this message.")
	return b.String()
}
