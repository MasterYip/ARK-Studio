package org

// Personal settings (read path), SecretBox.open and resolve_assistant (org.py).

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"arkstudio/go/internal/assistcfg"
	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pytext"
	"arkstudio/go/internal/rnd"
)

const secretPrefix = "ark1"

// secretKeys caches SecretBox._load_key per key file (one OrgManager per server).
var secretKeys sync.Map

func unb64(text string) ([]byte, error) {
	if n := len(text) % 4; n != 0 {
		text += strings.Repeat("=", 4-n)
	}
	return base64.URLEncoding.DecodeString(text)
}

// secretKey is SecretBox._load_key for <private>/org/secret.key and ARK_ORG_SECRET_KEY.
func secretKey() ([]byte, error) {
	path := filepath.Join(assistcfg.PrivateDir(), "org", "secret.key")
	if k, ok := secretKeys.Load(path); ok {
		return k.([]byte), nil
	}
	digest := func(raw []byte) []byte {
		sum := sha256.Sum256(raw)
		secretKeys.Store(path, sum[:])
		return sum[:]
	}
	if override := pytext.Strip(os.Getenv("ARK_ORG_SECRET_KEY")); override != "" {
		raw, err := hex.DecodeString(override)
		if err != nil {
			if raw, err = unb64(override); err != nil {
				return nil, &Error{500, "ARK_ORG_SECRET_KEY must be a hex or base64 32-byte key", "SECRET_KEY_INVALID"}
			}
		}
		if n := len(raw); n != 16 && n != 24 && n != 32 {
			return nil, &Error{500, "ARK_ORG_SECRET_KEY must decode to 16-32 bytes", "SECRET_KEY_INVALID"}
		}
		return digest(raw), nil
	}
	existing, err := os.ReadFile(path)
	if err == nil {
		if len(existing) >= 16 {
			return digest(existing), nil
		}
		return nil, &Error{500, "the local credential key is corrupt (too short); restore a valid key or set ARK_ORG_SECRET_KEY explicitly", "SECRET_KEY_INVALID"}
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, &Error{500, "the local credential key exists but cannot be read; fix its permissions or set ARK_ORG_SECRET_KEY explicitly", "SECRET_KEY_UNAVAILABLE"}
	}
	raw := rnd.Bytes("org", 32) // secrets.token_bytes(32) of org.py's "org" stream
	if os.MkdirAll(filepath.Dir(path), 0o777) != nil || os.WriteFile(path, raw, 0o600) != nil {
		return nil, &Error{500, "ARK cannot create the local secret key; personal credentials cannot be stored safely on this host", "SECRET_KEY_UNAVAILABLE"}
	}
	return digest(raw), nil
}

func keystream(key, aad, nonce []byte, n int) []byte {
	out := make([]byte, 0, n+32)
	var counter [8]byte
	for i := uint64(0); len(out) < n; i++ {
		binary.BigEndian.PutUint64(counter[:], i)
		h := sha256.New()
		h.Write([]byte("ark-org-keystream\x01"))
		h.Write(key)
		h.Write(aad)
		h.Write(nonce)
		h.Write(counter[:])
		out = h.Sum(out)
	}
	return out[:n]
}

// OpenSecret is SecretBox.open(token, aad=aad).
func OpenSecret(token, aad string) (string, error) {
	parts := strings.SplitN(token, "$", 4)
	if len(parts) != 4 {
		return "", &Error{500, "stored secret is unreadable", "SECRET_CORRUPT"}
	}
	if parts[0] != secretPrefix {
		return "", &Error{500, "stored secret has an unknown version", "SECRET_CORRUPT"}
	}
	nonce, err1 := unb64(parts[1])
	cipher, err2 := unb64(parts[2])
	tag, err3 := unb64(parts[3])
	if err1 != nil || err2 != nil || err3 != nil {
		return "", &Error{500, "stored secret is unreadable", "SECRET_CORRUPT"}
	}
	key, err := secretKey()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("ark-org-tag\x02"))
	mac.Write([]byte(aad))
	mac.Write(nonce)
	mac.Write(cipher)
	if !hmac.Equal(mac.Sum(nil), tag) {
		return "", &Error{500, "stored credentials cannot be decrypted with this host's key (restore ~/.ark/org/secret.key or re-enter them)", "SECRET_KEY_MISMATCH"}
	}
	stream := keystream(key, []byte(aad), nonce, len(cipher))
	plain := make([]byte, len(cipher))
	for i := range cipher {
		plain[i] = cipher[i] ^ stream[i]
	}
	return strings.ToValidUTF8(string(plain), "�"), nil
}

