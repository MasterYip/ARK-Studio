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
//                        answers {review_new, review_total} for the project
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
	"sync"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/sqlitedb"
	"arkstudio/go/internal/store"
)

const (
	needsSeenPath     = "/api/needs/seen"
	needsCardsMaxKeep = 1024 // databases whose cards are cached
	needsSeenMaxItems = 200  // keys in one POST
)

var needsSeenGoOnly = []PortedRoute{{"goonly:exact:" + needsSeenPath, "POST"}}

type needsCardsEntry struct {
	sig   string
	cards []store.NeedsCard
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

// readNeedsCards runs store.NeedsCards on the open project's read pool, or
// on a short-lived read-only connection.
func (a *App) readNeedsCards(ctx context.Context, folder, db string) ([]store.NeedsCard, error) {
	if p := a.Projects.Cached(folder); p != nil && p.Store != nil {
		var out []store.NeedsCard
		err := p.Store.DB.Read(ctx, func(q sqlitedb.Querier) error {
			var err error
			out, err = store.NeedsCards(ctx, q)
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
		return nil, err
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	return store.NeedsCards(ctx, conn)
}

// needsCardsFor is the (cached) card list of the project in folder; ok=false
// when it cannot be read.
func (a *App) needsCardsFor(ctx context.Context, folder string) ([]store.NeedsCard, bool) {
	db := filepath.Join(folder, "ark.sqlite3")
	sig, _, ok := dbSignature(db)
	if !ok {
		return nil, false
	}
	if e, ok := a.reviews.get(db); ok && e.sig == sig {
		return e.cards, true
	}
	cards, err := a.readNeedsCards(ctx, folder, db)
	if err != nil {
		if e, ok := a.reviews.get(db); ok {
			return e.cards, true // keep the last good figures
		}
		return nil, false
	}
	a.reviews.put(db, needsCardsEntry{sig: sig, cards: cards})
	return cards, true
}

// mayReview: the listed permission lets the person decide reviews.
func mayReview(permission any) bool {
	return org.PermissionRank[pyjson.Str(permission)] >= org.PermissionRank["write"]
}

// withNewReviews sets review_new and review_total on every listed project
// the person may write to.
func (c *reqCtx) withNewReviews(projects []any) {
	user := c.actor()
	if user == "" || len(projects) == 0 || c.app.Org == nil {
		return
	}
	seen, err := c.app.Org.ReviewSeen(c.ctx, user)
	if err != nil {
		return
	}
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
				cards, ok := c.app.needsCardsFor(c.ctx, pyjson.Str(item.Value("path")))
				if !ok {
					continue
				}
				fresh, total := store.NewCards(cards, seen[pyjson.Str(item.Value("id"))])
				results[i] = result{fresh, total, true}
			}
		}()
	}
	for i, raw := range projects {
		item, ok := raw.(*pyjson.Object)
		if !ok || pyjson.Str(item.Value("path")) == "" || !mayReview(item.Value("permission")) {
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
	p, err := c.require("write") // read-only members have no badge
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
	var keys []string
	if raw, ok := body.Value("items").([]any); ok {
		if len(raw) > needsSeenMaxItems {
			return apiErr(400, "too many items", "")
		}
		for _, v := range raw {
			key, ok := v.(string)
			if !ok || !store.ValidNeedsKey(key) {
				return apiErr(400, "items must be Needs you keys (u:<uid> or rg:RG-<n>)", "")
			}
			keys = append(keys, key)
		}
	} else if v := body.Value("items"); v != nil {
		return apiErr(400, "items must be a list", "")
	}
	if !all && len(keys) == 0 {
		return apiErr(400, "send all: true or items", "")
	}
	cards, ok := c.app.needsCardsFor(c.ctx, p.Path)
	if !ok {
		var err error
		if err = p.Store.DB.Read(c.ctx, func(q sqlitedb.Querier) error {
			cards, err = store.NeedsCards(c.ctx, q)
			return err
		}); err != nil {
			return err
		}
	}
	now := clock.Now()
	id := p.ID()
	if all {
		// To the second, like the stored review times (NeedsSeen).
		if err := c.app.Org.MarkReviewListSeen(c.ctx, user, id, store.MarkTimeOf(now.Truncate(time.Second))); err != nil {
			return err
		}
	}
	current := store.CardMarks(cards)
	items := map[string]string{}
	for _, key := range keys {
		if since, ok := current[key]; ok {
			items[key] = since // keys no longer in Needs you are left out
		}
	}
	if err := c.app.Org.MarkReviewItemsSeen(c.ctx, user, id, items, store.MarkTimeOf(now)); err != nil {
		return err
	}
	seen, err := c.app.Org.ReviewSeen(c.ctx, user)
	if err != nil {
		return err
	}
	fresh, total := store.NewCards(cards, seen[id])
	c.ok(pyjson.NewObject("review_new", int64(fresh), "review_total", int64(total)))
	return nil
}
