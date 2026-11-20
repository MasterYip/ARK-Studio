package providers

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/platform/textfile"

	"arkstudio/go/internal/assistcfg"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pyos"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/compat/pyurl"
	"arkstudio/go/internal/platform/procx"
	"arkstudio/go/internal/project"
)

// Constants of providers.py.
const (
	DefaultTTL            = 60 * time.Second
	versionTimeout        = 4 * time.Second
	maxVersionLine        = 120
	modelListTimeout      = 8 * time.Second
	modelHTTPTimeout      = 5 * time.Second
	maxModelOutputBytes   = 8 * 1024 * 1024
	maxConfigBytes        = 512 * 1024
	maxModels             = 150
	maxModelError         = 160
	maxCredentialFileSize = 2 * 1024 * 1024
)

var statusOrder = map[string]int{"ready": 0, "unknown": 1, "unauthenticated": 2, "missing": 3}

var versionProbed = set("claude", "codex", "dsh", "opencode", "cursor", "gemini", "echo")

var authEnv = map[string][]string{
	"claude":   {"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"},
	"codex":    {"OPENAI_API_KEY", "CODEX_API_KEY"},
	"dsh":      {"TENCENTHUB_API_KEY", "DEEPSEEK_API_KEY", "DSH_API_KEY"},
	"opencode": {"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "OPENROUTER_API_KEY", "DEEPSEEK_API_KEY", "TENCENTHUB_API_KEY", "GROQ_API_KEY"},
	"cursor":   {"CURSOR_API_KEY"},
	"gemini":   {"GEMINI_API_KEY", "GOOGLE_API_KEY"},
}

var authDetail = map[string]string{
	"claude":   "Run `claude /login`, or set ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN in the env block of ~/.claude/settings.json.",
	"codex":    "Run `codex login` or set OPENAI_API_KEY.",
	"dsh":      "Configure model credentials in DSH or Settings → Assistant.",
	"opencode": "Run `opencode auth login` or export a provider API key.",
	"cursor":   "ARK cannot inspect the Cursor keychain; sign in with the Cursor CLI.",
	"gemini":   "Run `gemini` and sign in, or set GEMINI_API_KEY.",
}

var (
	authRedaction = regexp.MustCompile(`[A-Za-z0-9_\-]{24,}`)
	controlChars  = regexp.MustCompile("[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]")
	ansiRe        = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	modelIDRe     = regexp.MustCompile(`\A[A-Za-z0-9][A-Za-z0-9._:/@\[\]+-]{0,119}\z`)
	secretRun     = regexp.MustCompile(`[A-Za-z0-9_]{28,}`)
	effortIDRe    = regexp.MustCompile(`\A[a-z][a-z0-9_-]{0,23}\z`)
)

var secretPrefixes = []string{"sk-", "sk_", "pk-", "rk-", "ghp_", "gho_", "xox", "aiza", "bearer "}

var probeEnvKeys = set("PATH", "Path", "PATHEXT", "SYSTEMROOT", "SystemRoot", "COMSPEC", "ComSpec",
	"WINDIR", "HOME", "USERPROFILE", "TEMP", "TMP", "TMPDIR", "LANG", "LC_ALL",
	"APPDATA", "LOCALAPPDATA", "PROGRAMDATA", "PROGRAMFILES", "PROGRAMFILES(X86)",
	"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME")

var listEnvKeys = func() map[string]bool {
	m := set("CODEX_HOME", "OPENCODE_CONFIG", "USERNAME", "USER", "HOMEDRIVE", "HOMEPATH")
	for k := range probeEnvKeys {
		m[k] = true
	}
	return m
}()

// environ is os.environ as Python sees it (keys upper-cased on Windows).
func environ() [][2]string {
	var out [][2]string
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			continue // "=C:=C:\..." pseudo variables
		}
		if runtime.GOOS == "windows" {
			k = strings.ToUpper(k)
		}
		out = append(out, [2]string{k, v})
	}
	return out
}

func filteredEnv(keys map[string]bool) []string {
	out := []string{}
	for _, kv := range environ() {
		if keys[kv[0]] {
			out = append(out, kv[0]+"="+kv[1])
		}
	}
	return out
}

// SanitizeVersion is _sanitize_version.
func SanitizeVersion(text string) string {
	value := ansiRe.ReplaceAllString(text, "")
	value = controlChars.ReplaceAllString(value, "")
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " ")
	value = strings.Join(strings.FieldsFunc(value, pytext.IsSpace), " ")
	value = authRedaction.ReplaceAllString(value, "[redacted]")
	return pytext.Prefix(value, maxVersionLine)
}

// RunResult is the dict of the version / list runners; ReturnCode nil is None.
type RunResult struct {
	ReturnCode     *int
	Stdout, Stderr string
	Error          string
}

// HTTPResult is the dict of the HTTP getter; Status nil is None.
type HTTPResult struct {
	Status *int
	Body   string
	Error  string
}

func decodeReplace(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	return pyurl.DecodeReplace(b)
}

// commandSpec is _version_argv / _command_argv: a .cmd/.bat shim runs
// through cmd.exe with the exact list2cmdline Python builds.
func commandSpec(program string, args []string) procx.Spec {
	ext := strings.ToLower(filepath.Ext(program))
	if runtime.GOOS == "windows" && (ext == ".cmd" || ext == ".bat") {
		comspec := os.Getenv("COMSPEC")
		if comspec == "" {
			comspec = "cmd.exe"
		}
		inner := pyos.List2Cmdline(append([]string{program}, args...))
		argv := []string{comspec, "/d", "/s", "/c", inner}
		return procx.Spec{Name: comspec, Args: argv[1:], CmdLine: pyos.List2Cmdline(argv)}
	}
	return procx.Spec{Name: program, Args: args,
		CmdLine: windowsOnly(pyos.List2Cmdline(append([]string{program}, args...)))}
}

func windowsOnly(s string) string {
	if runtime.GOOS == "windows" {
		return s
	}
	return ""
}

func intp(n int) *int { return &n }

func errorText(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	return err.Error()
}

// DefaultRunner runs the read-only probes through a bounded process pool.
type DefaultRunner struct{ Pool *procx.Pool }

