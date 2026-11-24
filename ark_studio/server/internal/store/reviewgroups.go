package store

// Go only: review groups (docs/agents/working-mode.md, "Review groups").
//
// When one delivery puts several units up for review, the agent submits one
// review group instead of setting each unit to review: a title, a summary,
// an anchored note and the member units (each with an optional note of its
// own).  The group is one card in Needs you; the person accepts or sends
// back the group, or decides member by member.  Members keep their own
// status (a member that is itself in review is accepted / sent back with
// the group).  A group is not a unit: it lives in review_groups (created on
// first use, see reviewrounds.go), so it never shows in graphs, the map,
// search, counts or exports.  When the review is over (every member
// accepted, or the group closed) the group is deleted; the activity log
// and the members' review rounds keep the record ("accepted in review group
// <title>").  A group sent back stays, waiting for the agent to resubmit it
// (status review: a new round) or to close it.

import (
	"context"
	"strconv"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/sqlitedb"
)

// Review group limits.
const (
	maxGroupTitle      = 240
	maxGroupSummary    = 2000
	maxGroupMemberNote = 300
	maxGroupMembers    = 60
	ReviewGroupPrefix  = "RG-"
)

// ErrSingleMemberGroup is the message for a group of one unit.
const errSingleMember = "a review group is for one delivery that puts several units up for review; " +
	"for a single unit set its status to review with a review_note instead"

func groupRef(id int64) string { return ReviewGroupPrefix + strconv.FormatInt(id, 10) }

func parseGroupRef(ref string) (int64, error) {
	text := strings.TrimPrefix(strings.TrimSpace(ref), ReviewGroupPrefix)
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil || n <= 0 {
		return 0, storeErr("review group not found: %s", ref)
	}
	return n, nil
}

type groupMemberInput struct {
	ref  string
	note string
}

func groupMembersInput(raw any) ([]groupMemberInput, error) {
	list, err := listOf(raw)
	if err != nil {
		return nil, storeErr("members must be a list")
	}
	var out []groupMemberInput
	for _, item := range list {
		switch v := item.(type) {
		case string:
			out = append(out, groupMemberInput{ref: pytext.Strip(v)})
		case *pyjson.Object:
			ref := v.Value("uid")
			if !pyjson.Truthy(ref) {
				ref = v.Value("unit")
			}
			note := v.Value("note")
			if !pyjson.Truthy(note) {
				note = v.Value("anchors")
			}
			text := ""
			switch n := note.(type) {
			case string:
				text = n
			case []any:
				parts := []string{}
				for _, a := range n {
					parts = append(parts, pyjson.Str(a))
				}
				text = strings.Join(parts, " ")
			case nil:
			default:
				return nil, storeErr("member note must be a string")
			}
			text = pytext.Strip(text)
			if pytext.Len(text) > maxGroupMemberNote {
				return nil, storeErr("member note is longer than %d characters", maxGroupMemberNote)
			}
			out = append(out, groupMemberInput{ref: pytext.Strip(pyjson.Str(ref)), note: text})
		default:
			return nil, storeErr("each member is a uid or {uid, note}")
		}
	}
	return out, nil
}

// resolveMembers looks the member units up (live units only, once each).
func resolveMembers(ctx context.Context, q sqlitedb.Querier, inputs []groupMemberInput) ([]sqlitedb.Row, []string, error) {
	var rows []sqlitedb.Row
	var notes []string
	seen := map[int64]bool{}
	for _, in := range inputs {
		if in.ref == "" {
			return nil, nil, storeErr("member uid is required")
		}
		row, err := unitRow(ctx, q, in.ref)
		if err != nil {
			return nil, nil, err
		}
		if row.Int("archived") != 0 {
			return nil, nil, storeErr("unit is in the recycle bin: %s", in.ref)
		}
		if seen[row.Int("id")] {
			continue
		}
		seen[row.Int("id")] = true
		rows = append(rows, row)
		notes = append(notes, in.note)
	}
	if len(rows) < 2 {
		return nil, nil, storeErr(errSingleMember)
	}
	if len(rows) > maxGroupMembers {
		return nil, nil, storeErr("a review group has at most %d members", maxGroupMembers)
	}
	return rows, notes, nil
}

