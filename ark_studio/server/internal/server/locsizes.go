package server

// Location sizes on the ARK server's own host (Go only, 2026-11-20).
//
// Reading a project's setup or Resources graph schedules, in the background
// and at most every ten minutes per project, a measurement of each of the
// project's locations on this host whose size is missing or older than
// sizeMaxAge.  One worker measures one path at a time on its own OS thread
// at idle IO priority (dirsize.Background), within a 20 s and two-million-
// entry cap, pausing between paths; requests never wait for it.  The ↻
// button (setup action "measure") queues one location again, at most once
// a minute.  Sizes of other devices are reported by those devices
// (setup action "location-stats", internal/setup/locstats.go).
//
// ARK_LOCATION_SIZES=off turns the measuring off; it is off under the
// replay clock (contract replays stay deterministic).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/platform/dirsize"
	"arkstudio/go/internal/setup"
)

const (
	sizeMaxAge       = 12 * time.Hour
	sizeCappedMaxAge = 72 * time.Hour // trees that hit a cap: measured less often
	sizeScanEvery    = 10 * time.Minute
	sizeRefreshEvery = time.Minute
	sizeQueueMax     = 500
	sizeGap          = 2 * time.Second
)

type sizeJob struct{ project, resource, host, path string }

func (j sizeJob) key() string { return j.project + "\x00" + setup.StatKey(j.resource, j.host, j.path) }

type sizeService struct {
	off     bool
	opt     dirsize.Options
	gap     time.Duration
	measure func(ctx context.Context, path string, opt dirsize.Options) dirsize.Result
	host    func() string

	mu       sync.Mutex
	queue    []sizeJob
	queued   map[string]bool
	running  bool
	lastScan map[string]time.Time
	lastAsk  map[string]time.Time
}

var sizeServices sync.Map // *App -> *sizeService

func newSizeService() *sizeService {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("ARK_LOCATION_SIZES")))
	return &sizeService{
		off: os.Getenv("ARK_REPLAY_CLOCK") == "1" || mode == "off" || mode == "0" || mode == "false" || mode == "no",
		opt: dirsize.Options{MaxDuration: dirsize.DefaultMaxDuration, MaxEntries: dirsize.DefaultMaxEntries,
			Pause: time.Millisecond, PauseEvery: 4096},
		gap:      sizeGap,
		measure:  dirsize.Background,
		host:     setup.MachineFingerprint,
		queued:   map[string]bool{},
		lastScan: map[string]time.Time{},
		lastAsk:  map[string]time.Time{},
	}
}

func (a *App) sizes() *sizeService {
	if s, ok := sizeServices.Load(a); ok {
		return s.(*sizeService)
	}
	s, _ := sizeServices.LoadOrStore(a, newSizeService())
	return s.(*sizeService)
}

// scan queues, in the background, this host's locations of the project
// whose size is due.  Throttled per project; never blocks.
func (s *sizeService) scan(projectPath string) {
	if s.off || projectPath == "" {
		return
	}
	now := time.Now()
	s.mu.Lock()
	if t, ok := s.lastScan[projectPath]; ok && now.Sub(t) < sizeScanEvery {
		s.mu.Unlock()
		return
	}
	s.lastScan[projectPath] = now
	s.mu.Unlock()
	go s.scanNow(projectPath, now)
}

func (s *sizeService) scanNow(projectPath string, now time.Time) {
	if fi, err := os.Stat(filepath.Join(projectPath, setup.ProfileDir)); err != nil || !fi.IsDir() {
		return
	}
	env := &setup.Env{Project: projectPath, DefaultOwner: "local"}
	setup.Lock()
	locs := env.HostLocations(s.host())
	setup.Unlock()
	if len(locs) == 0 {
		return
	}
	lookup := env.StatLookup()
	for _, l := range locs {
		stat := lookup(l.Resource, l.HostID, l.Path)
		maxAge := sizeMaxAge
		if stat != nil && pyjson.Truthy(stat.Value("capped")) {
			maxAge = sizeCappedMaxAge
		}
		if setup.NeedsMeasure(stat, now.UTC(), maxAge) {
			s.enqueue(sizeJob{projectPath, l.Resource, l.HostID, l.Path})
		}
	}
}

// refresh queues one location now (the ↻ button).  It answers false and
// the wait when the location was asked for less than a minute ago.
func (s *sizeService) refresh(job sizeJob) (bool, time.Duration) {
	if s.off {
		return false, 0
	}
	now := time.Now()
	s.mu.Lock()
	if t, ok := s.lastAsk[job.key()]; ok && now.Sub(t) < sizeRefreshEvery {
		s.mu.Unlock()
		return false, sizeRefreshEvery - now.Sub(t)
	}
	s.lastAsk[job.key()] = now
	if len(s.lastAsk) > 4*sizeQueueMax {
		for k, t := range s.lastAsk {
			if now.Sub(t) >= sizeRefreshEvery {
				delete(s.lastAsk, k)
			}
		}
	}
	s.mu.Unlock()
	return s.enqueue(job), 0
}

func (s *sizeService) enqueue(job sizeJob) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.queued[job.key()] {
		return true
	}
	if len(s.queue) >= sizeQueueMax {
		return false
	}
	s.queued[job.key()] = true
	s.queue = append(s.queue, job)
	if !s.running {
		s.running = true
		go s.work()
	}
	return true
}

func (s *sizeService) work() {
	for {
		s.mu.Lock()
		if len(s.queue) == 0 {
			s.running = false
			s.mu.Unlock()
			return
		}
		job := s.queue[0]
		s.queue = s.queue[1:]
		s.mu.Unlock()
		s.run(job)
		s.mu.Lock()
		delete(s.queued, job.key())
		s.mu.Unlock()
		if s.gap > 0 {
			time.Sleep(s.gap)
		}
	}
}

func (s *sizeService) run(job sizeJob) {
	r := s.measure(context.Background(), setup.ExpandPath(job.path), s.opt)
	_ = (&setup.Env{Project: job.project}).PutLocationStat(statFromResult(job, r, time.Now()))
}

// statFromResult is a measured result as a stored stat.
func statFromResult(job sizeJob, r dirsize.Result, now time.Time) *pyjson.Object {
	stat := pyjson.NewObject("resource", job.resource, "host_id", job.host, "path", job.path,
		"kind", r.Kind, "bytes", r.Bytes, "files", r.Files)
	if r.Git {
		stat.Set("git", true).Set("git_bytes", r.GitBytes).Set("git_files", r.GitFiles)
	}
	if r.Capped != "" {
		stat.Set("capped", r.Capped)
	}
	if r.Skipped > 0 {
		stat.Set("skipped", r.Skipped)
	}
	if r.Error != "" {
		stat.Set("error", r.Error)
	}
	return stat.Set("measured_at", now.UTC().Format("2006-01-02T15:04:05")+"+00:00").
		Set("source", "server").Set("by", "server")
}
