package store

import (
	"context"
	"math"
	"sort"
	"strconv"
	"strings"

	"arkstudio/go/internal/platform/clock"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/sqlitedb"
)

// Port of arkstudio/export.py and Store.units_with_tags / assignees_in_use.

// UnitsWithTags is Store.units_with_tags() (live units, newest first).
func (s *Store) UnitsWithTags(ctx context.Context) ([]*pyjson.Object, error) {
	var out []*pyjson.Object
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		var err error
		out, err = s.unitsWithTags(ctx, q)
		return err
	})
	return out, err
}

func (s *Store) unitsWithTags(ctx context.Context, q sqlitedb.Querier) ([]*pyjson.Object, error) {
	rows, err := sqlitedb.Query(ctx, q, "SELECT * FROM units WHERE archived=0 ORDER BY updated_at DESC")
	if err != nil {
		return nil, err
	}
	return s.hydrate(ctx, q, rows, true)
}

// AssigneesInUse is Store.assignees_in_use.
func (s *Store) AssigneesInUse(ctx context.Context) ([]string, error) {
	var out []string
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		rows, err := sqlitedb.Query(ctx, q, "SELECT DISTINCT assignee FROM units WHERE assignee IS NOT NULL ORDER BY assignee COLLATE NOCASE")
		for _, r := range rows {
			out = append(out, pyjson.Str(r.Get("assignee")))
		}
		return err
	})
	return out, err
}

// MediaRow is Store.media_row.
func (s *Store) MediaRow(ctx context.Context, id int64) (sqlitedb.Row, bool, error) {
	var row sqlitedb.Row
	var ok bool
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		var err error
		row, ok, err = sqlitedb.QueryOne(ctx, q, "SELECT * FROM unit_media WHERE id=?", id)
		return err
	})
	return row, ok, err
}

// ProjectJSON is export.project_json.
func (s *Store) ProjectJSON(ctx context.Context) (*pyjson.Object, error) {
	var out *pyjson.Object
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		units, err := s.unitsWithTags(ctx, q)
		if err != nil {
			return err
		}
		linkRows, err := sqlitedb.Query(ctx, q, "SELECT * FROM unit_links ORDER BY id")
		if err != nil {
			return err
		}
		links := []any{}
		for _, r := range linkRows {
			links = append(links, pyjson.NewObject("source", r.Get("source_id"), "target", r.Get("target_id"),
				"relation", r.Get("relation"), "note", r.Get("note"), "created_at", r.Get("created_at")))
		}
		mediaRows, err := sqlitedb.Query(ctx, q, "SELECT * FROM unit_media ORDER BY id")
		if err != nil {
			return err
		}
		media := []any{}
		for _, r := range mediaRows {
			media = append(media, pyjson.NewObject("id", r.Get("id"), "unit_id", r.Get("unit_id"), "url", r.Get("url"),
				"kind", r.Get("kind"), "caption", r.Get("caption")))
		}
		commentRows, err := sqlitedb.Query(ctx, q, "SELECT c.*, u.uid FROM unit_comments c JOIN units u ON u.id=c.unit_id ORDER BY c.id")
		if err != nil {
			return err
		}
		comments := []any{}
		for _, r := range commentRows {
			comments = append(comments, pyjson.NewObject("id", r.Get("id"), "unit_id", r.Get("unit_id"),
				"unit_uid", r.Get("uid"), "author", r.Get("author"), "kind", r.Get("kind"), "body", r.Get("body"),
				"created_at", r.Get("created_at"), "reply_to", r.Get("reply_to"), "resolved_at", r.Get("resolved_at"),
				"resolved_by", r.Get("resolved_by")))
		}
		if units == nil {
			units = []*pyjson.Object{}
		}
		out = pyjson.NewObject("format", "ark-studio-export", "version", int64(1), "exported_at", clock.UTCNow(),
			"project", s.Manifest.Snapshot(), "units", units, "links", links, "media", media, "comments", comments)
		return nil
	})
	return out, err
}

func knownTypeIndex(t string) int {
	for i, k := range KnownTypes {
		if k[0] == t {
			return i
		}
	}
	return 99
}

func knownTypeLabel(t string) string {
	for _, k := range KnownTypes {
		if k[0] == t {
			return k[1]
		}
	}
	return t
}

func knownTypeColor(t string) string {
	for _, k := range KnownTypes {
		if k[0] == t {
			return k[2]
		}
	}
	return "#94a3b8"
}

func getOr(o *pyjson.Object, key string, def any) any {
	if v, ok := o.Get(key); ok {
		return v
	}
	return def
}

