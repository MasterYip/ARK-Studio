package resources

import (
	"context"
	"errors"
	"testing"

	"arkstudio/go/internal/api"
	"arkstudio/go/internal/api/apitest"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pyurl"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/sessions"
)

// denyCtx refuses every project permission.
type denyCtx struct {
	apitest.Ctx
	asked []string
}

func (f *denyCtx) Context() context.Context { return context.Background() }
func (f *denyCtx) Query() pyurl.Values      { return nil }
func (f *denyCtx) OK(*pyjson.Object)        {}
func (f *denyCtx) Require(level string) (*project.Project, error) {
	f.asked = append(f.asked, level)
	return nil, api.Err(403, "Project permission required: "+level, "PROJECT_PERMISSION")
}
func (f *denyCtx) Actor() string                                { return "ana" }
func (f *denyCtx) ReadJSON() (*pyjson.Object, error)            { return pyjson.NewObject(), nil }
func (f *denyCtx) RequireTaskAgent() (*sessions.Session, error) { return nil, nil }
func (f *denyCtx) BindTaskSession(*sessions.Session)            {}

func TestDevicePostPaths(t *testing.T) {
	cases := []struct {
		path    string
		handled bool
	}{
		{"/api/resources/devices/d1", true},
		{"/api/resources/devices/a%2Fb", true}, // the raw path; the id is unquoted later
		{"/api/resources/devices/", false},
		{"/api/resources/devices/a/b", false}, // fullmatch
		{"/api/resources/graph", false},
	}
	for _, tc := range cases {
		c := &denyCtx{}
		handled, err := Handlers{}.DevicePost(c, tc.path, pyjson.NewObject())
		if handled != tc.handled {
			t.Errorf("%s: handled %v", tc.path, handled)
			continue
		}
		var ae *api.Error
		if handled && (!errors.As(err, &ae) || ae.Status != 403 || len(c.asked) != 1 || c.asked[0] != "read") {
			t.Errorf("%s: %v %v", tc.path, err, c.asked)
		}
	}
}

func TestGraphNeedsRead(t *testing.T) {
	c := &denyCtx{}
	var ae *api.Error
	if err := (Handlers{}).Graph(c); !errors.As(err, &ae) || ae.Status != 403 || c.asked[0] != "read" {
		t.Fatalf("%v %v", err, c.asked)
	}
}

func TestConnectNeedsRead(t *testing.T) {
	for _, tc := range []struct {
		path    string
		handled bool
	}{
		{"/api/resources/connect/gpu:a", true},
		{"/api/resources/connect/", false},
		{"/api/resources/connect/a/b", false},
		{"/api/resources/connect", false},
	} {
		c := &denyCtx{}
		handled, err := Handlers{}.ConnectPost(c, tc.path, pyjson.NewObject())
		var ae *api.Error
		if handled != tc.handled || (handled && (!errors.As(err, &ae) || ae.Status != 403 || c.asked[0] != "read")) {
			t.Errorf("%s: %v %v %v", tc.path, handled, err, c.asked)
		}
	}
	c := &denyCtx{}
	var ae *api.Error
	if err := (Handlers{}).Connect(c); !errors.As(err, &ae) || ae.Status != 403 || c.asked[0] != "read" {
		t.Fatalf("%v %v", err, c.asked)
	}
}
