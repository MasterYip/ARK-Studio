package server

// Go only: GET /api/projects (and GET /api/desktop/projects) name, per
// project, who created it and who made its latest change, for the project
// picker cards.  tests/python/contract/go_only.py AUGMENTED "/api/projects"
// strips these members before the replay compares bodies.
//
//   creator        {"user", "name"}: the account that created the project
//                  (org.ProjectCreators, see internal/org/people.go for why
//                  created_by alone is not enough); absent when unknown
//   created_by_me  true when that account is the viewer (same account id)
//   last_actor     {"user", "name", "agent", "at"}: who made the change that
//                  set "updated".  user/name are the account (for an agent,
//                  the account its token belongs to); agent is the agent's
//                  label as presence shows it ("MasterYip/视频字体") or null
//                  for a person.  Absent when the latest change is not known
//                  (made outside this server, or before it recorded writers).
//
// A person is named only when the viewer may already see them in that
// project: the viewer themself or a member of the project (direct or team
// grant).  Otherwise the member is left out.
//
// Writers are noted from every activity row a request's writes log (the
// store's activity observer, routes_presence.go) and on project creation,
// kept in memory and written to the org database in the background (at most
// one batch every lastWriterFlush).  Listing costs three org queries
// (projects, users, stored writers) and, for people other than the viewer,
// one indexed membership lookup per (person, project), whatever the number
// of activity rows.

import (
	"context"
	"strings"
	"sync"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/platform/clock"
)

const (
	lastWriterFlush = 2 * time.Second
	// A stored writer names the "updated" change when its time is at most
	// lastWriterLead before it (the manifest is touched after the activity
	// row) and not later than lastWriterLag after it.
	lastWriterLead = 60 * time.Second
	lastWriterLag  = 5 * time.Second
)

// lastWriters is the latest writer per project id; the zero value is ready.
type lastWriters struct {
	mu    sync.Mutex
	mem   map[string]org.LastWriter
	dirty map[string]org.LastWriter
	kick  chan struct{}
	once  sync.Once
}

func (w *lastWriters) note(id string, lw org.LastWriter) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.mem == nil {
		w.mem, w.dirty = map[string]org.LastWriter{}, map[string]org.LastWriter{}
	}
	w.mem[id] = lw
	w.dirty[id] = lw
}

func (w *lastWriters) snapshot() map[string]org.LastWriter {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string]org.LastWriter, len(w.mem))
	for k, v := range w.mem {
		out[k] = v
	}
	return out
}

// flush writes the pending writers to the org database.
func (w *lastWriters) flush(ctx context.Context, m *org.Manager) error {
	w.mu.Lock()
	batch := w.dirty
	w.dirty = map[string]org.LastWriter{}
	w.mu.Unlock()
	if len(batch) == 0 || m == nil {
		return nil
	}
	err := m.SaveLastWriters(ctx, batch)
	if err != nil {
		w.mu.Lock()
		for k, v := range batch {
			if _, newer := w.dirty[k]; !newer {
				w.dirty[k] = v
			}
		}
		w.mu.Unlock()
	}
	return err
}

// noteWriter records actor (an activity actor) as the latest writer of the
// project, for account user, and schedules the background save.
func (a *App) noteWriter(projectID, user, actor string) {
	if projectID == "" || projectID == demoID || actor == "" || actor == "system" || strings.HasPrefix(actor, "system:") ||
		strings.HasPrefix(actor, "import:") {
		return
	}
	a.writers.note(projectID, org.LastWriter{User: user, Actor: actor, At: clock.UTCNow()})
	if a.Org == nil {
		return
	}
	a.writers.once.Do(func() {
		a.writers.kick = make(chan struct{}, 1)
		go func() {
			for range a.writers.kick {
				_ = a.writers.flush(context.Background(), a.Org)
				time.Sleep(lastWriterFlush)
			}
		}()
	})
	select {
	case a.writers.kick <- struct{}{}:
	default:
	}
}

func parseStamp(v any) (time.Time, bool) {
	s := pyjson.Str(v)
	if v == nil || s == "" {
		return time.Time{}, false
	}
	return parseActivityAt(s)
}

