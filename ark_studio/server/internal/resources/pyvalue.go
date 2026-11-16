package resources

import (
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"

	"arkstudio/go/internal/platform/pyerr"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
)

// Python semantics the graph code relies on: truthiness, float()/int(),
// round(), true division, time parsing (CPython 3.10 fromisoformat, the
// reference interpreter of the replay) and datetime.fromtimestamp.

// graphError carries a Python exception out of the pure graph code; BuildGraph
// recovers it and returns the error (GET: 500 "Internal error: ...").
type graphError struct{ err error }

func raise(err error) { panic(graphError{err}) }

func internal(msg string) { raise(&pyerr.InternalError{Message: msg}) }

func isNone(v any) bool {
	if v == nil {
		return true
	}
	if o, ok := v.(*pyjson.Object); ok && o == nil {
		return true
	}
	return false
}

// or is Python's `a or b`.
func or(a, b any) any {
	if pyjson.Truthy(a) {
		return a
	}
	return b
}

// mustObj is a value Python calls .get() on: a dict, else AttributeError.
func mustObj(v any) *pyjson.Object {
	if o, ok := v.(*pyjson.Object); ok && o != nil {
		return o
	}
	internal("'" + pyerr.TypeName(v) + "' object has no attribute 'get'")
	return nil
}

// orObj is `(v or {})` followed by .get().
func orObj(v any) *pyjson.Object {
	if !pyjson.Truthy(v) {
		return pyjson.NewObject()
	}
	return mustObj(v)
}

// iterate is `for x in (v or [])`.
func iterate(v any) []any {
	if !pyjson.Truthy(v) {
		return nil
	}
	switch x := v.(type) {
	case []any:
		return x
	case *pyjson.Object:
		out := make([]any, 0, x.Len())
		for _, k := range x.Keys() {
			out = append(out, k)
		}
		return out
	case string:
		var out []any
		for _, r := range x {
			out = append(out, string(r))
		}
		return out
	}
	internal("'" + pyerr.TypeName(v) + "' object is not iterable")
	return nil
}

// hashKey is a dict key for a JSON value with Python's equality (1 == 1.0 ==
// True); unhashable values raise TypeError.
func hashKey(v any) string {
	switch x := v.(type) {
	case nil:
		return "N"
	case string:
		return "s" + x
	case bool:
		if x {
			return "n1"
		}
		return "n0"
	case int64:
		return "n" + strconv.FormatInt(x, 10)
	case *big.Int:
		return "n" + x.String()
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e18 {
			return "n" + strconv.FormatInt(int64(x), 10)
		}
		return "f" + strconv.FormatFloat(x, 'g', -1, 64)
	case *pyjson.Object:
		internal("unhashable type: 'dict'")
	case []any:
		internal("unhashable type: 'list'")
	}
	return "?" + pyjson.Repr(v)
}

// pyEq is Python == for two JSON scalars.
func pyEq(a, b any) bool {
	_, ao := a.(*pyjson.Object)
	_, al := a.([]any)
	_, bo := b.(*pyjson.Object)
	_, bl := b.([]any)
	if ao || al || bo || bl {
		return pyjson.Repr(a) == pyjson.Repr(b) && ao == bo && al == bl
	}
	return hashKey(a) == hashKey(b)
}

// pyFloat is float(v).
func pyFloat(v any) float64 {
	switch x := v.(type) {
	case bool:
		if x {
			return 1
		}
		return 0
	case int64:
		return float64(x)
	case *big.Int:
		f, _ := new(big.Float).SetInt(x).Float64()
		return f
	case float64:
		return x
	case string:
		s := strings.ReplaceAll(pytext.Strip(x), "_", "")
		if f, err := strconv.ParseFloat(s, 64); err == nil || strings.Contains(err.Error(), "range") {
			return f
		}
		raise(&pyerr.ValueError{Message: "could not convert string to float: " + pyjson.Repr(x)})
	case nil:
		internal("float() argument must be a string or a real number, not 'NoneType'")
	}
	internal("float() argument must be a string or a real number, not '" + pyerr.TypeName(v) + "'")
	return 0
}

