//go:build !windows

package sessions

import (
	"io/fs"
	"os"

	"arkstudio/go/internal/compat/pyos"
)

func decodeANSI([]byte) (string, bool) { return "", false }

// lookProgram is the POSIX Popen search: a bare name on the child's PATH.
func lookProgram(name string, env []string) (string, error) {
	if pyos.HasDirPart(name) {
		return name, nil
	}
	if found := pyos.Which(name, envPath(env), true); found != "" {
		return found, nil
	}
	return "", &os.PathError{Op: "exec", Path: name, Err: fs.ErrNotExist}
}
