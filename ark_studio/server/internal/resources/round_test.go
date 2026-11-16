package resources

import (
	"math"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

// CPython 3.10 round(x) and max(0.0, x) for the GPU memory figures of the
// Resources graph (python -c "round(v)", "max(0.0, v)"): an infinity or NaN
// raises (the graph route answers 500 "Internal error: ..."), and max keeps
// 0.0 for NaN and negatives.
func TestRoundAndMaxLikeCPython(t *testing.T) {
	big300 := "1000000000000000052504760255204420248704468581108159154915854115511802457988908195786371375080447864043704443832883878176942523235360430575644792184786706982848387200926575803737830233794788090059368953234970799945081119038967640880074652742780142494579258788820056842838115669472196386865459400540160"
	cases := []struct {
		x            float64
		round, error string
		max          float64
	}{
		{0, "0", "", 0}, {math.Copysign(0, -1), "0", "", 0}, {0.5, "0", "", 0.5}, {1.5, "2", "", 1.5},
		{2.5, "2", "", 2.5}, {-2.5, "-2", "", 0}, {1e300, big300, "", 1e300}, {-1e300, "-" + big300, "", 0},
		{math.Inf(1), "", "cannot convert float infinity to integer", math.Inf(1)},
		{math.Inf(-1), "", "cannot convert float infinity to integer", 0},
		{math.NaN(), "", "cannot convert float NaN to integer", 0},
		{123456.5, "123456", "", 123456.5},
	}
	for _, tc := range cases {
		got, msg := func() (s, msg string) {
			defer func() {
				if v := recover(); v != nil {
					msg = v.(graphError).err.Error()
				}
			}()
			return intText(round0(tc.x)), ""
		}()
		if got != tc.round || msg != tc.error {
			t.Errorf("round(%v) = %q / %q, want %q / %q", tc.x, got, msg, tc.round, tc.error)
		}
		if m := max0(tc.x); m != tc.max || math.Signbit(m) {
			t.Errorf("max(0.0, %v) = %v, want %v", tc.x, m, tc.max)
		}
	}
}

// GPUs reporting infinite or NaN memory (Go-only fix, go_only.py FIXES
// "resources-graph-nonfinite-gpu-memory"): Python's round() raises and the
// graph answers 500; Go reports those figures as null and keeps the rest.
func TestGPUNonFiniteMemoryIsNull(t *testing.T) {
	cases := []struct {
		name               string
		used, total        any
		wantUsed, wantTot  any
		wantSumTot, wantOt any
	}{
		{"inf total", int64(100), "inf", 100.0, nil, nil, int64(100)},
		{"nan used", "nan", int64(2048), nil, 2048.0, int64(2048), int64(0)},
		{"-inf used", "-inf", int64(10), nil, 10.0, int64(10), int64(0)},
		{"finite", 300.5, int64(1000), 300.5, 1000.0, int64(1000), int64(300)},
	}
	for _, tc := range cases {
		g := &graph{}
		device := pyjson.NewObject("id", "dev:x")
		result := pyjson.NewObject("gpus", []any{pyjson.NewObject("index", int64(0),
			"memory_used_mb", tc.used, "memory_total_mb", tc.total)})
		gpuResources(g, device, result, "x", map[string]*pyjson.Object{}, false)
		occ, _ := device.Value("occupancy").(*pyjson.Object)
		if occ == nil {
			t.Fatalf("%s: no occupancy", tc.name)
		}
		if got := occ.Value("mem_total_mb"); got != tc.wantSumTot {
			t.Errorf("%s: occupancy mem_total_mb %#v, want %#v", tc.name, got, tc.wantSumTot)
		}
		if got := occ.Value("mem_others_mb"); got != tc.wantOt {
			t.Errorf("%s: occupancy mem_others_mb %#v, want %#v", tc.name, got, tc.wantOt)
		}
		var metrics *pyjson.Object
		for _, r := range g.resources {
			if m, ok := r.Value("metrics").(*pyjson.Object); ok && m.Has("mem_used_mb") && m.Has("index") {
				metrics = m
			}
		}
		if metrics == nil {
			t.Fatalf("%s: no GPU resource", tc.name)
		}
		if got := metrics.Value("mem_used_mb"); got != tc.wantUsed {
			t.Errorf("%s: mem_used_mb %#v, want %#v", tc.name, got, tc.wantUsed)
		}
		if got := metrics.Value("mem_total_mb"); got != tc.wantTot {
			t.Errorf("%s: mem_total_mb %#v, want %#v", tc.name, got, tc.wantTot)
		}
	}
}
