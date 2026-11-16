package sessions

import (
	"strings"
	"time"

	"arkstudio/go/internal/platform/clock"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
)

// Error is SessionError: a clean 4xx for the API.
type Error struct {
	Message string
	Status  int
	Code    string
}

func (e *Error) Error() string { return e.Message }

func serr(message string, status int, code string) *Error {
	if status == 0 {
		status = 400
	}
	return &Error{Message: message, Status: status, Code: code}
}

// NowISO is sessions.utc_now().
func NowISO() string { return clock.Format(clock.Stamp()) }

// strOr is str(value or "").
func strOr(v any) string {
	if !pyjson.Truthy(v) {
		return ""
	}
	return pyjson.Str(v)
}

// intOr is int(value or def) for stored metadata.
func intOr(v any, def int64) int64 {
	if !pyjson.Truthy(v) {
		return def
	}
	if n, ok := pyIntValue(v); ok {
		return n
	}
	return def
}

func objOr(v any) *pyjson.Object {
	if o, ok := v.(*pyjson.Object); ok && o.Len() > 0 {
		return o
	}
	return pyjson.NewObject()
}

// deepCopy copies nested objects and lists (dict(meta) is shallow in
// Python, but Python never mutates a payload after building it).
func deepCopy(v any) any {
	switch x := v.(type) {
	case *pyjson.Object:
		out := pyjson.NewObject()
		for _, k := range x.Keys() {
			out.Set(k, deepCopy(x.Value(k)))
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = deepCopy(item)
		}
		return out
	}
	return v
}

func strList(items []string) []any {
	out := make([]any, len(items))
	for i, s := range items {
		out[i] = s
	}
	return out
}

func stringsOf(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			out = append(out, pyjson.Str(item))
		}
		return out
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// limit is str(value)[:n] in code points.
func limit(s string, n int) string { return pytext.Prefix(s, n) }

// modeToken is _mode_token.
func modeToken(v any) string {
	return strings.ReplaceAll(strings.ReplaceAll(pytext.Lower(pytext.Strip(strOr(v))), "-", "_"), " ", "_")
}

// parseISO is datetime.fromisoformat for the stamps ARK writes.
func parseISO(v any) (time.Time, bool) {
	s, ok := v.(string)
	if !ok {
		return time.Time{}, false
	}
	for _, layout := range []string{"2006-01-02T15:04:05-07:00", "2006-01-02T15:04:05.999999-07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
