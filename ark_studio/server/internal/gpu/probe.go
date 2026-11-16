package gpu

// probe.go is vendor/gpu_server_probe/probe.py: nvidia-smi parsing, the
// system metrics query and the free-GPU ranking.

import (
	"regexp"
	"strconv"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
)

const (
	maxWarnings           = 20
	maxRanked             = 64
	freeMemoryThresholdMB = 512.0
)

func optFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int64:
		return float64(x), true
	}
	return 0, false
}

// OccupancyPct is probe.occupancy_pct.
func OccupancyPct(gpu *pyjson.Object) any {
	total, totalOK := optFloat(gpu.Value("memory_total_mb"))
	used, usedOK := optFloat(gpu.Value("memory_used_mb"))
	util, utilOK := optFloat(gpu.Value("utilization_pct"))
	var values []float64
	if totalOK && total != 0 && usedOK {
		values = append(values, pyMax(0.0, pyMin(100.0, 100.0*used/total)))
	}
	if utilOK {
		values = append(values, pyMax(0.0, pyMin(100.0, util)))
	}
	if len(values) == 0 {
		return nil
	}
	best := values[0]
	for _, v := range values[1:] {
		best = pyMax(best, v)
	}
	return round1(best)
}

// dispatchKey is probe.dispatch_key.
func dispatchKey(gpu *pyjson.Object) (tuple, error) {
	util := 101.0
	if v, ok := optFloat(gpu.Value("utilization_pct")); ok {
		util = v
	}
	free := 0.0
	if v, ok := optFloat(gpu.Value("free_memory_mb")); ok && v != 0 {
		free = v
	}
	index, err := intOf(gpu.Value("index"))
	if err != nil {
		return nil, err
	}
	return tuple{util, -free, index}, nil
}

// intOf is int(x or 0) for an int64 / float64 / *big.Int value.
func intOf(v any) (any, error) {
	switch x := v.(type) {
	case int64:
		return x, nil
	case float64:
		if x == 0 {
			return int64(0), nil
		}
		return floatToInt(x)
	case nil:
		return int64(0), nil
	}
	return v, nil
}

var naTokens = map[string]bool{"n/a": true, "[n/a]": true, "not supported": true, "[not supported]": true,
	"unknown": true, "none": true}

// number is probe._number.
func number(token string) (float64, bool) {
	token = pytext.Strip(token)
	if token == "" || naTokens[pytext.Lower(token)] {
		return 0, false
	}
	v, err := PyFloat(token)
	if err != nil {
		return 0, false
	}
	return v, true
}

// gpuInt is probe._int: nil when missing or fractional.
func gpuInt(token string) (any, error) {
	value, ok := number(token)
	if !ok {
		return nil, nil
	}
	n, err := floatToInt(value)
	if err != nil {
		return nil, err
	}
	if value != num(n) {
		if b, isBig := n.(interface{ String() string }); isBig {
			_ = b
			return n, nil
		}
		return nil, nil
	}
	return n, nil
}

func optRound(v float64, ok bool) any {
	if !ok {
		return nil
	}
	return round1(v)
}

