package textfile

import (
	"runtime"
	"testing"
)

func TestUniversal(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""}, {"a\nb", "a\nb"}, {"a\r\nb", "a\nb"}, {"a\rb", "a\nb"},
		{"a\r\r\nb\r", "a\n\nb\n"}, {"\n\r", "\n\n"},
	}
	for _, c := range cases {
		if got := Universal(c.in); got != c.want {
			t.Errorf("Universal(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestEncode(t *testing.T) {
	want := "a\nb\n"
	if runtime.GOOS == "windows" {
		want = "a\r\nb\r\n"
	}
	if got := string(Encode("a\nb\n")); got != want {
		t.Errorf("Encode = %q, want %q", got, want)
	}
	if got := Universal(string(Encode("x\ny"))); got != "x\ny" {
		t.Errorf("round trip = %q", got)
	}
}
