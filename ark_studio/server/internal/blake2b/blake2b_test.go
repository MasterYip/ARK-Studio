package blake2b

import (
	"encoding/hex"
	"testing"
)

func TestAgainstHashlib(t *testing.T) {
	want := map[int]string{
		0:    "cae66941d9efbd404e4d88758ea67670",
		1:    "7025e075d5e2f6cde3cc051a31f07660",
		127:  "40efb3ed1b6eb973c5f774422adb0230",
		128:  "06e5bcee475a6bf6d9b8a07c6f4ae80a",
		129:  "2ec7e7bf8a8265ffd68aadd70ecd2b7e",
		256:  "dbb08d79105462394550aa4d4e10133f",
		1000: "8095a6f38992fefe1c12ac8ec5e70791",
	}
	for n, w := range want {
		d := make([]byte, n)
		for i := range d {
			d[i] = byte(i * 7 % 256)
		}
		if got := hex.EncodeToString(Sum(d, 16)); got != w {
			t.Fatalf("%d: %s want %s", n, got, w)
		}
	}
}
