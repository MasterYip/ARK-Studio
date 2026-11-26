package server

// Go only: "new for review" badges of the project picker.
//
//   GET  /api/projects   every project the person may write to carries
//                        review_new    Needs you cards with something the
//                                      person has not seen yet
//                        review_total  all Needs you cards
//   POST /api/needs/seen {all: true, items: [shown keys]}  the person opened
//                                              the current project's Needs you
//                                              list (items: the cards it showed)
//                        {items: ["u:T-12", "rg:RG-3"]}  they opened these
//                        {clear: ["a:T-12"]}  they cleared these assignments
//                                              from Needs you (read, and hidden
//                                              until assigned again)
//                        answers {review_new, review_total} for the project
//   GET  /api/needs/assigned  the current project's units others assigned to
//                        the person (store/assignments.go), one entry per
//                        batch: {items: [{batch, by, by_name, actor, at, new,
//                        units: [{uid, title, type, status, key, since, new}]}]}
//                        Units in review or open decisions are left out: they
//                        are in Needs you already.  Read members get theirs too.
//
// What counts as a card and as new: store/needscards.go; what the person has
// seen: org/reviewseen.go (server-side, so every device agrees).  Read-only
// members get no badge: nothing in Needs you waits for them.
//
// Cost: one org query for the person's marks, and per project the cards,
// cached per database and reused until the database files change (the same
// signature as project_activity.go).  A project that is not open is read on
// a short-lived read-only connection.  tests/python/contract/go_only.py
// AUGMENTED "/api/projects" strips the two members before the replay
// compares bodies.

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/sqlitedb"
	"arkstudio/go/internal/store"
)

const (
	needsSeenPath     = "/api/needs/seen"
	needsAssignedPath = "/api/needs/assigned"
	needsNewPath      = "/api/needs/new"
	needsCardsMaxKeep = 1024 // databases whose cards are cached
	needsSeenMaxItems = 200  // keys in one POST
)

var needsSeenGoOnly = []PortedRoute{{"goonly:exact:" + needsSeenPath, "POST"},
	{"goonly:exact:" + needsAssignedPath, "GET,HEAD"}, {"goonly:exact:" + needsNewPath, "GET,HEAD"}}

type needsCardsEntry struct {
	sig      string
	cards    []store.NeedsCard
	assigned []store.AssignedCard // every person's; AssignedTo picks one
}

// projectNeedsCards caches the cards per database; the zero value is ready.
type projectNeedsCards struct {
	mu      sync.Mutex
	entries map[string]needsCardsEntry
}

func (n *projectNeedsCards) get(db string) (needsCardsEntry, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	e, ok := n.entries[db]
	return e, ok
}

func (n *projectNeedsCards) put(db string, e needsCardsEntry) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.entries == nil || len(n.entries) >= needsCardsMaxKeep {
		n.entries = map[string]needsCardsEntry{}
	}
	n.entries[db] = e
}

// readNeeds runs store.NeedsCards and store.AssignedCards.
func readNeeds(ctx context.Context, q sqlitedb.Querier) (needsCardsEntry, error) {
	var e needsCardsEntry
	var err error
	if e.cards, err = store.NeedsCards(ctx, q); err != nil {
		return e, err
	}
	e.assigned, err = store.AssignedCards(ctx, q)
	return e, err
}

