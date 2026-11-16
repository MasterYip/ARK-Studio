package main

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestBuildMix(t *testing.T) {
	for _, tc := range []struct {
		pct    float64
		topics bool
		ops    int
	}{{0, true, 7}, {15, true, 11}, {15, false, 9}, {50, true, 11}} {
		mix := buildMix(tc.pct, tc.topics)
		if len(mix) != tc.ops {
			t.Fatalf("pct %v topics %v: %d ops, want %d", tc.pct, tc.topics, len(mix), tc.ops)
		}
		extra := map[string]float64{}
		for _, w := range writeOps {
			extra[w.name] = w.extraReads
		}
		var sum, writes, requests float64
		for _, o := range mix {
			sum += o.weight
			requests += o.weight * (1 + extra[o.name])
			if o.write {
				writes += o.weight
			}
		}
		if math.Abs(sum-1) > 1e-9 || math.Abs(writes/requests-tc.pct/100) > 1e-9 {
			t.Errorf("pct %v: weights sum %v, write share of requests %v", tc.pct, sum, writes/requests)
		}
	}
}

func TestWriteBodies(t *testing.T) {
	u := &vuser{name: "user0001", uids: []string{"N-0001", "TP-0001"},
		topics:    []topic{{id: 7, uid: "TP-0001", members: []int{3, 4}}},
		topicUIDs: map[string]bool{"TP-0001": true}}
	for _, w := range writeOps {
		rq := w.build(u)
		if w.name == "unit_patch" {
			if rq.name != "unit_get" || rq.write || rq.method != "GET" || rq.path != "/api/units/N-0001" {
				t.Fatalf("unit_patch must read the unit first: %+v", rq)
			}
			rq = *rq.next("ok", []byte(`{"unit":{"tags":["area:x","importance:p2","status:open","type:note"]}}`))
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(rq.body), &body); err != nil {
			t.Fatalf("%s: %v %q", w.name, err, rq.body)
		}
		switch w.name {
		case "unit_patch":
			if !rq.write || rq.method != "PATCH" || rq.path != "/api/units/N-0001" || body["title"] == "" || body["status"] == nil ||
				!strings.Contains(rq.body, `"tags":["area:x","load"]`) {
				t.Errorf("unit_patch: %+v", rq)
			}
		case "topic_layout":
			items := body["items"].([]any)
			if rq.path != "/api/units/7/members" || len(items) == 0 {
				t.Errorf("topic_layout: %+v", rq)
			}
		case "topic_design":
			if rq.name != "topic_add" || rq.method != "POST" || !strings.Contains(rq.body, `"add":["N-0001"]`) {
				t.Errorf("topic_design add: %+v", rq)
			}
			if rq.next("ok", []byte(`{"report":{"added":[]}}`)) != nil || len(u.added) != 0 {
				t.Fatal("a skipped unit must not be removed later")
			}
			rq.next("ok", []byte(`{"report":{"added":["N-0001"],"skipped":[]}}`))
			if len(u.added) != 1 {
				t.Fatalf("added = %v", u.added)
			}
		}
	}
	// With an added unit, topic_design eventually removes it again.
	var removed bool
	for range 64 {
		rq := writeOps[3].build(u)
		if rq.name == "topic_remove" {
			if rq.method != "PATCH" || !strings.Contains(rq.body, `"remove":["N-0001"]`) {
				t.Errorf("topic_remove: %+v", rq)
			}
			rq.next("ok", nil)
			removed = true
			break
		}
	}
	if !removed || len(u.added) != 0 {
		t.Errorf("remove: removed=%v added=%v", removed, u.added)
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
	}{
		"ok":             {201, ""},
		"conflict":       {409, `{"error":"stale"}`},
		"db-locked":      {500, `{"error":{"message":"database is locked"}}`},
		"server-timeout": {503, `{"ok":false,"error":{"code":"TIMEOUT"}}`},
		"http-5xx":       {500, `{"error":"boom"}`},
		"http-4xx":       {400, `{"error":"bad"}`},
	}
	for want, c := range cases {
		if got := classify(c.status, []byte(c.body)); got != want {
			t.Errorf("classify(%d, %s) = %s, want %s", c.status, c.body, got, want)
		}
	}
}

func TestStatsDelta(t *testing.T) {
	bounds := []float64{1, 10, 100}
	before := map[string]sqliteClass{"wal": {Writes: 10, Queued: 2, WaitTotalMS: 5, HistLeMS: bounds, Hist: []int64{10, 0, 0, 0}}}
	after := map[string]sqliteClass{"wal": {Writes: 110, Queued: 52, WaitTotalMS: 505, WaitMaxMS: 180, DepthMax: 9,
		HistLeMS: bounds, Hist: []int64{60, 40, 9, 1}}}
	d := statsDelta(before, after)["wal"].(map[string]any)
	if d["writes"].(int64) != 100 || d["queued"].(int64) != 50 || d["wait_mean_ms"].(float64) != 5 {
		t.Errorf("delta: %v", d)
	}
	// 50 in <=1 ms, 40 in <=10 ms, 9 in <=100 ms, 1 above.
	if d["wait_p50_ms"] != 1.0 || d["wait_p95_ms"] != 100.0 || d["wait_p99_ms"] != 100.0 {
		t.Errorf("percentiles: p50 %v p95 %v p99 %v", d["wait_p50_ms"], d["wait_p95_ms"], d["wait_p99_ms"])
	}
	after["wal"] = sqliteClass{WaitMaxMS: 40, HistLeMS: bounds, Hist: []int64{60, 40, 9, 1}}
	if p95 := statsDelta(before, after)["wal"].(map[string]any)["wait_p95_ms"]; p95 != 40.0 {
		t.Errorf("p95 must be capped at the maximum: %v", p95)
	}
	if got := histPercentile(bounds, []int64{0, 0, 0, 0}, .5); got != 0 {
		t.Errorf("empty histogram: %v", got)
	}
}
