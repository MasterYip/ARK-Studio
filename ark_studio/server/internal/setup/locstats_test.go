package setup

import (
	"strings"
	"testing"
	"time"

	"arkstudio/go/internal/compat/pyjson"
)

func statsEnv(t *testing.T) (*Env, *Viewer) {
	t.Helper()
	e := testEnv(t)
	ana := NewViewer(Viewer{User: "ana", Admin: true, CanWrite: true, HostID: "pc-1", HostSource: "header"})
	raw := pyjson.NewObject("resources", []any{
		pyjson.NewObject("id", "code", "kind", "repository", "name", "code",
			"details", pyjson.NewObject("role", "primary"), "path_or_ref", "/home/ana/code"),
		pyjson.NewObject("id", "db", "kind", "storage", "name", "db",
			"details", pyjson.NewObject("kind", "other"), "path_or_ref", "/home/ana/db.sqlite3"),
	})
	mustSave(t, e, ana, "project", raw, false)
	return e, ana
}

func report(items ...*obj) *obj {
	l := make([]any, len(items))
	for i, x := range items {
		l[i] = x
	}
	return pyjson.NewObject("items", l)
}

func TestReportLocationStatsScopedToDevice(t *testing.T) {
	e, ana := statsEnv(t)
	res, err := e.ReportLocationStats(ana, report(
		pyjson.NewObject("resource", "project/code", "bytes", int64(2300), "files", int64(12), "git_bytes", int64(1200)),
		pyjson.NewObject("resource", "project/nope", "bytes", int64(1)),
	), "human:ana", "desktop")
	if err != nil {
		t.Fatal(err)
	}
	if len(listOr(res.Value("stored"))) != 1 || len(listOr(res.Value("errors"))) != 1 {
		t.Fatalf("result: %s", pyjson.Dumps(res))
	}
	// Another device cannot report a path that lives on pc-1.
	bob := NewViewer(Viewer{User: "bob", CanWrite: true, HostID: "laptop-b", HostSource: "header"})
	if _, err := e.ReportLocationStats(bob, report(pyjson.NewObject("resource", "project/code", "bytes", int64(5))),
		"human:bob", "desktop"); err == nil || !strings.Contains(err.Error(), "no location") {
		t.Fatalf("other device accepted: %v", err)
	}
	// Naming another host id explicitly is refused too.
	_, err = e.ReportLocationStats(ana, report(pyjson.NewObject("resource", "project/code", "host_id", "laptop-b",
		"bytes", int64(5))), "human:ana", "desktop")
	if err == nil {
		t.Fatal("foreign host_id accepted")
	}
	// Read-only members and callers without an explicit device id cannot report.
	ro := NewViewer(Viewer{User: "carl", HostID: "pc-1", HostSource: "header"})
	if _, err := e.ReportLocationStats(ro, report(pyjson.NewObject("resource", "project/code", "bytes", int64(5))),
		"human:carl", "desktop"); err == nil || err.(*Error).Status != 403 {
		t.Fatalf("read-only accepted: %v", err)
	}
	implicit := NewViewer(Viewer{User: "ana", CanWrite: true, HostID: "pc-1"})
	if _, err := e.ReportLocationStats(implicit, report(pyjson.NewObject("resource", "project/code", "bytes", int64(5))),
		"human:ana", "desktop"); err == nil || err.(*Error).Code != "SETUP_HOST_REQUIRED" {
		t.Fatalf("implicit host accepted: %v", err)
	}

	p := mustPayload(t, e, ana)
	var stats *obj
	for _, x := range listOr(p.Value("resources")) {
		item := mapOf(x)
		if item.Value("key") == "project/code" {
			stats = mapOf(mapOf(listOr(item.Value("locations"))[0]).Value("stats"))
		}
		if item.Value("key") == "project/db" && mapOf(listOr(item.Value("locations"))[0]).Has("stats") {
			t.Fatal("unmeasured location has stats")
		}
	}
	if stats == nil || stats.Value("bytes") != int64(2300) || stats.Value("git_bytes") != int64(1200) ||
		stats.Value("source") != "desktop" || stats.Value("git") != true {
		t.Fatalf("stats: %s", pyjson.Dumps(stats))
	}
	dev := deviceByID(p, "pc-1")
	found := false
	for _, x := range listOr(dev.Value("paths")) {
		if pp := mapOf(x); pp.Value("resource") == "project/code" && mapOf(pp.Value("stats")).Value("files") == int64(12) {
			found = true
		}
	}
	if !found {
		t.Fatalf("device path without stats: %s", pyjson.Dumps(dev))
	}
	// A newer report replaces the old one.
	if _, err := e.ReportLocationStats(ana, pyjson.NewObject("resource", "project/code", "bytes", int64(7)),
		"agent:x", "agent"); err != nil {
		t.Fatal(err)
	}
	if n := len(ReadLocationStats(e.Project)); n != 1 {
		t.Fatalf("stats not replaced: %d", n)
	}
}

