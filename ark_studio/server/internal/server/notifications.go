package server

// Go only: the person's notification feed for the Android app.
//
//   GET /api/notifications[?since=<cursor>]
//
// answers {cursor, scope, projects, items, active}:
//   items    what needs the person, newest first; with since, only what
//            entered that state after the cursor
//   active   the ids of every item that still needs them, whatever since
//            says (the app withdraws a notification whose id is gone, e.g.
//            a review the person already opened in the browser)
//   cursor   the value to send as since next time (a little before now, so
//            an item committed while this request ran is not lost; the app
//            drops ids it already posted)
//   scope    "account" (every project the person may open) or "project"
//            (a phone paired by QR code stays on its project, like
//            GET /api/projects)
//
// An item is {id, project: {id, name}, kind, priority, title, body,
// deep_link: {type, ref, unit_type, project_id, route}, created_at, count}.
// Kinds and sources:
//   review    Needs you cards with something new for the person (the seen
//             model of project_reviews.go / org/reviewseen.go: what they
//             opened in the browser does not notify).  One item per card:
//             a review group or an automatic delivery group counts once.
//             Projects the person may only read have none, reviews the
//             person opened on their own work neither (ownOrElsewhere).
//   decision  open decisions in the same cards (high priority)
//   assigned  units someone else assigned to the person (store/
//             assignments.go): one item per batch, "Ana assigned you 3
//             nodes", with by / by_name (who assigned them).  Read once the
//             person opened the unit or the item was shown in Needs you
//             (seen keys a:<uid>); read-only members get them too.
//   approval  pending approval requests of the person's agent sessions
//             (high priority)
//   session_error  the person's sessions that errored, or that ARK stopped
//             when it restarted, in the last notifySessionWindow (high)
//   session_done   the person's sessions that ended normally (low)
// Agents' progress, comments and the person's own actions (assigning
// yourself included) are not items.
//
// Cost: the cached Needs you cards per project (needsCardsFor) and one pass
// over the session list; no per-project activity reads.

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/sessions"
	"arkstudio/go/internal/store"
)

const (
	notificationsPath   = "/api/notifications"
	notifyOverlap       = 2 * time.Minute
	notifySessionWindow = 24 * time.Hour
	notifyMaxItems      = 200
)

var notificationsGoOnly = []PortedRoute{{"goonly:exact:" + notificationsPath, "GET,HEAD"}}

type notifyProject struct {
	id, name, path string
	review         bool // may decide reviews (write or more)
}

type notifyItem struct {
	id, kind, priority, title, body string
	by, byName                      string // assigned: who assigned
	project                         notifyProject
	link                            *pyjson.Object
	at                              time.Time
	count                           int
}

// notifyProjects is what the feed covers: the person's projects, or only
// the paired project of a phone paired by QR code.
func (c *reqCtx) notifyProjects() ([]notifyProject, string, error) {
	listed, err := c.visibleProjects()
	if err != nil {
		return nil, "", err
	}
	scope, only := "account", ""
	if s := c.mobileSession; s != nil {
		source := "qr"
		if r := c.app.Mobile.PairingFor(s); r != nil {
			source = r.Source
		}
		if c.app.Auth == nil || source != "password" {
			scope, only = "project", s.ProjectID
		}
	}
	var out []notifyProject
	for _, raw := range listed {
		o, ok := raw.(*pyjson.Object)
		if !ok {
			continue
		}
		id := pyjson.Str(o.Value("id"))
		if id == "" || id == demoID || (only != "" && id != only) {
			continue
		}
		out = append(out, notifyProject{id: id, name: pyjson.Str(o.Value("name")), path: pyjson.Str(o.Value("path")),
			review: mayReview(o.Value("permission"))})
	}
	return out, scope, nil
}

// sameActor: an actor name ("ana", "human:ana", "human:web:ana") is user.
func sameActor(actor, user string) bool {
	a, u := strings.ToLower(strings.TrimSpace(actor)), strings.ToLower(strings.TrimSpace(user))
	if a == "" || u == "" {
		return false
	}
	if a == u {
		return true
	}
	i := strings.LastIndex(a, ":")
	return strings.HasPrefix(a, "human:") && i >= 0 && a[i+1:] == u
}

// ownOrElsewhere: the review is the person's own doing.  A unit's assignee
// defaults to its creator, so it says whose work it is, not who decides: a
// review counts as the person's own when they opened it, or opened it from
// the browser ("human:ui") on their own unit.  Agents' review requests and
// open decisions always notify.
func ownOrElsewhere(kind string, m store.NeedsMark, user string) bool {
	if kind == "decision" {
		return false
	}
	return sameActor(m.By, user) || (strings.HasPrefix(m.By, "human:") && sameActor(m.Assignee, user))
}