// memberBaseline is what a member is compared against in a new round.
func memberBaseline(ctx context.Context, q sqlitedb.Querier, row sqlitedb.Row) (kind, at, body string, media int64, err error) {
	b, m, a, ok, err := reviewBaseline(ctx, q, row.Int("id"))
	if err != nil {
		return "", "", "", 0, err
	}
	if ok {
		return "review", a, b, m, nil
	}
	media, err = maxMediaID(ctx, q, row.Int("id"))
	return "entered", clock.UTCNow(), row.Str("body"), media, err
}

func insertMembers(ctx context.Context, q sqlitedb.Querier, groupID int64, rows []sqlitedb.Row, notes []string) error {
	for i, row := range rows {
		kind, at, body, media, err := memberBaseline(ctx, q, row)
		if err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO review_group_members(group_id,unit_id,position,note,base_kind,base_at,
                   base_body,base_media) VALUES(?,?,?,?,?,?,?,?)`, groupID, row.Int("id"), i, notes[i], kind, at, body, media); err != nil {
			return err
		}
	}
	return nil
}

func memberUIDs(rows []sqlitedb.Row) []any {
	out := make([]any, len(rows))
	for i, r := range rows {
		out[i] = r.Get("uid")
	}
	return out
}

// CreateReviewGroup is POST /api/agent/v1/reviews (and /api/reviews):
// {title, summary, review_note|note, members: [uid | {uid, note}]}.
// It returns the group and the anchors that do not resolve.
func (s *Store) CreateReviewGroup(ctx context.Context, data *pyjson.Object, actor string) (*pyjson.Object, error) {
	title, err := cleanTextField(data.Value("title"), "title", maxGroupTitle, true)
	if err != nil {
		return nil, err
	}
	summary, err := cleanTextField(data.Value("summary"), "summary", maxGroupSummary, false)
	if err != nil {
		return nil, err
	}
	noteRaw := data.Value("review_note")
	if noteRaw == nil {
		noteRaw = data.Value("note")
	}
	note, err := cleanReviewNote(noteRaw)
	if err != nil {
		return nil, err
	}
	inputs, err := groupMembersInput(data.Value("members"))
	if err != nil {
		return nil, err
	}
	var id int64
	err = s.write(ctx, func(w *wtx) error {
		w.full = true
		rows, notes, err := resolveMembers(ctx, w.q, inputs)
		if err != nil {
			return err
		}
		if IsAgentActor(actor) { // Go only (reviewnote_required.go)
			if err := checkAgentGroupReviewNote(note, notes, false, ""); err != nil {
				return err
			}
		}
		if err := ensureReviewTables(ctx, w.q); err != nil {
			return err
		}
		now := clock.UTCNow()
		res, err := w.q.ExecContext(ctx, `INSERT INTO review_groups(title,summary,note,status,round,created_at,created_by,
                   opened_at,updated_at) VALUES(?,?,?,'review',1,?,?,?,?)`, title, summary, note, now, actor, now, now)
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		if err := insertMembers(ctx, w.q, id, rows, notes); err != nil {
			return err
		}
		return w.log(actor, "review.group.create", nil, pyjson.NewObject("review", groupRef(id), "title", title,
			"created_by", actor, "members", memberUIDs(rows), "review_note", note))
	})
	if err != nil {
		return nil, err
	}
	return s.ReviewGroup(ctx, groupRef(id))
}

func groupRow(ctx context.Context, q sqlitedb.Querier, ref string) (sqlitedb.Row, error) {
	id, err := parseGroupRef(ref)
	if err != nil {
		return sqlitedb.Row{}, err
	}
	if has, err := hasReviewTables(ctx, q); err != nil {
		return sqlitedb.Row{}, err
	} else if !has {
		return sqlitedb.Row{}, storeErr("review group not found: %s", ref)
	}
	row, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT * FROM review_groups WHERE id=?", id)
	if err != nil {
		return row, err
	}
	if !ok {
		return row, storeErr("review group not found: %s (it may have been closed)", ref)
	}
	return row, nil
}

// UpdateReviewGroup changes a group: title, summary, review_note, members
// (the whole list), status "review" (resubmit after a send back: a new
// round) or "closed" (delete it).
func (s *Store) UpdateReviewGroup(ctx context.Context, ref string, patch *pyjson.Object, actor string) (*pyjson.Object, error) {
	closed := false
	err := s.write(ctx, func(w *wtx) error {
		w.full = true
		g, err := groupRow(ctx, w.q, ref)
		if err != nil {
			return err
		}
		id := g.Int("id")
		title, summary, note := g.Str("title"), g.Str("summary"), g.Str("note")
		if patch.Has("title") {
			if title, err = cleanTextField(patch.Value("title"), "title", maxGroupTitle, true); err != nil {
				return err
			}
		}
		if patch.Has("summary") {
			if summary, err = cleanTextField(patch.Value("summary"), "summary", maxGroupSummary, false); err != nil {
				return err
			}
		}
		if patch.Has("review_note") || patch.Has("note") {
			raw := patch.Value("review_note")
			if !patch.Has("review_note") {
				raw = patch.Value("note")
			}
			if note, err = cleanReviewNote(raw); err != nil {
				return err
			}
		}
		status := g.Str("status")
		if patch.Has("status") {
			status = pytext.Lower(pytext.Strip(pyjson.Str(patch.Value("status"))))
			if status != "review" && status != "closed" {
				return storeErr("status must be review (resubmit) or closed")
			}
		}
		if status == "closed" {
			closed = true
			return closeGroup(w, g, "closed", actor, "")
		}
		round, opened := g.Int("round"), g.Str("opened_at")
		now := clock.UTCNow()
		resubmit := g.Str("status") == "sent-back" && status == "review"
		if resubmit {
			round++
			opened = now
		}
		if patch.Has("members") {
			inputs, err := groupMembersInput(patch.Value("members"))
			if err != nil {
				return err
			}
			rows, notes, err := resolveMembers(ctx, w.q, inputs)
			if err != nil {
				return err
			}
			// Keep the decisions of members that stay; new members start pending.
			old, err := sqlitedb.Query(ctx, w.q, "SELECT * FROM review_group_members WHERE group_id=?", id)
			if err != nil {
				return err
			}
			kept := map[int64]sqlitedb.Row{}
			for _, m := range old {
				kept[m.Int("unit_id")] = m
			}
			if _, err := w.q.ExecContext(ctx, "DELETE FROM review_group_members WHERE group_id=?", id); err != nil {
				return err
			}
			for i, row := range rows {
				if m, ok := kept[row.Int("id")]; ok {
					if _, err := w.q.ExecContext(ctx, `INSERT INTO review_group_members(group_id,unit_id,position,note,outcome,
                   decided_at,decided_by,base_kind,base_at,base_body,base_media) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
						id, row.Int("id"), i, notes[i], m.Get("outcome"), m.Get("decided_at"), m.Get("decided_by"),
						m.Get("base_kind"), m.Get("base_at"), m.Get("base_body"), m.Get("base_media")); err != nil {
						return err
					}
					continue
				}
				if err := insertMembers(ctx, w.q, id, rows[i:i+1], notes[i:i+1]); err != nil {
					return err
				}
				if _, err := w.q.ExecContext(ctx, "UPDATE review_group_members SET position=? WHERE group_id=? AND unit_id=?", i, id, row.Int("id")); err != nil {
					return err
				}
			}
		}
		if resubmit {
			// Members sent back are up again, compared with what the person saw.
			pending, err := sqlitedb.Query(ctx, w.q, `SELECT m.unit_id, u.body FROM review_group_members m JOIN units u ON u.id=m.unit_id
                   WHERE m.group_id=? AND m.outcome!='accepted'`, id)
			if err != nil {
				return err
			}
			for _, m := range pending {
				row, err := unitRow(ctx, w.q, strconv.FormatInt(m.Int("unit_id"), 10))
				if err != nil {
					return err
				}
				kind, at, body, media, err := memberBaseline(ctx, w.q, row)
				if err != nil {
					return err
				}
				if _, err := w.q.ExecContext(ctx, `UPDATE review_group_members SET outcome='', decided_at=NULL, decided_by=NULL,
                   base_kind=?, base_at=?, base_body=?, base_media=? WHERE group_id=? AND unit_id=?`,
					kind, at, body, media, id, m.Int("unit_id")); err != nil {
					return err
				}
			}
		}
		// Go only: an agent (re)submits with a note (reviewnote_required.go).
		noteSent := patch.Has("review_note") || patch.Has("note")
		if IsAgentActor(actor) && status == "review" && (resubmit || noteSent) {
			sent := note
			if !noteSent {
				sent = ""
			}
			members, err := sqlitedb.Query(ctx, w.q, "SELECT note FROM review_group_members WHERE group_id=?", id)
			if err != nil {
				return err
			}
			memberNotes := make([]string, len(members))
			for i, m := range members {
				memberNotes[i] = m.Str("note")
			}
			if err := checkAgentGroupReviewNote(sent, memberNotes, resubmit, g.Str("note")); err != nil {
				return err
			}
		}
		if _, err := w.q.ExecContext(ctx, `UPDATE review_groups SET title=?, summary=?, note=?, status=?, round=?, opened_at=?,
                   updated_at=? WHERE id=?`, title, summary, note, status, round, opened, now, id); err != nil {
			return err
		}
		action := "review.group.update"
		if resubmit {
			action = "review.group.resubmit"
		}
		return w.log(actor, action, nil, pyjson.NewObject("review", groupRef(id), "title", title,
			"created_by", g.Get("created_by"), "round", round, "review_note", note))
	})
	if err != nil {
		return nil, err
	}
	if closed {
		return pyjson.NewObject("id", ref, "closed", true), nil
	}
	return s.ReviewGroup(ctx, ref)
}

