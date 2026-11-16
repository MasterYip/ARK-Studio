package sessionctx

// ContextStore: external session transcripts uploaded into a project folder
// (<project>/sessions/ark-ses-<id>.jsonl + .json).

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pytext"
	"arkstudio/go/internal/secretredact"
	"arkstudio/go/internal/sessions"
	"arkstudio/go/internal/store"
)

var (
	locksGuard sync.Mutex
	locks      = map[string]*sync.Mutex{}
)

// Store is ContextStore.
type Store struct {
	ProjectPath  string
	Root         string
	ExtraSecrets []string
}

// NewStore is ContextStore(project_path, extra_secrets=...).
func NewStore(projectPath string, extraSecrets []string) *Store {
	p := resolve(projectPath)
	var secrets []string
	for _, s := range extraSecrets {
		if s != "" {
			secrets = append(secrets, s)
		}
	}
	return &Store{ProjectPath: p, Root: filepath.Join(p, SessionsDir), ExtraSecrets: secrets}
}

func (s *Store) lock(id string) *sync.Mutex {
	key := s.Root + "|" + id
	locksGuard.Lock()
	defer locksGuard.Unlock()
	m := locks[key]
	if m == nil {
		m = &sync.Mutex{}
		locks[key] = m
	}
	return m
}

// CheckID is ContextStore.check_id.
func CheckID(id string) (string, error) {
	id = pytext.Lower(pytext.Strip(id))
	if !ExternalIDRe.MatchString(id) {
		return "", cerr("invalid ARK session id (expected ark-ses-…)", 400, "SESSION_ID_INVALID")
	}
	return id, nil
}

func (s *Store) paths(id string) (string, string) {
	return filepath.Join(s.Root, id+".jsonl"), filepath.Join(s.Root, id+".json")
}

func (s *Store) readMeta(id string) *pyjson.Object {
	_, metaPath := s.paths(id)
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		return nil
	}
	v, err := pyjson.Loads(universal(string(raw)))
	if err != nil {
		return nil
	}
	o, _ := v.(*pyjson.Object)
	return o
}

func (s *Store) writeMeta(meta *pyjson.Object) error {
	_, metaPath := s.paths(pyjson.Str(meta.Value("id")))
	if err := os.MkdirAll(s.Root, 0o755); err != nil {
		return err
	}
	tmp := strings.TrimSuffix(metaPath, ".json") + ".json.tmp"
	if err := os.WriteFile(tmp, store.TextFile(string(pyjson.DumpsIndent(meta, 2))+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, metaPath)
}

// glob lists root/ark-ses-*<suffix> like Path.glob (case-insensitive on
// Windows, sorted as Paths).
func (s *Store) glob(suffix string) []string {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return nil
	}
	fold := func(x string) string {
		if runtime.GOOS == "windows" {
			return strings.ToLower(x)
		}
		return x
	}
	var out []string
	for _, e := range entries {
		name := fold(e.Name())
		if strings.HasPrefix(name, "ark-ses-") && strings.HasSuffix(name, suffix) && len(name) >= len("ark-ses-")+len(suffix) {
			out = append(out, e.Name())
		}
	}
	sort.Slice(out, func(i, j int) bool { return fold(out[i]) < fold(out[j]) })
	return out
}

// UsageBytes is usage_bytes.
func (s *Store) UsageBytes() int64 {
	var total int64
	for _, name := range s.glob(".jsonl") {
		if fi, err := os.Stat(filepath.Join(s.Root, name)); err == nil {
			total += fi.Size()
		}
	}
	return total
}

// Public is ContextStore.public.
func (s *Store) Public(meta *pyjson.Object) *pyjson.Object {
	agent := orAny(meta.Value("agent"), "other")
	native := orStr(meta.Value("native_session_id"))
	cwd := orAny(meta.Value("cwd"), "")
	command := ResumeCommand(pyjson.Str(agent), native, orStr(meta.Value("cwd")))
	return pyjson.NewObject(
		"id", meta.Value("id"),
		"source", "external",
		"agent", agent,
		"title", orAny(meta.Value("title"), ""),
		"model", orAny(meta.Value("model"), ""),
		"cwd", cwd,
		"native_session_id", native,
		"created_at", meta.Value("created_at"),
		"updated_at", meta.Value("updated_at"),
		"uploaded_by", orAny(meta.Value("uploaded_by"), ""),
		"owner", orAny(meta.Value("owner"), ""),
		"shared", pyjson.Truthy(meta.Value("shared")),
		"received_bytes", pyIntOr0(meta.Value("received_bytes")),
		"bytes", pyIntOr0(meta.Value("bytes")),
		"lines", pyIntOr0(meta.Value("lines")),
		"redactions", pyIntOr0(meta.Value("redactions")),
		"truncated_lines", pyIntOr0(meta.Value("truncated_lines")),
		"linked_units", pyList(meta.Value("linked_units")),
		"task_ref", orAny(meta.Value("task_ref"), ""),
		"prompt", orAny(meta.Value("prompt"), ""),
		"status", "stored",
		"resume", pyjson.NewObject("native", false, "command", command, "cwd", cwd),
	)
}

