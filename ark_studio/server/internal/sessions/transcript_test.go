package sessions

import (
	"os"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

// TestTranscriptVectors compares BuildTranscript with the recorded
// transcripts (testdata, first from CPython's build_transcript via
// tests/python/contract/gen_transcript_vectors.py).  The Go server owns the
// behaviour now: Codex JSON events are mapped by transcript_codex.go since
// 2026-11-23.  ARK_TRANSCRIPT_VECTORS_OUT=<file> writes Go's outputs there
// (a JSON list) to review and merge.
func TestTranscriptVectors(t *testing.T) {
	var dumped []any
	raw, err := os.ReadFile("testdata/transcript_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	cases, err := pyjson.Loads(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range cases.([]any) {
		item := c.(*pyjson.Object)
		in := item.Value("input").(*pyjson.Object)
		var stdout, stderr []*pyjson.Object
		for _, e := range in.Value("stdout").([]any) {
			stdout = append(stdout, e.(*pyjson.Object))
		}
		for _, e := range in.Value("stderr").([]any) {
			stderr = append(stderr, e.(*pyjson.Object))
		}
		got := BuildTranscript(TranscriptInput{SessionID: pyjson.Str(in.Value("session_id")),
			Status: pyjson.Str(in.Value("status")), Prompt: strOr(in.Value("prompt")), PromptAt: in.Value("prompt_at"),
			Stdout: stdout, Stderr: stderr, Error: strOr(in.Value("error")), Notice: strOr(in.Value("notice")),
			Platform: strOr(in.Value("platform"))})
		got.Set("generated_at", "<GEN>")
		dumped = append(dumped, got)
		want := string(pyjson.Dumps(item.Value("output")))
		if g := string(pyjson.Dumps(got)); g != want {
			n := 0
			for n < len(g) && n < len(want) && g[n] == want[n] {
				n++
			}
			lo := max(0, n-80)
			t.Errorf("case %d (%s, %s) differs at %d:\n go: %s\n py: %s", i, in.Value("session_id"), in.Value("status"), n,
				g[lo:min(len(g), n+120)], want[lo:min(len(want), n+120)])
		}
	}
	if out := os.Getenv("ARK_TRANSCRIPT_VECTORS_OUT"); out != "" {
		_ = os.WriteFile(out, pyjson.Dumps(dumped), 0o644)
	}
}

func TestNativeIDAndResume(t *testing.T) {
	for line, want := range map[string]string{
		`{"type": "system", "session_id": "abcdefgh-1234"}`:      "abcdefgh-1234",
		`{"type": "thread.started", "thread_id": "th_12345678"}`: "th_12345678",
		"  Session ID:  0199-codex-abcdef  ":                     "0199-codex-abcdef",
		`{"type": "system", "session_id": "short"}`:              "",
		`{"session_id": "has space in it"}`:                      "",
	} {
		if got := NativeSessionIDFromLine(line); got != want {
			t.Errorf("%q: got %q want %q", line, got, want)
		}
	}
	got := resumeArgv([]string{"codex", "exec", "--json", "do it"}, "codex", "N1", "do it")
	if pyjson.Str(strList(got)) != "['codex', 'exec', '--json', 'resume', 'N1', 'do it']" {
		t.Errorf("codex resume argv %v", got)
	}
	got = resumeArgv([]string{"claude", "-p", "x"}, "claude", "N2", "x")
	if pyjson.Str(strList(got)) != "['claude', '--resume', 'N2', '-p', 'x']" {
		t.Errorf("claude resume argv %v", got)
	}
}