func TestReportValidation(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	bad := []*obj{
		pyjson.NewObject("bytes", int64(-1)),
		pyjson.NewObject("bytes", "12"),
		pyjson.NewObject("bytes", 1.5),
		pyjson.NewObject("files", int64(3)),
		pyjson.NewObject("bytes", int64(5), "git_bytes", int64(6)),
		pyjson.NewObject("bytes", int64(5), "kind", "volume"),
		pyjson.NewObject("bytes", int64(5), "measured_at", "2026-11-21T12:00:00+00:00"),
		pyjson.NewObject("bytes", int64(5), "measured_at", "2026-01-01T00:00:00+00:00"),
		pyjson.NewObject("bytes", int64(5), "measured_at", "yesterday"),
	}
	for _, b := range bad {
		if _, msg := cleanReport(b, now); msg == "" {
			t.Errorf("accepted %s", pyjson.Dumps(b))
		}
	}
	s, msg := cleanReport(pyjson.NewObject("bytes", int64(5), "files", int64(1), "capped", true,
		"measured_at", "2026-11-20T11:00:00Z"), now)
	if msg != "" || s.Value("capped") != "time" || s.Value("measured_at") != "2026-11-20T11:00:00+00:00" {
		t.Fatalf("%s %s", msg, pyjson.Dumps(s))
	}
	if s, msg := cleanReport(pyjson.NewObject("kind", "missing"), now); msg != "" || s.Value("bytes") != int64(0) {
		t.Fatalf("missing: %s", msg)
	}
}

func TestNeedsMeasureAndAge(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	if !NeedsMeasure(nil, now, time.Hour) {
		t.Fatal("unmeasured must be measured")
	}
	fresh := pyjson.NewObject("measured_at", "2026-11-20T11:30:00+00:00")
	old := pyjson.NewObject("measured_at", "2026-11-19T11:00:00+00:00")
	if NeedsMeasure(fresh, now, time.Hour) || !NeedsMeasure(old, now, time.Hour) {
		t.Fatal("age threshold")
	}
	if age, ok := StatAge(old, now); !ok || age <= StatStaleAfter {
		t.Fatalf("stale: %v", age)
	}
	if NeedsMeasure(pyjson.NewObject("measured_at", "junk"), now, time.Hour) != true {
		t.Fatal("bad time must be re-measured")
	}
}

func TestHostLocationsFollowAliases(t *testing.T) {
	e, ana := statsEnv(t)
	if n := len(e.HostLocations("pc-1")); n != 2 {
		t.Fatalf("locations: %d", n)
	}
	if n := len(e.HostLocations("other")); n != 0 {
		t.Fatalf("foreign: %d", n)
	}
	newID := NewViewer(Viewer{User: "ana", Admin: true, CanWrite: true, HostID: "pc-new", HostSource: "header"})
	if _, err := e.AddAlias(newID, "pc-1", "human:ana"); err != nil {
		t.Fatal(err)
	}
	if n := len(e.HostLocations("pc-new")); n != 2 {
		t.Fatalf("aliased: %d", n)
	}
	// A size reported under the new id lands on the location's own host id.
	if _, err := e.ReportLocationStats(newID, pyjson.NewObject("resource", "project/db", "kind", "file", "bytes", int64(9)),
		"human:ana", "desktop"); err != nil {
		t.Fatal(err)
	}
	if s := e.StatLookup()("project/db", "pc-new", "/home/ana/db.sqlite3"); s == nil || s.Value("host_id") != "pc-1" {
		t.Fatalf("alias lookup: %v", s)
	}
	_ = ana
}
