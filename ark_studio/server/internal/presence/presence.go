// Package presence tracks which agent is on which node of a project, live
// and in memory only (T-0043, Go only).  Nothing is persisted: a restart
// forgets every entry.
//
// Each agent (an agent token, a personal agent token or a task agent) has at
// most two entries per project:
//
//   - a cursor: the unit it touched last (a read shows as "reading", a write
//     as "writing").  It moves with every touch and expires CursorTTL after
//     the last one.
//   - a working entry: the unit it announced with POST
//     /api/agent/v1/presence {"state": "working"}.  It stays WorkingTTL unless
//     refreshed (another announcement, or any touch of the same unit) and
//     ends with {"state": "done"}.
//
// A cursor on the unit of the agent's working entry is folded into the
// working entry, so one agent never shows twice on one node.
//
// Watchers (the event stream) wait on Changed; changes are published at most
// once per Interval per project, the last state of a burst always included.
package presence

import (
	"sort"
	"sync"
	"time"
)

// Defaults (the Hub fields can be changed before use, e.g. in tests).
const (
	DefaultCursorTTL  = 30 * time.Second
	DefaultWorkingTTL = 5 * time.Minute
	DefaultInterval   = 250 * time.Millisecond // at most 4 broadcasts / s / project
	maxPerProject     = 64                     // agents tracked per project
	maxNote           = 160
	maxLabel          = 40
)

// Kinds and states of an entry.
const (
	KindCursor  = "cursor"
	KindWorking = "working"

	StateReading = "reading"
	StateWriting = "writing"
	StateWorking = "working"
)

// Touch is one thing an agent did to a unit.
type Touch struct {
	Actor    string // stable identity, e.g. "agent:ana/claude" or "task:s_123"
	Label    string // short display name, e.g. "claude"
	ColorKey string // shared colour identity, while Actor remains unique
	Unit     int64  // the unit's row id (graph node id)
	UID      string // the unit's uid (T-0043), when known
	Title    string // the unit's title, when known
	Action   string // read, create, update, status, comment, media, link, history, working
	Note     string // explicit announcements only
}

// Entry is one agent on one unit.
type Entry struct {
	Actor    string    `json:"actor"`
	Label    string    `json:"label"`
	ColorKey string    `json:"color_key,omitempty"`
	Kind     string    `json:"kind"`
	State    string    `json:"state"`
	Unit     int64     `json:"unit"`
	UID      string    `json:"uid,omitempty"`
	Title    string    `json:"title,omitempty"`
	Action   string    `json:"action"`
	Note     string    `json:"note,omitempty"`
	At       time.Time `json:"at"`
	Expires  time.Time `json:"expires_at"`
}

type key struct{ actor, kind string }

type project struct {
	entries     map[key]*Entry
	version     uint64
	changed     chan struct{} // closed at the next publish
	lastPublish time.Time
	pending     bool // a publish is scheduled
	published   uint64
}

// Hub holds the presence of every project.  The zero value is not usable;
// use New.
type Hub struct {
	CursorTTL  time.Duration
	WorkingTTL time.Duration
	Interval   time.Duration
	// Now and AfterFunc are the clock (replaced in tests).
	Now       func() time.Time
	AfterFunc func(d time.Duration, f func())

	mu       sync.Mutex
	seq      uint64 // versions are unique across projects (a forgotten project restarts above it)
	projects map[string]*project
}

// New is a Hub with the default lifetimes and throttle.
func New() *Hub {
	return &Hub{CursorTTL: DefaultCursorTTL, WorkingTTL: DefaultWorkingTTL, Interval: DefaultInterval,
		Now: time.Now, AfterFunc: func(d time.Duration, f func()) { time.AfterFunc(d, f) },
		projects: map[string]*project{}}
}

func (h *Hub) proj(id string) *project {
	p := h.projects[id]
	if p == nil {
		p = &project{entries: map[key]*Entry{}, changed: make(chan struct{})}
		h.projects[id] = p
	}
	return p
}

// IsWrite reports whether an action changes the unit (shown as "writing").
func IsWrite(action string) bool { return action != "read" }

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// Record notes an implicit touch (a read or a write) in a project.
func (h *Hub) Record(projectID string, t Touch) {
	if projectID == "" || t.Actor == "" || t.Unit == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.Now()
	p := h.proj(projectID)
	h.sweep(p, now)
	state := StateReading
	if IsWrite(t.Action) {
		state = StateWriting
	}
	if w := p.entries[key{t.Actor, KindWorking}]; w != nil && w.Unit == t.Unit {
		// Activity on the announced unit refreshes the working entry; the
		// cursor joins it.
		w.Action, w.At, w.Expires = t.Action, now, now.Add(h.WorkingTTL)
		if t.Label != "" {
			w.Label = clip(t.Label, maxLabel)
		}
		if t.ColorKey != "" {
			w.ColorKey = clip(t.ColorKey, maxLabel)
		}
		fillUnit(w, t)
		delete(p.entries, key{t.Actor, KindCursor})
		h.changedLocked(projectID, p, now)
		return
	}
	k := key{t.Actor, KindCursor}
	e := p.entries[k]
	if e == nil {
		if h.full(p, t.Actor) {
			return
		}
		e = &Entry{Actor: t.Actor, Kind: KindCursor}
		p.entries[k] = e
	} else if e.Unit != t.Unit {
		e.UID, e.Title = "", ""
	}
	e.Label = clip(t.Label, maxLabel)
	e.ColorKey = clip(t.ColorKey, maxLabel)
	e.Unit, e.State, e.Action, e.At, e.Expires = t.Unit, state, t.Action, now, now.Add(h.CursorTTL)
	fillUnit(e, t)
	h.changedLocked(projectID, p, now)
}

