package pyerr

import (
	"errors"
	"math/big"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

func TestTypeName(t *testing.T) {
	cases := []struct {
		v    any
		want string
	}{
		{nil, "NoneType"}, {true, "bool"}, {int64(1), "int"}, {1.5, "float"},
		{"s", "str"}, {[]byte("b"), "bytes"}, {[]any{}, "list"}, {pyjson.NewObject(), "dict"},
		{big.NewInt(1), "int"}, {struct{}{}, "object"},
	}
	for _, c := range cases {
		if got := TypeName(c.v); got != c.want {
			t.Errorf("TypeName(%#v) = %q, want %q", c.v, got, c.want)
		}
	}
}

func TestAttrError(t *testing.T) {
	err := AttrError([]any{}, "get")
	var ie *InternalError
	if !errors.As(err, &ie) || ie.Error() != "'list' object has no attribute 'get'" {
		t.Fatalf("got %v", err)
	}
	var ve error = &ValueError{Message: "bad"}
	if ve.Error() != "bad" {
		t.Fatal(ve)
	}
}
