package org

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

type starterEnv struct {
	t     *testing.T
	m     *Manager
	ctx   context.Context
	clock *float64
}

func newStarterEnv(t *testing.T) *starterEnv {
	t.Helper()
	t.Setenv("ARK_ORG_SECRET_KEY", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	secretKeys.Range(func(k, _ any) bool { secretKeys.Delete(k); return true })
	t.Cleanup(func() { secretKeys.Range(func(k, _ any) bool { secretKeys.Delete(k); return true }) })
	m, err := Open(filepath.Join(t.TempDir(), "org.sqlite3"), 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	ctx := context.Background()
	hash := "pbkdf2_sha256$1000$c2FsdHNhbHRzYWx0$" + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if err := m.Bootstrap(ctx, "owner", hash, false, nil); err != nil {
		t.Fatal(err)
	}
	if len(AvatarColors) == 0 {
		AvatarColors = []string{"#4f46e5"}
	}
	if _, err := m.CreateUser(ctx, "owner", pyjson.NewObject("username", "ana", "password", "correct horse battery", "role", "member")); err != nil {
		t.Fatal(err)
	}
	now := 1_800_000_000.0
	saved := TokenClock
	TokenClock = func() float64 { return now }
	t.Cleanup(func() { TokenClock = saved })
	return &starterEnv{t: t, m: m, ctx: ctx, clock: &now}
}

func (e *starterEnv) starter(user string, members ...any) (*pyjson.Object, error) {
	*e.clock++
	body := pyjson.NewObject("label", "anything", "scope", "write", "expires_days", int64(90), "purpose", "starter")
	for i := 0; i+1 < len(members); i += 2 {
		body.Set(members[i].(string), members[i+1])
	}
	return e.m.CreateAgentToken(e.ctx, user, body)
}

func (e *starterEnv) mustStarter(user string, members ...any) (string, string, *pyjson.Object) {
	e.t.Helper()
	out, err := e.starter(user, members...)
	if err != nil {
		e.t.Fatal(err)
	}
	secret := pyjson.Str(out.Value("token"))
	if !strings.HasPrefix(secret, tokenPrefix) {
		e.t.Fatalf("no secret returned: %v", out.Keys())
	}
	return secret, pyjson.Str(out.Value("item").(*pyjson.Object).Value("id")), out
}

func (e *starterEnv) works(secret string) bool {
	e.t.Helper()
	_, _, ok, err := e.m.AuthenticateToken(e.ctx, secret)
	if err != nil {
		e.t.Fatal(err)
	}
	return ok
}

func (e *starterEnv) active(user string) map[string]string {
	e.t.Helper()
	list, err := e.m.ListAgentTokens(e.ctx, user)
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]string{}
	for _, item := range list.Value("tokens").([]any) {
		o := item.(*pyjson.Object)
		if o.Value("expired") != true {
			out[pyjson.Str(o.Value("id"))] = pyjson.Str(o.Value("label"))
		}
	}
	return out
}

// Copy prompt reuses the project's stored starter token; the value is kept
// sealed, not in plain text.
func TestStarterTokenReuse(t *testing.T) {
	e := newStarterEnv(t)
	secret, id, first := e.mustStarter("owner", "project", "prj_a")
	if first.Value("reused") != false {
		t.Fatalf("first copy reused: %v", first.Value("reused"))
	}
	label := pyjson.Str(first.Value("item").(*pyjson.Object).Value("label"))
	if !strings.HasPrefix(label, "Home starter prompt (prj_a) – ") || len(label) > maxTokenLabel {
		t.Fatalf("label %q", label)
	}
	for i := 0; i < 15; i++ {
		again, againID, out := e.mustStarter("owner", "project", "prj_a")
		if again != secret || againID != id || out.Value("reused") != true {
			t.Fatalf("copy %d minted a new token (reused %v)", i, out.Value("reused"))
		}
	}
	if n := len(e.active("owner")); n != 1 {
		t.Fatalf("%d active tokens after repeated copies", n)
	}
	row, ok, err := e.m.row(e.ctx, "SELECT secret FROM starter_token_secrets WHERE token_id=?", id)
	if err != nil || !ok {
		t.Fatalf("no stored secret: %v", err)
	}
	if strings.Contains(row.Str("secret"), secret) || !IsSecretToken(row.Str("secret")) {
		t.Fatal("the stored value is not sealed")
	}
	// Another project gets its own token.
	other, otherID, _ := e.mustStarter("owner", "project", "prj_b")
	if other == secret || otherID == id {
		t.Fatal("projects share a token")
	}
	// Peek shows the current item without the value.
	peek, err := e.starter("owner", "project", "prj_a", "mode", "peek")
	if err != nil {
		t.Fatal(err)
	}
	if peek.Has("token") || pyjson.Str(peek.Value("current").(*pyjson.Object).Value("id")) != id {
		t.Fatalf("peek: %v", peek.Keys())
	}
	// Revoked in Settings: the next copy mints a new one and drops the value.
	if _, err := e.m.RevokeAgentToken(e.ctx, "owner", id); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := e.m.row(e.ctx, "SELECT 1 FROM starter_token_secrets WHERE token_id=?", id); ok {
		t.Error("the value of a revoked token is kept")
	}
	fresh, freshID, out := e.mustStarter("owner", "project", "prj_a")
	if fresh == secret || freshID == id || out.Value("reused") != false {
		t.Fatal("a revoked token was handed out")
	}
	// Expired: a new one as well.
	if err := e.m.exec(e.ctx, "UPDATE agent_tokens SET expires_at='2000-01-01T00:00:00+00:00' WHERE id=?", freshID); err != nil {
		t.Fatal(err)
	}
	if _, id2, _ := e.mustStarter("owner", "project", "prj_a"); id2 == freshID {
		t.Fatal("an expired token was reused")
	}
}

// "New token" revokes the project's previous starter token.
func TestStarterTokenNewRevokesOld(t *testing.T) {
	e := newStarterEnv(t)
	old, oldID, _ := e.mustStarter("owner", "project", "prj_a")
	keep, _, _ := e.mustStarter("owner", "project", "prj_b")
	next, nextID, out := e.mustStarter("owner", "project", "prj_a", "mode", "new")
	if next == old || nextID == oldID {
		t.Fatal("new did not mint")
	}
	rotated, _ := out.Value("rotated").([]any)
	if len(rotated) != 1 || rotated[0] != oldID {
		t.Fatalf("rotated %v", rotated)
	}
	if e.works(old) || !e.works(next) || !e.works(keep) {
		t.Fatal("only the previous prj_a token should stop working")
	}
	if again, _, _ := e.mustStarter("owner", "project", "prj_a"); again != next {
		t.Fatal("copy after new does not reuse the new token")
	}
}

// Old starter tokens (no stored value) keep working, are never reused, and
// can be revoked with revoke_old or from Settings (cleanup).
func TestStarterTokenOldTokens(t *testing.T) {
	e := newStarterEnv(t)
	var oldIDs []string
	for i := 0; i < 4; i++ {
		*e.clock++
		out, err := e.m.CreateAgentToken(e.ctx, "owner", pyjson.NewObject("label", "x", "scope", "write", "expires_days", int64(90)))
		if err != nil {
			t.Fatal(err)
		}
		id := pyjson.Str(out.Value("item").(*pyjson.Object).Value("id"))
		label := starterLabel("prj_a", TokenClock())
		if i == 3 {
			label = "Home starter prompt – 2026-11-01" // unscoped, from before projects
		}
		if err := e.m.exec(e.ctx, "UPDATE agent_tokens SET label=? WHERE id=?", label, id); err != nil {
			t.Fatal(err)
		}
		oldIDs = append(oldIDs, id)
	}
	peek, err := e.starter("owner", "project", "prj_a", "mode", "peek")
	if err != nil {
		t.Fatal(err)
	}
	if peek.Value("current") != nil || peek.Value("old_tokens") != int64(3) {
		t.Fatalf("peek current %v old %v", peek.Value("current"), peek.Value("old_tokens"))
	}
	_, id, out := e.mustStarter("owner", "project", "prj_a", "revoke_old", true)
	rotated, _ := out.Value("rotated").([]any)
	if len(rotated) != 3 {
		t.Fatalf("revoke_old revoked %v", rotated)
	}
	now := e.active("owner")
	if _, ok := now[oldIDs[3]]; !ok {
		t.Error("the unscoped old token belongs to no project and stays")
	}
	if _, ok := now[id]; !ok || len(now) != 2 {
		t.Fatalf("active after revoke_old: %d", len(now))
	}
	for i := 0; i < 3; i++ {
		if _, again, _ := e.mustStarter("owner", "project", "prj_a"); again != id {
			t.Fatal("copy did not reuse")
		}
	}
	_, newID, _ := e.mustStarter("owner", "project", "prj_a", "mode", "new")
	// Settings cleanup: older starter tokens of every project go, current ones stay.
	if all, err := e.starter("owner", "mode", "peek"); err != nil || all.Value("old_tokens") != int64(1) || all.Value("current") != nil {
		t.Fatalf("peek without project: %v", err)
	}
	out, err = e.starter("owner", "mode", "cleanup")
	if err != nil {
		t.Fatal(err)
	}
	if revoked, _ := out.Value("revoked").([]any); len(revoked) != 1 || revoked[0] != oldIDs[3] {
		t.Fatalf("cleanup revoked %v", revoked)
	}
	if _, ok := e.active("owner")[newID]; !ok {
		t.Fatal("cleanup revoked the current token")
	}
}

// There is no cap on active tokens: plain and starter tokens past ten are created.
func TestAgentTokensNoLimit(t *testing.T) {
	e := newStarterEnv(t)
	for i := 0; i < 25; i++ {
		*e.clock++
		if _, err := e.m.CreateAgentToken(e.ctx, "owner", pyjson.NewObject("label", "filler")); err != nil {
			t.Fatalf("token %d: %v", i+1, err)
		}
	}
	for _, p := range []string{"prj_a", "prj_b", "prj_c"} {
		e.mustStarter("owner", "project", p)
	}
	e.mustStarter("owner", "project", "prj_a", "mode", "new")
	if n := len(e.active("owner")); n != 28 {
		t.Fatalf("%d active tokens, want 28", n)
	}
	list, err := e.m.ListAgentTokens(e.ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if list.Has("limit") {
		t.Fatal("the list still reports a limit")
	}
}

// Stored values are per user: another account never gets them.
func TestStarterTokenPerUser(t *testing.T) {
	e := newStarterEnv(t)
	mine, mineID, _ := e.mustStarter("owner", "project", "prj_a")
	theirs, theirsID, _ := e.mustStarter("ana", "project", "prj_a")
	if theirs == mine || theirsID == mineID {
		t.Fatal("another user got the owner's token")
	}
	peek, err := e.starter("ana", "project", "prj_a", "mode", "peek")
	if err != nil || pyjson.Str(peek.Value("current").(*pyjson.Object).Value("id")) != theirsID {
		t.Fatalf("peek for ana: %v", err)
	}
	// ana's "new" and cleanup leave the owner's token alone.
	e.mustStarter("ana", "project", "prj_a", "mode", "new")
	if _, err := e.starter("ana", "mode", "cleanup"); err != nil {
		t.Fatal(err)
	}
	if again, _, _ := e.mustStarter("owner", "project", "prj_a"); again != mine || !e.works(mine) {
		t.Fatal("the owner's token changed")
	}
	// A sealed value moved to another user's row does not open.
	if err := e.m.exec(e.ctx, "UPDATE starter_token_secrets SET secret=(SELECT secret FROM starter_token_secrets WHERE token_id=?) "+
		"WHERE user_id=(SELECT id FROM users WHERE username='ana')", mineID); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := e.mustStarter("ana", "project", "prj_a"); got == mine {
		t.Fatal("ana opened the owner's token")
	}
}

func TestStarterTokenValidation(t *testing.T) {
	e := newStarterEnv(t)
	for _, members := range [][]any{
		{},
		{"project", "../x"},
		{"project", strings.Repeat("p", 41)},
		{"mode", "new"},
	} {
		if _, err := e.starter("owner", members...); !isCode(err, "AGENT_TOKEN_PROJECT_INVALID") {
			t.Errorf("%v: %v", members, err)
		}
	}
	if _, err := e.starter("owner", "project", "prj_a", "mode", "steal"); !isCode(err, "AGENT_TOKEN_MODE_INVALID") {
		t.Errorf("unknown mode: %v", err)
	}
	if _, err := e.m.CreateAgentToken(e.ctx, "owner", pyjson.NewObject("purpose", "ci", "project", "prj_a")); !isCode(err, "AGENT_TOKEN_PURPOSE_INVALID") {
		t.Errorf("unknown purpose: %v", err)
	}
	plain, err := e.m.CreateAgentToken(e.ctx, "owner", pyjson.NewObject("label", "CI", "scope", "read"))
	if err != nil || plain.Has("rotated") || plain.Has("reused") {
		t.Fatalf("plain create: %v", err)
	}
}

func isCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}
