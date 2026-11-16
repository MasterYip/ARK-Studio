// Package sessionwatch is a port of arkstudio/session_watch.py: upload an
// agent's session JSONL to ARK at an interval (opt-in watcher).
//
// Behaviour (requests on the wire, log lines, exit codes) mirrors the Python
// module run under CPython 3.10 without a zstd decoder: DSH ".jsonl.zstd"
// files are found by discovery but reading them fails exactly as Python's
// read_source does when neither compression.zstd nor zstandard is present.
package sessionwatch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"os/signal"
	"strconv"
	"time"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pytext"
	"arkstudio/go/internal/pyurl"
)

const (
	DefaultChunk  = 1024 * 1024
	searchDays    = 14
	searchLimit   = 400
	ExitDisabled  = 3
	ExitNotFound  = 4
	exitTokenless = 2
)

// Options are the parsed command-line arguments (build_parser's namespace).
type Options struct {
	ID, File, Agent, Server, Project, TokenEnv, Title string
	Interval                                          float64
	Once                                              bool
}

// DefaultServer is build_parser's --server default.
func DefaultServer() string {
	if v := os.Getenv("ARK_URL"); v != "" {
		return v
	}
	return "http://127.0.0.1:8890"
}

// DefaultProject is build_parser's --project default.
func DefaultProject() string { return os.Getenv("ARK_PROJECT") }

// Stderr receives the traceback-style line when the Python module would have
// died with an uncaught exception (exit status 1).
var Stderr io.Writer = os.Stderr

// sleep is time.sleep (replaceable in tests); it returns false when ctx ends.
var sleep = func(ctx context.Context, seconds float64) bool {
	t := time.NewTimer(time.Duration(seconds * float64(time.Second)))
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

var now = time.Now

// Run is session_watch.main() minus argument parsing: run() with Ctrl+C
// (KeyboardInterrupt) returning 0.  env nil means the process environment;
// home "" means Path.home(); log is print (the caller appends "\n").
func Run(o Options, env func(string) (string, bool), home string, log func(string)) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return RunContext(ctx, o, env, home, log)
}

// RunContext is Run with an explicit cancellation context: when ctx is done
// the watcher stops wherever it is and returns 0 (KeyboardInterrupt).
func RunContext(ctx context.Context, o Options, env func(string) (string, bool), home string, log func(string)) (code int) {
	if env == nil {
		env = os.LookupEnv
	}
	defer func() {
		if r := recover(); r != nil {
			switch x := r.(type) {
			case interrupted:
				code = 0
			case crash:
				fmt.Fprintf(Stderr, "Traceback (most recent call last):\n  File \"session_watch.py\"\n%s: %s\n", x.typ, x.msg)
				code = 1
			default:
				panic(r)
			}
		}
	}()
	return run(ctx, o, env, home, log)
}

func envGet(env func(string) (string, bool), key string) string {
	v, _ := env(key)
	return v
}

func run(ctx context.Context, o Options, env func(string) (string, bool), home string, log func(string)) int {
	token := pytext.Strip(envGet(env, o.TokenEnv))
	if token == "" {
		log(fmt.Sprintf("set %s to your ARK agent token first (it is never read from the command line)", o.TokenEnv))
		return exitTokenless
	}
	sessionID := pytext.Lower(pytext.Strip(o.ID))
	agent := o.Agent
	var path pyPath
	if o.File != "" {
		path = newPath(expandUser(o.File))
	} else {
		name, found, ok := findSessionFile(sessionID, agent, home, env)
		if !ok {
			log(fmt.Sprintf("no recent session file mentions %s; pass --file", sessionID))
			return ExitNotFound
		}
		if agent == "" {
			agent = name
		}
		path = found
		log(fmt.Sprintf("watching %s (%s)", path, agent))
	}
	c := newClient(ctx, o.Server, token, o.Project)
	interval := o.Interval
	if !(interval > 10.0) {
		interval = 10.0
	}
	meta := pyjson.NewObject("title", o.Title)
	return watch(ctx, c, sessionID, path, interval, agent, o.Once, meta, log)
}

// ------------------------------------------------------------ discovery

type root struct {
	name string
	path pyPath
}

func agentRoots(agent, home string, env func(string) (string, bool)) []root {
	var h pyPath
	if home != "" {
		h = newPath(home)
	} else {
		var ok bool
		if h, ok = homePath(); !ok {
			throw("RuntimeError", "Could not determine home directory.")
		}
	}
	var roots []root
	if agent == "" || agent == "claude" {
		base := h.join(".claude")
		if v := envGet(env, "CLAUDE_CONFIG_DIR"); v != "" {
			base = newPath(v)
		}
		roots = append(roots, root{"claude", base.join("projects")})
	}
	if agent == "" || agent == "codex" {
		base := h.join(".codex")
		if v := envGet(env, "CODEX_HOME"); v != "" {
			base = newPath(v)
		}
		roots = append(roots, root{"codex", base.join("sessions")})
	}
	if agent == "" || agent == "dsh" {
		roots = append(roots, root{"dsh", h.join(".dsh", "sessions")})
	}
	return roots
}

