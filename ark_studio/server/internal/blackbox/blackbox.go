// Package blackbox ports arkstudio/blackbox.py: out-of-band SQLite snapshots
// for project rollback, kept next to the project root.
package blackbox

import (
	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/platform/textfile"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/sqlitedb"
)

// ValueError is a Python ValueError (HTTP 400 on POST).
type ValueError struct{ Message string }

func (e *ValueError) Error() string { return e.Message }

// OSError is FileNotFoundError and friends: the handler answers 500.
type OSError struct{ Message string }

func (e *OSError) Error() string { return e.Message }

// Project is what the black box needs from a project.
type Project interface {
	ID() string
	Name() string
	Folder() string
	Revision(ctx context.Context) (string, error)
	// Reopen closes the live store, runs replace, and opens a fresh store.
	Reopen(replace func() error) error
}

// BlackBox is one snapshot vault.
type BlackBox struct {
	Root     string
	Interval float64 // seconds
	Enabled  bool
	Current  func() Project

	mu           sync.Mutex
	lastRevision map[string]string
	lastAt       map[string]float64 // time.time() of the last snapshot
	stop         chan struct{}
	once         sync.Once
}

// New is BlackBox.__init__.
func New(root string, interval int64, enabled bool, current func() Project) *BlackBox {
	abs, _ := filepath.Abs(root)
	os.MkdirAll(abs, 0o700) //nolint:errcheck
	os.Chmod(abs, 0o700)    //nolint:errcheck
	if interval == 0 {
		interval = 900
	}
	return &BlackBox{Root: abs, Interval: float64(max(60, interval)), Enabled: enabled,
		Current: current, lastRevision: map[string]string{}, lastAt: map[string]float64{}, stop: make(chan struct{})}
}

func now() time.Time { return clock.Now().UTC() }

func utc() string { return clock.UTCNow() }

func unixNow() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func stamp() string { return now().Format("20060102T150405Z") }

// SafeID is blackbox._safe_id.
func SafeID(value string) string {
	var b strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	out := pytext.Prefix(b.String(), 120)
	if out == "" {
		return "project"
	}
	return out
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// sortedObject re-creates o with keys sorted (json.dumps(sort_keys=True)).
func sortedObject(v any) any {
	switch x := v.(type) {
	case *pyjson.Object:
		keys := append([]string(nil), x.Keys()...)
		sort.Strings(keys)
		out := pyjson.NewObject()
		for _, k := range keys {
			out.Set(k, sortedObject(x.Value(k)))
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = sortedObject(item)
		}
		return out
	}
	return v
}

func writeJSON(path string, payload *pyjson.Object) error {
	text := string(pyjson.DumpsIndent(sortedObject(payload), 2)) + "\n"
	return os.WriteFile(path, textfile.Encode(text), 0o644)
}

// Start runs the periodic loop (BlackBox.start).
func (b *BlackBox) Start() {
	if !b.Enabled {
		return
	}
	b.once.Do(func() { go b.loop() })
}

// Stop ends the loop.
func (b *BlackBox) Stop() {
	select {
	case <-b.stop:
	default:
		close(b.stop)
	}
}

func (b *BlackBox) loop() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-b.stop:
			return
		case <-t.C:
		}
		if !b.Enabled || b.Current == nil {
			continue
		}
		p := b.Current()
		if p == nil {
			continue
		}
		ctx := context.Background()
		revision, err := p.Revision(ctx)
		if err != nil {
			continue
		}
		b.mu.Lock()
		last, lastAt := b.lastRevision[p.ID()], b.lastAt[p.ID()]
		b.mu.Unlock()
		if revision == last || unixNow()-lastAt < b.Interval {
			continue
		}
		b.Snapshot(ctx, p, "periodic", false) //nolint:errcheck // best effort
	}
}

func (b *BlackBox) projectRoot(p Project) string {
	path := filepath.Join(b.Root, SafeID(p.ID()))
	os.MkdirAll(path, 0o700) //nolint:errcheck
	os.Chmod(path, 0o700)    //nolint:errcheck
	return path
}

