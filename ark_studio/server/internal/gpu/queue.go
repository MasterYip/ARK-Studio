package gpu

// queue.go is vendor/gpu_server_probe/queue.py: the opt-in bare-metal GPU
// job queue (SQLite file next to the server list, one placement worker).

import (
	"arkstudio/go/internal/platform/clock"
	"context"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pyos"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/rnd"
	"arkstudio/go/internal/sqlitedb"
)

var jobStatuses = map[string]bool{"queued": true, "starting": true, "running": true, "completed": true,
	"errored": true, "canceled": true}

const logTailMarker = "__ARK_QUEUE_LOG_TAIL_V1__"

// QueueError is queue.QueueError (a ValueError with an HTTP status).
type QueueError struct {
	Msg    string
	Status int
}

func (e *QueueError) Error() string { return e.Msg }

func qerr(msg string) error { return &QueueError{msg, 400} }

// logTail is queue._log_tail.
func logTail(value string, lines int) string {
	parts := pytext.SplitLines(value, false)
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return lastRunes(strings.Join(parts, "\n"), 8192)
}

func lastRunes(s string, n int) string {
	if pytext.Len(s) <= n {
		return s
	}
	return pytext.SliceFrom(s, pytext.Len(s)-n)
}

// pyIntArg is int(value) for a decoded JSON value: (value, typeOrValueError, err).
func pyIntArg(value any) (*big.Int, bool, error) {
	switch x := value.(type) {
	case int64:
		return big.NewInt(x), true, nil
	case *big.Int:
		return x, true, nil
	case float64:
		n, err := floatToInt(x)
		if err != nil {
			if _, isValue := err.(*ValueError); isValue {
				return nil, false, nil
			}
			return nil, false, err
		}
		switch v := n.(type) {
		case int64:
			return big.NewInt(v), true, nil
		case *big.Int:
			return v, true, nil
		}
	case string:
		if n, err := pytext.Int(x); err == nil {
			return big.NewInt(n), true, nil
		}
		if b, ok := bigIntText(x); ok {
			return b, true, nil
		}
	}
	return nil, false, nil
}

// bigIntText accepts int() text beyond int64 (ASCII digits only).
func bigIntText(s string) (*big.Int, bool) {
	t := strings.ReplaceAll(pytext.Strip(s), "_", "")
	if t == "" {
		return nil, false
	}
	b, ok := new(big.Int).SetString(t, 10)
	return b, ok
}

// queueInt is queue._int.
func queueInt(value any, label string, low, high, def int64) (int64, error) {
	if value == nil || value == "" {
		return def, nil
	}
	if _, isBool := value.(bool); isBool {
		return 0, qerr(label + " must be an integer")
	}
	n, ok, err := pyIntArg(value)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, qerr(label + " must be an integer")
	}
	if n.Cmp(big.NewInt(low)) < 0 || n.Cmp(big.NewInt(high)) > 0 {
		return 0, qerr(label + " must be between " + strconv.FormatInt(low, 10) + " and " + strconv.FormatInt(high, 10))
	}
	return n.Int64(), nil
}

