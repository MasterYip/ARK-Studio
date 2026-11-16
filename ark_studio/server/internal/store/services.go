package store

import (
	"context"
	"strconv"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/sqlitedb"
)

// GetUnitAny is Store.get_unit(ref) for a ref decoded from JSON (an int, a
// digit string or a uid; other values are str()-ed like Python does).
func (s *Store) GetUnitAny(ctx context.Context, ref any) (*pyjson.Object, error) {
	if text, ok := ref.(string); ok {
		return s.GetUnit(ctx, text)
	}
	var id int64
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		row, err := unitRowAny(ctx, q, ref)
		if err != nil {
			return err
		}
		id = row.Int("id")
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetUnit(ctx, strconv.FormatInt(id, 10))
}

// ReadQuery runs fn on a read connection of the project database (the raw
// `store.con.execute(...)` reads of importer.py).
func (s *Store) ReadQuery(ctx context.Context, fn func(q sqlitedb.Querier) error) error {
	return s.read(ctx, fn)
}
