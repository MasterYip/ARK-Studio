package main

import (
	"runtime"
	"sync"
	"time"
)

// procUsage is one process-tree sample.
type procUsage struct {
	cpu     time.Duration // user+kernel, cumulative
	rss     uint64        // working set bytes
	private uint64        // private/committed bytes
	threads int
	procs   int
}

// Usage is the summary stored in the result.
type Usage struct {
	PID            int     `json:"pid"`
	Samples        int     `json:"samples"`
	CPUCoresAvg    float64 `json:"cpu_cores_avg"`
	CPUCoresPeak   float64 `json:"cpu_cores_peak"`
	CPUPercentHost float64 `json:"cpu_percent_of_host_avg"`
	RSSMBAvg       float64 `json:"rss_mb_avg"`
	RSSMBPeak      float64 `json:"rss_mb_peak"`
	PrivateMBPeak  float64 `json:"private_mb_peak"`
	ThreadsPeak    int     `json:"threads_peak"`
	Processes      int     `json:"processes"`
	HostCPUs       int     `json:"host_cpus"`
}

type sampler struct {
	pid  int
	stop func() Usage
}

func startSampler(pid int) *sampler {
	s := &sampler{pid: pid}
	if pid == 0 {
		s.stop = func() Usage { return Usage{} }
		return s
	}
	done := make(chan struct{})
	var mu sync.Mutex
	u := Usage{PID: pid, HostCPUs: runtime.NumCPU()}
	var rssSum float64
	go func() {
		prev, okPrev := sampleTree(pid)
		prevAt := time.Now()
		first, firstAt := prev, prevAt
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				cur, ok := sampleTree(pid)
				mu.Lock()
				if ok && okPrev {
					wall := time.Since(firstAt).Seconds()
					u.CPUCoresAvg = (cur.cpu - first.cpu).Seconds() / wall
					u.CPUPercentHost = 100 * u.CPUCoresAvg / float64(u.HostCPUs)
				}
				if u.Samples > 0 {
					u.RSSMBAvg = rssSum / float64(u.Samples)
				}
				mu.Unlock()
				close(done)
				return
			case <-tick.C:
				cur, ok := sampleTree(pid)
				if !ok {
					continue
				}
				now := time.Now()
				mu.Lock()
				if okPrev {
					cores := (cur.cpu - prev.cpu).Seconds() / now.Sub(prevAt).Seconds()
					u.CPUCoresPeak = max(u.CPUCoresPeak, cores)
				}
				mb := float64(cur.rss) / (1 << 20)
				u.Samples++
				rssSum += mb
				u.RSSMBPeak = max(u.RSSMBPeak, mb)
				u.PrivateMBPeak = max(u.PrivateMBPeak, float64(cur.private)/(1<<20))
				u.ThreadsPeak = max(u.ThreadsPeak, cur.threads)
				u.Processes = max(u.Processes, cur.procs)
				mu.Unlock()
				prev, prevAt, okPrev = cur, now, true
			}
		}
	}()
	s.stop = func() Usage {
		done <- struct{}{}
		<-done
		mu.Lock()
		defer mu.Unlock()
		return u
	}
	return s
}
