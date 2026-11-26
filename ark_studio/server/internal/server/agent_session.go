package server

// Which ARK session an agent API request comes from (Go only, 2026-11-24).
// A session ARK launches gets its own agent token (routes_sessiontokens.go,
// label "ARK session <id>"), so a request with that token is the session's
// for certain.  A request with another token of the same person is tied to
// a session only by an X-ARK-Session header naming a session of that person
// in this project (the CLI helpers and watcher scripts send it from
// ARK_SESSION_ID).  Agent writes record the result in their activity detail
// (store.WithActivitySession), and the support panel lists such an agent as
// part of its session instead of a second "agent token" row.

import (
	"strings"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/sessions"
	"arkstudio/go/internal/store"
)

// SessionHeader names the ARK session an agent request comes from.
const SessionHeader = "X-ARK-Session"

type sessionLink struct{ id, via string }

// agentSessionLink is the request's ARK session: (id, "token") for a
// session's own token, (id, "header") for a checked X-ARK-Session header,
// ("", "") otherwise.  Computed once per request.
func (c *reqCtx) agentSessionLink() (string, string) {
	if c.sessLink == nil {
		c.sessLink = &sessionLink{}
		c.sessLink.id, c.sessLink.via = c.resolveAgentSession()
	}
	return c.sessLink.id, c.sessLink.via
}

func (c *reqCtx) resolveAgentSession() (string, string) {
	if c.token != nil {
		if label, _ := c.token.row.Get("label").(string); strings.HasPrefix(label, org.SessionTokenLabel) {
			if id := strings.TrimPrefix(label, org.SessionTokenLabel); sessions.IDRe.MatchString(id) {
				return id, "token"
			}
		}
	}
	id := pytext.Strip(c.header(SessionHeader))
	if id == "" || c.app.Sessions == nil || !sessions.IDRe.MatchString(id) {
		return "", ""
	}
	s, err := c.app.Sessions.Get(id)
	if err != nil {
		return "", ""
	}
	if c.app.Auth != nil {
		// Only the person who launched the session can speak for it.
		if actor, _ := c.app.Sessions.Owner(s); actor == "" || actor != c.actor() {
			return "", ""
		}
	}
	p := c.routeProject
	if p == nil {
		p = c.project
	}
	if p != nil && c.app.Sessions.ProjectID(s) != p.ID() {
		return "", ""
	}
	return id, "header"
}

// AgentSessionHeader is the agent's own session id when it runs outside ARK
// (a Claude Code or Codex session; the CLI helpers send ARK_AGENT_SESSION).
// It tells two agents with the same token on one device apart.
const AgentSessionHeader = "X-ARK-Agent-Session"

var reAgentSessionID = pyRe(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,99}$`, false)

// agentWriter is who the request writes as besides its ARK session: the
// personal token, the device and the agent's own session id.
func (c *reqCtx) agentWriter() store.ActivityAgent {
	var w store.ActivityAgent
	if c.token != nil {
		w.Token = strOrEmptyAny(c.token.row.Get("id"))
	}
	if h := pytext.Strip(c.header("X-ARK-Host")); reAgentSessionID.MatchString(h) {
		w.Host = h
	}
	if s := pytext.Strip(c.header(AgentSessionHeader)); reAgentSessionID.MatchString(s) {
		w.AgentSession = s
	}
	return w
}

// agentSelf is the request's writer identity, as agent writes record it.
func (c *reqCtx) agentSelf() store.Writer {
	id, _ := c.agentSessionLink()
	w := c.agentWriter()
	return store.Writer{Session: id, AgentSession: w.AgentSession, Token: w.Token, Host: w.Host}
}

// sessionWakeable reports whether ARK can still wake an ARK session: it
// exists, nobody closed it, and it runs or finished recently and can be
// resumed (as the session nudger sees it).
func (a *App) sessionWakeable(id string) bool {
	if a.Sessions == nil {
		return false
	}
	s, err := a.Sessions.Get(id)
	if err != nil || a.Sessions.NudgeExcluded(s) {
		return false
	}
	if a.Sessions.IsActive(s) {
		return true
	}
	last, err := time.Parse(time.RFC3339, pyjson.Str(a.Sessions.MetaValue(s, "last_activity_at")))
	return err == nil && time.Since(last) < nudgeResumeWindow && a.Sessions.Resumable(s)
}
