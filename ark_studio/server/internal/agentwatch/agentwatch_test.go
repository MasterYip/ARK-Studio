package agentwatch

import (
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/store"
)

func TestCursorRoundTrip(t *testing.T) {
	c := Cursor("ab12.cd", 42)
	epoch, id, ok := ParseCursor(c)
	if !ok || epoch != "ab12.cd" || id != 42 {
		t.Fatalf("ParseCursor(%q) = %q %d %v", c, epoch, id, ok)
	}
	for _, bad := range []string{"", "42", "u1.", "u1.x", "u1.x.-1", "u1.x.y", "u2.x.1"} {
		if _, _, ok := ParseCursor(bad); ok {
			t.Errorf("ParseCursor(%q) accepted", bad)
		}
	}
}

func TestParseKinds(t *testing.T) {
	if k, err := ParseKinds(" "); err != nil || k != nil {
		t.Fatalf("empty: %v %v", k, err)
	}
	k, err := ParseKinds("comment, sent_back,")
	if err != nil || len(k) != 2 || !k["comment"] || !k["sent_back"] {
		t.Fatalf("kinds: %v %v", k, err)
	}
	if _, err := ParseKinds("comment,nope"); err == nil {
		t.Fatal("unknown kind accepted")
	}
}

func TestIsHuman(t *testing.T) {
	for actor, want := range map[string]bool{"human:ui": true, "alice": true, "assistant:chat": true,
		"agent:alice": false, "Agent:api": false, "system:migration": false, "": false} {
		if got := IsHuman(actor); got != want {
			t.Errorf("IsHuman(%q) = %v", actor, got)
		}
	}
}

func detail(kv ...any) *pyjson.Object { return pyjson.NewObject(kv...) }

func unitRow(id int64, action, actor string, d *pyjson.Object) store.UpdateRow {
	return store.UpdateRow{ID: id, At: "2026-11-17T10:00:00+00:00", Actor: actor, Action: action, Detail: d,
		UnitID: 7, UID: "T-0007", Title: "Train the model", Type: "task", Status: "active", Priority: "p1",
		Assignee: "alice", Creator: "agent:alice/coder"}
}

func kinds(items []*pyjson.Object) []string {
	var out []string
	for _, it := range items {
		out = append(out, pyjson.Str(it.Value("kind")))
	}
	return out
}

var alice = Viewer{User: "alice", AgentActor: "agent:alice/coder", AgentName: "coder"}

func TestCommentsRepliesMentions(t *testing.T) {
	rows := []store.UpdateRow{
		unitRow(1, "comment.create", "human:ui", nil),
		unitRow(2, "comment.create", "human:ui", nil),
		unitRow(3, "comment.create", "human:ui", nil),
		unitRow(4, "comment.create", "agent:alice/coder", nil), // the agent's own reply
		unitRow(5, "unit.update", "agent:alice/coder", detail("fields", []any{"status"})),
	}
	rows[0].Comment = &store.UpdateComment{ID: 10, Author: "alice", Kind: "comment", Body: "Use the small model"}
	rows[1].Comment = &store.UpdateComment{ID: 11, Author: "bob", Kind: "comment", Body: "ok", ReplyTo: 9,
		ReplyToAuthor: "agent:alice/coder", ReplyToKind: "agent_reply"}
	rows[2].Comment = &store.UpdateComment{ID: 12, Author: "bob", Kind: "comment", Body: "@coder please rerun"}
	rows[3].Comment = &store.UpdateComment{ID: 13, Author: "agent:alice/coder", Kind: "agent_reply", Body: "done"}
	items := Items(rows, alice, nil)
	if got := kinds(items); len(got) != 3 || got[0] != KindComment || got[1] != KindReply || got[2] != KindMention {
		t.Fatalf("kinds %v", got)
	}
	c := items[1].Value("comment").(*pyjson.Object)
	if c.Value("id") != int64(11) || c.Value("reply_to") != int64(9) || items[1].Value("actor") != "bob" {
		t.Fatalf("reply item %s", pyjson.Dumps(items[1]))
	}
	if items[0].Value("uid") != "T-0007" || items[0].Value("link") != "/api/agent/v1/units/T-0007/comments" ||
		items[0].Value("id") != "a1" {
		t.Fatalf("item %s", pyjson.Dumps(items[0]))
	}
	// A reply to another account's agent is a plain comment.
	other := Viewer{User: "carol", AgentActor: "agent:carol"}
	if got := kinds(Items(rows[1:2], other, nil)); len(got) != 1 || got[0] != KindComment {
		t.Fatalf("other viewer %v", got)
	}
}

