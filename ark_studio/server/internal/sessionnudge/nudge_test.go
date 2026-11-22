package sessionnudge

import (
	"context"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

type fake struct {
	sessions []Session
	enabled  bool
	epoch    string
	rows     []int64 // update ids with an item each
	sent     map[string]string
}

func (f *fake) deps() Deps {
	return Deps{
		Sessions: func() []Session { return f.sessions },
		Enabled:  func(string) bool { return f.enabled },
		Top: func(context.Context, string) (string, int64, error) {
			top := int64(0)
			for _, r := range f.rows {
				top = max(top, r)
			}
			return f.epoch, top, nil
		},
		Since: func(_ context.Context, _ Session, after int64) (Batch, error) {
			b := Batch{Epoch: f.epoch, Top: after}
			for _, r := range f.rows {
				if r > after {
					b.Items = append(b.Items, pyjson.NewObject("summary", "alice commented on T-0001"))
					b.Top = max(b.Top, r)
				}
			}
			return b, nil
		},
		Send: func(s Session, text string) error { f.sent[s.ID] = text; return nil },
	}
}

func TestNudgeOnlyIdleSessionsWithNewUpdates(t *testing.T) {
	f := &fake{enabled: true, epoch: "e1", rows: []int64{1, 2}, sent: map[string]string{}}
	f.sessions = []Session{{ID: "s1", ProjectID: "p", Idle: true, Accepts: true}}
	n := &Nudger{D: f.deps()}
	ctx := context.Background()
	if got := n.Tick(ctx); len(got) != 0 {
		t.Fatal("first sight starts at now")
	}
	f.rows = append(f.rows, 3)
	f.sessions[0].Idle = false
	if got := n.Tick(ctx); len(got) != 0 {
		t.Fatal("a busy session is not nudged")
	}
	f.sessions[0].Idle = true
	if got := n.Tick(ctx); len(got) != 1 || !strings.Contains(f.sent["s1"], "1 new update") || !strings.Contains(f.sent["s1"], "/api/agent/v1/updates") {
		t.Fatalf("nudge: %v %q", got, f.sent["s1"])
	}
	if got := n.Tick(ctx); len(got) != 0 {
		t.Fatal("no repeat without new updates")
	}
	f.enabled = false
	f.rows = append(f.rows, 4)
	if got := n.Tick(ctx); len(got) != 0 {
		t.Fatal("project switched off")
	}
	f.enabled = true
	f.epoch = "e2"
	if got := n.Tick(ctx); len(got) != 0 {
		t.Fatal("a new history epoch restarts at now")
	}
	// A finished, resumable session is continued with the nudge.
	f.sessions = []Session{{ID: "s2", ProjectID: "p", Stopped: true}}
	n.Tick(ctx)
	f.rows = append(f.rows, 9)
	if got := n.Tick(ctx); len(got) != 1 || got[0] != "s2" {
		t.Fatalf("stopped session: %v", got)
	}
	f.sessions = nil
	n.Tick(ctx)
	if len(n.cursor) != 0 {
		t.Fatal("cursors of ended sessions are dropped")
	}
}

func TestTextListsAtMostFive(t *testing.T) {
	var items []*pyjson.Object
	for i := 0; i < 7; i++ {
		items = append(items, pyjson.NewObject("summary", "x"))
	}
	if s := Text(Session{ID: "s1", ProjectID: "p"}, "", items); !strings.Contains(s, "and 2 more") || strings.Count(s, "- x") != 5 {
		t.Fatal(s)
	}
}

// One format for the watcher's messages and ARK's reminders (T-0053).
func TestTextFormat(t *testing.T) {
	s := Text(Session{ID: "ses_1", ProjectID: "prj_x"}, "BrainStorm", []*pyjson.Object{pyjson.NewObject("summary", "alice replied")})
	for _, want := range []string{"ARK UPDATE for ARK session ses_1, ARK project BrainStorm (prj_x): 1 new update(s)",
		"- alice replied", "If you are not working on ARK project BrainStorm (prj_x) in this session, ignore this message."} {
		if !strings.Contains(s, want) {
			t.Fatalf("lacks %q:\n%s", want, s)
		}
	}
}

// A session whose own watcher waits on the feed is not reminded as well;
// its cursor follows the feed, so it is not reminded of those updates later.
func TestNudgeSkipsSessionsWithTheirOwnWatcher(t *testing.T) {
	f := &fake{enabled: true, epoch: "e1", rows: []int64{1}, sent: map[string]string{}}
	f.sessions = []Session{{ID: "s1", ProjectID: "p", Idle: true, Accepts: true, Watching: true}}
	n := &Nudger{D: f.deps()}
	ctx := context.Background()
	n.Tick(ctx)
	f.rows = append(f.rows, 2)
	if got := n.Tick(ctx); len(got) != 0 {
		t.Fatalf("nudged a session with its own watcher: %v", got)
	}
	f.sessions[0].Watching = false
	if got := n.Tick(ctx); len(got) != 0 {
		t.Fatal("reminded of updates its watcher already delivered")
	}
	f.rows = append(f.rows, 3)
	if got := n.Tick(ctx); len(got) != 1 {
		t.Fatalf("after the watcher stopped: %v", got)
	}
}

// Between turns with sub-agents or background tasks running, a session is
// busy: no nudge.  Once they finish, the pending updates are delivered.
func TestNudgeWaitsForBackgroundWork(t *testing.T) {
	f := &fake{enabled: true, epoch: "e1", rows: []int64{1}, sent: map[string]string{}}
	f.sessions = []Session{{ID: "s1", ProjectID: "p", Idle: true, Accepts: true, Busy: true}}
	n := &Nudger{D: f.deps()}
	ctx := context.Background()
	n.Tick(ctx)
	f.rows = append(f.rows, 2, 3)
	if got := n.Tick(ctx); len(got) != 0 {
		t.Fatal("a session whose background work runs is not nudged")
	}
	f.sessions[0].Busy = false
	if got := n.Tick(ctx); len(got) != 1 || !strings.Contains(f.sent["s1"], "2 new update") {
		t.Fatalf("nudge after the background work ended: %v %q", got, f.sent["s1"])
	}
}
