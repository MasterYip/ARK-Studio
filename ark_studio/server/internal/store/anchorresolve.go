package store

// Go only: review anchors that survive later edits (docs/agents/
// working-mode.md "Review anchors").
//
// A "#slug" anchor comes from the heading text at the time the note was
// written, so a later edit of the heading changes its slug.  On read, an
// anchor that matches no id resolves to the closest heading
// (resolveHeading; web/js/review-model.js resolveHeadingAnchor mirrors it).
// Only an anchor with no plausible heading stays broken.

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// bodyAnchor is a heading or {#id} block of a body.
type bodyAnchor struct {
	line     int    // index of its line in the body
	heading  bool   // a heading (else a block with an explicit id)
	text     string // plain heading text
	explicit string // the {#id} marker ("" = none)
	id       string // the id it renders ("" = none)
}

func bodyLines(body string) []string {
	return strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
}

// scanBodyAnchors lists the headings and {#id} blocks of a body in order,
// with the ids store/anchors.go BodyAnchors (and the web renderer) give them.
func scanBodyAnchors(lines []string) []bodyAnchor {
	var out []bodyAnchor
	counts := map[string]int{}
	fence := ""
	for i, line := range lines {
		if m := anchorFenceRe.FindStringSubmatch(line); m != nil {
			marker := m[1][:3]
			if fence == "" {
				fence = marker
			} else if marker == fence {
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		explicit := ""
		if m := explicitIDRe.FindStringSubmatch(line); m != nil {
			explicit = m[1]
			line = line[:len(line)-len(m[0])]
		}
		if m := atxHeadingRe.FindStringSubmatch(line); m != nil {
			a := bodyAnchor{line: i, heading: true, text: plainHeading(m[2]), explicit: explicit, id: explicit}
			if explicit == "" {
				if slug := AnchorSlug(a.text); slug != "" {
					n := counts[slug]
					counts[slug] = n + 1
					a.id = slug
					if n > 0 {
						a.id += "-" + strconv.Itoa(n)
					}
				}
			}
			out = append(out, a)
			continue
		}
		if explicit != "" {
			out = append(out, bodyAnchor{line: i, explicit: explicit, id: explicit})
		}
	}
	return out
}

// ------------------------------------------------------------ resolution

// anchorFuzzyMin is the similarity a fuzzy match needs; anchorFuzzyMargin
// is how far it must lead the runner-up (else the match is ambiguous).
const (
	anchorFuzzyMin    = 0.6
	anchorFuzzyMargin = 0.08
)

var trailingParenRe = regexp.MustCompile(`\s*[(\[【〔][^()\[\]【】〔〕]*[)\]】〕]\s*$`)

// foldWidth maps full-width ASCII forms and the ideographic space to ASCII.
func foldWidth(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 0xFF01 && r <= 0xFF5E:
			return r - 0xFEE0
		case r == 0x3000:
			return ' '
		}
		return r
	}, s)
}

// anchorKey is the text a loose match compares: width-folded, lower case,
// without trailing parentheticals ("（DEX-1c）", "(v2)"), letters and digits
// only.
func anchorKey(text string) string {
	text = strings.ToLower(foldWidth(text))
	for {
		next := trailingParenRe.ReplaceAllString(text, "")
		if next == text || strings.TrimSpace(next) == "" {
			break
		}
		text = next
	}
	return lettersOnly(text)
}

func lettersOnly(text string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(foldWidth(text)) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// similarity is the Dice coefficient of the character bigrams of a and b.
func similarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}
	grams := func(s string) map[string]int {
		out := map[string]int{}
		rs := []rune(s)
		if len(rs) == 1 {
			out[s]++
			return out
		}
		for i := 0; i+1 < len(rs); i++ {
			out[string(rs[i:i+2])]++
		}
		return out
	}
	ga, gb := grams(a), grams(b)
	na, nb, common := 0, 0, 0
	for g, n := range ga {
		na += n
		if m := gb[g]; m > 0 {
			common += min(n, m)
		}
	}
	for _, n := range gb {
		nb += n
	}
	return 2 * float64(common) / float64(na+nb)
}

// resolveHeading finds the heading or block a "#slug" anchor means, in this
// order: an explicit id, an automatic slug, a loose match (trailing
// parentheticals, punctuation, case and width ignored; slug or link label
// against the heading text), the clearly best fuzzy match of the heading
// text with the slug and the label.  how is "explicit" | "slug" |
// "normalized" | "fuzzy"; ok false when nothing plausible matches.
func resolveHeading(slug, label string, anchors []bodyAnchor) (a bodyAnchor, how string, ok bool) {
	for _, x := range anchors {
		if x.explicit != "" && x.explicit == slug {
			return x, "explicit", true
		}
	}
	for _, x := range anchors {
		if x.explicit == "" && x.id != "" && x.id == slug {
			return x, "slug", true
		}
	}
	slugKey, labelKey := anchorKey(slug), anchorKey(label)
	var found []bodyAnchor
	for _, x := range anchors {
		if x.id == "" {
			continue
		}
		keys := []string{anchorKey(x.id)}
		if x.heading {
			keys = append(keys, anchorKey(x.text), lettersOnly(x.text))
		}
		for _, k := range keys {
			if k != "" && (k == slugKey || k == labelKey) {
				found = append(found, x)
				break
			}
		}
	}
	if len(found) == 1 {
		return found[0], "normalized", true
	}
	if len(found) > 1 {
		return bodyAnchor{}, "", false
	}
	best, second, at := 0.0, 0.0, -1
	for i, x := range anchors {
		if x.id == "" {
			continue
		}
		text := x.id
		if x.heading {
			text = x.text
		}
		key := anchorKey(text)
		score := max(similarity(slugKey, key), similarity(labelKey, key))
		if score > best {
			best, second, at = score, best, i
		} else if score > second {
			second = score
		}
	}
	if at >= 0 && best >= anchorFuzzyMin && best-second >= anchorFuzzyMargin {
		return anchors[at], "fuzzy", true
	}
	return bodyAnchor{}, "", false
}

// anchorIDList is "#a, #b, ..." for an error message (at most 12).
func anchorIDList(anchors []bodyAnchor) string {
	var ids []string
	for _, a := range anchors {
		if a.id != "" {
			ids = append(ids, "#"+a.id)
		}
	}
	if len(ids) == 0 {
		return "none"
	}
	if len(ids) > 12 {
		ids = append(ids[:12], "...")
	}
	return strings.Join(ids, ", ")
}
