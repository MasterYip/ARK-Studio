package org

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

// UX-18: Home starter-prompt tokens rotate per user and project instead of
// piling up to the 10-token limit; plain creates are unchanged.
func TestStarterTokenRotation(t *testing.T) {
	m, err := Open(filepath.Join(t.TempDir(), "org.sqlite3"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ctx := context.Background()
	if err := m.Bootstrap(ctx, "", "", false, nil); err != nil {
		t.Fatal(err)
	}
	clockNow := 1_800_000_000.0
	saved := TokenClock
	TokenClock = func() float64 { return clockNow }
	defer func() { TokenClock = saved }()

	starter := func(project string) *pyjson.Object {
		t.Helper()
		clockNow++
		out, err := m.CreateAgentToken(ctx, "local", pyjson.NewObject("label", "anything", "scope", "write",
			"expires_days", int64(90), "purpose", "starter", "project", project))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	active := func() map[string]string { // id -> label
		t.Helper()
		list, err := m.ListAgentTokens(ctx, "local")
		if err != nil {
			t.Fatal(err)
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

	// A plain token and one for another project are never touched.
	plain, err := m.CreateAgentToken(ctx, "local", pyjson.NewObject("label", "CI", "scope", "read"))
	if err != nil {
		t.Fatal(err)
	}
	if plain.Has("rotated") {
		t.Fatalf("plain create carries rotated: %v", plain.Keys())
	}
	plainID := pyjson.Str(plain.Value("item").(*pyjson.Object).Value("id"))
	other := starter("prj_other")
	otherID := pyjson.Str(other.Value("item").(*pyjson.Object).Value("id"))

	// Eleven copies in a row: no limit error, two active starter tokens.
	var ids []string
	for i := 0; i < 11; i++ {
		out := starter("prj_a")
		item := out.Value("item").(*pyjson.Object)
		label := pyjson.Str(item.Value("label"))
		if !strings.HasPrefix(label, "Home starter prompt (prj_a) – ") || len(label) > maxTokenLabel {
			t.Fatalf("label %q", label)
		}
		if !strings.HasPrefix(pyjson.Str(out.Value("token")), tokenPrefix) {
			t.Fatal("no secret returned")
		}
		rotated, _ := out.Value("rotated").([]any)
		if i < 2 && len(rotated) != 0 || i >= 2 && (len(rotated) != 1 || rotated[0] != ids[i-2]) {
			t.Fatalf("copy %d rotated %v (ids %v)", i, rotated, ids)
		}
		ids = append(ids, pyjson.Str(item.Value("id")))
	}
	now := active()
	if len(now) != 4 {
		t.Fatalf("active tokens %v", now)
	}
	for _, id := range []string{plainID, otherID, ids[9], ids[10]} {
		if _, ok := now[id]; !ok {
			t.Errorf("%s should stay active: %v", id, now)
		}
	}
	// Revoked tokens no longer authenticate; the kept ones do.
	if _, _, ok, err := m.AuthenticateToken(ctx, pyjson.Str(other.Value("token"))); err != nil || !ok {
		t.Errorf("other project's starter token: %v %v", ok, err)
	}

	// An expired starter token is retired without counting as active.
	if err := m.exec(ctx, "UPDATE agent_tokens SET expires_at='2000-01-01T00:00:00+00:00' WHERE id=?", ids[10]); err != nil {
		t.Fatal(err)
	}
	out := starter("prj_a")
	rotated, _ := out.Value("rotated").([]any)
	if len(rotated) != 1 || rotated[0] != ids[10] {
		t.Fatalf("expired newest: rotated %v", rotated)
	}
	if _, ok := active()[ids[9]]; !ok {
		t.Error("the newest unexpired previous token is kept")
	}

	// Full account: a starter copy still succeeds when it frees a slot,
	// a plain create does not.
	for len(active()) < maxActiveTokens {
		clockNow++
		if _, err := m.CreateAgentToken(ctx, "local", pyjson.NewObject("label", "filler")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.CreateAgentToken(ctx, "local", pyjson.NewObject("label", "one more")); !isCode(err, "AGENT_TOKEN_LIMIT") {
		t.Fatalf("plain create at the limit: %v", err)
	}
	starter("prj_a")
	if n := len(active()); n != maxActiveTokens {
		t.Fatalf("after a starter copy at the limit: %d active", n)
	}
	// A first starter token for a new project frees nothing: limit.
	if _, err := m.CreateAgentToken(ctx, "local", pyjson.NewObject("purpose", "starter", "project", "prj_new")); !isCode(err, "AGENT_TOKEN_LIMIT") {
		t.Fatalf("new project at the limit: %v", err)
	}

	// Validation.
	for _, body := range []*pyjson.Object{
		pyjson.NewObject("purpose", "starter"),
		pyjson.NewObject("purpose", "starter", "project", "../x"),
		pyjson.NewObject("purpose", "starter", "project", strings.Repeat("p", 41)),
	} {
		if _, err := m.CreateAgentToken(ctx, "local", body); !isCode(err, "AGENT_TOKEN_PROJECT_INVALID") {
			t.Errorf("%v: %v", body.Keys(), err)
		}
	}
	if _, err := m.CreateAgentToken(ctx, "local", pyjson.NewObject("purpose", "ci", "project", "prj_a")); !isCode(err, "AGENT_TOKEN_PURPOSE_INVALID") {
		t.Errorf("unknown purpose: %v", err)
	}
}

func isCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}