// pyInt is int(v) (small values).
func pyInt(v any) int64 {
	switch x := v.(type) {
	case bool:
		if x {
			return 1
		}
		return 0
	case int64:
		return x
	case *big.Int:
		return x.Int64()
	case float64:
		return int64(x)
	case string:
		n, err := pytext.Int(x)
		if err != nil {
			raise(&pyerr.ValueError{Message: "invalid literal for int() with base 10: " + pyjson.Repr(x)})
		}
		return n
	}
	internal("int() argument must be a string, a bytes-like object or a real number, not '" + pyerr.TypeName(v) + "'")
	return 0
}

func bigInt(v any) (*big.Int, bool) {
	switch x := v.(type) {
	case bool:
		if x {
			return big.NewInt(1), true
		}
		return big.NewInt(0), true
	case int64:
		return big.NewInt(x), true
	case *big.Int:
		return x, true
	}
	return nil, false
}

// trueDiv is a / b for numbers (int / int is correctly rounded like CPython).
func trueDiv(a, b any) float64 {
	ai, aok := bigInt(a)
	bi, bok := bigInt(b)
	if aok && bok {
		if bi.Sign() == 0 {
			raise(&pyerr.InternalError{Message: "division by zero"})
		}
		f, _ := new(big.Rat).SetFrac(ai, bi).Float64()
		return f
	}
	d := pyFloat(b)
	if d == 0 {
		raise(&pyerr.InternalError{Message: "float division by zero"})
	}
	return pyFloat(a) / d
}

// mul100 is 100 * v.
func mul100(v any) any {
	if i, ok := bigInt(v); ok {
		r := new(big.Int).Mul(big.NewInt(100), i)
		if r.IsInt64() {
			return r.Int64()
		}
		return r
	}
	return 100 * pyFloat(v)
}

// roundN is round(x, n): ints stay ints, floats round correctly (half-even
// on the exact binary value, like CPython's dtoa-based round).
func roundN(v any, n int) any {
	switch x := v.(type) {
	case bool:
		return pyInt(x)
	case int64, *big.Int:
		return x
	case float64:
		return roundFloat(x, n)
	}
	internal("type " + pyerr.TypeName(v) + " doesn't define __round__ method")
	return nil
}

func roundFloat(x float64, n int) float64 {
	if math.IsInf(x, 0) || math.IsNaN(x) || x == 0 {
		return x
	}
	f, err := strconv.ParseFloat(strconv.FormatFloat(x, 'f', n, 64), 64)
	if err != nil {
		return x
	}
	if f == 0 && math.Signbit(x) {
		return math.Copysign(0, -1)
	}
	return f
}

// round0 is round(x) of a float: an int, half to even.  Like CPython, an
// infinity raises OverflowError and NaN ValueError (500 in do_GET).
func round0(x float64) any {
	if math.IsInf(x, 0) {
		internal("cannot convert float infinity to integer")
	}
	if math.IsNaN(x) {
		raise(&pyerr.ValueError{Message: "cannot convert float NaN to integer"})
	}
	r := math.RoundToEven(x)
	if math.Abs(r) < 9e18 {
		return int64(r)
	}
	b, _ := new(big.Float).SetFloat64(r).Int(nil)
	return b
}

func intText(v any) string {
	switch x := v.(type) {
	case int64:
		return strconv.FormatInt(x, 10)
	case *big.Int:
		return x.String()
	}
	return pyjson.Str(v)
}

// ------------------------------------------------------------------ times

// parseTime is resources_graph._parse_time (ok=false is None).
func parseTime(v any) (float64, bool) {
	switch x := v.(type) {
	case nil:
		return 0, false
	case string:
		if x == "" {
			return 0, false
		}
	case bool, int64, *big.Int, float64:
		return pyFloat(x), true
	}
	return fromISOFormat(strings.ReplaceAll(pyjson.Str(v), "Z", "+00:00"))
}