// readNeedsCards runs readNeeds on the open project's read pool, or on a
// short-lived read-only connection.
func (a *App) readNeedsCards(ctx context.Context, folder, db string) (needsCardsEntry, error) {
	if p := a.Projects.Cached(folder); p != nil && p.Store != nil {
		var out needsCardsEntry
		err := p.Store.DB.Read(ctx, func(q sqlitedb.Querier) error {
			var err error
			out, err = readNeeds(ctx, q)
			return err
		})
		return out, err
	}
	v := url.Values{}
	v.Add("mode", "ro")
	v.Add("_pragma", "busy_timeout(2000)")
	v.Add("_pragma", "query_only(1)")
	conn, err := sql.Open("sqlite", "file:"+filepath.ToSlash(db)+"?"+v.Encode())
	if err != nil {
		return needsCardsEntry{}, err
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	return readNeeds(ctx, conn)
}

// needsFor is the (cached) card list and assignments of the project in
// folder; ok=false when they cannot be read.
func (a *App) needsFor(ctx context.Context, folder string) (needsCardsEntry, bool) {
	db := filepath.Join(folder, "ark.sqlite3")
	sig, _, ok := dbSignature(db)
	if !ok {
		return needsCardsEntry{}, false
	}
	if e, ok := a.reviews.get(db); ok && e.sig == sig {
		return e, true
	}
	e, err := a.readNeedsCards(ctx, folder, db)
	if err != nil {
		if e, ok := a.reviews.get(db); ok {
			return e, true // keep the last good figures
		}
		return needsCardsEntry{}, false
	}
	e.sig = sig
	a.reviews.put(db, e)
	return e, true
}

// needsCardsFor is the (cached) card list of the project in folder.
func (a *App) needsCardsFor(ctx context.Context, folder string) ([]store.NeedsCard, bool) {
	e, ok := a.needsFor(ctx, folder)
	return e.cards, ok
}

// newAssigned counts the person's assignment cards with something new, and
// all of them (the badge adds them to the review figures).
func newAssigned(cards []store.AssignedCard, user string, seen store.NeedsSeen) (fresh, total int) {
	unread := store.NeedsSeen{Items: seen.Items} // only reading the item counts, not the list watermark
	for _, c := range store.AssignedTo(cards, user, seen) {
		total++
		for _, m := range c.Marks() {
			if unread.ItemNew(m) {
				fresh++
				break
			}
		}
	}
	return fresh, total
}

// mayReview: the listed permission lets the person decide reviews.
func mayReview(permission any) bool {
	return org.PermissionRank[pyjson.Str(permission)] >= org.PermissionRank["write"]
}

// withNewReviews sets review_new and review_total on every listed project
// the person may write to, and on every project where units are assigned to
// them (assignments count for read-only members too).
func (c *reqCtx) withNewReviews(projects []any) {
	user := c.actor()
	if user == "" || len(projects) == 0 || c.app.Org == nil {
		return
	}
	seen, err := c.app.Org.ReviewSeen(c.ctx, user)
	if err != nil {
		return
	}
	others := c.app.Org.HighlightOthers(c.ctx, user)
	type result struct {
		fresh, total int
		ok           bool
	}
	results := make([]result, len(projects))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < min(activityWorkers, len(projects)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				item := projects[i].(*pyjson.Object)
				e, ok := c.app.needsFor(c.ctx, pyjson.Str(item.Value("path")))
				if !ok {
					continue
				}
				mine := org.Viewer(seen, pyjson.Str(item.Value("id")), user, others)
				var r result
				if mayReview(item.Value("permission")) {
					r.fresh, r.total = store.NewCards(e.cards, mine)
					r.ok = true
				}
				if fresh, total := newAssigned(e.assigned, user, mine); total > 0 {
					r.fresh, r.total, r.ok = r.fresh+fresh, r.total+total, true
				}
				results[i] = r
			}
		}()
	}
	for i, raw := range projects {
		item, ok := raw.(*pyjson.Object)
		if !ok || pyjson.Str(item.Value("path")) == "" {
			continue
		}
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	for i, raw := range projects {
		if item, ok := raw.(*pyjson.Object); ok && results[i].ok {
			item.Set("review_new", int64(results[i].fresh)).Set("review_total", int64(results[i].total))
		}
	}
}

