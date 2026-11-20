// Package agentrules composes the rules an external coding agent follows in
// an ARK project (T-0053).  The catalog (catalog.json) holds ARK's required
// protocol, always in the prompt, and preference rules that a project and a
// person switch on or off and fill in.  Compose turns the catalog plus the
// project and personal settings into numbered prompt lines and a version
// that changes whenever the effective text changes, so a pasted prompt can
// be traced to the rules it was made from and a long-running agent can see
// that the rules moved on (GET /api/agent/v1/rules).
//
// Storage is plain JSON beside existing data: the project's settings in
// <project folder>/agent-rules.json (they travel with the project), a
// person's settings under the private directory, one file per user.
package agentrules

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

//go:embed catalog.json
var catalogJSON []byte

// Text is one string in both prompt languages.
type Text struct {
	EN string `json:"en"`
	ZH string `json:"zh"`
}

// Pick returns the text for lang ("zh" or anything else for English).
func (t Text) Pick(lang string) string {
	if lang == "zh" && t.ZH != "" {
		return t.ZH
	}
	return t.EN
}

// Param is a value a preference rule takes.  A multi param holds a
// comma-separated list (Source names where the settings UI finds the
// options, e.g. "git_repos": the project's registered repositories and
// task spaces).  ChoiceText renders a value in the prompt language;
// ListText renders a multi value without its own ChoiceText ("{list}").
type Param struct {
	ID         string          `json:"id"`
	Default    string          `json:"default"`
	Choices    []string        `json:"choices,omitempty"`
	Multi      bool            `json:"multi,omitempty"`
	Source     string          `json:"source,omitempty"`
	Max        int             `json:"max,omitempty"`
	Label      Text            `json:"label"`
	ChoiceText map[string]Text `json:"choice_text,omitempty"`
	ListText   *Text           `json:"list_text,omitempty"`
}

var reListItem = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,79}$`)

// splitList is a multi value as items: trimmed, empty ones dropped, no repeats.
func splitList(v string) []string {
	var out []string
	seen := map[string]bool{}
	for _, x := range strings.Split(v, ",") {
		x = strings.TrimSpace(x)
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// render is a param value as prompt text in one language.
func (p Param) render(v, lang string) string {
	if t, ok := p.ChoiceText[v]; ok {
		return t.Pick(lang)
	}
	if !p.Multi {
		return v
	}
	items := splitList(v)
	for _, x := range items {
		if t, ok := p.ChoiceText[x]; ok { // "all" wins over named items
			return t.Pick(lang)
		}
	}
	sep := ", "
	if lang == "zh" {
		sep = "、"
	}
	list := strings.Join(items, sep)
	if p.ListText != nil {
		return strings.ReplaceAll(p.ListText.Pick(lang), "{list}", list)
	}
	return list
}

// Rule is one catalog entry.
type Rule struct {
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`  // "required" or "preference"
	Scope   string          `json:"scope"` // "ark", "project", "personal" or "both" (personal, project overrides)
	Order   int             `json:"order"`
	Default bool            `json:"default,omitempty"`
	Silent  bool            `json:"silent,omitempty"` // only params, no line of its own
	Title   Text            `json:"title"`
	Text    Text            `json:"text,omitempty"`
	OffText *Text           `json:"off_text,omitempty"`
	Params  []Param         `json:"params,omitempty"`
	TextBy  string          `json:"text_by,omitempty"`
	Texts   map[string]Text `json:"texts,omitempty"`
}

// Catalog is catalog.json.
type Catalog struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	Header  Text   `json:"header"`
	Footer  Text   `json:"footer"`
	FooterP Text   `json:"footer_preferences"`
	Rules   []Rule `json:"rules"`
}

var (
	catOnce sync.Once
	cat     *Catalog
	catErr  error
)

// Default is the embedded catalog.
func Default() (*Catalog, error) {
	catOnce.Do(func() {
		var c Catalog
		if catErr = json.Unmarshal(catalogJSON, &c); catErr == nil {
			sort.SliceStable(c.Rules, func(i, j int) bool { return c.Rules[i].Order < c.Rules[j].Order })
			cat = &c
		}
	})
	return cat, catErr
}

// RuleSetting is one rule's stored choice.
type RuleSetting struct {
	Enabled *bool             `json:"enabled,omitempty"`
	Params  map[string]string `json:"params,omitempty"`
}

// Custom is a rule a project or person writes themselves.
type Custom struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	Text    Text   `json:"text"`
}

// Settings is what one scope stores.
type Settings struct {
	Rules  map[string]RuleSetting `json:"rules,omitempty"`
	Custom []Custom               `json:"custom,omitempty"`
}

