package store

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"arkstudio/go/internal/platform/clock"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/compat/pyurl"
	"arkstudio/go/internal/sqlitedb"
)

// Port of arkstudio/markdown_bundle.py: selected units as a portable
// Markdown bundle (ZIP).

const (
	bundleMaxUnits = 2000
	bundleMaxDepth = 3
)

// BundleError is markdown_bundle.BundleError (a ValueError: HTTP 400).
type BundleError struct{ Message string }

func (e *BundleError) Error() string { return e.Message }

var (
	videoExt = set("mp4", "webm", "mov", "m4v", "ogv")
	audioExt = set("mp3", "wav", "ogg", "oga", "m4a", "aac", "flac", "opus")
	imageExt = set("png", "jpg", "jpeg", "gif", "webp", "svg", "bmp", "avif")

	space     = pytext.SpaceClass
	uidRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}\n?$`)
	wikiRe    = regexp.MustCompile(`(\\?)\[\[([A-Za-z0-9][A-Za-z0-9_.-]{0,63})(?:\|([^\]]*))?\]\]`)
	linkPat   = `(!?)\[([^\]]*)\]\([` + space + `]*(<[^>]*>|[^)` + space + `]+)([` + space + `]+"[^"]*")?[` + space + `]*\)`
	linkRe    = regexp.MustCompile(linkPat)
	playerRe  = regexp.MustCompile(`\A([` + space + `]*(?:>[` + space + `]*)*)` + linkPat + `[` + space + `]*\z`)
	attrRe    = regexp.MustCompile(`(?i)(?:src|href|poster)[` + space + `]*=[` + space + `]*(["'])`)
	mediaRe   = regexp.MustCompile(`(?i)\A(?:media:(\p{Nd}+)|/api/media/(\p{Nd}+)/raw(?:[?#].*)?)\n?\z`)
	fenceRe   = regexp.MustCompile(`^[` + space + `]{0,3}(` + "`" + `{3,}|~{3,})`)
	extRe     = regexp.MustCompile(`\.([A-Za-z0-9]+)\n?$`)
	safeRe    = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
	tagSlug   = regexp.MustCompile(`[^A-Za-z0-9_/-]+`)
	codeRe    = regexp.MustCompile("`+[^`]*`+")
	httpRe    = regexp.MustCompile(`(?i)^https?://`)
	schemeRe2 = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*:`)
	pathQRe   = regexp.MustCompile(`[?&]path=([^&#]*)`)
	refSplit  = regexp.MustCompile(`[` + space + `,]+`)
)

func set(items ...string) map[string]bool {
	out := map[string]bool{}
	for _, i := range items {
		out[i] = true
	}
	return out
}

func ext(name string) string {
	s := strings.SplitN(strings.SplitN(name, "?", 2)[0], "#", 2)[0]
	m := extRe.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return strings.ToLower(m[1])
}

func safeName(name, fallback string) string {
	clean := strings.Trim(safeRe.ReplaceAllString(name, "_"), "._")
	clean = pytext.Prefix(clean, 120)
	if clean == "" {
		return fallback
	}
	return clean
}

func bundleSlug(text, fallback string) string {
	s := pytext.Prefix(strings.Trim(safeRe.ReplaceAllString(text, "-"), "-."), 60)
	if s == "" {
		return fallback
	}
	return s
}

func yamlStr(v any) string {
	if v == nil {
		return string(pyjson.Dumps(""))
	}
	return string(pyjson.Dumps(pyjson.Str(v)))
}

func label(text string) string {
	return strings.NewReplacer(`\`, `\\`, "[", `\[`, "]", `\]`).Replace(text)
}

func htmlAttr(v string) string {
	return strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;").Replace(v)
}

func kindOf(url string) string {
	e := ext(url)
	switch {
	case videoExt[e]:
		return "video"
	case audioExt[e]:
		return "audio"
	case imageExt[e]:
		return "image"
	}
	return ""
}

// pathName is pathlib.Path(s).name on Windows (both separators, drive).
func pathName(s string) string {
	s = strings.ReplaceAll(s, `\`, "/")
	if len(s) >= 2 && s[1] == ':' && ((s[0] >= 'A' && s[0] <= 'Z') || (s[0] >= 'a' && s[0] <= 'z')) {
		s = s[2:]
	}
	var parts []string
	for _, p := range strings.Split(s, "/") {
		if p != "" && p != "." {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

func isWordRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) }

// boundary is Python's Unicode \b at byte offset i.
func boundary(s string, i int) bool {
	var prev, next bool
	if i > 0 {
		r := []rune(s[:i])
		prev = isWordRune(r[len(r)-1])
	}
	if i < len(s) {
		for _, r := range s[i:] {
			next = isWordRune(r)
			break
		}
	}
	return prev != next
}

type asset struct {
	arcname, kind, origin, err string
	source                     string // "" when missing
	alias                      bool
}

// MediaResolver maps a media id to a local file or an error message.
type MediaResolver func(ctx context.Context, mediaID int64) (string, error)

// Bundle is markdown_bundle.MarkdownBundle.
type Bundle struct {
	s           *Store
	projectPath string
	projectName string
	resolve     MediaResolver
	baseURL     string

	units    map[int64]*pyjson.Object
	order    []int64
	selected map[int64]bool
	via      map[int64]string
	viaOrder []int64
	media    map[int64]*asset
	mediaSeq []int64
	files    map[string]*asset
	fileSeq  []string
	remote   map[string]bool
	uidToID  map[string]int64
	extra    map[int64]*bundleUnit
	ctx      context.Context
}

type bundleUnit struct {
	linksOut, linksIn []any
	attachments       []sqlitedb.Row
}

// NewBundle is MarkdownBundle(store, project_path=..., project_name=..., resolve_media=...).
func (s *Store) NewBundle(projectPath, projectName string, resolve MediaResolver) *Bundle {
	if projectName == "" {
		projectName = "ARK project"
	}
	abs, _ := filepath.Abs(projectPath)
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	return &Bundle{s: s, projectPath: abs, projectName: projectName, resolve: resolve,
		units: map[int64]*pyjson.Object{}, selected: map[int64]bool{}, via: map[int64]string{},
		media: map[int64]*asset{}, files: map[string]*asset{}, remote: map[string]bool{},
		uidToID: map[string]int64{}, extra: map[int64]*bundleUnit{}}
}

// ParseRefs is markdown_bundle.parse_refs for a query string value.
func ParseRefs(value string) ([]string, error) {
	var refs []string
	for _, item := range refSplit.Split(value, -1) {
		if t := pytext.Strip(item); t != "" {
			refs = append(refs, t)
		}
	}
	for _, ref := range refs {
		if !(pytext.IsDigit(ref) || uidRe.MatchString(ref)) {
			return nil, &BundleError{"invalid node reference: " + ref}
		}
	}
	if len(refs) > bundleMaxUnits {
		return nil, &BundleError{"at most 2000 nodes per export"}
	}
	return refs, nil
}

// Collect is MarkdownBundle.collect.
func (b *Bundle) Collect(ctx context.Context, refs []string, depth int64) error {
	b.ctx = ctx
	depth = max(0, min(bundleMaxDepth, depth))
	return b.s.read(ctx, func(q sqlitedb.Querier) error {
		var seeds []int64
		seen := map[int64]bool{}
		for _, ref := range refs {
			ref = pytext.Strip(ref)
			if ref == "" {
				continue
			}
			row, err := unitRow(ctx, q, ref)
			if err != nil {
				if se, ok := err.(*Error); ok {
					return &BundleError{se.Message}
				}
				return err
			}
			if !seen[row.Int("id")] {
				seen[row.Int("id")] = true
				seeds = append(seeds, row.Int("id"))
			}
		}
		if len(seeds) == 0 {
			return &BundleError{"select at least one node to export"}
		}
		frontier := append([]int64(nil), seeds...)
		for _, id := range seeds {
			b.selected[id] = true
		}
		for level := int64(0); level < depth; level++ {
			var next []int64
			for _, unitID := range frontier {
				refs, err := b.references(ctx, q, unitID)
				if err != nil {
					return err
				}
				for _, r := range refs {
					if seen[r.target] {
						continue
					}
					seen[r.target] = true
					b.via[r.target] = r.reason
					b.viaOrder = append(b.viaOrder, r.target)
					next = append(next, r.target)
					if len(seen) > bundleMaxUnits {
						return &BundleError{"more than 2000 nodes; export a smaller selection"}
					}
				}
			}
			frontier = next
		}
		ordered := append(append([]int64(nil), seeds...), b.viaOrder...)
		byID := map[int64]sqlitedb.Row{}
		for off := 0; off < len(ordered); off += 400 {
			batch := sqlitedb.Args(ordered[off:min(off+400, len(ordered))])
			rows, err := groupRows(ctx, q, "SELECT * FROM units WHERE id IN ({marks})", batch)
			if err != nil {
				return err
			}
			for _, r := range rows {
				byID[r.Int("id")] = r
			}
		}
		var rows []sqlitedb.Row
		for _, id := range ordered {
			if r, ok := byID[id]; ok {
				rows = append(rows, r)
			}
		}
		hydrated, err := b.s.hydrate(ctx, q, rows, true)
		if err != nil {
			return err
		}
		for _, u := range hydrated {
			id := u.Value("id").(int64)
			b.units[id] = u
			b.uidToID[pyjson.Str(u.Value("uid"))] = id
		}
		for _, id := range ordered {
			if _, ok := b.units[id]; ok {
				b.order = append(b.order, id)
			}
		}
		for _, id := range b.order {
			out, err := linksOf(ctx, q, id, true)
			if err != nil {
				return err
			}
			in, err := linksOf(ctx, q, id, false)
			if err != nil {
				return err
			}
			att, err := sqlitedb.Query(ctx, q, "SELECT id, url, kind, caption FROM unit_media WHERE unit_id=? ORDER BY id", id)
			if err != nil {
				return err
			}
			b.extra[id] = &bundleUnit{out, in, att}
		}
		return nil
	})
}

type reference struct {
	target int64
	reason string
}

func (b *Bundle) references(ctx context.Context, q sqlitedb.Querier, unitID int64) ([]reference, error) {
	row, _, err := sqlitedb.QueryOne(ctx, q, "SELECT uid, body FROM units WHERE id=?", unitID)
	if err != nil {
		return nil, err
	}
	var out []reference
	for _, m := range wikiRe.FindAllStringSubmatch(row.Str("body"), -1) {
		if m[1] != "" {
			continue
		}
		t, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT id FROM units WHERE uid=?", m[2])
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, reference{t.Int("id"), row.Str("uid") + " mentions"})
		}
	}
	links, err := sqlitedb.Query(ctx, q, "SELECT target_id, relation FROM unit_links WHERE source_id=? ORDER BY id", unitID)
	if err != nil {
		return nil, err
	}
	for _, l := range links {
		if l.Str("relation") != "supersedes" {
			out = append(out, reference{l.Int("target_id"), row.Str("uid") + " " + l.Str("relation")})
		}
	}
	return out, nil
}

func (b *Bundle) mediaAsset(mediaID int64) (*asset, string, bool) {
	if a, ok := b.media[mediaID]; ok {
		return a, "", true
	}
	row, ok, err := b.s.MediaRow(b.ctx, mediaID)
	if err != nil || !ok {
		return nil, "", false
	}
	url := row.Str("url")
	if httpRe.MatchString(url) {
		b.remote[url] = true
		return nil, url, true
	}
	name := pathName(pyurl.Unquote(strings.SplitN(url, "?", 2)[0]))
	e := ext(name)
	kind := pytext.Lower(row.Str("kind"))
	if kind != "video" && kind != "audio" && kind != "image" {
		switch {
		case videoExt[e]:
			kind = "video"
		case audioExt[e]:
			kind = "audio"
		case imageExt[e]:
			kind = "image"
		default:
			kind = "file"
		}
	}
	id := strconv.FormatInt(mediaID, 10)
	a := &asset{arcname: "media/" + id + "-" + safeName(name, "media-"+id), kind: kind, origin: "media:" + id}
	if source, err := b.resolve(b.ctx, mediaID); err != nil {
		a.err = err.Error()
		if a.err == "" {
			a.err = "Exception"
		}
	} else {
		a.source = source
	}
	if a.source != "" {
		for _, mid := range b.mediaSeq {
			other := b.media[mid]
			if other.source == a.source && !other.alias {
				a.arcname, a.alias = other.arcname, true
				break
			}
		}
	}
	b.media[mediaID] = a
	b.mediaSeq = append(b.mediaSeq, mediaID)
	return a, "", true
}

func (b *Bundle) fileAsset(rel string) *asset {
	var parts []string
	for _, part := range strings.Split(strings.ReplaceAll(rel, `\`, "/"), "/") {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			if len(parts) == 0 {
				return nil
			}
			parts = parts[:len(parts)-1]
		} else {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return nil
	}
	key := strings.Join(parts, "/")
	if a, ok := b.files[key]; ok {
		return a
	}
	target := resolvePath(filepath.Join(b.projectPath, filepath.FromSlash(key)))
	if target != b.projectPath && !strings.HasPrefix(target, b.projectPath+string(filepath.Separator)) {
		return nil
	}
	if fi, err := os.Stat(target); err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	e := ext(filepath.Base(target))
	kind := "file"
	switch {
	case videoExt[e]:
		kind = "video"
	case audioExt[e]:
		kind = "audio"
	case imageExt[e]:
		kind = "image"
	}
	safe := make([]string, len(parts))
	for i, p := range parts {
		safe[i] = safeName(p, "file")
	}
	a := &asset{arcname: "files/" + strings.Join(safe, "/"), source: target, kind: kind, origin: key}
	b.files[key] = a
	b.fileSeq = append(b.fileSeq, key)
	return a
}

func resolvePath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

// rewriteURL is MarkdownBundle._rewrite_url.
func (b *Bundle) rewriteURL(raw string) (string, string) {
	url := pytext.Strip(raw)
	if strings.HasPrefix(url, "<") && strings.HasSuffix(url, ">") {
		url = pytext.Strip(url[1 : len(url)-1])
	}
	if m := mediaRe.FindStringSubmatch(url); m != nil {
		digits := m[1]
		if digits == "" {
			digits = m[2]
		}
		id, _ := pytext.Int(digits)
		a, remote, ok := b.mediaAsset(id)
		if remote != "" {
			return remote, kindOf(remote)
		}
		if !ok || a == nil || a.source == "" {
			if a != nil {
				return raw, a.kind
			}
			return raw, ""
		}
		return "../" + a.arcname, a.kind
	}
	if schemeRe2.MatchString(url) || strings.HasPrefix(url, "#") || strings.HasPrefix(url, "//") {
		if httpRe.MatchString(url) {
			return raw, kindOf(url)
		}
		return raw, ""
	}
	if strings.HasPrefix(url, "/api/workspace/raw") {
		var a *asset
		if m := pathQRe.FindStringSubmatch(url); m != nil {
			a = b.fileAsset(pyurl.Unquote(m[1]))
		}
		if a != nil {
			return "../" + a.arcname, a.kind
		}
		return raw, ""
	}
	if strings.HasPrefix(url, "/") {
		if b.baseURL != "" {
			return b.baseURL + url, ""
		}
		return raw, ""
	}
	path, suffix, hasHash := strings.Cut(url, "#")
	a := b.fileAsset(pyurl.Unquote(strings.SplitN(path, "?", 2)[0]))
	if a == nil {
		return raw, ""
	}
	out := "../" + a.arcname
	if hasHash && suffix != "" {
		out += "#" + suffix
	}
	return out, a.kind
}

func (b *Bundle) nodeLink(uid string, lbl *string) string {
	if id, ok := b.uidToID[uid]; ok {
		if u := b.units[id]; u != nil {
			text := uid + " · " + pyjson.Str(u.Value("title"))
			if lbl != nil {
				text = *lbl
			}
			return "[" + label(text) + "](" + uid + ".md)"
		}
	}
	var title any
	found := false
	b.s.read(b.ctx, func(q sqlitedb.Querier) error { //nolint:errcheck
		row, ok, err := sqlitedb.QueryOne(b.ctx, q, "SELECT title FROM units WHERE uid=?", uid)
		if ok {
			title, found = row.Get("title"), true
		}
		return err
	})
	text := uid
	if lbl != nil {
		text = *lbl
	} else if pyjson.Truthy(title) {
		text = uid + " · " + pyjson.Str(title)
	}
	if b.baseURL != "" && found {
		return "[" + label(text) + "](" + b.baseURL + "/?unit=" + uid + ")"
	}
	return text
}

// rewriteInline is MarkdownBundle._rewrite_inline.
func (b *Bundle) rewriteInline(text string) string {
	var pieces []string
	last := 0
	for _, loc := range codeRe.FindAllStringIndex(text, -1) {
		pieces = append(pieces, text[last:loc[0]], text[loc[0]:loc[1]])
		last = loc[1]
	}
	pieces = append(pieces, text[last:])
	var out strings.Builder
	for _, piece := range pieces {
		if strings.HasPrefix(piece, "`") {
			out.WriteString(piece)
			continue
		}
		piece = wikiRe.ReplaceAllStringFunc(piece, func(s string) string {
			m := wikiRe.FindStringSubmatch(s)
			if m[1] != "" {
				return s
			}
			var lbl *string
			if t := pytext.Strip(m[3]); t != "" {
				lbl = &t
			}
			return b.nodeLink(m[2], lbl)
		})
		piece = linkRe.ReplaceAllStringFunc(piece, func(s string) string {
			m := linkRe.FindStringSubmatch(s)
			url, _ := b.rewriteURL(m[3])
			if strings.Contains(url, " ") && !strings.HasPrefix(url, "<") {
				url = "<" + url + ">"
			}
			return m[1] + "[" + m[2] + "](" + url + m[4] + ")"
		})
		piece = b.rewriteAttrs(piece)
		out.WriteString(piece)
	}
	return out.String()
}

