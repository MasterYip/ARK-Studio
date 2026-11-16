package sessionctx

import _ "embed"

// WatcherScript is arkstudio/session_watch.py, served as
// GET /api/agent/v1/sessions/watcher.py.  The copy beside this file is
// checked out with the same line endings as the Python file (TestWatcherCopy
// keeps the two identical).
//
//go:embed session_watch.py
var WatcherScript []byte
