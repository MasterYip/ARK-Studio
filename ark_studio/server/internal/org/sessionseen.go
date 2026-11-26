package org

// Go only: what each person has seen of their sessions' finished turns, and
// their session notification switches, for the "finished" bubbles of
// Execute › Runs and the finish notifications (server/session_notices.go).
// Kept on the server so every device of the person agrees.
//
// session_seen has one row per (username, item):
//   item "s:<session id>"  value = the turn_end.at the person has seen
//   item "pref:done"       value "0" turns off "session finished" notifications
//   item "pref:error"      value "0" turns off "session failed" notifications
// tests/python/contract/statediff.py GO_ONLY_TABLES leaves the table out of
// the Python/Go state comparison.

import (
	"context"
	"strings"

	"arkstudio/go/internal/sqlitedb"
)

const sessionSeenSchema = `CREATE TABLE IF NOT EXISTS session_seen(
  username TEXT NOT NULL,
  item TEXT NOT NULL,
  value TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL,
  PRIMARY KEY(username, item)
)`

// SessionNotifyPrefs are a person's session notification switches.
type SessionNotifyPrefs struct {
	Done, Error bool
}

// SessionSeen is what username has seen: the seen turn_end.at by session id,
// and their notification switches (both on unless turned off).
func (m *Manager) SessionSeen(ctx context.Context, username string) (map[string]string, SessionNotifyPrefs, error) {
	prefs := SessionNotifyPrefs{Done: true, Error: true}
	rows, err := m.rows(ctx, "SELECT item, value FROM session_seen WHERE username=?", strings.ToLower(username))
	if err != nil {
		return nil, prefs, err
	}
	seen := map[string]string{}
	for _, r := range rows {
		item, value := r.Str("item"), r.Str("value")
		switch {
		case strings.HasPrefix(item, "s:"):
			seen[item[2:]] = value
		case item == "pref:done":
			prefs.Done = value != "0"
		case item == "pref:error":
			prefs.Error = value != "0"
		}
	}
	return seen, prefs, nil
}

// MarkSessionSeen records that username has seen the session's finish at
// `at` (a turn_end.at; a later finish is new again).
func (m *Manager) MarkSessionSeen(ctx context.Context, username, sessionID, at, now string) error {
	return m.DB.Write(ctx, func(q sqlitedb.Querier) error {
		_, err := q.ExecContext(ctx, "INSERT INTO session_seen(username,item,value,updated_at) VALUES(?,?,?,?) "+
			"ON CONFLICT(username,item) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at",
			strings.ToLower(username), "s:"+sessionID, at, now)
		return err
	}, nil)
}

// SetSessionNotifyPrefs saves username's notification switches.
func (m *Manager) SetSessionNotifyPrefs(ctx context.Context, username string, prefs SessionNotifyPrefs, now string) error {
	flag := func(on bool) string {
		if on {
			return "1"
		}
		return "0"
	}
	user := strings.ToLower(username)
	return m.DB.Write(ctx, func(q sqlitedb.Querier) error {
		for item, value := range map[string]string{"pref:done": flag(prefs.Done), "pref:error": flag(prefs.Error)} {
			if _, err := q.ExecContext(ctx, "INSERT INTO session_seen(username,item,value,updated_at) VALUES(?,?,?,?) "+
				"ON CONFLICT(username,item) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at",
				user, item, value, now); err != nil {
				return err
			}
		}
		return nil
	}, nil)
}

// ForgetSession drops everyone's seen mark of a deleted session.
func (m *Manager) ForgetSession(ctx context.Context, sessionID string) error {
	return m.DB.Write(ctx, func(q sqlitedb.Querier) error {
		_, err := q.ExecContext(ctx, "DELETE FROM session_seen WHERE item=?", "s:"+sessionID)
		return err
	}, nil)
}