// ProjectMarkdown is export.project_markdown.
func (s *Store) ProjectMarkdown(ctx context.Context) (string, error) {
	data, err := s.ProjectJSON(ctx)
	if err != nil {
		return "", err
	}
	manifest := data.Value("project").(*pyjson.Object)
	units := data.Value("units").([]*pyjson.Object)
	links := data.Value("links").([]any)
	lines := []string{
		"# " + pyjson.Str(getOr(manifest, "name", "ARK project")), "",
		strings.TrimRight("> "+pyjson.Str(getOr(manifest, "description", "")), " \t\n\r\x0b\x0c"), "",
		"Exported " + pyjson.Str(data.Value("exported_at")) + " · " + strconv.Itoa(len(units)) + " units · " +
			strconv.Itoa(len(links)) + " links", "", "## Contents", ""}
	byType := map[string][]*pyjson.Object{}
	var typeOrder []string
	for _, u := range units {
		t := pyjson.Str(u.Value("type"))
		if _, ok := byType[t]; !ok {
			typeOrder = append(typeOrder, t)
		}
		byType[t] = append(byType[t], u)
	}
	sort.SliceStable(typeOrder, func(i, j int) bool { return knownTypeIndex(typeOrder[i]) < knownTypeIndex(typeOrder[j]) })
	for _, t := range typeOrder {
		lines = append(lines, "- ["+knownTypeLabel(t)+"](#"+t+") ("+strconv.Itoa(len(byType[t]))+")")
	}
	lines = append(lines, "")
	linkMap := map[int64][]*pyjson.Object{}
	for _, l := range links {
		lo := l.(*pyjson.Object)
		src := lo.Value("source").(int64)
		linkMap[src] = append(linkMap[src], lo)
	}
	uid, title := map[int64]string{}, map[int64]string{}
	for _, u := range units {
		id := u.Value("id").(int64)
		uid[id], title[id] = pyjson.Str(u.Value("uid")), pyjson.Str(u.Value("title"))
	}
	for _, t := range typeOrder {
		lines = append(lines, "## "+t, "")
		for _, u := range byType[t] {
			lines = append(lines, "### "+pyjson.Str(u.Value("uid"))+" · "+pyjson.Str(u.Value("title")), "")
			meta := []string{"`" + pyjson.Str(u.Value("status")) + "`", "`" + pyjson.Str(u.Value("importance")) + "`"}
			if pyjson.Truthy(u.Value("start_at")) || pyjson.Truthy(u.Value("end_at")) {
				meta = append(meta, orDash(u.Value("start_at"))+" → "+orDash(u.Value("end_at")))
			}
			if pyjson.Truthy(u.Value("source_ref")) {
				meta = append(meta, "source `"+pyjson.Str(u.Value("source_ref"))+"`")
			}
			if pyjson.Truthy(u.Value("assignee")) {
				meta = append(meta, "assignee `"+pyjson.Str(u.Value("assignee"))+"`")
			}
			lines = append(lines, strings.Join(meta, " · "), "")
			if pyjson.Truthy(u.Value("body")) {
				lines = append(lines, pyjson.Str(u.Value("body")), "")
			}
			var tags []string
			for _, tg := range u.Value("tags").([]string) {
				if !reservedNS[TagNamespace(tg)] {
					tags = append(tags, "`"+tg+"`")
				}
			}
			if len(tags) > 0 {
				lines = append(lines, "Tags: "+strings.Join(tags, " "), "")
			}
			id := u.Value("id").(int64)
			for _, l := range linkMap[id] {
				target := l.Value("target").(int64)
				u2, ok := uid[target]
				if !ok {
					u2 = "?"
				}
				lines = append(lines, "- "+pyjson.Str(l.Value("relation"))+" → "+u2+" · "+title[target])
			}
			if len(linkMap[id]) > 0 {
				lines = append(lines, "")
			}
		}
	}
	return pytext.RStrip(strings.Join(lines, "\n")) + "\n", nil
}

func orDash(v any) string {
	if pyjson.Truthy(v) {
		return pyjson.Str(v)
	}
	return "—"
}

