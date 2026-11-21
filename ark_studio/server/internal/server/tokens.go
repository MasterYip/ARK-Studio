package server

import (
	"context"
	"crypto/subtle"
	"os"
	"strings"
	"sync"
	"time"

	"arkstudio/go/internal/platform/clock"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/sessions"
	"arkstudio/go/internal/sqlitedb"
)

// Agent credentials: the legacy server token (<root>/.agent-token),
// personal arka_ tokens (org.sqlite3 agent_tokens) and read-only task-agent
// session tokens (agent-sessions/<id>/meta.json).

type personalToken struct {
	row, user sqlitedb.Row
}

// failureLimiter is agent_tokens.FailureLimiter (20 failures / 300 s per client).
type failureLimiter struct {
	mu      sync.Mutex
	buckets map[string][]float64
}

const failLimit, failWindow = 20, 300.0

func (f *failureLimiter) bucket(key string, now float64) []float64 {
	b := f.buckets[key]
	for len(b) > 0 && now-b[0] > failWindow {
		b = b[1:]
	}
	f.buckets[key] = b
	return b
}

// nowSeconds is time.time() for the limiter; the replay pins it (N8) so the
// 300 s window cannot expire a failure on one server and not the other.
func nowSeconds() float64 {
	if t, ok := clock.Replay(); ok {
		return float64(t.UnixNano()) / 1e9
	}
	return float64(time.Now().UnixNano()) / 1e9
}

func (f *failureLimiter) check(key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bucket(key, nowSeconds())) >= failLimit {
		return apiErr(429, "Too many failed agent token attempts. Wait a few minutes and try again.", "AGENT_TOKEN_RATE_LIMITED")
	}
	return nil
}

func (f *failureLimiter) record(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := nowSeconds()
	f.buckets[key] = append(f.bucket(key, now), now)
	if len(f.buckets) > 4096 {
		for k, b := range f.buckets {
			if len(b) == 0 {
				delete(f.buckets, k)
			}
		}
	}
}

// bindPersonalToken is Handler._bind_personal_agent_token.
func (c *reqCtx) bindPersonalToken() (bool, error) {
	if !c.personalAgentPathAllowed(c.path) {
		return false, nil
	}
	secret := c.agentToken()
	if !strings.HasPrefix(secret, "arka_") {
		return false, nil
	}
	client := c.clientIP()
	if err := c.app.failures.check(client); err != nil {
		return false, err
	}
	row, user, ok, err := c.app.authenticatePersonalToken(c.ctx, secret)
	if err != nil {
		return false, err
	}
	if !ok {
		c.app.failures.record(client)
		return false, apiErr(401, "The agent token is invalid, expired or revoked.", "AGENT_TOKEN_INVALID")
	}
	if c.app.Auth != nil {
		c.forcedProject = pytext.Prefix(pytext.Strip(c.header("X-ARK-Project")), 120)
	}
	c.user = pyjson.Str(user.Get("username"))
	c.token = &personalToken{row: row, user: user}
	return true, nil
}

// authenticatePersonalToken is the check behind a personal arka_ token: a
// live token of an enabled user who, in account mode, is still a member of
// the organisation. Shared by the agent API and the desktop gate.
func (a *App) authenticatePersonalToken(ctx context.Context, secret string) (sqlitedb.Row, sqlitedb.Row, bool, error) {
	row, user, ok, err := a.Org.AuthenticateToken(ctx, secret)
	if err != nil || !ok || a.Auth == nil {
		return row, user, ok, err
	}
	_, member, err := a.Org.Membership(ctx, user.Str("id"), a.Org.OrgID(ctx))
	if err != nil {
		return row, user, false, err
	}
	return row, user, member, nil
}

// requireAgent is Handler._require_agent (server token or bound personal token).
func (c *reqCtx) requireAgent() error {
	if c.token != nil {
		return nil
	}
	token := c.agentToken()
	client := c.clientIP()
	if err := c.app.failures.check(client); err != nil {
		return err
	}
	if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(c.app.AgentToken())) != 1 {
		if token != "" {
			c.app.failures.record(client)
		}
		return apiErr(401, "A valid agent token is required", "")
	}
	return nil
}

func (c *reqCtx) taskReadSession() *sessions.Session {
	token := c.readToken()
	if token == "" || c.app.Sessions == nil {
		return nil
	}
	return c.app.Sessions.ForReadToken(token)
}

// bindTaskSession is Handler._bind_task_agent_context.
func (c *reqCtx) bindTaskSession(s *sessions.Session) {
	ref, _ := s.Meta.Value("project").(*pyjson.Object)
	c.user = strOr(s.Meta.Value("actor"))
	c.forcedProject = strOr(ref.Value("id"))
	c.task = s
}

func strOr(v any) string {
	if !pyjson.Truthy(v) {
		return ""
	}
	return pyjson.Str(v)
}

// requireTaskAgent is Handler._require_task_agent.
func (c *reqCtx) requireTaskAgent() (*sessions.Session, error) {
	s := c.taskReadSession()
	if s == nil {
		return nil, apiErr(401, "A valid read-only task-agent token is required", "")
	}
	return s, nil
}

// rejectTaskReadToken is Handler._reject_task_read_token.
func (c *reqCtx) rejectTaskReadToken(action string) error {
	for _, candidate := range []string{c.readToken(), c.agentToken()} {
		if candidate != "" && c.app.Sessions != nil && c.app.Sessions.ForReadToken(candidate) != nil {
			return apiErr(403, "Task-agent read tokens cannot "+action, "")
		}
	}
	return nil
}

// projectByID is App.project_by_id.
func (c *reqCtx) projectByID(id string) (*project.Project, error) {
	id = pytext.Strip(id)
	if id == "" {
		return nil, nil
	}
	if id == demoID {
		p, err := c.demoProject(false)
		if err != nil {
			return nil, nil
		}
		return p, nil
	}
	row, ok, err := c.app.Org.ProjectRow(c.ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		if row, ok, err = c.app.Org.ProjectByPath(c.ctx, id); err != nil {
			return nil, err
		}
	}
	if ok {
		path := row.Str("path")
		if fi, err := os.Stat(path); err == nil && fi.IsDir() {
			if p, err := c.app.Projects.Open(path); err == nil {
				return p, nil
			}
			return nil, nil
		}
	}
	for _, s := range c.app.Projects.ListProjects() {
		if pyjson.Str(s.Value("id")) == id || pyjson.Str(s.Value("folder")) == id {
			if p, err := c.app.Projects.Open(pyjson.Str(s.Value("path"))); err == nil {
				return p, nil
			}
			return nil, nil
		}
	}
	return nil, nil
}
