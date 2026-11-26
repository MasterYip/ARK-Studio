package search

import (
	"errors"
	"strings"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pyre"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/workspace"
)

// Search › Replace over project files.  The preview runs the same file
// search as the Search view (ignores, include/exclude, limits) and reports,
// per file, how many matches would change, a few changed lines and the
// file's hash.  Applying takes that list back: a file whose hash changed
// since the preview is left alone (a conflict), the others are rewritten
// atomically.  Matching is per line like the search itself.

const (
	MaxReplaceFiles   = 500
	MaxReplacement    = 10_000
	replaceSamples    = 5
	ReplaceTimeLimit  = 20 * time.Second
	replaceStepBudget = stepBudget
)

// FileRef is one file of an apply request with the hash the preview saw.
type FileRef struct{ Path, Hash string }

// expand fills a replacement template for one match.  Plain searches use the
// text as is; regular expressions understand $1..$99, $& (or $0), $$ and
// \n, \t, \\ (VS Code's rules).
func expand(tmpl []rune, regex bool, line []rune, m pyre.Match, groups int) string {
	if !regex {
		return string(tmpl)
	}
	var b strings.Builder
	group := func(g int) {
		if s, e, ok := m.Group(g); ok {
			b.WriteString(string(line[s:e]))
		}
	}
	for i := 0; i < len(tmpl); i++ {
		c := tmpl[i]
		if c == '\\' && i+1 < len(tmpl) {
			switch tmpl[i+1] {
			case 'n':
				b.WriteByte('\n')
				i++
				continue
			case 't':
				b.WriteByte('\t')
				i++
				continue
			case '\\':
				b.WriteByte('\\')
				i++
				continue
			}
		}
		if c != '$' || i+1 >= len(tmpl) {
			b.WriteRune(c)
			continue
		}
		next := tmpl[i+1]
		switch {
		case next == '$':
			b.WriteByte('$')
			i++
		case next == '&':
			group(0)
			i++
		case next >= '0' && next <= '9':
			g := int(next - '0')
			used := 1
			if i+2 < len(tmpl) && tmpl[i+2] >= '0' && tmpl[i+2] <= '9' {
				if two := g*10 + int(tmpl[i+2]-'0'); two <= groups {
					g, used = two, 2
				}
			}
			if g > groups {
				b.WriteRune(c) // not a group: keep "$n" literally
				continue
			}
			group(g)
			i += used
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

type sample struct {
	line          int
	before, after string
}

func clipLine(s string) string {
	r := []rune(s)
	if len(r) <= MaxLineChars {
		return s
	}
	return string(r[:MaxLineChars]) + "…"
}

// replaceText applies re line by line.  Line ends ("\n", "\r\n") stay as
// they were.  It returns the new text, the number of replaced matches and
// the first changed lines.
func replaceText(text string, re *pyre.Regexp, tmpl []rune, regex bool, budget *int) (string, int, []sample, error) {
	lines := strings.SplitAfter(text, "\n")
	count := 0
	var samples []sample
	var out strings.Builder
	out.Grow(len(text))
	for n, raw := range lines {
		body, end := raw, ""
		if strings.HasSuffix(body, "\n") {
			body, end = body[:len(body)-1], "\n"
			if strings.HasSuffix(body, "\r") {
				body, end = body[:len(body)-1], "\r\n"
			}
		}
		rs := []rune(body)
		found, err := re.FindAll(rs, budget)
		if err != nil {
			return "", 0, nil, err
		}
		changed := false
		var line strings.Builder
		last := 0
		for _, m := range found {
			if m.End <= m.Start {
				continue
			}
			line.WriteString(string(rs[last:m.Start]))
			line.WriteString(expand(tmpl, regex, rs, m, re.NumGroups()))
			last = m.End
			count++
			changed = true
		}
		if !changed {
			out.WriteString(raw)
			continue
		}
		line.WriteString(string(rs[last:]))
		if len(samples) < replaceSamples {
			samples = append(samples, sample{n + 1, clipLine(body), clipLine(line.String())})
		}
		out.WriteString(line.String())
		out.WriteString(end)
	}
	return out.String(), count, samples, nil
}

func checkReplacement(value string) ([]rune, error) {
	if pytext.Len(value) > MaxReplacement {
		return nil, &Error{"the replacement is too long"}
	}
	return []rune(value), nil
}

func fileError(err error) string {
	var we *workspace.Error
	if errors.As(err, &we) {
		return we.Message
	}
	return err.Error()
}

// ReplacePreview lists what replacing query with replacement would change.
func ReplacePreview(root, query string, o Options, include, exclude, replacement string) (*pyjson.Object, error) {
	re, err := BuildMatcher(query, o)
	if err != nil {
		return nil, err
	}
	tmpl, err := checkReplacement(replacement)
	if err != nil {
		return nil, err
	}
	var paths []string
	capped := false
	summary, err := IterFiles(root, query, o, include, exclude, nil, func(item *pyjson.Object) bool {
		if len(paths) >= MaxReplaceFiles {
			capped = true
			return false
		}
		paths = append(paths, pyjson.Str(item.Value("path")))
		return true
	})
	if err != nil {
		return nil, err
	}
	started := time.Now()
	budget := replaceStepBudget
	files, skipped := []any{}, []any{}
	total := 0
	timedOut := false
	for _, path := range paths {
		if time.Since(started) > ReplaceTimeLimit {
			timedOut = true
			break
		}
		f, err := workspace.ReadText(root, path)
		if err != nil {
			skipped = append(skipped, pyjson.NewObject("path", path, "reason", fileError(err)))
			continue
		}
		_, count, samples, err := replaceText(f.Text, re, tmpl, o.Regex, &budget)
		if err != nil {
			timedOut = true
			break
		}
		if count == 0 {
			continue
		}
		total += count
		rows := make([]any, len(samples))
		for i, s := range samples {
			rows[i] = pyjson.NewObject("line", s.line, "before", s.before, "after", s.after)
		}
		files = append(files, pyjson.NewObject("path", f.Rel, "count", count, "hash", f.Hash, "samples", rows))
	}
	truncated := capped || timedOut
	if summary != nil && pyjson.Truthy(summary.Value("truncated")) {
		truncated = true
	}
	return pyjson.NewObject("query", query, "replacement", replacement, "files", files, "total", total,
		"file_count", len(files), "skipped", skipped, "truncated", truncated, "timed_out", timedOut), nil
}

// ReplaceApply rewrites the previewed files.  Each file must still have the
// hash the preview reported.
func ReplaceApply(root, query string, o Options, replacement string, refs []FileRef) (*pyjson.Object, error) {
	re, err := BuildMatcher(query, o)
	if err != nil {
		return nil, err
	}
	tmpl, err := checkReplacement(replacement)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, &Error{"files are required"}
	}
	if len(refs) > MaxReplaceFiles {
		return nil, &Error{"too many files in one replace"}
	}
	started := time.Now()
	budget := replaceStepBudget
	replaced, conflicts := []any{}, []any{}
	total := 0
	timedOut := false
	seen := map[string]bool{}
	for _, ref := range refs {
		if seen[ref.Path] {
			continue
		}
		seen[ref.Path] = true
		if time.Since(started) > ReplaceTimeLimit {
			timedOut = true
			conflicts = append(conflicts, pyjson.NewObject("path", ref.Path, "reason", "not replaced: the time limit was reached"))
			continue
		}
		f, err := workspace.ReadText(root, ref.Path)
		if err != nil {
			conflicts = append(conflicts, pyjson.NewObject("path", ref.Path, "reason", fileError(err)))
			continue
		}
		if ref.Hash == "" || ref.Hash != f.Hash {
			conflicts = append(conflicts, pyjson.NewObject("path", f.Rel, "reason", "the file changed on disk since the preview; search again"))
			continue
		}
		text, count, _, err := replaceText(f.Text, re, tmpl, o.Regex, &budget)
		if err != nil {
			timedOut = true
			conflicts = append(conflicts, pyjson.NewObject("path", f.Rel, "reason", "not replaced: the pattern took too long"))
			continue
		}
		if count == 0 {
			continue
		}
		hash, err := workspace.WriteText(f, text)
		if err != nil {
			conflicts = append(conflicts, pyjson.NewObject("path", f.Rel, "reason", fileError(err)))
			continue
		}
		total += count
		replaced = append(replaced, pyjson.NewObject("path", f.Rel, "count", count, "hash", hash))
	}
	return pyjson.NewObject("query", query, "replaced", replaced, "conflicts", conflicts, "total", total,
		"file_count", len(replaced), "timed_out", timedOut), nil
}