// ParseNvidiaSMI is probe.parse_nvidia_smi.
func ParseNvidiaSMI(text string) ([]any, []any, error) {
	gpus := []any{}
	var warnings []string
	for _, line := range pytext.SplitLines(text, false) {
		raw := pytext.Strip(line)
		if raw == "" {
			continue
		}
		low := pytext.Lower(raw)
		if strings.HasPrefix(low, "index") || strings.HasPrefix(low, "no devices were found") {
			continue
		}
		parts := strings.Split(raw, ",")
		for i := range parts {
			parts[i] = pytext.Strip(parts[i])
		}
		if len(parts) < 5 {
			warnings = append(warnings, "ignored malformed nvidia-smi line: "+pytext.Prefix(raw, 80))
			continue
		}
		index, err := gpuInt(parts[0])
		if err != nil {
			return nil, nil, err
		}
		if index == nil {
			warnings = append(warnings, "ignored line with a non-numeric GPU index: "+pytext.Prefix(raw, 80))
			continue
		}
		name := pytext.Prefix(parts[1], 120)
		if name == "" {
			name = "GPU"
		}
		total, totalOK := number(parts[2])
		used, usedOK := number(parts[3])
		util, utilOK := number(parts[4])
		var umem, temp, power float64
		var umemOK, tempOK, powerOK bool
		if len(parts) > 5 {
			umem, umemOK = number(parts[5])
		}
		if len(parts) > 6 {
			temp, tempOK = number(parts[6])
		}
		if len(parts) > 7 {
			power, powerOK = number(parts[7])
		}
		var free float64
		freeOK := totalOK && usedOK
		if freeOK {
			free = pyMax(0.0, total-used)
		}
		var score any
		if freeOK && utilOK {
			score = round1(free * (1.0 - pyMin(100.0, pyMax(0.0, util))/100.0))
		}
		if !totalOK {
			warnings = append(warnings, "GPU "+pyjson.Str(index)+" did not report total memory")
		}
		gpu := pyjson.NewObject("index", index, "name", name,
			"memory_total_mb", optRound(total, totalOK), "memory_used_mb", optRound(used, usedOK),
			"free_memory_mb", optRound(free, freeOK), "utilization_pct", optRound(util, utilOK),
			"utilization_memory_pct", optRound(umem, umemOK), "temperature_c", optRound(temp, tempOK),
			"power_w", optRound(power, powerOK), "score", score)
		gpu.Set("occupancy_pct", OccupancyPct(gpu))
		gpus = append(gpus, gpu)
	}
	if len(warnings) > maxWarnings {
		extra := len(warnings) - maxWarnings
		warnings = append(warnings[:maxWarnings], "… "+strconv.Itoa(extra)+" more warnings")
	}
	out := make([]any, len(warnings))
	for i, w := range warnings {
		out[i] = w
	}
	return gpus, out, nil
}

