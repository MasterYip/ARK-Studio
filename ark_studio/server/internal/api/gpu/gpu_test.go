package gpu

import (
	"context"
	"errors"
	"testing"

	"arkstudio/go/internal/api"
	"arkstudio/go/internal/api/apitest"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pyurl"
	gpudom "arkstudio/go/internal/gpu"
	"arkstudio/go/internal/platform/pyerr"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/sessions"
)

// fakeCtx is an api.Ctx with a query string and a captured reply.
type fakeCtx struct {
	apitest.Ctx
	q    pyurl.Values
	sent *pyjson.Object
}

func (f *fakeCtx) Context() context.Context                     { return context.Background() }
func (f *fakeCtx) Query() pyurl.Values                          { return f.q }
func (f *fakeCtx) OK(d *pyjson.Object)                          { f.sent = d }
func (f *fakeCtx) Require(string) (*project.Project, error)     { return nil, api.Err(403, "no", "") }
func (f *fakeCtx) Actor() string                                { return "" }
func (f *fakeCtx) ReadJSON() (*pyjson.Object, error)            { return pyjson.NewObject(), nil }
func (f *fakeCtx) RequireTaskAgent() (*sessions.Session, error) { return nil, api.Err(403, "no", "") }
func (f *fakeCtx) BindTaskSession(*sessions.Session)            {}

func TestQueryInt(t *testing.T) {
	cases := []struct {
		q       pyurl.Values
		want    int64
		wantErr string
	}{
		{pyurl.Values{}, 100, ""},
		{pyurl.Values{"limit": {"5"}}, 5, ""},
		{pyurl.Values{"limit": {"0005"}}, 5, ""},
		{pyurl.Values{"limit": {"0"}}, 0, "limit must be between 1 and 500"},
		{pyurl.Values{"limit": {"501"}}, 0, "limit must be between 1 and 500"},
		{pyurl.Values{"limit": {"99999999999999999999"}}, 0, "limit must be between 1 and 500"},
		{pyurl.Values{"limit": {"-1"}}, 0, "limit must be an integer between 1 and 500"},
		{pyurl.Values{"limit": {"１"}}, 0, "limit must be an integer between 1 and 500"}, // ASCII digits only
		{pyurl.Values{"limit": {"1", "2"}}, 0, "limit must be an integer between 1 and 500"},
	}
	for _, tc := range cases {
		got, err := queryInt(&fakeCtx{q: tc.q}, "limit", 100, 1, 500)
		if tc.wantErr != "" {
			var ae *api.Error
			if !errors.As(err, &ae) || ae.Status != 400 || ae.Message != tc.wantErr {
				t.Errorf("%v: err %v, want 400 %q", tc.q, err, tc.wantErr)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%v: %d %v, want %d", tc.q, got, err, tc.want)
		}
	}
}

func TestListQuery(t *testing.T) {
	f, err := listQuery(&fakeCtx{q: pyurl.Values{"status": {"queued, running", "queued"}, "ids": {""}, "name": {"x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if f.Status != "queued,running" || !f.HasIDs || len(f.IDs) != 0 || f.Name != "x" || f.Limit != int64(100) {
		t.Fatalf("%+v", f)
	}
	for q, msg := range map[string]string{"name": "name accepts one value", "status": "status must contain at least one job state"} {
		vals := pyurl.Values{q: {"a", "b"}}
		if q == "status" {
			vals = pyurl.Values{q: {" , "}}
		}
		var ae *api.Error
		if _, err := listQuery(&fakeCtx{q: vals}); !errors.As(err, &ae) || ae.Message != msg {
			t.Errorf("%s: %v", q, err)
		}
	}
}

func TestErrMapping(t *testing.T) {
	cases := []struct {
		in   error
		want string
	}{
		{&gpudom.Error{Status: 409, Message: "busy", Code: "GPU_BUSY"}, "api 409 busy GPU_BUSY"},
		{&gpudom.ValueError{Msg: "bad"}, "value bad"},
		{&gpudom.InternalError{Msg: "boom"}, "internal boom"},
		{&gpudom.OverflowError{Msg: "big"}, "internal big"},
		{&gpudom.PythonError{Type: "ValueError", Msg: "v"}, "value v"},
		{&gpudom.PythonError{Type: "KeyError", Msg: "k"}, "internal k"},
		{&gpudom.RecursionError{}, "internal maximum recursion depth exceeded"},
	}
	for _, tc := range cases {
		got := ""
		var ae *api.Error
		var ve *pyerr.ValueError
		var ie *pyerr.InternalError
		switch err := Err(tc.in); {
		case errors.As(err, &ae):
			got = "api " + pyjson.Str(int64(ae.Status)) + " " + ae.Message + " " + ae.Code
		case errors.As(err, &ve):
			got = "value " + ve.Message
		case errors.As(err, &ie):
			got = "internal " + ie.Message
		}
		if got != tc.want {
			t.Errorf("%T: %q, want %q", tc.in, got, tc.want)
		}
	}
	if Err(nil) != nil {
		t.Fatal("nil error must stay nil")
	}
	other := errors.New("x")
	if Err(other) != other {
		t.Fatal("unknown errors pass through")
	}
}

func TestPaths(t *testing.T) {
	for path, want := range map[string]bool{
		"/api/agent/v1/gpu/jobs/j1":      true,
		"/api/agent/v1/gpu/jobs/j1/logs": true,
		"/api/agent/v1/gpu/jobs/j1/x":    false,
		"/api/agent/v1/gpu/jobs":         false,
	} {
		if IsAgentJobPath(path) != want {
			t.Errorf("%s: %v", path, !want)
		}
	}
	// Unknown GPU paths are not taken (the router tries the next route).
	h := Handlers{Svc: func() *gpudom.Service { return &gpudom.Service{} }}
	if ok, _ := h.Get(&fakeCtx{}, "/api/gpu/nope"); ok {
		t.Fatal("unknown GET path taken")
	}
	if ok, _ := h.Post(&fakeCtx{}, "/api/gpu/nope", pyjson.NewObject()); ok {
		t.Fatal("unknown POST path taken")
	}
	if ok, _ := h.TaskPost(&fakeCtx{}, "/api/task-agent/v1/gpu/other"); ok {
		t.Fatal("unknown task POST path taken")
	}
	// Task-agent POST checks the session before anything else.
	ok, err := h.TaskPost(&fakeCtx{}, "/api/task-agent/v1/gpu/jobs")
	var ae *api.Error
	if !ok || !errors.As(err, &ae) || ae.Status != 403 {
		t.Fatalf("task post without session: %v %v", ok, err)
	}
}
