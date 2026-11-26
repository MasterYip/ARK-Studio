package store

// Needs you cards for the project picker's "new for review" badges
// (internal/server/project_reviews.go).  The cards are the ones the Needs you
// list shows: every open review group, every automatic group of review units
// (needsgroups.go, one card however many members), every other unit in
// review and every open decision.
//
// A card holds the seen-mark keys of what it shows, each with the time it
// entered its current state:
//   rg:<RG-n>  review group  opened_at (a resubmission opens it again)
//   u:<uid>    unit          review: when its open round began (stateOf);
//                            decision: when it was created
// A card counts as new for a person while one of its items entered that
// state after the person last looked (NewCards).  An automatic group is new
// when any member is: a unit joining it is something new to look at.
//
// The function only reads the database (any read connection), so the server
// can run it on a short-lived read-only connection for a project that is not
// open.

import (
	"context"
	"strings"
	"time"

	"arkstudio/go/internal/sqlitedb"
)

// NeedsMark is one seen-mark key of a card and when that item entered its
// current state (MarkLayout; "" when unknown).  By is who put it in that
// state (the review round's opener or the group's author; "" unknown) and
// Assignee the unit's own assignee, for the notification feed
// (server/notifications.go), which leaves out what the person did themselves
// and what is assigned to someone else.
type NeedsMark struct {
	Key, Since   string
	By, Assignee string
}

// NeedsCard is one Needs you card.  Kind is "review_group", "auto_group",
// "review" or "decision"; Ref the group (RG-n) or unit uid it opens (an
// automatic group's primary unit), RefType that unit's type and Title what
// the card is called.
type NeedsCard struct {
	Items              []NeedsMark
	Kind, Ref, RefType string
	Title              string
}

// NeedsCards lists the Needs you cards of the database, groups first.
func NeedsCards(ctx context.Context, q sqlitedb.Querier) ([]NeedsCard, error) {
	var cards []NeedsCard
	if has, err := hasReviewTables(ctx, q); err != nil {
		return nil, err
	} else if has {
		groups, err := sqlitedb.Query(ctx, q, "SELECT id, title, created_by, opened_at FROM review_groups WHERE status='review' ORDER BY opened_at DESC, id DESC")
		if err != nil {
			return nil, err
		}
		for _, g := range groups {
			ref := groupRef(g.Int("id"))
			cards = append(cards, NeedsCard{Kind: "review_group", Ref: ref, Title: g.Str("title"),
				Items: []NeedsMark{{Key: "rg:" + ref, Since: markTime(g.Get("opened_at")), By: g.Str("created_by")}}})
		}
	}
	rows, err := sqlitedb.Query(ctx, q, "SELECT id, uid, type, status, title, body, assignee, created_at, updated_at FROM units WHERE "+
		needsYouWhere+needsYouOrder)
	if err != nil || len(rows) == 0 {
		return cards, err
	}
	var reviewIDs []int64
	for _, r := range rows {
		if r.Str("status") == "review" {
			reviewIDs = append(reviewIDs, r.Int("id"))
		}
	}
	idArgs := sqlitedb.Args(reviewIDs)
	rounds, err := reviewRoundsFor(ctx, q, idArgs)
	if err != nil {
		return nil, err
	}
	legacy, err := legacySince(ctx, q, idArgs)
	if err != nil {
		return nil, err
	}
	parents, links, err := needsRelations(ctx, q, rows)
	if err != nil {
		return nil, err
	}
	cands := make([]*needsCandidate, len(rows))
	marks := make([]NeedsMark, len(rows))
	for i, r := range rows {
		id := r.Int("id")
		since := r.Get("created_at")
		by := ""
		if r.Str("status") == "review" {
			st := stateOf(r, rounds[id], legacy[id])
			since = st.since
			by, _ = st.openedBy.(string)
		}
		cands[i] = needsCandidateOf(r, since, deliveryOf(r.Str("title"), r.Str("body")), parents[id], links[id])
		assignee, _ := r.Get("assignee").(string)
		marks[i] = NeedsMark{Key: "u:" + r.Str("uid"), Since: markTime(since), By: by, Assignee: assignee}
	}
	grouped := needsAutoGroups(cands)
	at := map[any]int{} // card object -> index in cards
	for i := range rows {
		if card, ok := grouped[i]; ok && cands[i].kind == "review" {
			if j, seen := at[card]; seen {
				cards[j].Items = append(cards[j].Items, marks[i])
				continue
			}
			at[card] = len(cards)
			primary, _ := card.Value("primary").(string)
			title, _ := card.Value("title").(string)
			refType := rows[i].Str("type")
			for k := range rows {
				if rows[k].Str("uid") == primary {
					refType = rows[k].Str("type")
				}
			}
			cards = append(cards, NeedsCard{Kind: "auto_group", Ref: primary, RefType: refType, Title: title,
				Items: []NeedsMark{marks[i]}})
			continue
		}
		kind := "review"
		if cands[i].kind == "decision" {
			kind = "decision"
		}
		cards = append(cards, NeedsCard{Kind: kind, Ref: rows[i].Str("uid"), RefType: rows[i].Str("type"),
			Title: rows[i].Str("title"), Items: []NeedsMark{marks[i]}})
	}
	return cards, nil
}