// postNeedsSeen answers POST /api/needs/seen for the current project.
func (c *reqCtx) postNeedsSeen() error {
	p, err := c.require("read") // read-only members: their assignments only
	if err != nil {
		return err
	}
	user := c.actor()
	if user == "" || c.app.Org == nil {
		return apiErr(403, "Sign in to keep track of what you have seen.", "")
	}
	body := c.body
	if body == nil {
		body = pyjson.NewObject()
	}
	all := body.Value("all") == true
	keys, err := needsKeys(body, "items")
	if err != nil {
		return err
	}
	cleared, err := needsKeys(body, "clear")
	if err != nil {
		return err
	}
	opened, err := needsKeys(body, "opened")
	if err != nil {
		return err
	}
	// "Mark as unread": forget that the person opened these items.
	unopened, err := needsKeys(body, "unopened")
	if err != nil {
		return err
	}
	if !all && len(keys) == 0 && len(cleared) == 0 && len(opened) == 0 && len(unopened) == 0 {
		return apiErr(400, "send all: true, items, opened, unopened or clear", "")
	}
	e, ok := c.app.needsFor(c.ctx, p.Path)
	if !ok {
		if err := p.Store.DB.Read(c.ctx, func(q sqlitedb.Querier) error {
			var err error
			e, err = readNeeds(c.ctx, q)
			return err
		}); err != nil {
			return err
		}
	}
	review := c.mayWriteTo(p)
	now := clock.Now()
	id := p.ID()
	if all && review {
		// To the second, like the stored review times (NeedsSeen).
		if err := c.app.Org.MarkReviewListSeen(c.ctx, user, id, store.MarkTimeOf(now.Truncate(time.Second))); err != nil {
			return err
		}
	}
	current := map[string]string{}
	if review {
		current = store.CardMarks(e.cards)
	}
	mine := store.AssignedTo(e.assigned, user, store.NeedsSeen{})
	keep := map[string]bool{}
	for _, card := range mine {
		for _, m := range card.Marks() {
			current[m.Key] = m.Since
			keep[m.Key] = true
		}
	}
	items := map[string]string{}
	for _, key := range keys {
		if since, ok := current[key]; ok {
			items[key] = since // keys no longer in Needs you are left out
		}
		// Opening a unit also reads the notice of its assignment.
		if a := "a:" + strings.TrimPrefix(key, "u:"); strings.HasPrefix(key, "u:") && keep[a] {
			items[a] = current[a]
		}
	}
	// Opened in the node details: the only thing that makes an item seen for
	// the Needs you bubbles and the picker badge (store.OpenedPrefix).
	for _, key := range opened {
		if since, ok := current[key]; ok {
			items[key] = since
			items[store.OpenedPrefix+key] = since
		}
	}
	for _, key := range cleared {
		if keep[key] {
			items[key] = current[key]
			items[store.ClearedPrefix+key] = current[key]
		}
	}
	if err := c.app.Org.MarkReviewItemsSeen(c.ctx, user, id, items, store.MarkTimeOf(now)); err != nil {
		return err
	}
	if len(unopened) > 0 {
		forget := make([]string, len(unopened))
		for i, k := range unopened {
			forget[i] = store.OpenedPrefix + k
		}
		if err := c.app.Org.ForgetReviewItems(c.ctx, user, id, forget); err != nil {
			return err
		}
	}
	if err := c.app.Org.ForgetAssignments(c.ctx, user, id, store.AssignedKeys(e.assigned, user)); err != nil {
		return err
	}
	seen, err := c.app.Org.ReviewSeen(c.ctx, user)
	if err != nil {
		return err
	}
	var fresh, total int
	if review {
		fresh, total = store.NewCards(e.cards, org.Viewer(seen, id, user, c.app.Org.HighlightOthers(c.ctx, user)))
	}
	af, at := newAssigned(e.assigned, user, seen[id])
	c.ok(pyjson.NewObject("review_new", int64(fresh+af), "review_total", int64(total+at), "new_keys", newKeysOf(e, review, org.Viewer(seen, id, user, c.app.Org.HighlightOthers(c.ctx, user)))))
	return nil
}

func newKeysOf(e needsCardsEntry, review bool, seen store.NeedsSeen) []any {
	out := []any{}
	if !review {
		return out
	}
	for _, k := range store.NewKeys(e.cards, seen) {
		out = append(out, k)
	}
	return out
}

