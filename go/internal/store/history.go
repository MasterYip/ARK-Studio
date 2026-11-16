package store

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"arkstudio/go/internal/nodehist"
	"arkstudio/go/internal/pydiff"
	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/sqlitedb"
)

// unixNow is time.time().
func unixNow() float64 { return float64(Clock().UnixNano()) / 1e9 }

// recordTextHistory is Store._record_text_history (inside a write).
func (s *Store) recordTextHistory(w *wtx, unitID int64, uid, title, body, actor, message string) error {
	state, ok, err := sqlitedb.QueryOne(w.ctx, w.q, "SELECT * FROM node_text_history WHERE unit_id=?", unitID)
	if err != nil {
		return err
	}
	if !ok || state.Int("enabled") == 0 {
		return nil
	}
	if message == "Save text" && strings.HasPrefix(actor, "human:") {
		if _, err := w.q.ExecContext(w.ctx, "INSERT INTO node_text_pending(unit_id,edited_at,actor) VALUES(?,?,?) "+
			"ON CONFLICT(unit_id) DO UPDATE SET edited_at=excluded.edited_at,actor=excluded.actor",
			unitID, unixNow(), actor); err != nil {
			return err
		}
		s.startHistoryWorker()
		return nil
	}
	head, err := s.history().Record(w.ctx, uid, title, body, state.Str("head"), actor, message)
	if err != nil {
		if he, ok := err.(*nodehist.Error); ok {
			return storeErr("%s", he.Message)
		}
		return err
	}
	if _, err := w.q.ExecContext(w.ctx, "UPDATE node_text_history SET head=? WHERE unit_id=?", head, unitID); err != nil {
		return err
	}
	_, err = w.q.ExecContext(w.ctx, "DELETE FROM node_text_pending WHERE unit_id=?", unitID)
	return err
}

func (s *Store) history() *nodehist.History {
	return &nodehist.History{Path: filepath.Join(s.Path, "history.git")}
}

// startHistoryWorker is Store._start_history_worker: flush pending human
// edits every 15 s (see flushNodeVersions).
func (s *Store) startHistoryWorker() {
	s.historyOnce.Do(func() {
		go func() {
			t := time.NewTicker(15 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-s.closing:
					return
				case <-t.C:
					s.FlushNodeVersions(context.Background(), unixNow()) //nolint:errcheck
				}
			}
		}()
	})
}

// FlushNodeVersions is Store.flush_node_versions: save a revision for human
// edits that paused for 5 minutes and changed enough text.
func (s *Store) FlushNodeVersions(ctx context.Context, now float64) (int, error) {
	var pending []sqlitedb.Row
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		var err error
		pending, err = sqlitedb.Query(ctx, q, "SELECT p.unit_id FROM node_text_pending p "+
			"JOIN node_text_history h ON h.unit_id=p.unit_id "+
			"WHERE h.enabled=1 AND p.edited_at<=? "+
			"ORDER BY p.checked_at,p.unit_id LIMIT 20", now-300)
		return err
	})
	if err != nil {
		return 0, err
	}
	saved := 0
	for _, item := range pending {
		select {
		case <-s.closing:
			return saved, nil
		default:
		}
		unitID := item.Int("unit_id")
		err := s.write(ctx, func(w *wtx) error {
			w.noTouch = true
			q := w.q
			if _, err := q.ExecContext(ctx, "UPDATE node_text_history SET head=head WHERE unit_id=?", unitID); err != nil {
				return err
			}
			prow, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT * FROM node_text_pending WHERE unit_id=?", unitID)
			if err != nil {
				return err
			}
			if !ok || toFloat(prow.Get("edited_at")) > now-300 {
				return nil
			}
			row, err := unitRowAny(ctx, q, unitID)
			if err != nil {
				return err
			}
			state, _, err := sqlitedb.QueryOne(ctx, q, "SELECT * FROM node_text_history WHERE unit_id=?", unitID)
			if err != nil {
				return err
			}
			if state.Int("enabled") == 0 {
				return nil
			}
			if _, err := q.ExecContext(ctx, "UPDATE node_text_pending SET checked_at=? WHERE unit_id=?", now, unitID); err != nil {
				return err
			}
			baseline, err := s.nodeRevision(ctx, q, unitID, state.Str("head"))
			if err != nil {
				return err
			}
			bt, bb := []rune(pyjson.Str(baseline.Value("title"))), []rune(pyjson.Str(baseline.Value("body")))
			size := len(bt) + len(bb)
			threshold := min(100, max(1, (size+9)/10))
			changed := 0
			for _, f := range [][2][]rune{{bt, []rune(row.Str("title"))}, {bb, []rune(row.Str("body"))}} {
				old, nw := f[0], f[1]
				start := 0
				for start < min(len(old), len(nw)) && old[start] == nw[start] {
					start++
				}
				old, nw = old[start:], nw[start:]
				end := 0
				for end < min(len(old), len(nw)) && old[len(old)-1-end] == nw[len(nw)-1-end] {
					end++
				}
				if end > 0 {
					old, nw = old[:len(old)-end], nw[:len(nw)-end]
				}
				if len(old)+len(nw) > 20000 {
					changed += max(len(old), len(nw))
				} else {
					for _, op := range pydiff.Opcodes(old, nw) {
						if op.Tag != "equal" {
							changed += max(op.I2-op.I1, op.J2-op.J1)
						}
					}
				}
			}
			if changed == 0 {
				_, err := q.ExecContext(ctx, "DELETE FROM node_text_pending WHERE unit_id=?", unitID)
				return err
			}
			if changed >= threshold {
				if err := s.recordTextHistory(w, unitID, row.Str("uid"), row.Str("title"), row.Str("body"),
					prow.Str("actor"), "Save after editing pause"); err != nil {
					return err
				}
				saved++
			}
			return nil
		})
		if err != nil {
			return saved, err
		}
	}
	return saved, nil
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	}
	return 0
}

// nodeRevision is Store.node_revision inside a transaction.
func (s *Store) nodeRevision(ctx context.Context, q sqlitedb.Querier, unitID int64, commit string) (*pyjson.Object, error) {
	state, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT head FROM node_text_history WHERE unit_id=?", unitID)
	if err != nil {
		return nil, err
	}
	if !ok || state.Str("head") == "" {
		return nil, storeErr("No text history for this node")
	}
	rev, err := s.history().Revision(ctx, state.Str("head"), commit)
	if err != nil {
		if he, ok := err.(*nodehist.Error); ok {
			return nil, storeErr("%s", he.Message)
		}
		return nil, err
	}
	return rev, nil
}
