package api

import (
	"strconv"
	"strings"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/compat/pyurl"
)

// AcceptsGzip is static_assets.accepts_gzip.
func AcceptsGzip(header string) bool {
	qualities := map[string]float64{}
	for _, item := range strings.Split(strings.ToLower(header), ",") {
		parts := strings.Split(strings.TrimSpace(item), ";")
		encoding := strings.TrimSpace(parts[0])
		quality := 1.0
		for _, p := range parts[1:] {
			key, value, _ := strings.Cut(strings.TrimSpace(p), "=")
			if key == "q" {
				f, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
				if err != nil {
					f = 0
				}
				quality = f
			}
		}
		qualities[encoding] = quality
	}
	q, ok := qualities["gzip"]
	if !ok {
		q = qualities["*"]
	}
	return q > 0 && q <= 1
}

// BoolArg is server._bool: str(value).lower() in {"1", "true", "yes", "on"}.
func BoolArg(v any) bool {
	switch LowerStr(v) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// LowerStr is str(value).lower() (None -> "none").
func LowerStr(v any) string {
	if v == nil {
		return "none"
	}
	return strings.ToLower(pyjson.Str(v))
}

// Stream is a streamed reply body after its headers were sent
// (Ctx.StartStream): each write should be followed by Flush.
type Stream interface {
	Write(p []byte) (int, error)
	// Flush pushes buffered bytes to the client.
	Flush() error
	// SetWriteDeadline bounds the next writes (a stalled client).
	SetWriteDeadline(t time.Time) error
}

// JSONType is the Content-Type of JSON replies.
const JSONType = "application/json; charset=utf-8"

// QInt is _int((query.get(name) or [def])[0], default, low, high).
func QInt(q pyurl.Values, name string, def, low, high int64) int64 {
	n, err := pytext.Int(q.First(name, ""))
	if err != nil {
		return def
	}
	return max(low, min(high, n))
}