// Version is _default_run.
func (r DefaultRunner) Version(program string) RunResult {
	spec := commandSpec(program, []string{"--version"})
	spec.Timeout, spec.MaxOutput, spec.Env = versionTimeout, maxModelOutputBytes, filteredEnv(probeEnvKeys)
	res, err := r.Pool.RunSpec(context.Background(), spec)
	if err != nil && !errors.Is(err, procx.ErrOutputLimit) {
		return RunResult{Error: errorText(err)}
	}
	return RunResult{ReturnCode: intp(res.ExitCode), Stdout: decodeReplace(res.Stdout), Stderr: decodeReplace(res.Stderr)}
}

// List is _default_list_run.
func (r DefaultRunner) List(argv []string) RunResult {
	spec := commandSpec(argv[0], argv[1:])
	spec.Timeout, spec.MaxOutput, spec.Env = modelListTimeout, maxModelOutputBytes+1, filteredEnv(listEnvKeys)
	res, err := r.Pool.RunSpec(context.Background(), spec)
	if errors.Is(err, procx.ErrOutputLimit) {
		if len(res.Stdout) > maxModelOutputBytes {
			return RunResult{ReturnCode: intp(res.ExitCode), Error: "output too large"}
		}
		err = nil
	}
	if err != nil {
		return RunResult{Error: errorText(err)}
	}
	stderr := res.Stderr
	if len(stderr) > 4096 {
		stderr = stderr[:4096]
	}
	return RunResult{ReturnCode: intp(res.ExitCode), Stdout: decodeReplace(res.Stdout), Stderr: decodeReplace(stderr)}
}

// HTTPGet is _default_http_get.
func (r DefaultRunner) HTTPGet(raw string, headers [][2]string) HTTPResult {
	u, err := url.Parse(raw)
	if err != nil || (strings.ToLower(u.Scheme) != "http" && strings.ToLower(u.Scheme) != "https") {
		scheme := ""
		if err == nil {
			scheme = strings.ToLower(u.Scheme)
		} else if i := strings.Index(raw, ":"); i > 0 {
			scheme = strings.ToLower(raw[:i])
		}
		if scheme != "http" && scheme != "https" {
			return HTTPResult{Error: "unsupported URL scheme"}
		}
		return HTTPResult{Error: "unreachable"}
	}
	req, err := http.NewRequest("GET", raw, nil)
	if err != nil {
		return HTTPResult{Error: "unreachable"}
	}
	for _, h := range headers {
		req.Header.Set(h[0], h[1])
	}
	client := &http.Client{Timeout: modelHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		var ne interface{ Timeout() bool }
		if errors.As(err, &ne) && ne.Timeout() || strings.Contains(strings.ToLower(err.Error()), "timed out") {
			return HTTPResult{Error: "timed out"}
		}
		return HTTPResult{Error: "unreachable"}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return HTTPResult{Status: intp(resp.StatusCode), Error: "HTTP " + strconv.Itoa(resp.StatusCode)}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelOutputBytes+1))
	if err != nil {
		return HTTPResult{Error: "unreachable"}
	}
	if len(body) > maxModelOutputBytes {
		return HTTPResult{Status: intp(resp.StatusCode), Error: "response too large"}
	}
	return HTTPResult{Status: intp(resp.StatusCode), Body: decodeReplace(body)}
}

// ---------------------------------------------------------------- auth

type fileState string

func readJSONStructure(path string) (fileState, any) {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		if err != nil && !os.IsNotExist(err) {
			return "unreadable", nil
		}
		return "missing", nil
	}
	if fi.Size() > maxCredentialFileSize {
		return "unreadable", nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "unreadable", nil
	}
	text := textfile.Universal(decodeReplace(raw))
	if pytext.Strip(text) == "" {
		return "empty", nil
	}
	v, err := pyjson.Loads(text)
	if err != nil {
		return "unreadable", nil
	}
	return "present", v
}

func fileLabel(path, home string) string {
	p, h := project.Resolve(path), project.Resolve(home)
	if rel, err := filepath.Rel(h, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
		if rel == "." {
			return "file:~/."
		}
		return "file:~/" + filepath.ToSlash(rel)
	}
	return "file:" + filepath.Base(path)
}

func envSources(names []string) []any {
	out := []any{}
	for _, name := range names {
		if pytext.Strip(os.Getenv(name)) != "" {
			out = append(out, "env:"+name)
		}
	}
	return out
}

func appendSources(sources []any, more ...any) []any {
	out := append([]any{}, sources...)
	return append(out, more...)
}

func asObject(v any) (*pyjson.Object, bool) {
	o, ok := v.(*pyjson.Object)
	return o, ok
}

func strStrip(v any) string {
	if !pyjson.Truthy(v) {
		return ""
	}
	return pytext.Strip(pyjson.Str(v))
}

func authRecord(state string, authenticated any, method any, sources []any, detail string) *pyjson.Object {
	return pyjson.NewObject("state", state, "authenticated", authenticated, "method", method,
		"sources", sources, "detail", detail)
}

