package agentrules

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustCatalog(t *testing.T) *Catalog {
	t.Helper()
	c, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func on(v bool) *bool { return &v }

func TestCatalogIsWellFormed(t *testing.T) {
	c := mustCatalog(t)
	seen := map[string]bool{}
	for _, r := range c.Rules {
		if seen[r.ID] {
			t.Fatalf("duplicate id %s", r.ID)
		}
		seen[r.ID] = true
		if r.Kind != "required" && r.Kind != "preference" {
			t.Fatalf("%s: kind %q", r.ID, r.Kind)
		}
		if r.Kind == "required" && r.Scope != "ark" {
			t.Fatalf("%s: required rules belong to ARK", r.ID)
		}
		if r.Kind == "preference" && r.Scope != "project" && r.Scope != "personal" && r.Scope != "both" {
			t.Fatalf("%s: scope %q", r.ID, r.Scope)
		}
		if r.Title.EN == "" || r.Title.ZH == "" {
			t.Fatalf("%s: title in both languages", r.ID)
		}
		texts := []Text{r.Text}
		if r.TextBy != "" {
			texts = nil
			for _, x := range r.Texts {
				texts = append(texts, x)
			}
		}
		if r.OffText != nil {
			texts = append(texts, *r.OffText)
		}
		for _, x := range texts {
			if r.Silent {
				continue
			}
			if x.EN == "" || x.ZH == "" {
				t.Fatalf("%s: text in both languages", r.ID)
			}
			// Every API path, JSON field and placeholder of the English
			// text appears verbatim in the Chinese one.
			for _, tok := range apiTokens(x.EN) {
				if !strings.Contains(x.ZH, tok) {
					t.Fatalf("%s: %q missing from the Chinese text", r.ID, tok)
				}
			}
		}
	}
}

func apiTokens(s string) []string {
	var out []string
	for _, f := range strings.Fields(s) {
		f = strings.Trim(f, ".,;:()")
		if strings.HasPrefix(f, "/api/") || strings.HasPrefix(f, "{{") {
			out = append(out, f)
		}
	}
	return out
}

func TestDefaultsCompose(t *testing.T) {
	c := mustCatalog(t)
	got := c.Compose(Settings{}, Settings{})
	if !strings.HasPrefix(got.Version, "r-") || len(got.Version) != 10 {
		t.Fatalf("version %q", got.Version)
	}
	en := strings.Join(got.Lines.EN, "\n")
	for _, want := range []string{
		"CORE RULES (rules " + got.Version + ")",
		"C1. Read before your first write",
		"C8. First thing after setup, start the ARK watcher in the background",
		"C10. Before you change files, GET /api/agent/v1/setup with X-ARK-Host",
		"C11. Do not write the setup register",
		"P1. Plans need my approval",
		"P2. Post one short summary comment",
		"P3. Write progress lines, results and replies in the language I write to you in.",
		"P4. Task files go in the task's folder YYYYMMDD_<task ID> inside a task space from GET /api/agent/v1/setup.",
		"\"YYYY-MM-DD HH:MM UTC · what ran",
		"SELF-CHECK before every reply to me",
		"P1-P4?",
	} {
		if !strings.Contains(en, want) {
			t.Fatalf("missing %q in\n%s", want, en)
		}
	}
	if strings.Contains(en, "{{") || strings.Contains(strings.Join(got.Lines.ZH, "\n"), "{{") {
		t.Fatalf("unfilled placeholder:\n%s", en)
	}
	if len(got.Lines.EN) != len(got.Lines.ZH) {
		t.Fatal("languages differ in length")
	}
	// Off by default: not in the prompt.
	for _, absent := range []string{"exact command that reproduces", "by PID", "Deployment:"} {
		if strings.Contains(en, absent) {
			t.Fatalf("default prompt has %q", absent)
		}
	}
	if again := c.Compose(Settings{}, Settings{}); again.Version != got.Version || strings.Join(again.Lines.EN, "") != strings.Join(got.Lines.EN, "") {
		t.Fatal("composition is not deterministic")
	}
}

// The web client falls back to the default composition when it talks to an
// older server (web/js/starter-prompts.js START_SHARED.rules); it must be
// this catalog's.  After a catalog change, regenerate those lines.
func TestStarterPromptFallbackIsTheDefaultComposition(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "web", "js", "starter-prompts.js"))
	if err != nil {
		t.Skip("web sources not beside the server:", err)
	}
	got := mustCatalog(t).Compose(Settings{}, Settings{})
	for _, lines := range [][]string{got.Lines.EN, got.Lines.ZH} {
		for _, line := range lines {
			var b strings.Builder
			enc := json.NewEncoder(&b)
			enc.SetEscapeHTML(false)
			if err := enc.Encode(line); err != nil {
				t.Fatal(err)
			}
			if lit := strings.TrimSpace(b.String()); !strings.Contains(string(src), lit) {
				t.Fatalf("starter-prompts.js lacks the default rule line %s", lit)
			}
		}
	}
}

