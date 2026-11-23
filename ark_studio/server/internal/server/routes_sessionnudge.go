package server

// Session nudges (Go only, T-0053): wiring of internal/sessionnudge.  A
// session ARK launched that is idle gets one input turn listing people's
// replies in ARK since its last look.  People's replies always reach the
// sessions ARK launched (T-0109): settings.sessions.nudge_idle is still
// accepted from older clients but no longer read.

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

// nudgeCursorKey: session meta key with the nudger's feed cursor
// {epoch, after}, so a server restart does not drop updates held back
// while a turn ran.
const nudgeCursorKey = "nudge_cursor"

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
				if err != nil || a.Sessions.NudgeExcluded(s) {
					continue // ended or closed by a person: ARK leaves it alone
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
				watching := false // a watcher the session started itself (before ARK_WAKE=ark)
				for _, id := range a.Sessions.AgentTokenIDs(s) {
					watching = watching || a.tokenLongPolling(id)
				}
				out = append(out, sessionnudge.Session{ID: s.ID, ProjectID: a.Sessions.ProjectID(s), User: owner,
					Idle: status == "idle", Accepts: true, Stopped: stopped, Watching: watching})
			}
			return out
		},
		ProjectName: func(id string) string {
			if p := a.nudgeProject(context.Background(), id); p != nil {
				return pyjson.Str(orDefault(p.Manifest.Get("name"), ""))
			}
			return ""
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
		Load: func(id string) (string, int64, bool) {
			s, err := a.Sessions.Get(id)
			if err != nil {
				return "", 0, false
			}
			c, _ := a.Sessions.MetaValue(s, nudgeCursorKey).(*pyjson.Object)
			if c == nil {
				return "", 0, false
			}
			after, ok := c.Value("after").(int64)
			epoch := pyjson.Str(c.Value("epoch"))
			return epoch, after, ok && epoch != ""
		},
		Save: func(id, epoch string, after int64) {
			if s, err := a.Sessions.Get(id); err == nil {
				a.Sessions.SetMeta(s, nudgeCursorKey, pyjson.NewObject("epoch", epoch, "after", after))
			}
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