// closeGroup deletes the group (its members and comments go with it) and
// logs the record.
func closeGroup(w *wtx, g sqlitedb.Row, outcome, actor, note string) error {
	members, err := sqlitedb.Query(w.ctx, w.q, `SELECT u.uid, m.outcome FROM review_group_members m JOIN units u ON u.id=m.unit_id
                   WHERE m.group_id=? ORDER BY m.position`, g.Int("id"))
	if err != nil {
		return err
	}
	list := []any{}
	for _, m := range members {
		list = append(list, pyjson.NewObject("uid", m.Get("uid"), "outcome", nilIfEmpty(m.Str("outcome"))))
	}
	if _, err := w.q.ExecContext(w.ctx, "DELETE FROM review_groups WHERE id=?", g.Int("id")); err != nil {
		return err
	}
	return w.log(actor, "review.group.close", nil, pyjson.NewObject("review", groupRef(g.Int("id")), "title", g.Get("title"),
		"summary", g.Get("summary"), "created_by", g.Get("created_by"), "outcome", outcome, "note", note,
		"rounds", g.Get("round"), "members", list))
}

// DecideReviewGroup is Accept / Send back on a group: {action: accept |
// send_back, note, members: {uid: accept | send_back}} (members overrides
// the action for those units).  Members still pending get the outcome;
// members in status review take the matching status change; a member sent
// back also gets the note as a sent_back comment, which reaches the agent.
// When every member is accepted the group is closed.
func (s *Store) DecideReviewGroup(ctx context.Context, ref string, data *pyjson.Object, author, actor string, who Who) (*pyjson.Object, error) {
	if author == "" {
		author = "user"
	}
	action := pytext.Lower(pytext.Strip(pyjson.Str(data.Value("action"))))
	if action != "accept" && action != "send_back" {
		return nil, storeErr("action must be accept or send_back")
	}
	note, err := cleanText(orEmptyString(data.Value("note")), "note", maxComment)
	if err != nil {
		return nil, err
	}
	note = pytext.Strip(note)
	overrides := map[string]string{}
	if o, ok := data.Value("members").(*pyjson.Object); ok {
		for _, k := range o.Keys() {
			v := pytext.Lower(pytext.Strip(pyjson.Str(o.Value(k))))
			if v != "accept" && v != "send_back" {
				return nil, storeErr("member decision must be accept or send_back")
			}
			overrides[k] = v
		}
	} else if data.Has("members") && data.Value("members") != nil {
		return nil, storeErr("members must be an object {uid: accept | send_back}")
	}
	closed := false
	err = s.write(ctx, func(w *wtx) error {
		w.full = true
		g, err := groupRow(ctx, w.q, ref)
		if err != nil {
			return err
		}
		if g.Str("status") != "review" {
			return storeErr("this review group was sent back and waits for the agent")
		}
		members, err := sqlitedb.Query(ctx, w.q, `SELECT m.*, u.uid FROM review_group_members m JOIN units u ON u.id=m.unit_id
                   WHERE m.group_id=? ORDER BY m.position`, g.Int("id"))
		if err != nil {
			return err
		}
		for uid := range overrides {
			found := false
			for _, m := range members {
				if m.Str("uid") == uid {
					found = true
				}
			}
			if !found {
				return storeErr("%s is not a member of %s", uid, groupRef(g.Int("id")))
			}
		}
		if action == "send_back" && note == "" {
			return storeErr("say what is missing (note) when sending back")
		}
		now := clock.UTCNow()
		allAccepted := true
		results := []any{}
		for _, m := range members {
			decision := action
			if v, ok := overrides[m.Str("uid")]; ok {
				decision = v
			}
			if m.Str("outcome") == "accepted" {
				results = append(results, pyjson.NewObject("uid", m.Get("uid"), "outcome", "accepted"))
				continue
			}
			outcome := "accepted"
			if decision == "send_back" {
				outcome, allAccepted = "sent-back", false
			}
			if err := s.decideMember(w, g, m, outcome, note, author, actor, who, now); err != nil {
				return err
			}
			results = append(results, pyjson.NewObject("uid", m.Get("uid"), "outcome", outcome))
		}
		if err := w.log(actor, "review.group.decide", nil, pyjson.NewObject("review", groupRef(g.Int("id")),
			"title", g.Get("title"), "created_by", g.Get("created_by"), "round", g.Get("round"), "action", action,
			"note", note, "members", results)); err != nil {
			return err
		}
		if allAccepted {
			closed = true
			return closeGroup(w, g, "accepted", actor, note)
		}
		_, err = w.q.ExecContext(ctx, "UPDATE review_groups SET status='sent-back', updated_at=? WHERE id=?", now, g.Int("id"))
		return err
	})
	if err != nil {
		return nil, err
	}
	if closed {
		return pyjson.NewObject("id", ref, "closed", true, "outcome", "accepted"), nil
	}
	return s.ReviewGroup(ctx, ref)
}

