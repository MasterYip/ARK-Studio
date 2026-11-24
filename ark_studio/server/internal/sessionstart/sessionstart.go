// Package sessionstart decides what an agent ARK launches does first
// (2026-11-24, T-0121): run setup, register this device, continue a task,
// resume an interrupted session, follow the first message or wait for it.
// The text is the one Home copies for external agents: texts.json holds
// the lines of the starter prompts (web/js/starter-prompts.js; a test keeps
// both the same), and a prompt chosen in the Run dialog arrives as text the
// web client built from the same source.  Pure: the server gathers the
// facts (Facts) and puts the result in the session block.
package sessionstart

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

//go:embed texts.json
var textsJSON []byte

// Line is one starter-prompt line in both languages.
type Line struct {
	EN string `json:"en"`
	ZH string `json:"zh"`
}

// Texts are the starter-prompt lines the starting points reuse.
type Texts struct {
	Setup       []Line `json:"setup"`
	DeviceSetup []Line `json:"device_setup"`
	Continue    []Line `json:"continue"`
	Resume      []Line `json:"resume"`
}

// Load returns the embedded texts.
func Load() Texts {
	var t Texts
	if err := json.Unmarshal(textsJSON, &t); err != nil {
		panic("sessionstart: texts.json: " + err.Error())
	}
	return t
}

var texts = Load()

// Modes of a start.
const (
	ModeSetup       = "setup"        // the project's or this person's setup is not confirmed
	ModeDeviceSetup = "device-setup" // setup is confirmed; this device lacks paths
	ModeContinue    = "continue"     // a task was chosen
	ModeResume      = "resume"       // a later run of the same session
	ModeChosen      = "chosen"       // a prompt chosen in the Run dialog
	ModeFollow      = "follow"       // nothing else: the first message says what to do
	ModeWait        = "wait"         // nothing else and no first message yet
	ModeFree        = "free"         // Run dialog choice: no prompt, follow or wait
)

// Setup is the readiness of the project for the launching person on the
// device the session runs on (GET /api/agent/v1/setup "readiness").
type Setup struct {
	Known         bool // false: not checked (no setup information)
	ProjectState  string
	UserState     string
	NeedsLocation []string
	HostID        string
	HostLabel     string
}

// NeedsSetup: the project layer is not confirmed, or this person's layer
// waits for a confirmation.
func (s Setup) NeedsSetup() bool {
	if !s.Known {
		return false
	}
	if s.ProjectState != "confirmed" && s.ProjectState != "verified" {
		return true
	}
	switch s.UserState {
	case "draft", "proposed", "stale":
		return true
	}
	return false
}

// NeedsDevice: setup is confirmed but this device has no path for some of
// the resources the work needs.
func (s Setup) NeedsDevice() bool {
	return s.Known && !s.NeedsSetup() && len(s.NeedsLocation) > 0
}

// Comment is an open comment on the task (a person's, unresolved).
type Comment struct {
	ID     int64
	Kind   string // comment, sent_back, decided, ...
	Author string
	Body   string
}

// Task is the task chosen for the session.
type Task struct {
	UID          string
	Title        string
	Status       string
	InReview     bool
	LastOutcome  string // of the last closed review round
	Open         []Comment
	LastProgress string // the last dated line of the body
}

// SendBacks counts the open send-back comments.
func (t *Task) SendBacks() int {
	n := 0
	for _, c := range t.Open {
		if c.Kind == "sent_back" {
			n++
		}
	}
	return n
}

// Chosen is a prompt picked in the Run dialog (its text already filled).
type Chosen struct {
	ID    string
	Title string
	Text  string
}

// Resume describes a later run of the same session.
type Resume struct {
	Turn   int64
	Status string // the session's state before this run
	Notice string
}

// Facts are what decides the start.
type Facts struct {
	Setup  Setup
	Task   *Task
	Prompt string // the first message ("" = none)
	Chosen *Chosen
	Resume *Resume
}

// Start is the session's starting point.
type Start struct {
	Mode    string `json:"mode"`
	Title   string `json:"title"`   // the chosen prompt's title, the task, ...
	Summary string `json:"summary"` // one line (English)
	Text    string `json:"text"`    // the "Start:" section of the session block
	Task    string `json:"task,omitempty"`
}

// DefaultMode is the start ARK proposes without a choice: resume for a
// later run, setup or device setup when missing, continue for a task, else
// free (follow the first message, or wait for it).
func DefaultMode(f Facts) string {
	switch {
	case f.Resume != nil:
		return ModeResume
	case f.Setup.NeedsSetup():
		return ModeSetup
	case f.Setup.NeedsDevice():
		return ModeDeviceSetup
	case f.Task != nil:
		return ModeContinue
	}
	return ModeFree
}

// Compose builds the start: a later run resumes; a prompt chosen in the
// Run dialog comes next; else the default.
func Compose(f Facts) Start {
	mode := DefaultMode(f)
	if f.Resume == nil && f.Chosen != nil {
		switch f.Chosen.ID {
		case "", ModeFree:
			mode = ModeFree
		case ModeSetup, ModeDeviceSetup, ModeContinue:
			// The built-in prompts with live facts: same lines, plus
			// what ARK knows now (missing paths, open comments).
			mode = f.Chosen.ID
			if mode == ModeContinue && f.Task == nil {
				mode = ModeChosen
			}
		default:
			mode = ModeChosen
		}
	}
	if mode == ModeFree {
		mode = ModeWait
		if strings.TrimSpace(f.Prompt) != "" {
			mode = ModeFollow
		}
	}
	var st Start
	switch mode {
	case ModeResume:
		st = resumeStart(f)
	case ModeSetup:
		st = setupStart(f.Setup)
	case ModeDeviceSetup:
		st = deviceStart(f.Setup)
	case ModeContinue:
		st = continueStart(f.Task)
	case ModeChosen:
		st = Start{Mode: ModeChosen, Title: f.Chosen.Title, Summary: "run the prompt chosen at launch: " + f.Chosen.Title,
			Text: "Start (chosen at launch: " + f.Chosen.Title + "):\n" + strings.TrimSpace(f.Chosen.Text)}
	case ModeFollow:
		st = Start{Mode: ModeFollow, Summary: "follow the first message",
			Text: "Start: follow my first message. Read GET /api/agent/v1/context first (rule C1)."}
	default:
		st = Start{Mode: ModeWait, Summary: "wait for the first message",
			Text: "Start: wait for my first message. Until it arrives, read GET /api/agent/v1/context and change nothing."}
	}
	if f.Task != nil && st.Task == "" {
		st.Task = f.Task.UID
	}
	return st
}

