package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// sqliteClass mirrors one class of the Go server's "ark_sqlite" expvar
// (go/internal/sqlitedb/stats.go).
type sqliteClass struct {
	Databases      int64     `json:"databases"`
	Writes         int64     `json:"writes"`
	Queued         int64     `json:"queued"`
	WaitCancelled  int64     `json:"wait_cancelled"`
	WaitTotalMS    float64   `json:"wait_total_ms"`
	WaitMaxMS      float64   `json:"wait_max_ms"`
	HoldTotalMS    float64   `json:"hold_total_ms"`
	HoldMaxMS      float64   `json:"hold_max_ms"`
	Depth          int64     `json:"depth"`
	DepthMax       int64     `json:"depth_max"`
	Busy           int64     `json:"busy_errors"`
	Locked         int64     `json:"locked_errors"`
	Checkpoints    int64     `json:"wal_checkpoints"`
	CheckpointBusy int64     `json:"wal_checkpoint_busy"`
	WALMaxBytes    int64     `json:"wal_max_bytes"`
	HistLeMS       []float64 `json:"wait_hist_le_ms"`
	Hist           []int64   `json:"wait_hist"`
	ReadPool       struct {
		WaitCount   int64   `json:"wait_count"`
		WaitTotalMS float64 `json:"wait_total_ms"`
		InUse       int64   `json:"in_use"`
		Open        int64   `json:"open"`
	} `json:"read_pool"`
}

func fetchServerStats(statsURL string) (map[string]sqliteClass, error) {
	c := &http.Client{Timeout: 10 * time.Second}
	resp, err := c.Get(statsURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: status %d", statsURL, resp.StatusCode)
	}
	var vars struct {
		ArkSQLite map[string]sqliteClass `json:"ark_sqlite"`
	}
	if err := json.Unmarshal(data, &vars); err != nil {
		return nil, err
	}
	if vars.ArkSQLite == nil {
		return nil, fmt.Errorf("%s has no ark_sqlite variable", statsURL)
	}
	return vars.ArkSQLite, nil
}

// histPercentile is the upper bound (ms) of the bucket holding the p-th
// observation (statsDelta caps it at the observed maximum).  The overflow
// bucket reports the last bound (30 s, the server's request timeout).
func histPercentile(bounds []float64, counts []int64, p float64) float64 {
	var total int64
	for _, c := range counts {
		total += c
	}
	if total == 0 || len(bounds) == 0 {
		return 0
	}
	rank := int64(float64(total-1)*p) + 1
	var seen int64
	for i, c := range counts {
		seen += c
		if seen >= rank {
			if i < len(bounds) {
				return bounds[i]
			}
			return bounds[len(bounds)-1]
		}
	}
	return bounds[len(bounds)-1]
}

// statsDelta reports what happened between two snapshots: counters as
// deltas; maxima, depth and pool gauges as the "after" value (since server
// start), plus write-queue wait percentiles from the histogram delta.
func statsDelta(before, after map[string]sqliteClass) map[string]any {
	out := map[string]any{}
	for name, a := range after {
		b := before[name]
		hist := make([]int64, len(a.Hist))
		for i := range a.Hist {
			hist[i] = a.Hist[i]
			if i < len(b.Hist) {
				hist[i] -= b.Hist[i]
			}
		}
		writes := a.Writes - b.Writes
		cls := map[string]any{
			"databases": a.Databases, "writes": writes, "queued": a.Queued - b.Queued,
			"wait_cancelled": a.WaitCancelled - b.WaitCancelled,
			"wait_total_ms":  a.WaitTotalMS - b.WaitTotalMS, "hold_total_ms": a.HoldTotalMS - b.HoldTotalMS,
			"busy_errors": a.Busy - b.Busy, "locked_errors": a.Locked - b.Locked,
			"wal_checkpoints": a.Checkpoints - b.Checkpoints, "wal_checkpoint_busy": a.CheckpointBusy - b.CheckpointBusy,
			"wal_max_bytes":   a.WALMaxBytes,
			"wait_p50_ms":     min(histPercentile(a.HistLeMS, hist, .50), a.WaitMaxMS),
			"wait_p95_ms":     min(histPercentile(a.HistLeMS, hist, .95), a.WaitMaxMS),
			"wait_p99_ms":     min(histPercentile(a.HistLeMS, hist, .99), a.WaitMaxMS),
			"wait_hist_le_ms": a.HistLeMS, "wait_hist": hist,
			"since_start": map[string]any{"wait_max_ms": a.WaitMaxMS, "hold_max_ms": a.HoldMaxMS,
				"depth_max": a.DepthMax, "depth_now": a.Depth},
			"read_pool": map[string]any{"wait_count": a.ReadPool.WaitCount - b.ReadPool.WaitCount,
				"wait_total_ms": a.ReadPool.WaitTotalMS - b.ReadPool.WaitTotalMS,
				"in_use_now":    a.ReadPool.InUse, "open_now": a.ReadPool.Open},
		}
		if writes > 0 {
			cls["wait_mean_ms"] = (a.WaitTotalMS - b.WaitTotalMS) / float64(writes)
			cls["hold_mean_ms"] = (a.HoldTotalMS - b.HoldTotalMS) / float64(writes)
			cls["queued_fraction"] = float64(a.Queued-b.Queued) / float64(writes)
		}
		out[name] = cls
	}
	return out
}
