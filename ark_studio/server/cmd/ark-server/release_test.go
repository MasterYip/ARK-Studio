package main

import "testing"

func TestReleaseVersion(t *testing.T) {
	saved := version
	defer func() { version = saved }()
	for in, want := range map[string]string{
		"2.1.2": "2.1.2", "2.2.0-rc1": "2.2.0-rc1",
		"2.0-go": release, "0e5c1b4a5d0e0b7d6f3c2a1e9b8d7c6f5e4d3c2b": release,
	} {
		version = in
		if got := releaseVersion(); got != want {
			t.Errorf("releaseVersion() with %q = %q, want %q", in, got, want)
		}
	}
}
