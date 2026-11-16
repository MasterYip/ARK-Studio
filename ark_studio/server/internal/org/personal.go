package org

// Personal settings: SecretBox.seal, personal_settings_public,
// save_personal_assistant / save_personal_agents, personal_environment_public
// and save_personal_environment (org.py).

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"math"
	"math/big"
	"regexp"
	"sort"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/compat/pyurl"
	"arkstudio/go/internal/linuxacct"
	"arkstudio/go/internal/rnd"
)

// PersonalScopes is PERSONAL_SCOPES.
var PersonalScopes = []string{"assistant", "agents", "environment"}

var reasoningEfforts = map[string]bool{"low": true, "medium": true, "high": true, "max": true}

var (
	reProviderID = regexp.MustCompile(`\A[a-z][a-z0-9_-]{0,40}\z`)
	reEnvName    = regexp.MustCompile(`\A[A-Za-z_][A-Za-z0-9_]{0,63}\z`)
)

func b64(raw []byte) string { return strings.TrimRight(base64.URLEncoding.EncodeToString(raw), "=") }

// SealSecret is SecretBox.seal(plaintext, aad=aad); the nonce comes from the
// "org" stream like Python's org.secrets.
func SealSecret(plaintext, aad string) (string, error) {
	data := []byte(plaintext)
	nonce := rnd.Bytes("org", 16)
	key, err := secretKey()
	if err != nil {
		return "", err
	}
	stream := keystream(key, []byte(aad), nonce, len(data))
	cipher := make([]byte, len(data))
	for i := range data {
		cipher[i] = data[i] ^ stream[i]
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("ark-org-tag\x02"))
	mac.Write([]byte(aad))
	mac.Write(nonce)
	mac.Write(cipher)
	return strings.Join([]string{secretPrefix, b64(nonce), b64(cipher), b64(mac.Sum(nil))}, "$"), nil
}

func validateScope(scope string) (string, error) {
	text := pytext.Lower(pytext.Strip(scope))
	if text != "assistant" && text != "agents" && text != "environment" {
		return "", &Error{400, "scope must be assistant or agents", "INVALID_SCOPE"}
	}
	return text, nil
}

// personalUser is OrgManager._user_or_error (the user id).
func (m *Manager) personalUser(ctx context.Context, username string) (string, error) {
	user, ok, err := m.UserByName(ctx, username)
	if err != nil {
		return "", err
	}
	if !ok || pyjson.Truthy(user.Get("disabled")) {
		return "", &Error{403, "the account is not available", "ACCOUNT_UNAVAILABLE"}
	}
	return user.Str("id"), nil
}

// PersonalSettings is OrgManager.personal_settings (with opened secrets).
func (m *Manager) PersonalSettings(ctx context.Context, username, scope string) (*pyjson.Object, error) {
	s, err := validateScope(scope)
	if err != nil {
		return nil, err
	}
	return m.personalSettings(ctx, username, s)
}

func lastRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

