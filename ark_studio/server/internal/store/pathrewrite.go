package store

// Path rewrites across a project (Go only, 2026-11-24; server/routes_taskspace.go):
// the relocate tool and the absolute -> logical pass read every unit body
// and attachment link, compute the new text elsewhere (setup.RewritePrefix,
// setup.ToLogical) and write all changes in one transaction.  Each change
// carries the text it expects to replace, so a body edited in between is
// reported as a conflict instead of being overwritten; that same check makes
// undo safe.

import (
	"context"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/sqlitedb"
)

// UnitText is one live unit's body and its attachment links.
type UnitText struct {
	ID    int64
	UID   string
	Title string
	Body  string
	Media []MediaURL
}

// MediaURL is one attachment's id and URL.
type MediaURL struct {
	ID  int64
	URL string
}

// UnitTexts returns every unit outside the recycle bin with its body and
// attachment URLs (uploaded assets, which are project-relative, included).
func (s *Store) UnitTexts(ctx context.Context) ([]UnitText, error) {
	var out []UnitText
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		rows, err := sqlitedb.Query(ctx, q, "SELECT id, uid, title, body FROM units WHERE archived=0 ORDER BY id")
		if err != nil {
			return err
		}
		index := map[int64]int{}
		for _, r := range rows {
			index[r.Int("id")] = len(out)
			out = append(out, UnitText{ID: r.Int("id"), UID: pyjson.Str(r.Get("uid")), Title: pyjson.Str(r.Get("title")),
				Body: pyjson.Str(r.Get("body"))})
		}
		media, err := sqlitedb.Query(ctx, q, "SELECT id, unit_id, url FROM unit_media ORDER BY id")
		if err != nil {
			return err
		}
		for _, m := range media {
			if i, ok := index[m.Int("unit_id")]; ok {
				out[i].Media = append(out[i].Media, MediaURL{ID: m.Int("id"), URL: pyjson.Str(m.Get("url"))})
			}
		}
		return nil
	})
	return out, err
}

// TextEdit is one change: a unit body (MediaID 0) or an attachment URL,
// from Before to After.
type TextEdit struct {
	UID     string `json:"uid"`
	MediaID int64  `json:"media_id,omitempty"`
	Before  string `json:"before"`
	After   string `json:"after"`
}

// ApplyTextEdits writes edits in one transaction.  An edit whose current
// text is not Before is skipped and returned in conflicts (already After:
// counted as unchanged, so applying twice changes nothing).
func (s *Store) ApplyTextEdits(ctx context.Context, edits []TextEdit, batch, actor string) (applied, unchanged, conflicts []TextEdit, err error) {
	if batch == "" {
		batch = NewBatchID()
	}
	err = s.write(ctx, func(w *wtx) error {
		applied, unchanged, conflicts = nil, nil, nil
		w.batch = batch
		var touched []int64
		for _, e := range edits {
			row, err := unitRowAny(ctx, w.q, e.UID)
			if err != nil {
				if !isStoreErr(err) {
					return err
				}
				conflicts = append(conflicts, e)
				continue
			}
			if e.MediaID == 0 {
				cur := pyjson.Str(row.Get("body"))
				switch cur {
				case e.After:
					unchanged = append(unchanged, e)
					continue
				case e.Before:
				default:
					conflicts = append(conflicts, e)
					continue
				}
				w.noTouch, w.full, w.units = false, false, nil
				if _, err := s.updateUnitTx(w, row.Int("id"), pyjson.NewObject("body", e.After), actor, Who{}, "Move paths"); err != nil {
					if !isStoreErr(err) {
						return err
					}
					conflicts = append(conflicts, e)
					continue
				}
				applied = append(applied, e)
				touched = append(touched, row.Int("id"))
				continue
			}
			m, found, err := sqlitedb.QueryOne(ctx, w.q, "SELECT url FROM unit_media WHERE id=? AND unit_id=?", e.MediaID, row.Int("id"))
			if err != nil {
				return err
			}
			if !found {
				conflicts = append(conflicts, e)
				continue
			}
			switch pyjson.Str(m.Get("url")) {
			case e.After:
				unchanged = append(unchanged, e)
				continue
			case e.Before:
			default:
				conflicts = append(conflicts, e)
				continue
			}
			if _, err := w.q.ExecContext(ctx, "UPDATE unit_media SET url=? WHERE id=?", e.After, e.MediaID); err != nil {
				return err
			}
			if err := w.log(actor, "media.update", row.Int("id"), pyjson.NewObject("uid", row.Get("uid"),
				"media_id", e.MediaID, "from", e.Before, "to", e.After)); err != nil {
				return err
			}
			applied = append(applied, e)
			touched = append(touched, row.Int("id"))
		}
		w.units, w.full, w.noTouch = touched, false, len(touched) == 0
		return nil
	})
	return applied, unchanged, conflicts, err
}