// authFor is _auth.
func (c *Catalog) authFor(id string) *pyjson.Object {
	names := authEnv[id]
	sources := envSources(names)
	baseDetail, ok := authDetail[id]
	if !ok {
		baseDetail = "ARK cannot verify this provider's credentials."
	}
	base := func(state string, authenticated, method any, srcs []any, detail string) *pyjson.Object {
		return authRecord(state, authenticated, method, srcs, detail)
	}
	home := c.Home
	switch id {
	case "echo", "shell":
		return authRecord("not_required", true, "none", []any{}, "Local process; no provider account is involved.")
	case "custom":
		return authRecord("unknown", nil, "operator", []any{}, "Authentication is managed by the operator-defined command.")
	case "dsh":
		if hasKey, source := c.assistantKey(); hasKey {
			return authRecord("authenticated", true, "ark_assistant", appendSources(sources, source),
				"ARK found a configured assistant model credential for DSH.")
		}
		if len(sources) > 0 {
			return base("authenticated", true, "environment", sources, "Provider API key found in the environment.")
		}
		return base("unknown", nil, nil, sources,
			"DSH may manage its own profile; ARK cannot verify it. Configure a key in Settings → Assistant to pass one explicitly.")
	case "claude":
		if len(sources) > 0 {
			return base("authenticated", true, "environment", sources, "Claude credential found in the environment.")
		}
		if ok, method, more, detail := claudeSettingsAuth(claudeLayers(home)); ok {
			return base("authenticated", true, method, appendSources(sources, more...), detail)
		}
		path := filepath.Join(claudeConfigDir(home), ".credentials.json")
		state, data := readJSONStructure(path)
		if state != "missing" {
			sources = appendSources(sources, fileLabel(path, home))
		}
		if d, ok := asObject(data); state == "present" && ok {
			oauth, ok := asObject(d.Value("claudeAiOauth"))
			if !ok {
				oauth = pyjson.NewObject()
			}
			access := strStrip(oauth.Value("accessToken"))
			refresh := strStrip(oauth.Value("refreshToken"))
			expires := oauth.Value("expiresAt")
			if refresh != "" || access != "" {
				expired := false
				if access != "" && refresh == "" && pyjson.Truthy(expires) {
					if f, ok := pytext.FloatValue(expires); ok {
						expired = f <= float64(time.Now().UnixNano())/1e9*1000
					}
				}
				if !expired {
					return base("authenticated", true, "credentials_file", sources,
						"Claude CLI login found in ~/.claude/.credentials.json.")
				}
			}
		}
		if state == "unreadable" {
			return base("unknown", nil, nil, sources,
				"Claude's credential file exists but could not be read; run `claude /login` to repair it.")
		}
		return base("unauthenticated", false, nil, sources, authDetail["claude"])
	case "codex":
		if len(sources) > 0 {
			return base("authenticated", true, "environment", sources, "Provider API key found in the environment.")
		}
		if ok, more, detail := codexProviderAuth(home); ok {
			return base("authenticated", true, "config_provider", appendSources(sources, more...), detail)
		}
		path := filepath.Join(codexDir(home), "auth.json")
		state, data := readJSONStructure(path)
		if state != "missing" {
			sources = appendSources(sources, fileLabel(path, home))
		}
		if d, ok := asObject(data); state == "present" && ok {
			tokens, isObj := asObject(d.Value("tokens"))
			if (isObj && tokens.Len() > 0) || strStrip(d.Value("OPENAI_API_KEY")) != "" {
				return base("authenticated", true, "credentials_file", sources, "Codex login found in ~/.codex/auth.json.")
			}
		}
		if state == "unreadable" {
			return base("unknown", nil, nil, sources,
				"Codex's auth file exists but could not be read; run `codex login` to repair it.")
		}
		return base("unauthenticated", false, nil, sources, authDetail["codex"])
	case "opencode":
		if len(sources) > 0 {
			return base("authenticated", true, "environment", sources, "Provider API key found in the environment.")
		}
		candidates := []string{
			filepath.Join(home, ".local", "share", "opencode", "auth.json"),
			filepath.Join(home, ".config", "opencode", "auth.json"),
		}
		if appdata := pytext.Strip(os.Getenv("APPDATA")); appdata != "" {
			candidates = append(candidates, filepath.Join(appdata, "opencode", "auth.json"))
		}
		for _, path := range candidates {
			state, data := readJSONStructure(path)
			if state == "present" {
				sources = appendSources(sources, fileLabel(path, home))
				if d, ok := asObject(data); ok && d.Len() > 0 {
					return base("authenticated", true, "credentials_file", sources, "OpenCode auth file found.")
				}
			} else if state == "unreadable" {
				return base("unknown", nil, nil, appendSources(sources, fileLabel(path, home)),
					"OpenCode's auth file exists but could not be read.")
			}
		}
		return base("unauthenticated", false, nil, sources, authDetail["opencode"])
	case "gemini":
		if len(sources) > 0 {
			return base("authenticated", true, "environment", sources, "Provider API key found in the environment.")
		}
		path := filepath.Join(home, ".gemini", "oauth_creds.json")
		state, data := readJSONStructure(path)
		if state != "missing" {
			sources = appendSources(sources, fileLabel(path, home))
		}
		if d, ok := asObject(data); state == "present" && ok && d.Len() > 0 {
			return base("authenticated", true, "credentials_file", sources, "Gemini login found in ~/.gemini/oauth_creds.json.")
		}
		if state == "unreadable" {
			return base("unknown", nil, nil, sources, "Gemini's credential file exists but could not be read.")
		}
		return base("unauthenticated", false, nil, sources, authDetail["gemini"])
	case "cursor":
		if len(sources) > 0 {
			return base("authenticated", true, "environment", sources, "CURSOR_API_KEY found in the environment.")
		}
		return base("unknown", nil, nil, []any{}, authDetail["cursor"])
	}
	return authRecord("unknown", nil, nil, sources, baseDetail)
}

// ---------------------------------------------------------------- models

var builtinEfforts = map[string][]string{"claude": {"low", "medium", "high", "xhigh", "max"}}

func looksSecret(value string) bool {
	lowered := pytext.Lower(pytext.Strip(value))
	for _, p := range secretPrefixes {
		if strings.HasPrefix(lowered, p) {
			return true
		}
	}
	return secretRun.MatchString(value)
}

// CleanModelID is _clean_model_id.
func CleanModelID(value any) string {
	s, ok := value.(string)
	if !ok {
		return ""
	}
	s = pytext.Strip(s)
	if s == "" || !modelIDRe.MatchString(s) || looksSecret(s) {
		return ""
	}
	return s
}

func cleanLabel(value any, fallback string) string {
	s, ok := value.(string)
	if !ok {
		return fallback
	}
	if text := pytext.Prefix(SanitizeVersion(s), 80); text != "" {
		return text
	}
	return fallback
}

func iterValues(values any) []any {
	switch v := values.(type) {
	case []any:
		return v
	case string:
		out := []any{}
		for _, r := range v {
			out = append(out, string(r))
		}
		return out
	case *pyjson.Object:
		out := []any{}
		for _, k := range v.Keys() {
			out = append(out, k)
		}
		return out
	}
	return nil
}

