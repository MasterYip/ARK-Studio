package sessions

// Native forks ("Fork in new tab"): the provider copies the parent's own
// conversation into a new session, so the fork starts with the full
// context, not a summary.
//
//	Claude Code: claude -p --resume <id> --fork-session …
//	Codex:       codex exec fork <id> <prompt>   (a prompt is required)
//
// Other providers, or a parent without a native session id, get the seeded
// fork (forkContext: a summary of the parent and the path to its log).

const nativeForkPrompt = "Continue from here."

// forkArgv rewrites a launch command to fork native session `nativeID`.
// ok is false when the provider cannot fork natively.
func forkArgv(argv []string, platform, nativeID, prompt string) ([]string, bool) {
	argv = append([]string(nil), argv...)
	switch platform {
	case "claude":
		if contains(argv, "--resume") || contains(argv, "-r") || len(argv) < 1 {
			return nil, false
		}
		return insertAt(argv, 1, "--resume", nativeID, "--fork-session"), true
	case "codex":
		if !contains(argv, "exec") || contains(argv, "resume") || contains(argv, "fork") {
			return nil, false
		}
		at := len(argv)
		if prompt != "" && contains(argv, prompt) {
			at = indexOf(argv, prompt)
		}
		return insertAt(argv, at, "fork", nativeID), true
	}
	return nil, false
}

// canForkNatively reports a parent the provider can fork with its own history.
func (m *Manager) canForkNatively(parent *Session, platform string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	native := strOr(parent.Meta.Value("native_session_id"))
	if native == "" || platform != strOr(parent.Meta.Value("platform")) {
		return "", false
	}
	return native, platform == "claude" || platform == "codex"
}
