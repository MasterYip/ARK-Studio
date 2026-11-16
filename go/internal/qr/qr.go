// Package qr ports arkstudio/qr.py: a dependency-free QR Code encoder (byte
// mode, versions 1-10, ECC level M) that renders an SVG string.
package qr

import (
	"errors"
	"html"
	"strconv"
	"strings"
)

var blocksM = map[int]struct {
	lengths []int
	ec      int
}{
	1: {[]int{16}, 10}, 2: {[]int{28}, 16}, 3: {[]int{44}, 26}, 4: {[]int{32, 32}, 18},
	5: {[]int{43, 43}, 24}, 6: {[]int{27, 27, 27, 27}, 16}, 7: {[]int{31, 31, 31, 31}, 18},
	8: {[]int{38, 38, 39, 39}, 22}, 9: {[]int{36, 36, 36, 37, 37}, 22}, 10: {[]int{43, 43, 43, 43, 44}, 26},
}

var alignment = map[int][]int{1: {}, 2: {6, 18}, 3: {6, 22}, 4: {6, 26}, 5: {6, 30}, 6: {6, 34},
	7: {6, 22, 38}, 8: {6, 24, 42}, 9: {6, 26, 46}, 10: {6, 28, 50}}

const (
	formatMask  = 0x5412
	formatPoly  = 0x537
	versionPoly = 0x1F25
)

var gfExp [512]int
var gfLog [256]int

func init() {
	v := 1
	for i := 0; i < 255; i++ {
		gfExp[i] = v
		gfLog[v] = i
		v <<= 1
		if v&0x100 != 0 {
			v ^= 0x11D
		}
	}
	for i := 255; i < 512; i++ {
		gfExp[i] = gfExp[i-255]
	}
}

func gfMul(a, b int) int {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[gfLog[a]+gfLog[b]]
}

func rsGenerator(degree int) []int {
	poly := []int{1}
	for i := 0; i < degree; i++ {
		factor := gfExp[i]
		result := make([]int, len(poly)+1)
		for p, c := range poly {
			result[p] ^= c
			result[p+1] ^= gfMul(c, factor)
		}
		poly = result
	}
	return poly
}

func rsRemainder(data []int, degree int) []int {
	gen := rsGenerator(degree)
	rem := append(append([]int{}, data...), make([]int, degree)...)
	for i := range data {
		factor := rem[i]
		if factor == 0 {
			continue
		}
		for off, c := range gen {
			rem[i+off] ^= gfMul(c, factor)
		}
	}
	return rem[len(rem)-degree:]
}

func sum(xs []int) int {
	t := 0
	for _, x := range xs {
		t += x
	}
	return t
}

func capacity(version int) int {
	countBits := 8
	if version > 9 {
		countBits = 16
	}
	return (sum(blocksM[version].lengths)*8 - 4 - countBits) / 8
}

func encodeData(payload []byte, version int) []int {
	var bits []int
	appendBits := func(value, length int) {
		for s := length - 1; s >= 0; s-- {
			bits = append(bits, (value>>s)&1)
		}
	}
	appendBits(0b0100, 4)
	countBits := 8
	if version > 9 {
		countBits = 16
	}
	appendBits(len(payload), countBits)
	for _, b := range payload {
		appendBits(int(b), 8)
	}
	dataCodewords := sum(blocksM[version].lengths)
	capBits := dataCodewords * 8
	appendBits(0, min(4, capBits-len(bits)))
	for len(bits)%8 != 0 {
		bits = append(bits, 0)
	}
	var codewords []int
	for i := 0; i < len(bits); i += 8 {
		v := 0
		for _, b := range bits[i : i+8] {
			v = v<<1 | b
		}
		codewords = append(codewords, v)
	}
	pads := []int{0xEC, 0x11}
	for p := 0; len(codewords) < dataCodewords; p++ {
		codewords = append(codewords, pads[p%2])
	}
	return codewords
}

func interleave(codewords []int, version int) []int {
	spec := blocksM[version]
	var blocks [][]int
	off := 0
	maxLen := 0
	for _, l := range spec.lengths {
		blocks = append(blocks, codewords[off:off+l])
		off += l
		maxLen = max(maxLen, l)
	}
	var ecBlocks [][]int
	for _, b := range blocks {
		ecBlocks = append(ecBlocks, rsRemainder(b, spec.ec))
	}
	var out []int
	for i := 0; i < maxLen; i++ {
		for _, b := range blocks {
			if i < len(b) {
				out = append(out, b[i])
			}
		}
	}
	for i := 0; i < spec.ec; i++ {
		for _, b := range ecBlocks {
			out = append(out, b[i])
		}
	}
	return out
}

func bitLength(v int) int {
	n := 0
	for v > 0 {
		n++
		v >>= 1
	}
	return n
}