func TestPreferencesChangeTextAndVersion(t *testing.T) {
	c := mustCatalog(t)
	base := c.Compose(Settings{}, Settings{})
	personal := Settings{Rules: map[string]RuleSetting{
		"plan-approval":   {Enabled: on(false)},
		"summary-comment": {Enabled: on(false)},
		"language":        {Params: map[string]string{"records_language": "zh"}},
		"progress-format": {Params: map[string]string{"progress_format": "YYYY-MM-DD HH:MM"}},
	}, Custom: []Custom{{ID: "no-periods", Enabled: true, Text: Text{EN: "Promo copy has no full stops."}}}}
	project := Settings{Rules: map[string]RuleSetting{
		"task-folder":      {Params: map[string]string{"folder_pattern": "YYYYMMDD_<PREFIX>-<NAME>-<serial>"}},
		"no-kill-by-name":  {Enabled: on(true)},
		"evidence-command": {Enabled: on(true)},
		// A personal rule set on the project is ignored: each rule reads only its own scope.
		"plan-approval": {Enabled: on(true)},
	}}
	got := c.Compose(project, personal)
	if got.Version == base.Version {
		t.Fatal("version did not change")
	}
	en := strings.Join(got.Lines.EN, "\n")
	for _, want := range []string{
		"P1. Plans do not need my approval",
		"P2. Post no comments except replies to mine",
		"in Chinese (中文)",
		"\"YYYY-MM-DD HH:MM · what ran",
		"YYYYMMDD_<PREFIX>-<NAME>-<serial>",
		"by PID",
		"exact command that reproduces",
		"Promo copy has no full stops.",
	} {
		if !strings.Contains(en, want) {
			t.Fatalf("missing %q in\n%s", want, en)
		}
	}
	zh := strings.Join(got.Lines.ZH, "\n")
	if !strings.Contains(zh, "Promo copy has no full stops.") {
		t.Fatal("custom rule without Chinese text falls back to English")
	}
}

