package sessions

// npm command shims on Windows (2026-11-27).  `npm install -g` puts a
// `<name>.cmd` batch file on PATH (Gemini CLI has no .exe).  Sessions start
// programs the way CreateProcess does, without PATHEXT, so a bare "gemini"
// would not be found; and a batch file runs through cmd.exe, which cannot
// pass a prompt with line breaks, quotes or `&` intact.  So when only such
// a shim exists, ARK reads the script it calls and runs Node with it:
//
//   "%dp0%\node_modules\@google\gemini-cli\bundle\gemini.js" %*
//     -> node <npm folder>\node_modules\@google\gemini-cli\bundle\gemini.js
//
// Node is the shim's own node.exe when it has one, else node on PATH.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// npmShimScriptRe finds the script of an npm cmd-shim (npm 6 and 7+).
var npmShimScriptRe = regexp.MustCompile(`"%~?dp0%?\\([^"%]+?\.(?:js|mjs|cjs))"`)

// npmShimScript is the absolute script path of the shim at cmdPath ("" when
// the file is not an npm shim for a Node script).
func npmShimScript(cmdPath, text string) string {
	m := npmShimScriptRe.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	script := filepath.Join(filepath.Dir(cmdPath), filepath.FromSlash(strings.ReplaceAll(m[1], `\`, "/")))
	if fi, err := os.Stat(script); err != nil || fi.IsDir() {
		return ""
	}
	return script
}

// npmShimArgv runs an npm shim's script with Node directly; argv unchanged
// when the program is found as it is or is not an npm shim.  look finds a
// program the way the session start does; shim finds <name>.cmd on PATH.
func npmShimArgv(argv []string, look func(string) (string, error), shim func(string) string) []string {
	if len(argv) == 0 || strings.ContainsAny(argv[0], `/\:`) || filepath.Ext(argv[0]) != "" {
		return argv
	}
	if _, err := look(argv[0]); err == nil {
		return argv
	}
	cmdPath := shim(argv[0] + ".cmd")
	if cmdPath == "" {
		return argv
	}
	raw, err := os.ReadFile(cmdPath)
	if err != nil || len(raw) > 64*1024 {
		return argv
	}
	script := npmShimScript(cmdPath, string(raw))
	if script == "" {
		return argv
	}
	node := filepath.Join(filepath.Dir(cmdPath), "node.exe")
	if fi, err := os.Stat(node); err != nil || fi.IsDir() {
		if node, err = look("node"); err != nil {
			return argv
		}
	}
	return append([]string{node, script}, argv[1:]...)
}

// pathFile finds a file name on PATH ("" when absent).
func pathFile(name string, env []string) string {
	path := os.Getenv("PATH")
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.EqualFold(k, "PATH") {
			path = v
		}
	}
	for _, d := range filepath.SplitList(path) {
		if d == "" {
			continue
		}
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}
