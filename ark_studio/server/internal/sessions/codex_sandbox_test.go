package sessions

import (
	"strings"
	"testing"
)

func TestCodexSandboxArgs(t *testing.T) {
	newer, older := [3]int{0, 166, 0}, [3]int{0, 120, 3}
	join := func(a []string) string { return strings.Join(a, " ") }
	for _, c := range []struct {
		access string
		v      [3]int
		want   string
	}{
		{"read_only", newer, `permissions.ark_session.extends=":read-only"`},
		{"plan", newer, `permissions.ark_session.extends=":read-only"`},
		{"full_access", newer, `permissions.ark_session.extends=":workspace"`},
		{"accept_edits", newer, `permissions.ark_session.extends=":workspace"`},
		{"full_access", older, "-s workspace-write -c sandbox_workspace_write.network_access=true"},
		{"read_only", older, "-s read-only"},
	} {
		got := join(codexSandboxArgs(c.access, c.v))
		if !strings.Contains(got, c.want) {
			t.Fatalf("%s %v: %s", c.access, c.v, got)
		}
		if versionAtLeast(c.v, codexProfileSince) && !strings.Contains(got, "network.enabled=true") {
			t.Fatalf("%s: no network: %s", c.access, got)
		}
		if strings.Contains(got, "danger") {
			t.Fatalf("filesystem widened: %s", got)
		}
	}
}

func TestWithCodexSandbox(t *testing.T) {
	old := CodexVersion
	CodexVersion = func(string) [3]int { return [3]int{0, 166, 0} }
	defer func() { CodexVersion = old }()
	argv := withCodexSandbox([]string{"codex", "exec", "-m", "gpt", "--skip-git-repo-check", "do it"}, "full_access")
	if argv[1] != "exec" || argv[2] != "-c" || !strings.Contains(strings.Join(argv, " "), "default_permissions") || argv[len(argv)-1] != "do it" {
		t.Fatalf("%q", argv)
	}
	// A command that already chooses a sandbox is left alone.
	for _, own := range [][]string{
		{"codex", "exec", "-s", "danger-full-access", "x"},
		{"codex", "exec", "--dangerously-bypass-approvals-and-sandbox", "x"},
		{"codex", "exec", "-c", "sandbox_mode=\"read-only\"", "x"},
	} {
		if got := withCodexSandbox(own, "full_access"); len(got) != len(own) {
			t.Fatalf("changed %q -> %q", own, got)
		}
	}
	if got := withCodexSandbox([]string{"codex", "x"}, "full_access"); len(got) != 2 {
		t.Fatal("only exec runs get sandbox arguments")
	}
	if v := parseCodexVersion("codex-cli 0.166.0\n"); v != [3]int{0, 166, 0} {
		t.Fatal(v)
	}
}
