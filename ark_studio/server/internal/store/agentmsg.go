package store

// Go only, experimental (T-0044): messages from a person to their agents
// (docs/development/agent-messaging.md).  A message is a row of
// agent_messages, created on first use (it is not part of the Python
// schema, so projects that never use messaging keep the frozen schema), and
// an "agent.message" activity row, which is how it reaches the agents'
// GET /api/agent/v1/updates feed.  Replies append to the message's thread.

import (
	"context"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/sqlitedb"
)

// Message limits.
const (
	MaxMessageText   = 4000
	MaxMessageThread = 50
	MessageAction    = "agent.message"
	MessageReply     = "agent.message.reply"
)

// MessageStatuses in lifecycle order.
var MessageStatuses = []string{"queued", "delivered", "read", "answered"}

const agentMessagesSchema = `CREATE TABLE IF NOT EXISTS agent_messages(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  sender TEXT NOT NULL,
  owner TEXT NOT NULL,
  target TEXT NOT NULL,
  unit_id INTEGER,
  body TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'queued',
  created_at TEXT NOT NULL,
  delivered_at TEXT,
  delivered_to TEXT,
  read_at TEXT,
  answered_at TEXT,
  thread_json TEXT NOT NULL DEFAULT '[]'
);
CREATE INDEX IF NOT EXISTS agent_messages_owner ON agent_messages(owner, id);`

func hasMessagesTable(ctx context.Context, q sqlitedb.Querier) (bool, error) {
	_, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT 1 FROM sqlite_master WHERE type='table' AND name='agent_messages'")
	return ok, err
}

// MessageInput is a new message.
type MessageInput struct {
	Sender string // account name of the person
	Owner  string // whose agents receive it (the sender in this version)
	Target string // "mine", "token:<id>" or "label:<label>"
	Unit   string // optional unit ref
	Body   string
	Actor  string // activity actor (human:<name>)
}

func messageObject(ctx context.Context, q sqlitedb.Querier, r sqlitedb.Row) (*pyjson.Object, error) {
	var unit any
	if id, ok := r.Get("unit_id").(int64); ok {
		u, found, err := sqlitedb.QueryOne(ctx, q, "SELECT uid, title, type FROM units WHERE id=?", id)
		if err != nil {
			return nil, err
		}
		if found {
			unit = pyjson.NewObject("id", id, "uid", u.Get("uid"), "title", u.Get("title"), "type", u.Get("type"))
		}
	}
	thread, ok := pyjson.Loads(r.Str("thread_json"))
	if ok != nil {
		thread = []any{}
	}
	return pyjson.NewObject("id", r.Get("id"), "sender", r.Get("sender"), "owner", r.Get("owner"),
		"target", r.Get("target"), "unit", unit, "body", r.Get("body"), "status", r.Get("status"),
		"created_at", r.Get("created_at"), "delivered_at", r.Get("delivered_at"), "delivered_to", r.Get("delivered_to"),
		"read_at", r.Get("read_at"), "answered_at", r.Get("answered_at"), "thread", thread), nil
}

// CreateMessage stores a message and its activity row.
func (s *Store) CreateMessage(ctx context.Context, in MessageInput) (*pyjson.Object, error) {
	text := pytext.Strip(strings.ReplaceAll(in.Body, "\x00", ""))
	if text == "" {
		return nil, storeErr("message text is required")
	}
	if pytext.Len(text) > MaxMessageText {
		return nil, storeErr("message is longer than %d characters", MaxMessageText)
	}
	var out *pyjson.Object
	err := s.write(ctx, func(w *wtx) error {
		w.noTouch = true
		if _, err := w.q.ExecContext(ctx, agentMessagesSchema); err != nil {
			return err
		}
		var unitID any
		var uid, title any
		if in.Unit != "" {
			row, err := unitRow(ctx, w.q, in.Unit)
			if err != nil {
				return err
			}
			unitID, uid, title = row.Int("id"), row.Get("uid"), row.Get("title")
		}
		res, err := w.q.ExecContext(ctx, `INSERT INTO agent_messages(sender,owner,target,unit_id,body,status,created_at)
               VALUES(?,?,?,?,?,'queued',?)`, in.Sender, in.Owner, in.Target, unitID, text, clock.UTCNow())
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		// The detail carries no text: activity is visible to every member.
		detail := pyjson.NewObject("message_id", id, "owner", in.Owner, "target", in.Target)
		if uid != nil {
			detail.Set("uid", uid).Set("title", title)
		}
		if err := w.log(in.Actor, MessageAction, unitID, detail); err != nil {
			return err
		}
		r, _, err := sqlitedb.QueryOne(ctx, w.q, "SELECT * FROM agent_messages WHERE id=?", id)
		if err != nil {
			return err
		}
		out, err = messageObject(ctx, w.q, r)
		return err
	})
	return out, err
}

// MessageFilter selects messages.
type MessageFilter struct {
	Owner  string
	Sender string // "" = any
	Unit   string // "" = any
	IDs    []int64
	Limit  int
}

