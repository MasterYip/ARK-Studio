package presence

import (
	"sort"
	"sync"
	"testing"
	"time"
)

// fakeClock drives Now and AfterFunc by hand.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []fakeTimer
}

type fakeTimer struct {
	at time.Time
	f  func()
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

func (c *fakeClock) AfterFunc(d time.Duration, f func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.timers = append(c.timers, fakeTimer{c.now.Add(d), f})
}

// advance moves the clock and runs the timers that are due.
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	sort.SliceStable(c.timers, func(i, j int) bool { return c.timers[i].at.Before(c.timers[j].at) })
	var due []func()
	keep := c.timers[:0]
	for _, t := range c.timers {
		if !t.at.After(c.now) {
			due = append(due, t.f)
		} else {
			keep = append(keep, t)
		}
	}
	c.timers = keep
	c.mu.Unlock()
	for _, f := range due {
		f()
	}
}

func testHub() (*Hub, *fakeClock) {
	c := &fakeClock{now: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)}
	h := New()
	h.Now, h.AfterFunc = c.Now, c.AfterFunc
	return h, c
}

func closed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestCursorMovesAndExpires(t *testing.T) {
	h, c := testHub()
	h.Record("p1", Touch{Actor: "agent:a", Label: "a", Unit: 1, Action: "read"})
	got, _ := h.Snapshot("p1")
	if len(got) != 1 || got[0].Unit != 1 || got[0].State != StateReading || got[0].Kind != KindCursor {
		t.Fatalf("after read: %+v", got)
	}
	c.advance(10 * time.Second)
	h.Record("p1", Touch{Actor: "agent:a", Label: "a", Unit: 2, UID: "T-0002", Action: "update"})
	got, _ = h.Snapshot("p1")
	if len(got) != 1 || got[0].Unit != 2 || got[0].State != StateWriting || got[0].UID != "T-0002" {
		t.Fatalf("after write elsewhere: %+v", got)
	}
	// 30 s after the last touch the cursor is gone.
	c.advance(29 * time.Second)
	if got, _ = h.Snapshot("p1"); len(got) != 1 {
		t.Fatalf("expired early: %+v", got)
	}
	c.advance(time.Second)
	if got, _ = h.Snapshot("p1"); len(got) != 0 {
		t.Fatalf("not expired: %+v", got)
	}
	// Other projects are separate.
	if got, _ = h.Snapshot("p2"); len(got) != 0 {
		t.Fatalf("p2: %+v", got)
	}
}

func TestWorkingEntryLastsAndFoldsTheCursor(t *testing.T) {
	h, c := testHub()
	if h.Announce("p1", Touch{Actor: "agent:a", Label: "a", Unit: 5}, "busy") {
		t.Fatal("unknown state accepted")
	}
	h.Announce("p1", Touch{Actor: "agent:a", Label: "a", Unit: 5, Note: "refactor"}, StateWorking)
	// A read elsewhere adds a cursor; a touch of the announced unit folds it.
	h.Record("p1", Touch{Actor: "agent:a", Label: "a", Unit: 6, Action: "read"})
	if got, _ := h.Snapshot("p1"); len(got) != 2 {
		t.Fatalf("want cursor + working: %+v", got)
	}
	h.Record("p1", Touch{Actor: "agent:a", Label: "a", Unit: 5, Action: "comment"})
	got, _ := h.Snapshot("p1")
	if len(got) != 1 || got[0].Kind != KindWorking || got[0].Action != "comment" || got[0].Note != "refactor" {
		t.Fatalf("folded: %+v", got)
	}
	// Refreshed by the comment: still there 4 min later, gone after 5 min.
	c.advance(4 * time.Minute)
	if got, _ = h.Snapshot("p1"); len(got) != 1 {
		t.Fatalf("working expired early: %+v", got)
	}
	c.advance(time.Minute)
	if got, _ = h.Snapshot("p1"); len(got) != 0 {
		t.Fatalf("working not expired: %+v", got)
	}
	// done ends it; the cursor stays briefly.
	h.Announce("p1", Touch{Actor: "agent:a", Label: "a", Unit: 7}, StateWorking)
	h.Announce("p1", Touch{Actor: "agent:a", Label: "a", Unit: 7}, "done")
	got, _ = h.Snapshot("p1")
	if len(got) != 1 || got[0].Kind != KindCursor || got[0].Action != "done" {
		t.Fatalf("after done: %+v", got)
	}
	c.advance(DefaultCursorTTL)
	if got, _ = h.Snapshot("p1"); len(got) != 0 {
		t.Fatalf("done cursor stayed: %+v", got)
	}
}