func findSessionFile(sessionID, agent, home string, env func(string) (string, bool)) (string, pyPath, bool) {
	needle := []byte(sessionID)
	var best *candidate
	bestName := ""
	for _, r := range agentRoots(agent, home, env) {
		for _, cand := range candidates(r.path, now()) {
			data, err := readSource(cand.path)
			if err != nil || !bytes.Contains(data, needle) {
				continue
			}
			if fi, err := os.Stat(cand.path.String()); err == nil {
				m := stMtime(fi)
				if best == nil || m > best.mtime {
					best = &candidate{cand.path, m}
					bestName = r.name
				}
			}
			break // candidates are newest first per root
		}
	}
	if best == nil {
		return "", pyPath{}, false
	}
	return bestName, best.path, true
}

const zstdMissing = "this session file is zstd-compressed; install the zstandard package or use Python 3.14+"

// readSource is read_source(path) with no zstd decoder installed.
func readSource(p pyPath) ([]byte, error) {
	name := p.String()
	data, err := readBytes(name)
	if err != nil {
		return nil, err
	}
	if p.suffix() != ".zstd" {
		return data, nil
	}
	return nil, &osError{zstdMissing} // RuntimeError: same handling as OSError in watch()
}

// readBytes is Path.read_bytes with CPython's OSError text.
func readBytes(name string) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, fileError(err, name)
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.IsDir() {
		return nil, &osError{fmt.Sprintf("[Errno %d] %s: %s", dirReadErrno, strerror(dirReadErrno), pyjson.Repr(name))}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fileError(err, "")
	}
	return data, nil
}

func fileError(err error, name string) error {
	errno, ok := fileErrno(err)
	if !ok {
		return &osError{err.Error()}
	}
	s := fmt.Sprintf("[Errno %d] %s", errno, strerror(errno))
	if name != "" {
		s += ": " + pyjson.Repr(name)
	}
	return &osError{s}
}

// ------------------------------------------------------------ Python ints

const (
	maxInt = math.MaxInt64
	minInt = math.MinInt64
)

// pyInt is int(v) for a decoded JSON value (saturating beyond int64).
func pyInt(v any) int64 {
	switch x := v.(type) {
	case bool:
		if x {
			return 1
		}
		return 0
	case int64:
		return x
	case int:
		return int64(x)
	case *big.Int:
		if x.IsInt64() {
			return x.Int64()
		}
		if x.Sign() > 0 {
			return maxInt
		}
		return minInt
	case float64:
		if math.IsNaN(x) {
			throw("ValueError", "cannot convert float NaN to integer")
		}
		if math.IsInf(x, 0) {
			throw("OverflowError", "cannot convert float infinity to integer")
		}
		t := math.Trunc(x)
		if t >= 9.223372036854775807e18 {
			return maxInt
		}
		if t <= -9.223372036854775808e18 {
			return minInt
		}
		return int64(t)
	case string:
		n, err := pytext.Int(x)
		if err != nil {
			var ie *pytext.IntError
			if errors.As(err, &ie) {
				throw("ValueError", err.Error())
			}
			if pytext.Strip(x)[0] == '-' {
				return minInt
			}
			return maxInt
		}
		return n
	}
	throw("TypeError", "int() argument must be a string, a bytes-like object or a real number, not '"+pyTypeName(v)+"'")
	return 0
}

func or(v any, fallback any) any {
	if pyjson.Truthy(v) {
		return v
	}
	return fallback
}

func addSat(a, b int64) int64 {
	s := a + b
	if a > 0 && b > 0 && s < 0 {
		return maxInt
	}
	if a < 0 && b < 0 && s >= 0 {
		return minInt
	}
	return s
}

// Python index normalisation for bytes.find/rfind/slicing.
func clampIndex(i, n int64) int64 {
	if i < 0 {
		i += n
		if i < 0 {
			i = 0
		}
	}
	if i > n {
		i = n
	}
	return i
}

func pyFind(data []byte, start int64) int64 {
	n := int64(len(data))
	s := clampIndex(start, n)
	if i := bytes.IndexByte(data[s:], '\n'); i >= 0 {
		return s + int64(i)
	}
	return -1
}

func pyRfind(data []byte, start, end int64) int64 {
	n := int64(len(data))
	s, e := clampIndex(start, n), clampIndex(end, n)
	if s > e {
		return -1
	}
	if i := bytes.LastIndexByte(data[s:e], '\n'); i >= 0 {
		return s + int64(i)
	}
	return -1
}

func pySlice(data []byte, a, b int64) []byte {
	n := int64(len(data))
	s, e := clampIndex(a, n), clampIndex(b, n)
	if s >= e {
		return nil
	}
	return data[s:e]
}

// ------------------------------------------------------------ upload

type chunk struct {
	offset, length int64
	content        []byte
}