// rewriteAttrs is HTML_ATTR_RE.sub(_rewrite_attr): a backreference regex,
// matched by hand (the value ends at the same quote on the same line).
func (b *Bundle) rewriteAttrs(s string) string {
	var out strings.Builder
	pos := 0
	for pos < len(s) {
		loc := attrRe.FindStringSubmatchIndex(s[pos:])
		if loc == nil {
			break
		}
		start, quoteEnd := pos+loc[0], pos+loc[1]
		if !boundary(s, start) {
			out.WriteString(s[pos : start+1])
			pos = start + 1
			continue
		}
		quote := s[quoteEnd-1]
		rest := s[quoteEnd:]
		closeAt := strings.IndexByte(rest, quote)
		nl := strings.IndexByte(rest, '\n')
		if closeAt < 0 || (nl >= 0 && nl < closeAt) {
			out.WriteString(s[pos : start+1])
			pos = start + 1
			continue
		}
		value := rest[:closeAt]
		end := quoteEnd + closeAt + 1
		raw := strings.ReplaceAll(value, "&amp;", "&")
		url, _ := b.rewriteURL(raw)
		out.WriteString(s[pos:start])
		if url == raw {
			out.WriteString(s[start:end])
		} else {
			out.WriteString(s[start:quoteEnd] + htmlAttr(url) + string(quote))
		}
		pos = end
	}
	out.WriteString(s[pos:])
	return out.String()
}