// jobLabel is JOB_RE.match (full=false: "$" also before a final "\n") or
// JOB_RE.fullmatch.
func jobLabel(s string, full bool) bool {
	if !full && strings.HasSuffix(s, "\n") {
		s = s[:len(s)-1]
	}
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

type pathMap struct {
	from, to string
	servers  []string
}

func pathMaps(queue *pyjson.Object) []pathMap {
	raw, _ := queue.Value("path_map").([]any)
	var out []pathMap
	for _, item := range raw {
		o, ok := item.(*pyjson.Object)
		if !ok {
			continue
		}
		m := pathMap{from: pyjson.Str(orEmpty(o.Value("from"))), to: pyjson.Str(orEmpty(o.Value("to")))}
		if list, ok := o.Value("servers").([]any); ok {
			for _, s := range list {
				m.servers = append(m.servers, pyjson.Str(s))
			}
		}
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool { return pytext.Len(out[i].from) > pytext.Len(out[j].from) })
	return out
}

func skipMap(m pathMap, serverName string) bool {
	if len(m.servers) == 0 || serverName == "" {
		return false
	}
	for _, s := range m.servers {
		if s == serverName {
			return false
		}
	}
	return true
}

// mapPath is queue._map_path.
func mapPath(path string, maps []pathMap, serverName string) string {
	if path == "" {
		return path
	}
	for _, m := range maps {
		if m.from == "" || skipMap(m, serverName) {
			continue
		}
		if path == m.from || strings.HasPrefix(path, m.from+"/") {
			return m.to + path[len(m.from):]
		}
	}
	return path
}

// mapText is queue._map_text.
func mapText(text string, maps []pathMap, serverName string) string {
	if text == "" {
		return text
	}
	for _, m := range maps {
		if skipMap(m, serverName) {
			continue
		}
		text = pyReplace(text, m.from, m.to)
	}
	return text
}

// pyReplace is str.replace (an empty old inserts between every code point).
func pyReplace(s, old, repl string) string {
	if old != "" {
		return strings.ReplaceAll(s, old, repl)
	}
	var b strings.Builder
	b.WriteString(repl)
	for _, r := range s {
		b.WriteRune(r)
		b.WriteString(repl)
	}
	return b.String()
}

// Queue is queue.GpuQueue.
type Queue struct {
	ConfigPath    string
	DBPath        string
	ProbeRunner   Runner
	CommandRunner func(server *pyjson.Object, command string, timeout int) CommandResult
	mu            sync.Mutex
	tickMu        sync.Mutex
	db            *sqlitedb.DB
	stop          chan struct{}
	running       bool
	wg            sync.WaitGroup
	// allowed is the per-job server scope (project id, owner): the worker
	// places a job only on servers its project registers (T-0018, scope.go).
	// nil: every configured server.
	allowed func(projectID, owner string) Scope
}

const queueDDL = `CREATE TABLE IF NOT EXISTS gpu_jobs (
                id TEXT PRIMARY KEY, status TEXT NOT NULL, created_at TEXT NOT NULL,
                updated_at TEXT NOT NULL, server TEXT, gpu_indices TEXT NOT NULL DEFAULT '[]',
                job TEXT NOT NULL, project TEXT NOT NULL, owner TEXT NOT NULL,
                session TEXT NOT NULL, task TEXT NOT NULL DEFAULT '', command TEXT NOT NULL, work_dir TEXT NOT NULL,
                env_json TEXT NOT NULL, timeout_minutes INTEGER NOT NULL,
                gpu_count INTEGER NOT NULL DEFAULT 1,
                min_free_memory_mb INTEGER NOT NULL DEFAULT 1024,
                max_utilization_pct INTEGER NOT NULL DEFAULT 10,
                tags_json TEXT NOT NULL DEFAULT '[]',
                pid INTEGER, exit_code INTEGER, error TEXT NOT NULL DEFAULT '',
                log TEXT NOT NULL DEFAULT '', log_path TEXT NOT NULL DEFAULT '',
                started_at TEXT, ended_at TEXT)`

// NewQueue is GpuQueue(config_path, environ=os.environ).
func NewQueue(configPath string) (*Queue, error) {
	configPath = pathStr(pyos.ExpandUser(configPath))
	dbPath := pytext.Strip(os.Getenv("ARK_GPU_QUEUE_DB"))
	if dbPath == "" {
		dbPath = configPath + ".queue.sqlite3"
	}
	q := &Queue{ConfigPath: configPath, DBPath: pathStr(pyos.ExpandUser(dbPath)),
		ProbeRunner: RunRemoteAllocation, CommandRunner: RunRemoteCommand}
	if err := q.ensureDB(); err != nil {
		return nil, err
	}
	return q, nil
}

func (q *Queue) ensureDB() error {
	if err := os.MkdirAll(filepath.Dir(q.DBPath), 0o755); err != nil {
		return err
	}
	db, err := sqlitedb.Open(q.DBPath, sqlitedb.Options{Journal: "DELETE", Readers: 2, BusyTimeoutMS: 15000})
	if err != nil {
		return err
	}
	q.db = db
	ctx := context.Background()
	return db.Write(ctx, func(tx sqlitedb.Querier) error {
		if _, err := tx.ExecContext(ctx, queueDDL); err != nil {
			return err
		}
		rows, err := sqlitedb.Query(ctx, tx, "PRAGMA table_info(gpu_jobs)")
		if err != nil {
			return err
		}
		existing := map[string]bool{}
		for _, r := range rows {
			existing[pyjson.Str(r.Vals[1])] = true
		}
		for _, col := range [][2]string{{"gpu_count", "INTEGER NOT NULL DEFAULT 1"},
			{"min_free_memory_mb", "INTEGER NOT NULL DEFAULT 1024"},
			{"max_utilization_pct", "INTEGER NOT NULL DEFAULT 10"},
			{"tags_json", "TEXT NOT NULL DEFAULT '[]'"}, {"task", "TEXT NOT NULL DEFAULT ''"}} {
			if !existing[col[0]] {
				if _, err := tx.ExecContext(ctx, "ALTER TABLE gpu_jobs ADD COLUMN "+col[0]+" "+col[1]); err != nil {
					return err
				}
			}
		}
		for _, ddl := range []string{
			"CREATE INDEX IF NOT EXISTS gpu_jobs_status ON gpu_jobs(status, created_at)",
			"CREATE INDEX IF NOT EXISTS gpu_jobs_project_created ON gpu_jobs(project, created_at DESC, id DESC)",
			"CREATE INDEX IF NOT EXISTS gpu_jobs_project_status ON gpu_jobs(project, status, created_at DESC, id DESC)",
			"CREATE INDEX IF NOT EXISTS gpu_jobs_project_name ON gpu_jobs(project, job, created_at DESC, id DESC)",
		} {
			if _, err := tx.ExecContext(ctx, ddl); err != nil {
				return err
			}
		}
		return nil
	}, nil)
}

func (q *Queue) query(sql string, args ...any) ([]sqlitedb.Row, error) {
	var rows []sqlitedb.Row
	err := q.db.Read(context.Background(), func(tx sqlitedb.Querier) error {
		var err error
		rows, err = sqlitedb.Query(context.Background(), tx, sql, args...)
		return err
	})
	return rows, err
}

func (q *Queue) exec(sql string, args ...any) error {
	ctx := context.Background()
	return q.db.Write(ctx, func(tx sqlitedb.Querier) error {
		_, err := tx.ExecContext(ctx, sql, args...)
		return err
	}, nil)
}

// public is GpuQueue._public.
func public(row sqlitedb.Row) *pyjson.Object {
	data := pyjson.NewObject()
	for i, col := range row.Cols {
		data.Set(col, row.Vals[i])
	}
	loadsOr := func(key, def string) any {
		text := pyjson.Str(data.Value(key))
		if !pyjson.Truthy(data.Value(key)) {
			text = def
		}
		v, err := pyjson.Loads(text)
		if err != nil {
			v, _ = pyjson.Loads(def)
		}
		return v
	}
	data.Set("gpu_indices", loadsOr("gpu_indices", "[]"))
	data.Set("env", loadsOr("env_json", "{}"))
	data.Set("tags", loadsOr("tags_json", "[]"))
	data.Delete("env_json")
	data.Delete("tags_json")
	data.Set("name", orDefault(data, "job", ""))
	data.Set("log_tail", logTail(pyjson.Str(orDefault(data, "log", "")), 20))
	if code := data.Value("exit_code"); code != nil && code != int64(0) && !pyjson.Truthy(data.Value("error")) {
		data.Set("error", "remote process exited with code "+pyjson.Str(code))
	}
	return data
}

func orDefault(o *pyjson.Object, key string, def any) any {
	if v, ok := o.Get(key); ok {
		return v
	}
	return def
}

// Start is GpuQueue.start: the worker loop.
func (q *Queue) Start() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.running {
		return
	}
	q.running = true
	q.stop = make(chan struct{})
	q.wg.Add(1)
	go q.loop(q.stop)
}

