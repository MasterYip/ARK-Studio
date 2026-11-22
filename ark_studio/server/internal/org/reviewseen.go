package org

// Go only: what each person has seen of each project's Needs you list, for
// the project picker's "new for review" badges (internal/server/
// project_reviews.go, store/needscards.go).  Kept on the server so every
// device of the person agrees.
//
// review_seen has one row per (username, project, item):
//   item "*"        the list watermark: when the person last opened the
//                   project's Needs you list (since = seen_at = that time)
//   item "u:<uid>"  a unit (or "rg:RG-n" a review group) the person opened
//                   later, with the item's since at that time
// Opening the list moves the watermark and drops the item rows it covers,
// so the table holds at most one row per open item per person.
// tests/python/contract/statediff.py GO_ONLY_TABLES leaves the table out of
// the Python/Go state comparison.

import (
	"context"
	"strings"

	"arkstudio/go/internal/sqlitedb"
	"arkstudio/go/internal/store"
)

const reviewSeenSchema = `CREATE TABLE IF NOT EXISTS review_seen(
  username TEXT NOT NULL,
  project_id TEXT NOT NULL,
  item TEXT NOT NULL,
  since TEXT NOT NULL DEFAULT '',
  seen_at TEXT NOT NULL,
  PRIMARY KEY(username, project_id, item)
)`

// reviewSeenWatermark is the item of the list watermark row.
const reviewSeenWatermark = "*"

// ReviewSeen is everything username has seen, by project id (one query).
func (m *Manager) ReviewSeen(ctx context.Context, username string) (map[string]store.NeedsSeen, error) {
	rows, err := m.rows(ctx, "SELECT project_id, item, since FROM review_seen WHERE username=?", strings.ToLower(username))
	if err != nil {
		return nil, err
	}
	out := map[string]store.NeedsSeen{}
	for _, r := range rows {
		id := r.Str("project_id")
		s, ok := out[id]
		if !ok {
			s = store.NeedsSeen{Items: map[string]string{}}
		}
		if item := r.Str("item"); item == reviewSeenWatermark {
			s.Watermark = r.Str("since")
		} else {
			s.Items[item] = r.Str("since")
		}
		out[id] = s
	}
	return out, nil
}

// MarkReviewListSeen moves username's watermark of the project to at and
// forgets the item marks it covers.
func (m *Manager) MarkReviewListSeen(ctx context.Context, username, projectID, at string) error {
	user := strings.ToLower(username)
	return m.DB.Write(ctx, func(q sqlitedb.Querier) error {
		if _, err := q.ExecContext(ctx, "INSERT INTO review_seen(username,project_id,item,since,seen_at) VALUES(?,?,?,?,?) "+
			"ON CONFLICT(username,project_id,item) DO UPDATE SET since=excluded.since, seen_at=excluded.seen_at "+
			"WHERE excluded.since >= review_seen.since", user, projectID, reviewSeenWatermark, at, at); err != nil {
			return err
		}
		_, err := q.ExecContext(ctx, "DELETE FROM review_seen WHERE username=? AND project_id=? AND item<>? AND since<>'' AND since<?",
			user, projectID, reviewSeenWatermark, at)
		return err
	}, nil)
}

// MarkReviewItemsSeen records the items (key -> the item's current since)
// as seen by username.
func (m *Manager) MarkReviewItemsSeen(ctx context.Context, username, projectID string, items map[string]string, at string) error {
	if len(items) == 0 {
		return nil
	}
	user := strings.ToLower(username)
	return m.DB.Write(ctx, func(q sqlitedb.Querier) error {
		for key, since := range items {
			if _, err := q.ExecContext(ctx, "INSERT INTO review_seen(username,project_id,item,since,seen_at) VALUES(?,?,?,?,?) "+
				"ON CONFLICT(username,project_id,item) DO UPDATE SET since=excluded.since, seen_at=excluded.seen_at",
				user, projectID, key, since, at); err != nil {
				return err
			}
		}
		return nil
	}, nil)
}
