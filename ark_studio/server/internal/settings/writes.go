package settings

import (
	"math"
	"math/big"
	"os"
	"sort"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/compat/pyurl"
)

// Write side of arkstudio/settings.py: normalize_assistant(strict=True),
// update_assistant, normalize_agents(strict=True), update_agents,
// update_palette, _saved_palettes(strict=True), settings_patch.

var assistantFields = map[string]bool{"base_url": true, "model": true, "reasoning_effort": true, "timeout": true,
	"api_key_env": true, "enabled": true}

// pyIntArg is int(value) for normalize_assistant's timeout: ok=false is the
// TypeError/ValueError Python catches; an infinite float overflows (500).
func pyIntArg(v any) (int64, bool, error) {
	switch x := v.(type) {
	case bool:
		if x {
			return 1, true, nil
		}
		return 0, true, nil
	case int64:
		return x, true, nil
	case *big.Int:
		if x.IsInt64() {
			return x.Int64(), true, nil
		}
		if x.Sign() > 0 {
			return math.MaxInt64, true, nil
		}
		return math.MinInt64, true, nil
	case float64:
		if math.IsInf(x, 0) {
			return 0, false, &InternalError{Message: "cannot convert float infinity to integer"}
		}
		if math.IsNaN(x) {
			return 0, false, nil
		}
		return int64(x), true, nil
	case string:
		n, err := pytext.Int(x)
		if err != nil {
			if _, ok := err.(*pytext.IntError); ok {
				return 0, false, nil
			}
			return math.MaxInt64, true, nil
		}
		return n, true, nil
	}
	return 0, false, nil
}

// InternalError is a Python exception outside SettingsError/ValueError: the
// handler answers 500 "Internal error: <message>".
type InternalError struct{ Message string }

func (e *InternalError) Error() string { return e.Message }

func validateProjectEnvName(name, field string) (string, error) {
	text := pytext.Strip(name)
	if forbiddenEnvRe.MatchString(text) {
		return "", &Error{field + " must not reference ARK_/TOKEN/SECRET/PASSWORD/CREDENTIAL " +
			"environment variables; use a personal setting for credentials"}
	}
	return text, nil
}

// normalizeAssistantStrict is normalize_assistant(value, strict=True).
func normalizeAssistantStrict(value any) (*pyjson.Object, error) {
	source, _ := value.(*pyjson.Object)
	out := pyjson.NewObject()
	if !absent(source, "base_url") {
		raw := source.Value("base_url")
		text := ""
		if pyjson.Truthy(raw) {
			text = pyjson.Str(raw)
		}
		url := strings.TrimRight(pytext.Strip(text), "/")
		p := pyurl.Parse(url)
		if (p.Scheme != "http" && p.Scheme != "https") || p.Netloc == "" {
			return nil, &Error{"assistant base_url must be an http(s) URL"}
		}
		if strings.ContainsFunc(url, pytext.IsSpace) || pytext.Len(url) > 400 {
			return nil, &Error{"assistant base_url is invalid"}
		}
		out.Set("base_url", url)
	}
	if !absent(source, "model") {
		model := pytext.Strip(pyjson.Str(source.Value("model")))
		if model == "" || pytext.Len(model) > 120 || strings.ContainsFunc(model, pytext.IsSpace) {
			return nil, &Error{"assistant model must be a short name without spaces"}
		}
		out.Set("model", model)
	}
	if !absent(source, "reasoning_effort") {
		effort := pytext.Lower(pytext.Strip(pyjson.Str(source.Value("reasoning_effort"))))
		if effort != "low" && effort != "medium" && effort != "high" && effort != "max" {
			return nil, &Error{"assistant reasoning_effort must be one of high, low, max, medium"}
		}
		out.Set("reasoning_effort", effort)
	}
	if !absent(source, "timeout") {
		n, ok, err := pyIntArg(source.Value("timeout"))
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, &Error{"assistant timeout must be a number"}
		}
		if n < 5 || n > 300 {
			return nil, &Error{"assistant timeout must be between 5 and 300 seconds"}
		}
		out.Set("timeout", n)
	}
	if !absent(source, "api_key_env") {
		name := pytext.Strip(pyjson.Str(source.Value("api_key_env")))
		if !envNameRe.MatchString(name) {
			return nil, &Error{"assistant api_key_env must be an environment variable name"}
		}
		text, err := validateProjectEnvName(name, "assistant api_key_env")
		if err != nil {
			return nil, err
		}
		if !keyAllowlist()[strings.ToUpper(text)] {
			return nil, &Error{"assistant api_key_env is not an allowed provider key; use a known " +
				"model API key name or ask the operator to extend " +
				"ARK_PROJECT_ASSISTANT_KEY_ENVS"}
		}
		out.Set("api_key_env", text)
	}
	if v, ok := source.Get("enabled"); ok && v != nil {
		out.Set("enabled", pyjson.Truthy(v))
	}
	return out, nil
}

