// Package testpath trims PATH for test binaries.
//
// Every server App probes the agent CLIs (claude, codex, gemini ...) on PATH.
// With a long developer PATH those lookups put hundreds of thousands of
// entries into the test log, which the go command re-checks before it reuses
// a cached result (minutes on Windows), and the results depended on which
// CLIs the machine has installed.  Call Use from TestMain.
package testpath

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// tools are the programs the tests run; their folders stay on PATH.
var tools = []string{"git", "sh", "python3", "python", "node", "ssh", "ffmpeg",
	"powershell", "pwsh", "cmd", "taskkill", "ps", "sleep"}

// Use sets PATH to the folders that hold the tools the tests use (and, on
// Windows, PATHEXT to the executable extensions).
func Use() {
	var dirs []string
	seen := map[string]bool{}
	for _, tool := range tools {
		p, err := exec.LookPath(tool)
		if err != nil {
			continue
		}
		dir := filepath.Dir(p)
		if key := strings.ToLower(dir); !seen[key] {
			seen[key] = true
			dirs = append(dirs, dir)
		}
	}
	os.Setenv("PATH", strings.Join(dirs, string(os.PathListSeparator)))
	if runtime.GOOS == "windows" {
		os.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD")
	}
}
