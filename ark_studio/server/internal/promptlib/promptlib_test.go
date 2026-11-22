package promptlib

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanChecksAndWarns(t *testing.T) {
	in, warn, err := Clean(Input{Title: "  Weekly digest ", Description: "one\nline", Body: "Summarise {project} for {user}; skip {when}.\r\n"})
	if err != nil || in.Title != "Weekly digest" || in.Description != "one line" || strings.Contains(in.Body, "\r") {
		t.Fatalf("%+v %v", in, err)
	}
	if len(warn) != 1 || !strings.Contains(warn[0], "{when}") {
		t.Fatalf("warnings %v", warn)
	}
	// JSON in a body is not a placeholder.
	if _, warn, _ := Clean(Input{Title: "x", Body: `POST {"body": "x"} for {unit}`}); len(warn) != 0 {
		t.Fatalf("json braces warned: %v", warn)
	}
	for _, bad := range []Input{{Body: "b"}, {Title: "t"}, {Title: strings.Repeat("t", 81), Body: "b"},
		{Title: "t", Body: strings.Repeat("b", 8001)}, {Title: "t", Body: "b", ID: "x1"}} {
		if _, _, err := Clean(bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}

func TestPutDeleteImportAndVersions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p", "prompts.json")
	f, err := Update(path, func(f *File) error {
		_, err := Put(f, Input{Title: "A", Body: "a"}, "ana")
		return err
	})
	if err != nil || len(f.Prompts) != 1 || !reCustomID.MatchString(f.Prompts[0].ID) || f.Prompts[0].CreatedBy != "ana" {
		t.Fatalf("%+v %v", f, err)
	}
	id, v1 := f.Prompts[0].ID, f.Prompts[0].Version
	lib1 := LibraryVersion(f.Prompts)
	f, err = Update(path, func(f *File) error {
		_, err := Put(f, Input{ID: id, Title: "A", Body: "a2"}, "bob")
		return err
	})
	if err != nil || f.Prompts[0].Version == v1 || f.Prompts[0].UpdatedBy != "bob" || LibraryVersion(f.Prompts) == lib1 {
		t.Fatalf("edit: %+v %v", f.Prompts, err)
	}
	if _, err := Update(path, func(f *File) error {
		_, err := Put(f, Input{ID: "cp-00000000", Title: "x", Body: "y"}, "a")
		return err
	}); err != ErrNotFound {
		t.Fatalf("unknown id: %v", err)
	}
	f, _ = Update(path, func(f *File) error {
		added, skipped, err := Import(f, []Input{{Title: "A", Body: "a2"}, {Title: "B", Body: "b"}}, "ana")
		if added != 1 || skipped != 1 {
			t.Errorf("import %d %d", added, skipped)
		}
		return err
	})
	if len(f.Prompts) != 2 {
		t.Fatalf("after import %d", len(f.Prompts))
	}
	again, _ := Load(path)
	if len(again.Prompts) != 2 || again.Prompts[0].Version == "" || again.Format != Format {
		t.Fatalf("reload %+v", again)
	}
	if _, err := Update(path, func(f *File) error { return Delete(f, id) }); err != nil {
		t.Fatal(err)
	}
	if f, _ := Load(path); len(f.Prompts) != 1 || f.Prompts[0].Title != "B" {
		t.Fatalf("delete %+v", f.Prompts)
	}
}

func TestPinsAndLimits(t *testing.T) {
	got, err := CleanPins([]string{"team", "setup", "team", "cp-0a1b2c3d"})
	if err != nil || strings.Join(got, ",") != "team,setup,cp-0a1b2c3d" {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := CleanPins([]string{"Bad Id"}); err == nil {
		t.Fatal("bad id accepted")
	}
	many := make([]string, MaxPins+1)
	for i := range many {
		many[i] = "p" + strings.Repeat("a", i+1)
	}
	if _, err := CleanPins(many); err == nil {
		t.Fatal("too many pins accepted")
	}
	f := &File{}
	for i := 0; i < MaxPrompts; i++ {
		if _, err := Put(f, Input{Title: "t", Body: "b"}, "a"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Put(f, Input{Title: "t", Body: "b"}, "a"); err == nil {
		t.Fatal("limit not enforced")
	}
}
