package pytext

import (
	"strings"
)

// Ports of arkstudio/store.py excerpt helpers (same regexes, Python \s/\d).
var (
	mdFenceRe     = MustCompile("^\\s*(?:```|~~~)")
	mdImageRe     = MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	mdLinkRe      = MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	mdPrefixRe    = MustCompile(`^\s{0,3}(?:#{1,6}\s+|>\s?|[-*+]\s+|\d+[.)]\s+|\|)`)
	mdInlineRe    = MustCompile("(?:\\*\\*|__|`|~~)")
	spaceRunRe    = MustCompile(`\s+`)
	mdImageOnlyRe = MustCompile(`^\s*!\[[^\]]*\]\([^)]*\)\s*\z`)
	// NoteLineRe is store._NOTE_LINE (re.M).
	NoteLineRe = MustCompile(`(?m)^\*\*(?:Sent back by user|Decided by user|User comment|Agent reply) \(\d{4}-\d{2}-\d{2}\):\*\* .*$`)
)

func lines(body string) []string {
	return strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
}

// Excerpt is store.excerpt(body, limit).
func Excerpt(body string, limit int) string {
	var out []string
	fenced, size := false, 0
	for _, line := range lines(body) {
		if mdFenceRe.MatchString(line) {
			fenced = !fenced
			continue
		}
		stripped := Strip(line)
		if fenced || stripped == "$$" || stripped == "---" || stripped == "***" {
			continue
		}
		// Each regex runs only when its literal trigger is present; the
		// results are identical (tested against Python vectors), just cheaper.
		if strings.Contains(line, "![") {
			line = mdImageRe.ReplaceAllString(line, "$1")
		}
		if strings.Contains(line, "](") {
			line = mdLinkRe.ReplaceAllString(line, "$1")
		}
		line = mdPrefixRe.ReplaceAllString(line, "")
		if strings.ContainsAny(line, "*_`~") {
			line = mdInlineRe.ReplaceAllString(line, "")
		}
		line = strings.ReplaceAll(line, "|", " ")
		if Strip(line) != "" {
			plain := line
			if needsSpaceCollapse(line) {
				plain = spaceRunRe.ReplaceAllString(line, " ")
			}
			plain = Strip(plain)
			size += Len(plain)
			if len(out) > 0 {
				size++
			}
			out = append(out, plain)
			if limit >= 0 && size > limit {
				return RStrip(Prefix(strings.Join(out, " "), limit)) + "…"
			}
		}
	}
	text := strings.Join(out, " ")
	if Len(text) > limit {
		return RStrip(Prefix(text, limit)) + "…"
	}
	return text
}

// MarkdownExcerpt is store.markdown_excerpt(body, limit).
func MarkdownExcerpt(body string, limit int) (string, bool) {
	var blocks, current []string
	fenced := false
	for _, line := range lines(body) {
		if mdFenceRe.MatchString(line) {
			fenced = !fenced
			continue
		}
		if fenced || mdImageOnlyRe.MatchString(line) {
			continue
		}
		if Strip(line) != "" {
			current = append(current, RStrip(line))
		} else if len(current) > 0 {
			blocks = append(blocks, strings.Join(current, "\n"))
			current = nil
		}
	}
	if len(current) > 0 {
		blocks = append(blocks, strings.Join(current, "\n"))
	}
	var out []string
	size := 0
	for _, block := range blocks {
		n := Len(block)
		if len(out) > 0 && size+n > limit {
			return strings.Join(out, "\n\n"), true
		}
		if size+n > limit {
			cut := Prefix(block, limit)
			nl := runeIndexLast(cut, "\n")
			if nl > limit/2 {
				cut = Prefix(cut, nl)
			} else if i := strings.LastIndex(cut, " "); i >= 0 {
				cut = cut[:i]
			}
			return RStrip(cut) + " …", true
		}
		out = append(out, block)
		size += n + 2
	}
	return strings.Join(out, "\n\n"), false
}

// runeIndexLast is str.rfind(sub) in code points (-1 when absent).
func runeIndexLast(s, sub string) int {
	i := strings.LastIndex(s, sub)
	if i < 0 {
		return -1
	}
	return Len(s[:i])
}

// needsSpaceCollapse reports whether re.sub(r"\s+", " ", line) would change
// line: a whitespace run longer than one, or any whitespace other than ' '.
func needsSpaceCollapse(line string) bool {
	prevSpace := false
	for _, r := range line {
		if IsSpace(r) {
			if prevSpace || r != ' ' {
				return true
			}
			prevSpace = true
		} else {
			prevSpace = false
		}
	}
	return false
}
