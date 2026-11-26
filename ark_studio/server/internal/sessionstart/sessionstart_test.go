package sessionstart

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func has(t *testing.T, s Start, mode string, parts ...string) {
	t.Helper()
	if s.Mode != mode {
		t.Fatalf("mode %q, want %q:\n%s", s.Mode, mode, s.Text)
	}
	for _, p := range parts {
		if !strings.Contains(s.Text, p) {
			t.Fatalf("start lacks %q:\n%s", p, s.Text)
		}
	}
}

var ready = Setup{Known: true, ProjectState: "confirmed", UserState: "confirmed", HostID: "abc123def456"}

func TestSetupMissingStartsWithSetup(t *testing.T) {
	f := Facts{Setup: Setup{Known: true, ProjectState: "unset", HostID: "abc123def456"}, Task: &Task{UID: "T-0042", Title: "x", Status: "active"}}
	has(t, Compose(f), ModeSetup, "Start: set up first", "Setup of this project is unset", "abc123def456", texts.Setup[0].EN)
	f.Setup = Setup{Known: true, ProjectState: "confirmed", UserState: "proposed"}
	has(t, Compose(f), ModeSetup, "your setup layer is proposed")
	f.Setup = Setup{Known: true, ProjectState: "verified", NeedsLocation: []string{"task_space:main"}, HostID: "h1", HostLabel: "laptop"}
	has(t, Compose(f), ModeDeviceSetup, "task_space:main", "laptop, h1", texts.DeviceSetup[1].EN, "setup/verify")
}

func TestTaskChosenContinuesWithSendBacks(t *testing.T) {
	task := &Task{UID: "T-0042", Title: "Render", Status: "active", LastOutcome: "sent_back",
		LastProgress: "- 2026-11-24 10:00 UTC · rendered shot 3",
		Open:         []Comment{{ID: 127, Kind: "sent_back", Author: "MasterYip", Body: "fix the colour"}, {ID: 128, Kind: "comment", Author: "ann", Body: "and the title"}}}
	s := Compose(Facts{Setup: ready, Task: task})
	has(t, s, ModeContinue, `continue T-0042 "Render"`, "last review: sent_back", "rendered shot 3",
		"#127 send-back by MasterYip: fix the colour", "#128 comment by ann", "TASK: continue T-0042.")
	if s.Summary != "continue T-0042 (1 send-back comment(s))" || s.Task != "T-0042" {
		t.Fatalf("summary %q task %q", s.Summary, s.Task)
	}
	if strings.Contains(s.Text, "{{unit}}") {
		t.Fatal("placeholder left")
	}
	task.InReview = true
	has(t, Compose(Facts{Setup: ready, Task: task}), ModeContinue, "waiting for my review")
}

// A send-back is why the task came back: the Start section keeps it whole.
func TestLongSendBackIsNotCut(t *testing.T) {
	body := strings.Repeat("Redo the colour grade of shot 3. ", 600) + "\nSecond paragraph.\r\nLast line <END>"
	task := &Task{UID: "T-0042", Title: "Render", Status: "active",
		Open: []Comment{{ID: 127, Kind: "sent_back", Author: "MasterYip", Body: body}}}
	s := Compose(Facts{Setup: ready, Task: task})
	has(t, s, ModeContinue, strings.TrimSpace(strings.Repeat("Redo the colour grade of shot 3. ", 600)),
		"\n  Second paragraph.\n  Last line <END>")
}

func TestResume(t *testing.T) {
	f := Facts{Setup: Setup{Known: true, ProjectState: "unset"}, Resume: &Resume{Turn: 3, Status: "stopped", Notice: "context limit"},
		Task:   &Task{UID: "T-0007", Title: "Draft", Status: "active", LastProgress: "2026-11-24 09:00 UTC · wrote section 2"},
		Chosen: &Chosen{ID: "status", Title: "Check", Text: "TASK: status"}}
	has(t, Compose(f), ModeResume, "run 3", "after it was stopped", "context limit", "wrote section 2", texts.Resume[1].EN)
}

func TestNothingChosenWaitsOrFollows(t *testing.T) {
	has(t, Compose(Facts{Setup: ready}), ModeWait, "wait for my first message")
	has(t, Compose(Facts{Setup: ready, Prompt: "fix the bug"}), ModeFollow, "follow my first message")
	has(t, Compose(Facts{}), ModeWait) // setup unknown: no guess
	if DefaultMode(Facts{Setup: ready}) != ModeFree {
		t.Fatal("default without a task")
	}
}

func TestChosenPromptOverridesDefault(t *testing.T) {
	f := Facts{Setup: Setup{Known: true, ProjectState: "unset"}, Chosen: &Chosen{ID: "plan-idea", Title: "Plan an idea", Text: "TASK: plan this idea: X."}}
	has(t, Compose(f), ModeChosen, "Start (chosen at launch: Plan an idea):", "TASK: plan this idea: X.")
	f.Chosen = &Chosen{ID: ModeFree}
	has(t, Compose(f), ModeWait)
	f.Chosen = &Chosen{ID: ModeContinue, Title: "Continue", Text: "TASK: continue T-1."}
	has(t, Compose(f), ModeChosen, "TASK: continue T-1.") // no task facts: the client's text
}

func TestLastProgress(t *testing.T) {
	body := "## Plan\n2026-01-01 is a date in prose? no\n\n## Progress\n- 2026-11-23 10:00 UTC · a\n- **2026-11-24 11:00 UTC** · b\n\nTrailing text"
	if got := LastProgress(body); got != "- **2026-11-24 11:00 UTC** · b" {
		t.Fatalf("%q", got)
	}
}

// texts.json is the starter prompts' text: every line is in
// web/js/starter-prompts.js.  After changing those prompts, regenerate it.
func TestTextsAreTheStarterPrompts(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "web", "js", "starter-prompts.js"))
	if err != nil {
		t.Skip("web sources not beside the server:", err)
	}
	all := [][]Line{texts.Setup, texts.DeviceSetup, texts.Continue, texts.Resume}
	for _, list := range all {
		if len(list) == 0 {
			t.Fatal("empty text list")
		}
		for _, l := range list {
			for _, s := range []string{l.EN, l.ZH} {
				var b strings.Builder
				enc := json.NewEncoder(&b)
				enc.SetEscapeHTML(false)
				_ = enc.Encode(s)
				if lit := strings.TrimSpace(b.String()); !strings.Contains(string(src), lit) {
					t.Fatalf("starter-prompts.js lacks %s", lit)
				}
			}
		}
	}
}
