package store

// Helpers shared by the cross-project operations (Go only): copying units
// from another project (importark.go).  The split / merge prototype
// (feat/project-move) uses the same helpers.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"arkstudio/go/internal/sqlitedb"
)

var (
	// [[UID]] references in Markdown (a leading backslash escapes one).
	xRefRe = regexp.MustCompile(`(\\?)\[\[([A-Za-z0-9][A-Za-z0-9_-]*)\]\]`)
	// media:N and /api/media/N/raw.
	xMediaRe = regexp.MustCompile(`(media:|/api/media/)(\d+)(/raw)?`)
	xFenceRe = regexp.MustCompile("^[ \t]{0,3}(`{3,}|~{3,})")
)

// mediaRefs lists the media ids a text references.
func mediaRefs(text string) []int64 {
	var out []int64
	for _, m := range xMediaRe.FindAllStringSubmatch(text, -1) {
		if m[1] == "/api/media/" && m[3] == "" {
			continue
		}
		if n, err := strconv.ParseInt(m[2], 10, 64); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// rewriteRefs rewrites [[UID]] and media references outside fenced code:
// ref maps a uid to its replacement text ("" keeps it), media maps an old
// media id to the new one (0 keeps it).
func rewriteRefs(text string, ref func(uid string) string, media func(id int64) int64) string {
	lines := strings.SplitAfter(text, "\n")
	fence := ""
	for i, line := range lines {
		if m := xFenceRe.FindStringSubmatch(line); m != nil {
			mark := m[1][:1]
			if fence == "" {
				fence = mark
			} else if fence == mark {
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		line = xRefRe.ReplaceAllStringFunc(line, func(s string) string {
			m := xRefRe.FindStringSubmatch(s)
			if m[1] != "" {
				return s
			}
			if r := ref(m[2]); r != "" {
				return "[[" + r + "]]"
			}
			return s
		})
		line = xMediaRe.ReplaceAllStringFunc(line, func(s string) string {
			m := xMediaRe.FindStringSubmatch(s)
			if m[1] == "/api/media/" && m[3] == "" {
				return s
			}
			n, _ := strconv.ParseInt(m[2], 10, 64)
			if id := media(n); id > 0 {
				return m[1] + strconv.FormatInt(id, 10) + m[3]
			}
			return s
		})
		lines[i] = line
	}
	return strings.Join(lines, "")
}

// projectRelative is a media url that names a file inside the project folder.
func projectRelative(url string) bool {
	if url == "" || httpRe.MatchString(url) || schemeRe2.MatchString(url) || filepath.IsAbs(url) ||
		strings.HasPrefix(url, "/") || strings.HasPrefix(url, "~") {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(url)))
	return clean != ".." && !strings.HasPrefix(clean, "../")
}

// linkFile hard-links src to dst, or copies it when that fails.
func linkFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

// freeRel is rel, or rel with a -N suffix when the target has it already.
func freeRel(root, rel string, used map[string]bool) string {
	ext := filepath.Ext(rel)
	stem := strings.TrimSuffix(rel, ext)
	cand := rel
	for n := 1; ; n++ {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(cand))); os.IsNotExist(err) && !used[cand] {
			used[cand] = true
			return cand
		}
		cand = fmt.Sprintf("%s-%d%s", stem, n, ext)
	}
}

// treeParents is the parent of every live unit that has one: the target of
// its part_of link, else of its derived_from link (lowest link id first);
// cycles are cut.
func treeParents(ctx context.Context, q sqlitedb.Querier) (map[int64]int64, error) {
	rows, err := sqlitedb.Query(ctx, q, `SELECT l.source_id s, l.target_id t, l.relation r FROM unit_links l
                   JOIN units a ON a.id=l.source_id JOIN units b ON b.id=l.target_id
                   WHERE a.archived=0 AND b.archived=0 AND l.relation IN ('part_of','derived_from') AND l.source_id<>l.target_id
                   ORDER BY l.id`)
	if err != nil {
		return nil, err
	}
	parent := map[int64]int64{}
	for _, rel := range []string{"part_of", "derived_from"} {
		for _, r := range rows {
			if r.Str("r") != rel {
				continue
			}
			if _, ok := parent[r.Int("s")]; !ok {
				parent[r.Int("s")] = r.Int("t")
			}
		}
	}
	ids := make([]int64, 0, len(parent))
	for id := range parent {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, u := range ids {
		seen := map[int64]bool{}
		for x := u; ; {
			p, ok := parent[x]
			if !ok {
				break
			}
			if seen[x] {
				delete(parent, x)
				break
			}
			seen[x] = true
			x = p
		}
	}
	return parent, nil
}

func childrenOf(parent map[int64]int64) map[int64][]int64 {
	out := map[int64][]int64{}
	for c, p := range parent {
		out[p] = append(out[p], c)
	}
	for _, list := range out {
		sort.Slice(list, func(i, j int) bool { return list[i] < list[j] })
	}
	return out
}
