package store

// Go only (T-0044): the raw material of GET /api/agent/v1/updates.  The
// activity table is the feed: its AUTOINCREMENT id is the cursor position
// and its actor tells humans ("human:...", plain account names) from agents
// ("agent:...").  internal/agentwatch turns these rows into update items;
// this file only reads them, joined with the unit and comment they concern,
// and offers a wake-up signal for long polls.

import (
	"context"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/sqlitedb"
)

// UpdateComment is the comment an activity row (comment.*) refers to.
type UpdateComment struct {
	ID            int64
	Author        string
	Kind          string
	Body          string
	ReplyTo       int64 // 0 = none
	ReplyToAuthor string
	ReplyToKind   string
	Resolved      bool
}

// UpdateRow is one activity row with the current state of its unit.
type UpdateRow struct {
	ID       int64
	At       string
	Actor    string
	Action   string
	Detail   *pyjson.Object
	UnitID   int64 // 0 = none
	UID      string
	Title    string
	Type     string
	Status   string
	Priority string // importance
	// Assignee is the unit's effective assignee (own or inherited through
	// part_of), "" when none.
	Assignee string
	// Creator is the actor of the unit's unit.create row ("" when unknown).
	Creator string
	Comment *UpdateComment
	// Writers are the agent writers of the unit (nil when it has none).
	Writers *UnitWriters
}

// Writer is who made an agent write, as its activity detail records it
// (WithActivitySession, WithActivityAgent).  The zero value is unknown.
type Writer struct {
	Session      string // the ARK session
	AgentSession string // the agent's own session id (X-ARK-Agent-Session)
	Token        string // the personal token's id
	Host         string // the device (X-ARK-Host)
}

// Known reports a writer that can be told apart from other agents.
func (w Writer) Known() bool { return w.Session != "" || w.AgentSession != "" || w.Token != "" }

// WriterOf reads the writer from an activity detail.
func WriterOf(d *pyjson.Object) Writer {
	if d == nil {
		return Writer{}
	}
	str := func(k string) string { s, _ := d.Value(k).(string); return s }
	return Writer{Session: str("session"), AgentSession: str("agent_session"), Token: str("agent_token"), Host: str("agent_host")}
}

// UnitWriters are the agents that wrote a unit: who created it, who last
// handed it to review (status review, needs review, review_note), who last
// wrote it at all (edits and comments), and who wrote each agent comment.
type UnitWriters struct {
	Created, Review, Edited Writer
	Comments                map[int64]Writer
	created                 bool
}

// UpdatesBatch is what UpdatesSince read.
type UpdatesBatch struct {
	Rows  []UpdateRow
	Epoch string // change_log_id: a restored snapshot starts a new epoch
	Top   int64  // the newest activity id read (or the cursor when none)
	Max   int64  // the newest activity id in the table
	More  bool   // more rows after Top
}

// ActivityHead returns the epoch and the newest activity id.
func (s *Store) ActivityHead(ctx context.Context) (string, int64, error) {
	var epoch string
	var top int64
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		var err error
		if epoch, _, err = MetaGet(ctx, q, ChangeLogID); err != nil {
			return err
		}
		r, _, err := sqlitedb.QueryOne(ctx, q, "SELECT COALESCE(MAX(id),0) top FROM activity")
		top = r.Int("top")
		return err
	})
	return epoch, top, err
}

// UpdatesSince reads up to limit activity rows after the id after, oldest
// first, with their unit and comment.
func (s *Store) UpdatesSince(ctx context.Context, after int64, limit int) (*UpdatesBatch, error) {
	out := &UpdatesBatch{Top: after}
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		var err error
		if out.Epoch, _, err = MetaGet(ctx, q, ChangeLogID); err != nil {
			return err
		}
		r, _, err := sqlitedb.QueryOne(ctx, q, "SELECT COALESCE(MAX(id),0) top FROM activity")
		if err != nil {
			return err
		}
		out.Max = r.Int("top")
		if out.Max <= after {
			return nil
		}
		rows, err := sqlitedb.Query(ctx, q, `SELECT a.id, a.at, a.actor, a.action, a.unit_id, a.detail_json,
                   u.uid, u.title, u.type, u.status, u.importance
             FROM activity a LEFT JOIN units u ON u.id=a.unit_id
             WHERE a.id > ? ORDER BY a.id LIMIT ?`, after, limit+1)
		if err != nil {
			return err
		}
		if len(rows) > limit {
			rows, out.More = rows[:limit], true
		}
		creators, err := unitCreators(ctx, q, rows)
		if err != nil {
			return err
		}
		writers, err := unitWriters(ctx, q, creators)
		if err != nil {
			return err
		}
		var index map[int64]assigneeEntry
		for _, r := range rows {
			row := UpdateRow{ID: r.Int("id"), At: r.Str("at"), Actor: r.Str("actor"), Action: r.Str("action"),
				Detail: parseDetail(r.Get("detail_json")), UID: strOrEmpty(r.Get("uid")),
				Title: strOrEmpty(r.Get("title")), Type: strOrEmpty(r.Get("type")), Status: strOrEmpty(r.Get("status")),
				Priority: strOrEmpty(r.Get("importance"))}
			if id, ok := r.Get("unit_id").(int64); ok {
				row.UnitID, row.Creator, row.Writers = id, creators[id], writers[id]
			}
			if row.UnitID != 0 && row.UID != "" {
				if index == nil {
					if index, err = s.assigneeIndex(ctx, q); err != nil {
						return err
					}
				}
				if e, ok := index[row.UnitID]; ok && e.hasName {
					row.Assignee = e.name
				}
			}
			if strings.HasPrefix(row.Action, "comment.") {
				if cid, ok := row.Detail.Value("comment_id").(int64); ok {
					if row.Comment, err = updateComment(ctx, q, cid); err != nil {
						return err
					}
				}
			}
			out.Rows = append(out.Rows, row)
			out.Top = row.ID
		}
		return nil
	})
	return out, err
}

