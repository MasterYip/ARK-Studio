package sessions

import (
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
)

// Session titles.  A session started without a first message has no title
// (the client shows "New session · <provider>") until it is renamed or the
// first message names it.

const autoTitleChars = 60

// Rename sets the session's title; an empty title returns to the default.
func (m *Manager) Rename(id, title string) (*pyjson.Object, error) {
	s, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	title = limit(pytext.Strip(strings.Join(strings.Fields(title), " ")), maxTitle)
	m.mu.Lock()
	defer m.mu.Unlock()
	if title == "" {
		s.Meta.Set("title", "")
		s.Meta.Delete("title_set")
	} else {
		s.Meta.Set("title", title).Set("title_set", true)
	}
	s.Meta.Set("last_activity_at", NowISO())
	m.saveLocked(s)
	go m.notify(s, "rename")
	return m.publicLocked(s, true), nil
}

// autoTitle names an untitled session after its first message.
func (m *Manager) autoTitle(s *Session, text string) {
	line := ""
	for _, part := range strings.Split(text, "\n") {
		if part = pytext.Strip(part); part != "" {
			line = part
			break
		}
	}
	if line == "" {
		return
	}
	if r := []rune(line); len(r) > autoTitleChars {
		line = strings.TrimSpace(string(r[:autoTitleChars-1])) + "…"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if pytext.Strip(strOr(s.Meta.Value("title"))) != "" {
		return
	}
	s.Meta.Set("title", line)
	m.saveLocked(s)
}
