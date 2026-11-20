// Package agentdocs serves ARK's Markdown documentation to agents
// (GET /api/agent/v1/docs and /api/agent/v1/docs/{path}, Go only).
//
// Release builds embed the selected docs/ files: `go generate ./...` runs
// cmd/docsgen, which copies them into internal/agentdocs/dist together with
// a manifest of their last-change dates.  Without that step only dist/.keep
// is embedded and the docs are read from the repository's docs/ folder on
// disk (next to the web/ folder the server serves, or found in a parent of
// the working directory / the executable), so edits show up without a
// rebuild in development.
//
// Which files agents get is decided here, in one place, for both sources:
//
//   - every *.md directly in docs/guide, docs/agents and docs/operations;
//   - from docs/development only design.md (architecture),
//     development.md (running and testing ARK) and agent-messaging.md
//     (the update feed and experimental messages);
//   - never: docs/development/archive, docs/development/go-port (port
//     bookkeeping, contract.json), the performance investigations,
//     release.md, docs/screenshots, docs/README.md (the index endpoint
//     replaces it), agents/skills-attribution.md (licence provenance) and
//     operations/public-export.md (maintainer-only publishing script).
package agentdocs

import (
	"path"
	"strings"
)

// Sections are the docs/ sub-folders agents can read, in index order.
var Sections = []string{"agents", "guide", "operations", "development"}

// sectionWide lists the sections whose top-level *.md files are all included.
var sectionWide = map[string]bool{"agents": true, "guide": true, "operations": true}

// extraFiles are single files included from the other sections.
var extraFiles = map[string]bool{
	"development/design.md":          true,
	"development/development.md":     true,
	"development/agent-messaging.md": true,
}

// excludedFiles are left out of otherwise included sections.
var excludedFiles = map[string]bool{
	"agents/skills-attribution.md": true,
	"operations/public-export.md":  true,
}

// Included reports whether a docs/-relative slash path is served to agents.
func Included(rel string) bool {
	if !strings.HasSuffix(rel, ".md") || excludedFiles[rel] {
		return false
	}
	if extraFiles[rel] {
		return true
	}
	dir, file := path.Split(rel)
	return file != "" && sectionWide[strings.TrimSuffix(dir, "/")]
}

// StartHere is the suggested reading order for an agent new to ARK.
var StartHere = []struct{ Path, Why string }{
	{"agents/agent-interface.md", "The contract: discovery, setup gate, tokens, task manifests, evidence and conformance levels."},
	{"agents/external-agent.md", "How an external coordinator (Claude Code, Codex, DSH) reads and writes an ARK project."},
	{"agents/working-mode.md", "The recommended working mode: plan in a task node, progress lines, review nodes with media, decision nodes, watching for replies."},
	{"agents/skills.md", "The setup, agent-team, rrsi and design skills and their HTTP endpoints."},
	{"guide/home-overview.md", "What the Home screen shows a person, and the starter prompts."},
	{"guide/topics.md", "Topics: curated subgraphs that tell one storyline."},
	{"guide/node-history.md", "Opt-in Git history of node text: read, diff, restore."},
	{"agents/session-context.md", "Uploading and reading agent session transcripts."},
	{"operations/gpu.md", "GPU servers, the job queue and the dispatch rule."},
	{"operations/multi-user-setup.md", "The shared project setup register (repositories, compute, commands)."},
}