func TestValidate(t *testing.T) {
	c := mustCatalog(t)
	bad := []struct {
		scope string
		s     Settings
	}{
		{"personal", Settings{Rules: map[string]RuleSetting{"progress-body": {Enabled: on(false)}}}},
		{"personal", Settings{Rules: map[string]RuleSetting{"nope": {}}}},
		{"project", Settings{Rules: map[string]RuleSetting{"plan-approval": {Enabled: on(false)}}}},
		{"personal", Settings{Rules: map[string]RuleSetting{"language": {Params: map[string]string{"records_language": "fr"}}}}},
		{"project", Settings{Rules: map[string]RuleSetting{"task-folder": {Params: map[string]string{"folder_pattern": "a\nb"}}}}},
		{"personal", Settings{Custom: []Custom{{ID: "Bad Id", Text: Text{EN: "x"}}}}},
		{"personal", Settings{Custom: []Custom{{ID: "empty"}}}},
	}
	for i, b := range bad {
		if err := c.Validate(b.scope, b.s); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
	ok := Settings{Rules: map[string]RuleSetting{"plan-approval": {Enabled: on(false)}}, Custom: []Custom{{ID: "x-1", Text: Text{ZH: "规则"}}}}
	if err := c.Validate("personal", ok); err != nil {
		t.Fatal(err)
	}
}

func TestSaveLoad(t *testing.T) {
	dir := t.TempDir()
	path := PersonalFile(dir, "../alice")
	if filepath.Dir(path) != filepath.Join(dir, "agent-rules") {
		t.Fatalf("user name escapes the directory: %s", path)
	}
	s, err := Load(path)
	if err != nil || len(s.Rules) != 0 {
		t.Fatalf("missing file: %v %v", s, err)
	}
	want := Settings{Rules: map[string]RuleSetting{"plan-approval": {Enabled: on(false)}}}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || got.Rules["plan-approval"].Enabled == nil || *got.Rules["plan-approval"].Enabled {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temp file left behind")
	}
}

func TestGitPushRule(t *testing.T) {
	c := mustCatalog(t)
	base := c.Compose(Settings{}, Settings{})
	if strings.Contains(strings.Join(base.Lines.EN, "\n"), "push the branch") {
		t.Fatal("git-push is on by default")
	}
	// The person turns it on for every repository; the project narrows it.
	personal := Settings{Rules: map[string]RuleSetting{"git-push": {Enabled: on(true)}}}
	got := c.Compose(Settings{}, personal)
	en := strings.Join(got.Lines.EN, "\n")
	for _, want := range []string{"push the branch to its remote (origin,", "every git repository you commit to", "standing approval",
		"your work branches only, never the default branch", "Never force-push"} {
		if !strings.Contains(en, want) {
			t.Fatalf("missing %q in\n%s", want, en)
		}
	}
	if got.Version == base.Version {
		t.Fatal("version unchanged")
	}
	project := Settings{Rules: map[string]RuleSetting{"git-push": {Params: map[string]string{
		"git_repos": "ark-studio, agent-team-tasks", "git_branches": "both", "git_remote": "MasterYip/ARK-Studio-Dev"}}}}
	got = c.Compose(project, personal)
	en, zh := strings.Join(got.Lines.EN, "\n"), strings.Join(got.Lines.ZH, "\n")
	for _, want := range []string{"(MasterYip/ARK-Studio-Dev,", "the repositories ark-studio, agent-team-tasks",
		"your work branches and the default branch"} {
		if !strings.Contains(en, want) {
			t.Fatalf("missing %q in\n%s", want, en)
		}
	}
	if !strings.Contains(zh, "仓库 ark-studio、agent-team-tasks；工作分支和默认分支") {
		t.Fatalf("zh rendering:\n%s", zh)
	}
	var e Effective
	for _, x := range got.Rules {
		if x.ID == "git-push" {
			e = x
		}
	}
	if !e.Enabled || e.Source != "project" {
		t.Fatalf("project should override personal: %+v", e)
	}
	// The project switching it off wins over the person.
	off := Settings{Rules: map[string]RuleSetting{"git-push": {Enabled: on(false)}}}
	if strings.Contains(strings.Join(c.Compose(off, personal).Lines.EN, "\n"), "push the branch") {
		t.Fatal("project off should win")
	}
	// "all" among named repositories means all.
	all := Settings{Rules: map[string]RuleSetting{"git-push": {Enabled: on(true), Params: map[string]string{"git_repos": "ark-studio,all"}}}}
	if !strings.Contains(strings.Join(c.Compose(all, Settings{}).Lines.EN, "\n"), "every git repository you commit to") {
		t.Fatal("all should win")
	}
	// Both scopes may set it; bad values are refused.
	for _, scope := range []string{"project", "personal"} {
		if err := c.Validate(scope, project); err != nil {
			t.Fatalf("%s: %v", scope, err)
		}
	}
	for _, bad := range []map[string]string{{"git_branches": "release"}, {"git_repos": "a b"}, {"git_remote": "x\ny"}} {
		if err := c.Validate("project", Settings{Rules: map[string]RuleSetting{"git-push": {Params: bad}}}); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
}
