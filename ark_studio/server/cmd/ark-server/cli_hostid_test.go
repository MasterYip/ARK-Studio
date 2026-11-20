package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func TestAgentHostID(t *testing.T) {
	var out bytes.Buffer
	if code := agentHostID(nil, &out); code != 0 {
		t.Skip("no device id here:", out.String())
	}
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(strings.TrimSpace(out.String())) {
		t.Fatalf("%q", out.String())
	}
	out.Reset()
	agentHostID([]string{"--json"}, &out)
	if !strings.Contains(out.String(), `"method":"machine-id"`) && !strings.Contains(out.String(), `"method":"mac"`) {
		t.Fatalf("%q", out.String())
	}
}