func cleanEfforts(values any) []string {
	out := []string{}
	if !pyjson.Truthy(values) {
		return out
	}
	for _, value := range iterValues(values) {
		if d, ok := value.(*pyjson.Object); ok {
			value = d.Value("effort")
			if !pyjson.Truthy(value) {
				value = d.Value("id")
			}
		}
		if s, ok := value.(string); ok {
			s = pytext.Lower(pytext.Strip(s))
			if effortIDRe.MatchString(s) && !contains(out, s) {
				out = append(out, s)
			}
		}
	}
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

func shortError(text string) string { return pytext.Prefix(SanitizeVersion(text), maxModelError) }

func entry(id, source, via, label string, efforts []string, defaultEffort string) *pyjson.Object {
	if label == "" {
		label = id
	}
	item := pyjson.NewObject("id", id, "label", label, "source", source, "via", via)
	if len(efforts) > 0 {
		item.Set("efforts", append([]string{}, efforts...))
		if contains(efforts, defaultEffort) {
			item.Set("default_effort", defaultEffort)
		}
	}
	return item
}

// discovery is one _result dict.
type discovery struct {
	method        string
	models        []*pyjson.Object
	def           string
	defaultEffort string
	err           string
	fullList      bool
	extraMethods  []string // further config files that were read
}

func result(method string, models []*pyjson.Object, def, effort, err string, full bool) discovery {
	if len(models) > maxModels {
		models = models[:maxModels]
	}
	return discovery{method: method, models: models, def: def, defaultEffort: effort, err: err, fullList: full}
}

func runFailure(label string, r RunResult) string {
	if r.Error != "" {
		return shortError(label + " " + r.Error)
	}
	if r.ReturnCode != nil && *r.ReturnCode != 0 {
		return label + " exited with " + strconv.Itoa(*r.ReturnCode)
	}
	return ""
}

func codexCLIModels(program string, list func([]string) RunResult, bundled bool) discovery {
	args := []string{"debug", "models"}
	if bundled {
		args = append(args, "--bundled")
	}
	label := "codex " + strings.Join(args, " ")
	method := "cli:" + label
	r := list(append([]string{program}, args...))
	if f := runFailure(label, r); f != "" {
		return result(method, nil, "", "", f, false)
	}
	data, err := pyjson.Loads(r.Stdout)
	if err != nil {
		return result(method, nil, "", "", label+" returned malformed JSON", false)
	}
	rows := data
	if d, ok := data.(*pyjson.Object); ok {
		rows = d.Value("models")
	}
	list2, ok := rows.([]any)
	if !ok {
		return result(method, nil, "", "", label+" returned no model list", false)
	}
	type ranked struct {
		priority float64
		index    int
		item     *pyjson.Object
	}
	var items []ranked
	for index, raw := range list2 {
		row, ok := raw.(*pyjson.Object)
		if !ok {
			continue
		}
		if v, ok := row.Value("visibility").(string); ok && v == "hide" {
			continue
		}
		slug := row.Value("slug")
		if !pyjson.Truthy(slug) {
			slug = row.Value("id")
		}
		id := CleanModelID(slug)
		if id == "" {
			continue
		}
		priority, ok := pytext.FloatValue(row.Value("priority"))
		if !ok {
			priority = float64(index + 1000)
		}
		efforts := cleanEfforts(row.Value("supported_reasoning_levels"))
		items = append(items, ranked{priority, index, entry(id, "detected", "cli",
			cleanLabel(row.Value("display_name"), id), efforts, pytext.Lower(strStrip(row.Value("default_reasoning_level"))))})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].priority != items[j].priority {
			return items[i].priority < items[j].priority
		}
		return items[i].index < items[j].index
	})
	var models []*pyjson.Object
	for _, it := range items {
		models = append(models, it.item)
	}
	if len(models) == 0 {
		return result(method, nil, "", "", label+" listed no models", false)
	}
	return result(method, models, "", "", "", true)
}

func opencodeCLIModels(program string, list func([]string) RunResult) discovery {
	method := "cli:opencode models"
	r := list([]string{program, "models"})
	if f := runFailure("opencode models", r); f != "" {
		return result(method, nil, "", "", f, false)
	}
	var models []*pyjson.Object
	seen := map[string]bool{}
	for _, line := range pytext.SplitLines(ansiRe.ReplaceAllString(r.Stdout, ""), false) {
		token := pytext.Strip(line)
		if !strings.Contains(token, "/") || strings.Contains(token, " ") {
			continue
		}
		if id := CleanModelID(token); id != "" && !seen[id] {
			seen[id] = true
			models = append(models, entry(id, "detected", "cli", "", nil, ""))
		}
	}
	if len(models) == 0 {
		return result(method, nil, "", "", "opencode models returned no model ids", false)
	}
	return result(method, models, "", "", "", true)
}

func readTextBounded(path string) (fileState, string) {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		if err != nil && !os.IsNotExist(err) {
			return "unreadable", ""
		}
		return "missing", ""
	}
	if fi.Size() > maxConfigBytes {
		return "unreadable", ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "unreadable", ""
	}
	text := decodeReplace(raw)
	text = strings.TrimPrefix(text, "\ufeff")
	return "present", textfile.Universal(text)
}

func readJSONConfig(path string) (fileState, any) {
	state, text := readTextBounded(path)
	if state != "present" {
		return state, nil
	}
	if pytext.Strip(text) == "" {
		return "empty", nil
	}
	if v, err := pyjson.Loads(text); err == nil {
		return "present", v
	}
	var kept []string
	for _, line := range pytext.SplitLines(text, false) {
		if !strings.HasPrefix(pytext.LStrip(line), "//") {
			kept = append(kept, line)
		}
	}
	if v, err := pyjson.Loads(strings.Join(kept, "\n")); err == nil {
		return "present", v
	}
	return "malformed", nil
}

func hasModel(models []*pyjson.Object, id string) bool {
	for _, m := range models {
		if pyjson.Str(m.Value("id")) == id {
			return true
		}
	}
	return false
}

func codexConfigModels(home string) discovery {
	path := filepath.Join(codexDir(home), "config.toml")
	_, rest, _ := strings.Cut(fileLabel(path, home), ":")
	method := "config:" + rest
	state, text := readTextBounded(path)
	if state == "missing" {
		return result(method, nil, "", "", "", false)
	}
	if state != "present" {
		return result(method, nil, "", "", filepath.Base(path)+" could not be read", false)
	}
	top, profiles := tomlModelKeys(text)
	def := CleanModelID(top.Value("model"))
	effort := cleanEfforts([]any{top.Value("model_reasoning_effort")})
	var models []*pyjson.Object
	if def != "" {
		models = append(models, entry(def, "detected", "config", "", nil, ""))
	}
	for _, name := range profiles.Keys() {
		keys := profiles.Value(name).(*pyjson.Object)
		if id := CleanModelID(keys.Value("model")); id != "" && !hasModel(models, id) {
			models = append(models, entry(id, "detected", "config",
				id+" (profile "+pytext.Prefix(cleanLabel(name, "profile"), 24)+")", nil, ""))
		}
	}
	e := ""
	if len(effort) > 0 {
		e = effort[0]
	}
	return result(method, models, def, e, "", false)
}