// rewriteBody is MarkdownBundle._rewrite_body.
func (b *Bundle) rewriteBody(body string) string {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	var out []string
	fence := ""
	for _, line := range lines {
		marker := fenceRe.FindStringSubmatch(line)
		if fence != "" {
			out = append(out, line)
			if marker != nil && marker[1][0] == fence[0] && len(marker[1]) >= len(fence) {
				stripped := pytext.Strip(line)
				if pytext.Strip(pytext.SliceFrom(stripped, pytext.Len(marker[1]))) == "" {
					fence = ""
				}
			}
			continue
		}
		if marker != nil {
			fence = marker[1]
			out = append(out, line)
			continue
		}
		if player, ok := b.playerLine(line); ok {
			out = append(out, player)
		} else {
			out = append(out, b.rewriteInline(line))
		}
	}
	return strings.Join(out, "\n")
}

// playerLine is MarkdownBundle._player_line.
func (b *Bundle) playerLine(line string) (string, bool) {
	m := playerRe.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	prefix, lbl, target := m[1], m[3], m[4]
	url, kind := b.rewriteURL(target)
	if kind != "video" && kind != "audio" {
		return "", false
	}
	src := htmlAttr(url)
	tag := `<audio controls preload="metadata" src="` + src + `"></audio>`
	if kind == "video" {
		tag = `<video controls preload="metadata" src="` + src + `" style="max-width:100%"></video>`
	}
	link := url
	if strings.Contains(url, " ") {
		link = "<" + url + ">"
	}
	caption := "[open](" + link + ")"
	if lbl != "" {
		caption = "*" + lbl + "* ([open](" + link + "))"
	}
	return prefix + tag + "\n" + prefix + "\n" + prefix + caption, true
}

