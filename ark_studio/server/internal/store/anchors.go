package store

// Go only: review anchors (docs/agents/working-mode.md, "Review anchors").
//
// A review note points the reviewer at the exact place to look.  An anchor
// is the target of a Markdown link in the note, or a bare media reference:
//
//	#slug                 a heading (or a {#id} block) in the reviewed unit's body
//	T-0042                another unit
//	T-0042#slug           a heading or block in another unit's body
//	media:421             an attachment (unit_media id)
//	media:421@0:08        ... a video at 0:08
//	media:421@0:08-0:12   ... a video from 0:08 to 0:12
//
// Heading slugs follow web/js/rich-model.js anchorSlug exactly: the heading's
// plain text, lower-cased, every run of characters that are not letters or
// digits replaced by "-", leading/trailing "-" removed; the second heading
// with the same slug gets "-1", the third "-2".  A line, heading or
// paragraph that ends in "{#id}" carries the explicit id instead (the
// renderer hides the marker).

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/sqlitedb"
)

// Anchor is one parsed anchor of a review note.
type Anchor struct {
	Raw   string  // as written (the link target or bare reference)
	Label string  // link text ("" for a bare reference)
	Kind  string  // "heading" | "unit" | "media"
	Unit  string  // uid ("" = the reviewed unit itself)
	Slug  string  // heading / block id ("" = the whole unit)
	Media int64   // media id (kind media)
	Start float64 // seconds (-1 = none)
	End   float64 // seconds (-1 = none)
}

var (
	anchorLinkRe  = regexp.MustCompile(`\[([^\]\n]*)\]\(\s*<?([^)\s>]+)>?(?:\s+"[^"]*")?\s*\)`)
	anchorMediaRe = regexp.MustCompile(`(?:^|[^\w/(:])(media:\d+(?:@[0-9:.]+(?:-[0-9:.]+)?)?)`)
	anchorUnitRe  = regexp.MustCompile(`^([A-Z]{1,4}-\d{1,9})(?:#(.+))?$`)
	anchorMediaT  = regexp.MustCompile(`^media:(\d+)(?:@([0-9:.]+)(?:-([0-9:.]+))?)?$`)
	explicitIDRe  = regexp.MustCompile(`\s*\{#([A-Za-z0-9\p{L}][\w\p{L}\p{N}.-]*)\}\s*$`)
	atxHeadingRe  = regexp.MustCompile(`^ {0,3}(#{1,6})(?:[ \t]+(.*?))?(?:[ \t]+#+)?[ \t]*$`)
	anchorFenceRe = regexp.MustCompile("^ {0,3}(```+|~~~+)")
	inlineImgRe   = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	inlineLinkRe  = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	inlineRefRe   = regexp.MustCompile(`\[\[([A-Za-z0-9][A-Za-z0-9_-]*)\]\]`)
	inlineHTMLRe  = regexp.MustCompile(`<[^>]+>`)
)

// ParseAnchors returns the anchors of a review note in order, once each.
func ParseAnchors(note string) []Anchor {
	var out []Anchor
	seen := map[string]bool{}
	add := func(raw, label string) {
		a, ok := parseAnchor(raw)
		if !ok || seen[a.Raw] {
			return
		}
		seen[a.Raw] = true
		a.Label = strings.TrimSpace(label)
		out = append(out, a)
	}
	rest := note
	for _, m := range anchorLinkRe.FindAllStringSubmatchIndex(note, -1) {
		add(note[m[4]:m[5]], note[m[2]:m[3]])
	}
	// Bare media references outside links.
	rest = anchorLinkRe.ReplaceAllString(rest, " ")
	for _, m := range anchorMediaRe.FindAllStringSubmatch(rest, -1) {
		add(m[1], "")
	}
	return out
}

func parseAnchor(raw string) (Anchor, bool) {
	raw = strings.TrimSpace(raw)
	a := Anchor{Raw: raw, Start: -1, End: -1}
	switch {
	case strings.HasPrefix(raw, "#") && len(raw) > 1:
		a.Kind, a.Slug = "heading", raw[1:]
		return a, true
	case strings.HasPrefix(raw, "media:"):
		m := anchorMediaT.FindStringSubmatch(raw)
		if m == nil {
			return a, false
		}
		a.Kind = "media"
		a.Media, _ = strconv.ParseInt(m[1], 10, 64)
		if m[2] != "" {
			start, ok := parseClock(m[2])
			if !ok {
				return a, false
			}
			a.Start = start
		}
		if m[3] != "" {
			end, ok := parseClock(m[3])
			if !ok || end < a.Start {
				return a, false
			}
			a.End = end
		}
		return a, true
	}
	if m := anchorUnitRe.FindStringSubmatch(raw); m != nil {
		a.Unit = m[1]
		a.Kind = "unit"
		if m[2] != "" {
			a.Kind, a.Slug = "heading", m[2]
		}
		return a, true
	}
	return a, false
}

// parseClock reads "8", "0:08", "1:02:03" or "8.5" as seconds.
func parseClock(text string) (float64, bool) {
	parts := strings.Split(text, ":")
	if len(parts) > 3 {
		return 0, false
	}
	total := 0.0
	for i, p := range parts {
		if p == "" {
			return 0, false
		}
		v, err := strconv.ParseFloat(p, 64)
		if err != nil || v < 0 || (i > 0 && v >= 60) {
			return 0, false
		}
		total = total*60 + v
	}
	return total, true
}

