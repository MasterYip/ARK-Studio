package sessionwatch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type outcome struct {
	Code  int        `json:"code"`
	Logs  []string   `json:"logs"`
	Crash string     `json:"crash"`
	Reqs  []recorded `json:"-"`
}

func lookup(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := env[k]; return v, ok }
}

func freshDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

// play runs every invocation of sc with invoke and returns the outcomes.
func play(t *testing.T, sc scenario, dir string, srv *fakeARK,
	invoke func(o Options, env map[string]string, home string) outcome) []outcome {
	t.Helper()
	freshDir(t, dir)
	srv.reset()
	o, env, home := sc.setup(t, dir, srv)
	var outs []outcome
	for i := 0; i <= len(sc.between); i++ {
		before := len(srv.requests())
		out := invoke(o, env, home)
		out.Reqs = srv.requests()[before:]
		outs = append(outs, out)
		if i < len(sc.between) {
			sc.between[i](t, dir, srv)
		}
	}
	return outs
}

func goInvoke(o Options, env map[string]string, home string) outcome {
	var out outcome
	var stderr bytes.Buffer
	old := Stderr
	Stderr = &stderr
	defer func() { Stderr = old }()
	out.Code = RunContext(context.Background(), o, lookup(env), home, func(s string) { out.Logs = append(out.Logs, s) })
	if s := stderr.String(); s != "" {
		lines := strings.Split(strings.TrimSpace(s), "\n")
		typ, _, _ := strings.Cut(lines[len(lines)-1], ":")
		out.Crash = typ[strings.LastIndex(typ, ".")+1:]
	}
	return out
}

func init() {
	sleep = func(ctx context.Context, seconds float64) bool { panic("unexpected sleep") }
}

func setupNoProxy(t *testing.T) {
	t.Setenv("NO_PROXY", "*")
}

func runScenario(t *testing.T, name string) []outcome {
	t.Helper()
	setupNoProxy(t)
	for _, sc := range scenarios() {
		if sc.name == name {
			return play(t, sc, filepath.Join(t.TempDir(), "w"), newFakeARK(t), goInvoke)
		}
	}
	t.Fatalf("no scenario %s", name)
	return nil
}

// ------------------------------------------------------------ Go-only checks

func TestOnceUploadWire(t *testing.T) {
	outs := runScenario(t, "once_upload")
	out := outs[0]
	if out.Code != 0 || len(out.Reqs) != 2 {
		t.Fatalf("code=%d reqs=%d logs=%v", out.Code, len(out.Reqs), out.Logs)
	}
	get, post := out.Reqs[0], out.Reqs[1]
	host := get.header("Host")
	wantGet := "GET /api/agent/v1/sessions/" + sid + " HTTP/1.1\r\n" +
		"Accept-Encoding: identity\r\n" +
		"Host: " + host + "\r\n" +
		"User-Agent: Python-urllib/3.10\r\n" +
		"Authorization: Bearer arka_test_token\r\n" +
		"Accept: application/json\r\n" +
		"X-Agent-Name: session-watch\r\n" +
		"X-Ark-Project: proj-1\r\n" +
		"Connection: close"
	if get.Head != wantGet {
		t.Errorf("GET head:\n%s\nwant:\n%s", get.Head, wantGet)
	}
	content := strings.Join(claudeLines[:2], "\n") + "\n"
	body := fmt.Sprintf(`{"offset": 0, "length": %d, "content": %s, "sha256": "%s", "mode": "append", "title": "My run \u00fc", "agent": "claude"}`,
		len(content), mustJSON(content), sha(content))
	body = strings.Replace(body, "ü", `\u00fc`, 1)
	if post.Body != body {
		t.Errorf("POST body:\n%s\nwant:\n%s", post.Body, body)
	}
	wantPost := "POST /api/agent/v1/sessions/" + sid + "/upload HTTP/1.1\r\n" +
		"Accept-Encoding: identity\r\n" +
		fmt.Sprintf("Content-Length: %d\r\n", len(body)) +
		"Host: " + host + "\r\n" +
		"User-Agent: Python-urllib/3.10\r\n" +
		"Authorization: Bearer arka_test_token\r\n" +
		"Accept: application/json\r\n" +
		"X-Agent-Name: session-watch\r\n" +
		"X-Ark-Project: proj-1\r\n" +
		"Content-Type: application/json\r\n" +
		"Connection: close"
	if post.Head != wantPost {
		t.Errorf("POST head:\n%s\nwant:\n%s", post.Head, wantPost)
	}
	if want := []string{fmt.Sprintf("uploaded %d bytes (server has %d)", len(content), len(content))}; !reflect.DeepEqual(out.Logs, want) {
		t.Errorf("logs %q want %q", out.Logs, want)
	}
}

func mustJSON(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSpace(b.String())
}

func sha(s string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(s)))
}

func TestIncrementalAndShrink(t *testing.T) {
	outs := runScenario(t, "incremental_append")
	if len(outs[1].Reqs) != 1 || outs[1].Logs != nil {
		t.Fatalf("second pass should only GET: %+v", outs[1])
	}
	last := outs[2].Reqs
	if len(last) != 2 {
		t.Fatalf("third pass: %+v", outs[2])
	}
	b := last[1].bodyObject(t)
	if b.Value("mode") != "append" || b.Value("offset").(int64) == 0 {
		t.Errorf("incremental body %s", last[1].Body)
	}

	outs = runScenario(t, "shrink_replace")
	second := outs[1]
	if len(second.Reqs) != 2 {
		t.Fatalf("shrink: %+v", second)
	}
	b = second.Reqs[1].bodyObject(t)
	if b.Value("mode") != "replace" || b.Value("offset").(int64) != 0 {
		t.Errorf("shrink body %s", second.Reqs[1].Body)
	}
	if !strings.Contains(second.Logs[0], "shrank (") || !strings.HasSuffix(second.Logs[0], "bytes); re-uploading it") {
		t.Errorf("shrink log %q", second.Logs)
	}
}

