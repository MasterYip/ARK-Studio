package sessions

import (
	"time"

	"arkstudio/go/internal/compat/pyjson"
)

// Closing a session.  A person ends a session explicitly (关闭 / End, or
// before it is deleted): its process stops and it is marked closed, so ARK
// never wakes or resumes it on its own (internal/sessionnudge reads
// NudgeExcluded).  A person's next message reopens it.
//
//   meta.closed      ended by a person with Close
//   meta.user_ended  its process was stopped by a person (Stop, Close)

// Close ends the session's process (when it runs) and marks it closed.
func (m *Manager) Close(id string) (*pyjson.Object, error) {
	s, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	s.Meta.Set("closed", true).Set("user_ended", true).Set("closed_at", NowISO())
	m.saveLocked(s)
	m.mu.Unlock()
	if err := m.endProcess(s); err != nil {
		return nil, err
	}
	m.notify(s, "close")
	return m.Public(s, true), nil
}

// endProcess stops a running process and waits for it (a long-lived Claude
// Code process is ended, not interrupted).
func (m *Manager) endProcess(s *Session) error {
	if !m.IsActive(s) {
		return nil
	}
	m.mu.Lock()
	p := s.proc
	m.mu.Unlock()
	if p != nil && p.running() && isStream(s) {
		m.mu.Lock()
		s.Meta.Set("turn_state", "waiting") // end the process, not the turn
		m.saveLocked(s)
		m.mu.Unlock()
	}
	if _, err := m.RequestStop(s.ID); err != nil {
		return err
	}
	deadline := time.Now().Add(StopGrace + 3*time.Second)
	for m.IsActive(s) && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if m.IsActive(s) {
		return serr("the session did not stop in time", 409, "")
	}
	return nil
}

// StopByUser is Stop pressed by a person: a long-lived process in a turn
// only has the turn interrupted; anything else ends the process, and ARK
// then leaves the session alone (no nudges, no resumes).
func (m *Manager) StopByUser(id string) (*pyjson.Object, error) {
	s, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	if state := m.streamTurnState(s); state != "running" && state != "interrupting" {
		m.mu.Lock()
		s.Meta.Set("user_ended", true)
		m.saveLocked(s)
		m.mu.Unlock()
	}
	return m.RequestStop(id)
}

// Reopen clears the closed marks (a person continues the session).
func (m *Manager) Reopen(s *Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.Meta.Has("closed") || s.Meta.Has("user_ended") {
		s.Meta.Delete("closed")
		s.Meta.Delete("user_ended")
		s.Meta.Delete("closed_at")
		m.saveLocked(s)
	}
}

// NudgeExcluded reports a session ARK must not wake or resume on its own.
func (m *Manager) NudgeExcluded(s *Session) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return pyjson.Truthy(s.Meta.Value("closed")) || pyjson.Truthy(s.Meta.Value("user_ended"))
}

// EndAndArchive ends the session, then takes it off the list; its folder
// (the transcript) is kept under .archived.
func (m *Manager) EndAndArchive(id string) (*pyjson.Object, error) {
	if _, err := m.Close(id); err != nil {
		return nil, err
	}
	return m.Archive(id)
}

// EndAndDelete ends the session, then deletes it with its files.
func (m *Manager) EndAndDelete(id string) (*pyjson.Object, error) {
	if _, err := m.Close(id); err != nil {
		return nil, err
	}
	return m.Delete(id)
}