func opencodeConfigModels(home string) discovery {
	base := pytext.Strip(os.Getenv("XDG_CONFIG_HOME"))
	folder := filepath.Join(home, ".config", "opencode")
	if base != "" {
		folder = filepath.Join(base, "opencode")
	}
	for _, name := range []string{"opencode.json", "opencode.jsonc", "config.json"} {
		path := filepath.Join(folder, name)
		state, raw := readJSONConfig(path)
		if state == "missing" {
			continue
		}
		_, rest, _ := strings.Cut(fileLabel(path, home), ":")
		method := "config:" + rest
		data, ok := asObject(raw)
		if !ok {
			return result(method, nil, "", "", name+" is "+string(state), false)
		}
		def := CleanModelID(data.Value("model"))
		var models []*pyjson.Object
		for _, value := range []string{def, CleanModelID(data.Value("small_model"))} {
			if value != "" && !hasModel(models, value) {
				models = append(models, entry(value, "detected", "config", "", nil, ""))
			}
		}
		providerMap, ok := asObject(data.Value("provider"))
		if !ok {
			providerMap = pyjson.NewObject()
		}
		for _, providerName := range providerMap.Keys() {
			spec, ok := asObject(providerMap.Value(providerName))
			if !ok {
				continue
			}
			modelsMap, ok := asObject(spec.Value("models"))
			if !ok {
				continue
			}
			for _, modelName := range modelsMap.Keys() {
				id := CleanModelID(providerName + "/" + modelName)
				if id != "" && !hasModel(models, id) {
					var label any = ""
					if ms, ok := asObject(modelsMap.Value(modelName)); ok {
						label = ms.Value("name")
					}
					if !pyjson.Truthy(label) {
						label = id
					}
					models = append(models, entry(id, "detected", "config", cleanLabel(label, id), nil, ""))
				}
			}
		}
		return result(method, models, def, "", "", false)
	}
	return result("config:~/.config/opencode/opencode.json", nil, "", "", "", false)
}

func geminiConfigModels(home string) discovery {
	path := filepath.Join(home, ".gemini", "settings.json")
	method := "config:~/.gemini/settings.json"
	state, raw := readJSONConfig(path)
	if state == "missing" {
		return result(method, nil, "", "", "", false)
	}
	data, ok := asObject(raw)
	if !ok {
		return result(method, nil, "", "", "settings.json is "+string(state), false)
	}
	model := data.Value("model")
	if m, ok := asObject(model); ok {
		model = m.Value("name")
	}
	def := CleanModelID(model)
	var models []*pyjson.Object
	if def != "" {
		models = append(models, entry(def, "detected", "config", "", nil, ""))
	}
	return result(method, models, def, "", "", false)
}

var configReaders = map[string]func(string) discovery{
	"codex": codexConfigModels, "claude": claudeConfigModels,
	"opencode": opencodeConfigModels, "gemini": geminiConfigModels,
}

func openAICompatibleModels(baseURL, apiKey string, get func(string, [][2]string) HTTPResult) discovery {
	method := "api:GET /models"
	baseURL = strings.TrimRight(pytext.Strip(baseURL), "/")
	if baseURL == "" {
		return result(method, nil, "", "", "no endpoint configured", false)
	}
	headers := [][2]string{{"Accept", "application/json"}}
	if apiKey != "" {
		headers = append(headers, [2]string{"Authorization", "Bearer " + apiKey})
	}
	resp := get(baseURL+"/models", headers)
	if resp.Error != "" {
		return result(method, nil, "", "", shortError("models endpoint "+resp.Error), false)
	}
	data, err := pyjson.Loads(resp.Body)
	if err != nil {
		return result(method, nil, "", "", "models endpoint returned malformed JSON", false)
	}
	rows := data
	if d, ok := data.(*pyjson.Object); ok {
		rows = d.Value("data")
	}
	list, ok := rows.([]any)
	if !ok {
		return result(method, nil, "", "", "models endpoint returned no model list", false)
	}
	var models []*pyjson.Object
	for _, row := range list {
		v := row
		if d, ok := row.(*pyjson.Object); ok {
			v = d.Value("id")
		}
		if id := CleanModelID(v); id != "" && !hasModel(models, id) {
			models = append(models, entry(id, "detected", "api", "", nil, ""))
		}
	}
	if len(models) == 0 {
		return result(method, nil, "", "", "models endpoint listed no models", false)
	}
	return result(method, models, "", "", "", true)
}

func presetModels(preset *pyjson.Object) []any {
	if l, ok := preset.Value("models").([]any); ok {
		return l
	}
	if l, ok := preset.Value("models").([]string); ok {
		out := make([]any, len(l))
		for i, s := range l {
			out[i] = s
		}
		return out
	}
	return nil
}