func TestExitCodes(t *testing.T) {
	cases := map[string]int{
		"token_missing": 2, "token_blank_custom_env": 2, "not_found": ExitNotFound, "agent_other_not_found": ExitNotFound,
		"disabled_project": ExitDisabled, "status_401": ExitDisabled, "status_403_no_body": ExitDisabled,
		"upload_403_disabled": ExitDisabled, "upload_500_json": 0, "connection_refused": 0, "status_not_a_dict": 1,
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			out := runScenario(t, name)[0]
			if out.Code != want {
				t.Errorf("code %d want %d (logs %q)", out.Code, want, out.Logs)
			}
		})
	}
	out := runScenario(t, "disabled_project")[0]
	want := []string{"ARK refused the upload (SESSION_UPLOADS_DISABLED): this ARK project does not accept session uploads",
		"Ask a project admin to turn on session uploads in Execute > Runs > Session context."}
	if !reflect.DeepEqual(out.Logs, want) {
		t.Errorf("disabled logs %q", out.Logs)
	}
	if out := runScenario(t, "token_missing")[0]; out.Logs[0] != "set ARK_TOKEN to your ARK agent token first (it is never read from the command line)" || len(out.Reqs) != 0 {
		t.Errorf("token logs %q", out.Logs)
	}
	if out := runScenario(t, "status_403_no_body")[0]; out.Logs[0] != "ARK refused the upload (403): Forbidden" {
		t.Errorf("403 logs %q", out.Logs)
	}
	if out := runScenario(t, "upload_500_json")[0]; out.Logs[0] != "upload failed (500 INTERNAL): boom; retrying later" {
		t.Errorf("500 logs %q", out.Logs)
	}
}

func TestChunkingMarker(t *testing.T) {
	out := runScenario(t, "chunking_and_marker")[0]
	if len(out.Reqs) < 4 {
		t.Fatalf("expected several uploads: %d", len(out.Reqs))
	}
	found := false
	for _, r := range out.Reqs[1:] {
		b := r.bodyObject(t)
		if c := b.Value("content").(string); strings.HasPrefix(c, `{"type": "ark_truncated", "bytes": `) {
			found = true
		} else if len(c) > 300 {
			t.Errorf("chunk over limit: %d", len(c))
		}
	}
	if !found {
		t.Error("no ark_truncated marker")
	}
}

func TestSplitChunksPythonIndexing(t *testing.T) {
	data := []byte("aa\nbbbb\ncc\npartial")
	var got []chunk
	splitChunks(data, 0, 4, func(c chunk) bool { got = append(got, c); return true })
	want := []chunk{{0, 3, []byte("aa\n")}, {3, 5, []byte(`{"type": "ark_truncated", "bytes": 4}` + "\n")}, {8, 3, []byte("cc\n")}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
}

func TestIntervalLoopAndInterrupt(t *testing.T) {
	setupNoProxy(t)
	srv := newFakeARK(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "s.jsonl")
	write(t, file, jsonl(claudeLines[0]))
	o := baseOpts(srv, file)
	o.Once, o.Interval = false, 3
	ctx, cancel := context.WithCancel(context.Background())
	var sleeps []float64
	old := sleep
	sleep = func(ctx context.Context, s float64) bool {
		sleeps = append(sleeps, s)
		if len(sleeps) == 2 {
			cancel() // Ctrl+C while sleeping
			return false
		}
		appendFile(t, file, jsonl(claudeLines[1]))
		return true
	}
	defer func() { sleep = old }()
	code := RunContext(ctx, o, lookup(tokenEnv()), "", func(string) {})
	if code != 0 || !reflect.DeepEqual(sleeps, []float64{10, 10}) {
		t.Fatalf("code %d sleeps %v", code, sleeps)
	}
	if n := len(srv.requests()); n != 4 {
		t.Errorf("requests %d, want 4", n)
	}

	// Ctrl+C during a request: the server never answers.
	srv.reset()
	srv.hook = func(int, recorded) string { time.Sleep(2 * time.Second); return "" }
	ctx2, cancel2 := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel2()
	start := time.Now()
	if code := RunContext(ctx2, baseOpts(srv, file), lookup(tokenEnv()), "", func(string) {}); code != 0 {
		t.Errorf("interrupted code %d", code)
	}
	if time.Since(start) > time.Second {
		t.Error("interrupt did not abort the request")
	}
}

func TestPathlibStr(t *testing.T) {
	if !onWindows {
		cases := map[string]string{"/a//b/./c/": "/a/b/c", "a/../b": "a/../b", "": ".", "//x/y": "//x/y", "///x": "/x"}
		for in, want := range cases {
			if got := newPath(in).String(); got != want {
				t.Errorf("%q -> %q want %q", in, got, want)
			}
		}
		return
	}
	cases := map[string]string{
		"C:/a//b/./c/": `C:\a\b\c`, `a\..\b`: `a\..\b`, "": ".", `\\srv\share\x`: `\\srv\share\x`,
		"c:": "c:", "C:rel/x": `C:rel\x`, "/x/y": `\x\y`, `\\?\C:\x`: `\\?\C:\x`,
	}
	for in, want := range cases {
		if got := newPath(in).String(); got != want {
			t.Errorf("%q -> %q want %q", in, got, want)
		}
	}
	if s := newPath("x/a.jsonl.zstd").suffix(); s != ".zstd" {
		t.Errorf("suffix %q", s)
	}
}
