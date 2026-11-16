package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"arkstudio/go/internal/nodehist"
	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pytext"
	"arkstudio/go/internal/sqlitedb"
)

// Node text history API of store.py: node_history, set_node_history,
// node_revision, node_diff, save_node_version, restore_node_revision,
// apply_node_history_change.

// textHash is Store._text_hash.
func textHash(row sqlitedb.Row) string {
	sum := sha256.Sum256(pyjson.Dumps([]any{row.Get("title"), row.Get("body")}))
	return hex.EncodeToString(sum[:])
}

func historyErr(err error) error {
	if he, ok := err.(*nodehist.Error); ok {
		return storeErr("%s", he.Message)
	}
	return err
}

// hashArg is a revision argument: a non-string never matches the hash
// pattern, so it fails validation like Python's isinstance check.
func hashArg(v any) string {
	s, ok := v.(string)
	if !ok {
		return "\x00"
	}
	return s
}

// NodeHistory is Store.node_history(ref, limit, offset).
func (s *Store) NodeHistory(ctx context.Context, ref any, limit, offset int64) (*pyjson.Object, error) {
	if limit < 1 || limit > 200 || offset < 0 {
		return nil, storeErr("History limit must be 1..200 and offset must be non-negative")
	}
	var out *pyjson.Object
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		row, err := unitRowAny(ctx, q, ref)
		if err != nil {
			return err
		}
		state, hasState, err := sqlitedb.QueryOne(ctx, q, "SELECT * FROM node_text_history WHERE unit_id=?", row.Int("id"))
		if err != nil {
			return err
		}
		var head any
		if hasState {
			head = state.Get("head")
		}
		available := nodehist.Available()
		versions := []any{}
		if h, _ := head.(string); h != "" && available {
			if versions, err = s.history().Versions(ctx, h, limit+1, offset); err != nil {
				return historyErr(err)
			}
		}
		media, err := sqlitedb.Query(ctx, q, "SELECT id,url,kind FROM unit_media WHERE unit_id=?", row.Int("id"))
		if err != nil {
			return err
		}
		kinds := pyjson.NewObject()
		for _, m := range media {
			kind := mediaPreviewKind(m.Get("kind"), m.Get("url"), false)
			var v any = kind
			if kind == "" {
				v = m.Get("kind")
			}
			kinds.Set(pyjson.Str(m.Get("id")), v)
		}
		_, pending, err := sqlitedb.QueryOne(ctx, q, "SELECT 1 FROM node_text_pending WHERE unit_id=?", row.Int("id"))
		if err != nil {
			return err
		}
		shown := versions
		if int64(len(shown)) > limit {
			shown = shown[:limit]
		}
		out = pyjson.NewObject("id", row.Get("id"), "uid", row.Get("uid"),
			"enabled", hasState && state.Int("enabled") != 0, "available", available, "head", head,
			"media_kinds", kinds, "current_hash", textHash(row), "versions", shown, "pending", pending,
			"has_more", int64(len(versions)) > limit, "limit", limit, "offset", offset)
		return nil
	})
	return out, err
}

// SetNodeHistory is Store.set_node_history.
func (s *Store) SetNodeHistory(ctx context.Context, ref, enabled any, actor string) (*pyjson.Object, error) {
	on, ok := enabled.(bool)
	if !ok {
		return nil, storeErr("enabled must be a boolean")
	}
	err := s.write(ctx, func(w *wtx) error {
		row, err := unitRowAny(ctx, w.q, ref)
		if err != nil {
			return err
		}
		if _, err := w.q.ExecContext(ctx, "INSERT OR IGNORE INTO node_text_history(unit_id) VALUES(?)", row.Int("id")); err != nil {
			return err
		}
		if row, err = unitRowAny(ctx, w.q, ref); err != nil {
			return err
		}
		if _, err := w.q.ExecContext(ctx, "UPDATE node_text_history SET enabled=? WHERE unit_id=?", b2i(on), row.Int("id")); err != nil {
			return err
		}
		if on {
			if err := s.recordTextHistory(w, row.Int("id"), row.Str("uid"), row.Str("title"), row.Str("body"), actor, "Enable history"); err != nil {
				return err
			}
		}
		return w.log(actor, "unit.history", row.Int("id"), pyjson.NewObject("uid", row.Get("uid"), "enabled", on))
	})
	if err != nil {
		return nil, err
	}
	return s.NodeHistory(ctx, ref, 50, 0)
}