// mergeModels is merge_models.
func mergeModels(preset *pyjson.Object, results []discovery, checkedAt any, assistantModel string, installed bool) *pyjson.Object {
	id := pyjson.Str(preset.Value("id"))
	var options []*pyjson.Object
	seen := map[string]*pyjson.Object{}
	add := func(item *pyjson.Object) bool {
		key := pyjson.Str(item.Value("id"))
		existing, ok := seen[key]
		if !ok {
			cp := item.Copy()
			seen[key] = cp
			options = append(options, cp)
			return true
		}
		if pyjson.Truthy(item.Value("efforts")) && !pyjson.Truthy(existing.Value("efforts")) {
			existing.Set("efforts", item.Value("efforts"))
			if pyjson.Truthy(item.Value("default_effort")) {
				existing.Set("default_effort", item.Value("default_effort"))
			}
		}
		return false
	}
	pm := presetModels(preset)
	for _, raw := range pm {
		s := ""
		if pyjson.Truthy(raw) {
			s = pyjson.Str(raw)
		}
		if s = pytext.Prefix(pytext.Strip(s), 120); s != "" {
			add(entry(s, "configured", "preset", "", nil, ""))
		}
	}
	methods := []string{}
	var errs []string
	def, defEffort := "", ""
	full := false
	for _, r := range results {
		if r.err != "" {
			errs = append(errs, r.err)
		}
		if len(r.models) > 0 || r.def != "" {
			methods = append(methods, r.method)
		}
		methods = append(methods, r.extraMethods...)
		for _, item := range r.models {
			add(item)
		}
		if def == "" {
			def = r.def
		}
		if defEffort == "" {
			defEffort = r.defaultEffort
		}
		full = full || (r.fullList && len(r.models) > 0)
	}
	if id == "dsh" && installed && assistantModel != "" {
		if mid := CleanModelID(assistantModel); mid != "" {
			add(entry(mid, "detected", "config", mid+" (ARK assistant)", nil, ""))
			if def == "" {
				def = mid
			}
			methods = append(methods, "config:ARK assistant")
		}
	}
	if pe := builtinEfforts[id]; len(pe) > 0 {
		for _, item := range options {
			if !item.Has("efforts") {
				item.Set("efforts", append([]string{}, pe...))
			}
		}
	}
	if d, ok := seen[def]; def != "" && ok {
		d.Set("default", true)
		if defEffort != "" && contains(stringsOf(d.Value("efforts")), defEffort) {
			d.Set("default_effort", defEffort)
		}
	}
	source := "provider_default"
	detected, available := false, false
	for _, item := range options {
		switch item.Value("source") {
		case "detected":
			detected = true
		case "available":
			available = true
		}
	}
	switch {
	case len(pm) > 0:
		source = "configured"
	case detected:
		source = "detected"
	case available:
		source = "available"
	}
	detail := ""
	switch {
	case full:
		detail = "Detected from " + strings.Join(methods, ", ") + "."
	case len(methods) > 0:
		detail = "Read from " + strings.Join(methods, ", ") + "."
	case len(pm) > 0:
		detail = "Configured in the preset."
	default:
		detail = "No model list is available; the provider default is used."
	}
	var uniq []string
	for _, e := range errs {
		if !contains(uniq, e) {
			uniq = append(uniq, e)
		}
	}
	if len(options) > maxModels {
		options = options[:maxModels]
	}
	opts := make([]any, len(options))
	for i, o := range options {
		opts[i] = o
	}
	return pyjson.NewObject(
		"options", opts,
		"source", source,
		"methods", methods,
		"default", def,
		"default_effort", defEffort,
		"error", pytext.Prefix(strings.Join(uniq, "; "), maxModelError*2),
		"checked_at", checkedAt,
		"detail", detail,
		"builtin", false,
		"full_list", full,
		"reason", "",
	)
}