// peopleView answers "may the viewer see this person in this project" and
// caches the membership lookups for one listing.
type peopleView struct {
	c       *reqCtx
	viewer  string
	people  map[string]org.Person
	members map[[2]string]bool
}

func (v *peopleView) person(username string) (org.Person, bool) {
	p, ok := v.people[strings.ToLower(username)]
	return p, ok && !p.Disabled
}

func (v *peopleView) visible(username, projectID string) bool {
	if username == "" {
		return false
	}
	if v.c.app.Auth == nil || strings.EqualFold(username, v.viewer) {
		return true
	}
	key := [2]string{strings.ToLower(username), projectID}
	if seen, ok := v.members[key]; ok {
		return seen
	}
	member, err := v.c.app.Org.ProjectMember(v.c.ctx, username, projectID)
	v.members[key] = err == nil && member
	return v.members[key]
}

func (v *peopleView) label(p org.Person) string {
	if p.DisplayName != "" {
		return p.DisplayName
	}
	return p.Username
}

// withProjectPeople sets creator, created_by_me and last_actor on every
// listed project (pyjson objects with "id", "created" and "updated").
func (c *reqCtx) withProjectPeople(projects []any) {
	if len(projects) == 0 || c.app.Org == nil {
		return
	}
	creators, err := c.app.Org.ProjectCreators(c.ctx)
	if err != nil {
		return
	}
	people, err := c.app.Org.People(c.ctx)
	if err != nil {
		return
	}
	stored, err := c.app.Org.LastWriters(c.ctx)
	if err != nil {
		stored = map[string]org.LastWriter{}
	}
	for id, w := range c.app.writers.snapshot() {
		if old, ok := stored[id]; !ok || w.At >= old.At {
			stored[id] = w
		}
	}
	view := &peopleView{c: c, viewer: c.actor(), people: people, members: map[[2]string]bool{}}
	viewerID := ""
	if me, ok := view.person(view.viewer); ok {
		viewerID = me.ID
	}
	for _, raw := range projects {
		item, ok := raw.(*pyjson.Object)
		if !ok {
			continue
		}
		id := pyjson.Str(item.Value("id"))
		if id == "" || id == demoID {
			continue
		}
		mine := false
		if cr, ok := creators[id]; ok && cr.Username != "" {
			created, okA := parseStamp(item.Value("created"))
			registered, okB := parseActivityAt(cr.CreatedAt)
			if p, known := view.person(cr.Username); known && okA && okB && absDuration(registered.Sub(created)) <= org.CreatorWindow {
				mine = viewerID != "" && p.ID == viewerID
				if view.visible(p.Username, id) {
					item.Set("creator", pyjson.NewObject("user", p.Username, "name", view.label(p)))
				}
			}
		}
		item.Set("created_by_me", mine)
		if lw, ok := stored[id]; ok {
			if actor := c.lastActor(view, id, lw, item.Value("updated")); actor != nil {
				item.Set("last_actor", actor)
			}
		}
	}
}

// lastActor is the last_actor member for a stored writer, or nil.
func (c *reqCtx) lastActor(view *peopleView, projectID string, lw org.LastWriter, updatedValue any) *pyjson.Object {
	at, okAt := parseActivityAt(lw.At)
	updated, okUp := parseStamp(updatedValue)
	if !okAt || !okUp || at.Before(updated.Add(-lastWriterLead)) || at.After(updated.Add(lastWriterLag)) {
		return nil
	}
	var agent any
	if strings.HasPrefix(lw.Actor, "agent:") {
		agent = agentLabel(lw.Actor)
	}
	var user, name any
	if lw.User != "" {
		p, ok := view.person(lw.User)
		if !ok || !view.visible(p.Username, projectID) {
			return nil // the label of an agent names its account too
		}
		user, name = p.Username, view.label(p)
	} else if agent == nil {
		return nil
	}
	return pyjson.NewObject("user", user, "name", name, "agent", agent, "at", lw.At)
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
