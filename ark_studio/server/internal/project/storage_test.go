package project

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

func storageManager(t *testing.T) (*Manager, string) {
	t.Helper()
	base := t.TempDir()
	m, err := NewManager(filepath.Join(base, "data", "projects"), 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m, base
}

func checkCode(err error) string {
	if ce, ok := err.(*CheckError); ok {
		return ce.Code
	}
	if err != nil {
		return "other:" + err.Error()
	}
	return ""
}

func TestNewProjectsGoToTheChosenFolderAndOldOnesStayListed(t *testing.T) {
	m, base := storageManager(t)
	ctx := context.Background()
	old, err := m.Create(ctx, "Old", "", "blank", false, "")
	if err != nil {
		t.Fatal(err)
	}
	chosen := filepath.Join(base, "elsewhere")
	if err := os.MkdirAll(chosen, 0o755); err != nil {
		t.Fatal(err)
	}
	m.SetStorage(StorageConfig{Default: chosen})
	p, err := m.Create(ctx, "New", "", "blank", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(p.Path) != Resolve(chosen) {
		t.Fatalf("created in %s, want %s", p.Path, chosen)
	}
	if filepath.Dir(old.Path) != m.Root {
		t.Fatalf("the old project moved: %s", old.Path)
	}
	names := map[string]bool{}
	for _, s := range m.ListProjects() {
		names[pyjson.Str(s.Value("name"))] = true
	}
	if !names["Old"] || !names["New"] {
		t.Fatalf("listed: %v", names)
	}
	// Back to the built-in folder: the chosen one stays listed.
	m.SetStorage(StorageConfig{Folders: []string{chosen}})
	if dir, fb := m.NewProjectFolder(); dir != m.Root || fb {
		t.Fatalf("reset: %s %v", dir, fb)
	}
	if len(m.ListProjects()) != 2 || !m.InFolders(chosen) {
		t.Fatal("the chosen folder's project is no longer listed")
	}
	if err := m.Delete(p.ID()); err != nil {
		t.Fatalf("delete in a listed folder: %v", err)
	}
	infos := m.FolderInfos()
	if len(infos) != 2 || !infos[0].Builtin || !infos[0].Default || infos[0].Projects != 1 || infos[1].Projects != 0 {
		t.Fatalf("infos: %+v", infos)
	}
}

func TestMissingChosenFolderFallsBackToRoot(t *testing.T) {
	m, base := storageManager(t)
	gone := filepath.Join(base, "usb", "ark")
	m.SetStorage(StorageConfig{Default: gone})
	dir, fallback := m.NewProjectFolder()
	if dir != m.Root || !fallback {
		t.Fatalf("got %s %v", dir, fallback)
	}
	p, err := m.Create(context.Background(), "Safe", "", "blank", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(p.Path) != m.Root {
		t.Fatalf("created in %s", p.Path)
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Fatal("the missing folder was created")
	}
}

func TestCheckFolder(t *testing.T) {
	m, base := storageManager(t)
	if _, err := m.Create(context.Background(), "Proj", "", "blank", false, ""); err != nil {
		t.Fatal(err)
	}
	projectDir := pyjson.Str(m.ListProjects()[0].Value("path"))
	fresh := filepath.Join(base, "new", "deeper")
	res, err := m.CheckFolder(fresh)
	if err != nil || res.Exists || !res.WillCreate || res.Free <= 0 {
		t.Fatalf("fresh: %+v %v", res, err)
	}
	if _, err := os.Stat(fresh); !os.IsNotExist(err) {
		t.Fatal("check created the folder")
	}
	if err := PrepareFolder(res); err != nil {
		t.Fatal(err)
	}
	if res, err := m.CheckFolder(fresh); err != nil || !res.Exists {
		t.Fatalf("after prepare: %+v %v", res, err)
	}
	file := filepath.Join(base, "a-file")
	os.WriteFile(file, []byte("x"), 0o644)
	volume := filepath.VolumeName(base) + string(filepath.Separator)
	cases := map[string]string{
		"":                                  "STORAGE_PATH_REQUIRED",
		"relative/dir":                      "STORAGE_PATH_RELATIVE",
		volume:                              "STORAGE_PATH_SYSTEM",
		filepath.Join(projectDir, "assets"): "STORAGE_PATH_IN_PROJECT",
		projectDir:                          "STORAGE_PATH_IN_PROJECT",
		filepath.Join(base, "data"):         "STORAGE_PATH_DATA",
		file:                                "STORAGE_PATH_NOT_FOLDER",
		filepath.Join(file, "below"):        "STORAGE_PATH_NOT_FOLDER",
	}
	if goos == "windows" {
		cases[`C:\Windows\Temp\ark`] = "STORAGE_PATH_SYSTEM"
		cases[`C:\Program Files\ARK`] = "STORAGE_PATH_SYSTEM"
	} else {
		cases["/etc/ark"] = "STORAGE_PATH_SYSTEM"
		cases["/usr/local/ark"] = "STORAGE_PATH_SYSTEM"
		cases["/home"] = "STORAGE_PATH_SYSTEM"
	}
	for path, want := range cases {
		if _, err := m.CheckFolder(path); checkCode(err) != want {
			t.Errorf("%q: got %q want %s", path, checkCode(err), want)
		}
	}
	// The built-in folder itself is fine (choosing it resets).
	if _, err := m.CheckFolder(m.Root); err != nil {
		t.Errorf("root: %v", err)
	}
}

func TestSetStorageDedupesAndBounds(t *testing.T) {
	m, base := storageManager(t)
	a := filepath.Join(base, "a")
	var many []string
	for i := 0; i < MaxStorageFolders+5; i++ {
		many = append(many, filepath.Join(base, "f", strings.Repeat("x", i+1)))
	}
	m.SetStorage(StorageConfig{Default: a, Folders: append([]string{a, m.Root, "relative", a + string(filepath.Separator)}, many...)})
	got := m.Storage()
	if got.Default != Resolve(a) || got.Folders[0] != Resolve(a) || len(got.Folders) != MaxStorageFolders {
		t.Fatalf("got %+v", got)
	}
	for _, f := range got.Folders {
		if f == m.Root || !filepath.IsAbs(f) {
			t.Fatalf("kept %s", f)
		}
	}
	// The built-in folder as "default" means no choice.
	m.SetStorage(StorageConfig{Default: m.Root})
	if got := m.Storage(); got.Default != "" || len(got.Folders) != 0 {
		t.Fatalf("root default: %+v", got)
	}
}
