// Package promptlib keeps the custom prompts of the Home prompt library
// (2026-11-22): prompts a person writes for their coding agents, in their
// own scope or, written by a project admin, the project's.  The built-in
// prompts live in the web app (web/js/starter-prompts.js); the copied text
// is always composed with the project's agent rules (internal/agentrules),
// so a custom prompt only holds its own instructions.
//
// Storage follows the agent rules: a project's prompts in
// <project>/prompts.json, a person's in <private>/prompts/<user>.json, which
// also keeps their Home pins per project.  Each prompt has a content version
// ("p-" + hash), the library one over all of them ("l-" + hash).
package promptlib

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	Format      = "ark-prompts"
	MaxPrompts  = 50 // per scope
	MaxPins     = 12
	maxTitle    = 80
	maxDesc     = 200
	maxBody     = 8000
	ScopeUser   = "personal"
	ScopeProj   = "project"
	customIDPre = "cp-"
)

// Placeholders are filled when a prompt is copied: {project} the project's
// name and id, {unit} a unit id chosen at copy time, {user} the person.
var Placeholders = []string{"{project}", "{unit}", "{user}"}

var (
	rePlaceholder = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)
	reCustomID    = regexp.MustCompile(`^cp-[0-9a-f]{8}$`)
	rePinID       = regexp.MustCompile(`^[a-z][a-z0-9-]{0,40}$`)
	reUserFile    = regexp.MustCompile(`[^A-Za-z0-9._-]`)
	reProjectKey  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,80}$`)
)

// Prompt is one custom prompt.
type Prompt struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Body        string `json:"body"`
	Scope       string `json:"scope,omitempty"` // set when listed
	CreatedBy   string `json:"created_by,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
	UpdatedBy   string `json:"updated_by,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
	Version     string `json:"version,omitempty"`
}

// File is one scope's prompts; a person's file also has their pins.
type File struct {
	Format  string              `json:"format"`
	Prompts []Prompt            `json:"prompts"`
	Pins    map[string][]string `json:"pins,omitempty"` // project id -> ordered prompt ids
}

// ProjectFile is where a project keeps its prompts.
func ProjectFile(projectDir string) string { return filepath.Join(projectDir, "prompts.json") }

// PersonalFile is where a person's prompts and pins live.
func PersonalFile(privateDir, user string) string {
	name := reUserFile.ReplaceAllString(user, "_")
	if name == "" {
		name = "_"
	}
	return filepath.Join(privateDir, "prompts", name+".json")
}

// Load reads a file; a missing one is empty.
func Load(path string) (File, error) {
	f := File{Format: Format}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	f.Format = Format
	for i := range f.Prompts {
		f.Prompts[i].Version = ContentVersion(f.Prompts[i])
	}
	return f, nil
}

var saveMu sync.Mutex

// Update reads a file, applies fn and writes it atomically (one writer at a time).
func Update(path string, fn func(*File) error) (File, error) {
	saveMu.Lock()
	defer saveMu.Unlock()
	f, err := Load(path)
	if err != nil {
		return f, err
	}
	if err := fn(&f); err != nil {
		return f, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return f, err
	}
	for i := range f.Prompts {
		f.Prompts[i].Scope, f.Prompts[i].Version = "", ""
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return f, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return f, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return f, err
	}
	for i := range f.Prompts {
		f.Prompts[i].Version = ContentVersion(f.Prompts[i])
	}
	return f, nil
}

// ContentVersion hashes what an agent receives: title, description, body.
func ContentVersion(p Prompt) string {
	h := sha256.Sum256([]byte(p.Title + "\x00" + p.Description + "\x00" + p.Body))
	return "p-" + hex.EncodeToString(h[:])[:8]
}

// LibraryVersion hashes both scopes' prompts.
func LibraryVersion(lists ...[]Prompt) string {
	h := sha256.New()
	for _, list := range lists {
		for _, p := range list {
			fmt.Fprintf(h, "%s\x00%s\n", p.ID, ContentVersion(p))
		}
		h.Write([]byte{0})
	}
	return "l-" + hex.EncodeToString(h.Sum(nil))[:8]
}

// Input is a prompt as sent by a person or an agent.
type Input struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Body        string `json:"body"`
}

// Clean trims and checks one prompt; warnings name placeholders that are not
// filled at copy time (they stay as written).
func Clean(in Input) (Input, []string, error) {
	in.Title = strings.TrimSpace(in.Title)
	in.Description = strings.TrimSpace(strings.Join(strings.Fields(in.Description), " "))
	in.Body = strings.TrimSpace(strings.ReplaceAll(in.Body, "\r\n", "\n"))
	switch {
	case in.Title == "":
		return in, nil, errors.New("a prompt needs a title")
	case utf8.RuneCountInString(in.Title) > maxTitle:
		return in, nil, fmt.Errorf("the title is longer than %d characters", maxTitle)
	case utf8.RuneCountInString(in.Description) > maxDesc:
		return in, nil, fmt.Errorf("the description is longer than %d characters", maxDesc)
	case in.Body == "":
		return in, nil, errors.New("a prompt needs a body")
	case utf8.RuneCountInString(in.Body) > maxBody:
		return in, nil, fmt.Errorf("the body is longer than %d characters", maxBody)
	case in.ID != "" && !reCustomID.MatchString(in.ID):
		return in, nil, errors.New("id must be cp-<8 hex digits>")
	}
	var warn []string
	seen := map[string]bool{}
	for _, m := range rePlaceholder.FindAllStringSubmatch(in.Body+" "+in.Title, -1) {
		ph := "{" + m[1] + "}"
		known := false
		for _, k := range Placeholders {
			known = known || k == ph
		}
		if !known && !seen[ph] {
			seen[ph] = true
			warn = append(warn, fmt.Sprintf("%s is not a placeholder (use %s); it stays as written", ph, strings.Join(Placeholders, ", ")))
		}
	}
	return in, warn, nil
}

func newID(existing []Prompt) string {
	for {
		var b [4]byte
		_, _ = rand.Read(b[:])
		id := customIDPre + hex.EncodeToString(b[:])
		taken := false
		for _, p := range existing {
			taken = taken || p.ID == id
		}
		if !taken {
			return id
		}
	}
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// Put creates (no id) or replaces (known id) a prompt in f; it returns the stored prompt.
func Put(f *File, in Input, actor string) (Prompt, error) {
	if in.ID != "" {
		for i, p := range f.Prompts {
			if p.ID == in.ID {
				p.Title, p.Description, p.Body = in.Title, in.Description, in.Body
				p.UpdatedBy, p.UpdatedAt = actor, now()
				f.Prompts[i] = p
				return p, nil
			}
		}
		return Prompt{}, ErrNotFound
	}
	if len(f.Prompts) >= MaxPrompts {
		return Prompt{}, fmt.Errorf("a scope holds at most %d prompts", MaxPrompts)
	}
	at := now()
	p := Prompt{ID: newID(f.Prompts), Title: in.Title, Description: in.Description, Body: in.Body,
		CreatedBy: actor, CreatedAt: at, UpdatedBy: actor, UpdatedAt: at}
	f.Prompts = append(f.Prompts, p)
	return p, nil
}

// ErrNotFound is a prompt id that is not in the scope.
var ErrNotFound = errors.New("no such prompt in this scope")

// Delete removes a prompt from f.
func Delete(f *File, id string) error {
	for i, p := range f.Prompts {
		if p.ID == id {
			f.Prompts = append(f.Prompts[:i], f.Prompts[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

// Import adds prompts from an export; a prompt with the same title and body
// as one already there is skipped.  It returns how many were added and skipped.
func Import(f *File, list []Input, actor string) (added, skipped int, err error) {
	for _, raw := range list {
		in, _, err := Clean(Input{Title: raw.Title, Description: raw.Description, Body: raw.Body})
		if err != nil {
			return added, skipped, fmt.Errorf("prompt %d: %w", added+skipped+1, err)
		}
		dup := false
		for _, p := range f.Prompts {
			dup = dup || (p.Title == in.Title && p.Body == in.Body)
		}
		if dup {
			skipped++
			continue
		}
		if _, err := Put(f, in, actor); err != nil {
			return added, skipped, err
		}
		added++
	}
	return added, skipped, nil
}

// CleanPins checks a pin list: known id shapes, no repeats, at most MaxPins.
func CleanPins(ids []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if !rePinID.MatchString(id) {
			return nil, fmt.Errorf("%q is not a prompt id", id)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	if len(out) > MaxPins {
		return nil, fmt.Errorf("at most %d prompts can be pinned", MaxPins)
	}
	return out, nil
}

// ProjectKey checks a project id used as a pin key.
func ProjectKey(id string) bool { return reProjectKey.MatchString(id) }

// Listed returns a copy of the prompts with their scope set, newest first.
func Listed(list []Prompt, scope string) []Prompt {
	out := make([]Prompt, len(list))
	copy(out, list)
	for i := range out {
		out[i].Scope = scope
		out[i].Version = ContentVersion(out[i])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out
}
