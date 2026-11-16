package agentdocs

import (
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SummaryMax is the longest summary the index returns, in characters.
const SummaryMax = 300

// Heading is one ATX heading outside code fences.
type Heading struct {
	Level int
	Text  string
	Slug  string // GitHub-style anchor, unique within the document
	line  int
}

var (
	reHeading  = regexp.MustCompile(`^ {0,3}(#{1,6})[ \t]+(.*?)[ \t#]*$`)
	reLink     = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	reMdLink   = regexp.MustCompile(`\]\(([^)\s]+)((?:\s+"[^"]*")?)\)`)
	reMetaLine = regexp.MustCompile(`^\*\*[^*]+:\*\*`)
)

// fenceOf returns the fence marker a line opens or closes ("" if none).
func fenceOf(line string) string {
	t := strings.TrimLeft(line, " ")
	if len(line)-len(t) > 3 {
		return ""
	}
	for _, m := range []string{"```", "~~~"} {
		if strings.HasPrefix(t, m) {
			return m
		}
	}
	return ""
}

// eachLine calls fn for every line with whether it sits inside a code fence
// (fence lines themselves count as inside).
func eachLine(lines []string, fn func(i int, line string, inFence bool)) {
	open := ""
	for i, line := range lines {
		if f := fenceOf(line); f != "" {
			if open == "" {
				open = f
				fn(i, line, true)
				continue
			}
			if f == open {
				open = ""
				fn(i, line, true)
				continue
			}
		}
		fn(i, line, open != "")
	}
}

// plainInline drops the inline Markdown that GitHub drops from anchors and
// summaries: links keep their text, emphasis and code markers go.
func plainInline(s string) string {
	s = reLink.ReplaceAllString(s, "$1")
	return strings.NewReplacer("**", "", "__", "", "`", "").Replace(s)
}

// Slug is GitHub's heading anchor: lower case, punctuation removed (letters,
// digits, '-', '_' and spaces kept), spaces become '-'.
func Slug(text string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(plainInline(text)) {
		switch {
		case r == ' ':
			b.WriteByte('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Headings lists the document's headings with unique slugs (a repeated slug
// gets -1, -2, ... like GitHub).
func Headings(md string) []Heading {
	var out []Heading
	seen := map[string]int{}
	eachLine(strings.Split(md, "\n"), func(i int, line string, inFence bool) {
		if inFence {
			return
		}
		m := reHeading.FindStringSubmatch(line)
		if m == nil {
			return
		}
		text := strings.TrimSpace(m[2])
		slug := Slug(text)
		if n, dup := seen[slug]; dup {
			seen[slug] = n + 1
			slug = slug + "-" + strconv.Itoa(n+1)
		} else {
			seen[slug] = 0
		}
		out = append(out, Heading{Level: len(m[1]), Text: plainInline(text), Slug: slug, line: i})
	})
	return out
}

// Section returns the heading's section: its heading line through the line
// before the next heading of the same or a higher level.
func Section(md, slug string) (string, bool) {
	lines := strings.Split(md, "\n")
	hs := Headings(md)
	for i, h := range hs {
		if h.Slug != slug {
			continue
		}
		end := len(lines)
		for _, next := range hs[i+1:] {
			if next.Level <= h.Level {
				end = next.line
				break
			}
		}
		return strings.TrimRight(strings.Join(lines[h.line:end], "\n"), "\n") + "\n", true
	}
	return "", false
}

// Title is the first level-1 heading, else the file name.
func Title(md, rel string) string {
	for _, h := range Headings(md) {
		if h.Level == 1 && h.Text != "" {
			return h.Text
		}
	}
	return strings.TrimSuffix(path.Base(rel), ".md")
}

// frontMatter splits a leading "---" YAML block off and returns its
// description (or summary) value.
func frontMatter(md string) (desc, rest string) {
	if !strings.HasPrefix(md, "---\n") {
		return "", md
	}
	end := strings.Index(md[4:], "\n---")
	if end < 0 {
		return "", md
	}
	block := md[4 : 4+end]
	rest = md[4+end+4:]
	if i := strings.IndexByte(rest, '\n'); i >= 0 {
		rest = rest[i+1:]
	} else {
		rest = ""
	}
	for _, line := range strings.Split(block, "\n") {
		key, value, ok := strings.Cut(line, ":")
		key = strings.TrimSpace(key)
		if ok && (key == "description" || key == "summary") && desc == "" {
			desc = strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	return desc, rest
}

// Summary is the front-matter description, else the first prose paragraph
// (headings, code, tables, quotes, images and "**Key:** value" status lines
// skipped), as plain text of at most SummaryMax characters.
func Summary(md string) string {
	desc, body := frontMatter(md)
	if desc == "" {
		var para []string
		done := false
		flush := func() {
			if len(para) == 0 {
				return
			}
			meta := true
			for _, l := range para {
				if !reMetaLine.MatchString(l) {
					meta = false
				}
			}
			if !meta {
				desc, done = strings.Join(para, " "), true
			}
			para = nil
		}
		eachLine(strings.Split(body, "\n"), func(_ int, line string, inFence bool) {
			if done {
				return
			}
			t := strings.TrimSpace(line)
			switch {
			case inFence:
				flush()
			case t == "":
				flush()
			case len(para) == 0 && (strings.HasPrefix(t, "#") || strings.HasPrefix(t, "|") ||
				strings.HasPrefix(t, ">") || strings.HasPrefix(t, "<") || strings.HasPrefix(t, "![") ||
				strings.HasPrefix(t, "---") || strings.HasPrefix(t, "===")):
			default:
				para = append(para, t)
			}
		})
		flush()
	}
	return truncate(strings.Join(strings.Fields(plainInline(desc)), " "), SummaryMax)
}

func truncate(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	r := []rune(s)[:limit-3]
	cut := string(r)
	if i := strings.LastIndexByte(cut, ' '); i > len(cut)/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:") + "..."
}

// RewriteLinks turns relative links to other served docs into API paths
// (base + "/" + path, a "#fragment" becoming "?section=fragment").  Links
// to anything else, absolute URLs, in-page anchors and code blocks are
// left untouched.
func RewriteLinks(md, docPath, base string, served func(string) bool) string {
	lines := strings.Split(md, "\n")
	eachLine(lines, func(i int, line string, inFence bool) {
		if inFence || !strings.Contains(line, "](") {
			return
		}
		lines[i] = reMdLink.ReplaceAllStringFunc(line, func(m string) string {
			sub := reMdLink.FindStringSubmatch(m)
			target, title := sub[1], sub[2]
			if strings.Contains(target, "://") || strings.HasPrefix(target, "/") ||
				strings.HasPrefix(target, "#") || strings.HasPrefix(target, "mailto:") {
				return m
			}
			file, frag, _ := strings.Cut(target, "#")
			resolved := path.Clean(path.Join(path.Dir(docPath), file))
			if !served(resolved) {
				return m
			}
			url := base + "/" + resolved
			if frag != "" {
				url += "?section=" + frag
			}
			return "](" + url + title + ")"
		})
	})
	return strings.Join(lines, "\n")
}
