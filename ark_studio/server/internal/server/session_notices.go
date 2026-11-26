package server

// Go only: finished sessions the person has not looked at yet, for the
// bubbles on Execute and Runs, the session list's unseen marks and the
// browser/desktop notifications (web/js/session-notices.js).
//
//   GET  /api/agent-sessions/notices   the current project's unseen finishes
//   POST /api/agent-sessions/notices   {"seen": ["s_..."]} marks sessions seen
//                                      (their current finish); {"prefs":
//                                      {"done": bool, "error": bool}} saves
//                                      the notification switches
//
// Both answer {items: [{id, title, outcome, at, reason}], done, errors,
// prefs: {done, error}}.  A finish is a session's turn_end (sessions/
// finish.go): the main conversation's turn ended cleanly ("done") or failed
// ("error").  It counts for the session's owner while no new turn runs,
// until they open the session (org/sessionseen.go keeps what they saw per
// person, so every device agrees).  The phone feed (notifications.go) reads
// the same finishes and seen marks.

import (
	"sort"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/sessions"
	"arkstudio/go/internal/store"
)

const (
	sessionNoticesPath = "/api/agent-sessions/notices"
	sessionSeenMax     = 200
)

var sessionNoticesGoOnly = []PortedRoute{{"goonly:exact:" + sessionNoticesPath, "GET,HEAD"},
	{"goonly:exact:" + sessionNoticesPath, "POST"}}

// unseenFinishes are the finishes of project (all projects when empty) that
// wait for user: theirs (any session without sign-in), main conversations
// only, no turn running, not closed, newer than what they have seen.
func unseenFinishes(all []sessions.Finish, projectID, user string, auth bool, seen map[string]string) []sessions.Finish {
	var out []sessions.Finish
	for _, f := range all {
		if f.Outcome == "" || f.At == "" || !f.Main || f.Busy || f.Closed {
			continue
		}
		if projectID != "" && f.ProjectID != projectID {
			continue
		}
		if auth && !sameActor(f.Owner, user) {
			continue
		}
		at := store.ParseTime(f.At)
		if at.IsZero() {
			continue
		}
		if mark := seen[f.ID]; mark != "" {
			if s := store.ParseTime(mark); !s.IsZero() && !at.After(s) {
				continue
			}
		}
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool { return store.ParseTime(out[i].At).After(store.ParseTime(out[j].At)) })
	return out
}

// sessionNoticesPayload is the reply of both routes.
func sessionNoticesPayload(list []sessions.Finish, prefs org.SessionNotifyPrefs) *pyjson.Object {
	items := []any{}
	done, errs := 0, 0
	for _, f := range list {
		if f.Outcome == "error" {
			errs++
		} else {
			done++
		}
		items = append(items, pyjson.NewObject("id", f.ID, "title", f.Title, "outcome", f.Outcome, "at", f.At,
			"reason", f.Reason))
	}
	return pyjson.NewObject("items", items, "done", int64(done), "errors", int64(errs),
		"prefs", pyjson.NewObject("done", prefs.Done, "error", prefs.Error))
}

func (c *reqCtx) sessionNotices(projectID string) (*pyjson.Object, error) {
	user := c.actor()
	seen, prefs, err := c.app.Org.SessionSeen(c.ctx, user)
	if err != nil {
		return nil, err
	}
	var all []sessions.Finish
	if c.app.Sessions != nil {
		all = c.app.Sessions.Finishes()
	}
	return sessionNoticesPayload(unseenFinishes(all, projectID, user, c.app.Auth != nil, seen), prefs), nil
}

// getSessionNotices answers GET /api/agent-sessions/notices.
func (c *reqCtx) getSessionNotices() error {
	p, err := c.require("read")
	if err != nil {
		return err
	}
	out, err := c.sessionNotices(p.ID())
	if err == nil {
		c.ok(out)
	}
	return err
}

// postSessionNotices answers POST /api/agent-sessions/notices.
func (c *reqCtx) postSessionNotices() error {
	p, err := c.require("read") // only the person's own marks
	if err != nil {
		return err
	}
	user := c.actor()
	if c.app.Auth != nil && user == "" {
		return apiErr(401, "Sign in with the ARK account to continue.", "AUTH_REQUIRED")
	}
	body := c.body
	if body == nil {
		body = pyjson.NewObject()
	}
	now := clock.Format(clock.Now().UTC())
	if raw := body.Value("seen"); raw != nil {
		ids, ok := raw.([]any)
		if !ok || len(ids) > sessionSeenMax {
			return apiErr(400, "seen must be a list of session ids", "")
		}
		at := map[string]string{}
		if c.app.Sessions != nil {
			for _, f := range c.app.Sessions.Finishes() {
				at[f.ID] = f.At
			}
		}
		for _, v := range ids {
			id, _ := v.(string)
			if !sessions.IDRe.MatchString(id) {
				return apiErr(400, "seen must be a list of session ids", "")
			}
			if at[id] == "" {
				continue // nothing finished yet
			}
			if err := c.guardSessionAccess(id, "read"); err != nil {
				return err
			}
			if err := c.app.Org.MarkSessionSeen(c.ctx, user, id, at[id], now); err != nil {
				return err
			}
		}
	}
	if raw, ok := body.Value("prefs").(*pyjson.Object); ok {
		_, prefs, err := c.app.Org.SessionSeen(c.ctx, user)
		if err != nil {
			return err
		}
		if v, ok := raw.Value("done").(bool); ok {
			prefs.Done = v
		}
		if v, ok := raw.Value("error").(bool); ok {
			prefs.Error = v
		}
		if err := c.app.Org.SetSessionNotifyPrefs(c.ctx, user, prefs, now); err != nil {
			return err
		}
	}
	out, err := c.sessionNotices(p.ID())
	if err == nil {
		c.ok(out)
	}
	return err
}

// finishWithin: the finish happened in the last d (the phone feed's window).
func finishWithin(f sessions.Finish, now time.Time, d time.Duration) bool {
	at := store.ParseTime(f.At)
	return !at.IsZero() && now.Sub(at) <= d
}
