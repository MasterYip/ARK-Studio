package server

// Session nudges (Go only, T-0053): wiring of internal/sessionnudge.  A
// session ARK launched that is idle gets one input turn listing people's
// replies in ARK since its last look.  Project setting
// settings.sessions.nudge_idle (default on).

import (
	"context"
	"strings"
	"time"

	"arkstudio/go/internal/agentwatch"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/sessionnudge"
	"arkstudio/go/internal/sessions"
)

// nudgeResumeWindow: a finished session is woken only this long after its
// last activity (older ones are started again by a person).
const nudgeResumeWindow = 24 * time.Hour

// NudgeEnabled reads settings.sessions.nudge_idle (absent: on).
func NudgeEnabled(p *project.Project) bool {
	settings := p.Manifest.Get("settings")
	s, _ := settings.(*pyjson.Object)
	if s == nil {
		return true
	}
	sess, _ := s.Value("sessions").(*pyjson.Object)
	if sess == nil {
		return true
	}
	v, ok := sess.Get("nudge_idle")
	return !ok || v == nil || pyjson.Truthy(v)
}

func (a *App) nudgeProject(ctx context.Context, id string) *project.Project {
	c := &reqCtx{app: a, ctx: ctx}
	p, err := c.projectByID(id)
	if err != nil {
		return nil
	}
	return p
}

// NudgeDeps connects the nudger to this app.
func (a *App) NudgeDeps() sessionnudge.Deps {
	return sessionnudge.Deps{
		Sessions: func() []sessionnudge.Session {
			var out []sessionnudge.Session
			for _, b := range a.Sessions.Brief() {
				status := pyjson.Str(b.Value("status"))
				s, err := a.Sessions.Get(pyjson.Str(b.Value("id")))
				if err != nil {
					continue
				}
				stopped := false
				switch status {
				case "idle", "running":
				case "stopped": // finished one-shot runs, recent and natively resumable
					last, err := time.Parse(time.RFC3339, pyjson.Str(b.Value("last_activity_at")))
					stopped = err == nil && time.Since(last) < nudgeResumeWindow && a.Sessions.Resumable(s)
					if !stopped {
						continue
					}
				default:
					continue
				}
				owner, _ := a.Sessions.Owner(s)
				out = append(out, sessionnudge.Session{ID: s.ID, ProjectID: a.Sessions.ProjectID(s), User: owner,
					Idle: status == "idle", Accepts: true, Stopped: stopped})
			}
			return out
		},
		Enabled: func(id string) bool {
			p := a.nudgeProject(context.Background(), id)
			return p != nil && NudgeEnabled(p)
		},
		Top: func(ctx context.Context, id string) (string, int64, error) {
			p := a.nudgeProject(ctx, id)
			if p == nil {
				return "", 0, context.Canceled
			}
			return p.Store.ActivityHead(ctx)
		},
		Since: func(ctx context.Context, s sessionnudge.Session, after int64) (sessionnudge.Batch, error) {
			p := a.nudgeProject(ctx, s.ProjectID)
			if p == nil {
				return sessionnudge.Batch{}, context.Canceled
			}
			batch, err := p.Store.UpdatesSince(ctx, after, 200)
			if err != nil {
				return sessionnudge.Batch{}, err
			}
			v := agentwatch.Viewer{User: s.User, AnyAgent: true, Mine: true}
			return sessionnudge.Batch{Items: agentwatch.Items(batch.Rows, v, nil), Epoch: batch.Epoch, Top: batch.Top}, nil
		},
		Send: func(s sessionnudge.Session, text string) error {
			if s.Stopped {
				// The owner's agent settings (model, provider overrides) as when they continue it.
				c := &reqCtx{app: a, ctx: context.Background(), user: s.User}
				_, err := a.Sessions.ContinueSession(c.sessionHooks(), s.ID, text,
					sessions.ContinueOptions{Resume: true, Actor: s.User})
				return err
			}
			_, err := a.Sessions.SendInput(nil, s.ID, text, nil)
			if err != nil && strings.Contains(err.Error(), "does not accept") {
				return nil // a one-shot session: nothing to wake, move on
			}
			return err
		},
	}
}

// StartSessionNudge runs the nudger every `every` until ctx ends.
func (a *App) StartSessionNudge(ctx context.Context, every time.Duration) {
	if a.Sessions == nil {
		return
	}
	n := &sessionnudge.Nudger{D: a.NudgeDeps()}
	go n.Run(ctx, every)
}