func csvField(s string) string {
	if strings.ContainsAny(s, ",\"\r\n") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

// ProjectCSV is export.project_csv (csv.writer: QUOTE_MINIMAL, CRLF rows).
func (s *Store) ProjectCSV(ctx context.Context) (string, error) {
	data, err := s.ProjectJSON(ctx)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	row := func(fields ...string) {
		for i, f := range fields {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(csvField(f))
		}
		b.WriteString("\r\n")
	}
	row("uid", "type", "title", "status", "importance", "start_at", "end_at", "tags", "origin", "source_ref",
		"updated_at", "body", "assignee")
	str := func(v any) string {
		if pyjson.Truthy(v) {
			return pyjson.Str(v)
		}
		return ""
	}
	for _, u := range data.Value("units").([]*pyjson.Object) {
		row(pyjson.Str(u.Value("uid")), pyjson.Str(u.Value("type")), pyjson.Str(u.Value("title")),
			pyjson.Str(u.Value("status")), pyjson.Str(u.Value("importance")), str(u.Value("start_at")),
			str(u.Value("end_at")), strings.Join(u.Value("tags").([]string), " "), str(u.Value("origin")),
			str(u.Value("source_ref")), str(u.Value("updated_at")),
			strings.ReplaceAll(str(u.Value("body")), "\r\n", "\n"), str(u.Value("assignee")))
	}
	return b.String(), nil
}

func f1(x float64) string { return strconv.FormatFloat(x, 'f', 1, 64) }

// GraphSVG is export.graph_svg.
func (s *Store) GraphSVG(ctx context.Context) (string, error) {
	data, err := s.graphNodes(ctx, 240)
	if err != nil {
		return "", err
	}
	nodes := data.Value("nodes").([]*pyjson.Object)
	edges := data.Value("edges").([]any)
	if len(nodes) == 0 {
		return `<svg xmlns="http://www.w3.org/2000/svg" width="800" height="500"></svg>`, nil
	}
	const width, height = 1200, 760
	cx, cy := float64(width)/2, float64(height)/2
	byType := map[string][]*pyjson.Object{}
	var order []string
	for _, n := range nodes {
		t := pyjson.Str(n.Value("type"))
		if _, ok := byType[t]; !ok {
			order = append(order, t)
		}
		byType[t] = append(byType[t], n)
	}
	pos := map[int64][2]float64{}
	radiusStep := float64(min(280, 60+len(nodes)*2))
	for ring, t := range order {
		group := byType[t]
		radius := 80 + float64(ring)*radiusStep
		for i, n := range group {
			angle := (2 * math.Pi * float64(i) / float64(max(1, len(group)))) + float64(ring)*0.35
			pos[n.Value("id").(int64)] = [2]float64{cx + math.Cos(angle)*radius, cy + math.Sin(angle)*radius}
		}
	}
	name := pyjson.Str(getOr(s.Manifest.Snapshot(), "name", "ARK project"))
	parts := []string{
		`<svg xmlns="http://www.w3.org/2000/svg" width="1200" height="760" viewBox="0 0 1200 760" font-family="Segoe UI, sans-serif">`,
		`<rect width="100%" height="100%" fill="#0f141c"/>`,
		`<text x="36" y="52" fill="#e8eef6" font-size="24" font-weight="700">` + name + `</text>`,
		`<text x="36" y="78" fill="#93a2b6" font-size="13">` + strconv.Itoa(len(nodes)) + " nodes · " +
			strconv.Itoa(len(edges)) + " links · exported from ARK Studio</text>",
		`<g stroke="#2b3646" stroke-width="1.2" opacity="0.7">`,
	}
	for _, e := range edges {
		eo := e.(*pyjson.Object)
		a, okA := pos[eo.Value("source").(int64)]
		b, okB := pos[eo.Value("target").(int64)]
		if okA && okB {
			parts = append(parts, `<line x1="`+f1(a[0])+`" y1="`+f1(a[1])+`" x2="`+f1(b[0])+`" y2="`+f1(b[1])+`"/>`)
		}
	}
	parts = append(parts, "</g>")
	for _, n := range nodes {
		p := pos[n.Value("id").(int64)]
		degree := 1.0
		if d, ok := n.Get("degree"); ok {
			degree = float64(d.(int64))
		}
		r := 5 + math.Min(12, degree*0.8)
		parts = append(parts, `<circle cx="`+f1(p[0])+`" cy="`+f1(p[1])+`" r="`+f1(r)+`" fill="`+
			knownTypeColor(pyjson.Str(n.Value("type")))+`" stroke="#0f141c" stroke-width="2"/>`)
		if r >= 8 {
			label := strings.ReplaceAll(strings.ReplaceAll(pytext.Prefix(pyjson.Str(n.Value("title")), 32), "&", "&amp;"), "<", "&lt;")
			parts = append(parts, `<text x="`+f1(p[0]+r+4)+`" y="`+f1(p[1]+4)+`" fill="#c4cfdd" font-size="10">`+label+"</text>")
		}
	}
	parts = append(parts, "</svg>")
	return strings.Join(parts, "\n"), nil
}

// graphNodes is store.graph(None, limit=limit) (not slim).
func (s *Store) graphNodes(ctx context.Context, limit int64) (*pyjson.Object, error) {
	var out *pyjson.Object
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		var err error
		out, err = s.graph(ctx, q, Filters{}, false, limit, false)
		return err
	})
	return out, err
}