func en(lines []Line, vars map[string]string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		text := l.EN
		for k, v := range vars {
			text = strings.ReplaceAll(text, "{{"+k+"}}", v)
		}
		out = append(out, text)
	}
	return out
}

func device(s Setup) string {
	if s.HostLabel != "" && s.HostLabel != s.HostID {
		return s.HostLabel + ", " + s.HostID
	}
	return s.HostID
}

func setupStart(s Setup) Start {
	state := s.ProjectState
	if state == "" {
		state = "unset"
	}
	head := fmt.Sprintf("Start: set up first. Setup of this project is %s", state)
	if s.UserState == "draft" || s.UserState == "proposed" || s.UserState == "stale" {
		head += ", your setup layer is " + s.UserState
	}
	head += " (this device: " + device(s) + "). Do not start task work before I confirm setup."
	lines := append([]string{head}, en(texts.Setup, nil)...)
	return Start{Mode: ModeSetup, Title: "setup", Summary: "set up first (setup is " + state + ")", Text: strings.Join(lines, "\n")}
}

func deviceStart(s Setup) Start {
	head := "Start: register this device's paths first. Missing on this device (" + device(s) + "): " +
		strings.Join(s.NeedsLocation, ", ") + "."
	lines := append([]string{head}, en(texts.DeviceSetup, nil)...)
	lines = append(lines, "3. Then POST /api/agent/v1/setup/verify and tell me what is ready and what is still missing. Create no units while you do this.")
	return Start{Mode: ModeDeviceSetup, Title: "device setup", Summary: "register this device's paths (" +
		strings.Join(s.NeedsLocation, ", ") + ")", Text: strings.Join(lines, "\n")}
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func continueStart(t *Task) Start {
	head := fmt.Sprintf("Start: continue %s \"%s\" (status %s", t.UID, t.Title, t.Status)
	if t.InReview {
		head += ", waiting for my review"
	} else if t.LastOutcome != "" {
		head += ", last review: " + t.LastOutcome
	}
	head += ")."
	lines := []string{head}
	if t.LastProgress != "" {
		lines = append(lines, "Last progress line: "+clip(t.LastProgress, 300))
	}
	summary := "continue " + t.UID
	if len(t.Open) > 0 {
		lines = append(lines, fmt.Sprintf("Open comments to address first (%d; reply with reply_to and resolve each):", len(t.Open)))
		for _, c := range t.Open {
			kind := "comment"
			if c.Kind == "sent_back" {
				kind = "send-back"
			}
			lines = append(lines, fmt.Sprintf("- #%d %s by %s: %s", c.ID, kind, c.Author, clip(c.Body, 400)))
		}
		if n := t.SendBacks(); n > 0 {
			summary += fmt.Sprintf(" (%d send-back comment(s))", n)
		} else {
			summary += fmt.Sprintf(" (%d open comment(s))", len(t.Open))
		}
	}
	lines = append(lines, en(texts.Continue, map[string]string{"unit": t.UID})...)
	return Start{Mode: ModeContinue, Title: t.UID + " " + t.Title, Summary: summary, Text: strings.Join(lines, "\n"), Task: t.UID}
}

func resumeStart(f Facts) Start {
	r := f.Resume
	head := fmt.Sprintf("Start: resume. This is run %d of this ARK session; ARK continued it", r.Turn)
	if r.Status != "" {
		head += " after it was " + r.Status
	}
	head += "."
	lines := []string{head}
	if r.Notice != "" {
		lines = append(lines, "Last state: "+clip(r.Notice, 300))
	}
	if f.Task != nil {
		lines = append(lines, fmt.Sprintf("Task %s \"%s\" (status %s).", f.Task.UID, f.Task.Title, f.Task.Status))
		if f.Task.LastProgress != "" {
			lines = append(lines, "Last progress line: "+clip(f.Task.LastProgress, 300))
		}
		if len(f.Task.Open) > 0 {
			lines = append(lines, fmt.Sprintf("%d open comment(s) on it: GET /api/agent/v1/units/%s/comments.", len(f.Task.Open), f.Task.UID))
		}
	}
	lines = append(lines, "If my new message says what to do, do that; otherwise:")
	lines = append(lines, en(texts.Resume[1:], nil)...)
	return Start{Mode: ModeResume, Title: "resume", Summary: fmt.Sprintf("resume (run %d)", r.Turn), Text: strings.Join(lines, "\n")}
}

var progressRe = regexp.MustCompile(`^\s*(?:[-*]\s*)?(?:\*\*)?\d{4}-\d{2}-\d{2}`)

// LastProgress is the last dated line of a task body (the progress format
// of rule C3: "YYYY-MM-DD HH:MM UTC · ...").
func LastProgress(body string) string {
	lines := strings.Split(body, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if progressRe.MatchString(lines[i]) {
			return strings.TrimSpace(lines[i])
		}
	}
	return ""
}
