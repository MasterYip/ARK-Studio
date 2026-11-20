package store

// T-0028: the Assistant thread is strictly per user and project; the
// pre-v10 rows (empty user) go to the project owner once, or stay the
// local user's in local single-user mode.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/sqlitedb"
)

func chatStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir(), NewManifest(pyjson.NewObject("id", "prj_chat", "name", "Chat")), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// legacyChat writes rows like a pre-v10 server (empty user).
func legacyChat(t *testing.T, st *Store, texts ...string) {
	t.Helper()
	for _, text := range texts {
		if err := st.DB.Write(context.Background(), func(q sqlitedb.Querier) error {
			_, err := q.ExecContext(context.Background(),
				"INSERT INTO chat_messages(at,role,content,tools_json,meta_json,user) VALUES('t','user',?,'[]','{}','')", text)
			return err
		}, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func addChat(t *testing.T, st *Store, user string, texts ...string) {
	t.Helper()
	for _, text := range texts {
		if _, err := st.AddChat(context.Background(), "user", text, nil, nil, user); err != nil {
			t.Fatal(err)
		}
	}
}

func thread(t *testing.T, st *Store, user string, legacy bool) string {
	t.Helper()
	rows, err := st.ChatHistory(context.Background(), 400, &user, legacy)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, pyjson.Str(r.Value("content")))
	}
	return strings.Join(out, ",")
}

func TestChatHistoryIsPerUser(t *testing.T) {
	st := chatStore(t)
	legacyChat(t, st, "old1", "old2")
	addChat(t, st, "alice", "a1")
	addChat(t, st, "bob", "b1")
	addChat(t, st, "alice", "a2")
	addChat(t, st, "local", "l1")
	for user, want := range map[string]string{"alice": "a1,a2", "bob": "b1", "carol": "", "": ""} {
		if got := thread(t, st, user, false); got != want {
			t.Errorf("%q: %q, want %q", user, got, want)
		}
	}
	// Local single-user mode: the pre-v10 rows are the local user's.
	if got := thread(t, st, "local", true); got != "old1,old2,l1" {
		t.Errorf("local mode: %q", got)
	}
	// user nil is the whole table (no route reads it).
	all, err := st.ChatHistory(context.Background(), 400, nil, false)
	if err != nil || len(all) != 6 {
		t.Errorf("all rows: %d %v", len(all), err)
	}
}

func TestClearChatOnlyOwnThread(t *testing.T) {
	ctx := context.Background()
	st := chatStore(t)
	legacyChat(t, st, "old1")
	addChat(t, st, "alice", "a1", "a2")
	addChat(t, st, "bob", "b1")
	// An empty user name (no account) owns nothing outside local mode.
	if n, err := st.ClearChat(ctx, "", false); err != nil || n != 0 {
		t.Fatalf("anonymous clear: %d %v", n, err)
	}
	n, err := st.ClearChat(ctx, "alice", false)
	if err != nil || n != 2 {
		t.Fatalf("alice clear: %d %v", n, err)
	}
	if got := thread(t, st, "alice", false); got != "" {
		t.Errorf("alice after clear: %q", got)
	}
	if got := thread(t, st, "bob", false); got != "b1" {
		t.Errorf("bob after alice's clear: %q", got)
	}
	if got := thread(t, st, "local", true); got != "old1" {
		t.Errorf("legacy rows after alice's clear: %q", got)
	}
	addChat(t, st, "alice", "a3")
	if got := thread(t, st, "alice", false); got != "a3" {
		t.Errorf("alice after a new message: %q", got)
	}

	// Local mode: the clear counts and hides the pre-v10 rows too.
	addChat(t, st, "local", "l1")
	if n, err := st.ClearChat(ctx, "local", true); err != nil || n != 2 {
		t.Fatalf("local clear: %d %v", n, err)
	}
	if got := thread(t, st, "local", true); got != "" {
		t.Errorf("local after clear: %q", got)
	}
	if got := thread(t, st, "bob", false); got != "b1" {
		t.Errorf("bob after local clear: %q", got)
	}
}

func TestAdoptLegacyChat(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	manifest := NewManifest(pyjson.NewObject("id", "prj_chat", "name", "Chat"))
	st, err := Open(dir, manifest, 4)
	if err != nil {
		t.Fatal(err)
	}
	legacyChat(t, st, "old1", "old2", "old3")
	addChat(t, st, "owner", "o1")
	addChat(t, st, "ana", "m1")
	// The owner had cleared their view after old1 (a pre-T-0028 clear).
	if err := st.DB.Write(ctx, func(q sqlitedb.Querier) error {
		return MetaSet(ctx, q, chatClearedKey("owner"), "1")
	}, nil); err != nil {
		t.Fatal(err)
	}

	// No owner known: nothing changes and the flag stays unset.
	if n, err := st.AdoptLegacyChat(ctx, func() (string, error) { return " ", nil }); err != nil || n != 0 {
		t.Fatalf("no owner: %d %v", n, err)
	}
	// Owner lookups that fail surface the error (on a fresh store).
	st.Close()
	if st, err = Open(dir, manifest, 4); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("org down")
	if _, err := st.AdoptLegacyChat(ctx, func() (string, error) { return "", boom }); !errors.Is(err, boom) {
		t.Fatalf("lookup error: %v", err)
	}
	if got := thread(t, st, "local", true); got != "old1,old2,old3" {
		t.Fatalf("legacy rows before adoption: %q", got)
	}

	n, err := st.AdoptLegacyChat(ctx, func() (string, error) { return "owner", nil })
	if err != nil || n != 3 {
		t.Fatalf("adopt: %d %v", n, err)
	}
	// The owner's earlier clear still hides old1; members see only their own.
	if got := thread(t, st, "owner", false); got != "old2,old3,o1" {
		t.Errorf("owner thread: %q", got)
	}
	if got := thread(t, st, "ana", false); got != "m1" {
		t.Errorf("member thread: %q", got)
	}

	// Idempotent: the meta key makes later calls (and later stores) no-ops.
	called := false
	again := func() (string, error) { called = true; return "someone", nil }
	if n, err := st.AdoptLegacyChat(ctx, again); err != nil || n != 0 || called {
		t.Fatalf("second call: %d %v called=%v", n, err, called)
	}
	st.Close()
	if st, err = Open(dir, manifest, 4); err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	legacyChat(t, st, "late") // e.g. written by an old server afterwards
	if n, err := st.AdoptLegacyChat(ctx, again); err != nil || n != 0 || called {
		t.Fatalf("after reopen: %d %v called=%v", n, err, called)
	}
	var owner string
	if err := st.DB.Read(ctx, func(q sqlitedb.Querier) error {
		var err error
		owner, _, err = MetaGet(ctx, q, LegacyChatOwner)
		return err
	}); err != nil || owner != "owner" {
		t.Fatalf("meta %s: %q %v", LegacyChatOwner, owner, err)
	}
	if got := thread(t, st, "owner", false); got != "old2,old3,o1" {
		t.Errorf("owner thread after reopen: %q", got)
	}
}
