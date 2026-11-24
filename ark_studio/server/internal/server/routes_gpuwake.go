package server

// GPU job wake-ups (Go only, 2026-11-23; internal/gpu/wake.go): whom a job
// queued through the API wakes when it ends, and the gpu.job.done activity
// row that carries the news to the update feed (kind gpu_job_done).  The
// session nudger delivers it to the ARK session that queued the job; an
// external agent's watcher wakes on it.

import (
	"context"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/gpu"
	"arkstudio/go/internal/org"
)

// gpuWake is whom a job queued by this request wakes: the ARK session whose
// agent token sent it, else the session the body names when it is this
// user's ARK session in this project (local mode shares one token), else
// the agent itself through the feed.  A person's job from the web UI wakes
// nobody.
func (c *reqCtx) gpuWake(projectID, session string) gpu.Wake {
	var w gpu.Wake
	if c.token != nil {
		w.Token = pyjson.Str(c.token.row.Get("id"))
		label := pyjson.Str(c.token.row.Get("label"))
		if id, ok := strings.CutPrefix(label, org.SessionTokenLabel); ok && c.app.Sessions != nil {
			if _, err := c.app.Sessions.Get(id); err == nil {
				w.Session = id
				return w
			}
		}
	}
	if !strings.HasPrefix(c.path, "/api/agent/") && c.token == nil {
		return gpu.Wake{}
	}
	w.Agent = true
	session = pytext.Strip(session)
	if session == "" || c.app.Sessions == nil {
		return w
	}
	s, err := c.app.Sessions.Get(session)
	if err != nil || c.app.Sessions.ProjectID(s) != projectID {
		return w
	}
	if owner, _ := c.app.Sessions.Owner(s); !strings.EqualFold(owner, c.actor()) {
		return w
	}
	w.Session = s.ID
	return w
}

// gpuJobFinished is Queue.OnFinished: a gpu.job.done row in the job's
// project.  A project that is gone drops the news.
func (a *App) gpuJobFinished(job *pyjson.Object, wake gpu.Wake) error {
	p := a.nudgeProject(context.Background(), pyjson.Str(job.Value("project")))
	if p == nil {
		return nil
	}
	detail := pyjson.NewObject("job", job.Value("id"), "name", job.Value("name"), "server", job.Value("server"),
		"status", job.Value("status"), "exit_code", job.Value("exit_code"),
		"duration_s", int64(gpu.JobDuration(job).Seconds()), "error", pytext.Prefix(strOrEmptyAny(job.Value("error")), 300),
		"log_path", job.Value("log_path"), "owner", job.Value("owner"), "session", wake.Session, "token", wake.Token)
	return p.Store.LogGPUJobDone(context.Background(), detail)
}