func orEmptyString(v any) any {
	if v == nil {
		return ""
	}
	return v
}

// decideMember records one member's outcome: the member row, a closed
// review round on the unit (the baseline of its next review), the status
// change of a unit in review, the sent_back comment and an activity row on
// the unit ("accepted in review group <title>").
func (s *Store) decideMember(w *wtx, g, m sqlitedb.Row, outcome, note, author, actor string, who Who, now string) error {
	ctx, q := w.ctx, w.q
	unitID := m.Int("unit_id")
	if _, err := q.ExecContext(ctx, "UPDATE review_group_members SET outcome=?, decided_at=?, decided_by=? WHERE group_id=? AND unit_id=?",
		outcome, now, actor, g.Int("id"), unitID); err != nil {
		return err
	}
	row, err := unitRow(ctx, q, strconv.FormatInt(unitID, 10))
	if err != nil {
		return err
	}
	label := "review group " + groupRef(g.Int("id")) + " “" + g.Str("title") + "”"
	if outcome == "sent-back" {
		text := "[" + label + "] " + note
		if _, err := insertComment(w, unitID, author, "sent_back", text, actor); err != nil {
			return err
		}
	}
	if row.Str("status") == "review" {
		status := "done"
		if outcome == "sent-back" {
			status = "active"
		}
		saved := w.units
		if _, err := s.updateUnitTx(w, unitID, pyjson.NewObject("status", status), actor, who, "Save text"); err != nil {
			return err
		}
		w.units = saved
	} else {
		// Not in review itself: record the decision as a closed round.
		r, _, err := sqlitedb.QueryOne(ctx, q, "SELECT COALESCE(MAX(round),0) n FROM review_rounds WHERE unit_id=?", unitID)
		if err != nil {
			return err
		}
		media, err := maxMediaID(ctx, q, unitID)
		if err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO review_rounds(unit_id,round,opened_at,opened_by,note,note_by,base_kind,base_at,
                   base_body,base_media,closed_at,closed_by,outcome,close_body,close_media) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			unitID, r.Int("n")+1, g.Str("opened_at"), g.Str("created_by"), "["+label+"] "+g.Str("note"), g.Str("created_by"),
			m.Str("base_kind"), m.Str("base_at"), m.Str("base_body"), m.Int("base_media"),
			now, actor, outcome, row.Str("body"), media); err != nil {
			return err
		}
	}
	return w.log(actor, "review.group.member", unitID, pyjson.NewObject("uid", row.Get("uid"), "title", row.Get("title"),
		"review", groupRef(g.Int("id")), "group_title", g.Get("title"), "outcome", outcome, "note", note))
}

