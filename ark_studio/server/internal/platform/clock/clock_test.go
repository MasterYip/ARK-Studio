package clock

import (
	"testing"
	"time"
)

func TestReplayPinning(t *testing.T) {
	wall := time.Date(2026, 1, 2, 3, 4, 5, 999, time.FixedZone("x", 3600))
	Wall = func() time.Time { return wall }
	defer func() { Wall = time.Now; SetReplay(nil) }()

	pinned := time.Date(2030, 12, 31, 23, 59, 59, 0, time.UTC)
	cases := []struct {
		name            string
		pin             *time.Time
		utc, wallS, day string
		replaying       bool
	}{
		{"wall", nil, "2026-01-02T02:04:05+00:00", "2026-01-02T02:04:05+00:00", "2026-01-02", false},
		{"pinned", &pinned, "2030-12-31T23:59:59+00:00", "2026-01-02T02:04:05+00:00", "2030-12-31", true},
	}
	for _, c := range cases {
		SetReplay(c.pin)
		if got := UTCNow(); got != c.utc {
			t.Errorf("%s: UTCNow %q, want %q", c.name, got, c.utc)
		}
		if got := WallUTCNow(); got != c.wallS {
			t.Errorf("%s: WallUTCNow %q, want %q", c.name, got, c.wallS)
		}
		if got := Today(); got != c.day {
			t.Errorf("%s: Today %q, want %q", c.name, got, c.day)
		}
		if _, ok := Replay(); ok != c.replaying {
			t.Errorf("%s: Replay ok=%v", c.name, ok)
		}
	}
	SetReplay(nil)
	if last, ok := LastReplay(); !ok || !last.Equal(pinned) {
		t.Errorf("LastReplay = %v %v, want the last pinned time", last, ok)
	}
}

func TestStamp(t *testing.T) {
	// before any replay pin in this process LastReplay may be set by
	// TestReplayPinning; Stamp must follow it once set.
	pinned := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
	SetReplay(&pinned)
	SetReplay(nil)
	if got := Stamp(); !got.Equal(pinned) {
		t.Fatalf("Stamp = %v, want the last pinned time", got)
	}
}