// AnchorSlug is rich-model.js anchorSlug.
func AnchorSlug(text string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(text)) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true
	}
	return b.String()
}

// plainHeading strips the inline Markdown a heading may carry.
func plainHeading(text string) string {
	text = inlineImgRe.ReplaceAllString(text, "$1")
	text = inlineLinkRe.ReplaceAllString(text, "$1")
	text = inlineRefRe.ReplaceAllString(text, "$1")
	text = inlineHTMLRe.ReplaceAllString(text, "")
	text = strings.NewReplacer("**", "", "__", "", "`", "", "~~", "", "*", "", "\\", "").Replace(text)
	return strings.TrimSpace(text)
}

// BodyAnchors lists the anchor ids a body renders: heading slugs (with the
// -1, -2 suffixes of repeated ones) and explicit {#id} markers, in order.
func BodyAnchors(body string) []string {
	var out []string
	for _, a := range scanBodyAnchors(bodyLines(body)) {
		if a.id != "" {
			out = append(out, a.id)
		}
	}
	return out
}

// anchorObject is the JSON form of an anchor (with whether it resolves).
func anchorObject(a Anchor, ok bool, problem string) *pyjson.Object {
	o := pyjson.NewObject("raw", a.Raw, "kind", a.Kind, "label", a.Label, "unit", nilIfEmpty(a.Unit),
		"slug", nilIfEmpty(a.Slug), "ok", ok)
	if a.Kind == "media" {
		o.Set("media", a.Media)
		if a.Start >= 0 {
			o.Set("start", a.Start)
		}
		if a.End >= 0 {
			o.Set("end", a.End)
		}
	}
	if problem != "" {
		o.Set("problem", problem)
	}
	return o
}

// resolveAnchors checks every anchor of note against the project: the
// reviewed unit is selfUID ("" when the note belongs to a review group, where
// "#slug" has no unit).  It returns the anchors as objects and the warnings
// for those that do not resolve.
func resolveAnchors(ctx context.Context, q sqlitedb.Querier, note, selfUID, selfBody string) ([]any, []any, error) {
	anchors := ParseAnchors(note)
	objects := []any{}
	warnings := []any{}
	bodies := map[string]string{}
	exists := map[string]bool{}
	if selfUID != "" {
		bodies[selfUID], exists[selfUID] = selfBody, true
	}
	for _, a := range anchors {
		problem := ""
		var approx *bodyAnchor
		approxHow := ""
		uid := a.Unit
		if a.Kind == "heading" && uid == "" {
			uid = selfUID
			if uid == "" {
				problem = "a #slug anchor needs a unit here: write T-0042#slug"
			}
		}
		if problem == "" && uid != "" {
			if _, known := exists[uid]; !known {
				r, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT body FROM units WHERE uid=? AND archived=0", uid)
				if err != nil {
					return nil, nil, err
				}
				exists[uid] = ok
				bodies[uid] = r.Str("body")
			}
			if !exists[uid] {
				problem = "unit not found: " + uid
			} else if a.Kind == "heading" {
				// Exact id, else the closest heading (anchorresolve.go).
				found, how, ok := resolveHeading(a.Slug, a.Label, scanBodyAnchors(bodyLines(bodies[uid])))
				switch {
				case !ok:
					problem = "no heading or {#id} block \"" + a.Slug + "\" in " + uid +
						"; its anchors: " + anchorIDList(scanBodyAnchors(bodyLines(bodies[uid])))
				case how == "normalized" || how == "fuzzy":
					approx = &found
					approxHow = how
				}
			}
		}
		var media *pyjson.Object
		if a.Kind == "media" {
			r, ok, err := sqlitedb.QueryOne(ctx, q, `SELECT m.*, u.uid FROM unit_media m JOIN units u ON u.id=m.unit_id
                   WHERE m.id=? AND u.archived=0`, a.Media)
			if err != nil {
				return nil, nil, err
			}
			if !ok {
				problem = "media not found: " + strconv.FormatInt(a.Media, 10)
			} else {
				media = mediaDict(r)
				media.Set("preview_kind", nilIfEmpty(mediaPreviewKind(media.Value("kind"), media.Value("url"), media.Value("is_image") == true))).
					Set("unit", r.Get("uid"))
			}
		}
		written := a.Slug
		if approx != nil {
			a.Slug = approx.id // the id to scroll to
		}
		obj := anchorObject(a, problem == "", problem)
		if approx != nil {
			obj.Set("approx", approxHow).Set("written_slug", written).Set("heading", approx.text)
			warnings = append(warnings, pyjson.NewObject("anchor", a.Raw, "problem",
				"no heading or {#id} block \""+written+"\"; it opens the closest heading #"+approx.id+
					" (\""+approx.text+"\") instead: link to #"+approx.id, "approx", true))
		}
		if media != nil {
			obj.Set("media_item", media)
		}
		objects = append(objects, obj)
		if problem != "" {
			warnings = append(warnings, pyjson.NewObject("anchor", a.Raw, "problem", problem))
		}
	}
	return objects, warnings, nil
}
