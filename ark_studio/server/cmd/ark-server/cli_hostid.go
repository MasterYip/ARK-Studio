package main

import (
	"encoding/json"
	"fmt"
	"io"

	"arkstudio/go/internal/hostid"
)

// agentHostID is `ark-server agent host-id [--json]`: the id an agent on
// this device sends as X-ARK-Host.
func agentHostID(argv []string, stdout io.Writer) int {
	id := hostid.Compute()
	if id.ID == "" {
		fmt.Fprintln(stdout, "no machine id and no physical network adapter found")
		return 1
	}
	if len(argv) > 0 && argv[0] == "--json" {
		b, _ := json.Marshal(id)
		fmt.Fprintln(stdout, string(b))
		return 0
	}
	fmt.Fprintln(stdout, id.ID)
	return 0
}
