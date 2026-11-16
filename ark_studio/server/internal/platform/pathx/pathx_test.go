package pathx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSamePath(t *testing.T) {
	defer func(g string) { goos = g }(goos)
	cases := []struct {
		os, a, b string
		want     bool
	}{
		{"windows", `C:\A\b`, `c:\a\B\`, true},
		{"windows", `C:\A\b\..\c`, `C:\A\c`, true},
		{"linux", "/a/B", "/a/b", false},
		{"linux", "/a/b/", "/a/./b", true},
	}
	for _, c := range cases {
		goos = c.os
		if c.os == "windows" && filepath.Separator == 0x2f {
			continue
		}
		if got := SamePath(c.a, c.b); got != c.want {
			t.Errorf("%s SamePath(%q, %q) = %v", c.os, c.a, c.b, got)
		}
	}
}

func TestExpandUser(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home")
	}
	cases := map[string]string{
		"~":      home,
		"~/x/y":  filepath.Join(home, "x", "y"),
		"~user":  "~user",
		"/abs/~": "/abs/~",
		"":       "",
	}
	for in, want := range cases {
		if got := ExpandUser(in); got != want {
			t.Errorf("ExpandUser(%q) = %q, want %q", in, got, want)
		}
	}
}
