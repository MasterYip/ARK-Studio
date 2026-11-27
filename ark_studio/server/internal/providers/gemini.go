package providers

// Gemini CLI detection (2026-11-27): its folder, sign-in methods other than
// an API key or the Google login cache, and its model list.
//
//   folder   $GEMINI_CLI_HOME/.gemini, else ~/.gemini
//   sign-in  GEMINI_API_KEY / GOOGLE_API_KEY (authEnv), the Google login in
//            oauth_creds.json, Vertex AI (GOOGLE_GENAI_USE_VERTEXAI=true with
//            GOOGLE_CLOUD_PROJECT or GOOGLE_API_KEY), a key in the folder's
//            .env file, or settings.json security.auth.selectedType
//   models   `gemini --list-models --output-format json` (no sign-in
//            needed: without one Gemini lists its built-in catalog); when
//            an older version has no list, Gemini's stable aliases
//
// Key values are never read into ARK beyond "is it set".

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
)

// geminiDir is Gemini CLI's settings folder.
func geminiDir(home string) string {
	if h := pytext.Strip(os.Getenv("GEMINI_CLI_HOME")); h != "" {
		return filepath.Join(h, ".gemini")
	}
	return filepath.Join(home, ".gemini")
}

// envFileHasKey reports a non-empty assignment of one of `names` in a .env
// file (the value itself is not kept).
func envFileHasKey(path string, names ...string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for n := 0; sc.Scan() && n < 400; n++ {
		line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(sc.Text()), "export "))
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		for _, name := range names {
			if strings.TrimSpace(k) == name && v != "" {
				return true
			}
		}
	}
	return false
}

// geminiOtherAuth reports a Gemini sign-in besides the API key variables and
// the login cache.
func geminiOtherAuth(home string) (bool, string, []any, string) {
	if strings.EqualFold(pytext.Strip(os.Getenv("GOOGLE_GENAI_USE_VERTEXAI")), "true") &&
		(pytext.Strip(os.Getenv("GOOGLE_CLOUD_PROJECT")) != "" || pytext.Strip(os.Getenv("GOOGLE_API_KEY")) != "") {
		return true, "vertex_ai", []any{"env:GOOGLE_GENAI_USE_VERTEXAI"}, "Gemini CLI uses Vertex AI (GOOGLE_GENAI_USE_VERTEXAI)."
	}
	dir := geminiDir(home)
	if env := filepath.Join(dir, ".env"); envFileHasKey(env, "GEMINI_API_KEY", "GOOGLE_API_KEY") {
		return true, "env_file", []any{fileLabel(env, home)}, "Gemini API key found in the .env file of Gemini CLI's folder."
	}
	path := filepath.Join(dir, "settings.json")
	if state, data := readJSONStructure(path); state == "present" {
		d, _ := asObject(data)
		security, _ := asObject(d.Value("security"))
		auth, _ := asObject(security.Value("auth"))
		switch strStrip(auth.Value("selectedType")) {
		case "vertex-ai", "compute-default-credentials", "cloud-shell", "gateway":
			return true, "settings", []any{fileLabel(path, home)},
				"Gemini CLI signs in with " + strStrip(auth.Value("selectedType")) + " (settings.json)."
		}
	}
	return false, "", nil, ""
}

// geminiCLIModels reads `gemini --list-models --output-format json`.
func geminiCLIModels(program string, list func([]string) RunResult) discovery {
	label := "gemini --list-models"
	method := "cli:" + label
	r := list([]string{program, "--list-models", "--output-format", "json"})
	if f := runFailure(label, r); f != "" {
		return result(method, nil, "", "", f, false)
	}
	text := r.Stdout
	if i := strings.Index(text, "["); i > 0 {
		text = text[i:] // a warning line ahead of the list
	}
	data, err := pyjson.Loads(text)
	if err != nil {
		return result(method, nil, "", "", label+" returned malformed JSON", false)
	}
	rows, ok := data.([]any)
	if !ok {
		if d, isObj := data.(*pyjson.Object); isObj {
			rows, ok = d.Value("models").([]any)
		}
	}
	if !ok {
		return result(method, nil, "", "", label+" returned no model list", false)
	}
	var models []*pyjson.Object
	def := ""
	for _, raw := range rows {
		row, ok := raw.(*pyjson.Object)
		if !ok {
			continue
		}
		id := CleanModelID(or(row.Value("modelId"), row.Value("id")))
		if id == "" {
			continue
		}
		name := cleanLabel(or(row.Value("displayName"), row.Value("name")), id)
		if id == "auto" {
			def = id
			if desc := cleanLabel(row.Value("description"), ""); desc != "" {
				name = pytext.Prefix(desc, 80)
			}
		}
		models = append(models, entry(id, "detected", "cli", name, nil, ""))
	}
	if len(models) == 0 {
		return result(method, nil, "", "", label+" listed no models", false)
	}
	return result(method, models, def, "", "", true)
}

// geminiAliases are Gemini CLI's model aliases, which follow its current
// models (for versions without --list-models).
func geminiAliases() discovery {
	var models []*pyjson.Object
	for _, a := range [][2]string{{"auto", "Gemini CLI picks the model"}, {"pro", "the current Pro model"},
		{"flash", "the current Flash model"}, {"flash-lite", "the current Flash-Lite model"}} {
		models = append(models, entry(a[0], "available", "builtin", a[1], nil, ""))
	}
	return result("builtin:Gemini CLI aliases", models, "", "", "", false)
}

func or(a, b any) any {
	if pyjson.Truthy(a) {
		return a
	}
	return b
}
