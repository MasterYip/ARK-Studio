package agentwatch

import (
	"testing"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/store"
)

func commentRow(w *store.UnitWriters, replyTo int64) store.UpdateRow {
	r := unitRow(10, "comment.create", "alice", detail("comment_id", int64(50)))
	r.Comment = &store.UpdateComment{ID: 50, Author: "alice", Kind: "comment", Body: "why lr 3e-4?", ReplyTo: replyTo}
	r.Writers = w
	return r
}

func routed(t *testing.T, v Viewer, r store.UpdateRow) *pyjson.Object {
	t.Helper()
	items := Items([]store.UpdateRow{r}, v, nil)
	if len(items) != 1 {
		t.Fatalf("items: %v", items)
	}
	return items[0]
}

// The writer of a unit is told apart by ARK session, else agent session id,
// else token and device; every viewer still gets the item.
func TestRoutingMatchesTheWriter(t *testing.T) {
	sessionA := store.Writer{Session: "s_aaaaaaaa", Token: "tok_s"}
	outside := store.Writer{Token: "tok_1", Host: "laptop"}
	cases := []struct {
		name   string
		writer store.Writer
		self   store.Writer
		want   bool
	}{
		{"same ARK session", sessionA, store.Writer{Session: "s_aaaaaaaa"}, true},
		{"other ARK session", sessionA, store.Writer{Session: "s_bbbbbbbb"}, false},
		{"outside agent, same token and device", outside, store.Writer{Token: "tok_1", Host: "laptop"}, true},
		{"outside agent, no device on the watcher", outside, store.Writer{Token: "tok_1"}, true},
		{"outside agent, other device", outside, store.Writer{Token: "tok_1", Host: "desktop"}, false},
		{"outside agent, other token", outside, store.Writer{Token: "tok_2", Host: "laptop"}, false},
		{"ARK session reading an outside agent's unit", outside, store.Writer{Session: "s_aaaaaaaa", Token: "tok_1"}, false},
		{"agent session ids differ on one token", store.Writer{Token: "tok_1", AgentSession: "cc-1"},
			store.Writer{Token: "tok_1", AgentSession: "cc-2"}, false},
		{"agent session id matches", store.Writer{Token: "tok_1", AgentSession: "cc-1"},
			store.Writer{Token: "tok_1", AgentSession: "cc-1"}, true},
	}
	for _, c := range cases {
		w := &store.UnitWriters{Created: c.writer, Edited: c.writer}
		it := routed(t, Viewer{User: "alice", Self: c.self}, commentRow(w, 0))
		if it.Value("for_you") != c.want || it.Value("audience") != AudienceSession {
			t.Errorf("%s: for_you %v audience %v", c.name, it.Value("for_you"), it.Value("audience"))
		}
	}
}

// Review hand-off beats creation in the reason; a reply goes to the
// comment's writer; an ended ARK session or an unknown writer falls back
// to the project.
func TestRoutingReasonsAndFallback(t *testing.T) {
	a, b := store.Writer{Session: "s_aaaaaaaa"}, store.Writer{Session: "s_bbbbbbbb"}
	w := &store.UnitWriters{Created: b, Review: a, Edited: a, Comments: map[int64]store.Writer{42: b}}
	it := routed(t, Viewer{User: "alice", Self: a}, commentRow(w, 0))
	if it.Value("reason") != "you submitted T-0007" {
		t.Fatalf("reason: %v", it.Value("reason"))
	}
	targets, _ := it.Value("target_sessions").([]any)
	if len(targets) != 2 {
		t.Fatalf("targets: %v", targets)
	}
	if it = routed(t, Viewer{User: "alice", Self: b}, commentRow(w, 0)); it.Value("reason") != "you created T-0007" {
		t.Fatalf("creator reason: %v", it.Value("reason"))
	}

	reply := commentRow(w, 42)
	reply.Comment.ReplyToAuthor = "agent:alice"
	if it = routed(t, Viewer{User: "alice", Self: b}, reply); it.Value("for_you") != true || it.Value("reason") != "reply to your comment #42" {
		t.Fatalf("reply for the comment's writer: %v %v", it.Value("for_you"), it.Value("reason"))
	}
	if it = routed(t, Viewer{User: "alice", Self: a}, reply); it.Value("for_you") != false {
		t.Fatal("a reply reached the unit's submitter instead of the comment's writer")
	}

	// Every writer session ended: the project, as before (alice's unit).
	dead := func(string) bool { return false }
	for _, self := range []store.Writer{a, b, {Session: "s_cccccccc"}} {
		it = routed(t, Viewer{User: "alice", AnyAgent: true, Self: self, SessionAlive: dead}, commentRow(w, 0))
		if it.Value("for_you") != true || it.Value("audience") != AudienceProject || it.Value("reason") != "fallback: no owner" {
			t.Fatalf("fallback for %v: %v %v %v", self, it.Value("for_you"), it.Value("audience"), it.Value("reason"))
		}
	}
	// No writer known at all.
	it = routed(t, Viewer{User: "alice", Self: a}, commentRow(&store.UnitWriters{Created: store.Writer{}}, 0))
	if it.Value("audience") != AudienceProject || it.Value("for_you") != true {
		t.Fatalf("no writer: %v %v", it.Value("audience"), it.Value("for_you"))
	}

	// for_you=1 drops the others' items; mine=1 keeps a unit the viewer
	// wrote even when another account's agent created it.
	if items := Items([]store.UpdateRow{commentRow(w, 0)}, Viewer{User: "alice", Self: store.Writer{Session: "s_cccccccc"}, ForYou: true}, nil); len(items) != 0 {
		t.Fatalf("for_you=1 kept another agent's item: %v", items)
	}
	other := commentRow(w, 0)
	other.Creator, other.Assignee = "agent:bob", "bob"
	if items := Items([]store.UpdateRow{other}, Viewer{User: "alice", Self: a, Mine: true}, nil); len(items) != 1 {
		t.Fatal("mine=1 dropped a unit the viewer submitted")
	}
}
