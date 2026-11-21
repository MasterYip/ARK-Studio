package store

import (
	"sort"
	"strings"
	"time"

	"arkstudio/go/internal/compat/pyjson"
)

// Automatic grouping of Needs you review items (one card for a delivery that
// went to review one unit at a time; agents should submit a review group
// instead, reviewgroups.go).  Computed here so every client (web, Android)
// shows the same cards; each grouped item carries the card as "auto_group".
//
// Two review items belong to one card when
//   - their bodies name the same delivery (branch, or commit when no branch
//     is named) and they are linked, share a parent or entered review within
//     needsDeliverySpan of each other, or
//   - no shared delivery is known, they are linked to each other or share a
//     parent (part_of / derived_from the same unit), and they entered review
//     within needsGroupWindow of each other.
//
// Two items naming different deliveries are never grouped.  Cards are the
// connected sets of such pairs with at least two members; decisions and
// review groups stay on their own.
const (
	needsGroupWindow  = 30 * time.Minute
	needsDeliverySpan = 24 * time.Hour
)

type needsRef struct{ uid, title string }

type needsLink struct{ uid, relation, dir string }

// needsCandidate is what the grouping reads of one Needs you item.
type needsCandidate struct {
	uid, typ, kind, title string
	since                 time.Time
	branch, commit, label string
	parents               []needsRef
	links                 []needsLink
}

func (c *needsCandidate) deliveryKey() string {
	if c.branch != "" {
		return "branch:" + c.branch
	}
	if c.commit != "" {
		return "commit:" + c.commit
	}
	return ""
}

type needsReason struct {
	a, b     string
	link     *pyjson.Object
	parents  []string
	delivery string
	minutes  int64
}

// needsPrimaryRank: the task the person asked for first, then a result.
var needsPrimaryRank = map[string]int{"task": 0, "experiment": 1, "artifact": 1, "result": 1, "run": 1, "claim": 2, "evidence": 3}

func primaryRank(typ string) int {
	if r, ok := needsPrimaryRank[typ]; ok {
		return r
	}
	return 4
}

func parentSet(c *needsCandidate) map[string]bool {
	out := map[string]bool{}
	for _, p := range c.parents {
		if p.uid != "" {
			out[p.uid] = true
		}
	}
	return out
}

// needsGroupReason says why a and b belong together (nil = they do not).
func needsGroupReason(a, b *needsCandidate) *needsReason {
	da, db := a.deliveryKey(), b.deliveryKey()
	if da != "" && db != "" && da != db {
		return nil
	}
	sameDelivery := da != "" && da == db
	gap := a.since.Sub(b.since)
	if gap < 0 {
		gap = -gap
	}
	var link *pyjson.Object
	for _, l := range a.links {
		if l.uid == b.uid {
			from, to := a.uid, b.uid
			if l.dir == "in" {
				from, to = b.uid, a.uid
			}
			link = pyjson.NewObject("from", from, "to", to, "relation", l.relation)
			break
		}
	}
	pa := parentSet(a)
	var shared []string
	for uid := range parentSet(b) {
		if pa[uid] {
			shared = append(shared, uid)
		}
	}
	sort.Strings(shared)
	if sameDelivery {
		if link == nil && len(shared) == 0 && gap > needsDeliverySpan {
			return nil
		}
	} else if gap > needsGroupWindow || (link == nil && len(shared) == 0) {
		return nil
	}
	delivery := ""
	if sameDelivery {
		delivery = da
	}
	return &needsReason{a: a.uid, b: b.uid, link: link, parents: shared, delivery: delivery,
		minutes: int64(gap/time.Minute + boolInt(gap%time.Minute >= 30*time.Second))}
}

func boolInt(b bool) time.Duration {
	if b {
		return 1
	}
	return 0
}

// needsShortTitle: "HOI-215 · Large sandbox ..." -> "HOI-215", else the title.
func needsShortTitle(title string) string {
	text := strings.TrimSpace(title)
	if text == "" {
		return ""
	}
	head := strings.TrimSpace(strings.SplitN(text, " · ", 2)[0])
	if n := len([]rune(head)); n >= 2 && len(head) < len(text) {
		return head
	}
	return text
}

// mostShared is the value most members name (ties: smallest), when at least
// half of the n members name it.
func mostShared(counts map[string]int, n int) string {
	best, bestN := "", 0
	for v, c := range counts {
		if c > bestN || (c == bestN && v < best) {
			best, bestN = v, c
		}
	}
	if best == "" || bestN*2 < n {
		return ""
	}
	return best
}

