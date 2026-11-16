// Package pyjson reproduces Python's json module byte for byte.
//
// ARK's Python server answers with json.dumps(obj, ensure_ascii=False): ", "
// and ": " separators, insertion-ordered keys, raw UTF-8, Python float repr
// (1.0, 1e+16, 1e-05) and arbitrary-precision integers.  encoding/json does
// none of that (sorted map keys, < escapes, 1e+06 floats), so responses are
// built from the ordered values in this package instead.
//
// Value model (what Decode returns and Encode accepts):
//
//	nil, bool, int64, *big.Int, float64, string, []any, *Object
//
// Encode also accepts int, []string, []int64, []*Object, map[string]int64
// (sorted keys, used only where Python sorts) and Raw.
package pyjson

import "math/big"

// Object is an insertion-ordered JSON object with Python dict semantics:
// assigning an existing key keeps its position, deleting and re-adding moves
// it to the end.  Keys and values live in parallel slices; an index map is
// built only for large objects, so the common 5-30 key object costs two
// allocations instead of a hash map.
type Object struct {
	keys  []string
	vals  []any
	index map[string]int // nil until len(keys) > indexAt
}

const indexAt = 32

// Raw is pre-encoded JSON, inserted verbatim.
type Raw []byte

// NewObject builds an object from alternating key/value pairs.
func NewObject(kv ...any) *Object {
	n := len(kv) / 2
	o := &Object{keys: make([]string, 0, n), vals: make([]any, 0, n)}
	for i := 0; i+1 < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

func (o *Object) find(key string) int {
	if o.index != nil {
		if i, ok := o.index[key]; ok {
			return i
		}
		return -1
	}
	for i, k := range o.keys {
		if k == key {
			return i
		}
	}
	return -1
}

// Set assigns key (Python “d[key] = value“).
func (o *Object) Set(key string, value any) *Object {
	if i := o.find(key); i >= 0 {
		o.vals[i] = value
		return o
	}
	o.keys = append(o.keys, key)
	o.vals = append(o.vals, value)
	if o.index != nil {
		o.index[key] = len(o.keys) - 1
	} else if len(o.keys) > indexAt {
		o.index = make(map[string]int, len(o.keys)*2)
		for i, k := range o.keys {
			o.index[k] = i
		}
	}
	return o
}

// Get returns the value and whether the key exists.
func (o *Object) Get(key string) (any, bool) {
	if o == nil {
		return nil, false
	}
	if i := o.find(key); i >= 0 {
		return o.vals[i], true
	}
	return nil, false
}

// Value returns the value or nil (Python “d.get(key)“).
func (o *Object) Value(key string) any {
	v, _ := o.Get(key)
	return v
}

// Has reports whether key exists.
func (o *Object) Has(key string) bool {
	_, ok := o.Get(key)
	return ok
}

// Delete removes key (Python “d.pop(key, None)“) and returns the old value.
func (o *Object) Delete(key string) any {
	if o == nil {
		return nil
	}
	i := o.find(key)
	if i < 0 {
		return nil
	}
	v := o.vals[i]
	o.keys = append(o.keys[:i:i], o.keys[i+1:]...)
	o.vals = append(o.vals[:i:i], o.vals[i+1:]...)
	if o.index != nil {
		o.index = make(map[string]int, len(o.keys)*2)
		for j, k := range o.keys {
			o.index[k] = j
		}
	}
	return v
}

// Keys returns the keys in insertion order (do not modify).
func (o *Object) Keys() []string {
	if o == nil {
		return nil
	}
	return o.keys
}

// Len is the number of keys.
func (o *Object) Len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

// Update copies every key of other into o (Python “d.update(other)“).
func (o *Object) Update(other *Object) *Object {
	for i, k := range other.Keys() {
		o.Set(k, other.vals[i])
	}
	return o
}

// Copy is a shallow copy (Python “dict(d)“).
func (o *Object) Copy() *Object {
	c := &Object{keys: append([]string(nil), o.Keys()...)}
	if o != nil {
		c.vals = append([]any(nil), o.vals...)
	}
	if len(c.keys) > indexAt {
		c.index = make(map[string]int, len(c.keys)*2)
		for i, k := range c.keys {
			c.index[k] = i
		}
	}
	return c
}

// BigInt wraps an integer outside int64 (from decoded JSON).
func BigInt(text string) *big.Int {
	n, _ := new(big.Int).SetString(text, 10)
	return n
}