func mentionsMedia(body string, id int64) bool {
	n := strconv.FormatInt(id, 10)
	for _, token := range []string{"media:" + n, "/api/media/" + n + "/raw"} {
		for from := 0; ; {
			i := strings.Index(body[from:], token)
			if i < 0 {
				break
			}
			at := from + i
			if boundary(body, at) && boundary(body, at+len(token)) {
				return true
			}
			from = at + 1
		}
	}
	return false
}

func (b *Bundle) note(u *pyjson.Object, exportedAt string) string {
	id := u.Value("id").(int64)
	uid := pyjson.Str(u.Value("uid"))
	role := "referenced"
	if b.selected[id] {
		role = "selected"
	}
	var tags []string
	var raw []string
	switch list := u.Value("tags").(type) {
	case []string:
		raw = list
	case []any:
		for _, t := range list {
			raw = append(raw, pyjson.Str(t))
		}
	}
	{
		for _, s := range raw {
			if !strings.HasPrefix(s, "type:") && !strings.HasPrefix(s, "status:") && !strings.HasPrefix(s, "importance:") {
				tags = append(tags, yamlStr(tagSlug.ReplaceAllString(s, "-")))
			}
		}
	}
	front := []string{"---", "uid: " + yamlStr(uid), "title: " + yamlStr(u.Value("title")),
		"aliases: [" + yamlStr(u.Value("title")) + "]", "type: " + yamlStr(u.Value("type")),
		"status: " + yamlStr(u.Value("status")), "importance: " + yamlStr(u.Value("importance")),
		"tags: [" + strings.Join(tags, ", ") + "]", "bundle_role: " + role}
	_, hasVia := b.via[id]
	if role == "referenced" && hasVia {
		front = append(front, "referenced_by: "+yamlStr(b.via[id]))
	}
	for _, key := range []string{"assignee", "start_at", "end_at", "source_ref", "created_at", "updated_at"} {
		if v := u.Value(key); pyjson.Truthy(v) {
			front = append(front, key+": "+yamlStr(v))
		}
	}
	front = append(front, "ark_project: "+yamlStr(b.projectName), "exported_at: "+yamlStr(exportedAt), "---", "")
	meta := []string{"`" + pyjson.Str(u.Value("type")) + "`", "`" + pyjson.Str(u.Value("status")) + "`"}
	if v := u.Value("importance"); pyjson.Truthy(v) {
		meta = append(meta, "`"+pyjson.Str(v)+"`")
	}
	if role == "referenced" && hasVia {
		meta = append(meta, "referenced by "+b.viaText(id))
	}
	lines := append(front, "# "+uid+" · "+pyjson.Str(u.Value("title")), "", strings.Join(meta, " · ")+" · [Index](../README.md)", "")
	bodyText := ""
	if v := u.Value("body"); pyjson.Truthy(v) {
		bodyText = pyjson.Str(v)
	}
	body := b.rewriteBody(bodyText)
	if pytext.Strip(body) != "" {
		lines = append(lines, pytext.RStrip(body), "")
	}
	extra := b.extra[id]
	var attachments []sqlitedb.Row
	for _, m := range extra.attachments {
		if !mentionsMedia(bodyText, m.Int("id")) {
			attachments = append(attachments, m)
		}
	}
	if len(attachments) > 0 {
		lines = append(lines, "## Attachments", "")
		for _, media := range attachments {
			mid := strconv.FormatInt(media.Int("id"), 10)
			url, kind := b.rewriteURL("media:" + mid)
			caption := ""
			if v := media.Get("caption"); pyjson.Truthy(v) {
				caption = pyjson.Str(v)
			} else if n := pathName(strOrNilStr(media.Get("url"))); n != "" {
				caption = n
			} else {
				caption = "media " + mid
			}
			switch {
			case strings.HasPrefix(url, "media:"):
				lines = append(lines, "- "+caption+" (missing)")
			case kind == "video" || kind == "audio":
				p, ok := b.playerLine("![" + caption + "](media:" + mid + ")")
				if !ok {
					p = "- [" + caption + "](" + url + ")"
				}
				lines = append(lines, p, "")
			case kind == "image":
				lines = append(lines, "!["+label(caption)+"]("+url+")", "")
			default:
				lines = append(lines, "- ["+label(caption)+"]("+url+")")
			}
		}
		if lines[len(lines)-1] != "" {
			lines = append(lines, "")
		}
	}
	if len(extra.linksOut) > 0 {
		lines = append(lines, "## Links", "")
		for _, l := range extra.linksOut {
			link := l.(*pyjson.Object)
			note := ""
			if v := link.Value("note"); pyjson.Truthy(v) {
				note = " — " + pyjson.Str(v)
			}
			target := link.Value("target").(*pyjson.Object)
			lines = append(lines, "- "+pyjson.Str(link.Value("relation"))+" → "+b.nodeLink(pyjson.Str(target.Value("uid")), nil)+note)
		}
		lines = append(lines, "")
	}
	var back []*pyjson.Object
	for _, l := range extra.linksIn {
		link := l.(*pyjson.Object)
		if _, ok := b.uidToID[pyjson.Str(link.Value("source").(*pyjson.Object).Value("uid"))]; ok {
			back = append(back, link)
		}
	}
	if len(back) > 0 {
		lines = append(lines, "## Linked from", "")
		for _, link := range back {
			lines = append(lines, "- "+b.nodeLink(pyjson.Str(link.Value("source").(*pyjson.Object).Value("uid")), nil)+" "+
				pyjson.Str(link.Value("relation"))+" this")
		}
		lines = append(lines, "")
	}
	return pytext.RStrip(strings.Join(lines, "\n")) + "\n"
}