// PersonalSettingsPublic is OrgManager.personal_settings_public.  scope is
// the caller's text: it is validated case-insensitively, but only the exact
// "assistant" selects the assistant view (as in Python).
func (m *Manager) PersonalSettingsPublic(ctx context.Context, username, scope string) (*pyjson.Object, error) {
	internal, err := m.PersonalSettings(ctx, username, scope)
	if err != nil {
		return nil, err
	}
	secretsMap, _ := internal.Delete("secrets").(*pyjson.Object)
	if secretsMap == nil {
		secretsMap = pyjson.NewObject()
	}
	if scope == "assistant" {
		values := pyjson.NewObject()
		for _, k := range []string{"base_url", "model", "reasoning_effort", "timeout"} {
			if v := internal.Value(k); !emptyish(v) {
				values.Set(k, v)
			}
		}
		overrides := values.Keys()
		sort.Strings(overrides)
		key := ""
		if v := secretsMap.Value("api_key"); pyjson.Truthy(v) {
			key = pyjson.Str(v)
		}
		return pyjson.NewObject("scope", "assistant", "user", username, "values", values,
			"overrides", strList(overrides), "has_key", key != "", "key_hint", KeyHint(key),
			"updated_at", internal.Value("updated_at"), "inherits", true), nil
	}
	providers, _ := internal.Value("providers").(*pyjson.Object)
	if providers == nil {
		providers = pyjson.NewObject()
	}
	namesBy := map[string][]any{}
	hints := map[string]*pyjson.Object{}
	for _, key := range secretsMap.Keys() {
		parts := strings.SplitN(key, ":", 3)
		if len(parts) != 3 || parts[0] != "env" {
			continue
		}
		pid, name := parts[1], parts[2]
		namesBy[pid] = append(namesBy[pid], name)
		value := pyjson.Str(secretsMap.Value(key))
		hint := "…"
		if pytext.Len(value) >= 4 {
			hint = "…" + lastRunes(value, 4)
		}
		if hints[pid] == nil {
			hints[pid] = pyjson.NewObject()
		}
		hints[pid].Set(name, hint)
	}
	idSet := map[string]bool{}
	for _, k := range providers.Keys() {
		idSet[k] = true
	}
	for k := range namesBy {
		idSet[k] = true
	}
	ids := make([]string, 0, len(idSet))
	for k := range idSet {
		ids = append(ids, k)
	}
	sort.Strings(ids)
	out := pyjson.NewObject()
	for _, pid := range ids {
		raw, _ := providers.Value(pid).(*pyjson.Object)
		if raw == nil {
			raw = pyjson.NewObject()
		}
		names := namesBy[pid]
		if names == nil {
			names = []any{}
		}
		hint := hints[pid]
		if hint == nil {
			hint = pyjson.NewObject()
		}
		command := raw.Value("command")
		if !pyjson.Truthy(command) {
			command = ""
		}
		enabled := true
		if v, ok := raw.Get("enabled"); ok {
			enabled = pyjson.Truthy(v)
		}
		overrides := []string{}
		for _, k := range []string{"command", "enabled"} {
			if raw.Has(k) {
				overrides = append(overrides, k)
			}
		}
		if len(names) > 0 {
			overrides = append(overrides, "env")
		}
		sort.Strings(overrides)
		out.Set(pid, pyjson.NewObject("command", command, "enabled", enabled, "env_names", names,
			"env_hints", hint, "overrides", strList(overrides)))
	}
	return pyjson.NewObject("scope", "agents", "user", username, "providers", out,
		"updated_at", internal.Value("updated_at"), "inherits", true), nil
}

func strList(list []string) []any {
	out := make([]any, len(list))
	for i, s := range list {
		out[i] = s
	}
	return out
}

func anyAsStr(v any) string {
	if !pyjson.Truthy(v) {
		return ""
	}
	return pyjson.Str(v)
}

func hasSpace(s string) bool {
	for _, r := range s {
		if pytext.IsSpace(r) {
			return true
		}
	}
	return false
}

func invalid(msg string) error { return &Error{400, msg, "INVALID_SETTINGS"} }

// cleanAssistantPatch is OrgManager._clean_assistant_patch.
func cleanAssistantPatch(patch *pyjson.Object) (*pyjson.Object, error) {
	allowed := map[string]bool{"base_url": true, "model": true, "reasoning_effort": true, "timeout": true,
		"api_key": true, "clear_key": true}
	var unknown []string
	for _, k := range patch.Keys() {
		if !allowed[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, invalid("unknown assistant setting: " + unknown[0])
	}
	clean := pyjson.NewObject()
	if patch.Has("base_url") {
		url := strings.TrimRight(pytext.Strip(anyAsStr(patch.Value("base_url"))), "/")
		if url != "" {
			parsed := pyurl.Parse(url)
			if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Netloc == "" ||
				hasSpace(url) || pytext.Len(url) > 400 {
				return nil, invalid("base_url must be an http(s) URL")
			}
		}
		clean.Set("base_url", url)
	}
	if patch.Has("model") {
		model := pytext.Strip(anyAsStr(patch.Value("model")))
		if model != "" && (pytext.Len(model) > 120 || hasSpace(model)) {
			return nil, invalid("model must be a short name without spaces")
		}
		clean.Set("model", model)
	}
	if patch.Has("reasoning_effort") {
		effort := pytext.Lower(pytext.Strip(anyAsStr(patch.Value("reasoning_effort"))))
		if effort != "" && !reasoningEfforts[effort] {
			return nil, invalid("reasoning_effort must be one of high, low, max, medium")
		}
		clean.Set("reasoning_effort", effort)
	}
	if patch.Has("timeout") {
		v := patch.Value("timeout")
		if emptyish(v) {
			clean.Set("timeout", nil)
		} else {
			n, ok, err := pyIntArg(v)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, invalid("timeout must be a number")
			}
			if n.Cmp(big.NewInt(5)) < 0 || n.Cmp(big.NewInt(300)) > 0 {
				return nil, invalid("timeout must be between 5 and 300 seconds")
			}
			clean.Set("timeout", n.Int64())
		}
	}
	return clean, nil
}