// insertComment adds a comment row inside an open write (AddComment's insert
// and activity row, without its own transaction).
func insertComment(w *wtx, unitID int64, author, kind, body, actor string) (int64, error) {
	res, err := w.q.ExecContext(w.ctx, "INSERT INTO unit_comments(unit_id,author,kind,body,created_at) VALUES(?,?,?,?,?)",
		unitID, author, kind, body, clock.UTCNow())
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	r, _, err := sqlitedb.QueryOne(w.ctx, w.q, "SELECT uid, title FROM units WHERE id=?", unitID)
	if err != nil {
		return 0, err
	}
	return id, w.log(actor, "comment.create", unitID, pyjson.NewObject("uid", r.Get("uid"), "title", r.Get("title"),
		"comment_id", id, "kind", kind, "reply_to", nil, "excerpt", pytext.Excerpt(body, 120)))
}

// AddReviewGroupComment adds a comment to a group (people: kind comment;
// agents: agent_reply).
func (s *Store) AddReviewGroupComment(ctx context.Context, ref string, body any, author, actor string) (*pyjson.Object, error) {
	text, err := cleanText(body, "comment", maxComment)
	if err != nil {
		return nil, err
	}
	if text = pytext.Strip(text); text == "" {
		return nil, storeErr("comment text is required")
	}
	kind := "comment"
	if isAgent(actor) || author == "" {
		author = actor
	}
	if isAgent(actor) {
		kind = "agent_reply"
	}
	err = s.write(ctx, func(w *wtx) error {
		g, err := groupRow(ctx, w.q, ref)
		if err != nil {
			return err
		}
		res, err := w.q.ExecContext(ctx, `INSERT INTO review_group_comments(group_id,round,author,kind,body,created_at)
                   VALUES(?,?,?,?,?,?)`, g.Int("id"), g.Int("round"), author, kind, text, clock.UTCNow())
		if err != nil {
			return err
		}
		cid, _ := res.LastInsertId()
		return w.log(actor, "review.group.comment", nil, pyjson.NewObject("review", groupRef(g.Int("id")),
			"title", g.Get("title"), "created_by", g.Get("created_by"), "comment_id", cid, "kind", kind, "body", text))
	})
	if err != nil {
		return nil, err
	}
	return s.ReviewGroup(ctx, ref)
}