// Close is GpuQueue.close.
func (q *Queue) Close() {
	q.mu.Lock()
	stop := q.stop
	running := q.running
	q.running = false
	q.mu.Unlock()
	if running {
		close(stop)
		done := make(chan struct{})
		go func() { q.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
}

func (q *Queue) loop(stop chan struct{}) {
	defer q.wg.Done()
	for {
		select {
		case <-stop:
			return
		default:
		}
		q.Tick() //nolint:errcheck // a queue failure must not kill ARK
		interval := int64(5)
		if cfg, err := LoadConfig(q.ConfigPath); err == nil {
			if n, ok := cfg.Value("queue").(*pyjson.Object).Value("poll_seconds").(int64); ok {
				interval = n
			}
		}
		select {
		case <-stop:
			return
		case <-time.After(time.Duration(max(1, interval)) * time.Second):
		}
	}
}

func (q *Queue) loadConfig() (*pyjson.Object, error) {
	cfg, err := LoadConfig(q.ConfigPath)
	if err != nil {
		var ce *ConfigError
		if errors.As(err, &ce) {
			return nil, &QueueError{"GPU queue config is invalid: " + ce.Error(), 422}
		}
		return nil, err
	}
	return cfg, nil
}

type jobSpec struct {
	id, job, project, owner, session, task, command, workDir, server string
	env                                                              *pyjson.Object
	gpuCount, minFree, maxUtil, timeout                              int64
	tags                                                             []any
}

func strField(body *pyjson.Object, keys ...string) string {
	for _, k := range keys {
		if v := body.Value(k); pyjson.Truthy(v) {
			return pyjson.Str(v)
		}
	}
	return ""
}

func getDefault(body *pyjson.Object, key string, def any) any {
	if v, ok := body.Get(key); ok {
		return v
	}
	return def
}

// validate is GpuQueue._validate.
func (q *Queue) validate(body *pyjson.Object, cfg *pyjson.Object) (*jobSpec, error) {
	queue := cfg.Value("queue").(*pyjson.Object)
	if !pyjson.Truthy(queue.Value("enabled")) {
		return nil, &QueueError{"GPU queue is disabled; set queue.enabled: true in the server list", 403}
	}
	command, err := DispatchCommand(body.Value("command"))
	if err != nil {
		return nil, qerr(err.Error())
	}
	workDir, err := DispatchWorkDir(body.Value("work_dir"))
	if err != nil {
		return nil, qerr(err.Error())
	}
	env, err := DispatchEnv(body.Value("env"))
	if err != nil {
		return nil, qerr(err.Error())
	}
	job := pytext.Strip(orStr(body.Value("job"), "ark-job"))
	project := pytext.Strip(orStr(body.Value("project"), "ark"))
	if !jobLabel(job, false) {
		return nil, qerr("job must be a short label using letters, digits, dot, dash or underscore")
	}
	if !jobLabel(project, false) {
		return nil, qerr("project must be a short label using letters, digits, dot, dash or underscore")
	}
	spec := &jobSpec{job: job, project: project, command: command, workDir: workDir, env: env}
	if spec.gpuCount, err = queueInt(getDefault(body, "gpu_count", body.Value("gpus")), "gpu_count", 1, 8, 1); err != nil {
		return nil, err
	}
	claim := body.Value("min_free_memory_mb")
	if claim == nil {
		claim = getDefault(body, "gpu_memory_mb", body.Value("memory_mb"))
	}
	if spec.minFree, err = queueInt(claim, "min_free_memory_mb", 0, 1_000_000, intOr(queue.Value("min_free_memory_mb"), 1024)); err != nil {
		return nil, err
	}
	if spec.maxUtil, err = queueInt(body.Value("max_utilization_pct"), "max_utilization_pct", 0, 100,
		intOr(queue.Value("max_utilization_pct"), 10)); err != nil {
		return nil, err
	}
	if spec.timeout, err = queueInt(body.Value("timeout_minutes"), "timeout_minutes", 1, 1440,
		intOr(queue.Value("default_timeout_minutes"), 240)); err != nil {
		return nil, err
	}
	spec.server = pytext.Strip(strField(body, "server"))
	if spec.server != "" && findServer(cfg, spec.server) == nil {
		return nil, qerr("unknown server " + pyjson.Repr(spec.server))
	}
	tagsRaw := body.Value("tags")
	if !pyjson.Truthy(tagsRaw) {
		tagsRaw = []any{}
	}
	tags, ok := tagsRaw.([]any)
	if !ok {
		return nil, qerr("tags must be a list")
	}
	spec.tags = []any{}
	for _, tag := range tags {
		if s := pytext.Strip(pyjson.Str(tag)); s != "" && len(spec.tags) < 8 {
			spec.tags = append(spec.tags, s)
		}
	}
	b := rnd.Bytes("gpu", 16)
	b[6] = b[6]&0x0f | 0x40
	spec.id = "gpu_" + hexString(b)[:16]
	spec.owner = pytext.Prefix(pytext.Strip(strField(body, "owner")), 80)
	spec.session = pytext.Prefix(pytext.Strip(strField(body, "session")), 80)
	spec.task = pytext.Prefix(pytext.Strip(strField(body, "task", "task_ref")), 80)
	return spec, nil
}

func hexString(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[2*i], out[2*i+1] = digits[c>>4], digits[c&15]
	}
	return string(out)
}

func orStr(v any, def string) string {
	if pyjson.Truthy(v) {
		return pyjson.Str(v)
	}
	return def
}

// intOr is int(value or def) for a validated config integer.
func intOr(v any, def int64) int64 {
	if n, ok := v.(int64); ok && n != 0 {
		return n
	}
	return def
}

func findServer(cfg *pyjson.Object, name any) *pyjson.Object {
	servers, _ := cfg.Value("servers").([]any)
	for _, s := range servers {
		if o := s.(*pyjson.Object); o.Value("name") == name {
			return o
		}
	}
	return nil
}

// Submit is GpuQueue.submit.
func (q *Queue) Submit(body *pyjson.Object, start bool) (*pyjson.Object, error) {
	return q.SubmitChecked(body, start, nil)
}

// SubmitChecked is Submit with check(config) run after validation (the
// project scope: Service.QueueSubmitFor).
func (q *Queue) SubmitChecked(body *pyjson.Object, start bool, check func(cfg *pyjson.Object) error) (*pyjson.Object, error) {
	cfg, err := q.loadConfig()
	if err != nil {
		return nil, err
	}
	job, err := q.validate(body, cfg)
	if err != nil {
		return nil, err
	}
	if check != nil {
		if err := check(cfg); err != nil {
			return nil, err
		}
	}
	now := clock.UTCNow()
	err = q.exec(`INSERT INTO gpu_jobs
                (id,status,created_at,updated_at,server,gpu_indices,job,project,owner,
                 session,task,command,work_dir,env_json,timeout_minutes,gpu_count,
                 min_free_memory_mb,max_utilization_pct,tags_json,error,log,log_path)
                VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		job.id, "queued", now, now, job.server, "[]", job.job, job.project, job.owner, job.session, job.task,
		job.command, job.workDir, string(pyjson.Dumps(job.env)), job.timeout, job.gpuCount, job.minFree,
		job.maxUtil, string(pyjson.DumpsASCII(job.tags)), "", "", "")
	if err != nil {
		return nil, err
	}
	if start {
		q.Start()
	}
	return q.Get(job.id)
}

// Get is GpuQueue.get.
func (q *Queue) Get(id string) (*pyjson.Object, error) {
	rows, err := q.query("SELECT * FROM gpu_jobs WHERE id=?", id)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, &QueueError{"GPU job not found", 404}
	}
	return public(rows[0]), nil
}

// ListFilters are the keyword arguments of GpuQueue.list.
type ListFilters struct {
	Owner, Project, Session, Status, NamePrefix, Since, Server, Name string
	Limit, Offset                                                    any
	IDs                                                              []any
	HasIDs                                                           bool
}

// List is GpuQueue.list.
func (q *Queue) List(f ListFilters) (*pyjson.Object, error) {
	if f.Limit == nil {
		f.Limit = int64(100)
	}
	if f.Offset == nil {
		f.Offset = int64(0)
	}
	limit, err := queueInt(f.Limit, "limit", 1, 500, 100)
	if err != nil {
		return nil, err
	}
	offset, err := queueInt(f.Offset, "offset", 0, 1_000_000, 0)
	if err != nil {
		return nil, err
	}
	var clauses []string
	var values []any
	for _, kv := range [][2]string{{"owner", f.Owner}, {"project", f.Project}, {"session", f.Session},
		{"server", f.Server}, {"job", f.Name}} {
		if kv[1] == "" {
			continue
		}
		if pytext.Len(kv[1]) > 256 || strings.ContainsFunc(kv[1], func(r rune) bool { return r < 32 }) {
			return nil, qerr("invalid " + kv[0] + " filter")
		}
		clauses = append(clauses, kv[0]+"=?")
		values = append(values, kv[1])
	}
	if f.Status != "" {
		var states []string
		seen := map[string]bool{}
		bad := false
		for _, part := range strings.Split(f.Status, ",") {
			if p := pytext.Strip(part); p != "" && !seen[p] {
				seen[p] = true
				states = append(states, p)
				bad = bad || !jobStatuses[p]
			}
		}
		if len(states) == 0 || bad {
			return nil, qerr("status must contain queued, starting, running, completed, errored or canceled")
		}
		clauses = append(clauses, "status IN ("+strings.TrimSuffix(strings.Repeat("?,", len(states)), ",")+")")
		for _, s := range states {
			values = append(values, s)
		}
	}
	if f.NamePrefix != "" {
		if !jobLabel(f.NamePrefix, true) {
			return nil, qerr("name_prefix must be 1 to 64 letters, digits, dots, dashes or underscores")
		}
		clauses = append(clauses, "job GLOB ?")
		values = append(values, f.NamePrefix+"*")
	}
	if f.Since != "" {
		stamp, ok := sinceStamp(f.Since)
		if !ok {
			return nil, qerr("since must be an ISO timestamp with a timezone")
		}
		clauses = append(clauses, "created_at>=?")
		values = append(values, stamp)
	}
	if f.HasIDs {
		if len(f.IDs) < 1 || len(f.IDs) > 500 {
			return nil, qerr("ids must contain 1 to 500 GPU job IDs")
		}
		for _, v := range f.IDs {
			s, isStr := v.(string)
			if !isStr || !validJobID(s) {
				return nil, qerr("ids must contain 1 to 500 GPU job IDs")
			}
		}
		clauses = append(clauses, "id IN ("+strings.TrimSuffix(strings.Repeat("?,", len(f.IDs)), ",")+")")
		values = append(values, f.IDs...)
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	var total int64
	var rows []sqlitedb.Row
	ctx := context.Background()
	err = q.db.Read(ctx, func(tx sqlitedb.Querier) error {
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM gpu_jobs"+where, values...).Scan(&total); err != nil {
			return err
		}
		var err error
		rows, err = sqlitedb.Query(ctx, tx, "SELECT * FROM gpu_jobs"+where+" ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?",
			append(append([]any{}, values...), limit, offset)...)
		return err
	})
	if err != nil {
		return nil, err
	}
	jobs := make([]any, len(rows))
	for i, r := range rows {
		jobs[i] = public(r)
	}
	hasMore := offset+int64(len(rows)) < total
	var next any
	if hasMore {
		next = offset + int64(len(rows))
	}
	cfg, err := q.loadConfig()
	if err != nil {
		return nil, err
	}
	return pyjson.NewObject("jobs", jobs, "total", total, "count", int64(len(rows)), "limit", limit,
		"offset", offset, "has_more", hasMore, "next_offset", next,
		"queue_enabled", pyjson.Truthy(cfg.Value("queue").(*pyjson.Object).Value("enabled"))), nil
}

func validJobID(s string) bool {
	if !strings.HasPrefix(s, "gpu_") {
		return false
	}
	rest := s[4:]
	if len(rest) < 1 || len(rest) > 64 {
		return false
	}
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func killCommand(pid int64) string {
	p := strconv.FormatInt(pid, 10)
	return "kill -TERM -" + p + " 2>/dev/null || kill -TERM " + p + " 2>/dev/null || true"
}

func pidOf(job *pyjson.Object) (int64, bool) {
	pid, ok := job.Value("pid").(int64)
	return pid, ok && pid != 0
}

// Cancel is GpuQueue.cancel.
func (q *Queue) Cancel(id string) (*pyjson.Object, error) {
	job, err := q.Get(id)
	if err != nil {
		return nil, err
	}
	switch job.Value("status") {
	case "completed", "errored", "canceled":
		return job, nil
	}
	cfg, err := q.loadConfig()
	if err != nil {
		return nil, err
	}
	if server := findServer(cfg, job.Value("server")); server != nil {
		if pid, ok := pidOf(job); ok {
			RunRemoteCommand(server, killCommand(pid), 10)
		}
	}
	now := clock.UTCNow()
	if err := q.exec("UPDATE gpu_jobs SET status='canceled', updated_at=?, ended_at=?, error=? WHERE id=?",
		now, now, "canceled by user", id); err != nil {
		return nil, err
	}
	return q.Get(id)
}

// Logs is GpuQueue.logs.
func (q *Queue) Logs(id string, limit any) (*pyjson.Object, error) {
	n, err := queueInt(limit, "tail", 1, 500, 200)
	if err != nil {
		return nil, err
	}
	job, err := q.Get(id)
	if err != nil {
		return nil, err
	}
	logPath := pyjson.Str(orEmpty(job.Value("log_path")))
	cached := pyjson.NewObject("log", logTail(pyjson.Str(orDefault(job, "log", "")), int(n)), "path", logPath,
		"cached", true)
	if !pyjson.Truthy(job.Value("server")) || logPath == "" {
		return cached, nil
	}
	cfg, err := q.loadConfig()
	if err != nil {
		return nil, err
	}
	server := findServer(cfg, job.Value("server"))
	if server == nil {
		return cached, nil
	}
	result := q.CommandRunner(server, "tail -n "+strconv.FormatInt(n, 10)+" "+ShlexQuote(logPath)+
		" 2>/dev/null | tail -c 65536", 15)
	if result.OK() && result.Stdout != "" {
		return pyjson.NewObject("log", lastRunes(result.Stdout, 65536), "path", logPath, "cached", false), nil
	}
	return cached, nil
}

type gpuKey struct {
	server string
	index  int64
}

func indicesOf(raw any) []int64 {
	text := pyjson.Str(raw)
	if !pyjson.Truthy(raw) {
		text = "[]"
	}
	v, err := pyjson.Loads(text)
	if err != nil {
		return nil
	}
	list, _ := v.([]any)
	var out []int64
	for _, item := range list {
		switch x := item.(type) {
		case int64:
			out = append(out, x)
		case float64:
			out = append(out, int64(x))
		}
	}
	return out
}

func (q *Queue) activeByGPU() (map[gpuKey]int64, error) {
	rows, err := q.query("SELECT server, gpu_indices FROM gpu_jobs WHERE status IN ('starting','running')")
	if err != nil {
		return nil, err
	}
	active := map[gpuKey]int64{}
	for _, r := range rows {
		for _, index := range indicesOf(r.Get("gpu_indices")) {
			active[gpuKey{pyjson.Str(orEmpty(r.Get("server"))), index}]++
		}
	}
	return active, nil
}

type jobGPU struct {
	server string
	index  int64
	job    string
}

func (q *Queue) reservedByGPU(results []*pyjson.Object) (map[gpuKey]int64, error) {
	observed := map[jobGPU]int64{}
	for _, result := range results {
		system, _ := result.Value("system").(*pyjson.Object)
		if system == nil {
			continue
		}
		procs, _ := system.Value("processes").([]any)
		for _, item := range procs {
			p := item.(*pyjson.Object)
			if !pyjson.Truthy(p.Value("job_id")) || p.Value("gpu_index") == nil {
				continue
			}
			mem, _ := p.Value("memory_mb").(int64)
			key := jobGPU{pyjson.Str(result.Value("server")), p.Value("gpu_index").(int64), pyjson.Str(p.Value("job_id"))}
			observed[key] += max(0, mem)
		}
	}
	rows, err := q.query("SELECT id, server, gpu_indices, min_free_memory_mb FROM gpu_jobs " +
		"WHERE status IN ('starting','running')")
	if err != nil {
		return nil, err
	}
	reserved := map[gpuKey]int64{}
	for _, r := range rows {
		for _, index := range indicesOf(r.Get("gpu_indices")) {
			key := gpuKey{pyjson.Str(orEmpty(r.Get("server"))), index}
			claim, _ := r.Get("min_free_memory_mb").(int64)
			reserved[key] += max(0, claim-observed[jobGPU{key.server, index, pyjson.Str(r.Get("id"))}])
		}
	}
	return reserved, nil
}

func (q *Queue) probe(cfg *pyjson.Object) ([]*pyjson.Object, error) {
	servers, _ := cfg.Value("servers").([]any)
	var results []*pyjson.Object
	for _, item := range servers {
		server := item.(*pyjson.Object)
		if v, ok := server.Get("enabled"); ok && !pyjson.Truthy(v) {
			continue
		}
		timeout, _ := server.Value("timeout_seconds").(int64)
		result, err := ProbeServer(server, q.ProbeRunner, int(timeout), nil)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

type selection struct {
	server *pyjson.Object
	gpus   []*pyjson.Object
}

func tagsSubset(job []any, server *pyjson.Object) bool {
	have := map[string]bool{}
	if list, ok := server.Value("tags").([]any); ok {
		for _, t := range list {
			have[pyjson.Str(t)] = true
		}
	}
	for _, t := range job {
		if !have[pyjson.Str(t)] {
			return false
		}
	}
	return true
}

// selectGPUs is GpuQueue._select.
func selectGPUs(job *pyjson.Object, cfg *pyjson.Object, results []*pyjson.Object, active, reserved map[gpuKey]int64) *selection {
	queue := cfg.Value("queue").(*pyjson.Object)
	claim, _ := job.Value("min_free_memory_mb").(int64)
	maxUtil, _ := job.Value("max_utilization_pct").(int64)
	count, _ := job.Value("gpu_count").(int64)
	jobServer := job.Value("server")
	jobTags, _ := job.Value("tags").([]any)
	var best *selection
	var bestScore tuple
	for _, result := range results {
		if !pyjson.Truthy(result.Value("ok")) {
			continue
		}
		if pyjson.Truthy(jobServer) && result.Value("server") != jobServer {
			continue
		}
		server := findServer(cfg, result.Value("server"))
		if server == nil {
			continue
		}
		if len(jobTags) > 0 && !tagsSubset(jobTags, server) {
			continue
		}
		type cand struct {
			key tuple
			gpu *pyjson.Object
		}
		var candidates []cand
		gpus, _ := result.Value("gpus").([]any)
		name := pyjson.Str(server.Value("name"))
		for _, item := range gpus {
			gpu := item.(*pyjson.Object)
			index, _ := gpu.Value("index").(int64)
			free, freeOK := optFloat(gpu.Value("free_memory_mb"))
			util, utilOK := optFloat(gpu.Value("utilization_pct"))
			if !freeOK || !utilOK {
				continue
			}
			key := gpuKey{name, index}
			available := free - float64(reserved[key])
			if available < float64(claim) {
				continue
			}
			if util > float64(maxUtil) {
				continue
			}
			used := active[key]
			limit, _ := queue.Value("max_jobs_per_gpu").(int64)
			if limit != 0 && used >= limit {
				continue
			}
			if v, ok := queue.Get("allow_colocation"); ok && !pyjson.Truthy(v) && used != 0 {
				continue
			}
			candidates = append(candidates, cand{tuple{util, -available, index}, gpu})
		}
		pySort(candidates, func(c cand) tuple { return c.key })
		if int64(len(candidates)) < count {
			continue
		}
		selected := candidates[:count]
		worst := selected[0].key
		for _, c := range selected[1:] {
			if tupleLt(worst, c.key) { // max(): replace when item > max
				worst = c.key
			}
		}
		score := append(append(tuple{}, worst...), name)
		if best == nil || tupleLt(score, bestScore) {
			out := make([]*pyjson.Object, len(selected))
			for i, c := range selected {
				out[i] = c.gpu
			}
			best, bestScore = &selection{server, out}, score
		}
	}
	return best
}

// script is GpuQueue._script.
func script(cfg, job *pyjson.Object, gpus []*pyjson.Object, serverName string) (string, string) {
	queue := cfg.Value("queue").(*pyjson.Object)
	maps := pathMaps(queue)
	id := pyjson.Str(job.Value("id"))
	state := strings.TrimRight(pyjson.Str(queue.Value("state_dir")), "/") + "/" + id
	statusPath := state + "/status"
	env := pyjson.NewObject()
	if qe, ok := queue.Value("env").(*pyjson.Object); ok {
		env.Update(qe)
	}
	if je, ok := job.Value("env").(*pyjson.Object); ok {
		env.Update(je)
	}
	mapped := map[string]string{}
	for _, k := range env.Keys() {
		mapped[k] = mapText(pyjson.Str(env.Value(k)), maps, serverName)
	}
	devices := make([]string, len(gpus))
	for i, g := range gpus {
		devices[i] = pyjson.Str(g.Value("index"))
	}
	mapped["CUDA_VISIBLE_DEVICES"] = strings.Join(devices, ",")
	mapped["ARK_GPU_JOB_ID"] = id
	mapped["ARK_GPU_JOB_STATUS"] = statusPath
	lines := []string{"#!/usr/bin/env bash", "set -o pipefail"}
	if setup := pyjson.Str(orEmpty(queue.Value("setup"))); setup != "" {
		lines = append(lines, mapText(setup, maps, serverName))
	}
	workDir := mapPath(pyjson.Str(orEmpty(job.Value("work_dir"))), maps, serverName)
	if workDir != "" {
		lines = append(lines, "cd "+ShlexQuote(workDir)+" || exit 86")
	}
	keys := make([]string, 0, len(mapped))
	for k := range mapped {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	exports := make([]string, len(keys))
	for i, k := range keys {
		exports[i] = k + "=" + ShlexQuote(mapped[k])
	}
	lines = append(lines, "export "+strings.Join(exports, " "))
	lines = append(lines, mapText(pyjson.Str(job.Value("command")), maps, serverName))
	lines = append(lines, "code=$?", `echo "$code" > "$ARK_GPU_JOB_STATUS"`, `exit "$code"`)
	return state, strings.Join(lines, "\n") + "\n"
}

// launch is GpuQueue._launch.
func (q *Queue) launch(job, server *pyjson.Object, gpus []*pyjson.Object, cfg *pyjson.Object) error {
	state, body := script(cfg, job, gpus, pyjson.Str(server.Value("name")))
	logPath, pidPath, launchPath := state+"/job.log", state+"/pid", state+"/launch.sh"
	launcher := "#!/usr/bin/env bash\n" + "echo $$ > " + ShlexQuote(pidPath) + "\n" +
		"exec bash " + ShlexQuote(state) + "/job.sh\n"
	qs := ShlexQuote(state)
	remote := "mkdir -p " + qs + " && " +
		"printf '%s' " + ShlexQuote(body) + " > " + qs + "/job.sh && " +
		"printf '%s' " + ShlexQuote(launcher) + " > " + ShlexQuote(launchPath) + " && " +
		"chmod +x " + qs + "/job.sh " + ShlexQuote(launchPath) + " && " +
		"setsid -f bash " + ShlexQuote(launchPath) + " </dev/null > " + ShlexQuote(logPath) + " 2>&1; " +
		"for i in $(seq 1 50); do [ -s " + ShlexQuote(pidPath) + " ] && break; sleep 0.1; done; " +
		"cat " + ShlexQuote(pidPath) + " 2>/dev/null || true"
	result := q.CommandRunner(server, remote, 20)
	var pid int64
	for _, token := range strings.FieldsFunc(result.Stdout, pytext.IsSpace) {
		if pytext.IsDigit(token) {
			n, err := pytext.Int(token)
			if err != nil {
				return &ValueError{"invalid literal for int() with base 10: " + pyjson.Repr(token)}
			}
			pid = n
		}
	}
	now := clock.UTCNow()
	if !result.OK() || pid == 0 {
		msg := result.Message
		if msg == "" {
			msg = "could not launch remote job"
		}
		return q.exec(`UPDATE gpu_jobs SET status='errored', updated_at=?, ended_at=?,
                              error=?, log_path=?, log=? WHERE id=?`,
			now, now, pytext.Prefix(msg, 500), logPath, logTail(result.Stdout+"\n"+result.Stderr, 20), job.Value("id"))
	}
	return q.exec(`UPDATE gpu_jobs SET status='running', updated_at=?, pid=?,
                          started_at=?, log_path=? WHERE id=?`, now, pid, now, logPath, job.Value("id"))
}

// reap is GpuQueue._reap.
func (q *Queue) reap(cfg *pyjson.Object) error {
	rows, err := q.query("SELECT * FROM gpu_jobs WHERE status='running'")
	if err != nil {
		return err
	}
	queue := cfg.Value("queue").(*pyjson.Object)
	for _, row := range rows {
		job := public(row)
		server := findServer(cfg, job.Value("server"))
		pid, hasPID := pidOf(job)
		if server == nil || !hasPID {
			continue
		}
		state := strings.TrimRight(pyjson.Str(queue.Value("state_dir")), "/") + "/" + pyjson.Str(job.Value("id"))
		statusPath := ShlexQuote(state + "/status")
		failureTail := `printf '\n` + logTailMarker + `\n'; ` +
			"tail -n 20 " + ShlexQuote(state+"/job.log") + " 2>/dev/null | tail -c 8192"
		p := strconv.FormatInt(pid, 10)
		command := "if [ -f " + statusPath + " ]; then code=$(cat " + statusPath + "); echo DONE:$code; " +
			`if [ "$code" != 0 ]; then ` + failureTail + "; fi; " +
			"elif kill -0 " + p + " 2>/dev/null; then echo RUNNING; " +
			"else echo LOST; " + failureTail + "; fi"
		result := q.CommandRunner(server, command, 12)
		if !result.OK() {
			continue
		}
		output, log := result.Stdout, ""
		if i := strings.Index(output, logTailMarker); i >= 0 {
			output, log = output[:i], output[i+len(logTailMarker):]
		}
		output = pytext.Strip(output)
		log = logTail(pytext.Strip(log), 20)
		var code *int64
		if strings.HasPrefix(output, "DONE:") {
			if n, err := pytext.Int(output[strings.Index(output, ":")+1:]); err == nil {
				code = &n
			}
		}
		if code == nil && output == "RUNNING" {
			elapsed := 0.0
			if started, ok := parseISO(pyjson.Str(orEmpty(job.Value("started_at")))); ok {
				elapsed = clock.Now().Sub(started).Seconds()
			}
			timeout, _ := job.Value("timeout_minutes").(int64)
			if elapsed > float64(timeout*60) {
				RunRemoteCommand(server, killCommand(pid), 10)
				n := int64(-1)
				code = &n
			} else {
				continue
			}
		}
		if code == nil {
			n := int64(-1)
			code = &n
		}
		now := clock.UTCNow()
		status, errText := "completed", ""
		if *code != 0 {
			status = "errored"
			errText = "remote process exited with code " + strconv.FormatInt(*code, 10)
			if log != "" {
				lines := pytext.SplitLines(log, false)
				errText += ": " + lines[len(lines)-1]
			}
		}
		if err := q.exec(`UPDATE gpu_jobs SET status=?, exit_code=?, updated_at=?,
                              ended_at=?, error=?, log=? WHERE id=? AND status='running'`,
			status, *code, now, now, pytext.Prefix(errText, 500), log, job.Value("id")); err != nil {
			return err
		}
	}
	return nil
}

// Tick is GpuQueue.tick.
func (q *Queue) Tick() error {
	if !q.tickMu.TryLock() {
		return nil
	}
	defer q.tickMu.Unlock()
	cfg, err := q.loadConfig()
	if err != nil {
		return err
	}
	queue := cfg.Value("queue").(*pyjson.Object)
	if !pyjson.Truthy(queue.Value("enabled")) {
		return nil
	}
	if err := q.reap(cfg); err != nil {
		return err
	}
	rows, err := q.query("SELECT * FROM gpu_jobs WHERE status='queued' ORDER BY created_at LIMIT 32")
	if err != nil || len(rows) == 0 {
		return err
	}
	results, err := q.probe(cfg)
	if err != nil {
		return err
	}
	active, err := q.activeByGPU()
	if err != nil {
		return err
	}
	reserved, err := q.reservedByGPU(results)
	if err != nil {
		return err
	}
	scopes := map[[2]string]Scope{}
	for _, row := range rows {
		job := public(row)
		candidates := results
		if q.allowed != nil {
			key := [2]string{pyjson.Str(orEmpty(job.Value("project"))), pyjson.Str(orEmpty(job.Value("owner")))}
			sc, ok := scopes[key]
			if !ok {
				sc = q.allowed(key[0], key[1])
				scopes[key] = sc
			}
			if !sc.All {
				candidates = nil
				for _, r := range results {
					if sc.Allows(pyjson.Str(r.Value("server"))) {
						candidates = append(candidates, r)
					}
				}
			}
		}
		sel := selectGPUs(job, cfg, candidates, active, reserved)
		if sel == nil {
			continue
		}
		indices := make([]any, len(sel.gpus))
		for i, g := range sel.gpus {
			indices[i] = g.Value("index")
		}
		now := clock.UTCNow()
		name := pyjson.Str(sel.server.Value("name"))
		if err := q.exec(`UPDATE gpu_jobs SET status='starting', server=?, gpu_indices=?,
                                  updated_at=?, started_at=? WHERE id=? AND status='queued'`,
			name, string(pyjson.Dumps(indices)), now, now, job.Value("id")); err != nil {
			return err
		}
		claim, _ := job.Value("min_free_memory_mb").(int64)
		for _, g := range sel.gpus {
			key := gpuKey{name, g.Value("index").(int64)}
			active[key]++
			reserved[key] += claim
		}
		if err := q.launch(job, sel.server, sel.gpus, cfg); err != nil {
			return err
		}
	}
	return nil
}