// pyIntArg is int(v) for a JSON value: ok=false for TypeError/ValueError;
// an OverflowError (float infinity) is returned as an internal error.
func pyIntArg(v any) (*big.Int, bool, error) {
	switch x := v.(type) {
	case bool:
		if x {
			return big.NewInt(1), true, nil
		}
		return big.NewInt(0), true, nil
	case int64:
		return big.NewInt(x), true, nil
	case *big.Int:
		return x, true, nil
	case float64:
		if x != x {
			return nil, false, nil
		}
		if math.IsInf(x, 0) {
			return nil, false, &internalErr{"cannot convert float infinity to integer"}
		}
		f := new(big.Float).SetFloat64(x)
		n, _ := f.Int(nil)
		return n, true, nil
	case string:
		n, err := pytext.Int(x)
		if err != nil {
			if b, ok := bigIntText(x); ok {
				return b, true, nil
			}
			return nil, false, nil
		}
		return big.NewInt(n), true, nil
	}
	return nil, false, nil
}

// bigIntText parses an ASCII decimal integer too large for int64.
func bigIntText(s string) (*big.Int, bool) {
	t := strings.ReplaceAll(pytext.Strip(s), "_", "")
	if t == "" || strings.Contains(s, "__") || strings.HasPrefix(pytext.Strip(s), "_") || strings.HasSuffix(pytext.Strip(s), "_") {
		return nil, false
	}
	b, ok := new(big.Int).SetString(t, 10)
	return b, ok
}

type internalErr struct{ msg string }

func (e *internalErr) Error() string { return e.msg }

// IsInternal reports a Python exception outside OrgError (500 "Internal error").
func IsInternal(err error) (string, bool) {
	if e, ok := err.(*internalErr); ok {
		return e.msg, true
	}
	return "", false
}

// SavePersonalAssistant is OrgManager.save_personal_assistant.
func (m *Manager) SavePersonalAssistant(ctx context.Context, username string, body *pyjson.Object) (*pyjson.Object, error) {
	uid, err := m.personalUser(ctx, username)
	if err != nil {
		return nil, err
	}
	current, err := m.personalSettings(ctx, username, "assistant")
	if err != nil {
		return nil, err
	}
	current.Delete("secrets")
	current.Delete("updated_at")
	// Untouched credentials keep their existing sealed token verbatim.
	sealed, err := m.sealedTokens(ctx, uid, "assistant")
	if err != nil {
		return nil, err
	}
	clean, err := cleanAssistantPatch(body)
	if err != nil {
		return nil, err
	}
	clearKey := pyjson.Truthy(body.Value("clear_key"))
	apiKey := pytext.Strip(anyAsStr(body.Value("api_key")))
	if apiKey != "" && pytext.Len(apiKey) > 4096 {
		return nil, invalid("api_key is too long")
	}
	data := pyjson.NewObject()
	for _, k := range []string{"base_url", "model", "reasoning_effort", "timeout"} {
		if v := current.Value(k); !emptyish(v) {
			data.Set(k, v)
		}
	}
	for _, k := range clean.Keys() {
		if v := clean.Value(k); emptyish(v) {
			data.Delete(k)
		} else {
			data.Set(k, v)
		}
	}
	if clearKey {
		sealed.Delete("api_key")
	} else if apiKey != "" {
		token, err := SealSecret(apiKey, secretAAD(uid, "assistant", "api_key"))
		if err != nil {
			return nil, err
		}
		sealed.Set("api_key", token)
	}
	blob, err := sealedBlob(sealed)
	if err != nil {
		return nil, err
	}
	if err := m.SaveSettings(ctx, uid, "assistant", data, &blob); err != nil {
		return nil, err
	}
	fields := clean.Keys()
	sort.Strings(fields)
	if err := m.Log(ctx, uid, "personal.assistant.save", "", pyjson.NewObject("fields", strList(fields),
		"key", sealed.Has("api_key"))); err != nil {
		return nil, err
	}
	return m.PersonalSettingsPublic(ctx, username, "assistant")
}

