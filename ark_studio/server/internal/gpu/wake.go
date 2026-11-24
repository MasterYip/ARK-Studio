package gpu

// Job-finished wake-ups (Go only, 2026-11-23): an agent that queued a job
// should not poll GET .../gpu/jobs until it ends.  A job records at submit
// time whom to wake (Wake, the gpu_jobs.wake column; the request body cannot
// set it), and when the job reaches a terminal state the queue tells
// Queue.OnFinished once (gpu_jobs.notified).  The server turns that into an
// update-feed item: the ARK session that queued the job gets it as an input
// turn (the session nudger), an external agent's watcher wakes on it.

import (
	"strings"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/platform/clock"
)

// Wake is whom a job's end is announced to.
type Wake struct {
	Session string `json:"session,omitempty"` // the ARK session that queued the job
	Token   string `json:"token,omitempty"`   // the personal agent token that queued it
	Agent   bool   `json:"agent,omitempty"`   // queued through the agent API (no session: the owner's feed)
}

// IsZero reports a job nobody waits for (queued from the web UI).
func (w Wake) IsZero() bool { return w.Session == "" && w.Token == "" && !w.Agent }

func (w Wake) encode() string {
	if w.IsZero() {
		return ""
	}
	o := pyjson.NewObject()
	if w.Session != "" {
		o.Set("session", w.Session)
	}
	if w.Token != "" {
		o.Set("token", w.Token)
	}
	if w.Agent {
		o.Set("agent", true)
	}
	return string(pyjson.Dumps(o))
}

func decodeWake(text string) Wake {
	v, err := pyjson.Loads(text)
	o, ok := v.(*pyjson.Object)
	if err != nil || !ok {
		return Wake{}
	}
	return Wake{Session: pyjson.Str(orEmpty(o.Value("session"))), Token: pyjson.Str(orEmpty(o.Value("token"))),
		Agent: pyjson.Truthy(o.Value("agent"))}
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// notifyMaxAge: a job that ended longer ago than this is not announced any
// more (a queue that ran without a listener for a long time).
const notifyMaxAge = 24 * time.Hour

// NotifyFinished tells OnFinished about the jobs that ended since the last
// look.  The worker calls it after every tick and Cancel after cancelling;
// without OnFinished it does nothing (the jobs wait for a listener).
func (q *Queue) NotifyFinished() {
	if q.OnFinished == nil || q.db == nil {
		return
	}
	q.notifyMu.Lock()
	defer q.notifyMu.Unlock()
	rows, err := q.query(`SELECT * FROM gpu_jobs WHERE notified=0 AND status IN ('completed','errored','canceled')
                          ORDER BY ended_at LIMIT 64`)
	if err != nil {
		return
	}
	for _, row := range rows {
		wake := decodeWake(pyjson.Str(orEmpty(row.Get("wake"))))
		job := public(row)
		id := job.Value("id")
		ended, ok := jobTime(job, "ended_at")
		stale := ok && clock.Now().Sub(ended) > notifyMaxAge
		if !wake.IsZero() && !stale {
			if q.OnFinished(job, wake) != nil {
				continue // next look
			}
		}
		_ = q.exec("UPDATE gpu_jobs SET notified=1 WHERE id=?", id)
	}
}

// jobTime is a job's time stamp (ARK writes +00:00; Z is accepted too).
func jobTime(job *pyjson.Object, key string) (time.Time, bool) {
	text := pyjson.Str(orEmpty(job.Value(key)))
	if strings.HasSuffix(text, "Z") {
		text = strings.TrimSuffix(text, "Z") + "+00:00"
	}
	return parseISO(text)
}

// JobDuration is how long a finished job ran (from start, or from submit
// when it never started); 0 when unknown.
func JobDuration(job *pyjson.Object) time.Duration {
	end, ok := jobTime(job, "ended_at")
	if !ok {
		return 0
	}
	start, ok := jobTime(job, "started_at")
	if !ok {
		if start, ok = jobTime(job, "created_at"); !ok {
			return 0
		}
	}
	if d := end.Sub(start); d > 0 {
		return d
	}
	return 0
}