// ResolveReviewGroupComment marks a group comment resolved (or open again).
func (s *Store) ResolveReviewGroupComment(ctx context.Context, ref string, commentID int64, resolved bool, actor string) (*pyjson.Object, error) {
	err := s.write(ctx, func(w *wtx) error {
		g, err := groupRow(ctx, w.q, ref)
		if err != nil {
			return err
		}
		var res interface{ RowsAffected() (int64, error) }
		if resolved {
			res, err = w.q.ExecContext(ctx, "UPDATE review_group_comments SET resolved_at=?, resolved_by=? WHERE id=? AND group_id=?",
				clock.UTCNow(), actor, commentID, g.Int("id"))
		} else {
			res, err = w.q.ExecContext(ctx, "UPDATE review_group_comments SET resolved_at=NULL, resolved_by=NULL WHERE id=? AND group_id=?",
				commentID, g.Int("id"))
		}
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return storeErr("comment not found on this review group: %d", commentID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.ReviewGroup(ctx, ref)
}

// ReviewGroup is one group with its members (title, type, status, note and
// anchors, outcome, what changed since the baseline) and comments.
func (s *Store) ReviewGroup(ctx context.Context, ref string) (*pyjson.Object, error) {
	var out *pyjson.Object
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		g, err := groupRow(ctx, q, ref)
		if err != nil {
			return err
		}
		out, err = s.groupObject(ctx, q, g, true)
		return err
	})
	return out, err
}

// ReviewGroups lists the groups (status "" = every one, "review" = those
// waiting on a person).
func (s *Store) ReviewGroups(ctx context.Context, status string) ([]any, error) {
	out := []any{}
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		var err error
		out, err = s.reviewGroups(ctx, q, status, false)
		return err
	})
	return out, err
}

