package gpu

import (
	"path/filepath"
	"strings"
	"testing"
)

// The public example server list (docs/operations/gpu_servers.example.yaml)
// parses, stays disabled and names only reserved .invalid hosts.
func TestPublicExampleConfig(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join("..", "..", "..", "..", "docs", "operations", "gpu_servers.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Value("enabled") != false {
		t.Fatalf("example must stay disabled: enabled=%v", cfg.Value("enabled"))
	}
	servers, _ := cfg.Value("servers").([]any)
	if len(servers) == 0 {
		t.Fatal("no servers in the example")
	}
	for _, s := range servers {
		host, _ := s.(interface{ Value(string) any }).Value("host").(string)
		if !strings.HasSuffix(host, ".invalid") {
			t.Errorf("example host %q is not a reserved .invalid name", host)
		}
	}
}
