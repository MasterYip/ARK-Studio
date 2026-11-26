// Package settings ports the read path of arkstudio/settings.py:
// project_settings(manifest) and the normalize_* helpers it calls with
// strict=False.
package settings

import (
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/compat/pyurl"
	"arkstudio/go/internal/store/markers"
)

// Error is SettingsError (a ValueError in Python).
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

var graphKeys = []string{"repulsion", "distance", "gravity", "damping"}
var graphRanges = map[string][2]float64{"repulsion": {0, 16000}, "distance": {0, 320}, "gravity": {0, 0.02}, "damping": {0, 1}}
var graphDefaults = map[string]float64{"repulsion": 3286.0639999999985, "distance": 0.0, "gravity": 0.011506123058542407, "damping": 0.3}

// Go-only: optional "cluster by type" physics (web/js/graph-core.js
// applyClusterForces).  Stored and returned only once set, so projects that
// never touch them answer exactly as before; the client treats a missing
// key as its default (cluster off, strengths 0.5, grouping by type).
var graphClusterKeys = []string{"cluster", "cluster_attract", "cluster_repel", "cluster_by"}
var graphClusterGroupings = []string{"type", "status", "tag"}

// graphClusterValue validates one clustering field.
func graphClusterValue(key string, value any) (any, error) {
	switch key {
	case "cluster":
		if b, ok := value.(bool); ok {
			return b, nil
		}
		return nil, &Error{"cluster must be true or false"}
	case "cluster_by":
		if s, ok := value.(string); ok && contains(graphClusterGroupings, s) {
			return s, nil
		}
		return nil, &Error{"cluster_by must be one of " + strings.Join(graphClusterGroupings, ", ")}
	}
	n, ok := gdNumberValue(value)
	if !ok {
		return nil, &Error{key + " must be a number"}
	}
	if n < 0 || n > 1 {
		return nil, &Error{key + " must be between 0 and 1"}
	}
	return n, nil
}

// PaletteKeys is PALETTE_KEYS.
var PaletteKeys = []string{"accent", "accent2", "accent_soft", "background", "surface", "text", "muted", "line", "success", "warning", "danger"}

// PresetOrder is the insertion order of PALETTE_PRESETS.
var PresetOrder = []string{"ark-light", "indigo-light", "forest-light", "sunset-light", "mono-light", "ark", "indigo", "forest", "sunset", "mono"}

// Presets is PALETTE_PRESETS (values in PaletteKeys order).
var Presets = map[string][]string{
	"ark-light":    {"#2563eb", "#1d4ed8", "#eaf1ff", "#f5f6f8", "#ffffff", "#1a2231", "#5b6474", "#d6dce5", "#047857", "#b45309", "#dc2626"},
	"indigo-light": {"#4f46e5", "#6d28d9", "#eeecff", "#f6f5fb", "#ffffff", "#24213b", "#625c7b", "#dcd8eb", "#047857", "#b45309", "#be123c"},
	"forest-light": {"#047857", "#0f766e", "#e1f3ec", "#f3f8f5", "#ffffff", "#172c24", "#536c60", "#d1e0d7", "#047857", "#b45309", "#b91c1c"},
	"sunset-light": {"#c2410c", "#9a3412", "#fff0e5", "#fbf6f3", "#ffffff", "#35251f", "#765e52", "#e6d8cf", "#047857", "#b45309", "#b91c1c"},
	"mono-light":   {"#475569", "#334155", "#e8ecf0", "#f6f7f8", "#ffffff", "#20252c", "#59616c", "#d5d9de", "#047857", "#a16207", "#b91c1c"},
	"ark":          {"#3b82f6", "#60a5fa", "#172554", "#0f141c", "#1d2532", "#e8eef6", "#93a2b6", "#273243", "#34d399", "#f59e0b", "#ef4444"},
	"indigo":       {"#6366f1", "#a78bfa", "#211a4d", "#101222", "#1c1f36", "#ecebff", "#a5a3c4", "#303456", "#34d399", "#fbbf24", "#fb7185"},
	"forest":       {"#10b981", "#6ee7b7", "#062e25", "#0d1714", "#172722", "#e7f7ef", "#94b8a8", "#28443a", "#34d399", "#f59e0b", "#f87171"},
	"sunset":       {"#f97316", "#fbbf24", "#451a03", "#1a1110", "#2a1b18", "#fff1e8", "#c4a294", "#4a2d24", "#34d399", "#fbbf24", "#ef4444"},
	"mono":         {"#94a3b8", "#cbd5e1", "#1e293b", "#0f1216", "#1b2028", "#f1f5f9", "#94a3b8", "#303845", "#a7f3d0", "#fde68a", "#fecaca"},
}

var hexRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}\z`)

// NormalizeGraph is normalize_graph.
func NormalizeGraph(value any) *pyjson.Object {
	source, _ := value.(*pyjson.Object)
	out := pyjson.NewObject()
	for _, key := range graphKeys {
		n, ok := pytext.FloatValue(source.Value(key))
		if !ok || math.IsInf(n, 0) || math.IsNaN(n) {
			n = graphDefaults[key]
		}
		r := graphRanges[key]
		out.Set(key, math.Max(r[0], math.Min(r[1], n)))
	}
	for _, key := range graphClusterKeys {
		if item, has := source.Get(key); has && item != nil {
			if clean, err := graphClusterValue(key, item); err == nil {
				out.Set(key, clean)
			}
		}
	}
	return out
}

func presetObject(name string) *pyjson.Object {
	o := pyjson.NewObject()
	for i, k := range PaletteKeys {
		o.Set(k, Presets[name][i])
	}
	return o
}

// PresetsObject is PALETTE_PRESETS as an ordered object.
func PresetsObject() *pyjson.Object {
	o := pyjson.NewObject()
	for _, name := range PresetOrder {
		o.Set(name, presetObject(name))
	}
	return o
}

func paletteColors(value any, def string) *pyjson.Object {
	source, _ := value.(*pyjson.Object)
	preset := ""
	if v := source.Value("preset"); pyjson.Truthy(v) {
		preset = strings.ToLower(pytext.Strip(pyjson.Str(v)))
	}
	if _, ok := Presets[preset]; !ok && preset != "custom" {
		preset = def
	}
	base := preset
	if _, ok := Presets[base]; !ok {
		base = def
	}
	colors := presetObject(base)
	for _, key := range PaletteKeys {
		if raw, ok := source.Value(key).(string); ok && hexRe.MatchString(pytext.Strip(raw)) {
			colors.Set(key, strings.ToLower(pytext.Strip(raw)))
		}
	}
	out := pyjson.NewObject("preset", preset)
	return out.Update(colors)
}

// PaletteMode is palette_mode.
func PaletteMode(colors *pyjson.Object) string {
	color, _ := colors.Value("background").(string)
	if color == "" {
		color = "#0f141c"
	}
	var lum float64
	weights := []float64{.2126, .7152, .0722}
	for i, off := range []int{1, 3, 5} {
		n, _ := strconv.ParseInt(color[off:off+2], 16, 64)
		v := float64(n) / 255
		if v <= .04045 {
			v = v / 12.92
		} else {
			v = math.Pow((v+.055)/1.055, 2.4)
		}
		lum += v * weights[i]
	}
	if lum > .179 {
		return "light"
	}
	return "dark"
}

// NormalizePalette is normalize_palette (saved palettes are read leniently).
func NormalizePalette(value any) *pyjson.Object {
	source, _ := value.(*pyjson.Object)
	legacy := paletteColors(source, "ark")
	modes := map[string]*pyjson.Object{
		"light": paletteColors(source.Value("light"), "ark-light"),
		"dark":  paletteColors(source.Value("dark"), "ark"),
	}
	_, l := source.Value("light").(*pyjson.Object)
	_, d := source.Value("dark").(*pyjson.Object)
	if !l && !d {
		modes[PaletteMode(legacy)] = legacy
	}
	enabled := false
	if source.Has("enabled") {
		enabled = pyjson.Truthy(source.Value("enabled"))
	}
	out := pyjson.NewObject("enabled", enabled)
	out.Update(legacy)
	out.Set("light", modes["light"]).Set("dark", modes["dark"])
	out.Set("saved", savedPalettes(source.Value("saved")))
	return out
}

func savedPalettes(value any) *pyjson.Object {
	result := pyjson.NewObject()
	src, ok := value.(*pyjson.Object)
	if value == nil || !ok || src.Len() > 24 {
		return result
	}
	allowed := map[string]bool{"preset": true}
	for _, k := range PaletteKeys {
		allowed[k] = true
	}
	for _, name := range src.Keys() {
		pair, ok := src.Value(name).(*pyjson.Object)
		n := pytext.Len(name)
		if n < 1 || n > 64 || name != strings.TrimFunc(name, pytext.IsSpace) || strings.ContainsFunc(name, func(r rune) bool { return r < 32 }) ||
			name == "__proto__" || name == "constructor" || name == "prototype" {
			continue
		}
		if !ok || pair.Len() != 2 || !pair.Has("light") || !pair.Has("dark") {
			continue
		}
		colors := pyjson.NewObject()
		valid := true
		for _, mode := range []string{"light", "dark"} {
			def := map[string]string{"light": "ark-light", "dark": "ark"}[mode]
			patch, ok := pair.Value(mode).(*pyjson.Object)
			if !ok {
				valid = false
				break
			}
			for _, k := range patch.Keys() {
				if !allowed[k] {
					valid = false
				}
			}
			if !valid {
				break
			}
			merged, err := updatePaletteColors(pyjson.NewObject("preset", def), patch)
			if err != nil || PaletteMode(merged) != mode {
				valid = false
				break
			}
			colors.Set(mode, merged)
		}
		if valid {
			result.Set(name, colors)
		}
	}
	return result
}

func updatePaletteColors(base, patch *pyjson.Object) (*pyjson.Object, error) {
	current := paletteColors(base, "ark")
	merged := current.Copy()
	if patch.Has("preset") {
		preset := ""
		if v := patch.Value("preset"); pyjson.Truthy(v) {
			preset = strings.ToLower(pytext.Strip(pyjson.Str(v)))
		}
		if _, ok := Presets[preset]; !ok && preset != "custom" {
			return nil, &Error{"unknown palette preset: " + preset}
		}
		if preset != current.Value("preset") {
			if preset == "custom" {
				merged.Set("preset", "custom")
			} else {
				merged = paletteColors(pyjson.NewObject("preset", preset), "ark")
			}
		}
	}
	for _, key := range PaletteKeys {
		if !patch.Has(key) {
			continue
		}
		raw, ok := patch.Value(key).(string)
		if !ok || !hexRe.MatchString(pytext.Strip(raw)) {
			return nil, &Error{key + " must be a #RRGGBB colour"}
		}
		merged.Set(key, strings.ToLower(pytext.Strip(raw)))
	}
	return paletteColors(merged, "ark"), nil
}

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}\z`)
var providerIDRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,40}\z`)
var forbiddenEnvRe = regexp.MustCompile(`(?i)^ARK_|TOKEN|SECRET|PASSWORD|CREDENTIAL`)
var assistantKeyEnvs = []string{"DEEPSEEK_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "TENCENTHUB_API_KEY", "GEMINI_API_KEY",
	"GOOGLE_API_KEY", "OPENROUTER_API_KEY", "GROQ_API_KEY", "CURSOR_API_KEY", "DSH_API_KEY"}

func keyAllowlist() map[string]bool {
	out := map[string]bool{}
	if override := pytext.Strip(os.Getenv("ARK_PROJECT_ASSISTANT_KEY_ENVS")); override != "" {
		for _, item := range strings.Split(override, ",") {
			if t := pytext.Strip(item); t != "" {
				out[strings.ToUpper(t)] = true
			}
		}
		return out
	}
	for _, k := range assistantKeyEnvs {
		out[k] = true
	}
	return out
}

func absent(o *pyjson.Object, key string) bool {
	v, ok := o.Get(key)
	return !ok || v == nil || v == ""
}

// NormalizeAssistant is normalize_assistant(strict=False).
func NormalizeAssistant(value any) (*pyjson.Object, error) {
	source, _ := value.(*pyjson.Object)
	out := pyjson.NewObject()
	if !absent(source, "base_url") {
		url := strings.TrimRight(pytext.Strip(pyjson.Str(source.Value("base_url"))), "/")
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
		effort := strings.ToLower(pytext.Strip(pyjson.Str(source.Value("reasoning_effort"))))
		if effort != "low" && effort != "medium" && effort != "high" && effort != "max" {
			return nil, &Error{"assistant reasoning_effort must be one of high, low, max, medium"}
		}
		out.Set("reasoning_effort", effort)
	}
	if !absent(source, "timeout") {
		var n int64
		var err error
		switch x := source.Value("timeout").(type) {
		case float64:
			n = int64(x)
		case bool:
			n = map[bool]int64{true: 1, false: 0}[x]
		case int64:
			n = x
		case string:
			n, err = pytext.Int(x)
		default:
			err = &Error{}
		}
		if err != nil {
			return nil, &Error{"assistant timeout must be a number"}
		}
		if n < 5 || n > 300 {
			return nil, &Error{"assistant timeout must be between 5 and 300 seconds"}
		}
		out.Set("timeout", n)
	}
	if !absent(source, "api_key_env") {
		name := pytext.Strip(pyjson.Str(source.Value("api_key_env")))
		if envNameRe.MatchString(name) && !forbiddenEnvRe.MatchString(name) && keyAllowlist()[strings.ToUpper(name)] {
			out.Set("api_key_env", name)
		}
	}
	if v, ok := source.Get("enabled"); ok && v != nil {
		out.Set("enabled", pyjson.Truthy(v))
	}
	return out, nil
}

// NormalizeAgents is normalize_agents(strict=False) without a provider
// allow-list (project_settings passes none).
func NormalizeAgents(value any) *pyjson.Object {
	source, _ := value.(*pyjson.Object)
	providers := pyjson.NewObject()
	out := pyjson.NewObject("providers", providers)
	raw, ok := source.Value("providers").(*pyjson.Object)
	if !ok {
		return out
	}
	for _, providerID := range raw.Keys() {
		pid := strings.ToLower(pytext.Strip(providerID))
		if !providerIDRe.MatchString(pid) {
			continue
		}
		item, ok := raw.Value(providerID).(*pyjson.Object)
		if !ok {
			continue
		}
		clean := pyjson.NewObject()
		envNames, has := item.Get("env_names")
		list, isList := envNames.([]any)
		if has && envNames != nil && !(isList && len(list) == 0) {
			if !isList {
				continue
			}
			names := []any{}
			seen := map[string]bool{}
			for _, n := range list {
				text := ""
				if pyjson.Truthy(n) {
					text = pytext.Strip(pyjson.Str(n))
				}
				if !envNameRe.MatchString(text) || forbiddenEnvRe.MatchString(text) || seen[text] {
					continue
				}
				seen[text] = true
				names = append(names, text)
			}
			clean.Set("env_names", names)
		}
		if v, ok := item.Get("enabled"); ok && v != nil {
			clean.Set("enabled", pyjson.Truthy(v))
		}
		providers.Set(pid, clean)
	}
	return out
}

// ProjectSettings is project_settings(manifest).
func ProjectSettings(manifest *pyjson.Object) (*pyjson.Object, error) {
	s, _ := manifest.Value("settings").(*pyjson.Object)
	assistant, err := NormalizeAssistant(s.Value("assistant"))
	if err != nil {
		return nil, err
	}
	out := pyjson.NewObject("graph", NormalizeGraph(s.Value("graph")), "arrows", NormalizeArrows(s.Value("arrows")),
		"assistant", assistant,
		"agents", NormalizeAgents(s.Value("agents")), "palette", NormalizePalette(s.Value("palette")),
		"sessions", NormalizeSessions(s.Value("sessions")))
	// Go-only (T-0036): present only once a project admin has set a value,
	// so projects without one answer exactly like Python.
	if display, _ := GraphDisplay(s.Value("graph_display"), false); display.Len() > 0 {
		out.Set("graph_display", display)
	}
	// Go-only: node marker groups (internal/store/markers), present only once set.
	if groups, err := markers.Parse(s.Value("markers")); err == nil && groups != nil {
		out.Set("markers", markers.ToJSON(groups))
	}
	// Go-only: the tag namespace of lines of work (store/branches.go),
	// present only once set.
	if b, err := Branches(s.Value("branches")); err == nil && b != nil {
		out.Set("branches", b)
	}
	return out, nil
}

// UpdateGraph is update_graph(base, patch).
func UpdateGraph(base any, patch any) (*pyjson.Object, error) {
	merged := NormalizeGraph(base)
	p, ok := patch.(*pyjson.Object)
	if !ok {
		return nil, &Error{"graph settings must be an object"}
	}
	for _, key := range p.Keys() {
		if contains(graphClusterKeys, key) {
			clean, err := graphClusterValue(key, p.Value(key))
			if err != nil {
				return nil, err
			}
			merged.Set(key, clean)
			continue
		}
		r, known := graphRanges[key]
		if !known {
			return nil, &Error{"unknown graph setting: " + key}
		}
		value := p.Value(key)
		switch value.(type) {
		case int64, float64, string:
		default:
			if _, isBig := value.(interface{ String() string }); !isBig {
				return nil, &Error{key + " must be a number"}
			}
		}
		n, ok := pytext.FloatValue(value)
		if !ok {
			return nil, &Error{key + " must be a number"}
		}
		if math.IsInf(n, 0) || math.IsNaN(n) {
			return nil, &Error{key + " must be finite"}
		}
		if n < r[0] || n > r[1] {
			return nil, &Error{key + " must be between " + fmtG(r[0]) + " and " + fmtG(r[1])}
		}
		merged.Set(key, n)
	}
	return merged, nil
}

// fmtG is Python's format(x, "g") for the range bounds used here.
func fmtG(x float64) string { return strconv.FormatFloat(x, 'g', 6, 64) }

func personalPalette(value any) (*pyjson.Object, error) {
	v, ok := value.(*pyjson.Object)
	if !ok {
		return nil, &Error{"personal palette accepts only enabled, light and dark"}
	}
	for _, k := range v.Keys() {
		if k != "enabled" && k != "light" && k != "dark" {
			return nil, &Error{"personal palette accepts only enabled, light and dark"}
		}
	}
	if !v.Has("light") || !v.Has("dark") {
		return nil, &Error{"personal palette must contain light and dark palettes"}
	}
	enabled := any(true)
	if e, has := v.Get("enabled"); has {
		enabled = e
	}
	if _, isBool := enabled.(bool); !isBool {
		return nil, &Error{"personal palette enabled must be true or false"}
	}
	out := pyjson.NewObject("enabled", enabled)
	allowed := map[string]bool{"preset": true}
	for _, k := range PaletteKeys {
		allowed[k] = true
	}
	for _, mode := range []string{"light", "dark"} {
		def := map[string]string{"light": "ark-light", "dark": "ark"}[mode]
		patch, ok := v.Value(mode).(*pyjson.Object)
		if !ok {
			return nil, &Error{"personal palette accepts only palette colors and preset"}
		}
		for _, k := range patch.Keys() {
			if !allowed[k] {
				return nil, &Error{"personal palette accepts only palette colors and preset"}
			}
		}
		colors, err := updatePaletteColors(pyjson.NewObject("preset", def), patch)
		if err != nil {
			return nil, err
		}
		if PaletteMode(colors) != mode {
			return nil, &Error{mode + " palette background must match its mode"}
		}
		out.Set(mode, colors)
	}
	return out, nil
}

func personalGraph(value any) (*pyjson.Object, error) {
	v, ok := value.(*pyjson.Object)
	if !ok {
		return nil, &Error{"graph settings must be an object"}
	}
	var missing []string
	for _, k := range graphKeys {
		if !v.Has(k) {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, &Error{"personal graph settings need: " + strings.Join(missing, ", ")}
	}
	base := pyjson.NewObject()
	for _, k := range graphKeys {
		base.Set(k, graphDefaults[k])
	}
	return UpdateGraph(base, v)
}

// PersonalAppearance is personal_appearance(value) (read path, lenient).
func PersonalAppearance(value any) *pyjson.Object {
	out := pyjson.NewObject()
	v, ok := value.(*pyjson.Object)
	if !ok {
		return out
	}
	for _, group := range []string{"palette", "graph", "arrows", "graph_display"} {
		if v.Value(group) == nil {
			continue
		}
		var res *pyjson.Object
		var err error
		switch group {
		case "palette":
			res, err = personalPalette(v.Value(group))
		case "graph":
			res, err = personalGraph(v.Value(group))
		case "graph_display":
			res, _ = GraphDisplay(v.Value(group), false)
			if res.Len() == 0 {
				continue
			}
		default:
			res, err = personalArrows(v.Value(group))
		}
		if err == nil {
			out.Set(group, res)
		}
	}
	return out
}

// UpdatePersonalAppearance is update_personal_appearance(base, patch).
func UpdatePersonalAppearance(base, patch any) (*pyjson.Object, error) {
	p, ok := patch.(*pyjson.Object)
	if !ok {
		return nil, &Error{"appearance overrides must be an object"}
	}
	var unknown []string
	for _, k := range p.Keys() {
		if k != "palette" && k != "graph" && k != "arrows" && k != "graph_display" {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, &Error{"unknown appearance group: " + strings.Join(unknown, ", ")}
	}
	merged := PersonalAppearance(base)
	for _, group := range p.Keys() {
		value := p.Value(group)
		if value == nil {
			merged.Delete(group)
			continue
		}
		var res *pyjson.Object
		var err error
		switch group {
		case "palette":
			res, err = personalPalette(value)
		case "graph":
			res, err = personalGraph(value)
		case "graph_display":
			// Go-only (T-0036): sparse per-setting overrides; replaces the
			// whole group, an empty object clears it.
			res, err = GraphDisplay(value, true)
			if err == nil && res.Len() == 0 {
				merged.Delete(group)
				continue
			}
		default:
			res, err = personalArrows(value)
		}
		if err != nil {
			return nil, err
		}
		merged.Set(group, res)
	}
	return merged, nil
}
