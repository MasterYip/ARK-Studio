// Package pydiff ports difflib.SequenceMatcher (isjunk=None, autojunk=True)
// over sequences of comparable items, as store.flush_node_versions uses it on
// Python strings (sequences of code points).
package pydiff

// Opcode is one get_opcodes() entry.
type Opcode struct {
	Tag    string
	I1, I2 int
	J1, J2 int
}

type match struct{ a, b, size int }

// Opcodes is SequenceMatcher(None, a, b).get_opcodes().
func Opcodes[T comparable](a, b []T) []Opcode {
	m := newMatcher(a, b)
	return m.opcodes()
}

type matcher[T comparable] struct {
	a, b []T
	b2j  map[T][]int
}

func newMatcher[T comparable](a, b []T) *matcher[T] {
	m := &matcher[T]{a: a, b: b, b2j: map[T][]int{}}
	for i, elt := range b {
		m.b2j[elt] = append(m.b2j[elt], i)
	}
	// autojunk: purge popular elements when len(b) >= 200.
	n := len(b)
	if n >= 200 {
		ntest := n/100 + 1
		for elt, idxs := range m.b2j {
			if len(idxs) > ntest {
				delete(m.b2j, elt)
			}
		}
	}
	return m
}

// findLongestMatch is SequenceMatcher.find_longest_match without junk
// (bjunk is empty for isjunk=None; popular elements are simply absent).
func (m *matcher[T]) findLongestMatch(alo, ahi, blo, bhi int) match {
	a, b := m.a, m.b
	besti, bestj, bestsize := alo, blo, 0
	j2len := map[int]int{}
	for i := alo; i < ahi; i++ {
		newj2len := map[int]int{}
		for _, j := range m.b2j[a[i]] {
			if j < blo {
				continue
			}
			if j >= bhi {
				break
			}
			k := j2len[j-1] + 1
			newj2len[j] = k
			if k > bestsize {
				besti, bestj, bestsize = i-k+1, j-k+1, k
			}
		}
		j2len = newj2len
	}
	// bjunk is empty for isjunk=None (popular elements are not junk), so the
	// match extends over any equal neighbours.
	for besti > alo && bestj > blo && a[besti-1] == b[bestj-1] {
		besti, bestj, bestsize = besti-1, bestj-1, bestsize+1
	}
	for besti+bestsize < ahi && bestj+bestsize < bhi && a[besti+bestsize] == b[bestj+bestsize] {
		bestsize++
	}
	return match{besti, bestj, bestsize}
}

func (m *matcher[T]) matchingBlocks() []match {
	la, lb := len(m.a), len(m.b)
	type span struct{ alo, ahi, blo, bhi int }
	queue := []span{{0, la, 0, lb}}
	var blocks []match
	for len(queue) > 0 {
		s := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		x := m.findLongestMatch(s.alo, s.ahi, s.blo, s.bhi)
		if x.size > 0 {
			blocks = append(blocks, x)
			if s.alo < x.a && s.blo < x.b {
				queue = append(queue, span{s.alo, x.a, s.blo, x.b})
			}
			if x.a+x.size < s.ahi && x.b+x.size < s.bhi {
				queue = append(queue, span{x.a + x.size, s.ahi, x.b + x.size, s.bhi})
			}
		}
	}
	sortMatches(blocks)
	// Collapse adjacent blocks.
	var out []match
	i1, j1, k1 := 0, 0, 0
	for _, b := range blocks {
		if i1+k1 == b.a && j1+k1 == b.b {
			k1 += b.size
		} else {
			if k1 > 0 {
				out = append(out, match{i1, j1, k1})
			}
			i1, j1, k1 = b.a, b.b, b.size
		}
	}
	if k1 > 0 {
		out = append(out, match{i1, j1, k1})
	}
	return append(out, match{la, lb, 0})
}

func sortMatches(xs []match) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && less(xs[j], xs[j-1]); j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}

func less(x, y match) bool {
	if x.a != y.a {
		return x.a < y.a
	}
	if x.b != y.b {
		return x.b < y.b
	}
	return x.size < y.size
}

func (m *matcher[T]) opcodes() []Opcode {
	i, j := 0, 0
	var out []Opcode
	for _, b := range m.matchingBlocks() {
		tag := ""
		switch {
		case i < b.a && j < b.b:
			tag = "replace"
		case i < b.a:
			tag = "delete"
		case j < b.b:
			tag = "insert"
		}
		if tag != "" {
			out = append(out, Opcode{tag, i, b.a, j, b.b})
		}
		i, j = b.a+b.size, b.b+b.size
		if b.size > 0 {
			out = append(out, Opcode{"equal", b.a, i, b.b, j})
		}
	}
	return out
}