// Effective is one rule as it applies.
type Effective struct {
	ID       string            `json:"id"`
	Kind     string            `json:"kind"`
	Scope    string            `json:"scope"`
	Enabled  bool              `json:"enabled"`
	Source   string            `json:"source"` // "ark", "default", "project" or "personal"
	Params   map[string]string `json:"params,omitempty"`
	Text     Text              `json:"text"`
	Silent   bool              `json:"silent,omitempty"`
	Marker   string            `json:"marker,omitempty"` // "C3", "P2"
	Custom   bool              `json:"custom,omitempty"`
	Title    Text              `json:"title"`
	Disabled *Text             `json:"text_when_off,omitempty"`
}

// Composed is the composer's result.
type Composed struct {
	Version string      `json:"version"`
	Rules   []Effective `json:"rules"`
	Lines   struct {
		EN []string `json:"en"`
		ZH []string `json:"zh"`
	} `json:"lines"`
}

const (
	maxCustom     = 20
	maxCustomText = 600
)

var (
	reCustomID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	rePlace    = regexp.MustCompile(`\{\{(\w+)\}\}`)
)

// Validate checks settings for one scope ("project" or "personal") against
// the catalog: known rule ids of that scope, required rules untouched,
// params of the right kind and length, custom rules bounded.
func (c *Catalog) Validate(scope string, s Settings) error {
	byID := map[string]Rule{}
	for _, r := range c.Rules {
		byID[r.ID] = r
	}
	for id, rs := range s.Rules {
		r, ok := byID[id]
		if !ok {
			return fmt.Errorf("unknown rule %q", id)
		}
		if r.Kind == "required" {
			return fmt.Errorf("rule %q is required by ARK and cannot be changed", id)
		}
		if r.Scope != scope && r.Scope != "both" {
			return fmt.Errorf("rule %q is a %s setting", id, r.Scope)
		}
		for pid, v := range rs.Params {
			p, ok := findParam(r, pid)
			if !ok {
				return fmt.Errorf("rule %q has no parameter %q", id, pid)
			}
			if p.Multi {
				for _, x := range splitList(v) {
					if !reListItem.MatchString(x) {
						return fmt.Errorf("%s.%s: %q is not a resource id", id, pid, x)
					}
				}
			} else if len(p.Choices) > 0 && !contains(p.Choices, v) {
				return fmt.Errorf("%s.%s must be one of %s", id, pid, strings.Join(p.Choices, ", "))
			}
			limit := p.Max
			if limit == 0 {
				limit = 200
			}
			if utf8.RuneCountInString(v) > limit || strings.ContainsAny(v, "\n\r") {
				return fmt.Errorf("%s.%s must be one line of at most %d characters", id, pid, limit)
			}
		}
	}
	if len(s.Custom) > maxCustom {
		return fmt.Errorf("at most %d custom rules", maxCustom)
	}
	seen := map[string]bool{}
	for _, cu := range s.Custom {
		if !reCustomID.MatchString(cu.ID) || seen[cu.ID] {
			return fmt.Errorf("custom rule id %q must be unique, lowercase letters, digits and dashes", cu.ID)
		}
		seen[cu.ID] = true
		if strings.TrimSpace(cu.Text.EN) == "" && strings.TrimSpace(cu.Text.ZH) == "" {
			return fmt.Errorf("custom rule %q has no text", cu.ID)
		}
		if utf8.RuneCountInString(cu.Text.EN) > maxCustomText || utf8.RuneCountInString(cu.Text.ZH) > maxCustomText {
			return fmt.Errorf("custom rule %q is longer than %d characters", cu.ID, maxCustomText)
		}
	}
	return nil
}