// NodeRevision is Store.node_revision.
func (s *Store) NodeRevision(ctx context.Context, ref, commit any) (*pyjson.Object, error) {
	var out *pyjson.Object
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		row, err := unitRowAny(ctx, q, ref)
		if err != nil {
			return err
		}
		out, err = s.nodeRevision(ctx, q, row.Int("id"), hashArg(commit))
		return err
	})
	return out, err
}

func (s *Store) nodeDiff(ctx context.Context, q sqlitedb.Querier, row sqlitedb.Row, before, after any) (*pyjson.Object, error) {
	state, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT head,enabled FROM node_text_history WHERE unit_id=?", row.Int("id"))
	if err != nil {
		return nil, err
	}
	if !ok || state.Str("head") == "" {
		return nil, storeErr("No text history for this node")
	}
	b, a := "", ""
	if pyjson.Truthy(before) {
		b = hashArg(before)
	}
	if pyjson.Truthy(after) {
		a = hashArg(after)
	}
	result, err := s.history().Diff(ctx, state.Str("head"), b, a)
	if err != nil {
		return nil, historyErr(err)
	}
	_, pending, err := sqlitedb.QueryOne(ctx, q, "SELECT 1 FROM node_text_pending WHERE unit_id=?", row.Int("id"))
	if err != nil {
		return nil, err
	}
	canApply := state.Int("enabled") != 0 && result.Value("to") == state.Str("head") && pyjson.Truthy(result.Value("from")) && !pending
	return result.Set("current_hash", textHash(row)).Set("can_apply", canApply), nil
}

// NodeDiff is Store.node_diff.
func (s *Store) NodeDiff(ctx context.Context, ref, before, after any) (*pyjson.Object, error) {
	var out *pyjson.Object
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		row, err := unitRowAny(ctx, q, ref)
		if err != nil {
			return err
		}
		out, err = s.nodeDiff(ctx, q, row, before, after)
		return err
	})
	return out, err
}

// lockedRow re-reads the unit after taking SQLite's writer lock (lockSQL)
// and checks expected_hash and that history is enabled.
func lockedRow(w *wtx, ref any, lockSQL string, expected any, changedMsg, disabledMsg string) (sqlitedb.Row, error) {
	row, err := unitRowAny(w.ctx, w.q, ref)
	if err != nil {
		return row, err
	}
	if _, err := w.q.ExecContext(w.ctx, lockSQL, row.Int("id")); err != nil {
		return row, err
	}
	if row, err = unitRowAny(w.ctx, w.q, ref); err != nil {
		return row, err
	}
	if !pyjson.Truthy(expected) || !PyEqual(textHash(row), expected) {
		return row, storeErr("%s", changedMsg)
	}
	state, ok, err := sqlitedb.QueryOne(w.ctx, w.q, "SELECT enabled FROM node_text_history WHERE unit_id=?", row.Int("id"))
	if err != nil {
		return row, err
	}
	if !ok || state.Int("enabled") == 0 {
		return row, storeErr("%s", disabledMsg)
	}
	return row, nil
}

// SaveNodeVersion is Store.save_node_version.
func (s *Store) SaveNodeVersion(ctx context.Context, ref, expected any, actor string) (*pyjson.Object, error) {
	err := s.write(ctx, func(w *wtx) error {
		w.noTouch = true
		row, err := lockedRow(w, ref, "UPDATE node_text_history SET head=head WHERE unit_id=?", expected,
			"Node text changed; reload its history before saving a version", "Enable history before saving a version")
		if err != nil {
			return err
		}
		return s.recordTextHistory(w, row.Int("id"), row.Str("uid"), row.Str("title"), row.Str("body"), actor, "Manual checkpoint")
	})
	if err != nil {
		return nil, err
	}
	return s.NodeHistory(ctx, ref, 50, 0)
}