// Messages lists messages, newest first.
func (s *Store) Messages(ctx context.Context, f MessageFilter) ([]any, error) {
	out := []any{}
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		if ok, err := hasMessagesTable(ctx, q); err != nil || !ok {
			return err
		}
		where := []string{"owner=?"}
		args := []any{f.Owner}
		if f.Sender != "" {
			where, args = append(where, "sender=?"), append(args, f.Sender)
		}
		if f.Unit != "" {
			row, err := unitRow(ctx, q, f.Unit)
			if err != nil {
				return err
			}
			where, args = append(where, "unit_id=?"), append(args, row.Int("id"))
		}
		if len(f.IDs) > 0 {
			marks := sqlitedb.Marks(len(f.IDs))
			where = append(where, "id IN ("+marks+")")
			for _, id := range f.IDs {
				args = append(args, id)
			}
		}
		limit := f.Limit
		if limit <= 0 || limit > 200 {
			limit = 50
		}
		rows, err := sqlitedb.Query(ctx, q, "SELECT * FROM agent_messages WHERE "+strings.Join(where, " AND ")+
			" ORDER BY id DESC LIMIT ?", append(args, limit)...)
		if err != nil {
			return err
		}
		for _, r := range rows {
			o, err := messageObject(ctx, q, r)
			if err != nil {
				return err
			}
			out = append(out, o)
		}
		return nil
	})
	return out, err
}

// Message reads one message.
func (s *Store) Message(ctx context.Context, id int64) (*pyjson.Object, bool, error) {
	var out *pyjson.Object
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		if ok, err := hasMessagesTable(ctx, q); err != nil || !ok {
			return err
		}
		r, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT * FROM agent_messages WHERE id=?", id)
		if err != nil || !ok {
			return err
		}
		out, err = messageObject(ctx, q, r)
		return err
	})
	return out, out != nil, err
}

// MarkMessagesDelivered moves queued messages to delivered (to: who got
// them).  It is not an activity: delivery happens inside a read.
func (s *Store) MarkMessagesDelivered(ctx context.Context, ids []int64, to string) error {
	if len(ids) == 0 {
		return nil
	}
	return s.write(ctx, func(w *wtx) error {
		w.noTouch = true
		args := []any{clock.UTCNow(), to}
		for _, id := range ids {
			args = append(args, id)
		}
		_, err := w.q.ExecContext(ctx, "UPDATE agent_messages SET status='delivered', delivered_at=?, delivered_to=? "+
			"WHERE status='queued' AND id IN ("+sqlitedb.Marks(len(ids))+")", args...)
		return err
	})
}

// AckMessage marks a message read (by: the agent actor).
func (s *Store) AckMessage(ctx context.Context, id int64, by string) (*pyjson.Object, error) {
	return s.messageUpdate(ctx, id, func(w *wtx, r sqlitedb.Row) error {
		if r.Str("status") == "answered" || r.Str("status") == "read" {
			return nil
		}
		_, err := w.q.ExecContext(ctx, "UPDATE agent_messages SET status='read', read_at=?, "+
			"delivered_at=COALESCE(delivered_at, ?), delivered_to=COALESCE(delivered_to, ?) WHERE id=?",
			clock.UTCNow(), clock.UTCNow(), by, id)
		return err
	})
}

// ReplyMessage appends a reply to the thread and marks the message answered.
func (s *Store) ReplyMessage(ctx context.Context, id int64, body, by string) (*pyjson.Object, error) {
	text := pytext.Strip(strings.ReplaceAll(body, "\x00", ""))
	if text == "" {
		return nil, storeErr("reply text is required")
	}
	if pytext.Len(text) > MaxMessageText {
		return nil, storeErr("reply is longer than %d characters", MaxMessageText)
	}
	return s.messageUpdate(ctx, id, func(w *wtx, r sqlitedb.Row) error {
		thread, _ := pyjson.Loads(r.Str("thread_json"))
		items, _ := thread.([]any)
		if len(items) >= MaxMessageThread {
			return storeErr("this message thread is full")
		}
		now := clock.UTCNow()
		items = append(items, pyjson.NewObject("by", by, "body", text, "at", now))
		if _, err := w.q.ExecContext(ctx, "UPDATE agent_messages SET status='answered', answered_at=?, "+
			"read_at=COALESCE(read_at, ?), thread_json=? WHERE id=?", now, now, string(pyjson.Dumps(items)), id); err != nil {
			return err
		}
		var unitID any
		if u, ok := r.Get("unit_id").(int64); ok {
			unitID = u
		}
		return w.log(by, MessageReply, unitID, pyjson.NewObject("message_id", id, "owner", r.Get("owner")))
	})
}

func (s *Store) messageUpdate(ctx context.Context, id int64, fn func(w *wtx, r sqlitedb.Row) error) (*pyjson.Object, error) {
	var out *pyjson.Object
	err := s.write(ctx, func(w *wtx) error {
		w.noTouch = true
		if ok, err := hasMessagesTable(ctx, w.q); err != nil {
			return err
		} else if !ok {
			return storeErr("message not found: %d", id)
		}
		r, ok, err := sqlitedb.QueryOne(ctx, w.q, "SELECT * FROM agent_messages WHERE id=?", id)
		if err != nil {
			return err
		}
		if !ok {
			return storeErr("message not found: %d", id)
		}
		if err := fn(w, r); err != nil {
			return err
		}
		fresh, _, err := sqlitedb.QueryOne(ctx, w.q, "SELECT * FROM agent_messages WHERE id=?", id)
		if err != nil {
			return err
		}
		out, err = messageObject(ctx, w.q, fresh)
		return err
	})
	return out, err
}

// MessagesSentSince counts sender's messages created at or after since
// (an ISO time) in this project: the rate limit.
func (s *Store) MessagesSentSince(ctx context.Context, sender, since string) (int64, error) {
	var n int64
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		if ok, err := hasMessagesTable(ctx, q); err != nil || !ok {
			return err
		}
		r, _, err := sqlitedb.QueryOne(ctx, q, "SELECT COUNT(*) n FROM agent_messages WHERE sender=? AND created_at>=?", sender, since)
		n = r.Int("n")
		return err
	})
	return n, err
}