func (b *BlackBox) snapshotDirs(p Project) []string {
	root := b.projectRoot(p)
	entries, _ := os.ReadDir(root)
	var out []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if fi, err := os.Stat(filepath.Join(root, e.Name(), "manifest.json")); err == nil && fi.Mode().IsRegular() {
			out = append(out, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

func readManifest(path string) (*pyjson.Object, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	v, err := pyjson.Loads(strings.ReplaceAll(string(raw), "\r\n", "\n"))
	if err != nil {
		return nil, false
	}
	o, ok := v.(*pyjson.Object)
	return o, ok
}

// List is BlackBox.list.
func (b *BlackBox) List(p Project) []any {
	out := []any{}
	for _, name := range b.snapshotDirs(p) {
		m, ok := readManifest(filepath.Join(b.Root, SafeID(p.ID()), name, "manifest.json"))
		if !ok {
			continue
		}
		out = append(out, m.Set("id", name))
	}
	return out
}

// Get is BlackBox.get.
func (b *BlackBox) Get(p Project, snapshotID string) (*pyjson.Object, bool) {
	safe := SafeID(snapshotID)
	path := filepath.Join(b.projectRoot(p), safe, "manifest.json")
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return nil, false
	}
	m, ok := readManifest(path)
	if !ok {
		return nil, false
	}
	return m.Set("id", safe), true
}

func randHex(n int) string {
	buf := make([]byte, (n+1)/2)
	rand.Read(buf) //nolint:errcheck
	return hex.EncodeToString(buf)[:n]
}

// onlineBackup is sqlite3.Connection.backup into a new file plus the checks
// BlackBox.snapshot runs on the copy.
func onlineBackup(ctx context.Context, src, dst string) (integrity string, units, links int64, err error) {
	if err := sqlitedb.BackupFile(ctx, src, dst); err != nil {
		return "", 0, 0, err
	}
	check, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dst))
	if err != nil {
		return "", 0, 0, err
	}
	defer check.Close()
	if err := check.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return "", 0, 0, err
	}
	if err := check.QueryRowContext(ctx, "SELECT COUNT(*) FROM units").Scan(&units); err != nil {
		return "", 0, 0, err
	}
	if err := check.QueryRowContext(ctx, "SELECT COUNT(*) FROM unit_links").Scan(&links); err != nil {
		return "", 0, 0, err
	}
	return integrity, units, links, nil
}

func gzipFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	zw, _ := gzip.NewWriterLevel(out, 6)
	zw.Name = filepath.Base(src)
	if _, err := io.Copy(zw, in); err != nil {
		zw.Close()
		out.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func copyFileWithTimes(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return err
	}
	return os.Chtimes(dst, info.ModTime(), info.ModTime())
}