// needsAutoGroups returns, by candidate index, the card of every grouped
// candidate (the same object for all members of a card).
func needsAutoGroups(cands []*needsCandidate) map[int]*pyjson.Object {
	var idx []int
	for i, c := range cands {
		if c.uid != "" && c.kind == "review" {
			idx = append(idx, i)
		}
	}
	parent := map[int]int{}
	for _, i := range idx {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			i = parent[i]
		}
		return i
	}
	var reasons []*needsReason
	for x := 0; x < len(idx); x++ {
		for y := x + 1; y < len(idx); y++ {
			r := needsGroupReason(cands[idx[x]], cands[idx[y]])
			if r == nil {
				continue
			}
			reasons = append(reasons, r)
			rx, ry := find(idx[x]), find(idx[y])
			if rx != ry {
				hi, lo := rx, ry
				if lo > hi {
					hi, lo = lo, hi
				}
				parent[hi] = lo
			}
		}
	}
	byRoot := map[int][]int{}
	var roots []int
	for _, i := range idx {
		r := find(i)
		if _, ok := byRoot[r]; !ok {
			roots = append(roots, r)
		}
		byRoot[r] = append(byRoot[r], i)
	}
	out := map[int]*pyjson.Object{}
	for _, root := range roots {
		indexes := byRoot[root]
		if len(indexes) < 2 {
			continue
		}
		members := make([]*needsCandidate, len(indexes))
		uids := map[string]bool{}
		for k, i := range indexes {
			members[k] = cands[i]
			uids[cands[i].uid] = true
		}
		childOf := func(m *needsCandidate) int {
			for _, p := range m.parents {
				if uids[p.uid] {
					return 1
				}
			}
			return 0
		}
		ranked := append([]*needsCandidate(nil), members...)
		sort.SliceStable(ranked, func(i, j int) bool {
			a, b := ranked[i], ranked[j]
			if ra, rb := primaryRank(a.typ), primaryRank(b.typ); ra != rb {
				return ra < rb
			}
			if ca, cb := childOf(a), childOf(b); ca != cb {
				return ca < cb
			}
			if !a.since.Equal(b.since) {
				return a.since.Before(b.since)
			}
			return a.uid < b.uid
		})
		primary := ranked[0]
		ordered := []any{primary.uid}
		for _, m := range members {
			if m != primary {
				ordered = append(ordered, m.uid)
			}
		}
		// The parent most members share (at least half of them), outside the group.
		parentCount := map[string]int{}
		parentRef := map[string]needsRef{}
		for _, m := range members {
			seen := map[string]bool{}
			for _, p := range m.parents {
				if p.uid == "" || uids[p.uid] || seen[p.uid] {
					continue
				}
				seen[p.uid] = true
				parentCount[p.uid]++
				if _, ok := parentRef[p.uid]; !ok {
					parentRef[p.uid] = p
				}
			}
		}
		var parentObj any
		parentTitle := ""
		if uid := mostShared(parentCount, len(members)); uid != "" {
			ref := parentRef[uid]
			parentObj = pyjson.NewObject("uid", ref.uid, "title", ref.title)
			parentTitle = ref.title
		}
		labels := map[string]int{}
		keys := map[string]bool{}
		minT, maxT := members[0].since, members[0].since
		for _, m := range members {
			if m.label != "" {
				labels[m.label]++
			}
			keys[m.deliveryKey()] = true
			if m.since.Before(minT) {
				minT = m.since
			}
			if m.since.After(maxT) {
				maxT = m.since
			}
		}
		delivery := ""
		if len(keys) == 1 && !keys[""] {
			delivery = members[0].deliveryKey()
		}
		title := mostShared(labels, len(members))
		if title == "" {
			title = needsShortTitle(parentTitle)
		}
		if title == "" {
			title = primary.title
		}
		if title == "" {
			title = primary.uid
		}
		sortedUIDs := make([]string, 0, len(uids))
		for uid := range uids {
			sortedUIDs = append(sortedUIDs, uid)
		}
		sort.Strings(sortedUIDs)
		groupReasons := []any{}
		for _, r := range reasons {
			if !uids[r.a] || !uids[r.b] {
				continue
			}
			parents := make([]any, len(r.parents))
			for k, p := range r.parents {
				parents[k] = p
			}
			var link any
			if r.link != nil {
				link = r.link
			}
			groupReasons = append(groupReasons, pyjson.NewObject("a", r.a, "b", r.b, "link", link,
				"parents", parents, "delivery", r.delivery, "minutes", r.minutes))
		}
		span := maxT.Sub(minT)
		card := pyjson.NewObject("key", "group:"+strings.Join(sortedUIDs, ","), "primary", primary.uid,
			"members", ordered, "parent", parentObj, "delivery", delivery, "title", title,
			"span", int64((span+30*time.Second)/time.Minute), "reasons", groupReasons)
		for _, i := range indexes {
			out[i] = card
		}
	}
	return out
}

// parseNeedsTime reads a stored timestamp (zero time when unknown).
func parseNeedsTime(v any) time.Time {
	s, _ := v.(string)
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
