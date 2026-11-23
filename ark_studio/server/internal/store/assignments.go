package store

// Go only: who assigned a unit to its assignee, for "Assigned to me" in Needs
// you and the assignee's notifications (server/project_reviews.go,
// server/notifications.go).
//
// unit_assignments has one row per unit whose own assignee was set by
// someone else: the assignee, the account that set it (the signed-in person,
// or the user an agent token acts for), the write actor and when.  Assigning
// a unit to yourself, unassigning it, or a creator's default assignee leaves
// no row (and removes an earlier one), so the table only holds assignments
// that are news to the assignee.  Like review_rounds it is created on first
// use: projects where nobody assigns others keep the frozen Python schema.
//
// batch groups the assignments one person made to another together: the
// batch id of a multi-unit write, or else the batch of the assigner's
// previous assignment to the same person when it is at most
// AssignBatchWindow old.  The notification feed shows one item per batch.

import (
	"context"
	"strconv"
	"strings"
	"time"

	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/sqlitedb"
)

// AssignBatchWindow: assignments by one person to another this close together
// form one notice.
const AssignBatchWindow = 2 * time.Minute

const assignmentSchema = `CREATE TABLE IF NOT EXISTS unit_assignments(
  unit_id INTEGER PRIMARY KEY REFERENCES units(id) ON DELETE CASCADE,
  assignee TEXT NOT NULL,
  assigned_by TEXT NOT NULL,
  actor TEXT NOT NULL DEFAULT '',
  assigned_at TEXT NOT NULL,
  batch TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS unit_assignments_who ON unit_assignments(assignee, assigned_by, assigned_at);`

func hasAssignmentTable(ctx context.Context, q sqlitedb.Querier) (bool, error) {
	_, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT 1 FROM sqlite_master WHERE type='table' AND name='unit_assignments'")
	return ok, err
}