func strOrNilStr(v any) string {
	if !pyjson.Truthy(v) {
		return ""
	}
	return pyjson.Str(v)
}

func (b *Bundle) viaText(id int64) string {
	source, relation, _ := strings.Cut(b.via[id], " ")
	return b.nodeLink(source, &source) + " (" + relation + ")"
}

func (b *Bundle) assets() []*asset {
	var out []*asset
	for _, id := range b.mediaSeq {
		if a := b.media[id]; !a.alias {
			out = append(out, a)
		}
	}
	for _, key := range b.fileSeq {
		out = append(out, b.files[key])
	}
	return out
}

func (b *Bundle) readme(exportedAt string) string {
	var selected, referenced []*pyjson.Object
	for _, id := range b.order {
		if b.selected[id] {
			selected = append(selected, b.units[id])
		} else {
			referenced = append(referenced, b.units[id])
		}
	}
	media := b.assets()
	var missing []*asset
	for _, a := range media {
		if a.source == "" {
			missing = append(missing, a)
		}
	}
	lines := []string{"# " + b.projectName + " · Markdown bundle", "",
		"Exported " + exportedAt + " · " + strconv.Itoa(len(selected)) + " selected nodes · " + strconv.Itoa(len(referenced)) +
			" referenced nodes · " + strconv.Itoa(len(media)-len(missing)) + " media files", "",
		"Open this folder in VS Code and press `Ctrl+Shift+V` on any note to preview it, " +
			"or open the folder as an Obsidian vault. Links between notes are relative Markdown links.", "",
		"## Selected nodes", ""}
	for _, u := range selected {
		uid := pyjson.Str(u.Value("uid"))
		lines = append(lines, "- ["+label(uid+" · "+pyjson.Str(u.Value("title")))+"](nodes/"+uid+".md) · `"+
			pyjson.Str(u.Value("type"))+"` · `"+pyjson.Str(u.Value("status"))+"`")
	}
	if len(referenced) > 0 {
		lines = append(lines, "", "## Referenced nodes", "")
		for _, u := range referenced {
			uid := pyjson.Str(u.Value("uid"))
			source, relation, _ := strings.Cut(b.via[u.Value("id").(int64)], " ")
			lines = append(lines, pytext.RStrip("- ["+label(uid+" · "+pyjson.Str(u.Value("title")))+"](nodes/"+uid+".md) · `"+
				pyjson.Str(u.Value("type"))+"` · via "+source+" "+relation))
		}
	}
	if len(missing) > 0 {
		lines = append(lines, "", "## Missing media", "",
			"These references could not be copied; the notes keep their original target.", "")
		for _, a := range missing {
			msg := a.err
			if msg == "" {
				msg = "not found"
			}
			lines = append(lines, "- `"+a.origin+"`: "+msg)
		}
	}
	if len(b.remote) > 0 {
		lines = append(lines, "", "## Remote media (not downloaded)", "")
		var urls []string
		for u := range b.remote {
			urls = append(urls, u)
		}
		sort.Strings(urls)
		for _, u := range urls {
			lines = append(lines, "- <"+u+">")
		}
	}
	return pytext.RStrip(strings.Join(lines, "\n")) + "\n"
}