func TestReviewAnswerAbsorbsStatus(t *testing.T) {
	rows := []store.UpdateRow{
		unitRow(1, "comment.create", "human:ui", nil),
		unitRow(2, "unit.update", "human:ui", detail("fields", []any{"status"},
			"status", detail("from", "review", "to", "active"))),
		unitRow(3, "unit.update", "human:ui", detail("fields", []any{"importance"})),
	}
	rows[0].Comment = &store.UpdateComment{ID: 5, Author: "alice", Kind: "sent_back", Body: "Missing the baseline"}
	items := Items(rows, alice, nil)
	if got := kinds(items); len(got) != 2 || got[0] != KindSentBack || got[1] != KindImportance {
		t.Fatalf("kinds %v", got)
	}
	ch := items[0].Value("change").(*pyjson.Object)
	if ch.Value("to") != "active" {
		t.Fatalf("change %s", pyjson.Dumps(ch))
	}
	// Without the answer the status change is its own item.
	if got := kinds(Items(rows[1:2], alice, nil)); len(got) != 1 || got[0] != KindStatus {
		t.Fatalf("status alone %v", got)
	}
}

func TestMineAndAssignments(t *testing.T) {
	notMine := unitRow(1, "unit.update", "human:ui", detail("fields", []any{"status"},
		"status", detail("from", "open", "to", "done")))
	notMine.Assignee, notMine.Creator = "bob", "human:ui"
	if got := Items([]store.UpdateRow{notMine}, alice, nil); len(got) != 0 {
		t.Fatalf("status change on someone else's unit: %v", kinds(got))
	}
	assign := unitRow(2, "unit.update", "human:ui", detail("fields", []any{"assignee"},
		"assignee", detail("from", "bob", "to", "alice")))
	assign.Assignee, assign.Creator = "alice", "human:ui"
	created := unitRow(3, "unit.create", "human:ui", detail("assignee", "alice"))
	decision := unitRow(4, "unit.create", "human:ui", nil)
	decision.Type, decision.Assignee, decision.Creator = "decision", "bob", "human:ui"
	setup := store.UpdateRow{ID: 5, Actor: "human:bob", Action: "setup.project.confirmed", Detail: detail()}
	items := Items([]store.UpdateRow{assign, created, decision, setup}, alice, nil)
	if got := kinds(items); len(got) != 4 || got[0] != KindAssigned || got[1] != KindAssigned ||
		got[2] != KindDecision || got[3] != KindSetupConfirmed {
		t.Fatalf("kinds %v", got)
	}
	// mine=1 drops the decision on bob's unit but keeps setup confirmations.
	mine := alice
	mine.Mine = true
	if got := kinds(Items([]store.UpdateRow{decision, setup}, mine, nil)); len(got) != 1 || got[0] != KindSetupConfirmed {
		t.Fatalf("mine %v", got)
	}
	// A kinds filter.
	only := alice
	only.Kinds = map[string]bool{KindDecision: true}
	if got := kinds(Items([]store.UpdateRow{assign, decision}, only, nil)); len(got) != 1 || got[0] != KindDecision {
		t.Fatalf("filtered %v", got)
	}
}

