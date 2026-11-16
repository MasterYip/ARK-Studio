// Package clock is the server's time source: the wall clock, or the time a
// differential replay pins per request (ARK_REPLAY_CLOCK=1 and the header
// X-Replay-Clock; see tests/python/contract/replayclock.py).
//
// Python modules read time in two ways and the port keeps both: most call
// db.utc_now(), which the replay pins (UTCNow, Now), and a few call their
// own module-level utc_now() on the wall clock that the replay does not pin
// (WallUTCNow).
package clock

import (
	"sync/atomic"
	"time"
)

// ISOSeconds is the layout of utc_now() strings without the offset.
const ISOSeconds = "2006-01-02T15:04:05"

// Wall is the real clock; tests may replace it.
var Wall = time.Now

var (
	replay     atomic.Pointer[time.Time]
	lastReplay atomic.Pointer[time.Time]
)

// Now is the pinned replay time when one is set, else the wall clock.
func Now() time.Time {
	if t := replay.Load(); t != nil {
		return *t
	}
	return Wall()
}

// SetReplay pins Now for the current request of a differential replay; nil
// restores the wall clock.  Replays are sequential, so one process-wide
// value is enough.
func SetReplay(t *time.Time) {
	replay.Store(t)
	if t != nil {
		lastReplay.Store(t)
	}
}

// Replay reports the pinned replay time, if any.
func Replay() (time.Time, bool) {
	if t := replay.Load(); t != nil {
		return *t, true
	}
	return time.Time{}, false
}

// LastReplay is the most recent pinned replay time (it survives the
// request), used to stamp background work such as agent-session output.
func LastReplay() (time.Time, bool) {
	if t := lastReplay.Load(); t != nil {
		return *t, true
	}
	return time.Time{}, false
}

// Format is datetime.isoformat(timespec="seconds") of a UTC time: ISO
// seconds with "+00:00".
func Format(t time.Time) string { return t.UTC().Format(ISOSeconds) + "+00:00" }

// UTCNow is db.utc_now() (replay-aware).
func UTCNow() string { return Format(Now()) }

// WallUTCNow is a module-level utc_now() Python keeps on the wall clock
// (providers.py): the replay does not pin it.
func WallUTCNow() string { return Format(Wall()) }

// Today is db.today() (replay-aware).
func Today() string { return Now().UTC().Format("2006-01-02") }

// Stamp is the time background work stamps output with: under a replay the
// last pinned second (also outside requests, so output read by background
// readers is stamped like the replay shim stamps it in Python), else the
// wall clock.
func Stamp() time.Time {
	if t, ok := LastReplay(); ok {
		return t
	}
	return Wall()
}
