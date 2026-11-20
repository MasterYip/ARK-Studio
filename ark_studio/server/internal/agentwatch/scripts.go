package agentwatch

import _ "embed"

// The watcher scripts agents run without ark-server installed
// (GET /api/agent/v1/watcher.sh and watcher.ps1).

//go:embed scripts/watcher.sh
var WatcherSh []byte

//go:embed scripts/watcher.ps1
var WatcherPs1 []byte
