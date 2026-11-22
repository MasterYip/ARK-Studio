package sessions

import (
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

func TestForkArgvPerProvider(t *testing.T) {
	cases := []struct {
		platform string
		argv     []string
		prompt   string
		want     string
		ok       bool
	}{
		{"claude", []string{"claude", "-p", "--input-format", "stream-json"}, "", "claude --resume N1 --fork-session -p --input-format stream-json", true},
		{"claude", []string{"claude", "--resume", "X", "-p"}, "", "", false},
		{"codex", []string{"codex", "exec", "--skip-git-repo-check", "go on"}, "go on", "codex exec --skip-git-repo-check fork N1 go on", true},
		{"codex", []string{"codex", "exec", "resume", "X", "p"}, "p", "", false},
		{"dsh", []string{"dsh", "headless", "-"}, "", "", false},
	}
	for _, c := range cases {
		got, ok := forkArgv(c.argv, c.platform, "N1", c.prompt)
		if ok != c.ok || (ok && strings.Join(got, " ") != c.want) {
			t.Errorf("%s %v: got %v %v, want %q %v", c.platform, c.argv, got, ok, c.want, c.ok)
		}
	}
}

func TestForkSessionNativeAndSeeded(t *testing.T) {
	m := streamManager(t)
	parent, err := m.Start(nil, pyjson.NewObject("platform", "claude", "prompt", "hello"), "http://127.0.0.1:1", "alice")
	if err != nil {
		t.Fatal(err)
	}
	defer killSession(m, parent)
	waitFor(t, "native id", func() bool { return metaOf(m, parent, "native_session_id") == "fake-native-0001" })
	// Native: no prompt needed; the new process forks the parent's conversation.
	fork, err := m.ForkSession(nil, parent.ID, pyjson.NewObject("mode", "fork"), "http://127.0.0.1:1", "alice")
	if err != nil {
		t.Fatal(err)
	}
	defer killSession(m, fork)
	if metaOf(m, fork, "fork_method") != "native" || metaOf(m, fork, "forked_from") != parent.ID {
		t.Fatalf("native fork meta: %v %v", metaOf(m, fork, "fork_method"), metaOf(m, fork, "forked_from"))
	}
	if metaOf(m, fork, "forked_native_from") != "fake-native-0001" {
		t.Fatalf("launched with --resume <parent> --fork-session: %v", metaOf(m, fork, "forked_native_from"))
	}
	if !strings.HasPrefix(strOr(metaOf(m, fork, "title")), "Fork · ") {
		t.Errorf("fork title: %v", metaOf(m, fork, "title"))
	}
	// Seeded: a parent without a native id gets the summary context.
	m2 := streamManager(t)
	t.Setenv("ARK_CLAUDE_STREAM", "0")
	p2, err := m2.Start(nil, pyjson.NewObject("platform", "claude", "prompt", ""), "http://127.0.0.1:1", "bob")
	if err != nil {
		t.Fatal(err)
	}
	f2, err := m2.ForkSession(nil, p2.ID, pyjson.NewObject("mode", "fork"), "http://127.0.0.1:1", "bob")
	if err != nil {
		t.Fatal(err)
	}
	defer killSession(m2, f2)
	if metaOf(m2, f2, "fork_method") != "seeded" || !strings.Contains(strOr(metaOf(m2, f2, "prompt")), "Side chat context") {
		t.Fatalf("seeded fork: %v", metaOf(m2, f2, "fork_method"))
	}
	// A side chat still needs a prompt.
	if _, err := m2.ForkSession(nil, p2.ID, pyjson.NewObject("mode", "side_chat"), "http://127.0.0.1:1", "bob"); err == nil {
		t.Error("side chat without a prompt")
	}
}
