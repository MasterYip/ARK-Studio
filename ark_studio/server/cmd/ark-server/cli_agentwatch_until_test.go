package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --until-update waits for the first news, prints it and exits: run in the
// background, it wakes the agent (T-0053).
func TestAgentWatchUntilUpdate(t *testing.T) {
	srv, app := watchServer(t)
	state := filepath.Join(t.TempDir(), ".ark-watch-cursor")
	t.Setenv("ARK_TOKEN", app.AgentToken())
	unit := watchHTTP(t, srv, app, "POST", "/api/agent/v1/units", `{"type":"task","title":"Sweep","status":"review"}`, true)
	uid := unit["unit"].(map[string]any)["uid"].(string)
	// The first check only stores the cursor.
	var out0, err0 bytes.Buffer
	if code := agentWatch([]string{"--url", srv.URL, "--state", state, "--once"}, &out0, &err0); code != 0 {
		t.Fatal(err0.String())
	}
	done := make(chan string, 1)
	go func() {
		var out, errw bytes.Buffer
		agentWatch([]string{"--url", srv.URL, "--state", state, "--until-update", "--wait", "5"}, &out, &errw)
		done <- out.String()
	}()
	time.Sleep(500 * time.Millisecond)
	watchHTTP(t, srv, app, "POST", "/api/units/"+uid+"/comments", `{"body":"add the seed-2 baseline"}`, false)
	select {
	case out := <-done:
		if !strings.HasPrefix(out, "ARK UPDATE: 1 new update(s)") || !strings.Contains(out, "seed-2 baseline") {
			t.Fatalf("%q", out)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the watcher did not wake")
	}
	// Nothing new: it gives up after --max and says so.
	var out, errw bytes.Buffer
	agentWatch([]string{"--url", srv.URL, "--state", state, "--until-update", "--wait", "1", "--max", "1"}, &out, &errw)
	if !strings.Contains(out.String(), "no updates in 1s") {
		t.Fatalf("%q %q", out.String(), errw.String())
	}
}
