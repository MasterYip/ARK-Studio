package store

// Importing units from another ARK project (Go only; T-0070): a copy, the
// source is not changed.
//
//   - Selected units, optionally with their children (part_of, else
//     derived_from, as in the outline), their tags, the links between the
//     copied units, their attachments and the attachments their text embeds
//     from other units.  Comments, history and activity stay in the source.
//   - Uids: all kept when none is taken here; one clash renumbers the whole
//     batch from this project's counters (as decided for moves, D-0015).
//   - [[UID]] and media references in the copied text are rewritten; a
//     [[UID]] of a unit that was not copied becomes [[<source id>/UID]],
//     which the web app opens in the source project.
//   - Provenance: meta_json.imported_from, source_ref "ark:<project>/<uid>"
//     (so the same unit is imported only once: a repeat is skipped and
//     links and references go to the earlier copy) and a closing line
//     "Imported from <project> [[<project>/<uid>]]".
//   - Files are hard-linked when the projects share a disk, else copied;
//     the database insert is one transaction and a failure removes the
//     files it created.

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/sqlitedb"
)

// ImportRequest selects what to copy from the source project.
type ImportRequest struct {
	SourceID, SourceName string
	Units                []string // uids or ids in the source
	WithChildren         bool
	Lang                 string // "zh" or "en": the provenance line
}

type importUnit struct {
	row            sqlitedb.Row
	id             int64
	oldUID, newUID string
	newID          int64
	existing       bool // imported before: links and references use that copy
}

type importMedia struct {
	row    sqlitedb.Row
	oldID  int64
	owner  int64
	newURL string
	newID  int64
}

type importFile struct {
	src, rel string
	size     int64
}

type importPlan struct {
	req        ImportRequest
	units      []*importUnit
	byID       map[int64]*importUnit
	byUID      map[string]*importUnit
	links      []sqlitedb.Row
	media      []*importMedia
	mediaByOld map[int64]*importMedia
	files      map[string]*importFile
	tags       map[int64][]string
	missing    int64
	renumbered int64
}

func importRef(project, uid string) string { return "ark:" + project + "/" + uid }

func (p *importPlan) readSource(ctx context.Context, q sqlitedb.Querier, root string) error {
	p.byID, p.byUID = map[int64]*importUnit{}, map[string]*importUnit{}
	add := func(row sqlitedb.Row) bool {
		if _, ok := p.byID[row.Int("id")]; ok {
			return false
		}
		u := &importUnit{row: row, id: row.Int("id"), oldUID: row.Str("uid")}
		p.byID[u.id], p.byUID[u.oldUID] = u, u
		return true
	}
	var queue []int64
	for _, ref := range p.req.Units {
		row, err := unitRowAny(ctx, q, ref)
		if err != nil {
			return err
		}
		if row.Int("archived") != 0 {
			return storeErr("unit %s is in the recycle bin of the source project", row.Str("uid"))
		}
		if add(row) {
			queue = append(queue, row.Int("id"))
		}
	}
	if len(p.byID) == 0 {
		return storeErr("select at least one unit to import")
	}
	if p.req.WithChildren {
		parents, err := treeParents(ctx, q)
		if err != nil {
			return err
		}
		children := childrenOf(parents)
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			for _, c := range children[id] {
				row, err := unitRowAny(ctx, q, c)
				if err != nil {
					return err
				}
				if add(row) {
					queue = append(queue, c)
				}
			}
		}
	}
	for _, u := range p.byID {
		p.units = append(p.units, u)
	}
	sort.Slice(p.units, func(i, j int) bool { return p.units[i].id < p.units[j].id })
	ids := make([]any, len(p.units))
	for i, u := range p.units {
		ids[i] = u.id
	}
	in := "(" + sqlitedb.Marks(len(ids)) + ")"
	var err error
	if p.links, err = sqlitedb.Query(ctx, q, "SELECT * FROM unit_links WHERE source_id IN "+in+" AND target_id IN "+in+" ORDER BY id",
		append(ids, ids...)...); err != nil {
		return err
	}
	p.mediaByOld, p.files = map[int64]*importMedia{}, map[string]*importFile{}
	own, err := sqlitedb.Query(ctx, q, "SELECT * FROM unit_media WHERE unit_id IN "+in+" ORDER BY id", ids...)
	if err != nil {
		return err
	}
	for _, m := range own {
		im := &importMedia{row: m, oldID: m.Int("id"), owner: m.Int("unit_id")}
		p.media = append(p.media, im)
		p.mediaByOld[im.oldID] = im
	}
	for _, u := range p.units {
		for _, ref := range mediaRefs(u.row.Str("body")) {
			if _, ok := p.mediaByOld[ref]; ok {
				continue
			}
			m, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT * FROM unit_media WHERE id=?", ref)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			im := &importMedia{row: m, oldID: ref, owner: u.id}
			p.media = append(p.media, im)
			p.mediaByOld[ref] = im
		}
	}
	for _, m := range p.media {
		url := m.row.Str("url")
		if !projectRelative(url) {
			continue
		}
		if _, ok := p.files[url]; ok {
			continue
		}
		f := &importFile{src: filepath.Join(root, filepath.FromSlash(url)), rel: filepath.ToSlash(filepath.Clean(filepath.FromSlash(url)))}
		if fi, err := os.Stat(f.src); err == nil && fi.Mode().IsRegular() {
			f.size = fi.Size()
		} else {
			f.src = ""
			p.missing++
		}
		p.files[url] = f
	}
	p.tags = map[int64][]string{}
	rows, err := sqlitedb.Query(ctx, q, "SELECT unit_id, tag FROM unit_tags WHERE unit_id IN "+in+" ORDER BY unit_id, tag", ids...)
	if err != nil {
		return err
	}
	for _, r := range rows {
		p.tags[r.Int("unit_id")] = append(p.tags[r.Int("unit_id")], r.Str("tag"))
	}
	return nil
}