// List is ContextStore.list.
func (s *Store) List() []*pyjson.Object {
	var out []*pyjson.Object
	for _, name := range s.glob(".json") {
		meta := s.readMeta(strings.TrimSuffix(name, filepath.Ext(name)))
		if meta != nil {
			if id, ok := meta.Value("id").(string); ok && ExternalIDRe.MatchString(id) {
				out = append(out, s.Public(meta))
			}
		}
	}
	return out
}

// Get is ContextStore.get (the stored metadata).
func (s *Store) Get(id string) (*pyjson.Object, error) {
	id, err := CheckID(id)
	if err != nil {
		return nil, err
	}
	meta := s.readMeta(id)
	if meta == nil {
		return nil, errNotFound()
	}
	return meta, nil
}

// Status is ContextStore.status.
func (s *Store) Status(id string, uploadsEnabled bool) (*pyjson.Object, error) {
	id, err := CheckID(id)
	if err != nil {
		return nil, err
	}
	meta := s.readMeta(id)
	var received int64
	if meta != nil {
		received = pyIntOr0(meta.Value("received_bytes"))
	}
	return pyjson.NewObject("id", id, "uploads_enabled", uploadsEnabled, "exists", meta != nil,
		"received_bytes", received, "limits", CurrentLimits().Object()), nil
}

// Lines is ContextStore.lines.
func (s *Store) Lines(id string) ([]string, error) {
	id, err := CheckID(id)
	if err != nil {
		return nil, err
	}
	path, _ := s.paths(id)
	text, err := readText(path)
	if err != nil {
		return nil, nil
	}
	return pytext.SplitLines(text, false), nil
}

// RawPath is ContextStore.raw_path.
func (s *Store) RawPath(id string) (string, error) {
	if _, err := s.Get(id); err != nil {
		return "", err
	}
	id, _ = CheckID(id)
	path, _ := s.paths(id)
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			return "", err
		}
	}
	return path, nil
}

// Transcript is ContextStore.transcript.
func (s *Store) Transcript(id string) (*pyjson.Object, error) {
	meta, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	lines, _ := s.Lines(id)
	entries := TranscriptEntries(lines, meta.Value("updated_at"))
	if len(entries) > sessions.MaxSourceLines {
		entries = entries[len(entries)-sessions.MaxSourceLines:]
	}
	data := sessions.BuildTranscript(sessions.TranscriptInput{
		SessionID: pyjson.Str(meta.Value("id")), Status: "stored", Stdout: entries,
		Stderr: []*pyjson.Object{}, Platform: orStr(meta.Value("agent")),
	})
	data.Set("source", "external")
	return data, nil
}

// Update is ContextStore.update.
func (s *Store) Update(id string, body *pyjson.Object) (*pyjson.Object, error) {
	id, err := CheckID(id)
	if err != nil {
		return nil, err
	}
	l := s.lock(id)
	l.Lock()
	defer l.Unlock()
	meta, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	if body.Has("title") {
		meta.Set("title", clean(body.Value("title"), MaxTitle))
	}
	if body.Has("linked_units") {
		links, err := CleanLinks(body.Value("linked_units"))
		if err != nil {
			return nil, err
		}
		meta.Set("linked_units", links)
	}
	if body.Has("shared") {
		shared, err := CleanShared(body.Value("shared"))
		if err != nil {
			return nil, err
		}
		meta.Set("shared", shared)
	}
	meta.Set("updated_at", sessions.NowISO())
	if err := s.writeMeta(meta); err != nil {
		return nil, err
	}
	return s.Public(meta), nil
}

// Delete is ContextStore.delete.
func (s *Store) Delete(id string) (*pyjson.Object, error) {
	id, err := CheckID(id)
	if err != nil {
		return nil, err
	}
	l := s.lock(id)
	l.Lock()
	defer l.Unlock()
	if _, err := s.Get(id); err != nil {
		return nil, err
	}
	a, b := s.paths(id)
	for _, path := range []string{a, b} {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, cerr("could not delete session: "+err.Error(), 400, "")
		}
	}
	return pyjson.NewObject("deleted", id), nil
}

