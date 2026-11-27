package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// npm7Shim is the cmd-shim `npm install -g @google/gemini-cli` writes.
const npm7Shim = `@ECHO off
GOTO start
:find_dp0
SET dp0=%~dp0
EXIT /b
:start
SETLOCAL
CALL :find_dp0

IF EXIST "%dp0%\node.exe" (
  SET "_prog=%dp0%\node.exe"
) ELSE (
  SET "_prog=node"
  SET PATHEXT=%PATHEXT:;.JS;=;%
)

endLocal & goto #_undefined_# 2>NUL || title %COMSPEC% & "%_prog%"  "%dp0%\node_modules\@google\gemini-cli\bundle\gemini.js" %*
`

// npm6Shim is the older form.
const npm6Shim = `@IF EXIST "%~dp0\node.exe" (
  "%~dp0\node.exe"  "%~dp0\node_modules\@google\gemini-cli\bundle\gemini.js" %*
) ELSE (
  node  "%~dp0\node_modules\@google\gemini-cli\bundle\gemini.js" %*
)`

func TestNpmShimArgv(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "node_modules", "@google", "gemini-cli", "bundle", "gemini.js")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("//"), 0o644); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(dir, "gemini.cmd")
	if err := os.WriteFile(shim, []byte(strings.ReplaceAll(npm7Shim, "\n", "\r\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := func(string) (string, error) { return "", os.ErrNotExist }
	node := func(name string) (string, error) {
		if name == "node" {
			return "/opt/node/node.exe", nil
		}
		return "", os.ErrNotExist
	}
	found := func(name string) string {
		if name == "gemini.cmd" {
			return shim
		}
		return ""
	}
	// Node runs the script; the prompt stays one argument, line breaks and all.
	got := npmShimArgv([]string{"gemini", "-p", "a\nb & \"c\""}, node, found)
	if len(got) != 4 || got[0] != "/opt/node/node.exe" || got[1] != script || got[3] != "a\nb & \"c\"" {
		t.Fatalf("shim: %q", got)
	}
	// The shim's own node.exe wins.
	if err := os.WriteFile(filepath.Join(dir, "node.exe"), []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := npmShimArgv([]string{"gemini", "-p", "x"}, node, found); got[0] != filepath.Join(dir, "node.exe") {
		t.Fatalf("own node: %q", got)
	}
	// Left alone: a program with an extension or a path, one that is found,
	// a name without a shim.
	for _, argv := range [][]string{{"gemini.exe", "-p"}, {`C:\x\gemini`, "-p"}, {"other"}} {
		if got := npmShimArgv(argv, missing, found); got[0] != argv[0] {
			t.Fatalf("changed %q -> %q", argv, got)
		}
	}
	if got := npmShimArgv([]string{"gemini"}, func(string) (string, error) { return "gemini.exe", nil }, found); got[0] != "gemini" {
		t.Fatalf("found program changed: %q", got)
	}
	if s := npmShimScript(shim, npm6Shim); s != script {
		t.Fatalf("npm 6 shim: %q", s)
	}
	// A hand-written .cmd (like a launcher that finds an .exe) is no shim.
	if s := npmShimScript(shim, "@echo off\r\n\"%TARGET%\" %*"); s != "" {
		t.Fatalf("not a shim: %q", s)
	}
	if got := pathFile("gemini.cmd", []string{"PATH=" + dir}); got != shim {
		t.Fatalf("pathFile: %q", got)
	}
}