func isEmptyObject(v any) bool {
	o, ok := v.(*pyjson.Object)
	return ok && o.Len() == 0
}

// SavePersonalAgents is OrgManager.save_personal_agents.  allowed is
// org.allowed_providers (the session preset ids).
// Credentials the patch leaves alone keep their stored sealed tokens.
func (m *Manager) SavePersonalAgents(ctx context.Context, username string, body *pyjson.Object, allowed []string) (*pyjson.Object, error) {
	uid, err := m.personalUser(ctx, username)
	if err != nil {
		return nil, err
	}
	current, err := m.personalSettings(ctx, username, "agents")
	if err != nil {
		return nil, err
	}
	current.Delete("secrets")
	current.Delete("updated_at")
	commands := pyjson.NewObject()
	if p, ok := current.Value("providers").(*pyjson.Object); ok {
		commands = p.Copy()
	}
	patch, ok := body.Value("providers").(*pyjson.Object)
	if !ok {
		return nil, invalid("providers must be an object")
	}
	if len(allowed) > 0 {
		set := map[string]bool{}
		for _, a := range allowed {
			set[a] = true
		}
		for _, pid := range patch.Keys() {
			if !set[pytext.Lower(pid)] {
				return nil, invalid("unknown task-agent provider: " + pid)
			}
		}
	}
	// Keep previously sealed credentials (their stored tokens, never the
	// opened plaintext) unless the patch replaces or clears them.
	sealed, err := m.sealedTokens(ctx, uid, "agents")
	if err != nil {
		return nil, err
	}
	for _, rawID := range patch.Keys() {
		raw := patch.Value(rawID)
		pid := pytext.Lower(pytext.Strip(rawID))
		if !reProviderID.MatchString(pid) {
			return nil, invalid("invalid provider id: " + rawID)
		}
		prefix := "env:" + pid + ":"
		if raw == nil || isEmptyObject(raw) {
			commands.Delete(pid)
			for _, k := range sealed.Keys() {
				if strings.HasPrefix(k, prefix) {
					sealed.Delete(k)
				}
			}
			continue
		}
		rawObj, ok := raw.(*pyjson.Object)
		if !ok {
			return nil, invalid("provider " + pid + " settings must be an object")
		}
		item := pyjson.NewObject()
		if prev, ok := commands.Value(pid).(*pyjson.Object); ok {
			item = prev.Copy()
		}
		if rawObj.Has("command") {
			command := rawObj.Value("command")
			var parts []string
			list, isList := command.([]any)
			if isList {
				for _, p := range list {
					parts = append(parts, pyjson.Str(p))
				}
				if len(parts) == 0 || len(parts) > 24 {
					return nil, invalid("provider " + pid + " command is invalid")
				}
			} else if s := anyAsStr(command); pytext.Strip(s) != "" {
				parts = []string{s}
			}
			for _, part := range parts {
				if part == "" || pytext.Len(part) > 400 || strings.ContainsAny(part, "\r\n\x00") {
					return nil, invalid("provider " + pid + " command is invalid")
				}
			}
			if len(parts) == 0 {
				item.Delete("command")
			} else if isList {
				item.Set("command", strList(parts))
			} else {
				item.Set("command", parts[0])
			}
		}
		if rawObj.Has("enabled") {
			item.Set("enabled", pyjson.Truthy(rawObj.Value("enabled")))
		}
		if envPatch, ok := rawObj.Value("env").(*pyjson.Object); ok {
			for _, name := range envPatch.Keys() {
				envName := pytext.Strip(name)
				if !reEnvName.MatchString(envName) {
					return nil, invalid("invalid environment variable name: " + name)
				}
				text := anyAsStr(envPatch.Value(name))
				if pytext.Len(text) > 4096 {
					return nil, invalid("credential value is too long")
				}
				key := "env:" + pid + ":" + envName
				if text != "" {
					token, err := SealSecret(text, uid+":agents:"+key)
					if err != nil {
						return nil, err
					}
					sealed.Set(key, token)
				} else {
					sealed.Delete(key)
				}
			}
		}
		if clear, ok := rawObj.Value("clear_env").([]any); ok {
			for _, name := range clear {
				sealed.Delete("env:" + pid + ":" + pytext.Strip(anyAsStr(name)))
			}
		}
		hasEnv := false
		for _, k := range sealed.Keys() {
			if strings.HasPrefix(k, prefix) {
				hasEnv = true
				break
			}
		}
		if pyjson.Truthy(item.Value("command")) || item.Has("enabled") || hasEnv {
			commands.Set(pid, item)
		} else {
			commands.Delete(pid)
		}
	}
	blob, err := sealedBlob(sealed)
	if err != nil {
		return nil, err
	}
	if err := m.SaveSettings(ctx, uid, "agents", pyjson.NewObject("providers", commands), &blob); err != nil {
		return nil, err
	}
	ids := commands.Keys()
	sort.Strings(ids)
	if err := m.Log(ctx, uid, "personal.agents.save", "", pyjson.NewObject("providers", strList(ids),
		"secret_fields", int64(sealed.Len()))); err != nil {
		return nil, err
	}
	return m.PersonalSettingsPublic(ctx, username, "agents")
}

