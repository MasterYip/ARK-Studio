package org

import (
	"context"
	"path/filepath"
	"testing"

	"arkstudio/go/internal/pyjson"
)

func TestIsSecretToken(t *testing.T) {
	cases := map[any]bool{
		"ark1$a$b$c": true, "ark1$a$$c": false, "ark2$a$b$c": false, "ark1$a$b": false,
		"ark1$a$b$c$d": false, "plain": false, "": false, nil: false, int64(1): false,
	}
	for v, want := range cases {
		if got := IsSecretToken(v); got != want {
			t.Errorf("IsSecretToken(%#v) = %v, want %v", v, got, want)
		}
	}
}

// Port of main 6988ce9: untouched credentials keep their sealed token, and
// a plaintext value left by the old bug is re-sealed on read.
func TestPersonalSecretsRepairOnRead(t *testing.T) {
	t.Setenv("ARK_ORG_SECRET_KEY", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	secretKeys.Range(func(k, _ any) bool { secretKeys.Delete(k); return true })
	defer secretKeys.Range(func(k, _ any) bool { secretKeys.Delete(k); return true })
	m, err := Open(filepath.Join(t.TempDir(), "org.sqlite3"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ctx := context.Background()
	hash := "pbkdf2_sha256$1000$c2FsdHNhbHRzYWx0$" + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if err := m.Bootstrap(ctx, "owner", hash, false, nil); err != nil {
		t.Fatal(err)
	}
	user, _, err := m.UserByName(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	uid := user.Str("id")
	good, err := SealSecret("kept", secretAAD(uid, "agents", "env:codex:A"))
	if err != nil {
		t.Fatal(err)
	}
	blob := string(pyjson.Dumps(pyjson.NewObject("env:codex:A", good, "env:codex:B", "plain-value", "empty", "")))
	if err := m.SaveSecrets(ctx, uid, "agents", blob); err != nil {
		t.Fatal(err)
	}
	opened, err := m.secrets(ctx, uid, "agents")
	if err != nil {
		t.Fatalf("read must repair, got %v", err)
	}
	if pyjson.Str(opened.Value("env:codex:A")) != "kept" || pyjson.Str(opened.Value("env:codex:B")) != "plain-value" || opened.Has("empty") {
		t.Fatalf("opened = %s", pyjson.Dumps(opened))
	}
	tokens, err := m.sealedTokens(ctx, uid, "agents")
	if err != nil {
		t.Fatal(err)
	}
	if tokens.Len() != 2 || pyjson.Str(tokens.Value("env:codex:A")) != good || !IsSecretToken(tokens.Value("env:codex:B")) {
		t.Fatalf("stored tokens after repair = %s", pyjson.Dumps(tokens))
	}
	if _, err := sealedBlob(pyjson.NewObject("k", "plaintext")); err == nil || err.(*Error).Code != "SECRET_UNSEALED" {
		t.Fatalf("sealedBlob must refuse plaintext, got %v", err)
	}
}
