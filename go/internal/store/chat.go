package store

import (
	"context"
	"strconv"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/sqlitedb"
)

// Assistant chat history is per user and project (schema v10): a user's
// thread is their own rows plus shared rows ('' user, written before v10)
// newer than their meta key chat_cleared:<user>.

func chatClearedKey(user string) string { return "chat_cleared:" + user }

func metaInt(ctx context.Context, q sqlitedb.Querier, key string) (int64, error) {
	v, ok, err := MetaGet(ctx, q, key)
	if err != nil || !ok || v == "" {
		return 0, err
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, &InternalError{"invalid literal for int() with base 10: '" + v + "'"}
	}
	return n, nil
}

// ClearChat is Store.clear_chat(user): deletes the user's own rows, hides
// the older shared rows for them, and returns how many left the view.
func (s *Store) ClearChat(ctx context.Context, user string) (int64, error) {
	var n int64
	err := s.write(ctx, func(w *wtx) error {
		w.noTouch = true
		cleared, err := metaInt(ctx, w.q, chatClearedKey(user))
		if err != nil {
			return err
		}
		if err := w.q.QueryRowContext(ctx, "SELECT COUNT(*) FROM chat_messages WHERE (user=? OR user='') AND id>?",
			user, cleared).Scan(&n); err != nil {
			return err
		}
		var last int64
		if err := w.q.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM chat_messages").Scan(&last); err != nil {
			return err
		}
		if _, err := w.q.ExecContext(ctx, "DELETE FROM chat_messages WHERE user=?", user); err != nil {
			return err
		}
		return MetaSet(ctx, w.q, chatClearedKey(user), strconv.FormatInt(last, 10))
	})
	return n, err
}

// ChatClearDetail is the activity detail of chat.clear.
func ChatClearDetail(n int64) *pyjson.Object { return pyjson.NewObject("messages", n) }

// chatRow is Store._chat_row.
func chatRow(r sqlitedb.Row) *pyjson.Object {
	var tools any = []any{}
	if raw := r.Get("tools_json"); pyjson.Truthy(raw) {
		if v, err := pyjson.Loads(pyjson.Str(raw)); err == nil {
			tools = v
		}
	}
	if _, ok := tools.([]any); !ok {
		tools = []any{}
	}
	var meta any = pyjson.NewObject()
	if raw := r.Get("meta_json"); pyjson.Truthy(raw) {
		if v, err := pyjson.Loads(pyjson.Str(raw)); err == nil {
			meta = v
		}
	}
	if _, ok := meta.(*pyjson.Object); !ok {
		meta = pyjson.NewObject()
	}
	return pyjson.NewObject("id", r.Get("id"), "at", r.Get("at"), "role", r.Get("role"),
		"content", r.Get("content"), "tools", tools, "meta", meta)
}

// ChatHistory is Store.chat_history(limit, user); user nil is every message.
func (s *Store) ChatHistory(ctx context.Context, limit int64, user *string) ([]*pyjson.Object, error) {
	limit = max(1, min(limit, 400))
	var out []*pyjson.Object
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		var rows []sqlitedb.Row
		var err error
		if user == nil {
			rows, err = sqlitedb.Query(ctx, q, "SELECT * FROM (SELECT * FROM chat_messages ORDER BY id DESC LIMIT ?) ORDER BY id ASC", limit)
		} else {
			var cleared int64
			if cleared, err = metaInt(ctx, q, chatClearedKey(*user)); err != nil {
				return err
			}
			rows, err = sqlitedb.Query(ctx, q, "SELECT * FROM (SELECT * FROM chat_messages WHERE (user=? OR user='') AND id>? "+
				"ORDER BY id DESC LIMIT ?) ORDER BY id ASC", *user, cleared, limit)
		}
		if err != nil {
			return err
		}
		out = make([]*pyjson.Object, 0, len(rows))
		for _, r := range rows {
			out = append(out, chatRow(r))
		}
		return nil
	})
	return out, err
}

// AddChat is Store.add_chat(role, content, tools, meta, user).
func (s *Store) AddChat(ctx context.Context, role, content string, tools []any, meta *pyjson.Object, user string) (*pyjson.Object, error) {
	if tools == nil {
		tools = []any{}
	}
	if meta == nil {
		meta = pyjson.NewObject()
	}
	var out *pyjson.Object
	err := s.write(ctx, func(w *wtx) error {
		w.noTouch = true
		res, err := w.q.ExecContext(ctx, "INSERT INTO chat_messages(at,role,content,tools_json,meta_json,user) VALUES(?,?,?,?,?,?)",
			UTCNow(), role, content, string(pyjson.Dumps(tools)), string(pyjson.Dumps(meta)), user)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		row, _, err := sqlitedb.QueryOne(ctx, w.q, "SELECT * FROM chat_messages WHERE id=?", id)
		if err != nil {
			return err
		}
		out = chatRow(row)
		return nil
	})
	return out, err
}

// NextActions is Store.next_actions().
func (s *Store) NextActions(ctx context.Context) ([]any, error) {
	var out []any
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		var err error
		out, err = s.nextActions(ctx, q)
		return err
	})
	return out, err
}