// MarkLayout is the form seen marks are kept in: UTC with a fixed number of
// digits, so the database can compare them as text.
const MarkLayout = "2006-01-02T15:04:05.000000Z"

// MarkTimeOf is t in MarkLayout.
func MarkTimeOf(t time.Time) string { return t.UTC().Format(MarkLayout) }

// markTime is a stored timestamp in MarkLayout ("" when unknown).
func markTime(v any) string {
	t := parseNeedsTime(v)
	if t.IsZero() {
		return ""
	}
	return MarkTimeOf(t)
}

// NeedsSeen is what one person has looked at in one project: Watermark is
// when they last opened the Needs you list, to the second (everything that
// entered review before is seen), Items the items opened one by one since then (key ->
// the item's since at that time).
type NeedsSeen struct {
	Watermark string
	Items     map[string]string
	// Viewer (Go only): who looks, and whether items assigned to someone
	// else count for them (prefs.highlight_others_reviews).  With a User
	// and Others false, another person's items count as seen.
	User   string
	Others bool
}

// Pending: the item counts as new for the viewer (not opened, and theirs
// unless they asked to see everyone's).
func (s NeedsSeen) Pending(m NeedsMark) bool {
	if !s.Others && s.User != "" && m.Assignee != "" && !strings.EqualFold(m.Assignee, s.User) {
		return false
	}
	return !s.Opened(m)
}

// itemNew: the item entered its current state after the person last saw it.
func (s NeedsSeen) itemNew(m NeedsMark) bool {
	since := parseNeedsTime(m.Since)
	if since.IsZero() {
		// Unknown time: new until the list or the item was opened once.
		return s.Watermark == "" && s.Items[m.Key] == ""
	}
	// Stored times have whole seconds: an item that entered review in the
	// second the list was opened may not have been on it, so it stays new
	// unless it was marked one by one (the list sends what it showed).
	if w := parseNeedsTime(s.Watermark); !w.IsZero() && since.Before(w) {
		return false
	}
	if seen, ok := s.Items[m.Key]; ok {
		if t := parseNeedsTime(seen); seen == "" || !since.After(t) {
			return false
		}
	}
	return true
}

// ItemNew: the item entered its current state after the person last saw it
// (the notification feed lists exactly these).
func (s NeedsSeen) ItemNew(m NeedsMark) bool { return s.itemNew(m) }

// ParseTime reads a stored timestamp (zero time when unknown).
func ParseTime(v any) time.Time { return parseNeedsTime(v) }

// OpenedPrefix marks an item the person opened in the node details
// (inspector): "o:u:T-12" -> the item's since when opened.  Only opening
// counts as having seen it (the user, 2026-11-26: "看过：在属性栏点开过");
// showing the list does not.  A new review round (a later since) makes the
// item unseen again.
const OpenedPrefix = "o:"

// Opened reports whether the person opened the item in its current state.
func (s NeedsSeen) Opened(m NeedsMark) bool {
	v, ok := s.Items[OpenedPrefix+m.Key]
	if !ok {
		return false
	}
	since := parseNeedsTime(m.Since)
	if since.IsZero() || v == "" {
		return true
	}
	t := parseNeedsTime(v)
	return !t.IsZero() && !since.After(t)
}

// NewKeys lists the item keys of the cards the person has not opened yet.
func NewKeys(cards []NeedsCard, seen NeedsSeen) []string {
	out := []string{}
	for _, c := range cards {
		for _, m := range c.Items {
			if seen.Pending(m) {
				out = append(out, m.Key)
			}
		}
	}
	return out
}

// NewCards counts the cards with something new for the person, and all cards.
func NewCards(cards []NeedsCard, seen NeedsSeen) (fresh, total int) {
	for _, c := range cards {
		total++
		// New: an item the person has not opened yet (Opened).
		for _, m := range c.Items {
			if seen.Pending(m) {
				fresh++
				break
			}
		}
	}
	return fresh, total
}

// CardMarks is the current since of every item key of the cards (for
// recording what an opened item looked like).
func CardMarks(cards []NeedsCard) map[string]string {
	out := map[string]string{}
	for _, c := range cards {
		for _, m := range c.Items {
			out[m.Key] = m.Since
		}
	}
	return out
}

// ValidNeedsKey: a seen-mark key as NeedsCards and AssignedCards write them.
func ValidNeedsKey(key string) bool {
	if len(key) > 120 {
		return false
	}
	return ((strings.HasPrefix(key, "u:") || strings.HasPrefix(key, "a:")) && len(key) > 2) || (strings.HasPrefix(key, "rg:"+ReviewGroupPrefix) && len(key) > 3+len(ReviewGroupPrefix))
}
