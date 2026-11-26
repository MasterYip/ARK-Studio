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

type activitySessionKey struct{}

// WithActivitySession returns ctx with fn naming the ARK session an agent
// write under ctx comes from (Go only, 2026-11-24): the session id and how
// it is known ("token" for a session's own token, "header" for a checked
// X-ARK-Session header), "" when none.  An agent's activity rows then carry
// "session" and "session_via" in their detail, which ties the token's
// writes to the session in the support panel (SupportAgents).
func WithActivitySession(ctx context.Context, fn func() (id, via string)) context.Context {
	return context.WithValue(ctx, activitySessionKey{}, fn)
}

// ActivityAgent is who an agent write comes from besides its ARK session:
// the personal token's id, the device (X-ARK-Host) and the agent's own
// session id (X-ARK-Agent-Session), each "" when unknown.  They are kept in
// the detail as "agent_token", "agent_host" and "agent_session", and the
// update feed routes people's answers on a unit back to that writer
// (UnitWriters, agentwatch).
type ActivityAgent struct {
	Token, Host, AgentSession string
}

type activityAgentKey struct{}

// WithActivityAgent returns ctx with fn naming the token, device and agent
// session of the agent writes under ctx.
func WithActivityAgent(ctx context.Context, fn func() ActivityAgent) context.Context {
	return context.WithValue(ctx, activityAgentKey{}, fn)
}

func withActivitySession(ctx context.Context, actor string, detail *pyjson.Object) *pyjson.Object {
	if !isAgent(actor) {
		return detail
	}
	var id, via string
	if fn, _ := ctx.Value(activitySessionKey{}).(func() (string, string)); fn != nil {
		id, via = fn()
	}
	var who ActivityAgent
	if fn, _ := ctx.Value(activityAgentKey{}).(func() ActivityAgent); fn != nil {
		who = fn()
	}
	if id == "" && who == (ActivityAgent{}) {
		return detail
	}
	out := pyjson.NewObject()
	if detail != nil {
		out = detail.Copy()
	}
	if id != "" {
		out.Set("session", id).Set("session_via", via)
	}
	for _, kv := range [][2]string{{"agent_token", who.Token}, {"agent_host", who.Host}, {"agent_session", who.AgentSession}} {
		if kv[1] != "" {
			out.Set(kv[0], kv[1])
		}
	}
	return out
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