var cpuIdleRe = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)[` + pytext.SpaceClass + `]*id`)

func pyIntStr(s string) (int64, error) {
	n, err := pytext.Int(s)
	if err != nil {
		return 0, &ValueError{"invalid literal for int() with base 10: " + pyjson.Repr(s)}
	}
	return n, nil
}

type pmonKey struct{ gpu, pid int64 }

// ParseSystem is probe.parse_system.
func ParseSystem(text string) (*pyjson.Object, error) {
	out := pyjson.NewObject("load1", nil, "load5", nil, "load15", nil, "cores", nil,
		"cpu_load_pct", nil, "cpu_percent", nil,
		"memory_total_mb", nil, "memory_used_mb", nil, "memory_free_mb", nil, "memory_available_mb", nil,
		"processes", []any{}, "process_util_available", false, "error", nil)
	err := parseSystemInto(out, text)
	if err != nil {
		if ve, ok := err.(*ValueError); ok {
			out.Set("error", "could not parse system metrics: "+ve.Msg)
			return out, nil
		}
		return nil, err
	}
	return out, nil
}

func parseSystemInto(out *pyjson.Object, text string) error {
	section := ""
	buses := map[string]int64{}
	type procRow struct {
		pid    int64
		bus    string
		memory any
	}
	var rows []procRow
	envJobs := map[string]string{}
	pmon := map[pmonKey]float64{}
	for _, line := range pytext.SplitLines(text, false) {
		raw := pytext.RStrip(line)
		if strings.HasPrefix(raw, "__ARK_") {
			section = pytext.Lower(strings.ReplaceAll(strings.Trim(raw, "_"), "ARK_", ""))
			continue
		}
		parts := strings.FieldsFunc(raw, pytext.IsSpace)
		switch {
		case section == "load" && len(parts) >= 3:
			var vals [3]float64
			for i := 0; i < 3; i++ {
				v, err := PyFloat(parts[i])
				if err != nil {
					return err
				}
				vals[i] = v
			}
			out.Set("load1", vals[0]).Set("load5", vals[1]).Set("load15", vals[2])
		case section == "nproc" && len(parts) > 0:
			n, err := pyIntStr(parts[0])
			if err != nil {
				return err
			}
			out.Set("cores", n)
		case section == "mem" && len(parts) >= 4:
			for i, key := range []string{"memory_total_mb", "memory_used_mb", "memory_free_mb", "memory_available_mb"} {
				n, err := pyIntStr(parts[i])
				if err != nil {
					return err
				}
				out.Set(key, n)
			}
		case section == "cpu" && strings.Contains(raw, "id"):
			if m := cpuIdleRe.FindStringSubmatch(raw); m != nil {
				idle, _ := strconv.ParseFloat(m[1], 64)
				out.Set("cpu_percent", round1(pyMax(0.0, 100.0-idle)))
			}
		case section == "bus":
			fields := splitStrip(raw)
			if len(fields) >= 2 {
				if n, err := pytext.Int(fields[0]); err == nil {
					buses[fields[1]] = n
				}
			}
		case section == "proc":
			fields := splitStrip(raw)
			if len(fields) >= 3 {
				pid, err := pytext.Int(fields[0])
				if err != nil {
					continue
				}
				var memory any
				if f, err := PyFloat(fields[2]); err == nil {
					m, err := floatToInt(f)
					if err != nil {
						if _, isValue := err.(*ValueError); !isValue {
							return err
						}
					} else {
						memory = m
					}
				}
				rows = append(rows, procRow{pid, fields[1], memory})
			}
		case section == "pmon":
			if len(parts) == 0 || strings.HasPrefix(parts[0], "#") || len(parts) < 4 {
				continue
			}
			g, err1 := pytext.Int(parts[0])
			if err1 != nil {
				continue
			}
			pid, err2 := pytext.Int(parts[1])
			if err2 != nil {
				continue
			}
			out.Set("process_util_available", true)
			if parts[3] != "-" {
				if v, err := PyFloat(parts[3]); err == nil {
					pmon[pmonKey{g, pid}] = pyMax(0.0, pyMin(100.0, v))
				}
			}
		case section == "env" && len(parts) >= 2:
			envJobs[parts[0]] = parts[1]
		}
	}
	load1, ok := optFloat(out.Value("load1"))
	if cores, isInt := out.Value("cores").(int64); ok && isInt && cores != 0 {
		out.Set("cpu_load_pct", round1(pyMin(100.0, load1/float64(cores)*100.0)))
	}
	if out.Value("cpu_percent") == nil {
		out.Set("cpu_percent", out.Value("cpu_load_pct"))
	}
	processes := []any{}
	utilAvailable := out.Value("process_util_available") == true
	for _, row := range rows {
		index, known := buses[row.bus]
		if !known {
			continue
		}
		proc := pyjson.NewObject("pid", row.pid, "memory_mb", row.memory, "gpu_index", index,
			"job_id", envJobs[strconv.FormatInt(row.pid, 10)])
		if utilAvailable {
			proc.Set("sm_pct", pmon[pmonKey{index, row.pid}])
		} else {
			proc.Set("sm_pct", nil)
		}
		processes = append(processes, proc)
	}
	out.Set("processes", processes)
	return nil
}

func splitStrip(raw string) []string {
	fields := strings.Split(raw, ",")
	for i := range fields {
		fields[i] = pytext.Strip(fields[i])
	}
	return fields
}

// ProbeServer is probe.probe_server.  Go-only: with a system runner, the
// server's storage (the df section, storage.go) is added as "storage" when it
// parses; dataPaths are the remote data folders whose mounts are marked.
func ProbeServer(server *pyjson.Object, runner Runner, timeout int, systemRunner Runner, dataPaths ...string) (*pyjson.Object, error) {
	limit := timeoutOf(server, timeout)
	public := PublicServer(server)
	payload := pyjson.NewObject("server", public.Value("name"), "host", public.Value("host"),
		"port", public.Value("port"), "user", public.Value("user"), "ok", false, "gpus", []any{},
		"warnings", []any{}, "error", nil, "duration_ms", int64(0), "command", ProbeCommandText())
	result := runner(server, limit)
	payload.Set("duration_ms", result.DurationMS)
	if !result.OK() {
		kind, message := result.ErrorKind, result.Message
		if kind == "" {
			kind = "ssh_error"
		}
		if message == "" {
			message = "the remote probe failed"
		}
		payload.Set("error", pyjson.NewObject("kind", kind, "message", message))
		return payload, nil
	}
	metrics := result.Stdout
	if i := strings.Index(metrics, "__ARK_GPU__\n"); i >= 0 {
		system, err := ParseSystem(metrics[:i])
		if err != nil {
			return nil, err
		}
		payload.Set("system", system)
		metrics = metrics[i+len("__ARK_GPU__\n"):]
	}
	gpus, warnings, err := ParseNvidiaSMI(metrics)
	if err != nil {
		return nil, err
	}
	payload.Set("gpus", gpus).Set("warnings", warnings).Set("ok", true)
	if len(gpus) == 0 {
		payload.Set("warnings", append(warnings, "nvidia-smi returned no GPU rows"))
	}
	if systemRunner != nil {
		sys := systemRunner(server, limit)
		if sys.OK() {
			system, err := ParseSystem(sys.Stdout)
			if err != nil {
				payload.Set("system", pyjson.NewObject("error", pytext.Prefix(errTypeName(err)+": "+err.Error(), 300)))
			} else {
				payload.Set("system", system)
			}
			if storage := ParseStorage(sys.Stdout, dataPaths); len(storage) > 0 {
				payload.Set("storage", storage)
			}
		} else {
			msg := sys.Message
			if msg == "" {
				msg = "system query failed"
			}
			payload.Set("system", pyjson.NewObject("error", msg))
		}
	}
	return payload, nil
}

func errTypeName(err error) string {
	if _, ok := err.(*ValueError); ok {
		return "ValueError"
	}
	return "OverflowError"
}

// RankGPUs is probe.rank_gpus.
func RankGPUs(results []*pyjson.Object) ([]any, error) {
	type entry struct {
		obj *pyjson.Object
		key tuple
	}
	var ranked []entry
	for _, result := range results {
		if !pyjson.Truthy(result.Value("ok")) {
			continue
		}
		gpus, _ := result.Value("gpus").([]any)
		for _, item := range gpus {
			gpu := item.(*pyjson.Object)
			if gpu.Value("score") == nil || gpu.Value("free_memory_mb") == nil {
				continue
			}
			free, _ := optFloat(gpu.Value("free_memory_mb"))
			if free < freeMemoryThresholdMB {
				continue
			}
			util, utilOK := optFloat(gpu.Value("utilization_pct"))
			if utilOK && util >= 100 {
				continue
			}
			key, err := dispatchKey(gpu)
			if err != nil {
				return nil, err
			}
			obj := pyjson.NewObject("server", result.Value("server"), "host", result.Value("host"),
				"gpu_index", gpu.Value("index"), "gpu_name", gpu.Value("name"),
				"free_memory_mb", gpu.Value("free_memory_mb"), "utilization_pct", gpu.Value("utilization_pct"),
				"score", gpu.Value("score"), "occupancy_pct", OccupancyPct(gpu))
			ranked = append(ranked, entry{obj, append(key, pyjson.Str(result.Value("server")))})
		}
	}
	// sort key (item["_key"], str(item["server"])): a 2-tuple whose first item
	// is itself a tuple; flattening keeps the same order.
	pySort(ranked, func(e entry) tuple { return e.key })
	out := []any{}
	for i, e := range ranked {
		e.obj.Set("rank", int64(i+1))
		if i < maxRanked {
			out = append(out, e.obj)
		}
	}
	return out, nil
}