// RestoreNodeRevision is Store.restore_node_revision; it returns get_unit.
func (s *Store) RestoreNodeRevision(ctx context.Context, ref, commit, expected any, actor string) (*pyjson.Object, error) {
	var unitID int64
	err := s.write(ctx, func(w *wtx) error {
		row, err := lockedRow(w, ref, "UPDATE node_text_history SET enabled=enabled WHERE unit_id=?", expected,
			"Node text changed; reload its history before restoring", "Enable history before restoring a revision")
		if err != nil {
			return err
		}
		c := hashArg(commit)
		revision, err := s.nodeRevision(ctx, w.q, row.Int("id"), c)
		if err != nil {
			return err
		}
		patch := pyjson.NewObject("title", revision.Value("title"), "body", revision.Value("body"))
		unitID, err = s.updateUnitTx(w, ref, patch, actor, Who{}, "Restore "+pytext.Prefix(c, 12))
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetUnit(ctx, strconv.FormatInt(unitID, 10))
}

// ApplyNodeHistoryChange is Store.apply_node_history_change.
func (s *Store) ApplyNodeHistoryChange(ctx context.Context, ref, before, after, file, change, expected any, actor string) (*pyjson.Object, error) {
	f, ok := file.(string)
	if !ok || (f != "title.txt" && f != "body.md") {
		return nil, storeErr("History file must be title.txt or body.md")
	}
	var changeN int64
	hasChange := change != nil
	if hasChange {
		n, isInt := change.(int64)
		if !isInt {
			big, isBig := change.(interface{ String() string })
			if !isBig || strings.HasPrefix(big.String(), "-") {
				return nil, storeErr("History change must be a non-negative integer")
			}
			n = -1 // a huge int never matches a block index
		} else if n < 0 {
			return nil, storeErr("History change must be a non-negative integer")
		}
		changeN = n
	}
	var unitID int64
	err := s.write(ctx, func(w *wtx) error {
		row, err := lockedRow(w, ref, "UPDATE node_text_history SET enabled=enabled WHERE unit_id=?", expected,
			"Node text changed; reload its history before applying a change", "Enable history before applying a change")
		if err != nil {
			return err
		}
		b, a := hashArg(before), hashArg(after)
		original, err := s.nodeRevision(ctx, w.q, row.Int("id"), b)
		if err != nil {
			return err
		}
		target, err := s.nodeRevision(ctx, w.q, row.Int("id"), a)
		if err != nil {
			return err
		}
		if target.Value("title") != row.Get("title") || target.Value("body") != row.Get("body") {
			return storeErr("Choose the current version as To before applying a change")
		}
		field := "body"
		if f == "title.txt" {
			field = "title"
		}
		text := pyjson.Str(original.Value(field))
		if hasChange {
			diff, err := s.nodeDiff(ctx, w.q, row, b, a)
			if err != nil {
				return err
			}
			var block *pyjson.Object
			for _, item := range diff.Value("changes").([]any) {
				o := item.(*pyjson.Object)
				if o.Value("file") == f && o.Value("index") == changeN {
					block = o
					break
				}
			}
			if block == nil {
				return storeErr("History change not found; reload the diff")
			}
			text = nodehist.ReapplyBlock(pyjson.Str(original.Value(field)), pyjson.Str(target.Value(field)), block)
		}
		message := "Apply " + f + " from " + pytext.Prefix(b, 12)
		if hasChange {
			message += " change " + pyjson.Str(change)
		}
		unitID, err = s.updateUnitTx(w, ref, pyjson.NewObject(field, text), actor, Who{}, message)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetUnit(ctx, strconv.FormatInt(unitID, 10))
}