// personalSettings is OrgManager.personal_settings(username, scope): the
// settings object with "secrets" (opened values) and "updated_at".
func (m *Manager) personalSettings(ctx context.Context, username, scope string) (*pyjson.Object, error) {
	user, ok, err := m.UserByName(ctx, username)
	if err != nil {
		return nil, err
	}
	if !ok || pyjson.Truthy(user.Get("disabled")) {
		return nil, &Error{403, "the account is not available", "ACCOUNT_UNAVAILABLE"}
	}
	uid := user.Str("id")
	row, found, err := m.row(ctx, "SELECT * FROM user_settings WHERE user_id=? AND scope=?", uid, scope)
	if err != nil {
		return nil, err
	}
	raw := "{}"
	if found && pyjson.Truthy(row.Get("data_json")) {
		raw = row.Str("data_json")
	}
	data, isObj := pyjson.LoadsObject(raw)
	if !isObj || data == nil {
		data = pyjson.NewObject()
	}
	secrets, err := m.secrets(ctx, uid, scope)
	if err != nil {
		return nil, err
	}
	data.Set("secrets", secrets)
	var updated any
	if found {
		updated = row.Get("updated_at")
	}
	data.Set("updated_at", updated)
	return data, nil
}

// IsSecretToken is SecretBox.is_token: the shape of a sealed token (four
// "$"-separated non-empty parts with the version prefix), not its validity.
func IsSecretToken(v any) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	parts := strings.Split(s, "$")
	if len(parts) != 4 || parts[0] != secretPrefix {
		return false
	}
	for _, p := range parts[1:] {
		if p == "" {
			return false
		}
	}
	return true
}

func secretAAD(uid, scope, key string) string { return uid + ":" + scope + ":" + key }

// sealedTokens is OrgManager._sealed_tokens: the stored sealed tokens of one
// user/scope, never opened.  Repair-on-read: a non-empty string that is not
// shaped like a token (an earlier bug wrote untouched credentials back as
// plaintext) is sealed in place, the row rewritten, and the repair logged
// by key name only.
func (m *Manager) sealedTokens(ctx context.Context, uid, scope string) (*pyjson.Object, error) {
	tokens := pyjson.NewObject()
	row, found, err := m.row(ctx, "SELECT * FROM user_secrets WHERE user_id=? AND scope=?", uid, scope)
	if err != nil || !found {
		return tokens, err
	}
	text := ""
	if v := row.Get("data_json"); pyjson.Truthy(v) {
		text = pyjson.Str(v)
	}
	if pytext.Strip(text) == "" {
		return tokens, nil
	}
	stored, ok := pyjson.LoadsObject(text)
	if !ok || stored == nil {
		return tokens, nil
	}
	var repaired []string
	for _, key := range stored.Keys() {
		value, ok := stored.Value(key).(string)
		if !ok || value == "" {
			continue
		}
		if IsSecretToken(value) {
			tokens.Set(key, value)
			continue
		}
		token, err := SealSecret(value, secretAAD(uid, scope, key))
		if err != nil {
			return nil, err
		}
		tokens.Set(key, token)
		repaired = append(repaired, key)
	}
	if len(repaired) > 0 {
		blob, err := sealedBlob(tokens)
		if err != nil {
			return nil, err
		}
		if err := m.SaveSecrets(ctx, uid, scope, blob); err != nil {
			return nil, err
		}
		sort.Strings(repaired)
		if err := m.Log(ctx, uid, "personal.secrets.reseal", "", pyjson.NewObject("scope", scope, "keys", strList(repaired))); err != nil {
			return nil, err
		}
		fmt.Fprintf(os.Stderr, "ark: re-sealed %d plaintext credential field(s) for user %s scope %s: %s\n",
			len(repaired), uid, scope, strings.Join(repaired, ", "))
	}
	return tokens, nil
}

// sealedBlob is OrgManager._sealed_blob: refuse to persist anything unsealed.
func sealedBlob(tokens *pyjson.Object) (string, error) {
	for _, key := range tokens.Keys() {
		if !IsSecretToken(tokens.Value(key)) {
			return "", &Error{500, "refusing to store an unsealed credential", "SECRET_UNSEALED"}
		}
	}
	if tokens.Len() == 0 {
		return "{}", nil
	}
	return string(pyjson.Dumps(tokens)), nil
}