// Snapshot is BlackBox.snapshot.
func (b *BlackBox) Snapshot(ctx context.Context, p Project, reason string, force bool) (*pyjson.Object, error) {
	if !b.Enabled {
		return nil, &ValueError{Message: "black box is disabled"}
	}
	dbPath := filepath.Join(p.Folder(), "ark.sqlite3")
	if fi, err := os.Stat(dbPath); err != nil || !fi.Mode().IsRegular() {
		return nil, &OSError{"project database does not exist"}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	revision, err := p.Revision(ctx)
	if err != nil {
		return nil, err
	}
	if !force && reason == "periodic" && revision == b.lastRevision[p.ID()] {
		if list := b.List(p); len(list) > 0 {
			return list[0].(*pyjson.Object), nil
		}
	}
	projectRoot := b.projectRoot(p)
	base := stamp() + "-" + SafeID(reason)
	target := filepath.Join(projectRoot, base)
	if _, err := os.Stat(target); err == nil {
		target = filepath.Join(projectRoot, base+"-"+randHex(8))
	}
	temp := filepath.Join(projectRoot, ".tmp-"+randHex(32))
	if err := os.MkdirAll(temp, 0o755); err != nil {
		return nil, err
	}
	manifest, err := func() (*pyjson.Object, error) {
		raw := filepath.Join(temp, "ark.sqlite3")
		integrity, units, links, err := onlineBackup(ctx, dbPath, raw)
		if err != nil {
			return nil, err
		}
		if strings.ToLower(integrity) != "ok" {
			return nil, &ValueError{Message: "snapshot integrity check failed: " + integrity}
		}
		rawSHA, err := sha256File(raw)
		if err != nil {
			return nil, err
		}
		fi, _ := os.Stat(raw)
		rawSize := fi.Size()
		packed := raw + ".gz"
		if err := gzipFile(raw, packed); err != nil {
			return nil, err
		}
		if err := os.Remove(raw); err != nil {
			return nil, err
		}
		packedSHA, err := sha256File(packed)
		if err != nil {
			return nil, err
		}
		pfi, _ := os.Stat(packed)
		if fi, err := os.Stat(filepath.Join(p.Folder(), "project.json")); err == nil && fi.Mode().IsRegular() {
			if err := copyFileWithTimes(filepath.Join(p.Folder(), "project.json"), filepath.Join(temp, "project.json")); err != nil {
				return nil, err
			}
		}
		assets := []any{}
		assetsDir := filepath.Join(p.Folder(), "assets")
		if fi, err := os.Stat(assetsDir); err == nil && fi.IsDir() {
			var files []string
			filepath.WalkDir(assetsDir, func(path string, d fs.DirEntry, err error) error { //nolint:errcheck
				if err == nil && d.Type().IsRegular() {
					files = append(files, path)
				}
				return nil
			})
			SortPaths(files)
			for _, f := range files {
				rel, _ := filepath.Rel(p.Folder(), f)
				info, _ := os.Stat(f)
				sum, err := sha256File(f)
				if err != nil {
					return nil, err
				}
				assets = append(assets, pyjson.NewObject("path", filepath.ToSlash(rel), "size", info.Size(), "sha256", sum))
			}
		}
		if err := writeJSON(filepath.Join(temp, "assets.json"), pyjson.NewObject("assets", assets, "count", int64(len(assets)))); err != nil {
			return nil, err
		}
		manifest := pyjson.NewObject("format", "ark-blackbox-snapshot", "version", int64(1), "reason", SafeID(reason),
			"created_at", utc(), "project_id", p.ID(), "project_name", p.Name(), "project_path", p.Folder(),
			"revision", revision, "db_sha256", rawSHA, "db_size", rawSize, "snapshot_sha256", packedSHA,
			"snapshot_size", pfi.Size(), "unit_count", units, "link_count", links, "asset_count", int64(len(assets)),
			"integrity", "ok", "ark_version", "2.0")
		if err := writeJSON(filepath.Join(temp, "manifest.json"), manifest); err != nil {
			return nil, err
		}
		if err := os.Rename(temp, target); err != nil {
			return nil, err
		}
		return manifest, nil
	}()
	if err != nil {
		os.RemoveAll(temp) //nolint:errcheck
		return nil, err
	}
	b.lastRevision[p.ID()] = revision
	b.lastAt[p.ID()] = unixNow()
	b.prune(p)
	return manifest.Set("id", filepath.Base(target)), nil
}

// prune is BlackBox.prune.
func (b *BlackBox) prune(p Project) {
	list := b.List(p)
	if len(list) == 0 {
		return
	}
	keep := map[string]bool{}
	for i, item := range list {
		if i < 12 {
			keep[pyjson.Str(item.(*pyjson.Object).Value("id"))] = true
		}
	}
	n := 0
	for _, item := range list {
		o := item.(*pyjson.Object)
		if o.Value("reason") == "pre-agent" && n < 20 {
			keep[pyjson.Str(o.Value("id"))] = true
			n++
		}
	}
	var hourlyKeys, dailyKeys []string
	hourly, daily := map[string]string{}, map[string]string{}
	for _, item := range list {
		o := item.(*pyjson.Object)
		created := ""
		if v := o.Value("created_at"); pyjson.Truthy(v) {
			created = pyjson.Str(v)
		}
		id := pyjson.Str(o.Value("id"))
		if pytext.Len(created) >= 13 {
			k := pytext.Prefix(created, 13)
			if _, ok := hourly[k]; !ok {
				hourly[k] = id
				hourlyKeys = append(hourlyKeys, k)
			}
		}
		if pytext.Len(created) >= 10 {
			k := pytext.Prefix(created, 10)
			if _, ok := daily[k]; !ok {
				daily[k] = id
				dailyKeys = append(dailyKeys, k)
			}
		}
	}
	for i, k := range hourlyKeys {
		if i < 24 {
			keep[hourly[k]] = true
		}
	}
	for i, k := range dailyKeys {
		if i < 14 {
			keep[daily[k]] = true
		}
	}
	for _, item := range list {
		id := pyjson.Str(item.(*pyjson.Object).Value("id"))
		if !keep[id] {
			os.RemoveAll(filepath.Join(b.projectRoot(p), id)) //nolint:errcheck
		}
	}
}

// Restore is BlackBox.restore.
func (b *BlackBox) Restore(ctx context.Context, p Project, snapshotID string) (*pyjson.Object, error) {
	if !b.Enabled {
		return nil, &ValueError{Message: "black box is disabled"}
	}
	manifest, ok := b.Get(p, snapshotID)
	if !ok {
		return nil, &OSError{"snapshot not found"}
	}
	folder := filepath.Join(b.projectRoot(p), SafeID(snapshotID))
	archive := filepath.Join(folder, "ark.sqlite3.gz")
	if fi, err := os.Stat(archive); err != nil || !fi.Mode().IsRegular() {
		return nil, &OSError{"snapshot database is missing"}
	}
	temp := filepath.Join(p.Folder(), ".restore-"+randHex(32)+".sqlite3")
	defer os.Remove(temp)
	if err := gunzip(archive, temp); err != nil {
		return nil, err
	}
	check, err := sql.Open("sqlite", "file:"+filepath.ToSlash(temp))
	if err != nil {
		return nil, err
	}
	var result string
	err = check.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result)
	check.Close()
	if err != nil {
		return nil, err
	}
	if strings.ToLower(result) != "ok" {
		return nil, &ValueError{Message: "snapshot integrity check failed: " + result}
	}
	if _, err := b.Snapshot(ctx, p, "pre-restore", true); err != nil {
		return nil, err
	}
	err = p.Reopen(func() error {
		for _, suffix := range []string{"-wal", "-shm"} {
			if err := os.Remove(filepath.Join(p.Folder(), "ark.sqlite3"+suffix)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		return os.Rename(temp, filepath.Join(p.Folder(), "ark.sqlite3"))
	})
	if err != nil {
		return nil, err
	}
	revision, err := p.Revision(ctx)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.lastRevision[p.ID()] = revision
	b.lastAt[p.ID()] = unixNow()
	b.mu.Unlock()
	return pyjson.NewObject("snapshot", manifest, "revision", revision), nil
}

func gunzip(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	zr, err := gzip.NewReader(in)
	if err != nil {
		return fmt.Errorf("snapshot archive: %w", err)
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, zr); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// SortPaths orders paths like sorted() of pathlib.Path objects: component by
// component, case-insensitively on Windows (PureWindowsPath comparison).
func SortPaths(paths []string) {
	key := func(p string) []string {
		parts := strings.FieldsFunc(filepath.ToSlash(p), func(r rune) bool { return r == '/' })
		if os.PathSeparator == 0x5c {
			for i, part := range parts {
				parts[i] = strings.ToLower(part)
			}
		}
		return parts
	}
	sort.SliceStable(paths, func(i, j int) bool {
		a, b := key(paths[i]), key(paths[j])
		for k := 0; k < len(a) && k < len(b); k++ {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return len(a) < len(b)
	})
}