// splitChunks is split_chunks(data, start, limit); yield returning false
// stops the generator.
func splitChunks(data []byte, start, limit int64, yield func(chunk) bool) {
	end := pyRfind(data, 0, int64(len(data))) + 1
	offset := start
	for offset < end {
		stop := min(end, addSat(offset, limit))
		if stop < end {
			cut := pyRfind(data, offset, stop)
			if cut >= offset {
				stop = cut + 1
			} else {
				stop = pyFind(data, offset) + 1
				marker := append(pyjson.Dumps(pyjson.NewObject("type", "ark_truncated", "bytes", stop-offset-1)), '\n')
				if !yield(chunk{offset, stop - offset, marker}) {
					return
				}
				offset = stop
				continue
			}
		}
		if !yield(chunk{offset, stop - offset, pySlice(data, offset, stop)}) {
			return
		}
		offset = stop
	}
}

type syncResult struct{ sent, received int64 }

// syncOnce is sync_once(client, session_id, path, agent=..., meta=..., log=...).
func syncOnce(c *client, sessionID string, path pyPath, agent string, chunkBytes int64,
	meta *pyjson.Object, log func(string)) (syncResult, error) {
	stateV, err := c.status(sessionID)
	if err != nil {
		return syncResult{}, err
	}
	state := asDict(stateV)
	if !pyjson.Truthy(state.Value("uploads_enabled")) {
		return syncResult{}, &UploadError{403, "SESSION_UPLOADS_DISABLED", "this ARK project does not accept session uploads"}
	}
	limits := asDict(or(state.Value("limits"), pyjson.NewObject()))
	limit := min(chunkBytes, pyInt(or(limits.Value("chunk_bytes"), chunkBytes)))
	received := pyInt(or(state.Value("received_bytes"), int64(0)))
	data, err := readSource(path)
	if err != nil {
		return syncResult{}, err
	}
	mode := "append"
	if int64(len(data)) < received {
		log(fmt.Sprintf("%s shrank (%d < %d bytes); re-uploading it", path, len(data), received))
		received = 0
		mode = "replace"
	}
	var sent int64
	extra := pyjson.NewObject()
	if meta != nil {
		for _, k := range meta.Keys() {
			if v := meta.Value(k); pyjson.Truthy(v) {
				extra.Set(k, v)
			}
		}
	}
	if agent != "" && !extra.Has("agent") {
		extra.Set("agent", agent)
	}
	var failure error
	splitChunks(data, received, limit, func(ch chunk) bool {
		content := pyurl.DecodeReplace(ch.content)
		sum := sha256.Sum256([]byte(content))
		body := pyjson.NewObject("offset", ch.offset, "length", ch.length, "content", content,
			"sha256", hex.EncodeToString(sum[:]), "mode", mode)
		body.Update(extra)
		resultV, err := c.upload(sessionID, body)
		if err != nil {
			var ue *UploadError
			if errors.As(err, &ue) && ue.Status == 409 {
				log("offset changed on the server; resyncing on the next pass")
				return false
			}
			failure = err
			return false
		}
		result := asDict(resultV)
		mode = "append"
		sent += ch.length
		received = pyInt(or(result.Value("received_bytes"), addSat(ch.offset, ch.length)))
		return true
	})
	if failure != nil {
		return syncResult{}, failure
	}
	return syncResult{sent, received}, nil
}

// watch is watch(client, session_id, path, interval=..., once=..., meta=..., log=...).
func watch(ctx context.Context, c *client, sessionID string, path pyPath, interval float64, agent string,
	once bool, meta *pyjson.Object, log func(string)) int {
	for {
		if ctx.Err() != nil {
			return 0
		}
		result, err := syncOnce(c, sessionID, path, agent, DefaultChunk, meta, log)
		var ue *UploadError
		var oe *osError
		switch {
		case err == nil:
			if result.sent != 0 {
				log(fmt.Sprintf("uploaded %d bytes (server has %d)", result.sent, result.received))
			}
		case errors.As(err, &ue):
			if ue.Status == 401 || ue.Status == 403 {
				code := ue.Code
				if code == "" {
					code = strconv.Itoa(ue.Status)
				}
				log(fmt.Sprintf("ARK refused the upload (%s): %s", code, ue.Message))
				if ue.Code == "SESSION_UPLOADS_DISABLED" {
					log("Ask a project admin to turn on session uploads in Execute > Runs > Session context.")
				}
				return ExitDisabled
			}
			log(fmt.Sprintf("upload failed (%d %s): %s; retrying later", ue.Status, ue.Code, ue.Message))
		case errors.As(err, &oe):
			log(fmt.Sprintf("could not read or send %s: %s; retrying later", path, oe.text))
		default:
			panic(err)
		}
		if once {
			return 0
		}
		if interval*1e9 >= 9.223372036854775807e18 {
			throw("OverflowError", "timestamp too large to convert to C _PyTime_t")
		}
		if !sleep(ctx, interval) {
			return 0
		}
	}
}