// dumpsCompact is json.dumps(v, ensure_ascii=False, separators=(",", ":")).
func dumpsCompact(b []byte, v any) []byte {
	switch x := v.(type) {
	case *pyjson.Object:
		b = append(b, '{')
		for i, k := range x.Keys() {
			if i > 0 {
				b = append(b, ',')
			}
			b = pyjson.AppendString(b, k)
			b = append(b, ':')
			b = dumpsCompact(b, x.Value(k))
		}
		return append(b, '}')
	case []any:
		b = append(b, '[')
		for i, item := range x {
			if i > 0 {
				b = append(b, ',')
			}
			b = dumpsCompact(b, item)
		}
		return append(b, ']')
	}
	return pyjson.Append(b, v)
}

func (s *Store) prepare(content string, lineLimit int64) ([]string, []*pyjson.Object, int, int, error) {
	if content != "" && !strings.HasSuffix(content, "\n") {
		return nil, nil, 0, 0, cerr("upload complete JSONL lines only (content must end with a newline)", 400, "SESSION_NOT_JSONL")
	}
	var out []string
	var objects []*pyjson.Object
	redactions, truncated := 0, 0
	var raws []string
	if content != "" {
		parts := strings.Split(content, "\n")
		raws = parts[:len(parts)-1]
	}
	for i, raw := range raws {
		number := i + 1
		raw = strings.TrimRight(raw, "\r")
		if pytext.Strip(raw) == "" {
			continue
		}
		v, err := pyjson.Loads(raw)
		if err != nil {
			return nil, nil, 0, 0, cerr(fmt.Sprintf("line %d is not valid JSON", number), 400, "SESSION_LINE_NOT_JSON")
		}
		obj, ok := v.(*pyjson.Object)
		if !ok {
			return nil, nil, 0, 0, cerr(fmt.Sprintf("line %d is not a JSON object", number), 400, "SESSION_LINE_NOT_OBJECT")
		}
		objects = append(objects, obj)
		red, n := secretredact.Value(obj, s.ExtraSecrets)
		redactions += n
		robj, _ := red.(*pyjson.Object)
		line := string(dumpsCompact(nil, red))
		if int64(len(line)) > lineLimit {
			truncated++
			preview, _ := secretredact.Text(pytext.Prefix(raw, 300), s.ExtraSecrets)
			stype := ""
			if robj != nil {
				stype = pytext.Prefix(orStr(robj.Value("type")), 60)
			}
			line = string(pyjson.Dumps(pyjson.NewObject("type", "ark_truncated", "bytes", int64(len(raw)),
				"source_type", stype, "preview", preview)))
		}
		out = append(out, line)
	}
	return out, objects, redactions, truncated, nil
}

func lengthErr() error {
	return cerr("length must be a positive integer", 400, "SESSION_LENGTH_INVALID")
}

