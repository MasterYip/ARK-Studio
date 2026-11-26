package search

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, text := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func objects(v any) []*pyjson.Object {
	list, _ := v.([]any)
	out := make([]*pyjson.Object, len(list))
	for i, item := range list {
		out[i] = item.(*pyjson.Object)
	}
	return out
}

func refsOf(preview *pyjson.Object) []FileRef {
	var refs []FileRef
	for _, f := range objects(preview.Value("files")) {
		refs = append(refs, FileRef{pyjson.Str(f.Value("path")), pyjson.Str(f.Value("hash"))})
	}
	return refs
}

func TestReplaceTextKeepsLineEndsAndExpandsGroups(t *testing.T) {
	re, err := BuildMatcher(`(\w+)@(\w+)`, Options{Regex: true, Case: true})
	if err != nil {
		t.Fatal(err)
	}
	budget := stepBudget
	text := "ana@ark and bo@ark\r\nnone here\nlast@x"
	out, count, samples, err := replaceText(text, re, []rune("$2:$1 ($&) $$ $9"), true, &budget)
	if err != nil {
		t.Fatal(err)
	}
	want := "ark:ana (ana@ark) $ $9 and ark:bo (bo@ark) $ $9\r\nnone here\nx:last (last@x) $ $9"
	if out != want || count != 3 || len(samples) != 2 || samples[0].line != 1 || samples[1].line != 3 {
		t.Fatalf("got %q %d %+v", out, count, samples)
	}
	// Plain text: "$1" is literal; case-insensitive by default.
	re, _ = BuildMatcher("Todo", Options{})
	out, count, _, _ = replaceText("todo TODO toDo\n", re, []rune("$1"), false, &budget)
	if out != "$1 $1 $1\n" || count != 3 {
		t.Fatalf("plain: %q %d", out, count)
	}
	// Whole word.
	re, _ = BuildMatcher("cat", Options{Word: true})
	out, count, _, _ = replaceText("cat concat cat.", re, []rune("dog"), false, &budget)
	if out != "dog concat dog." || count != 2 {
		t.Fatalf("word: %q %d", out, count)
	}
}

func TestReplacePreviewAndApply(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"a.md":           "\xef\xbb\xbfcolor and colour\ncolor\n",
		"docs/b.txt":     "no match\n",
		"docs/c.txt":     "color\r\n",
		"skip/d.txt":     "color\n",
		"node_modules/x": "color\n",
	})
	o := Options{}
	preview, err := ReplacePreview(root, "color", o, "", "skip", "hue")
	if err != nil {
		t.Fatal(err)
	}
	files := objects(preview.Value("files"))
	if len(files) != 2 || preview.Value("total") != 3 {
		t.Fatalf("preview: %s", pyjson.Dumps(preview))
	}
	if files[0].Value("path") != "a.md" || files[0].Value("count") != 2 {
		t.Fatalf("first file: %s", pyjson.Dumps(files[0]))
	}
	samples := objects(files[0].Value("samples"))
	if samples[0].Value("before") != "color and colour" || samples[0].Value("after") != "hue and colour" {
		t.Fatalf("samples: %s", pyjson.Dumps(files[0]))
	}
	// Nothing was written by the preview.
	if read(t, root, "a.md") != "\xef\xbb\xbfcolor and colour\ncolor\n" {
		t.Fatal("preview wrote the file")
	}
	// A file edited after the preview is a conflict; the other is replaced.
	writeFiles(t, root, map[string]string{"docs/c.txt": "color color\r\n"})
	out, err := ReplaceApply(root, "color", o, "hue", refsOf(preview))
	if err != nil {
		t.Fatal(err)
	}
	if out.Value("total") != 2 || len(objects(out.Value("replaced"))) != 1 || len(objects(out.Value("conflicts"))) != 1 {
		t.Fatalf("apply: %s", pyjson.Dumps(out))
	}
	if got := read(t, root, "a.md"); got != "\xef\xbb\xbfhue and colour\nhue\n" {
		t.Fatalf("a.md: %q (the BOM stays)", got)
	}
	if got := read(t, root, "docs/c.txt"); got != "color color\r\n" {
		t.Fatalf("conflicting file was changed: %q", got)
	}
	if read(t, root, "skip/d.txt") != "color\n" || read(t, root, "node_modules/x") != "color\n" {
		t.Fatal("excluded or ignored files changed")
	}
}

func TestReplaceRefusesPathsOutsideTheProject(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "project")
	writeFiles(t, parent, map[string]string{"outside.txt": "color\n", "project/in.txt": "color\n"})
	out, err := ReplaceApply(root, "color", Options{}, "hue", []FileRef{
		{"../outside.txt", "x"}, {filepath.Join(parent, "outside.txt"), "x"}, {".git/config", "x"}, {"in.txt", "stale"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Value("total") != 0 || len(objects(out.Value("conflicts"))) != 4 {
		t.Fatalf("apply: %s", pyjson.Dumps(out))
	}
	if read(t, parent, "outside.txt") != "color\n" || read(t, root, "in.txt") != "color\n" {
		t.Fatal("a refused file was written")
	}
}

func TestReplaceErrors(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"a.txt": "x\n"})
	for _, tc := range []struct {
		q    string
		o    Options
		want string
	}{
		{"(", Options{Regex: true}, "invalid regular expression"},
		{"a*", Options{Regex: true}, "the pattern matches empty text"},
		{"  ", Options{}, "query is required"},
	} {
		_, err := ReplacePreview(root, tc.q, tc.o, "", "", "y")
		se, ok := err.(*Error)
		if !ok || len(se.Message) < len(tc.want) || se.Message[:len(tc.want)] != tc.want {
			t.Errorf("%q: %v, want %q", tc.q, err, tc.want)
		}
	}
	if _, err := ReplacePreview(root, "x", Options{}, "", "", string(make([]rune, MaxReplacement+1))); err == nil {
		t.Error("a huge replacement was accepted")
	}
	if _, err := ReplaceApply(root, "x", Options{}, "y", nil); err == nil {
		t.Error("an empty file list was accepted")
	}
	if _, err := ReplaceApply(root, "x", Options{}, "y", make([]FileRef, MaxReplaceFiles+1)); err == nil {
		t.Error("too many files were accepted")
	}
}

func TestReplacePreviewRespectsGitignoreAndCap(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	files := map[string]string{".gitignore": "generated/\n", "generated/g.txt": "color\n"}
	for i := 0; i < MaxReplaceFiles+5; i++ {
		files[fmt.Sprintf("many/f%04d.txt", i)] = "color\n"
	}
	writeFiles(t, root, files)
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	preview, err := ReplacePreview(root, "color", Options{}, "", "", "hue")
	if err != nil {
		t.Fatal(err)
	}
	if preview.Value("truncated") != true {
		t.Fatalf("not truncated: file_count %v", preview.Value("file_count"))
	}
	if n := len(objects(preview.Value("files"))); n > MaxReplaceFiles {
		t.Fatalf("%d files, cap %d", n, MaxReplaceFiles)
	}
	for _, f := range objects(preview.Value("files")) {
		if f.Value("path") == "generated/g.txt" {
			t.Fatal("a .gitignore'd file was offered")
		}
	}
	// Only "generated" matched by include: ignored, so nothing.
	preview, err = ReplacePreview(root, "color", Options{}, "generated/**", "", "hue")
	if err != nil || preview.Value("file_count") != 0 {
		t.Fatalf("ignored: %v %v", err, preview)
	}
}
