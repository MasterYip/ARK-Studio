package agentdocs

import (
	"fmt"
	"strings"
)

// CheckEmbedded verifies that this binary carries the agent docs: the
// embedded set is present, holds every start-here doc and, when docsDir is
// given, equals the included files of that docs/ folder (not stale).  It is
// `ark-server docs-check` and runs in release builds.
func CheckEmbedded(docsDir string) (string, error) {
	s, ok := Embedded()
	if !ok {
		return "", fmt.Errorf("no agent docs embedded: run `go generate ./internal/agentdocs` (or ./...) before go build")
	}
	var missing []string
	for _, item := range StartHere {
		if !s.Has(item.Path) {
			missing = append(missing, item.Path)
		}
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("embedded agent docs lack start-here docs: %s", strings.Join(missing, ", "))
	}
	total := 0
	for _, d := range s.Docs {
		total += d.Bytes()
	}
	summary := fmt.Sprintf("%d embedded agent docs, %d bytes", len(s.Docs), total)
	if docsDir == "" {
		return summary, nil
	}
	disk, err := FromDir(docsDir)
	if err != nil {
		return "", err
	}
	var stale []string
	for _, d := range disk.Docs {
		if e, ok := s.byPath[d.Path]; !ok {
			stale = append(stale, d.Path+" (not embedded)")
		} else if e.Text != d.Text {
			stale = append(stale, d.Path+" (changed)")
		}
	}
	for _, e := range s.Docs {
		if !disk.Has(e.Path) {
			stale = append(stale, e.Path+" (removed)")
		}
	}
	if len(stale) > 0 {
		return "", fmt.Errorf("embedded agent docs are stale against %s: %s; re-run go generate ./internal/agentdocs",
			docsDir, strings.Join(stale, ", "))
	}
	return summary + ", identical to " + docsDir, nil
}
