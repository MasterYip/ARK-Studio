package search

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"arkstudio/go/internal/api"
	"arkstudio/go/internal/api/apitest"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/store"
)

// readOnlyMember may read the project but not write to it.
type readOnlyMember struct{ *apitest.Ctx }

func (c readOnlyMember) Require(level string) (*project.Project, error) {
	c.Ctx.Levels = append(c.Ctx.Levels, level)
	if level != "read" {
		return nil, api.Err(403, "This action needs write access.", "")
	}
	return c.Ctx.Project, nil
}

func TestReplaceNeedsWriteAccess(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("color\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &project.Project{Path: root, Manifest: store.NewManifest(pyjson.NewObject("id", "p1", "name", "P"))}
	body := pyjson.NewObject("q", "color", "replace", "hue")

	reader := readOnlyMember{&apitest.Ctx{Project: p}}
	err := Replace(reader, root, body)
	var ae *api.Error
	if !errorsAs(err, &ae) || ae.Status != 403 || reader.Ctx.Sent != nil {
		t.Fatalf("read-only member: %v", err)
	}

	writer := &apitest.Ctx{Project: p}
	if err := Replace(writer, root, body); err != nil || writer.Sent == nil || writer.Sent.Value("total") != 1 {
		t.Fatalf("preview: %v %v", err, writer.Sent)
	}
	files := writer.Sent.Value("files")
	apply := pyjson.NewObject("q", "color", "replace", "hue", "apply", true, "files", files)
	if err := Replace(writer, root, apply); err != nil || writer.Sent.Value("total") != 1 {
		t.Fatalf("apply: %v %v", err, writer.Sent)
	}
	if raw, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(raw) != "hue\n" {
		t.Fatalf("file: %q", raw)
	}
	for _, bad := range []*pyjson.Object{
		pyjson.NewObject("q", "x"),                                // no replacement
		pyjson.NewObject("q", "(", "regex", true, "replace", "y"), // bad regex
		pyjson.NewObject("q", "x", "replace", "y", "apply", true), // no files
		pyjson.NewObject("q", "x", "replace", "y", "apply", true, "files", []any{"a.txt"}),
	} {
		c := &apitest.Ctx{Project: p}
		if err := Replace(c, root, bad); !errorsAs(err, &ae) || ae.Status != 400 {
			t.Errorf("%s: %v", pyjson.Dumps(bad), err)
		}
	}
}

func errorsAs(err error, target **api.Error) bool { return errors.As(err, target) }