func (s *Store) reviewGroups(ctx context.Context, q sqlitedb.Querier, status string, needs bool) ([]any, error) {
	out := []any{}
	if has, err := hasReviewTables(ctx, q); err != nil || !has {
		return out, err
	}
	query := "SELECT * FROM review_groups ORDER BY opened_at DESC, id DESC"
	args := []any{}
	if status != "" {
		query = "SELECT * FROM review_groups WHERE status=? ORDER BY opened_at DESC, id DESC"
		args = append(args, status)
	}
	rows, err := sqlitedb.Query(ctx, q, query, args...)
	if err != nil {
		return nil, err
	}
	for _, g := range rows {
		o, err := s.groupObject(ctx, q, g, !needs)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

func (s *Store) groupObject(ctx context.Context, q sqlitedb.Querier, g sqlitedb.Row, full bool) (*pyjson.Object, error) {
	id := g.Int("id")
	anchors, warnings, err := resolveAnchors(ctx, q, g.Str("note"), "", "")
	if err != nil {
		return nil, err
	}
	members, err := sqlitedb.Query(ctx, q, `SELECT m.*, u.uid, u.title, u.type, u.status, u.body, u.updated_at uupdated
                   FROM review_group_members m JOIN units u ON u.id=m.unit_id
                   WHERE m.group_id=? AND u.archived=0 ORDER BY m.position`, id)
	if err != nil {
		return nil, err
	}
	idList := make([]int64, len(members))
	for i, m := range members {
		idList[i] = m.Int("unit_id")
	}
	pv, err := s.previewMediaBatch(ctx, q, idList, NeedsMediaLimit)
	if err != nil {
		return nil, err
	}
	mediaIDs := map[int64][]int64{}
	mrows, err := groupRows(ctx, q, "SELECT unit_id, id FROM unit_media WHERE unit_id IN ({marks}) ORDER BY id", sqlitedb.Args(idList))
	if err != nil {
		return nil, err
	}
	for _, m := range mrows {
		mediaIDs[m.Int("unit_id")] = append(mediaIDs[m.Int("unit_id")], m.Int("id"))
	}
	list := []any{}
	pending := int64(0)
	for _, m := range members {
		uid := m.Str("uid")
		manchors, mwarn, err := resolveAnchors(ctx, q, m.Str("note"), uid, m.Str("body"))
		if err != nil {
			return nil, err
		}
		warnings = append(warnings, mwarn...)
		d := reviewDiff(m.Str("base_body"), m.Str("body"))
		newMedia := int64(0)
		for _, mid := range mediaIDs[m.Int("unit_id")] {
			if mid > m.Int("base_media") {
				newMedia++
			}
		}
		if m.Str("outcome") == "" {
			pending++
		}
		text, _ := pytext.MarkdownExcerpt(m.Str("body"), 400)
		item := pyjson.NewObject("uid", uid, "title", m.Get("title"), "type", m.Get("type"), "status", m.Get("status"),
			"updated_at", m.Get("uupdated"), "note", m.Get("note"), "anchors", manchors,
			"outcome", nilIfEmpty(m.Str("outcome")), "decided_at", m.Get("decided_at"), "decided_by", m.Get("decided_by"),
			"excerpt", text, "excerpt_plain", pytext.Excerpt(pytext.NoteLineRe.ReplaceAllString(m.Str("body"), ""), 320),
			"verdict", nilIfEmpty(reviewVerdict(m.Str("body"))), "delivery", deliveryOf(m.Str("title"), m.Str("body")),
			"base", pyjson.NewObject("kind", m.Get("base_kind"), "at", m.Get("base_at")),
			"changes", pyjson.NewObject("added", d.added, "removed", d.removed, "media", newMedia))
		media := pv[m.Int("unit_id")].media
		for _, x := range media {
			if o, ok := x.(*pyjson.Object); ok && o.Value("from") == nil {
				if mid, ok := o.Value("id").(int64); ok && mid > m.Int("base_media") {
					o.Set("new", true)
				}
			}
		}
		item.Set("media", media).Set("own_media", pv[m.Int("unit_id")].own)
		if full {
			item.Set("diff", pyjson.NewObject("hunks", d.hunks, "added", d.added, "removed", d.removed))
		}
		list = append(list, item)
	}
	comments := []any{}
	crows, err := sqlitedb.Query(ctx, q, "SELECT * FROM review_group_comments WHERE group_id=? ORDER BY created_at, id", id)
	if err != nil {
		return nil, err
	}
	open := int64(0)
	for _, c := range crows {
		resolved := pyjson.Truthy(c.Get("resolved_at"))
		if !resolved && c.Str("kind") != "agent_reply" {
			open++
		}
		comments = append(comments, pyjson.NewObject("id", c.Get("id"), "round", c.Get("round"), "author", c.Get("author"),
			"kind", c.Get("kind"), "body", c.Get("body"), "created_at", c.Get("created_at"), "resolved", resolved,
			"resolved_at", c.Get("resolved_at")))
	}
	return pyjson.NewObject("id", groupRef(id), "kind", "review_group", "title", g.Get("title"), "summary", g.Get("summary"),
		"review_note", g.Get("note"), "anchors", anchors, "warnings", warnings, "status", g.Get("status"),
		"round", g.Get("round"), "created_at", g.Get("created_at"), "created_by", g.Get("created_by"),
		"opened_at", g.Get("opened_at"), "updated_at", g.Get("updated_at"), "members", list, "pending", pending,
		"comments", comments, "comments_open", open), nil
}

// groupsOfUnit lists the open groups a unit belongs to (id, title, status).
func groupsOfUnit(ctx context.Context, q sqlitedb.Querier, unitID int64) ([]any, error) {
	out := []any{}
	if has, err := hasReviewTables(ctx, q); err != nil || !has {
		return out, err
	}
	rows, err := sqlitedb.Query(ctx, q, `SELECT g.id, g.title, g.status, g.round, m.outcome, m.note FROM review_group_members m
                   JOIN review_groups g ON g.id=m.group_id WHERE m.unit_id=? ORDER BY g.id`, unitID)
	for _, r := range rows {
		out = append(out, pyjson.NewObject("id", groupRef(r.Int("id")), "title", r.Get("title"), "status", r.Get("status"),
			"round", r.Get("round"), "outcome", nilIfEmpty(r.Str("outcome")), "note", r.Get("note")))
	}
	return out, err
}