// unitCreators maps the units of rows to the actor of their unit.create row.
func unitCreators(ctx context.Context, q sqlitedb.Querier, rows []sqlitedb.Row) (map[int64]string, error) {
	out := map[int64]string{}
	var ids []any
	for _, r := range rows {
		if id, ok := r.Get("unit_id").(int64); ok {
			if _, seen := out[id]; !seen {
				out[id] = ""
				ids = append(ids, id)
			}
		}
	}
	for _, chunk := range chunks(ids, 400) {
		found, err := sqlitedb.Query(ctx, q, `SELECT a.unit_id, a.actor FROM activity a JOIN (
                SELECT MIN(id) id FROM activity WHERE action='unit.create' AND unit_id IN (`+sqlitedb.Marks(len(chunk))+`)
                GROUP BY unit_id) f ON f.id=a.id`, chunk...)
		if err != nil {
			return nil, err
		}
		for _, r := range found {
			out[r.Int("unit_id")] = r.Str("actor")
		}
	}
	return out, nil
}

// unitWriters reads the agent writes of the units (the keys of units) and
// sums them up per unit.
func unitWriters(ctx context.Context, q sqlitedb.Querier, units map[int64]string) (map[int64]*UnitWriters, error) {
	out := map[int64]*UnitWriters{}
	ids := make([]any, 0, len(units))
	for id := range units {
		ids = append(ids, id)
	}
	for _, chunk := range chunks(ids, 400) {
		found, err := sqlitedb.Query(ctx, q, `SELECT unit_id, action, detail_json FROM activity
             WHERE unit_id IN (`+sqlitedb.Marks(len(chunk))+`) AND actor LIKE 'agent:%' ORDER BY id`, chunk...)
		if err != nil {
			return nil, err
		}
		for _, r := range found {
			action := r.Str("action")
			if !strings.HasPrefix(action, "unit.") && action != "comment.create" {
				continue
			}
			id := r.Int("unit_id")
			uw := out[id]
			if uw == nil {
				uw = &UnitWriters{}
				out[id] = uw
			}
			d := parseDetail(r.Get("detail_json"))
			w := WriterOf(d)
			uw.Edited = w
			switch {
			case action == "comment.create":
				if cid, ok := d.Value("comment_id").(int64); ok {
					if uw.Comments == nil {
						uw.Comments = map[int64]Writer{}
					}
					uw.Comments[cid] = w
				}
			case action == "unit.create" && !uw.created:
				uw.Created = w
				uw.created = true
			}
			if handsToReview(action, d) {
				uw.Review = w
			}
		}
	}
	return out, nil
}

// handsToReview reports an agent write that hands the unit to review.
func handsToReview(action string, d *pyjson.Object) bool {
	if d == nil {
		return false
	}
	switch action {
	case "unit.create":
		return d.Value("status") == "review"
	case "unit.needs":
		return d.Value("to") == "review"
	case "unit.update":
		if st, ok := d.Value("status").(*pyjson.Object); ok && st.Value("to") == "review" {
			return true
		}
		if list, ok := d.Value("fields").([]any); ok {
			for _, f := range list {
				if f == "review_note" {
					return true
				}
			}
		}
	}
	return false
}

func updateComment(ctx context.Context, q sqlitedb.Querier, id int64) (*UpdateComment, error) {
	r, ok, err := sqlitedb.QueryOne(ctx, q, `SELECT c.id, c.author, c.kind, c.body, c.reply_to, c.resolved_at,
              p.author AS parent_author, p.kind AS parent_kind
         FROM unit_comments c LEFT JOIN unit_comments p ON p.id=c.reply_to WHERE c.id=?`, id)
	if err != nil || !ok {
		return nil, err
	}
	out := &UpdateComment{ID: r.Int("id"), Author: r.Str("author"), Kind: r.Str("kind"), Body: r.Str("body"),
		ReplyToAuthor: strOrEmpty(r.Get("parent_author")), ReplyToKind: strOrEmpty(r.Get("parent_kind")),
		Resolved: pyjson.Truthy(r.Get("resolved_at"))}
	if p, ok := r.Get("reply_to").(int64); ok {
		out.ReplyTo = p
	}
	return out, nil
}

// ------------------------------------------------------------ write signal

// WriteSignal returns a channel that is closed after the next write this
// process commits to the store (any write: noteWrite runs after each).  A
// long poll takes the channel before it reads, so a write between the read
// and the wait still wakes it.  Writes by another process are not signalled;
// waiters re-read on a slow timer as well.
func (s *Store) WriteSignal() <-chan struct{} {
	s.signalMu.Lock()
	defer s.signalMu.Unlock()
	if s.signal == nil {
		s.signal = make(chan struct{})
	}
	return s.signal
}

// signalWrite wakes the WriteSignal waiters.
func (s *Store) signalWrite() {
	s.signalMu.Lock()
	if s.signal != nil {
		close(s.signal)
		s.signal = nil
	}
	s.signalMu.Unlock()
}