func noneOrEmpty(v any) bool { return v == nil || v == "" }

// UpdateAssistant is update_assistant(base, patch).
func UpdateAssistant(base, patch any) (*pyjson.Object, error) {
	merged, err := normalizeAssistantStrict(base)
	if err != nil {
		return nil, err
	}
	if patch == nil {
		return pyjson.NewObject(), nil
	}
	p, ok := patch.(*pyjson.Object)
	if !ok {
		return nil, &Error{"assistant settings must be an object"}
	}
	var unknown []string
	for _, k := range p.Keys() {
		if !assistantFields[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, &Error{"unknown assistant setting: " + unknown[0]}
	}
	for _, k := range p.Keys() {
		if noneOrEmpty(p.Value(k)) {
			merged.Delete(k)
		}
	}
	combined := merged.Copy()
	for _, k := range p.Keys() {
		if v := p.Value(k); !noneOrEmpty(v) {
			combined.Set(k, v)
		}
	}
	return normalizeAssistantStrict(combined)
}

// PresetIDs is the id of every task-agent preset (sessions.default_presets).
func PresetIDs() []string {
	ids := []string{"echo", "dsh", "claude", "codex", "opencode", "cursor", "gemini", "shell"}
	if pytext.Strip(os.Getenv("ARK_TASK_CUSTOM_COMMAND")) != "" {
		ids = append(ids, "custom")
	}
	return ids
}

func allowedSet(allowed []string) map[string]bool {
	out := map[string]bool{}
	for _, a := range allowed {
		out[strings.ToLower(a)] = true
	}
	return out
}

// normalizeAgentsStrict is normalize_agents(value, allowed_providers, strict=True).
func normalizeAgentsStrict(value any, allowedProviders []string) (*pyjson.Object, error) {
	source, _ := value.(*pyjson.Object)
	providers := pyjson.NewObject()
	out := pyjson.NewObject("providers", providers)
	raw, ok := source.Value("providers").(*pyjson.Object)
	if !ok {
		return out, nil
	}
	allowed := allowedSet(allowedProviders)
	for _, providerID := range raw.Keys() {
		pid := pytext.Lower(pytext.Strip(providerID))
		if !providerIDRe.MatchString(pid) {
			return nil, &Error{"invalid task-agent provider id: " + providerID}
		}
		if len(allowed) > 0 && !allowed[pid] {
			return nil, &Error{"unknown task-agent provider: " + pid}
		}
		item, ok := raw.Value(providerID).(*pyjson.Object)
		if !ok {
			return nil, &Error{"provider " + pid + " settings must be an object"}
		}
		clean := pyjson.NewObject()
		envNames, has := item.Get("env_names")
		list, isList := envNames.([]any)
		if has && envNames != nil && !(isList && len(list) == 0) {
			if !isList {
				return nil, &Error{"provider " + pid + " env_names must be a list"}
			}
			names := []any{}
			seen := map[string]bool{}
			for _, n := range list {
				text := ""
				if pyjson.Truthy(n) {
					text = pytext.Strip(pyjson.Str(n))
				}
				if !envNameRe.MatchString(text) {
					return nil, &Error{"provider " + pid + " env_names must be variable names"}
				}
				t, err := validateProjectEnvName(text, "provider "+pid+" env_names")
				if err != nil {
					return nil, err
				}
				if !seen[t] {
					seen[t] = true
					names = append(names, t)
				}
			}
			clean.Set("env_names", names)
		}
		if v, ok := item.Get("enabled"); ok && v != nil {
			clean.Set("enabled", pyjson.Truthy(v))
		}
		providers.Set(pid, clean)
	}
	return out, nil
}

// UpdateAgents is update_agents(base, patch, allowed_providers).
func UpdateAgents(base, patch any, allowedProviders []string) (*pyjson.Object, error) {
	merged, err := normalizeAgentsStrict(base, nil)
	if err != nil {
		return nil, err
	}
	if patch == nil {
		return pyjson.NewObject("providers", pyjson.NewObject()), nil
	}
	p, ok := patch.(*pyjson.Object)
	if !ok {
		return nil, &Error{"agents settings must be an object"}
	}
	var unknown []string
	for _, k := range p.Keys() {
		if k != "providers" {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, &Error{"unknown agents setting: " + unknown[0]}
	}
	providersRaw := p.Value("providers")
	if providersRaw == nil {
		return merged, nil
	}
	providers, ok := providersRaw.(*pyjson.Object)
	if !ok {
		return nil, &Error{"agents.providers must be an object"}
	}
	allowed := allowedSet(allowedProviders)
	mp := merged.Value("providers").(*pyjson.Object)
	for _, providerID := range providers.Keys() {
		pid := pytext.Lower(pytext.Strip(providerID))
		if len(allowed) > 0 && !allowed[pid] {
			return nil, &Error{"unknown task-agent provider: " + pid}
		}
		raw := providers.Value(providerID)
		if o, isObj := raw.(*pyjson.Object); raw == nil || (isObj && o.Len() == 0) {
			mp.Delete(pid)
			continue
		}
		item, ok := raw.(*pyjson.Object)
		if !ok {
			return nil, &Error{"provider " + pid + " settings must be an object"}
		}
		if item.Has("command") || item.Has("label") {
			return nil, &Error{"project settings must not configure task-agent commands or labels; " +
				"commands are personal settings (personal override > project > " +
				"server/env)"}
		}
		current := pyjson.NewObject()
		if c, ok := mp.Value(pid).(*pyjson.Object); ok {
			current = c.Copy()
		}
		for _, k := range item.Keys() {
			current.Set(k, item.Value(k))
		}
		norm, err := normalizeAgentsStrict(pyjson.NewObject("providers", pyjson.NewObject(pid, current)), allowedProviders)
		if err != nil {
			return nil, err
		}
		mp.Set(pid, norm.Value("providers").(*pyjson.Object).Value(pid))
	}
	return merged, nil
}

// savedPalettesStrict is _saved_palettes(value, strict=True).
func savedPalettesStrict(value any) (*pyjson.Object, error) {
	result := pyjson.NewObject()
	if value == nil {
		return result, nil
	}
	src, ok := value.(*pyjson.Object)
	if !ok || src.Len() > 24 {
		return nil, &Error{"saved palettes must be an object with at most 24 themes"}
	}
	allowed := map[string]bool{"preset": true}
	for _, k := range PaletteKeys {
		allowed[k] = true
	}
	for _, name := range src.Keys() {
		n := pytext.Len(name)
		if n < 1 || n > 64 || name != pytext.Strip(name) || strings.ContainsFunc(name, func(r rune) bool { return r < 32 }) ||
			name == "__proto__" || name == "constructor" || name == "prototype" {
			return nil, &Error{"saved theme name must contain 1 to 64 visible characters"}
		}
		pair, ok := src.Value(name).(*pyjson.Object)
		if !ok || pair.Len() != 2 || !pair.Has("light") || !pair.Has("dark") {
			return nil, &Error{"saved theme must contain light and dark palettes"}
		}
		colors := pyjson.NewObject()
		for _, mode := range []string{"light", "dark"} {
			def := map[string]string{"light": "ark-light", "dark": "ark"}[mode]
			patch, ok := pair.Value(mode).(*pyjson.Object)
			if !ok {
				return nil, &Error{"saved theme accepts only palette colors and preset"}
			}
			for _, k := range patch.Keys() {
				if !allowed[k] {
					return nil, &Error{"saved theme accepts only palette colors and preset"}
				}
			}
			merged, err := updatePaletteColors(pyjson.NewObject("preset", def), patch)
			if err != nil {
				return nil, err
			}
			if PaletteMode(merged) != mode {
				return nil, &Error{mode + " palette background must match its mode"}
			}
			colors.Set(mode, merged)
		}
		result.Set(name, colors)
	}
	return result, nil
}

// UpdatePalette is update_palette(base, patch).
func UpdatePalette(base, patch any) (*pyjson.Object, error) {
	p, ok := patch.(*pyjson.Object)
	if !ok {
		return nil, &Error{"palette settings must be an object"}
	}
	current := NormalizePalette(base)
	modes := map[string]*pyjson.Object{"light": current.Value("light").(*pyjson.Object), "dark": current.Value("dark").(*pyjson.Object)}
	anyMode := false
	for _, mode := range []string{"light", "dark"} {
		if !p.Has(mode) {
			continue
		}
		anyMode = true
		mp, ok := p.Value(mode).(*pyjson.Object)
		if !ok {
			return nil, &Error{mode + " palette must be an object"}
		}
		next, err := updatePaletteColors(modes[mode], mp)
		if err != nil {
			return nil, err
		}
		if PaletteMode(next) != mode {
			return nil, &Error{mode + " palette background must match its mode"}
		}
		modes[mode] = next
	}
	legacyPatch := pyjson.NewObject()
	for _, k := range p.Keys() {
		if k == "preset" || containsStr(PaletteKeys, k) {
			legacyPatch.Set(k, p.Value(k))
		}
	}
	var legacy *pyjson.Object
	if legacyPatch.Len() > 0 {
		var err error
		if legacy, err = updatePaletteColors(current, legacyPatch); err != nil {
			return nil, err
		}
	} else {
		legacy = paletteColors(current, "ark")
	}
	if legacyPatch.Len() > 0 && !anyMode {
		modes[PaletteMode(legacy)] = legacy
	} else if anyMode {
		legacy = modes[PaletteMode(legacy)]
	}
	saved := current.Value("saved").(*pyjson.Object).Copy()
	if p.Has("saved") {
		sp, ok := p.Value("saved").(*pyjson.Object)
		if !ok {
			return nil, &Error{"saved palettes must be an object"}
		}
		for _, name := range sp.Keys() {
			pair := sp.Value(name)
			if pair == nil {
				saved.Delete(name)
				continue
			}
			one, err := savedPalettesStrict(pyjson.NewObject(name, pair))
			if err != nil {
				return nil, err
			}
			for _, k := range one.Keys() {
				saved.Set(k, one.Value(k))
			}
		}
		var err error
		if saved, err = savedPalettesStrict(saved); err != nil {
			return nil, err
		}
	}
	enabled := current.Value("enabled")
	if v, ok := p.Get("enabled"); ok {
		enabled = v
	}
	out := pyjson.NewObject("enabled", pyjson.Truthy(enabled))
	for _, k := range legacy.Keys() {
		out.Set(k, legacy.Value(k))
	}
	out.Set("light", modes["light"]).Set("dark", modes["dark"]).Set("saved", saved)
	return out, nil
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// SettingsPatch is settings_patch(manifest, body, allowed_providers).
func SettingsPatch(manifest *pyjson.Object, body *pyjson.Object, allowedProviders []string) (*pyjson.Object, error) {
	current, err := ProjectSettings(manifest)
	if err != nil {
		return nil, err
	}
	if body == nil {
		body = pyjson.NewObject()
	}
	known := map[string]bool{"graph": true, "arrows": true, "assistant": true, "agents": true, "palette": true, "sessions": true}
	hasSection := false
	for k := range known {
		if body.Has(k) {
			hasSection = true
		}
	}
	if !hasSection {
		g, err := UpdateGraph(current.Value("graph"), body)
		if err != nil {
			return nil, err
		}
		return current.Set("graph", g), nil
	}
	var unknown []string
	for _, k := range body.Keys() {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, &Error{"unknown settings section: " + unknown[0]}
	}
	if body.Has("graph") {
		g, err := UpdateGraph(current.Value("graph"), body.Value("graph"))
		if err != nil {
			return nil, err
		}
		current.Set("graph", g)
	}
	if body.Has("arrows") {
		a, err := UpdateArrows(current.Value("arrows"), body.Value("arrows"))
		if err != nil {
			return nil, err
		}
		current.Set("arrows", a)
	}
	if body.Has("assistant") {
		a, err := UpdateAssistant(current.Value("assistant"), body.Value("assistant"))
		if err != nil {
			return nil, err
		}
		current.Set("assistant", a)
	}
	if body.Has("agents") {
		a, err := UpdateAgents(current.Value("agents"), body.Value("agents"), allowedProviders)
		if err != nil {
			return nil, err
		}
		current.Set("agents", a)
	}
	if body.Has("palette") {
		pl, err := UpdatePalette(current.Value("palette"), body.Value("palette"))
		if err != nil {
			return nil, err
		}
		current.Set("palette", pl)
	}
	if body.Has("sessions") {
		s, err := UpdateSessions(current.Value("sessions"), body.Value("sessions"))
		if err != nil {
			return nil, err
		}
		current.Set("sessions", s)
	}
	return current, nil
}

// NormalizeSessions is normalize_sessions: session-context upload consent.
func NormalizeSessions(value any) *pyjson.Object {
	src, _ := value.(*pyjson.Object)
	return pyjson.NewObject("accept_uploads", src.Value("accept_uploads") == true)
}

// UpdateSessions is update_sessions(base, patch).
func UpdateSessions(base, patch any) (*pyjson.Object, error) {
	p, ok := patch.(*pyjson.Object)
	if !ok {
		return nil, &Error{"sessions settings must be an object"}
	}
	var unknown []string
	for _, k := range p.Keys() {
		if k != "accept_uploads" {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, &Error{"unknown sessions setting: " + unknown[0]}
	}
	current := NormalizeSessions(base)
	if v, has := p.Get("accept_uploads"); has {
		b, ok := v.(bool)
		if !ok {
			return nil, &Error{"sessions.accept_uploads must be true or false"}
		}
		current.Set("accept_uploads", b)
	}
	return current, nil
}
