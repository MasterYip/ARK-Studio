// Package sessionctx ports arkstudio/session_context.py.  This file has the
// internal transcript mirrors the session manager keeps in step:
// <project>/sessions/internal/<s_id>/{meta.json,stdout.jsonl,stderr.jsonl},
// a redacted copy of an ARK task session so a copied project carries it.
// (The context store and its routes are ported with the session-context
// module.)
package sessionctx

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"arkstudio/go/internal/platform/textfile"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/compat/pyurl"
	"arkstudio/go/internal/secretredact"
	"arkstudio/go/internal/sessions"
)

const (
	SessionsDir = "sessions"
	InternalDir = "internal"
)

// InternalIDRe is INTERNAL_ID_RE.
var InternalIDRe = regexp.MustCompile(`\As_[a-z0-9]{8,32}\z`)

var mirrorMetaDrop = map[string]bool{"read_token": true, "read_env": true, "api": true, "command_preview": true,
	"linux_account_home": true, "approval_rules": true, "steer_queue": true, "pid": true}

// Error is ContextError (a SessionError).
type Error = sessions.Error

func resolve(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

// MirrorFolder is mirror_folder.
func MirrorFolder(projectPath, sessionID string) (string, error) {
	if !InternalIDRe.MatchString(sessionID) {
		return "", &Error{Message: "invalid session id", Status: 404, Code: "SESSION_NOT_FOUND"}
	}
	return filepath.Join(resolve(projectPath), SessionsDir, InternalDir, sessionID), nil
}

func redactedLog(path string, extra []string) (string, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	text := strings.ReplaceAll(strings.ReplaceAll(pyurl.DecodeReplace(raw), "\r\n", "\n"), "\r", "\n")
	var out []string
	for _, line := range pytext.SplitLines(text, false) {
		var entry any = line
		if v, err := pyjson.Loads(line); err == nil {
			entry = v
		}
		entry, _ = secretredact.Value(entry, extra)
		if s, ok := entry.(string); ok {
			out = append(out, s)
		} else {
			out = append(out, string(pyjson.Dumps(entry)))
		}
	}
	if len(out) == 0 {
		return "", true
	}
	return strings.Join(out, "\n") + "\n", true
}

func writeText(path, text string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, textfile.Encode(text), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// MirrorInternal is mirror_internal: the redacted mirror of one runtime
// session folder ("" when the folder has no valid meta.json).
func MirrorInternal(projectPath, runtimeFolder string, extraSecrets []string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(runtimeFolder, "meta.json"))
	if err != nil {
		return "", nil
	}
	meta, ok := pyjson.LoadsObject(strings.ReplaceAll(string(raw), "\r\n", "\n"))
	if !ok {
		return "", nil
	}
	id, _ := meta.Value("id").(string)
	if !InternalIDRe.MatchString(id) {
		return "", nil
	}
	var secrets []string
	for _, s := range extraSecrets {
		if s != "" {
			secrets = append(secrets, s)
		}
	}
	target, err := MirrorFolder(projectPath, id)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return "", err
	}
	clean := pyjson.NewObject()
	for _, k := range meta.Keys() {
		if !mirrorMetaDrop[k] {
			clean.Set(k, meta.Value(k))
		}
	}
	redacted, _ := secretredact.Value(clean, secrets)
	clean = redacted.(*pyjson.Object)
	clean.Set("mirrored_at", sessions.NowISO())
	for _, name := range []string{"stdout.jsonl", "stderr.jsonl"} {
		source := filepath.Join(runtimeFolder, name)
		if fi, err := os.Stat(source); err == nil && fi.Mode().IsRegular() {
			text, _ := redactedLog(source, secrets)
			if err := writeText(filepath.Join(target, name), text); err != nil {
				return "", err
			}
		}
	}
	if err := writeText(filepath.Join(target, "meta.json"), string(pyjson.DumpsIndent(clean, 2))+"\n"); err != nil {
		return "", err
	}
	return target, nil
}

// RemoveMirror is remove_mirror.
func RemoveMirror(projectPath, sessionID string) (bool, error) {
	target, err := MirrorFolder(projectPath, sessionID)
	if err != nil {
		return false, err
	}
	if fi, err := os.Stat(target); err == nil && fi.IsDir() {
		_ = os.RemoveAll(target)
		return true, nil
	}
	return false, nil
}