// Upload is ContextStore.upload.
func (s *Store) Upload(id string, body *pyjson.Object, actor, owner string, admin bool) (*pyjson.Object, error) {
	id, err := CheckID(id)
	if err != nil {
		return nil, err
	}
	lim := CurrentLimits()
	content, ok := body.Value("content").(string)
	if !ok {
		return nil, cerr("content must be a JSONL string", 400, "SESSION_CONTENT_INVALID")
	}
	data := []byte(content)
	if int64(len(data)) > lim.Chunk {
		return nil, cerr(fmt.Sprintf("upload chunk is too large (%d bytes; limit %d)", len(data), lim.Chunk), 413, "SESSION_CHUNK_TOO_LARGE")
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	if sent := pytext.Lower(pytext.Strip(orStr(body.Value("sha256")))); sent != "" && sent != digest {
		return nil, cerr("sha256 does not match the uploaded content", 400, "SESSION_HASH_MISMATCH")
	}
	mode := pytext.Lower(pytext.Strip(pyjson.Str(orAny(body.Value("mode"), "append"))))
	if mode != "append" && mode != "replace" {
		return nil, cerr("mode must be append or replace", 400, "SESSION_MODE_INVALID")
	}
	offset, err := store.PyInt(orAny(body.Value("offset"), int64(0)))
	if err != nil {
		return nil, cerr("offset must be a non-negative integer", 400, "SESSION_OFFSET_INVALID")
	}
	if offset < 0 || (mode == "replace" && offset != 0) {
		return nil, cerr("offset must be 0 for replace and non-negative otherwise", 400, "SESSION_REPLACE_OFFSET")
	}
	advance, err := store.PyInt(orAny(body.Value("length"), int64(len(data))))
	if err != nil {
		return nil, lengthErr()
	}
	if advance < 0 || (len(data) > 0 && advance == 0) || advance > 1024*1024*1024 {
		return nil, lengthErr()
	}

	l := s.lock(id)
	l.Lock()
	defer l.Unlock()
	meta := s.readMeta(id)
	fresh := meta == nil
	if fresh {
		meta = pyjson.NewObject("id", id, "kind", "external", "created_at", sessions.NowISO(),
			"owner", owner, "shared", false, "received_bytes", int64(0), "bytes", int64(0), "lines", int64(0),
			"redactions", int64(0), "truncated_lines", int64(0), "chunks", []any{}, "linked_units", []any{})
	} else if !CanManage(meta, owner, admin) {
		return nil, cerr("this session id belongs to another user", 403, "SESSION_UPLOAD_NOT_OWNER")
	}
	received := pyIntOr0(meta.Value("received_bytes"))
	if mode == "append" && offset != received {
		for _, chunk := range iterDicts(orAny(meta.Value("chunks"), []any{})) {
			co := int64(-1)
			if v, has := chunk.Get("offset"); has {
				if n, err := store.PyInt(v); err == nil {
					co = n
				}
			}
			if co == offset && chunk.Value("sha256") == digest {
				return pyjson.NewObject("session", s.Public(meta), "received_bytes", received,
					"appended_lines", int64(0), "redactions", int64(0), "duplicate", true), nil
			}
		}
		return nil, cerr(fmt.Sprintf("offset %d does not match the next expected offset %d", offset, received), 409, "SESSION_OFFSET_MISMATCH")
	}
	lines, objects, redactions, truncated, err := s.prepare(content, lim.Line)
	if err != nil {
		return nil, err
	}
	payload := ""
	if len(lines) > 0 {
		payload = strings.Join(lines, "\n") + "\n"
	}
	added := int64(len(payload))
	var current int64
	if mode != "replace" {
		current = pyIntOr0(meta.Value("bytes"))
	}
	if current+added > lim.Session {
		return nil, cerr(fmt.Sprintf("this session would exceed its storage limit of %d MB", lim.Session/(1024*1024)), 413, "SESSION_QUOTA")
	}
	projectNow := s.UsageBytes()
	if mode == "replace" {
		projectNow -= pyIntOr0(meta.Value("bytes"))
	}
	if projectNow+added > lim.Project {
		return nil, cerr(fmt.Sprintf("the project's session storage would exceed %d MB; delete old sessions first", lim.Project/(1024*1024)), 413, "SESSION_PROJECT_QUOTA")
	}
	path, _ := s.paths(id)
	if err := os.MkdirAll(s.Root, 0o755); err != nil {
		return nil, err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_APPEND
	if mode == "replace" {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	fh, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return nil, err
	}
	_, werr := fh.WriteString(payload)
	if closeErr := fh.Close(); werr == nil {
		werr = closeErr
	}
	if werr != nil {
		return nil, werr
	}
	sniffed := map[string]string{}
	if len(objects) > 0 {
		sniffed = SniffMetadata(objects)
	}
	for _, key := range []string{"agent", "model", "cwd", "native_session_id", "title", "prompt"} {
		var value string
		switch key {
		case "agent":
			value = cleanAgent(body.Value(key))
		case "title":
			value = clean(body.Value(key), MaxTitle)
		default:
			value = clean(body.Value(key), MaxField)
		}
		if value != "" {
			meta.Set(key, value)
		} else if sniffed[key] != "" && !pyjson.Truthy(meta.Value(key)) {
			meta.Set(key, clean(sniffed[key], MaxField))
		}
	}
	if task := clean(body.Value("task"), 40); task != "" && linkRe.MatchString(task) {
		meta.Set("task_ref", task)
	}
	if mode == "replace" {
		meta.Set("bytes", added)
		meta.Set("lines", int64(len(lines)))
		meta.Set("redactions", int64(redactions))
		meta.Set("truncated_lines", int64(truncated))
		meta.Set("chunks", []any{})
	} else {
		meta.Set("bytes", pyIntOr0(meta.Value("bytes"))+added)
		meta.Set("lines", pyIntOr0(meta.Value("lines"))+int64(len(lines)))
		meta.Set("redactions", pyIntOr0(meta.Value("redactions"))+int64(redactions))
		meta.Set("truncated_lines", pyIntOr0(meta.Value("truncated_lines"))+int64(truncated))
	}
	base := offset
	if mode == "replace" {
		base = 0
	}
	meta.Set("received_bytes", base+advance)
	chunks := pyList(meta.Value("chunks"))
	chunks = append(chunks, pyjson.NewObject("offset", offset, "length", advance, "sha256", digest))
	if len(chunks) > MaxChunks {
		chunks = chunks[len(chunks)-MaxChunks:]
	}
	meta.Set("chunks", chunks)
	meta.Set("updated_at", sessions.NowISO())
	if actor != "" {
		meta.Set("uploaded_by", actor)
	}
	if err := s.writeMeta(meta); err != nil {
		return nil, err
	}
	return pyjson.NewObject("session", s.Public(meta), "received_bytes", meta.Value("received_bytes"),
		"appended_lines", int64(len(lines)), "redactions", int64(redactions), "duplicate", false,
		"created", fresh), nil
}