// secrets is OrgManager._secrets: the opened values of sealedTokens.
func (m *Manager) secrets(ctx context.Context, uid, scope string) (*pyjson.Object, error) {
	tokens, err := m.sealedTokens(ctx, uid, scope)
	if err != nil {
		return nil, err
	}
	out := pyjson.NewObject()
	for _, key := range tokens.Keys() {
		value, err := OpenSecret(pyjson.Str(tokens.Value(key)), secretAAD(uid, scope, key))
		if err != nil {
			return nil, err
		}
		out.Set(key, value)
	}
	return out, nil
}

func emptyish(v any) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	return ok && s == ""
}

// KeyHint is f"…{key[-4:]}" (or "…" for a shorter key, "" for none).
func KeyHint(key string) string {
	if n := utf8.RuneCountInString(key); n >= 4 {
		r := []rune(key)
		return "…" + string(r[n-4:])
	} else if key != "" {
		return "…"
	}
	return ""
}

// ResolveAssistant is OrgManager.resolve_assistant(project_summary, username):
// personal override > project > global/env > built-in default, with
// per-field source labels.
func (m *Manager) ResolveAssistant(ctx context.Context, summary *pyjson.Object, username string, global *pyjson.Object) (*pyjson.Object, error) {
	projectAssistant := pyjson.NewObject()
	if summary != nil {
		if s, ok := summary.Value("settings").(*pyjson.Object); ok && s.Len() > 0 {
			if a, ok := s.Value("assistant").(*pyjson.Object); ok {
				projectAssistant = a
			}
		}
	}
	personal, err := m.personalSettings(ctx, username, "assistant")
	if err != nil {
		return nil, err
	}
	personalSecrets, _ := personal.Delete("secrets").(*pyjson.Object)
	if personalSecrets == nil {
		personalSecrets = pyjson.NewObject()
	}
	personal.Delete("updated_at")
	fields := []string{"base_url", "model", "reasoning_effort", "timeout"}
	values := map[string]any{"base_url": assistcfg.DefaultBaseURL, "model": assistcfg.DefaultModel,
		"reasoning_effort": assistcfg.DefaultReasoningEffort, "timeout": assistcfg.DefaultTimeout}
	sources := pyjson.NewObject()
	for _, key := range fields {
		sources.Set(key, "default")
	}
	for _, key := range fields {
		switch {
		case !emptyish(personal.Value(key)):
			values[key] = personal.Value(key)
			sources.Set(key, "personal")
		case !emptyish(projectAssistant.Value(key)):
			values[key] = projectAssistant.Value(key)
			sources.Set(key, "project")
		case !emptyish(global.Value(key)):
			values[key] = global.Value(key)
			sources.Set(key, "global")
		}
	}
	apiKey, keySource, keyName := "", "none", ""
	switch {
	case pyjson.Truthy(personalSecrets.Value("api_key")):
		apiKey, keySource = pyjson.Str(personalSecrets.Value("api_key")), "personal"
	case pyjson.Truthy(projectAssistant.Value("api_key_env")):
		keyName = pyjson.Str(projectAssistant.Value("api_key_env"))
		if candidate := pytext.Strip(os.Getenv(keyName)); candidate != "" {
			apiKey, keySource = candidate, "project"
		}
	case pyjson.Truthy(global.Value("api_key")):
		apiKey, keySource = pyjson.Str(global.Value("api_key")), "global"
	}
	sources.Set("api_key", keySource)
	out := pyjson.NewObject("base_url", values["base_url"], "model", values["model"],
		"reasoning_effort", values["reasoning_effort"], "timeout", values["timeout"], "api_key", apiKey,
		"sources", sources, "api_key_env", keyName, "project_configured", projectAssistant.Len() > 0,
		"personal_configured", personal.Len() > 0 || personalSecrets.Len() > 0)
	configured := apiKey != ""
	out.Set("configured", configured)
	out.Set("has_key", configured)
	out.Set("key_hint", KeyHint(apiKey))
	out.Set("personal_config_required", !configured && !pyjson.Truthy(out.Value("personal_configured")))
	return out, nil
}