// readTarget finds earlier copies and numbers the new ones (D-0015).  An
// import runs it again inside its write transaction, so the uids are the
// ones free at that moment.
func (p *importPlan) readTarget(ctx context.Context, q sqlitedb.Querier) error {
	p.renumbered = 0
	for _, u := range p.units {
		u.existing, u.newUID, u.newID = false, "", 0
	}
	taken := map[string]bool{}
	rows, err := sqlitedb.Query(ctx, q, "SELECT id, uid, source_ref FROM units")
	if err != nil {
		return err
	}
	earlier := map[string]sqlitedb.Row{}
	for _, r := range rows {
		taken[r.Str("uid")] = true
		if ref := r.Str("source_ref"); ref != "" {
			earlier[ref] = r
		}
	}
	counters := map[string]int64{}
	crows, err := sqlitedb.Query(ctx, q, "SELECT prefix, n FROM counters")
	if err != nil {
		return err
	}
	for _, c := range crows {
		counters[c.Str("prefix")] = c.Int("n")
	}
	clash := false
	for _, u := range p.units {
		if r, ok := earlier[importRef(p.req.SourceID, u.oldUID)]; ok {
			u.existing, u.newUID, u.newID = true, r.Str("uid"), r.Int("id")
			continue
		}
		if taken[u.oldUID] {
			clash = true
		}
	}
	for _, u := range p.units {
		if u.existing {
			continue
		}
		if !clash {
			u.newUID = u.oldUID
			continue
		}
		prefix := typePrefix(u.row.Str("type"))
		n := counters[prefix] + 1
		for taken[prefix+"-"+pad4(n)] {
			n++
		}
		counters[prefix] = n
		u.newUID = prefix + "-" + pad4(n)
		taken[u.newUID] = true
		if u.newUID != u.oldUID {
			p.renumbered++
		}
	}
	return nil
}

func (p *importPlan) summary() *pyjson.Object {
	units := []any{}
	var count, skipped int64
	for _, u := range p.units {
		units = append(units, pyjson.NewObject("uid", u.oldUID, "new_uid", u.newUID, "type", u.row.Get("type"),
			"title", u.row.Get("title"), "skipped", u.existing))
		if u.existing {
			skipped++
		} else {
			count++
		}
	}
	var files, bytes int64
	for _, f := range p.files {
		if f.src != "" {
			files++
			bytes += f.size
		}
	}
	return pyjson.NewObject("source", p.req.SourceID, "source_name", p.req.SourceName, "units", units,
		"unit_count", count, "skipped", skipped, "renumbered", p.renumbered, "links", int64(len(p.links)),
		"media", int64(len(p.media)), "files", files, "bytes", bytes, "files_missing", p.missing)
}

