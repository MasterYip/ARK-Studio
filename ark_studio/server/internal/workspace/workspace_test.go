package workspace

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"arkstudio/go/internal/compat/pyjson"
)

func TestRelPartsRejects(t *testing.T) {
	cases := map[string]int{
		"../x": 400, "/abs": 400, "C:/x": 400, "a\x00b": 400, ".git/config": 403,
		"node_modules": 403, "x.pyc": 403, "ark.sqlite3": 403,
	}
	for in, status := range cases {
		_, err := relParts(in)
		we, ok := err.(*Error)
		if !ok || we.Status != status {
			t.Fatalf("%q: %v, want status %d", in, err, status)
		}
	}
	parts, err := relParts(` .\src\.\main.py `)
	if err != nil || strings.Join(parts, "/") != "src/main.py" {
		t.Fatalf("got %v %v", parts, err)
	}
}

func TestResolveRejectsLinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if _, _, err := resolve(root, "link/x", false); err == nil || err.(*Error).Status != 403 {
		t.Fatalf("symlink component accepted: %v", err)
	}
}

func TestCapTextMatchesPython(t *testing.T) {
	// The cut compares a code-point index with half the byte limit.
	text := strings.Repeat("中", 30) + "\n" + strings.Repeat("x", 100)
	out, truncated := capText(text, 64)
	if !truncated || out != strings.Repeat("中", 21)+"\n…[diff truncated by ARK]\n" {
		t.Fatalf("%v %q", truncated, out)
	}
}

func TestParsersHandleRenames(t *testing.T) {
	num := numstatEntries([]byte("1\t2\tsrc/a.py\x00-\t-\tbin\x003\t0\t\x00old.txt\x00new.txt\x00"))
	if *num["src/a.py"].adds != 1 || !num["bin"].binary || *num["new.txt"].adds != 3 {
		t.Fatalf("%+v", num)
	}
	names := nameStatusEntries([]byte("M\x00src/a.py\x00R100\x00old.txt\x00new.txt\x00D\x00gone\x00"))
	want := []nameStatus{{"src/a.py", "M"}, {"new.txt", "R"}, {"gone", "D"}}
	if len(names) != len(want) {
		t.Fatalf("%+v", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("%+v", names)
		}
	}
}

func TestFileRoundTrip(t *testing.T) {
	root := t.TempDir()
	if _, err := WriteFile(root, pyjson.NewObject("path", "a.txt", "content", "x")); err == nil {
		t.Fatal("write without create succeeded")
	}
	out, err := WriteFile(root, pyjson.NewObject("path", "a.txt", "content", "x", "create", true))
	if err != nil || out.Value("created") != true {
		t.Fatal(out, err)
	}
	read, err := ReadFile(root, "a.txt")
	if err != nil || read.Value("content") != "x" || read.Value("hash") != "442a44457137672b3218c1007dc8f76a" {
		t.Fatal(read, err)
	}
	if _, err := WriteFile(root, pyjson.NewObject("path", "a.txt", "content", "y", "base_hash", "nope")); err == nil {
		t.Fatal("stale hash accepted")
	}
	if _, err := WriteFile(root, pyjson.NewObject("path", "a.txt", "content", "y", "base_hash", read.Value("hash"))); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalRunsAndCancels(t *testing.T) {
	m := NewManager()
	defer m.Shutdown()
	root := t.TempDir()
	term, _, err := m.Start(root, StartOptions{Command: "echo hello", Timeout: int64(10)})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, term)
	snap := term.Snapshot(0, 100)
	if snap.Value("status") != "exited" || snap.Value("exit_code") != int64(0) {
		t.Fatal(pyjson.Dumps(snap))
	}
	var out strings.Builder
	for _, e := range snap.Value("entries").([]any) {
		out.WriteString(e.(*pyjson.Object).Value("text").(string))
	}
	if strings.TrimSpace(out.String()) != "hello" {
		t.Fatalf("%q", out.String())
	}
	sleeper := "sleep 30"
	if runtime.GOOS == "windows" {
		sleeper = "ping -n 30 127.0.0.1 >nul"
	}
	long, _, err := m.Start(root, StartOptions{Command: sleeper})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Cancel(long.ID); err != nil {
		t.Fatal(err)
	}
	waitDone(t, long)
	if long.Status() != "cancelled" {
		t.Fatal(long.Status())
	}
}

func waitDone(t *testing.T, term *Terminal) {
	deadline := time.Now().Add(20 * time.Second)
	for term.running() {
		if time.Now().After(deadline) {
			t.Fatal("terminal did not finish")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
