package main

import (
	"fmt"
	"os"

	"arkstudio/go/internal/agentdocs"
)

// docsCheck is `ark-server docs-check [--docs DIR]` (Go only): exit 1 unless
// the binary embeds the agent docs (GET /api/agent/v1/docs), and with --docs
// unless they equal the included files of that docs/ folder.
func docsCheck(argv []string) int {
	dir := ""
	for i := 0; i < len(argv); i++ {
		switch {
		case argv[i] == "--docs" && i+1 < len(argv):
			dir = argv[i+1]
			i++
		default:
			fmt.Fprintln(os.Stderr, "usage: ark-server docs-check [--docs DIR]")
			return 2
		}
	}
	summary, err := agentdocs.CheckEmbedded(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ark-server docs-check:", err)
		return 1
	}
	fmt.Println("ark-server docs-check:", summary)
	return 0
}