func TestThrottle(t *testing.T) {
	h, c := testHub()
	ch := h.Changed("p1")
	h.Record("p1", Touch{Actor: "agent:a", Unit: 1, Action: "read"})
	if !closed(ch) {
		t.Fatal("the first change is published at once")
	}
	// A burst within the interval: one publish at its end, with the last state.
	publishes := 0
	ch = h.Changed("p1")
	for i := 0; i < 50; i++ {
		h.Record("p1", Touch{Actor: "agent:a", Unit: int64(2 + i%3), Action: "update"})
		c.advance(2 * time.Millisecond)
		if closed(ch) {
			publishes++
			ch = h.Changed("p1")
		}
	}
	// 100 ms of a burst of 50 changes: at most one publish so far.
	if publishes > 1 {
		t.Fatalf("burst published %d times", publishes)
	}
	c.advance(DefaultInterval)
	if !closed(ch) && publishes == 0 {
		t.Fatal("the end of the burst was never published")
	}
	// Over one simulated second of continuous changes: at most 4 + 1 publishes.
	publishes = 0
	ch = h.Changed("p1")
	for i := 0; i < 200; i++ {
		h.Record("p1", Touch{Actor: "agent:b", Unit: int64(10 + i%7), Action: "update"})
		c.advance(5 * time.Millisecond)
		if closed(ch) {
			publishes++
			ch = h.Changed("p1")
		}
	}
	if publishes < 3 || publishes > 5 {
		t.Fatalf("%d publishes in 1 s, want about 4", publishes)
	}
	_, version := h.Snapshot("p1")
	c.advance(DefaultInterval)
	if _, after := h.Snapshot("p1"); after < version {
		t.Fatal("published version went back")
	}
}

func TestSweepPublishesExpiryAndForgets(t *testing.T) {
	h, c := testHub()
	h.Record("p1", Touch{Actor: "agent:a", Unit: 1, Action: "read"})
	c.advance(time.Second)
	ch := h.Changed("p1")
	c.advance(DefaultCursorTTL)
	h.Sweep("p1")
	if !closed(ch) {
		t.Fatal("expiry not published")
	}
	if got, _ := h.Snapshot("p1"); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	h.mu.Lock()
	_, kept := h.projects["p1"]
	h.mu.Unlock()
	if kept {
		t.Fatal("an empty project is kept")
	}
}

func TestLimitsAndIgnoredTouches(t *testing.T) {
	h, _ := testHub()
	h.Record("", Touch{Actor: "agent:a", Unit: 1, Action: "read"})
	h.Record("p1", Touch{Actor: "", Unit: 1, Action: "read"})
	h.Record("p1", Touch{Actor: "agent:a", Unit: 0, Action: "read"})
	if got, _ := h.Snapshot("p1"); len(got) != 0 {
		t.Fatalf("ignored touches recorded: %+v", got)
	}
	for i := 0; i < maxPerProject+10; i++ {
		h.Record("p1", Touch{Actor: "agent:" + string(rune('A'+i)), Unit: 1, Action: "read"})
	}
	if got, _ := h.Snapshot("p1"); len(got) != maxPerProject {
		t.Fatalf("%d entries, want %d", len(got), maxPerProject)
	}
	long := make([]rune, 500)
	for i := range long {
		long[i] = 'x'
	}
	h.Announce("p2", Touch{Actor: "agent:a", Label: string(long), Unit: 3, Note: string(long)}, StateWorking)
	got, _ := h.Snapshot("p2")
	if len([]rune(got[0].Note)) != maxNote || len([]rune(got[0].Label)) != maxLabel {
		t.Fatalf("not clipped: %d %d", len(got[0].Note), len(got[0].Label))
	}
}

func TestColorKeyDoesNotMergeActors(t *testing.T) {
	h, _ := testHub()
	h.Record("p1", Touch{Actor: "agent:alice/codex", Label: "Codex", ColorKey: "agent:alice", Unit: 1, Action: "read"})
	h.Record("p1", Touch{Actor: "agent:alice/claude", Label: "Claude", ColorKey: "agent:alice", Unit: 1, Action: "read"})
	got, _ := h.Snapshot("p1")
	if len(got) != 2 {
		t.Fatalf("color key merged distinct actors: %+v", got)
	}
	for _, entry := range got {
		if entry.ColorKey != "agent:alice" {
			t.Fatalf("missing shared color key: %+v", got)
		}
	}
}
