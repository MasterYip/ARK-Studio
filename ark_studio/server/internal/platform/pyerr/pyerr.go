// Package pyerr is the part of Python's exception model that decides error
// replies: the exception class picks the HTTP status and str(exc) the text.
package pyerr

import "arkstudio/go/internal/compat/pyjson"

// ValueError is a Python ValueError raised outside StoreError (int() of a
// superscript digit and similar).  GET answers it with 500, POST with 400.
type ValueError struct{ Message string }

func (e *ValueError) Error() string { return e.Message }

// InternalError is a Python exception outside the ValueError family
// (AttributeError, TypeError, KeyError): every handler answers it with 500
// "Internal error: <message>".
type InternalError struct{ Message string }

func (e *InternalError) Error() string { return e.Message }

// TypeName is type(v).__name__ for a decoded JSON value.
func TypeName(v any) string {
	switch v.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case int64:
		return "int"
	case float64:
		return "float"
	case string:
		return "str"
	case []byte:
		return "bytes"
	case []any:
		return "list"
	case *pyjson.Object:
		return "dict"
	}
	if _, ok := v.(interface{ String() string }); ok {
		return "int" // *big.Int
	}
	return "object"
}

// AttrError is AttributeError("'<type>' object has no attribute '<name>'").
func AttrError(v any, name string) error {
	return &InternalError{Message: "'" + TypeName(v) + "' object has no attribute '" + name + "'"}
}