const environmentNote = "Only an organization admin can assign or remove a Linux " +
	"account. ARK launches task agents through `sudo -n` as the " +
	"selected account; there is no fallback to the server account."

// PersonalEnvironmentPublic is OrgManager.personal_environment_public.
func (m *Manager) PersonalEnvironmentPublic(ctx context.Context, username string) (*pyjson.Object, error) {
	if _, err := m.personalUser(ctx, username); err != nil {
		return nil, err
	}
	assigned, err := m.LinuxAccountsFor(ctx, username)
	if err != nil {
		return nil, err
	}
	selected, err := m.SelectedLinuxAccount(ctx, username)
	if err != nil {
		return nil, err
	}
	return pyjson.NewObject("scope", "environment", "user", username, "assigned", strList(assigned),
		"selected", selected, "capability", linuxacct.Available(), "note", environmentNote), nil
}

// SavePersonalEnvironment is OrgManager.save_personal_environment.
func (m *Manager) SavePersonalEnvironment(ctx context.Context, username string, body *pyjson.Object) (*pyjson.Object, error) {
	uid, err := m.personalUser(ctx, username)
	if err != nil {
		return nil, err
	}
	assigned, err := m.LinuxAccountsFor(ctx, username)
	if err != nil {
		return nil, err
	}
	if pyjson.Truthy(body.Value("test")) {
		account := anyAsStr(body.Value("linux_account"))
		if account == "" {
			if account, err = m.SelectedLinuxAccount(ctx, username); err != nil {
				return nil, err
			}
		}
		account = pytext.Strip(account)
		if !contains(assigned, account) {
			return nil, &Error{403, "You can only test a Linux account assigned to you by an organization admin.",
				"LINUX_ACCOUNT_NOT_ASSIGNED"}
		}
		info, err := linuxacct.Preflight(account, "")
		if err != nil {
			return nil, linuxErr(err)
		}
		if err := m.Log(ctx, uid, "personal.linux_account.test", "", pyjson.NewObject("account", account)); err != nil {
			return nil, err
		}
		return pyjson.NewObject("ok", true, "account", account, "home", info.Home, "uid", info.UID, "sudo", info.Sudo), nil
	}
	if body.Has("linux_account") {
		account := pytext.Strip(anyAsStr(body.Value("linux_account")))
		if account != "" && !contains(assigned, account) {
			return nil, &Error{403, "This Linux account is not assigned to you. Ask an organization admin to assign it first.",
				"LINUX_ACCOUNT_NOT_ASSIGNED"}
		}
		data := pyjson.NewObject()
		if account != "" {
			data.Set("linux_account", account)
		}
		if err := m.SaveSettings(ctx, uid, "environment", data, nil); err != nil {
			return nil, err
		}
		if err := m.Log(ctx, uid, "personal.linux_account.select", "", pyjson.NewObject("account", account)); err != nil {
			return nil, err
		}
	}
	return m.PersonalEnvironmentPublic(ctx, username)
}