func TestMessagesTargeting(t *testing.T) {
	row := store.UpdateRow{ID: 9, Actor: "human:alice", Action: store.MessageAction, Detail: detail("message_id", int64(3))}
	msg := func(owner, target string) map[int64]*pyjson.Object {
		return map[int64]*pyjson.Object{3: pyjson.NewObject("id", int64(3), "owner", owner, "target", target,
			"sender", owner, "body", "Stop the sweep", "status", "queued")}
	}
	v := alice
	v.TokenID, v.TokenLabel = "tok1", "laptop"
	for target, want := range map[string]bool{"mine": true, "token:tok1": true, "token:tok2": false,
		"label:Laptop": true, "label:server": false, "other": false} {
		got := Items([]store.UpdateRow{row}, v, msg("alice", target))
		if (len(got) == 1) != want {
			t.Errorf("target %s: %v", target, kinds(got))
		}
	}
	if got := Items([]store.UpdateRow{row}, v, msg("bob", "mine")); len(got) != 0 {
		t.Fatalf("bob's message reached alice's agent")
	}
	got := Items([]store.UpdateRow{row}, v, msg("alice", "mine"))
	m := got[0].Value("message").(*pyjson.Object)
	if m.Value("reply") != "/api/agent/v1/messages/3/reply" || got[0].Value("actor") != "alice" {
		t.Fatalf("message item %s", pyjson.Dumps(got[0]))
	}
}

// A finished GPU job reaches the ARK session that queued it, or the token
// that queued it; never another session, token or account.
func TestGPUJobDoneRouting(t *testing.T) {
	row := func(detail ...any) store.UpdateRow {
		d := pyjson.NewObject("job", "gpu_1", "name", "train", "server", "h20", "status", "errored",
			"exit_code", int64(1), "duration_s", int64(3725), "error", "remote process exited with code 1: OOM", "owner", "ana")
		for i := 0; i+1 < len(detail); i += 2 {
			d.Set(detail[i].(string), detail[i+1])
		}
		return store.UpdateRow{ID: 7, Actor: store.GPUJobActor, Action: store.GPUJobDoneAction, Detail: d}
	}
	session := row("session", "ses_1", "token", "tok_s")
	external := row("token", "tok_x")
	shared := row()
	cases := []struct {
		name string
		v    Viewer
		r    store.UpdateRow
		want bool
	}{
		{"own session", Viewer{User: "ana", AnyAgent: true, Mine: true, Session: "ses_1"}, session, true},
		{"other session", Viewer{User: "ana", AnyAgent: true, Mine: true, Session: "ses_2"}, session, false},
		{"session's own watcher", Viewer{User: "ana", TokenID: "tok_s"}, session, true},
		{"another token of the user", Viewer{User: "ana", TokenID: "tok_x"}, session, false},
		{"external token", Viewer{User: "ana", TokenID: "tok_x", Mine: true}, external, true},
		{"other token", Viewer{User: "ana", TokenID: "tok_y"}, external, false},
		{"session never gets external", Viewer{User: "ana", AnyAgent: true, Session: "ses_1"}, external, false},
		{"server token, owner", Viewer{User: "ana", AnyAgent: true}, shared, true},
		{"server token, other user", Viewer{User: "bob", AnyAgent: true}, shared, false},
		{"session never gets shared", Viewer{User: "ana", AnyAgent: true, Session: "ses_1"}, shared, false},
	}
	for _, tc := range cases {
		items := Items([]store.UpdateRow{tc.r}, tc.v, nil)
		if (len(items) == 1) != tc.want {
			t.Errorf("%s: %d items, want %v", tc.name, len(items), tc.want)
			continue
		}
		if tc.want {
			it := items[0]
			sum := pyjson.Str(it.Value("summary"))
			if it.Value("kind") != KindGPUJobDone || it.Value("link") != "/api/agent/v1/gpu/jobs/gpu_1/logs" ||
				!strings.Contains(sum, `GPU job gpu_1 "train" errored on h20 (exit 1) after 1h02m: remote process exited with code 1: OOM`) {
				t.Errorf("%s: item %s", tc.name, pyjson.Dumps(it))
			}
		}
	}
	if _, err := ParseKinds("gpu_job_done"); err != nil {
		t.Fatal(err)
	}
}
