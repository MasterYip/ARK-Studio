package sessions

import (
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

func TestStopByUserAndClose(t *testing.T) {
	m := streamManager(t)
	s, err := m.Start(nil, pyjson.NewObject("platform", "claude", "prompt", "slow start"), "http://127.0.0.1:1", "alice")
	if err != nil {
		t.Fatal(err)
	}
	defer killSession(m, s)
	waitFor(t, "slow turn", func() bool { return strings.Contains(logText(m, s), "echo: slow start") })
	// Stop in a turn interrupts it: the session is still the person's to use.
	if _, err := m.StopByUser(s.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "interrupted", func() bool { return metaOf(m, s, "turn_state") == "waiting" })
	if m.NudgeExcluded(s) || !m.IsActive(s) {
		t.Fatal("an interrupted turn keeps the session open")
	}
	// Close ends the process and keeps ARK away from the session.
	pub, err := m.Close(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if m.IsActive(s) || pub.Value("closed") != true || !m.NudgeExcluded(s) {
		t.Fatalf("closed: active=%v pub=%v", m.IsActive(s), pub.Value("closed"))
	}
	// A person's next message reopens it (resumed natively).
	if _, err := m.ContinueSession(nil, s.ID, "back again", ContinueOptions{Actor: "alice"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "resumed", func() bool { return strings.Contains(logText(m, s), "echo: back again") })
	if m.NudgeExcluded(s) || m.Public(s, false).Has("closed") {
		t.Fatal("reopened")
	}
	// Stop while it waits ends the process: ARK leaves it alone.
	waitFor(t, "waiting", func() bool { return metaOf(m, s, "turn_state") == "waiting" })
	if _, err := m.StopByUser(s.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "ended", func() bool { return !m.IsActive(s) })
	if !m.NudgeExcluded(s) {
		t.Fatal("ended by a person")
	}
}