// webRoute is the web app route of a unit, or Home (Needs you) for a review
// group (web/js/app.js parseHash).
func webRoute(kind, ref string) string {
	if kind == "review_group" {
		return "#/home"
	}
	return "#/editor/" + strings.ReplaceAll(url.PathEscape("unit:"+ref), ":", "%3A")
}

func unitLink(p notifyProject, typ, ref, unitType string) *pyjson.Object {
	return pyjson.NewObject("type", typ, "ref", ref, "unit_type", unitType, "project_id", p.id, "route", webRoute(typ, ref))
}

// cardItems are the review and decision items of one project.
func cardItems(p notifyProject, cards []store.NeedsCard, seen store.NeedsSeen, user string, auth bool) []notifyItem {
	var out []notifyItem
	for _, card := range cards {
		var fresh []store.NeedsMark
		for _, m := range card.Items {
			if !seen.ItemNew(m) {
				continue
			}
			if auth && ownOrElsewhere(card.Kind, m, user) {
				continue
			}
			fresh = append(fresh, m)
		}
		if len(fresh) == 0 {
			continue
		}
		var at time.Time
		keys := make([]string, 0, len(fresh))
		for _, m := range fresh {
			keys = append(keys, m.Key+"@"+m.Since)
			if t := store.ParseTime(m.Since); t.After(at) {
				at = t
			}
		}
		sort.Strings(keys)
		item := notifyItem{project: p, kind: "review", priority: "default", title: card.Title, at: at,
			count: len(card.Items)}
		switch card.Kind {
		case "decision":
			item.kind, item.priority = "decision", "high"
			item.link = unitLink(p, "unit", card.Ref, card.RefType)
		case "review_group":
			item.link = unitLink(p, "review_group", card.Ref, "")
		default:
			item.link = unitLink(p, "unit", card.Ref, card.RefType)
		}
		if item.title == "" {
			item.title = card.Ref
		}
		item.id = item.kind + ":" + p.id + ":" + card.Ref + ":" + keys[len(keys)-1]
		if len(keys) > 1 {
			item.id = item.kind + ":" + p.id + ":" + card.Ref + ":" + strings.Join(keys, ",")
		}
		out = append(out, item)
	}
	return out
}

// assignedItems are the assignment items of one project: the person's
// assignment cards with a unit they have not read.
func assignedItems(p notifyProject, cards []store.AssignedCard, seen store.NeedsSeen, user string,
	nameOf func(string) string) []notifyItem {
	var out []notifyItem
	unread := store.NeedsSeen{Items: seen.Items} // only reading the item counts, not the list watermark
	for _, card := range store.AssignedTo(cards, user, seen) {
		var keys []string
		for _, m := range card.Marks() {
			if unread.ItemNew(m) {
				keys = append(keys, m.Key+"@"+m.Since)
			}
		}
		if len(keys) == 0 {
			continue
		}
		sort.Strings(keys)
		first := card.Units[0]
		name := nameOf(card.By)
		item := notifyItem{project: p, kind: "assigned", priority: "default", title: first.Title,
			by: card.By, byName: name, at: store.ParseTime(card.Latest()), count: len(card.Units)}
		if item.title == "" {
			item.title = first.UID
		}
		if len(card.Units) == 1 {
			item.body = "Assigned to you by " + name
			item.link = unitLink(p, "unit", first.UID, first.Type)
		} else {
			item.body = name + " assigned you " + strconv.Itoa(len(card.Units)) + " nodes"
			item.link = pyjson.NewObject("type", "needs", "ref", card.Batch, "unit_type", "", "project_id", p.id,
				"route", "#/home")
		}
		item.id = "assigned:" + p.id + ":" + card.Batch + ":" + strings.Join(keys, ",")
		out = append(out, item)
	}
	return out
}