// ImportFrom copies the requested units of src into s; dryRun only plans.
func (s *Store) ImportFrom(ctx context.Context, src *Store, req ImportRequest, dryRun bool, actor string) (*pyjson.Object, error) {
	return s.importFrom(ctx, src, req, dryRun, actor, nil)
}

// planImport reads both sides of an import without writing.
func (s *Store) planImport(ctx context.Context, src *Store, req ImportRequest) (*importPlan, error) {
	if src == s {
		return nil, storeErr("the source is this project")
	}
	p := &importPlan{req: req}
	if err := src.read(ctx, func(q sqlitedb.Querier) error { return p.readSource(ctx, q, src.Path) }); err != nil {
		return nil, err
	}
	if err := s.read(ctx, func(q sqlitedb.Querier) error { return p.readTarget(ctx, q) }); err != nil {
		return nil, err
	}
	return p, nil
}

// importFrom is ImportFrom; check (when set) sees the plan as numbered
// inside the write transaction and can refuse it.
func (s *Store) importFrom(ctx context.Context, src *Store, req ImportRequest, dryRun bool, actor string,
	check func(*importPlan) error) (*pyjson.Object, error) {
	p, err := s.planImport(ctx, src, req)
	if err != nil {
		return nil, err
	}
	if dryRun {
		return p.summary(), nil
	}
	if check != nil {
		if err := check(p); err != nil {
			return nil, err
		}
	}
	fresh := 0
	for _, u := range p.units {
		if !u.existing {
			fresh++
		}
	}
	if fresh == 0 {
		out := p.summary()
		out.Set("imported", int64(0))
		return out, nil
	}
	// Files first; removed again when the insert fails.
	var created []string
	cleanup := func() {
		for _, rel := range created {
			os.Remove(filepath.Join(s.Path, filepath.FromSlash(rel)))
		}
	}
	used := map[string]bool{}
	urls := make([]string, 0, len(p.files))
	for url := range p.files {
		urls = append(urls, url)
	}
	sort.Strings(urls)
	newURL := map[string]string{}
	for _, url := range urls {
		f := p.files[url]
		if f.src == "" {
			newURL[url] = f.rel
			continue
		}
		rel := freeRel(s.Path, f.rel, used)
		if err := linkFile(f.src, filepath.Join(s.Path, filepath.FromSlash(rel))); err != nil {
			cleanup()
			return nil, err
		}
		created = append(created, rel)
		newURL[url] = rel
	}
	for _, m := range p.media {
		m.newURL = m.row.Str("url")
		if n, ok := newURL[m.newURL]; ok {
			m.newURL = n
		}
	}
	if importFailHook != nil {
		if err := importFailHook(); err != nil {
			cleanup()
			return nil, err
		}
	}
	err = s.write(ctx, func(w *wtx) error {
		w.full = true
		// Number again here: units made since the preview keep their uids.
		if err := p.readTarget(ctx, w.q); err != nil {
			return err
		}
		if check != nil {
			if err := check(p); err != nil {
				return err
			}
		}
		fresh = 0
		for _, u := range p.units {
			if !u.existing {
				fresh++
			}
		}
		return p.insert(w, actor)
	})
	if err == nil && fresh == 0 {
		cleanup() // copied by someone else meanwhile: nothing new
	}
	if err != nil {
		cleanup()
		return nil, err
	}
	out := p.summary()
	out.Set("imported", int64(fresh))
	created2 := []any{}
	for _, u := range p.units {
		if u.existing || len(created2) >= 50 {
			continue
		}
		if full, err := s.GetUnit(ctx, strconv.FormatInt(u.newID, 10)); err == nil {
			created2 = append(created2, full)
		}
	}
	out.Set("created", created2)
	return out, nil
}

// importFailHook lets tests fail between the files and the insert.
var importFailHook func() error

func (p *importPlan) provenance(u *importUnit) string {
	ref := "[[" + p.req.SourceID + "/" + u.oldUID + "]]"
	name := p.req.SourceName
	if name == "" {
		name = p.req.SourceID
	}
	if p.req.Lang == "zh" {
		return "导入自 " + name + " " + ref
	}
	return "Imported from " + name + " " + ref
}

