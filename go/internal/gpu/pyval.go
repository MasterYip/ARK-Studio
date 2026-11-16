package gpu

// Python value semantics the GPU port needs: float(str), round(x, 1),
// int(float), min/max with NaN, tuple ordering and list.sort.

import (
	"math"
	"math/big"
	"strconv"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pytext"
)

// ValueError carries the text of a Python ValueError that escapes to the
// HTTP layer (400 on POST, 500 on GET; see server.handleError).
type ValueError struct{ Msg string }

func (e *ValueError) Error() string { return e.Msg }

// InternalError is any other exception (500 "Internal error: ...").
type InternalError struct{ Msg string }

func (e *InternalError) Error() string { return e.Msg }

// PyFloat is float(text) for a str (pytext.Float) with CPython's
// ValueError text.
func PyFloat(text string) (float64, error) {
	if v, ok := pytext.Float(text); ok {
		return v, nil
	}
	if pytext.Strip(text) == "" { // CPython reports the stripped (empty) text
		return 0, &ValueError{"could not convert string to float: ''"}
	}
	return 0, &ValueError{"could not convert string to float: " + pyjson.Repr(text)}
}

// round1 is round(x, 1).
func round1(x float64) float64 {
	if math.IsInf(x, 0) || math.IsNaN(x) {
		return x
	}
	v, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'f', 1, 64), 64)
	return v
}

// pyMax / pyMin are max(a, b) / min(a, b): the first argument wins ties
// and comparisons with NaN.
func pyMax(a, b float64) float64 {
	if b > a {
		return b
	}
	return a
}

func pyMin(a, b float64) float64 {
	if b < a {
		return b
	}
	return a
}

// floatToInt is int(x) for a float (ValueError for NaN, OverflowError for
// infinities); big values become *big.Int like Python's unbounded int.
func floatToInt(x float64) (any, error) {
	if math.IsNaN(x) {
		return nil, &ValueError{"cannot convert float NaN to integer"}
	}
	if math.IsInf(x, 0) {
		return nil, &InternalError{"cannot convert float infinity to integer"}
	}
	t := math.Trunc(x)
	if t >= -9.2e18 && t <= 9.2e18 {
		return int64(t), nil
	}
	bf := new(big.Float).SetFloat64(t)
	bi, _ := bf.Int(nil)
	return bi, nil
}

// tuple is a Python tuple of float64 / int64 / string items.
type tuple []any

func itemEq(a, b any) bool {
	switch x := a.(type) {
	case float64:
		return x == num(b)
	case int64:
		if y, ok := b.(int64); ok {
			return x == y
		}
		return float64(x) == num(b)
	case string:
		y, ok := b.(string)
		return ok && x == y
	}
	return false
}

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	}
	return math.NaN()
}

func itemLt(a, b any) bool {
	if x, ok := a.(string); ok {
		return x < b.(string)
	}
	if x, ok := a.(int64); ok {
		if y, ok := b.(int64); ok {
			return x < y
		}
	}
	return num(a) < num(b)
}

// tupleLt is tuple.__lt__.
func tupleLt(a, b tuple) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if !itemEq(a[i], b[i]) {
			return itemLt(a[i], b[i])
		}
	}
	return len(a) < len(b)
}

// pySort is list.sort(key=...) with keys compared by tupleLt: CPython's
// timsort, which for fewer than 64 items is count_run plus binary insertion
// (identical results even when NaN makes the order inconsistent).  Longer
// lists use a stable merge sort (identical for consistent orders).
func pySort[T any](items []T, key func(T) tuple) {
	n := len(items)
	if n < 2 {
		return
	}
	keys := make([]tuple, n)
	for i, item := range items {
		keys[i] = key(item)
	}
	swap := func(i, j int) {
		items[i], items[j] = items[j], items[i]
		keys[i], keys[j] = keys[j], keys[i]
	}
	if n >= 64 {
		idx := make([]int, n)
		for i := range idx {
			idx[i] = i
		}
		mergeSort(idx, func(i, j int) bool { return tupleLt(keys[i], keys[j]) })
		out := make([]T, n)
		for i, k := range idx {
			out[i] = items[k]
		}
		copy(items, out)
		return
	}
	// count_run
	run := 1
	if tupleLt(keys[1], keys[0]) {
		run = 2
		for run < n && tupleLt(keys[run], keys[run-1]) {
			run++
		}
		for i, j := 0, run-1; i < j; i, j = i+1, j-1 {
			swap(i, j)
		}
	} else {
		run = 2
		for run < n && !tupleLt(keys[run], keys[run-1]) {
			run++
		}
	}
	// binarysort from index run
	for start := run; start < n; start++ {
		pivotItem, pivotKey := items[start], keys[start]
		l, r := 0, start
		for l < r {
			p := l + (r-l)>>1
			if tupleLt(pivotKey, keys[p]) {
				r = p
			} else {
				l = p + 1
			}
		}
		copy(items[l+1:start+1], items[l:start])
		copy(keys[l+1:start+1], keys[l:start])
		items[l], keys[l] = pivotItem, pivotKey
	}
}

func mergeSort(idx []int, less func(i, j int) bool) {
	if len(idx) < 2 {
		return
	}
	mid := len(idx) / 2
	left := append([]int(nil), idx[:mid]...)
	right := append([]int(nil), idx[mid:]...)
	mergeSort(left, less)
	mergeSort(right, less)
	i, j, k := 0, 0, 0
	for i < len(left) && j < len(right) {
		if less(right[j], left[i]) {
			idx[k] = right[j]
			j++
		} else {
			idx[k] = left[i]
			i++
		}
		k++
	}
	k += copy(idx[k:], left[i:])
	copy(idx[k:], right[j:])
}

func bigInt(n int64) *big.Int { return big.NewInt(n) }