// sessionItems are the approval and session items of the projects.
func sessionItems(alerts []sessions.Alert, projects map[string]notifyProject, user string, auth bool, now time.Time) []notifyItem {
	var out []notifyItem
	for _, a := range alerts {
		p, ok := projects[a.ProjectID]
		if !ok || (auth && !sameActor(a.Owner, user)) {
			continue
		}
		title := a.Title
		if title == "" {
			title = a.ID
		}
		link := pyjson.NewObject("type", "session", "ref", a.ID, "unit_type", "", "project_id", p.id,
			"route", "#/execute/sessions")
		for _, ap := range a.Approvals {
			body := ap.Description
			if body == "" {
				body = strings.TrimSpace(ap.Tool + " " + firstNonEmpty(ap.Command, ap.Path))
			}
			out = append(out, notifyItem{id: "approval:" + a.ID + ":" + ap.ID, kind: "approval", priority: "high",
				title: title, body: body, project: p, link: link, at: store.ParseTime(ap.RequestedAt), count: 1})
		}
		ended := store.ParseTime(a.EndedAt)
		if ended.IsZero() || now.Sub(ended) > notifySessionWindow {
			continue
		}
		switch {
		case a.Status == "errored" || a.Restarted:
			body := a.Error
			if a.Restarted {
				body = a.Notice
			}
			out = append(out, notifyItem{id: "session_error:" + a.ID + ":" + a.EndedAt, kind: "session_error",
				priority: "high", title: title, body: body, project: p, link: link, at: ended, count: 1})
		case a.Status == "stopped" && a.ExitCode == int64(0):
			out = append(out, notifyItem{id: "session_done:" + a.ID + ":" + a.EndedAt, kind: "session_done",
				priority: "low", title: title, project: p, link: link, at: ended, count: 1})
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// getNotifications answers GET /api/notifications.
func (c *reqCtx) getNotifications() error {
	user := c.actor()
	auth := c.app.Auth != nil
	if auth && user == "" {
		return apiErr(401, "Sign in with the ARK account to continue.", "AUTH_REQUIRED")
	}
	var since time.Time
	if raw := strings.TrimSpace(c.query.First("since", "")); raw != "" {
		if since = store.ParseTime(raw); since.IsZero() {
			return apiErr(400, "since must be a cursor from an earlier reply", "INVALID_CURSOR")
		}
	}
	projects, scope, err := c.notifyProjects()
	if err != nil {
		return err
	}
	now := clock.Now().UTC()
	byID := map[string]notifyProject{}
	for _, p := range projects {
		byID[p.id] = p
	}
	var items []notifyItem
	if c.app.Org != nil {
		seen, err := c.app.Org.ReviewSeen(c.ctx, user)
		if err != nil {
			return err
		}
		lists := make([][]notifyItem, len(projects))
		jobs := make(chan int)
		var wg sync.WaitGroup
		for w := 0; w < min(activityWorkers, len(projects)); w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range jobs {
					p := projects[i]
					e, ok := c.app.needsFor(c.ctx, p.path)
					if !ok {
						continue
					}
					if p.review {
						lists[i] = cardItems(p, e.cards, seen[p.id], user, auth)
					}
					lists[i] = append(lists[i], assignedItems(p, e.assigned, seen[p.id], user, c.displayNameOf)...)
				}
			}()
		}
		for i, p := range projects {
			if p.path != "" {
				jobs <- i
			}
		}
		close(jobs)
		wg.Wait()
		for _, l := range lists {
			items = append(items, l...)
		}
	}
	if c.app.Sessions != nil {
		items = append(items, sessionItems(c.app.Sessions.Alerts(), byID, user, auth, now)...)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].at.Equal(items[j].at) {
			return items[i].at.After(items[j].at)
		}
		return items[i].id < items[j].id
	})
	active := []any{}
	list := []any{}
	for _, it := range items {
		active = append(active, it.id)
		if (!since.IsZero() && !it.at.After(since)) || len(list) >= notifyMaxItems {
			continue
		}
		var created any
		if !it.at.IsZero() {
			created = store.MarkTimeOf(it.at)
		}
		list = append(list, pyjson.NewObject("id", it.id, "project", pyjson.NewObject("id", it.project.id, "name", it.project.name),
			"kind", it.kind, "priority", it.priority, "title", it.title, "body", it.body, "deep_link", it.link,
			"created_at", created, "count", int64(it.count)))
		if it.kind == "assigned" {
			list[len(list)-1].(*pyjson.Object).Set("by", it.by).Set("by_name", it.byName)
		}
	}
	names := []any{}
	for _, p := range projects {
		names = append(names, pyjson.NewObject("id", p.id, "name", p.name))
	}
	c.ok(pyjson.NewObject("cursor", store.MarkTimeOf(now.Add(-notifyOverlap)), "server_time", store.MarkTimeOf(now),
		"scope", scope, "projects", names, "items", list, "active", active))
	return nil
}

// displayNameOf is the account's display name, or the name itself.
func (c *reqCtx) displayNameOf(username string) string {
	if c.app.Org == nil || username == "" {
		return username
	}
	if row, ok, err := c.app.Org.UserByName(c.ctx, username); err == nil && ok {
		if d := strings.TrimSpace(row.Str("display_name")); d != "" {
			return d
		}
		return row.Str("username")
	}
	return username
}