func bchRemainder(value, poly int) int {
	for bitLength(value) >= bitLength(poly) {
		value ^= poly << (bitLength(value) - bitLength(poly))
	}
	return value
}

func formatBits(mask int) int {
	data := mask // ECC M = 00
	return ((data << 10) | bchRemainder(data<<10, formatPoly)) ^ formatMask
}

func versionBits(version int) int {
	return (version << 12) | bchRemainder(version<<12, versionPoly)
}

type matrix struct {
	version, size int
	modules, fn   [][]bool
}

func newMatrix(version int) *matrix {
	size := version*4 + 17
	m := &matrix{version: version, size: size}
	for i := 0; i < size; i++ {
		m.modules = append(m.modules, make([]bool, size))
		m.fn = append(m.fn, make([]bool, size))
	}
	return m
}

func (m *matrix) set(x, y int, dark bool) {
	if x >= 0 && x < m.size && y >= 0 && y < m.size {
		m.modules[y][x] = dark
		m.fn[y][x] = true
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func (m *matrix) drawFinder(cx, cy int) {
	for dy := -1; dy < 8; dy++ {
		for dx := -1; dx < 8; dx++ {
			x, y := cx+dx, cy+dy
			if !(x >= 0 && x < m.size && y >= 0 && y < m.size) {
				continue
			}
			inside := dx >= 0 && dx <= 6 && dy >= 0 && dy <= 6
			dark := inside && (dx == 0 || dx == 6 || dy == 0 || dy == 6 || (dx >= 2 && dx <= 4 && dy >= 2 && dy <= 4))
			m.set(x, y, dark)
		}
	}
}

func (m *matrix) drawAlignment(cx, cy int) {
	for dy := -2; dy < 3; dy++ {
		for dx := -2; dx < 3; dx++ {
			m.set(cx+dx, cy+dy, max(abs(dx), abs(dy)) != 1)
		}
	}
}

func (m *matrix) drawPatterns() {
	size := m.size
	m.drawFinder(0, 0)
	m.drawFinder(size-7, 0)
	m.drawFinder(0, size-7)
	for i := 8; i < size-8; i++ {
		dark := i%2 == 0
		m.set(6, i, dark)
		m.set(i, 6, dark)
	}
	centres := alignment[m.version]
	for _, cy := range centres {
		for _, cx := range centres {
			if (cx <= 8 && cy <= 8) || (cx >= size-9 && cy <= 8) || (cx <= 8 && cy >= size-9) {
				continue
			}
			m.drawAlignment(cx, cy)
		}
	}
	for i := 0; i < 9; i++ {
		if i != 6 {
			m.set(8, i, false)
			m.set(i, 8, false)
		}
	}
	for i := 0; i < 8; i++ {
		m.set(size-1-i, 8, false)
		m.set(8, size-1-i, false)
	}
	m.set(8, size-8, true)
	if m.version >= 7 {
		for i := 0; i < 18; i++ {
			a, b := size-11+i%3, i/3
			m.set(a, b, false)
			m.set(b, a, false)
		}
	}
}

func (m *matrix) placeData(bits []int) {
	size := m.size
	idx := 0
	for right := size - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		upward := ((right + 1) & 2) == 0
		for v := 0; v < size; v++ {
			y := v
			if upward {
				y = size - 1 - v
			}
			for off := 0; off < 2; off++ {
				x := right - off
				if m.fn[y][x] {
					continue
				}
				m.modules[y][x] = idx < len(bits) && bits[idx] == 1
				idx++
			}
		}
	}
}

func maskBit(mask, x, y int) bool {
	switch mask {
	case 0:
		return (x+y)%2 == 0
	case 1:
		return y%2 == 0
	case 2:
		return x%3 == 0
	case 3:
		return (x+y)%3 == 0
	case 4:
		return (x/3+y/2)%2 == 0
	case 5:
		return (x*y)%2+(x*y)%3 == 0
	case 6:
		return ((x*y)%2+(x*y)%3)%2 == 0
	}
	return ((x+y)%2+(x*y)%3)%2 == 0
}

func (m *matrix) applyMask(mask int) {
	for y := 0; y < m.size; y++ {
		for x := 0; x < m.size; x++ {
			if !m.fn[y][x] && maskBit(mask, x, y) {
				m.modules[y][x] = !m.modules[y][x]
			}
		}
	}
}

func (m *matrix) drawFormat(mask int) {
	size := m.size
	bits := formatBits(mask)
	for i := 0; i < 15; i++ {
		dark := (bits>>i)&1 == 1
		switch {
		case i < 6:
			m.set(8, i, dark)
		case i < 8:
			m.set(8, i+1, dark)
		default:
			m.set(14-i, 8, dark)
		}
		if i < 8 {
			m.set(size-1-i, 8, dark)
		} else {
			m.set(8, size-15+i, dark)
		}
	}
	m.set(8, size-8, true)
}

func (m *matrix) drawVersion() {
	if m.version < 7 {
		return
	}
	bits := versionBits(m.version)
	for i := 0; i < 18; i++ {
		dark := (bits>>i)&1 == 1
		a, b := m.size-11+i%3, i/3
		m.set(a, b, dark)
		m.set(b, a, dark)
	}
}

func (m *matrix) penalty() int {
	size := m.size
	score := 0
	cols := make([][]bool, size)
	for x := 0; x < size; x++ {
		cols[x] = make([]bool, size)
		for y := 0; y < size; y++ {
			cols[x][y] = m.modules[y][x]
		}
	}
	for _, lines := range [][][]bool{m.modules, cols} {
		for _, row := range lines {
			colour, run := row[0], 1
			for _, v := range row[1:] {
				if v == colour {
					run++
				} else {
					if run >= 5 {
						score += 3 + (run - 5)
					}
					colour, run = v, 1
				}
			}
			if run >= 5 {
				score += 3 + (run - 5)
			}
		}
	}
	for y := 0; y < size-1; y++ {
		for x := 0; x < size-1; x++ {
			v := m.modules[y][x]
			if m.modules[y][x+1] == v && m.modules[y+1][x] == v && m.modules[y+1][x+1] == v {
				score += 3
			}
		}
	}
	pattern := []bool{true, false, true, true, true, false, true}
	isLight := func(xs []bool) bool {
		if len(xs) != 4 {
			return false
		}
		for _, x := range xs {
			if x {
				return false
			}
		}
		return true
	}
	for _, lines := range [][][]bool{m.modules, cols} {
		for _, row := range lines {
			for start := 0; start < size-6; start++ {
				match := true
				for i, p := range pattern {
					if row[start+i] != p {
						match = false
						break
					}
				}
				if !match {
					continue
				}
				if isLight(row[max(0, start-4):start]) {
					score += 40
				}
				if isLight(row[start+7 : min(size, start+11)]) {
					score += 40
				}
			}
		}
	}
	dark := 0
	for _, row := range m.modules {
		for _, v := range row {
			if v {
				dark++
			}
		}
	}
	percent := dark * 100 / (size * size)
	score += 10 * (abs(percent-50) / 5)
	return score
}

// Encode is qr.encode: the module matrix of a non-empty UTF-8 payload.
func Encode(data string) ([][]bool, error) {
	if data == "" {
		return nil, errors.New("QR payload must be a non-empty string")
	}
	payload := []byte(data)
	version := 0
	for v := 1; v <= 10; v++ {
		if len(payload) <= capacity(v) {
			version = v
			break
		}
	}
	if version == 0 {
		return nil, errors.New("payload is too large for a version 1-10 QR code at ECC M")
	}
	codewords := interleave(encodeData(payload, version), version)
	var bits []int
	for _, cw := range codewords {
		for s := 7; s >= 0; s-- {
			bits = append(bits, (cw>>s)&1)
		}
	}
	var best *matrix
	bestScore := 0
	for mask := 0; mask < 8; mask++ {
		m := newMatrix(version)
		m.drawPatterns()
		m.placeData(bits)
		m.applyMask(mask)
		m.drawFormat(mask)
		m.drawVersion()
		if s := m.penalty(); best == nil || s < bestScore {
			best, bestScore = m, s
		}
	}
	return best.modules, nil
}

// SVG is qr.to_svg(data) with the default border, colours and title.
func SVG(data string) (string, error) {
	modules, err := Encode(data)
	if err != nil {
		return "", err
	}
	const border = 4
	title := html.EscapeString("ARK mobile pairing QR code")
	size := len(modules)
	dim := strconv.Itoa(size + border*2)
	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ` + dim + ` ` + dim + `" width="` + dim +
		`" height="` + dim + `" role="img" aria-label="` + title + `" shape-rendering="crispEdges">`)
	b.WriteString("<title>" + title + "</title>")
	b.WriteString(`<rect width="` + dim + `" height="` + dim + `" fill="#ffffff"/>`)
	for y, row := range modules {
		for x := 0; x < size; {
			if !row[x] {
				x++
				continue
			}
			start := x
			for x < size && row[x] {
				x++
			}
			b.WriteString(`<path d="M` + strconv.Itoa(start+border) + ` ` + strconv.Itoa(y+border) + `h` + strconv.Itoa(x-start) +
				`v1h` + strconv.Itoa(start-x) + `z" fill="#0f141c"/>`)
		}
	}
	b.WriteString("</svg>")
	return b.String(), nil
}