func stringsOf(v any) []string {
	switch l := v.(type) {
	case []string:
		return l
	case []any:
		out := make([]string, 0, len(l))
		for _, x := range l {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func kindFor(preset *pyjson.Object) string {
	switch {
	case pyjson.Truthy(preset.Value("demo")):
		return "demo"
	case preset.Value("id") == "shell":
		return "local"
	case preset.Value("id") == "custom":
		return "custom"
	}
	return "cli"
}

// capabilities is _capabilities.
func capabilities(preset, discovery *pyjson.Object) *pyjson.Object {
	var options []*pyjson.Object
	if l, ok := discovery.Value("options").([]any); ok {
		for _, x := range l {
			options = append(options, x.(*pyjson.Object))
		}
	}
	models := []string{}
	for _, item := range options {
		models = append(models, pyjson.Str(item.Value("id")))
	}
	configured := stringsOf(preset.Value("efforts"))
	var defaultOption *pyjson.Object
	for _, item := range options {
		if pyjson.Truthy(item.Value("default")) {
			defaultOption = item
			break
		}
	}
	var efforts []string
	effortsSource := "unsupported"
	switch {
	case len(configured) > 0:
		efforts, effortsSource = append([]string{}, configured...), "configured"
	case defaultOption != nil && pyjson.Truthy(defaultOption.Value("efforts")):
		efforts = append([]string{}, stringsOf(defaultOption.Value("efforts"))...)
		effortsSource = "detected"
		if defaultOption.Value("source") == "builtin" {
			effortsSource = "builtin"
		}
	case len(builtinEfforts[pyjson.Str(preset.Value("id"))]) > 0:
		efforts, effortsSource = append([]string{}, builtinEfforts[pyjson.Str(preset.Value("id"))]...), "builtin"
	default:
		efforts = []string{}
	}
	permissionModes := stringsOf(preset.Value("permission_modes"))
	if len(permissionModes) == 0 {
		permissionModes = stringsOf(preset.Value("access_modes"))
	}
	supportsApproval := pyjson.Truthy(preset.Value("supports_approval"))
	if len(permissionModes) == 0 {
		if supportsApproval {
			permissionModes = []string{"read_only", "plan", "ask", "accept_edits", "full_access"}
		} else {
			permissionModes = []string{"read_only", "plan", "full_access"}
		}
	}
	permissionModes = append([]string{}, permissionModes...)
	approvalModes := []string{}
	for _, m := range permissionModes {
		if m == "ask" || m == "accept_edits" {
			approvalModes = append(approvalModes, m)
		}
	}
	or := func(v any, def any) any {
		if pyjson.Truthy(v) {
			return v
		}
		return def
	}
	enforcement, mode, protocol := "ark_access_token", "unsupported", ""
	detail := "This preset has no interactive stdin, so approval modes are unavailable."
	if supportsApproval {
		enforcement, mode, protocol = "ark_access_token+stdin_approvals", "adapter_mediated", "ark-permission-v1"
		detail = "The adapter requests approval on stdout and ARK answers on stdin (ark-permission-v1)."
	}
	methods := append([]string{}, stringsOf(discovery.Value("methods"))...)
	opts := make([]any, len(options))
	for i, o := range options {
		opts[i] = o
	}
	return pyjson.NewObject(
		"models", models,
		"model_options", opts,
		"models_source", or(discovery.Value("source"), "provider_default"),
		"models_methods", methods,
		"models_checked_at", discovery.Value("checked_at"),
		"models_error", or(discovery.Value("error"), ""),
		"models_detail", or(discovery.Value("detail"), ""),
		"models_reason", or(discovery.Value("reason"), ""),
		"default_model", or(discovery.Value("default"), ""),
		"default_effort", or(discovery.Value("default_effort"), ""),
		"custom_models", true,
		"efforts", efforts,
		"efforts_source", effortsSource,
		"permission_modes", permissionModes,
		"permission_enforcement", enforcement,
		"native_approval", supportsApproval,
		"approval", pyjson.NewObject("supported", supportsApproval, "mode", mode, "protocol", protocol,
			"modes", approvalModes, "detail", detail),
		"streaming", "line_buffered",
		"followup", pyjson.Truthy(preset.Value("followup")),
		"prompt_mode", or(preset.Value("prompt_mode"), "stdin"),
		"stdin", pyjson.Truthy(preset.Value("followup")),
		"demo", pyjson.Truthy(preset.Value("demo")),
	)
}

// providerPath is _provider_path.
func providerPath() string {
	current := os.Getenv("PATH")
	if runtime.GOOS == "windows" {
		return current
	}
	home := pyos.Home()
	var parts []string
	for _, p := range []string{filepath.Join(home, ".local", "bin"), filepath.Join(home, "bin"), "/snap/bin", "/usr/local/bin"} {
		if pyos.IsDir(p) {
			parts = append(parts, p)
		}
	}
	for _, p := range strings.Split(current, ":") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	var ordered []string
	seen := map[string]bool{}
	for _, p := range parts {
		if !seen[p] {
			seen[p] = true
			ordered = append(ordered, p)
		}
	}
	return strings.Join(ordered, ":")
}

func resolveProgram(program string) string {
	program = pytext.Strip(program)
	if program == "" {
		return ""
	}
	if pyos.HasDirPart(program) {
		if pyos.IsFile(program) {
			return program
		}
		return ""
	}
	return pyos.Which(program, providerPath(), true)
}

// Catalog is ProviderCatalog with the production probes.
type Catalog struct {
	Presets []*pyjson.Object
	Home    string
	TTL     time.Duration
	Runner  interface {
		Version(program string) RunResult
		List(argv []string) RunResult
		HTTPGet(url string, headers [][2]string) HTTPResult
	}

	mu          sync.Mutex
	remote      map[string]discovery
	explicit    bool
	cached      *pyjson.Object
	cachedAt    float64
	lastRefresh any
}

// NewCatalog is ProviderCatalog(presets).
func NewCatalog(presets []*pyjson.Object, pool *procx.Pool) *Catalog {
	return &Catalog{Presets: presets, Home: pyos.Home(), TTL: DefaultTTL, Runner: DefaultRunner{Pool: pool},
		remote: map[string]discovery{}}
}

func (c *Catalog) assistantKey() (bool, string) {
	cfg, err := assistcfg.Load()
	if err != nil {
		return false, "ark_assistant"
	}
	return pytext.Strip(pyjson.Str(cfg.Value("api_key"))) != "", "ark_assistant"
}

func (c *Catalog) assistantModel() string {
	cfg, err := assistcfg.Load()
	if err != nil {
		return ""
	}
	return pyjson.Str(cfg.Value("model"))
}

func (c *Catalog) discoverModels(preset *pyjson.Object, resolved string) *pyjson.Object {
	id := pyjson.Str(preset.Value("id"))
	checkedAt := clock.WallUTCNow()
	var results []discovery
	installed := resolved != ""
	if reader, ok := configReaders[id]; installed && ok {
		results = append(results, reader(c.Home))
	}
	switch {
	case installed && id == "codex":
		remote, hasRemote := c.remote[id]
		if c.explicit {
			remote, hasRemote = codexCLIModels(resolved, c.Runner.List, false), true
			c.remote[id] = remote
		}
		if hasRemote && len(remote.models) > 0 {
			results = append(results, remote)
		} else {
			bundled := codexCLIModels(resolved, c.Runner.List, true)
			if hasRemote && remote.err != "" && len(bundled.models) == 0 {
				results = append(results, remote)
			}
			results = append(results, bundled)
		}
	case installed && id == "claude":
		remote, ok := c.remote[id]
		if c.explicit || !ok {
			remote = claudeAPIModels(c.Home, c.Runner.HTTPGet)
			c.remote[id] = remote
		}
		results = append(results, remote)
	case installed && id == "opencode":
		results = append(results, opencodeCLIModels(resolved, c.Runner.List))
	case installed && id == "dsh":
		if c.explicit {
			cfg, err := assistcfg.Load()
			base, key := "", ""
			if err == nil {
				base, key = pyjson.Str(cfg.Value("base_url")), pyjson.Str(cfg.Value("api_key"))
			}
			c.remote[id] = openAICompatibleModels(base, key, c.Runner.HTTPGet)
		}
		if remote, ok := c.remote[id]; ok {
			results = append(results, remote)
		}
	}
	return mergeModels(preset, results, checkedAt, c.assistantModel(), installed)
}

func (c *Catalog) probeOne(preset *pyjson.Object) *pyjson.Object {
	id := pyjson.Str(preset.Value("id"))
	command := Command(preset)
	program := ""
	if len(command) > 0 {
		program = command[0]
	}
	resolved := resolveProgram(program)
	installed := resolved != ""
	var version any
	versionError := ""
	if installed && versionProbed[id] {
		r := c.Runner.Version(resolved)
		output := SanitizeVersion(pytext.Strip(r.Stdout + " " + r.Stderr))
		switch {
		case r.Error != "":
			versionError = "version probe " + r.Error
		case r.ReturnCode != nil && *r.ReturnCode != 0:
			versionError = "version probe exited with " + strconv.Itoa(*r.ReturnCode)
		case output != "":
			version = output
		default:
			versionError = "version probe returned no version"
		}
	}
	auth := c.authFor(id)
	programLabel := program
	if programLabel == "" {
		programLabel = "command"
	}
	var status string
	if !installed {
		status = "missing"
		auth = auth.Copy().Set("state", "not_installed").Set("authenticated", nil).
			Set("detail", programLabel+" was not found on PATH.")
	} else if auth.Value("authenticated") == false {
		status = "unauthenticated"
	} else if auth.Value("authenticated") == true {
		status = "ready"
	} else {
		status = "unknown"
	}
	label := id
	if v := preset.Value("label"); pyjson.Truthy(v) {
		label = pyjson.Str(v)
	}
	authDetailText := func(def string) string {
		if v := auth.Value("detail"); pyjson.Truthy(v) {
			return pyjson.Str(v)
		}
		return def
	}
	var detail string
	switch status {
	case "ready":
		detail = label + " is installed and credentials were detected."
	case "missing":
		detail = programLabel + " was not found on PATH."
	case "unauthenticated":
		detail = authDetailText("Credentials were not detected.")
	default:
		detail = authDetailText("The provider is installed but ARK cannot verify its sign-in state.")
	}
	if (status == "ready" || status == "unknown") && versionError != "" {
		detail = detail + " " + pyos.Capitalize(versionError) + "."
	}
	description := ""
	if v := preset.Value("description"); pyjson.Truthy(v) {
		description = pyjson.Str(v)
	}
	return pyjson.NewObject(
		"id", id,
		"label", label,
		"description", description,
		"kind", kindFor(preset),
		"preset_id", id,
		"command", program,
		"installed", installed,
		"launchable", installed,
		"version", version,
		"version_error", versionError,
		"status", status,
		"authenticated", auth.Value("authenticated"),
		"auth", auth,
		"health", pyjson.NewObject("state", status, "detail", detail, "checked_at", clock.WallUTCNow()),
		"capabilities", capabilities(preset, c.discoverModels(preset, resolved)),
	)
}

func platformName() string {
	if runtime.GOOS == "windows" {
		return "nt"
	}
	return "posix"
}

func (c *Catalog) snapshotUncached() *pyjson.Object {
	started := time.Now()
	providers := make([]*pyjson.Object, 0, len(c.Presets))
	for _, p := range c.Presets {
		providers = append(providers, c.probeOne(p))
	}
	sort.SliceStable(providers, func(i, j int) bool {
		oi, ok := statusOrder[pyjson.Str(providers[i].Value("status"))]
		if !ok {
			oi = 9
		}
		oj, ok := statusOrder[pyjson.Str(providers[j].Value("status"))]
		if !ok {
			oj = 9
		}
		if oi != oj {
			return oi < oj
		}
		return pytext.Lower(pyjson.Str(providers[i].Value("label"))) < pytext.Lower(pyjson.Str(providers[j].Value("label")))
	})
	counts := pyjson.NewObject()
	list := make([]any, len(providers))
	for i, item := range providers {
		list[i] = item
		st := pyjson.Str(item.Value("status"))
		n, _ := counts.Value(st).(int64)
		counts.Set(st, n+1)
	}
	return pyjson.NewObject(
		"providers", list,
		"counts", counts,
		"total", int64(len(providers)),
		"generated_at", clock.WallUTCNow(),
		"last_refresh", c.lastRefresh,
		"probe_ms", int64(time.Since(started)/time.Millisecond),
		"ttl_seconds", int64(c.TTL/time.Second),
		"cached", false,
		"environment", pyjson.NewObject("platform", platformName(), "home", "~"),
	)
}

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// Snapshot is ProviderCatalog.snapshot(force=...).
func (c *Catalog) Snapshot(force bool) *pyjson.Object {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshotLocked(force)
}

func (c *Catalog) snapshotLocked(force bool) *pyjson.Object {
	t := now()
	ttl := c.TTL.Seconds()
	if c.cached != nil && !force && t-c.cachedAt < ttl {
		payload := c.cached.Copy()
		payload.Set("cached", true)
		payload.Set("cache_age_seconds", int64(math.Max(0, t-c.cachedAt)))
		return payload
	}
	c.explicit = force
	payload := c.snapshotUncached()
	c.explicit = false
	c.cached, c.cachedAt = payload, t
	if force {
		c.lastRefresh = payload.Value("generated_at")
		payload.Set("last_refresh", c.lastRefresh)
	}
	return payload
}

// Refresh is ProviderCatalog.refresh().
func (c *Catalog) Refresh() *pyjson.Object {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cached, c.cachedAt = nil, 0
	payload := c.snapshotLocked(true)
	payload.Set("cache_age_seconds", int64(0))
	return payload
}

// Summary is ProviderCatalog.summary(): the per-provider status map the
// session preset API merges in.
func (c *Catalog) Summary() *pyjson.Object {
	payload := c.Snapshot(false)
	providers := pyjson.NewObject()
	list, _ := payload.Value("providers").([]any)
	for _, x := range list {
		item := x.(*pyjson.Object)
		auth, _ := item.Value("auth").(*pyjson.Object)
		if auth == nil {
			auth = pyjson.NewObject()
		}
		health, ok := item.Value("health").(*pyjson.Object)
		if !ok {
			health = pyjson.NewObject()
		}
		caps, ok := item.Value("capabilities").(*pyjson.Object)
		if !ok {
			caps = pyjson.NewObject()
		}
		providers.Set(pyjson.Str(item.Value("id")), pyjson.NewObject(
			"status", item.Value("status"),
			"installed", item.Value("installed"),
			"version", item.Value("version"),
			"authenticated", item.Value("authenticated"),
			"auth_state", auth.Value("state"),
			"auth_detail", auth.Value("detail"),
			"health", health,
			"capabilities", caps,
		))
	}
	cached, has := payload.Get("cached")
	if !has {
		cached = false
	}
	return pyjson.NewObject("generated_at", payload.Value("generated_at"), "cached", cached, "providers", providers)
}

// MetadataFor is ProviderCatalog.metadata_for.
func (c *Catalog) MetadataFor(id string) *pyjson.Object {
	if p, ok := c.Summary().Value("providers").(*pyjson.Object).Value(id).(*pyjson.Object); ok {
		return p.Copy()
	}
	return pyjson.NewObject()
}

// PresetsWithProviderStatus is presets_with_provider_status.
func PresetsWithProviderStatus(presets []*pyjson.Object, c *Catalog) []*pyjson.Object {
	if c == nil {
		return presets
	}
	status := c.Summary().Value("providers").(*pyjson.Object)
	out := make([]*pyjson.Object, 0, len(presets))
	for _, preset := range presets {
		item := preset.Copy()
		if provider, ok := status.Value(pyjson.Str(item.Value("id"))).(*pyjson.Object); ok {
			caps := provider.Value("capabilities")
			if !pyjson.Truthy(caps) {
				caps = pyjson.NewObject()
			}
			item.Set("provider", provider)
			item.Set("provider_status", provider.Value("status"))
			item.Set("provider_version", provider.Value("version"))
			item.Set("provider_auth", pyjson.NewObject("state", provider.Value("auth_state"),
				"authenticated", provider.Value("authenticated"), "detail", provider.Value("auth_detail")))
			item.Set("provider_capabilities", caps)
		}
		out = append(out, item)
	}
	return out
}
