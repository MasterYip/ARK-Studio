// Package skillkit ports arkstudio/skills/protocol.py (template
// substitution and the task vocabulary), agent_team.py, rrsi.py, design.py
// and the render/validate dispatch of catalog.py, with Python's value
// semantics for decoded JSON: str() of non-strings, truthiness, int() and
// float() coercion, iteration of strings and mappings, and the exception
// classes the callers distinguish.
package skillkit

import (
	"math"
	"math/big"
	"strconv"
	"strings"

	"arkstudio/go/internal/platform/pyerr"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/gpu"
	"arkstudio/go/internal/skillbrief"
)

type obj = pyjson.Object

// Error is a ValueError subclass (ProtocolError, RrsiError, DesignError,
// SetupError or a plain ValueError): validate_payload turns it into
// {"ok": false, "errors": [msg]}, the CLI prints "error: msg" (exit 2) and
// the server answers 400.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

func errf(msg string) error { return &Error{msg} }

// SkillError is catalog.SkillError (a ValueError; the server answers 404).
type SkillError struct{ Msg string }

func (e *SkillError) Error() string { return e.Msg }

// Uncaught is any other Python exception (CLI traceback, server 500).
type Uncaught = skillbrief.Uncaught

func uncaught(class, msg string) error { return &Uncaught{Class: class, Message: msg} }

func str(v any) string { return pyjson.Str(v) }

func or(v, def any) any {
	if pyjson.Truthy(v) {
		return v
	}
	return def
}

// get is dict.get(key, default).
func get(o *obj, key string, def any) any {
	if v, ok := o.Get(key); ok {
		return v
	}
	return def
}

func isMapping(v any) bool { _, ok := v.(*obj); return ok }

func copyObj(o *obj) *obj {
	out := pyjson.NewObject()
	if o == nil {
		return out
	}
	for _, k := range o.Keys() {
		out.Set(k, o.Value(k))
	}
	return out
}

// iterate is iter(value) for a decoded JSON value.
func iterate(v any) ([]any, error) {
	switch x := v.(type) {
	case []any:
		return x, nil
	case string:
		out := make([]any, 0, len(x))
		for _, r := range x {
			out = append(out, string(r))
		}
		return out, nil
	case *obj:
		out := make([]any, 0, x.Len())
		for _, k := range x.Keys() {
			out = append(out, k)
		}
		return out, nil
	}
	return nil, uncaught("TypeError", "'"+pyerr.TypeName(v)+"' object is not iterable")
}

// asList is _as_list: None -> [], list -> list, anything else -> [value].
func asList(v any) []any {
	switch x := v.(type) {
	case nil:
		return []any{}
	case []any:
		return x
	}
	return []any{v}
}

// toDict is dict(value) for a decoded JSON value.
func toDict(v any) (*obj, error) {
	switch x := v.(type) {
	case *obj:
		return copyObj(x), nil
	case []any, string:
		items, _ := iterate(x)
		out := pyjson.NewObject()
		for i, item := range items {
			var pair []any
			switch p := item.(type) {
			case []any:
				pair = p
			case string:
				pair, _ = iterate(p)
			case *obj:
				pair, _ = iterate(p)
			default:
				return nil, uncaught("TypeError", "cannot convert dictionary update sequence element #"+strconv.Itoa(i)+" to a sequence")
			}
			if len(pair) != 2 {
				return nil, errf("dictionary update sequence element #" + strconv.Itoa(i) + " has length " + strconv.Itoa(len(pair)) + "; 2 is required")
			}
			key, ok := pair[0].(string)
			if !ok {
				return nil, uncaught("TypeError", "unhashable key in dictionary update sequence")
			}
			out.Set(key, pair[1])
		}
		return out, nil
	}
	return nil, uncaught("TypeError", "'"+pyerr.TypeName(v)+"' object is not iterable")
}

// text is _text(value, limit): str(value or "").strip()[:limit].
func text(v any, limit int) string {
	if !pyjson.Truthy(v) {
		return ""
	}
	return pytext.Prefix(pytext.Strip(str(v)), limit)
}

// pyFloat is float(value): ok is false for TypeError/ValueError.
func pyFloat(v any) (float64, bool, error) {
	switch x := v.(type) {
	case bool:
		if x {
			return 1, true, nil
		}
		return 0, true, nil
	case int64:
		return float64(x), true, nil
	case *big.Int:
		f, _ := new(big.Float).SetInt(x).Float64()
		if math.IsInf(f, 0) {
			return 0, false, uncaught("OverflowError", "int too large to convert to float")
		}
		return f, true, nil
	case float64:
		return x, true, nil
	case string:
		f, err := gpu.PyFloat(x)
		if err != nil {
			return 0, false, nil
		}
		return f, true, nil
	}
	return 0, false, nil
}