// Summary is MarkdownBundle.summary.
func (b *Bundle) Summary() (*pyjson.Object, error) {
	for _, id := range b.order {
		u := b.units[id]
		b.rewriteBody(strOrNilStr(u.Value("body")))
		for _, m := range b.extra[id].attachments {
			b.rewriteURL("media:" + strconv.FormatInt(m.Int("id"), 10))
		}
	}
	assets := b.assets()
	present, total := 0, int64(0)
	for _, a := range assets {
		if a.source != "" {
			present++
			if fi, err := os.Stat(a.source); err == nil {
				total += fi.Size()
			} else {
				return nil, err
			}
		}
	}
	return pyjson.NewObject("nodes", int64(len(b.order)), "selected", int64(len(b.selected)), "files", int64(present),
		"missing", int64(len(assets)-present), "bytes", total), nil
}

// WriteZip is MarkdownBundle.write_zip; it returns the manifest.
func (b *Bundle) WriteZip(w io.Writer) (*pyjson.Object, error) {
	if len(b.order) == 0 {
		return nil, &BundleError{"nothing to export; call collect() first"}
	}
	now := clock.Now()
	exportedAt := clock.UTCNow()
	first := b.units[b.order[0]]
	var rootParts []string
	for _, p := range []string{bundleSlug(b.projectName, "ark"), bundleSlug(pyjson.Str(first.Value("uid")), "nodes"), now.Local().Format("20060102-1504")} {
		if p != "" {
			rootParts = append(rootParts, p)
		}
	}
	root := strings.Join(rootParts, "-")
	type note struct{ uid, text string }
	var notes []note
	for _, id := range b.order {
		u := b.units[id]
		notes = append(notes, note{pyjson.Str(u.Value("uid")), b.note(u, exportedAt)})
	}
	readme := b.readme(exportedAt)
	assets := b.assets()
	nodes := []any{}
	for _, id := range b.order {
		u := b.units[id]
		role := "referenced"
		if b.selected[id] {
			role = "selected"
		}
		var via any
		if v, ok := b.via[id]; ok {
			via = v
		}
		nodes = append(nodes, pyjson.NewObject("uid", u.Value("uid"), "title", u.Value("title"), "type", u.Value("type"),
			"status", u.Value("status"), "role", role, "via", via, "path", "nodes/"+pyjson.Str(u.Value("uid"))+".md"))
	}
	files := []any{}
	for _, a := range assets {
		var size any
		if a.source != "" {
			if fi, err := os.Stat(a.source); err == nil {
				size = fi.Size()
			} else {
				return nil, err
			}
		}
		o := pyjson.NewObject("path", a.arcname, "origin", a.origin, "kind", a.kind, "size", size)
		if a.source == "" {
			msg := a.err
			if msg == "" {
				msg = "not found"
			}
			o.Set("error", msg)
		}
		files = append(files, o)
	}
	var remote []string
	for u := range b.remote {
		remote = append(remote, u)
	}
	sort.Strings(remote)
	manifest := pyjson.NewObject("format", "ark-markdown-bundle", "version", int64(1), "exported_at", exportedAt,
		"project", b.projectName, "root", root, "nodes", nodes, "files", files, "remote", stringsAny(remote))
	zw := zip.NewWriter(w)
	write := func(name string, data []byte, method uint16) error {
		f, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: method, Modified: now})
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		return err
	}
	if err := write(root+"/README.md", []byte(readme), zip.Deflate); err != nil {
		return nil, err
	}
	if err := write(root+"/manifest.json", append(pyjson.DumpsIndent(manifest, 2), '\n'), zip.Deflate); err != nil {
		return nil, err
	}
	for _, n := range notes {
		if err := write(root+"/nodes/"+n.uid+".md", []byte(n.text), zip.Deflate); err != nil {
			return nil, err
		}
	}
	for _, a := range assets {
		if a.source == "" {
			continue
		}
		e := ext(a.arcname)
		method := uint16(zip.Deflate)
		if (videoExt[e] || audioExt[e] || imageExt[e]) && e != "svg" && e != "bmp" {
			method = zip.Store
		}
		data, err := os.ReadFile(a.source)
		if err != nil {
			return nil, err
		}
		if err := write(root+"/"+a.arcname, data, method); err != nil {
			return nil, err
		}
	}
	return manifest, zw.Close()
}

