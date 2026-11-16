// dispatch.go ports vendor/gpu_server_probe/dispatch.py: build_request and
// its validators (_gpus, _work_dir, _command, _env), JOB_RE, plus Python's
// shlex.quote / shlex.join.

package gpu

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strings"
	"unicode"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pytext"
)

// Limits from dispatch.py.
const (
	MaxGPUs           = 8
	MaxGPUIndex       = 15
	MaxCommandChars   = 16384
	MaxTimeoutMinutes = 1440
	MaxEnvVars        = 16
)

// JobRE is JOB_RE (use MatchString for re.match semantics).
var JobRE = mustPattern(`^[A-Za-z0-9._-]{1,64}$`)

var envNameRE = mustPattern(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// DispatchError is dispatch.DispatchError.
type DispatchError struct{ Msg string }

func (e *DispatchError) Error() string { return e.Msg }

func dispErr(format string, a ...any) error { return &DispatchError{Msg: fmt.Sprintf(format, a...)} }

// OverflowError is Python's OverflowError from int(float('inf')); dispatch.py
// does not catch it, so it is not a DispatchError.
type OverflowError struct{ Msg string }

func (e *OverflowError) Error() string { return e.Msg }

var errIntConversion = errors.New("int() conversion failed") // TypeError or ValueError

// pyIntStr is int(s) for a str (base 10): surrounding whitespace, a sign,
// Unicode decimal digits and single underscores between digits.
func cfgPyIntStr(s string) (any, bool) {
	t := pytext.Strip(s)
	sign := ""
	if strings.HasPrefix(t, "-") || strings.HasPrefix(t, "+") {
		sign, t = t[:1], t[1:]
	}
	var b strings.Builder
	b.WriteString(sign)
	prevDigit, seen := false, false
	for _, r := range t {
		if r == '_' && prevDigit {
			prevDigit = false
			continue
		}
		if r < 0x80 {
			if r < '0' || r > '9' {
				return nil, false
			}
			b.WriteRune(r)
		} else {
			if !cfgIsDecimal(r) {
				return nil, false
			}
			b.WriteByte(byte('0' + cfgDigitValue(r)))
		}
		prevDigit, seen = true, true
	}
	if !seen || !prevDigit {
		return nil, false
	}
	n, ok := new(big.Int).SetString(b.String(), 10)
	if !ok {
		return nil, false
	}
	return cfgIntValue(n), true
}

func cfgIsDecimal(r rune) bool { return unicode.Is(unicode.Nd, r) }

// pyInt is int(v) for a decoded JSON value (bool is handled by callers).
func cfgPyInt(v any) (any, error) {
	switch x := v.(type) {
	case int:
		return int64(x), nil
	case int64:
		return x, nil
	case *big.Int:
		return cfgIntValue(x), nil
	case float64:
		if math.IsNaN(x) {
			return nil, errIntConversion
		}
		if math.IsInf(x, 0) {
			return nil, &OverflowError{"cannot convert float infinity to integer"}
		}
		t := math.Trunc(x)
		if t >= -9.223372036854775808e18 && t < 9.223372036854775808e18 {
			return int64(t), nil
		}
		n, _ := new(big.Float).SetFloat64(t).Int(nil)
		return n, nil
	case string:
		if n, ok := cfgPyIntStr(x); ok {
			return n, nil
		}
	}
	return nil, errIntConversion
}

func cfgIntText(v any) string {
	if n, ok := v.(int64); ok {
		return fmt.Sprint(n)
	}
	return v.(*big.Int).String()
}

func cfgIntIn(v any, low, high int64) (int64, bool) {
	n, ok := v.(int64)
	return n, ok && n >= low && n <= high
}

func cfgServerName(server any) string {
	o, _ := server.(*pyjson.Object)
	return pyjson.Str(o.Value("name"))
}

func cfgServerByName(servers []any, name string) (*pyjson.Object, error) {
	for _, s := range servers {
		o, _ := s.(*pyjson.Object)
		if n, ok := o.Value("name").(string); ok && n == name {
			return o, nil
		}
	}
	names := make([]string, len(servers))
	for i, s := range servers {
		names[i] = cfgServerName(s)
	}
	known := strings.Join(names, ", ")
	if known == "" {
		known = "none configured"
	}
	return nil, dispErr("unknown server %s; configured servers: %s", pyjson.Repr(name), known)
}

// DispatchGPUs is _gpus(raw).
func DispatchGPUs(raw any) ([]int64, error) {
	if raw == nil || raw == "" {
		return []int64{}, nil
	}
	var tokens []any
	switch x := raw.(type) {
	case string:
		for _, t := range strings.FieldsFunc(pytext.Strip(x), func(r rune) bool { return r == ',' || pytext.IsSpace(r) }) {
			tokens = append(tokens, t)
		}
	case []any:
		tokens = x
	default:
		return nil, dispErr("gpus must be a comma-separated string or a list of indices")
	}
	indices := []int64{}
	for _, token := range tokens {
		if _, ok := token.(bool); ok {
			return nil, dispErr("gpus must contain integer GPU indices")
		}
		v, err := cfgPyInt(token)
		if err != nil {
			if err == errIntConversion {
				return nil, dispErr("invalid GPU index %s", pyjson.Repr(token))
			}
			return nil, err
		}
		index, ok := cfgIntIn(v, 0, MaxGPUIndex)
		if !ok {
			return nil, dispErr("GPU index %s must be between 0 and %d", cfgIntText(v), MaxGPUIndex)
		}
		for _, seen := range indices {
			if seen == index {
				return nil, dispErr("duplicate GPU index %d", index)
			}
		}
		indices = append(indices, index)
	}
	if len(indices) > MaxGPUs {
		return nil, dispErr("at most %d GPUs can be requested", MaxGPUs)
	}
	return indices, nil
}

// DispatchWorkDir is _work_dir(raw).
func DispatchWorkDir(raw any) (string, error) {
	if raw == nil || raw == "" {
		return "", nil
	}
	s, ok := raw.(string)
	if !ok {
		return "", dispErr("work_dir must be a string")
	}
	path := pytext.Strip(s)
	if pytext.Len(path) > 240 {
		return "", dispErr("work_dir must be at most 240 characters")
	}
	if !strings.HasPrefix(path, "/") {
		return "", dispErr("work_dir must be an absolute POSIX path")
	}
	if cfgContains(strings.Split(path, "/"), "..") {
		return "", dispErr("work_dir must not contain '..'")
	}
	if cfgHasControl(path) {
		return "", dispErr("work_dir must not contain control characters")
	}
	return path, nil
}

// DispatchCommand is _command(raw).
func DispatchCommand(raw any) (string, error) {
	s, ok := raw.(string)
	if !ok {
		return "", dispErr("command is required")
	}
	command := pytext.Strip(s)
	if command == "" {
		return "", dispErr("command is required")
	}
	if pytext.Len(command) > MaxCommandChars {
		return "", dispErr("command must be at most %d characters", MaxCommandChars)
	}
	if strings.ContainsRune(command, 0) {
		return "", dispErr("command must not contain NUL bytes")
	}
	for _, r := range command {
		if r < 32 && r != '\n' && r != '\t' {
			return "", dispErr("command must not contain control characters")
		}
	}
	return command, nil
}

// DispatchEnv is _env(raw).
func DispatchEnv(raw any) (*pyjson.Object, error) {
	if raw == nil || raw == "" {
		return pyjson.NewObject(), nil
	}
	o, ok := raw.(*pyjson.Object)
	if !ok || o == nil {
		return nil, dispErr("env must be an object")
	}
	if o.Len() > MaxEnvVars {
		return nil, dispErr("env can have at most %d entries", MaxEnvVars)
	}
	env := pyjson.NewObject()
	for _, key := range o.Keys() {
		value := o.Value(key)
		if !envNameRE.MatchString(key) {
			return nil, dispErr("invalid environment variable name %s", pyjson.Repr(key))
		}
		var s string
		switch x := value.(type) {
		case bool:
			s = "0"
			if x {
				s = "1"
			}
		case string:
			s = x
		default:
			if !cfgIsNumber(value) {
				return nil, dispErr("env value for %s must be a string", pyjson.Repr(key))
			}
			s = pyjson.Str(value)
		}
		if pytext.Len(s) > 200 || strings.ContainsRune(s, 0) {
			return nil, dispErr("env value for %s is too long or contains NUL", pyjson.Repr(key))
		}
		env.Set(key, s)
	}
	return env, nil
}

func cfgShlexSafe(b byte) bool {
	return cfgIsAlnumASCII(b) || b == '_' || strings.IndexByte("@%+=:,./-", b) >= 0
}

// ShlexQuote is shlex.quote(s).
func ShlexQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for i := 0; i < len(s); i++ {
		if !cfgShlexSafe(s[i]) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// ShlexJoin is shlex.join(args).
func ShlexJoin(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = ShlexQuote(a)
	}
	return strings.Join(parts, " ")
}

// BuildRequest is build_request(body, servers); now is the generated_at
// value (datetime.now(timezone.utc).isoformat(timespec="seconds")).  Errors
// are *DispatchError, except int(inf) which is an *OverflowError as in
// CPython.
func BuildRequest(body any, servers []any, now string) (*pyjson.Object, error) {
	b, ok := body.(*pyjson.Object)
	if !ok || b == nil {
		return nil, dispErr("dispatch request must be an object")
	}
	if pyjson.Truthy(b.Value("execute")) || pyjson.Truthy(b.Value("launch")) {
		return nil, dispErr("ARK prepares dispatch requests only; it never executes remote jobs. " +
			"Use the gpu_server_probe CLI or your scheduler with the returned request.")
	}
	if len(servers) == 0 {
		return nil, dispErr("no GPU servers are configured")
	}
	name := ""
	if v := b.Value("server"); pyjson.Truthy(v) {
		name = pytext.Strip(pyjson.Str(v))
	}
	if name == "" {
		if len(servers) == 1 {
			name = cfgServerName(servers[0])
		} else {
			names := make([]string, len(servers))
			for i, s := range servers {
				names[i] = cfgServerName(s)
			}
			return nil, dispErr("server is required; configured servers: %s", strings.Join(names, ", "))
		}
	}
	server, err := cfgServerByName(servers, name)
	if err != nil {
		return nil, err
	}
	if !pyjson.Truthy(cfgGet(server, "enabled", true)) {
		return nil, dispErr("server %s is disabled in the server list", pyjson.Repr(name))
	}
	gpusRaw, ok := b.Get("gpus")
	if !ok {
		gpusRaw = b.Value("gpu_indices")
	}
	gpus, err := DispatchGPUs(gpusRaw)
	if err != nil {
		return nil, err
	}
	timeoutRaw := cfgGet(b, "timeout_minutes", int64(60))
	if _, ok := timeoutRaw.(bool); ok {
		return nil, dispErr("timeout_minutes must be an integer")
	}
	tv, err := cfgPyInt(timeoutRaw)
	if err != nil {
		if err == errIntConversion {
			return nil, dispErr("timeout_minutes must be an integer")
		}
		return nil, err
	}
	timeout, ok := cfgIntIn(tv, 1, MaxTimeoutMinutes)
	if !ok {
		return nil, dispErr("timeout_minutes must be between 1 and %d", MaxTimeoutMinutes)
	}
	label := func(key, def string) string {
		v := b.Value(key)
		if !pyjson.Truthy(v) {
			return def
		}
		return pytext.Strip(pyjson.Str(v))
	}
	job := label("job", "ark-job")
	if !JobRE.MatchString(job) {
		return nil, dispErr("job must be a short label using letters, digits, dot, dash or underscore")
	}
	project := label("project", "ark")
	if !JobRE.MatchString(project) {
		return nil, dispErr("project must be a short label using letters, digits, dot, dash or underscore")
	}
	workDir, err := DispatchWorkDir(b.Value("work_dir"))
	if err != nil {
		return nil, err
	}
	command, err := DispatchCommand(b.Value("command"))
	if err != nil {
		return nil, err
	}
	env, err := DispatchEnv(b.Value("env"))
	if err != nil {
		return nil, err
	}

	gpuText := make([]string, len(gpus))
	gpuList := make([]any, len(gpus))
	for i, g := range gpus {
		gpuText[i] = fmt.Sprint(g)
		gpuList[i] = g
	}
	cudaVisible := strings.Join(gpuText, ",")
	var pieces []string
	if workDir != "" {
		pieces = append(pieces, "cd "+ShlexQuote(workDir))
	}
	keys := append([]string(nil), env.Keys()...)
	sort.Strings(keys)
	envParts := make([]string, len(keys))
	for i, k := range keys {
		envParts[i] = k + "=" + ShlexQuote(env.Value(k).(string))
	}
	var prefixParts []string
	if p := strings.Join(envParts, " "); p != "" {
		prefixParts = append(prefixParts, p)
	}
	if len(gpus) > 0 {
		prefixParts = append(prefixParts, "CUDA_VISIBLE_DEVICES="+cudaVisible)
	}
	commandPrefix := ""
	if prefix := strings.Join(prefixParts, " "); prefix != "" {
		commandPrefix = prefix + " "
	}
	pieces = append(pieces, fmt.Sprintf("%stimeout %dm bash -lc %s", commandPrefix, timeout, ShlexQuote(command)))
	remoteCommand := strings.Join(pieces, " && ")

	transport := pyjson.NewObject(
		"host", server.Value("host"),
		"port", server.Value("port"),
		"user", server.Value("user"),
		"auth", "local SSH agent or ~/.ssh/config (no secrets stored by ARK)",
	)
	target := pyjson.Str(server.Value("host"))
	if pyjson.Truthy(server.Value("user")) {
		target = pyjson.Str(server.Value("user")) + "@" + target
	}
	argv := []string{"ssh", "-p", pyjson.Str(server.Value("port")), target, "--", "bash", "-lc", remoteCommand}

	var workDirValue any
	if workDir != "" {
		workDirValue = workDir
	}
	return pyjson.NewObject(
		"format", "ark-gpu-dispatch-request",
		"version", int64(1),
		"dry_run", true,
		"executed", false,
		"generated_at", now,
		"server", PublicServer(server),
		"transport", transport,
		"request", pyjson.NewObject(
			"job", job,
			"project", project,
			"server", server.Value("name"),
			"gpus", gpuList,
			"cuda_visible_devices", cudaVisible,
			"timeout_minutes", timeout,
			"work_dir", workDirValue,
			"env", env,
			"command", command,
		),
		"remote_command_preview", remoteCommand,
		"ssh_preview", ShlexJoin(argv),
		"notes", []any{
			"Dry run only: ARK never opens the remote shell or launches this job.",
			"Review the server, GPU indices and command before handing it to the " +
				"gpu_server_probe CLI or your scheduler.",
			"Authentication uses your local SSH agent or ~/.ssh/config; ARK stores " +
				"no password, key or token.",
			"env values are echoed back in this request; avoid putting secrets there.",
		},
		"next_steps", []any{
			"Review the request and the illustrative SSH command.",
			"Run the upstream gpu_server_probe launcher (or your scheduler) yourself " +
				"with this job descriptor.",
		},
	), nil
}
