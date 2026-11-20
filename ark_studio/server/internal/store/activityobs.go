package store

import (
	"context"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/sqlitedb"
)

// Activity observers (Go only, T-0043 agent presence): a request may carry
// a callback that sees every activity row its writes log, with the unit it
// names.  The server uses it to show which agent works on which node; the
// store itself keeps nothing.

// ActivityEvent is one logged activity row.
type ActivityEvent struct {
	Actor  string
	Action string
	Unit   int64 // the unit's row id, 0 when the row names none
	UID    string
	Title  string
	Status string // the new status when the row records a status change
}

type activityObserverKey struct{}

// WithActivityObserver returns ctx with fn called for every activity row a
// write under ctx logs (after the row is inserted, inside the write).  fn
// must be fast and must not use the store.
func WithActivityObserver(ctx context.Context, fn func(ActivityEvent)) context.Context {
	return context.WithValue(ctx, activityObserverKey{}, fn)
}

func observeActivity(ctx context.Context, actor, action string, unitID any, detail *pyjson.Object) {
	fn, _ := ctx.Value(activityObserverKey{}).(func(ActivityEvent))
	if fn == nil {
		return
	}
	ev := ActivityEvent{Actor: actor, Action: action}
	switch v := unitID.(type) {
	case int64:
		ev.Unit = v
	case int:
		ev.Unit = int64(v)
	}
	if detail != nil {
		if s, ok := detail.Value("uid").(string); ok {
			ev.UID = s
		}
		if s, ok := detail.Value("title").(string); ok {
			ev.Title = s
		}
		if st, ok := detail.Value("status").(*pyjson.Object); ok {
			ev.Status, _ = st.Value("to").(string)
		}
	}
	fn(ev)
}

// UnitBrief is a unit's row id, uid and title by id or uid (a store.Error
// when there is no such unit).
func (s *Store) UnitBrief(ctx context.Context, ref string) (id int64, uid, title string, err error) {
	err = s.read(ctx, func(q sqlitedb.Querier) error {
		row, err := unitRow(ctx, q, ref)
		if err != nil {
			return err
		}
		id, uid, title = row.Int("id"), row.Str("uid"), row.Str("title")
		return nil
	})
	return id, uid, title, err
}