// pyInt is int(value).
func pyInt(v any) (*big.Int, error) {
	switch x := v.(type) {
	case bool:
		if x {
			return big.NewInt(1), nil
		}
		return big.NewInt(0), nil
	case int64:
		return big.NewInt(x), nil
	case *big.Int:
		return x, nil
	case float64:
		if math.IsNaN(x) {
			return nil, errf("cannot convert float NaN to integer")
		}
		if math.IsInf(x, 0) {
			return nil, uncaught("OverflowError", "cannot convert float infinity to integer")
		}
		b, _ := big.NewFloat(math.Trunc(x)).Int(nil)
		return b, nil
	case string:
		n, err := pytext.Int(x)
		if err != nil {
			if _, ok := err.(*pytext.IntError); ok {
				return nil, errf(err.Error())
			}
			b, ok := bigIntText(x)
			if !ok {
				return nil, errf((&pytext.IntError{Text: x}).Error())
			}
			return b, nil
		}
		return big.NewInt(n), nil
	}
	return nil, uncaught("TypeError", "int() argument must be a string, a bytes-like object or a real number, not '"+pyerr.TypeName(v)+"'")
}

// bigIntText parses an ASCII-normalisable integer too large for int64.
func bigIntText(s string) (*big.Int, bool) {
	t := strings.ReplaceAll(pytext.Strip(s), "_", "")
	var b strings.Builder
	for i, r := range t {
		if i == 0 && (r == '-' || r == '+') {
			b.WriteRune(r)
			continue
		}
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
			continue
		}
		return nil, false
	}
	n, ok := new(big.Int).SetString(b.String(), 10)
	return n, ok
}

// num is a Python int or float operand (bool counts as int).
type num struct {
	isFloat bool
	i       *big.Int
	f       float64
}

// asNum converts a numeric JSON value; ok is false for str, None, list, dict.
func asNum(v any) (num, bool) {
	switch x := v.(type) {
	case bool:
		if x {
			return num{i: big.NewInt(1)}, true
		}
		return num{i: big.NewInt(0)}, true
	case int64:
		return num{i: big.NewInt(x)}, true
	case *big.Int:
		return num{i: x}, true
	case float64:
		return num{isFloat: true, f: x}, true
	}
	return num{}, false
}

func (n num) float() float64 {
	if n.isFloat {
		return n.f
	}
	f, _ := new(big.Float).SetInt(n.i).Float64()
	return f
}

// cmpInt compares n with an int constant (exact for big ints and floats).
func (n num) cmpInt(c int64) int {
	if !n.isFloat {
		return n.i.Cmp(big.NewInt(c))
	}
	switch {
	case n.f < float64(c):
		return -1
	case n.f > float64(c):
		return 1
	case n.f == float64(c):
		return 0
	}
	return 2 // NaN: every comparison is false
}

// lessThanZero is `value < 0` with Python's TypeError for non-numbers.
func lessThanZero(v any, op string) (bool, error) {
	n, ok := asNum(v)
	if !ok {
		return false, uncaught("TypeError", "'"+op+"' not supported between instances of '"+pyerr.TypeName(v)+"' and 'int'")
	}
	return n.cmpInt(0) == -1, nil
}

// fmtFixed is format(x, ".Nf") / "+.Nf" for a float.
func fmtFixed(x float64, prec int, sign bool) string {
	switch {
	case math.IsInf(x, 1):
		if sign {
			return "+inf"
		}
		return "inf"
	case math.IsInf(x, -1):
		return "-inf"
	case math.IsNaN(x):
		if sign {
			return "+nan"
		}
		return "nan"
	}
	s := strconv.FormatFloat(x, 'f', prec, 64)
	if sign && !strings.HasPrefix(s, "-") {
		s = "+" + s
	}
	return s
}

// joinStrings is ", ".join(items) for items that must all be str.
func joinStrings(sep string, items []any) (string, error) {
	parts := make([]string, len(items))
	for i, it := range items {
		s, ok := it.(string)
		if !ok {
			return "", uncaught("TypeError", "sequence item "+strconv.Itoa(i)+": expected str instance, "+pyerr.TypeName(it)+" found")
		}
		parts[i] = s
	}
	return strings.Join(parts, sep), nil
}

func strList(items []string) []any {
	out := make([]any, len(items))
	for i, s := range items {
		out[i] = s
	}
	return out
}

func inList(s string, l []string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