// fromISOFormat is datetime.fromisoformat(s).timestamp() of CPython 3.10
// (the C parser, byte for byte; naive values are read as UTC).
func fromISOFormat(s string) (float64, bool) {
	n := len(s)
	at := func(i int) byte {
		if i >= 0 && i < n {
			return s[i]
		}
		return 0
	}
	digits := func(p, count int) (int, int, bool) {
		v := 0
		for k := 0; k < count; k++ {
			c := at(p + k)
			if c < '0' || c > '9' {
				return 0, 0, false
			}
			v = v*10 + int(c-'0')
		}
		return v, p + count, true
	}
	// parse_hh_mm_ss_ff
	hms := func(p, end int) (vals [3]int, us int, rv int) {
		for i := 0; i < 3; i++ {
			v, np, ok := digits(p, 2)
			if !ok {
				return vals, 0, -3
			}
			vals[i] = v
			p = np
			c := at(p)
			p++
			if p >= end {
				if c != 0 {
					return vals, 0, 1
				}
				return vals, 0, 0
			} else if c == ':' {
				continue
			} else if c == '.' {
				break
			} else {
				return vals, 0, -4
			}
		}
		remains := end - p
		if remains != 6 && remains != 3 {
			return vals, 0, -3
		}
		v, np, ok := digits(p, remains)
		if !ok {
			return vals, 0, -3
		}
		if remains == 3 {
			v *= 1000
		}
		if at(np) != 0 {
			return vals, v, 1
		}
		return vals, v, 0
	}
	year, p, ok := digits(0, 4)
	if !ok || at(p) != '-' {
		return 0, false
	}
	month, p, ok := digits(p+1, 2)
	if !ok || at(p) != '-' {
		return 0, false
	}
	day, _, ok := digits(p+1, 2)
	if !ok {
		return 0, false
	}
	var clock [3]int
	us, tzSecs, tzUS := 0, 0, 0
	if n > 10 {
		c := at(10)
		p = 11
		if c&0x80 != 0 {
			switch c & 0xf0 {
			case 0xe0:
				p = 13
			case 0xf0:
				p = 14
			default:
				p = 12
			}
		}
		end := n
		tz := p
		for {
			if c := at(tz); c == '+' || c == '-' {
				break
			}
			tz++
			if tz >= end {
				break
			}
		}
		var rv int
		clock, us, rv = hms(p, tz)
		if rv < 0 || (tz == end && rv == 1) { // trailing text without a zone
			return 0, false
		}
		if tz != end {
			if l := end - tz; l != 6 && l != 9 && l != 16 { // +HH:MM[:SS[.ffffff]]
				return 0, false
			}
			sign := 1
			if at(tz) == '-' {
				sign = -1
			}
			tzv, tzus, rv := hms(tz+1, end)
			if rv != 0 {
				return 0, false
			}
			tzSecs = sign * (tzv[0]*3600 + tzv[1]*60 + tzv[2])
			tzUS = sign * tzus
		}
	}
	if year < 1 || month < 1 || month > 12 || day < 1 || day > daysIn(year, month) ||
		clock[0] > 23 || clock[1] > 59 || clock[2] > 59 {
		return 0, false
	}
	offsetUS := int64(tzSecs)*1_000_000 + int64(tzUS)
	if offsetUS <= -86_400_000_000 || offsetUS >= 86_400_000_000 {
		return 0, false
	}
	days := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC).Unix() / 86400
	total := (days*86400+int64(clock[0]*3600+clock[1]*60+clock[2]))*1_000_000 + int64(us) - offsetUS
	f, _ := new(big.Rat).SetFrac64(total, 1_000_000).Float64()
	return f, true
}

func daysIn(year, month int) int {
	return time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// iso is resources_graph._iso: datetime.fromtimestamp(ts, utc) (microseconds
// rounded half to even) formatted "%Y-%m-%dT%H:%M:%SZ".
func iso(ts float64) string {
	if math.IsNaN(ts) || math.IsInf(ts, 0) {
		raise(&pyerr.ValueError{Message: "cannot convert float NaN to integer"})
	}
	intpart, frac := math.Modf(ts)
	frac = math.RoundToEven(frac * 1e6)
	if frac >= 1e6 {
		intpart++
	} else if frac < 0 {
		intpart--
	}
	if intpart < -62135596800 || intpart > 253402300799 {
		raise(&pyerr.ValueError{Message: "year is out of range"})
	}
	return time.Unix(int64(intpart), 0).UTC().Format("2006-01-02T15:04:05Z")
}

func isoOpt(ts float64, ok bool) any {
	if !ok {
		return nil
	}
	return iso(ts)
}

// ago is resources_graph._ago.
func ago(seconds float64) string {
	switch {
	case seconds < 90:
		return "just now"
	case seconds < 5400:
		return intText(round0(seconds/60)) + " min ago"
	case seconds < 172800:
		return intText(round0(seconds/3600)) + " h ago"
	}
	return intText(round0(seconds/86400)) + " d ago"
}

// max0 is Python's max(0.0, x): the first argument wins ties and NaN
// (math.Max(0, NaN) is NaN).
func max0(x float64) float64 {
	if x > 0 {
		return x
	}
	return 0
}
