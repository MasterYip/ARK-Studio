package pydiff

import (
	"strconv"
	"strings"
)

// GroupedOpcodes is SequenceMatcher.get_grouped_opcodes(n).
func GroupedOpcodes[T comparable](a, b []T, n int) [][]Opcode {
	codes := Opcodes(a, b)
	if len(codes) == 0 {
		codes = []Opcode{{"equal", 0, 1, 0, 1}}
	}
	if c := codes[0]; c.Tag == "equal" {
		codes[0] = Opcode{c.Tag, max(c.I1, c.I2-n), c.I2, max(c.J1, c.J2-n), c.J2}
	}
	if c := codes[len(codes)-1]; c.Tag == "equal" {
		codes[len(codes)-1] = Opcode{c.Tag, c.I1, min(c.I2, c.I1+n), c.J1, min(c.J2, c.J1+n)}
	}
	nn := n + n
	var out [][]Opcode
	var group []Opcode
	for _, c := range codes {
		i1, j1 := c.I1, c.J1
		if c.Tag == "equal" && c.I2-c.I1 > nn {
			group = append(group, Opcode{c.Tag, i1, min(c.I2, i1+n), j1, min(c.J2, j1+n)})
			out = append(out, group)
			group = nil
			i1, j1 = max(i1, c.I2-n), max(j1, c.J2-n)
		}
		group = append(group, Opcode{c.Tag, i1, c.I2, j1, c.J2})
	}
	if len(group) > 0 && !(len(group) == 1 && group[0].Tag == "equal") {
		out = append(out, group)
	}
	return out
}

func formatRangeUnified(start, stop int) string {
	beginning := start + 1
	length := stop - start
	if length == 1 {
		return strconv.Itoa(beginning)
	}
	if length == 0 {
		beginning--
	}
	return strconv.Itoa(beginning) + "," + strconv.Itoa(length)
}

// UnifiedDiff is "".join(difflib.unified_diff(a, b, fromfile, tofile, n=n))
// for lines that keep their line endings.
func UnifiedDiff(a, b []string, fromfile, tofile string, n int) string {
	var sb strings.Builder
	for k, group := range GroupedOpcodes(a, b, n) {
		if k == 0 {
			sb.WriteString("--- " + fromfile + "\n+++ " + tofile + "\n")
		}
		first, last := group[0], group[len(group)-1]
		sb.WriteString("@@ -" + formatRangeUnified(first.I1, last.I2) + " +" + formatRangeUnified(first.J1, last.J2) + " @@\n")
		for _, c := range group {
			if c.Tag == "equal" {
				for _, line := range a[c.I1:c.I2] {
					sb.WriteString(" " + line)
				}
				continue
			}
			if c.Tag == "replace" || c.Tag == "delete" {
				for _, line := range a[c.I1:c.I2] {
					sb.WriteString("-" + line)
				}
			}
			if c.Tag == "replace" || c.Tag == "insert" {
				for _, line := range b[c.J1:c.J2] {
					sb.WriteString("+" + line)
				}
			}
		}
	}
	return sb.String()
}