func fillUnit(e *Entry, t Touch) {
	if t.UID != "" {
		e.UID = t.UID
	}
	if t.Title != "" {
		e.Title = clip(t.Title, 120)
	}
}

// full reports whether the project already tracks maxPerProject agents and
// actor is not one of them.
func (h *Hub) full(p *project, actor string) bool {
	actors := map[string]bool{}
	for k := range p.entries {
		actors[k.actor] = true
	}
	return !actors[actor] && len(actors) >= maxPerProject
}

// Announce sets (state "working") or ends (state "done") an agent's working
// entry.  It reports whether the state was valid.
func (h *Hub) Announce(projectID string, t Touch, state string) bool {
	if state != StateWorking && state != "done" {
		return false
	}
	if projectID == "" || t.Actor == "" {
		return true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.Now()
	p := h.proj(projectID)
	h.sweep(p, now)
	wk, ck := key{t.Actor, KindWorking}, key{t.Actor, KindCursor}
	if state == "done" {
		w := p.entries[wk]
		if w == nil || (t.Unit != 0 && w.Unit != t.Unit) {
			return true
		}
		delete(p.entries, wk)
		// The agent is still around: its cursor stays on the unit briefly.
		p.entries[ck] = &Entry{Actor: t.Actor, Label: w.Label, ColorKey: w.ColorKey, Kind: KindCursor, State: StateWriting, Unit: w.Unit,
			UID: w.UID, Title: w.Title, Action: "done", At: now, Expires: now.Add(h.CursorTTL)}
		h.changedLocked(projectID, p, now)
		return true
	}
	if t.Unit == 0 {
		return true
	}
	w := p.entries[wk]
	if w == nil {
		if h.full(p, t.Actor) {
			return true
		}
		w = &Entry{Actor: t.Actor, Kind: KindWorking, State: StateWorking}
		p.entries[wk] = w
	} else if w.Unit != t.Unit {
		w.UID, w.Title = "", ""
	}
	w.Label = clip(t.Label, maxLabel)
	w.ColorKey = clip(t.ColorKey, maxLabel)
	w.Unit, w.Action, w.Note, w.At, w.Expires = t.Unit, "working", clip(t.Note, maxNote), now, now.Add(h.WorkingTTL)
	fillUnit(w, t)
	if c := p.entries[ck]; c != nil && c.Unit == t.Unit {
		delete(p.entries, ck)
	}
	h.changedLocked(projectID, p, now)
	return true
}

// sweep drops expired entries; it reports whether any was dropped.
func (h *Hub) sweep(p *project, now time.Time) bool {
	dropped := false
	for k, e := range p.entries {
		if !now.Before(e.Expires) {
			delete(p.entries, k)
			dropped = true
		}
	}
	return dropped
}

// Sweep drops a project's expired entries and publishes the change.
func (h *Hub) Sweep(projectID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.projects[projectID]
	if p == nil {
		return
	}
	now := h.Now()
	if h.sweep(p, now) {
		h.changedLocked(projectID, p, now)
	}
	if len(p.entries) == 0 && !p.pending && p.published == p.version {
		// Nothing left to show or to announce: forget the project.  A
		// watcher holding the old channel is woken so it re-subscribes.
		close(p.changed)
		delete(h.projects, projectID)
	}
}

// changedLocked bumps the version and publishes now or at the end of the
// project's throttle interval.
func (h *Hub) changedLocked(projectID string, p *project, now time.Time) {
	h.seq++
	p.version = h.seq
	if p.pending {
		return
	}
	wait := h.Interval - now.Sub(p.lastPublish)
	if wait <= 0 {
		h.publishLocked(p, now)
		return
	}
	p.pending = true
	h.AfterFunc(wait, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if cur := h.projects[projectID]; cur == p {
			p.pending = false
			h.publishLocked(p, h.Now())
		}
	})
}

func (h *Hub) publishLocked(p *project, now time.Time) {
	p.lastPublish = now
	p.published = p.version
	close(p.changed)
	p.changed = make(chan struct{})
}

// Snapshot is a project's live entries (sorted by actor, then kind) and the
// version they were published under (0: nothing was ever recorded).
func (h *Hub) Snapshot(projectID string) ([]Entry, uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.projects[projectID]
	if p == nil {
		return []Entry{}, 0
	}
	now := h.Now()
	if h.sweep(p, now) {
		h.changedLocked(projectID, p, now)
	}
	out := make([]Entry, 0, len(p.entries))
	for _, e := range p.entries {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Actor != out[j].Actor {
			return out[i].Actor < out[j].Actor
		}
		return out[i].Kind < out[j].Kind
	})
	return out, p.published
}

// Changed is closed at the next publish of the project's presence.
func (h *Hub) Changed(projectID string) <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.proj(projectID).changed
}

// Published is the version of the project's last publish (0 when none).
func (h *Hub) Published(projectID string) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if p := h.projects[projectID]; p != nil {
		return p.published
	}
	return 0
}