func (p *importPlan) insert(w *wtx, actor string) error {
	ctx := w.ctx
	now := clock.UTCNow()
	for _, u := range p.units {
		if u.existing {
			continue
		}
		r := u.row
		meta := parseDetail(r.Get("meta_json"))
		for _, k := range []string{"external_links", "moved_to", "moved_from", "imported_from"} {
			meta.Delete(k)
		}
		meta.Set("imported_from", pyjson.NewObject("project", p.req.SourceID, "project_name", p.req.SourceName,
			"uid", u.oldUID, "at", now))
		res, err := w.q.ExecContext(ctx, `INSERT INTO units(uid,type,title,body,status,importance,start_at,end_at,created_at,
                   updated_at,origin,source_ref,source_title,meta_json,archived,assignee) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,?)`,
			u.newUID, r.Get("type"), r.Get("title"), "", r.Get("status"), r.Get("importance"), r.Get("start_at"), r.Get("end_at"),
			now, now, "ark-import", importRef(p.req.SourceID, u.oldUID), p.req.SourceName, string(pyjson.Dumps(meta)), r.Get("assignee"))
		if err != nil {
			return err
		}
		if u.newID, err = res.LastInsertId(); err != nil {
			return err
		}
		for _, tag := range p.tags[u.id] {
			if _, err := w.q.ExecContext(ctx, "INSERT OR IGNORE INTO unit_tags(unit_id,tag) VALUES(?,?)", u.newID, tag); err != nil {
				return err
			}
		}
	}
	for _, m := range p.media {
		owner := p.byID[m.owner]
		if owner.existing {
			continue
		}
		res, err := w.q.ExecContext(ctx, "INSERT INTO unit_media(unit_id,url,kind,caption,created_at) VALUES(?,?,?,?,?)",
			owner.newID, m.newURL, m.row.Get("kind"), m.row.Get("caption"), m.row.Get("created_at"))
		if err != nil {
			return err
		}
		if m.newID, err = res.LastInsertId(); err != nil {
			return err
		}
	}
	ref := func(uid string) string {
		if u, ok := p.byUID[uid]; ok {
			return u.newUID
		}
		return p.req.SourceID + "/" + uid
	}
	media := func(id int64) int64 {
		if m, ok := p.mediaByOld[id]; ok {
			return m.newID
		}
		return 0
	}
	for _, u := range p.units {
		if u.existing {
			continue
		}
		body := strings.TrimRight(rewriteRefs(u.row.Str("body"), ref, media), "\n")
		if body != "" {
			body += "\n\n"
		}
		body += p.provenance(u)
		if _, err := w.q.ExecContext(ctx, "UPDATE units SET body=? WHERE id=?", body, u.newID); err != nil {
			return err
		}
	}
	for _, l := range p.links {
		a, b := p.byID[l.Int("source_id")], p.byID[l.Int("target_id")]
		if a.existing && b.existing {
			continue
		}
		if _, err := w.q.ExecContext(ctx, `INSERT OR IGNORE INTO unit_links(source_id,target_id,relation,note,created_at,created_by)
                   VALUES(?,?,?,?,?,?)`, a.newID, b.newID, l.Get("relation"), l.Get("note"), now, l.Get("created_by")); err != nil {
			return err
		}
	}
	for _, u := range p.units {
		if u.existing {
			continue
		}
		prefix := typePrefix(u.row.Str("type"))
		if n, err := strconv.ParseInt(strings.TrimPrefix(u.newUID, prefix+"-"), 10, 64); err == nil {
			if _, err := w.q.ExecContext(ctx, `INSERT INTO counters(prefix,n) VALUES(?,?)
                   ON CONFLICT(prefix) DO UPDATE SET n=MAX(n, excluded.n)`, prefix, n); err != nil {
				return err
			}
		}
		if err := w.log(actor, "unit.create", u.newID, pyjson.NewObject("uid", u.newUID, "title", u.row.Get("title"),
			"type", u.row.Get("type"), "imported_from", importRef(p.req.SourceID, u.oldUID))); err != nil {
			return err
		}
	}
	return nil
}

// ImportedFrom maps the uids of project's units that have a copy here (live
// or in the recycle bin) to the uid of the copy.
func (s *Store) ImportedFrom(ctx context.Context, project string) (*pyjson.Object, error) {
	out := pyjson.NewObject()
	prefix := importRef(project, "")
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		rows, err := sqlitedb.Query(ctx, q, "SELECT uid, source_ref FROM units WHERE substr(source_ref,1,?)=? ORDER BY id",
			int64(len(prefix)), prefix)
		for _, r := range rows {
			out.Set(strings.TrimPrefix(r.Str("source_ref"), prefix), r.Str("uid"))
		}
		return err
	})
	return out, err
}
