package apkinfo

import (
	"archive/zip"
	"bytes"
	"os"
	"testing"
)

// testdata/AndroidManifest.xml is the compiled manifest of the native app's
// debug build (aapt2, versionCode 16, versionName 3.0).
func manifest(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/AndroidManifest.xml")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// APK is a zip holding the manifest (enough for Read).
func APK(t *testing.T, manifest []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	w, err := z.Create("AndroidManifest.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(manifest); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestParseCompiledManifest(t *testing.T) {
	info, err := Parse(manifest(t))
	if err != nil {
		t.Fatal(err)
	}
	if info.Package != "com.masteryip.arkstudio" || info.VersionCode != 16 || info.VersionName != "3.0" {
		t.Fatalf("info: %+v", info)
	}
	got, err := ReadBytes(APK(t, manifest(t)))
	if err != nil || got != info {
		t.Fatalf("from the zip: %+v %v", got, err)
	}
}

func TestRejectsOtherFiles(t *testing.T) {
	if _, err := Parse([]byte("<manifest/>")); err == nil {
		t.Error("plain XML accepted")
	}
	data := manifest(t)
	for _, n := range []int{8, 100, len(data) / 2} {
		if _, err := Parse(data[:n]); err == nil {
			t.Errorf("truncated at %d accepted", n)
		}
	}
	if _, err := ReadBytes([]byte("not a zip")); err == nil {
		t.Error("not a zip accepted")
	}
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	_, _ = z.Create("classes.dex")
	_ = z.Close()
	if _, err := ReadBytes(buf.Bytes()); err == nil {
		t.Error("zip without a manifest accepted")
	}
}