// getNeedsNew answers GET /api/needs/new: the Needs you items of the current
// project the person has not opened in the node details yet.
func (c *reqCtx) getNeedsNew() error {
	p, err := c.require("read")
	if err != nil {
		return err
	}
	user := c.actor()
	if user == "" || c.app.Org == nil {
		c.ok(pyjson.NewObject("new_keys", []any{}))
		return nil
	}
	e, ok := c.app.needsFor(c.ctx, p.Path)
	if !ok {
		if err := p.Store.DB.Read(c.ctx, func(q sqlitedb.Querier) error {
			var err error
			e, err = readNeeds(c.ctx, q)
			return err
		}); err != nil {
			return err
		}
	}
	seen, err := c.app.Org.ReviewSeen(c.ctx, user)
	if err != nil {
		return err
	}
	c.ok(pyjson.NewObject("new_keys", newKeysOf(e, c.mayWriteTo(p), org.Viewer(seen, p.ID(), user, c.app.Org.HighlightOthers(c.ctx, user)))))
	return nil
}

// mayWriteTo: the person (and their token's scope) may write to p, so the
// project's reviews wait for them too.
func (c *reqCtx) mayWriteTo(p *project.Project) bool {
	if c.token != nil {
		if cap, ok := org.PermissionRank[pyjson.Str(c.token.row.Get("scope"))]; ok && cap < org.PermissionRank["write"] {
			return false
		}
	}
	return c.app.Auth == nil || c.checkProjectAccess(p, "write") == nil
}

// needsKeys reads a list of Needs you keys from body[field].
func needsKeys(body *pyjson.Object, field string) ([]string, error) {
	v := body.Value(field)
	if v == nil {
		return nil, nil
	}
	raw, ok := v.([]any)
	if !ok {
		return nil, apiErr(400, field+" must be a list", "")
	}
	if len(raw) > needsSeenMaxItems {
		return nil, apiErr(400, "too many items", "")
	}
	keys := make([]string, 0, len(raw))
	for _, v := range raw {
		key, ok := v.(string)
		if !ok || !store.ValidNeedsKey(key) {
			return nil, apiErr(400, field+" must be Needs you keys (u:<uid>, a:<uid> or rg:RG-<n>)", "")
		}
		keys = append(keys, key)
	}
	return keys, nil
}

// getNeedsAssigned answers GET /api/needs/assigned for the current project.
func (c *reqCtx) getNeedsAssigned() error {
	p, err := c.require("read")
	if err != nil {
		return err
	}
	user := c.actor()
	items := []any{}
	if user == "" {
		c.ok(pyjson.NewObject("items", items))
		return nil
	}
	e, ok := c.app.needsFor(c.ctx, p.Path)
	if !ok {
		if err := p.Store.DB.Read(c.ctx, func(q sqlitedb.Querier) error {
			var err error
			e, err = readNeeds(c.ctx, q)
			return err
		}); err != nil {
			return err
		}
	}
	var seen store.NeedsSeen
	if c.app.Org != nil {
		all, err := c.app.Org.ReviewSeen(c.ctx, user)
		if err != nil {
			return err
		}
		seen = all[p.ID()]
	}
	unread := store.NeedsSeen{Items: seen.Items}
	for _, card := range store.AssignedTo(e.assigned, user, seen) {
		units := []any{}
		fresh := false
		for _, u := range card.Units {
			n := unread.ItemNew(store.NeedsMark{Key: u.Key(), Since: u.Since})
			fresh = fresh || n
			units = append(units, pyjson.NewObject("uid", u.UID, "title", u.Title, "type", u.Type, "status", u.Status,
				"key", u.Key(), "since", u.Since, "new", n))
		}
		items = append(items, pyjson.NewObject("batch", card.Batch, "by", card.By, "by_name", c.displayNameOf(card.By),
			"actor", card.Actor, "at", card.Latest(), "new", fresh, "units", units))
	}
	c.ok(pyjson.NewObject("items", items))
	return nil
}
