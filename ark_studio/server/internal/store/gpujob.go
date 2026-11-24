package store

// Go only (2026-11-23): a GPU queue job that ended, as an activity row, so
// that it reaches the update feed (GET /api/agent/v1/updates, kind
// gpu_job_done) of the agent that queued it, and through the session nudger
// the ARK session that queued it.  The detail holds no log text: activity
// is visible to every member.

import (
	"context"

	"arkstudio/go/internal/compat/pyjson"
)

// GPUJobDoneAction is the activity action of a finished GPU job.
const GPUJobDoneAction = "gpu.job.done"

// GPUJobActor is the actor of those rows.
const GPUJobActor = "system:gpu-queue"

// LogGPUJobDone records a finished GPU job (detail: job, name, server,
// status, exit_code, duration_s, error, log_path, owner, session, token).
func (s *Store) LogGPUJobDone(ctx context.Context, detail *pyjson.Object) error {
	return s.write(ctx, func(w *wtx) error {
		w.noTouch = true
		return w.log(GPUJobActor, GPUJobDoneAction, nil, detail)
	})
}
