package workspace

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func previewProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for rel, text := range map[string]string{
		"site/index.html":     "<h1>Site</h1>",
		"site/js/app.js":      "document.body.dataset.ran = '1';",
		"site/sub/index.html": "<p>sub</p>",
		"other/secret.txt":    "secret",
		"top.html":            "<p>top</p>",
		"notes.txt":           "not html",
		"node_modules/x/x.js": "ignored",
		"site/.git/config":    "nope",
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestPreviewTokenIsScopedToTheFolder(t *testing.T) {
	root := previewProject(t)
	p, name, err := IssuePreview(root, "p1", "ana", "site/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if p.Dir != "site" || name != "index.html" || len(p.Token) < 40 {
		t.Fatalf("issued %+v %q", p, name)
	}
	live, ok := LookupPreview(p.Token)
	if !ok {
		t.Fatal("token not found")
	}
	for rel, want := range map[string]string{"index.html": "index.html", "js/app.js": "app.js", "sub": "index.html", "sub/": "index.html"} {
		target, err := PreviewFile(live, rel)
		if err != nil || filepath.Base(target) != want {
			t.Errorf("%q: %v %q", rel, err, target)
		}
	}
	// Outside the folder, ignored, absolute, missing: refused.
	for _, rel := range []string{"../other/secret.txt", "..", "sub/../../other/secret.txt", "/etc/passwd", "C:/Windows/win.ini",
		".git/config", "missing.js", "", "a\x00b"} {
		if target, err := PreviewFile(live, rel); err == nil {
			t.Errorf("%q served %s", rel, target)
		}
	}
	// A root-level page may read the project, still not ignored folders.
	top, _, err := IssuePreview(root, "p1", "ana", "top.html")
	if err != nil || top.Dir != "" {
		t.Fatalf("top: %v %+v", err, top)
	}
	if _, err := PreviewFile(top, "other/secret.txt"); err != nil {
		t.Errorf("root page reads its project: %v", err)
	}
	if _, err := PreviewFile(top, "node_modules/x/x.js"); err == nil {
		t.Error("ignored folder served")
	}
	// Only HTML files, only inside the project.
	for _, file := range []string{"notes.txt", "site", "../x.html", "missing.html"} {
		if _, _, err := IssuePreview(root, "p1", "ana", file); err == nil {
			t.Errorf("issued for %q", file)
		}
	}
}

func TestPreviewTokenExpiresAndRenews(t *testing.T) {
	root := previewProject(t)
	now := time.Now()
	previewNow = func() time.Time { return now }
	t.Cleanup(func() { previewNow = time.Now })
	p, _, err := IssuePreview(root, "p1", "ana", "site/index.html")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(PreviewTTL - time.Minute)
	if _, ok := RenewPreview(p.Token, "p1", "bo"); ok {
		t.Fatal("another person renewed the token")
	}
	if _, ok := RenewPreview(p.Token, "p2", "ana"); ok {
		t.Fatal("renewed for another project")
	}
	if _, ok := RenewPreview(p.Token, "p1", "ana"); !ok {
		t.Fatal("renew failed")
	}
	now = now.Add(PreviewTTL - time.Second)
	if _, ok := LookupPreview(p.Token); !ok {
		t.Fatal("renewed token expired early")
	}
	now = now.Add(2 * time.Second)
	if _, ok := LookupPreview(p.Token); ok {
		t.Fatal("expired token still works")
	}
	if _, ok := RenewPreview(p.Token, "p1", "ana"); ok {
		t.Fatal("an expired token was renewed")
	}
	if _, ok := LookupPreview("not-a-token"); ok {
		t.Fatal("unknown token accepted")
	}
}

func TestPreviewRegistryIsBounded(t *testing.T) {
	root := previewProject(t)
	for i := 0; i < MaxPreviews+10; i++ {
		if _, _, err := IssuePreview(root, "p1", "ana", "top.html"); err != nil {
			t.Fatal(err)
		}
	}
	previews.Lock()
	n := len(previews.m)
	previews.Unlock()
	if n > MaxPreviews {
		t.Fatalf("%d tokens kept", n)
	}
}