func stringsAny(items []string) []any {
	out := make([]any, len(items))
	for i, s := range items {
		out[i] = s
	}
	return out
}

// LocalMediaResolver is markdown_bundle.local_media_resolver.
func (s *Store) LocalMediaResolver(projectPath string, roots []string) MediaResolver {
	project := resolvePath(projectPath)
	allowed := []string{project}
	for _, r := range roots {
		allowed = append(allowed, resolvePath(expandUser(r)))
	}
	return func(ctx context.Context, mediaID int64) (string, error) {
		row, ok, err := s.MediaRow(ctx, mediaID)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", &BundleError{"media not found"}
		}
		url := row.Str("url")
		var path string
		if strings.HasPrefix(pytext.Lower(url), "file://") {
			raw := pyurl.Unquote(pyurl.Parse(url).Path)
			if len(raw) >= 4 && raw[0] == '/' && raw[2] == ':' && raw[3] == '/' && unicode.IsLetter(rune(raw[1])) {
				raw = raw[1:]
			}
			path = raw
		} else {
			path = url
			if !filepath.IsAbs(path) {
				path = filepath.Join(project, path)
			}
		}
		path = resolvePath(expandUser(path))
		inside := false
		for _, root := range allowed {
			if path == root || strings.HasPrefix(path, strings.TrimRight(root, `\/`)+string(filepath.Separator)) {
				inside = true
				break
			}
		}
		if !inside {
			return "", &BundleError{"local media outside the project folder"}
		}
		if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
			return "", &BundleError{"media file not found: " + filepath.Base(path)}
		}
		return path, nil
	}
}

func expandUser(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return home + p[1:]
		}
	}
	return p
}