// sameUsername compares account names the way assignees resolve (case-insensitive).
func sameUsername(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// recordAssignment keeps unit_assignments in step with a unit whose own
// assignee became assignee ("" = unassigned) by the account by.
func recordAssignment(w *wtx, unitID int64, assignee, by, actor string) error {
	ctx, q := w.ctx, w.q
	if assignee == "" || by == "" || sameUsername(assignee, by) || strings.HasPrefix(actor, "system") {
		has, err := hasAssignmentTable(ctx, q)
		if err != nil || !has {
			return err
		}
		_, err = q.ExecContext(ctx, "DELETE FROM unit_assignments WHERE unit_id=?", unitID)
		return err
	}
	if _, err := q.ExecContext(ctx, assignmentSchema); err != nil {
		return err
	}
	now := clock.Now().UTC()
	at := MarkTimeOf(now)
	batch := w.batch
	if batch == "" {
		prev, ok, err := sqlitedb.QueryOne(ctx, q, `SELECT batch FROM unit_assignments
            WHERE assignee=? COLLATE NOCASE AND assigned_by=? COLLATE NOCASE AND assigned_at>=? AND unit_id<>?
            ORDER BY assigned_at DESC LIMIT 1`, assignee, by, MarkTimeOf(now.Add(-AssignBatchWindow)), unitID)
		if err != nil {
			return err
		}
		if ok {
			batch = prev.Str("batch")
		} else {
			batch = "a_" + strconv.FormatInt(now.UnixMilli(), 36) + "_" + strconv.FormatInt(unitID, 36)
		}
	}
	_, err := q.ExecContext(ctx, `INSERT INTO unit_assignments(unit_id,assignee,assigned_by,actor,assigned_at,batch)
        VALUES(?,?,?,?,?,?) ON CONFLICT(unit_id) DO UPDATE SET assignee=excluded.assignee,
        assigned_by=excluded.assigned_by, actor=excluded.actor, assigned_at=excluded.assigned_at, batch=excluded.batch`,
		unitID, assignee, by, actor, at, batch)
	return err
}

// whoAssigns is the account a write's assignments come from ("" unknown).
func (who Who) whoAssigns() string {
	if who.Creator == nil {
		return ""
	}
	return strings.TrimSpace(who.Creator())
}

// AssignedUnit is one unit of an AssignedCard.
type AssignedUnit struct {
	UID, Title, Type, Status string
	Since                    string // when it was assigned (MarkLayout)
	InNeeds                  bool   // in review or an open decision: Needs you lists it there
}

// AssignedCard is one batch of assignments by By to Assignee: the units that
// are still open and still assigned to Assignee.  Units already in Needs you
// for review or as a decision (InNeeds) show there once: AssignedTo leaves
// them out, but their read marks are kept.
type AssignedCard struct {
	Assignee, By, Actor, Batch string
	Units                      []AssignedUnit
}

// Key is the seen-mark key of a unit of the card.
func (u AssignedUnit) Key() string { return "a:" + u.UID }

// Marks are the card's seen marks (one per unit).
func (c AssignedCard) Marks() []NeedsMark {
	out := make([]NeedsMark, len(c.Units))
	for i, u := range c.Units {
		out[i] = NeedsMark{Key: u.Key(), Since: u.Since, By: c.By, Assignee: c.Assignee}
	}
	return out
}

// Latest is when the newest unit of the card was assigned.
func (c AssignedCard) Latest() string {
	latest := ""
	for _, u := range c.Units {
		if u.Since > latest {
			latest = u.Since
		}
	}
	return latest
}

// AssignedCards lists every open assignment of the database, newest batch
// first (any read connection, like NeedsCards).
func AssignedCards(ctx context.Context, q sqlitedb.Querier) ([]AssignedCard, error) {
	if has, err := hasAssignmentTable(ctx, q); err != nil || !has {
		return nil, err
	}
	rows, err := sqlitedb.Query(ctx, q, `SELECT a.assignee, a.assigned_by, a.actor, a.assigned_at, a.batch,
            u.uid, u.title, u.type, u.status, CASE WHEN `+needsYouCond("u.")+` THEN 1 ELSE 0 END AS in_needs
        FROM unit_assignments a JOIN units u ON u.id=a.unit_id
        WHERE u.assignee=a.assignee COLLATE NOCASE AND u.archived=0 AND u.status NOT IN (`+closedStatusSQL()+`)
        ORDER BY a.assigned_at DESC, u.id DESC`)
	if err != nil {
		return nil, err
	}
	var cards []AssignedCard
	at := map[string]int{}
	for _, r := range rows {
		key := strings.ToLower(r.Str("assignee")) + "\x00" + strings.ToLower(r.Str("assigned_by")) + "\x00" + r.Str("batch")
		i, ok := at[key]
		if !ok {
			i = len(cards)
			at[key] = i
			cards = append(cards, AssignedCard{Assignee: r.Str("assignee"), By: r.Str("assigned_by"),
				Actor: r.Str("actor"), Batch: r.Str("batch")})
		}
		cards[i].Units = append(cards[i].Units, AssignedUnit{UID: r.Str("uid"), Title: r.Str("title"),
			Type: r.Str("type"), Status: r.Str("status"), Since: r.Str("assigned_at"), InNeeds: r.Int("in_needs") != 0})
	}
	return cards, nil
}

// AssignedTo is the cards of cards assigned to user, without the units the
// person cleared from Needs you (seen.Cleared) and those Needs you lists for
// review or as a decision.
func AssignedTo(cards []AssignedCard, user string, seen NeedsSeen) []AssignedCard {
	var out []AssignedCard
	for _, c := range cards {
		if !sameUsername(c.Assignee, user) {
			continue
		}
		units := make([]AssignedUnit, 0, len(c.Units))
		for _, u := range c.Units {
			if !u.InNeeds && !seen.Cleared(u.Key(), u.Since) {
				units = append(units, u)
			}
		}
		if len(units) > 0 {
			c.Units = units
			out = append(out, c)
		}
	}
	return out
}

// AssignedKeys are the seen keys of every open assignment to user (the
// marks worth keeping).
func AssignedKeys(cards []AssignedCard, user string) map[string]bool {
	out := map[string]bool{}
	for _, c := range cards {
		if sameUsername(c.Assignee, user) {
			for _, u := range c.Units {
				out[u.Key()] = true
			}
		}
	}
	return out
}

// ClearedPrefix marks a seen item the person cleared from Needs you
// ("x:a:<uid>" -> the assignment's since then).
const ClearedPrefix = "x:"

// Cleared: the person cleared the item (key) from Needs you while it had
// this since; a later assignment shows again.
func (s NeedsSeen) Cleared(key, since string) bool {
	v, ok := s.Items[ClearedPrefix+key]
	return ok && v != "" && v >= since
}
