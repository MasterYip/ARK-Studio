package store

// Split suggestions (Go only; T-0068): the project's branches and the
// groups of branches that are only weakly linked to the rest, so a person
// can pick what to split off.
//
// A unit's parent is the target of its part_of link, else of its
// derived_from link (lowest link id first); a branch is a root and its
// descendants.  While one branch holds more than half of the units its
// root is a hub (the project's main task or storyline): it stays, and its
// children become branches.  Branches are then grouped by greedy modularity
// (Clauset-Newman-Moore) on the links between them, links inside a branch
// counting as self-loops; each group reports its
// size and the links that would cross a split.

import (
	"context"
	"sort"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/sqlitedb"
)

type graphUnit struct {
	id         int64
	uid, title string
	typ        string
}

type graphLink struct{ src, dst int64 }

func subtreeOf(root int64, children map[int64][]int64) []int64 {
	out, stack := []int64{}, []int64{root}
	for len(stack) > 0 {
		x := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		out = append(out, x)
		stack = append(stack, children[x]...)
	}
	return out
}

// SplitClusters is GET /api/project-move/clusters.
func (s *Store) SplitClusters(ctx context.Context) (*pyjson.Object, error) {
	var out *pyjson.Object
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		rows, err := sqlitedb.Query(ctx, q, "SELECT id, uid, title, type FROM units WHERE archived=0 ORDER BY id")
		if err != nil {
			return err
		}
		units := map[int64]graphUnit{}
		order := []int64{}
		for _, r := range rows {
			units[r.Int("id")] = graphUnit{r.Int("id"), r.Str("uid"), r.Str("title"), r.Str("type")}
			order = append(order, r.Int("id"))
		}
		lrows, err := sqlitedb.Query(ctx, q, "SELECT source_id, target_id FROM unit_links ORDER BY id")
		if err != nil {
			return err
		}
		var links []graphLink
		for _, l := range lrows {
			_, a := units[l.Int("source_id")]
			_, b := units[l.Int("target_id")]
			if a && b {
				links = append(links, graphLink{l.Int("source_id"), l.Int("target_id")})
			}
		}
		parent, err := treeParents(ctx, q)
		if err != nil {
			return err
		}
		out = clusterGraph(units, order, links, parent)
		return nil
	})
	return out, err
}

func clusterGraph(units map[int64]graphUnit, order []int64, links []graphLink, parent map[int64]int64) *pyjson.Object {
	children := childrenOf(parent)
	branches := map[int64][]int64{}
	for _, id := range order {
		if _, ok := parent[id]; !ok {
			branches[id] = subtreeOf(id, children)
		}
	}
	hubs := []any{}
	hubSet := map[int64]bool{}
	for {
		big, size := int64(0), 0
		for root, members := range branches {
			if len(members) > size || (len(members) == size && root < big) {
				big, size = root, len(members)
			}
		}
		if size*2 <= len(units) || len(children[big]) == 0 {
			break
		}
		hubSet[big] = true
		hubs = append(hubs, pyjson.NewObject("uid", units[big].uid, "title", units[big].title))
		delete(branches, big)
		for _, c := range children[big] {
			branches[c] = subtreeOf(c, children)
		}
	}
	owner := map[int64]int64{}
	for root, members := range branches {
		for _, m := range members {
			owner[m] = root
		}
	}
	type pair struct{ a, b int64 }
	key := func(a, b int64) pair {
		if a > b {
			a, b = b, a
		}
		return pair{a, b}
	}
	weight := map[pair]int64{}
	hubLinks := map[int64]int64{}
	internal := map[int64]int64{}
	for _, l := range links {
		a, okA := owner[l.src]
		b, okB := owner[l.dst]
		switch {
		case !okA && !okB:
		case !okA:
			hubLinks[b]++
		case !okB:
			hubLinks[a]++
		case a != b:
			weight[key(a, b)]++
		default:
			internal[a]++
		}
	}
	// Greedy modularity merge over branches.
	comm := map[int64][]int64{}
	deg := map[int64]float64{}
	var m float64
	for root := range branches {
		comm[root] = []int64{root}
	}
	cw := map[pair]float64{}
	// Links inside a branch count in its degree (as self-loops), so a dense
	// branch does not swallow a weakly linked one.
	for p, w := range weight {
		cw[p] = float64(w)
		deg[p.a] += float64(w)
		deg[p.b] += float64(w)
		m += float64(w)
	}
	for b, w := range internal {
		deg[b] += 2 * float64(w)
		m += float64(w)
	}
	for m > 0 {
		best, found := 0.0, false
		var bp pair
		for p, w := range cw {
			dq := w/m - deg[p.a]*deg[p.b]/(2*m*m)
			if dq > best+1e-12 || (found && dq > best-1e-12 && dq < best+1e-12 && (p.a < bp.a || (p.a == bp.a && p.b < bp.b))) {
				best, bp, found = dq, p, true
			}
		}
		if !found {
			break
		}
		i, j := bp.a, bp.b
		comm[i] = append(comm[i], comm[j]...)
		delete(comm, j)
		deg[i] += deg[j]
		delete(deg, j)
		next := map[pair]float64{}
		for p, w := range cw {
			a, b := p.a, p.b
			if a == j {
				a = i
			}
			if b == j {
				b = i
			}
			if a != b {
				next[key(a, b)] += w
			}
		}
		cw = next
	}
	in := map[int64]int64{} // branch -> community key
	for k, members := range comm {
		for _, b := range members {
			in[b] = k
		}
	}
	type cluster struct {
		key                   int64
		size, cut, hub, inner int64
		roots                 []int64
	}
	var list []*cluster
	for k, members := range comm {
		c := &cluster{key: k, roots: members}
		for _, b := range members {
			c.size += int64(len(branches[b]))
			c.hub += hubLinks[b]
		}
		list = append(list, c)
	}
	byKey := map[int64]*cluster{}
	for _, c := range list {
		byKey[c.key] = c
	}
	for p, w := range weight {
		if in[p.a] == in[p.b] {
			byKey[in[p.a]].inner += w
		} else {
			byKey[in[p.a]].cut += w
			byKey[in[p.b]].cut += w
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].size != list[j].size {
			return list[i].size > list[j].size
		}
		return list[i].key < list[j].key
	})
	clusters := []any{}
	for _, c := range list {
		sort.Slice(c.roots, func(i, j int) bool {
			if len(branches[c.roots[i]]) != len(branches[c.roots[j]]) {
				return len(branches[c.roots[i]]) > len(branches[c.roots[j]])
			}
			return c.roots[i] < c.roots[j]
		})
		roots := []any{}
		for _, r := range c.roots {
			roots = append(roots, pyjson.NewObject("uid", units[r].uid, "title", units[r].title, "type", units[r].typ,
				"units", int64(len(branches[r]))))
		}
		clusters = append(clusters, pyjson.NewObject("id", "c-"+units[c.roots[0]].uid, "units", c.size, "branches", int64(len(c.roots)), "cut_links", c.cut,
			"hub_links", c.hub, "roots", roots))
	}
	return pyjson.NewObject("units", int64(len(units)), "links", int64(len(links)), "hubs", hubs,
		"branches", int64(len(branches)), "clusters", clusters)
}