func findParam(r Rule, id string) (Param, bool) {
	for _, p := range r.Params {
		if p.ID == id {
			return p, true
		}
	}
	return Param{}, false
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Compose applies project then personal settings to the catalog.  Each
// preference rule takes its value only from the scope it belongs to.
// Output order is fixed: header, required rules C1..Cn in catalog order,
// enabled preference rules P1..Pm in catalog order, then custom rules
// (project first, then personal, in stored order), then the self-check.
func (c *Catalog) Compose(project, personal Settings) Composed {
	vars := map[string]string{}
	langVars := map[string]map[string]string{"en": {}, "zh": {}}
	var eff []Effective
	for _, r := range c.Rules {
		e := Effective{ID: r.ID, Kind: r.Kind, Scope: r.Scope, Title: r.Title, Silent: r.Silent, Source: "ark"}
		params := map[string]string{}
		for _, p := range r.Params {
			params[p.ID] = p.Default
		}
		if r.Kind == "required" {
			e.Enabled = true
		} else {
			e.Enabled, e.Source = r.Default, "default"
			var srcs []Settings
			var names []string
			switch r.Scope {
			case "personal":
				srcs, names = []Settings{personal}, []string{"personal"}
			case "both": // the person's choice, then the project's on top
				srcs, names = []Settings{personal, project}, []string{"personal", "project"}
			default:
				srcs, names = []Settings{project}, []string{"project"}
			}
			for i, src := range srcs {
				rs, ok := src.Rules[r.ID]
				if !ok {
					continue
				}
				if rs.Enabled != nil {
					e.Enabled = *rs.Enabled
				}
				for k, v := range rs.Params {
					if _, ok := params[k]; ok {
						params[k] = v
					}
				}
				e.Source = names[i]
			}
		}
		for k, v := range params {
			vars[k] = v
		}
		for _, p := range r.Params {
			for lang := range langVars {
				langVars[lang][p.ID] = p.render(params[p.ID], lang)
			}
		}
		if len(params) > 0 {
			e.Params = params
		}
		switch {
		case r.TextBy != "":
			e.Text = r.Texts[params[r.TextBy]]
		default:
			e.Text = r.Text
		}
		if r.OffText != nil {
			off := *r.OffText
			e.Disabled = &off
			if !e.Enabled {
				// A preference with an explicit "off" wording stays in the
				// prompt in that wording: switching it off is a rule too.
				e.Text, e.Enabled = off, true
				e.Source = r.Scope
			}
		}
		eff = append(eff, e)
	}
	for _, list := range []struct {
		scope string
		items []Custom
	}{{"project", project.Custom}, {"personal", personal.Custom}} {
		for _, cu := range list.items {
			t := cu.Text
			if t.EN == "" {
				t.EN = t.ZH
			}
			if t.ZH == "" {
				t.ZH = t.EN
			}
			eff = append(eff, Effective{ID: list.scope + ":" + cu.ID, Kind: "preference", Scope: list.scope,
				Enabled: cu.Enabled, Source: list.scope, Text: t, Custom: true, Title: Text{EN: cu.ID, ZH: cu.ID}})
		}
	}
	fillLang := func(s, lang string) string {
		return rePlace.ReplaceAllStringFunc(s, func(m string) string {
			key := m[2 : len(m)-2]
			if v, ok := langVars[lang][key]; ok {
				return v
			}
			if v, ok := vars[key]; ok {
				return v
			}
			return m
		})
	}
	nReq, nPref := 0, 0
	for i := range eff {
		e := &eff[i]
		e.Text = Text{EN: fillLang(e.Text.EN, "en"), ZH: fillLang(e.Text.ZH, "zh")}
		if !e.Enabled || e.Silent {
			continue
		}
		if e.Kind == "required" {
			nReq++
			e.Marker = "C" + strconv.Itoa(nReq)
		} else {
			nPref++
			e.Marker = "P" + strconv.Itoa(nPref)
		}
	}
	out := Composed{Rules: eff}
	out.Version = version(eff)
	vars["version"] = out.Version
	vars["preference_count"] = strconv.Itoa(nPref)
	for _, lang := range []string{"en", "zh"} {
		lines := []string{fillLang(c.Header.Pick(lang), lang)}
		for _, e := range eff {
			if e.Marker != "" {
				lines = append(lines, e.Marker+". "+e.Text.Pick(lang))
			}
		}
		footer := c.Footer.Pick(lang)
		if nPref > 0 {
			footer += c.FooterP.Pick(lang)
		}
		lines = append(lines, fillLang(footer, lang))
		if lang == "zh" {
			out.Lines.ZH = lines
		} else {
			out.Lines.EN = lines
		}
	}
	return out
}

// version hashes the enabled rule texts in both languages.
func version(eff []Effective) string {
	h := sha256.New()
	for _, e := range eff {
		if e.Marker == "" {
			continue
		}
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\n", e.Marker, e.ID, e.Text.EN, e.Text.ZH)
	}
	return "r-" + hex.EncodeToString(h.Sum(nil))[:8]
}

// ------------------------------------------------------------ storage

// ProjectFile is where a project keeps its rule settings.
func ProjectFile(projectDir string) string { return filepath.Join(projectDir, "agent-rules.json") }

var reUserFile = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// PersonalFile is where a person's rule settings live.
func PersonalFile(privateDir, user string) string {
	name := reUserFile.ReplaceAllString(user, "_")
	if name == "" {
		name = "_"
	}
	return filepath.Join(privateDir, "agent-rules", name+".json")
}

// Load reads settings; a missing file is empty settings.
func Load(path string) (Settings, error) {
	var s Settings
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	return s, err
}

var saveMu sync.Mutex

// Save writes settings atomically (temp file, then rename).
func Save(path string, s Settings) error {
	saveMu.Lock()
	defer saveMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
