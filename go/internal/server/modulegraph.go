package server

import (
	"bytes"
	"compress/gzip"
	"path"
	"strings"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pyre"
	"arkstudio/go/internal/pyurl"
	"arkstudio/go/internal/webui"
)

// Main a765ee8 (cold start): index.html lists the static import graph of
// the app entry module (server.APP_ENTRY, static_assets.module_graph).

const (
	appEntry    = "js/app.js"
	preloadMark = "<!-- ark:modulepreload -->"
)

var (
	staticImportRe = pyre.MustCompile(`(?:^|[;\n}])\s*(?:import|export)\s+(?:[^"'();]*?\s+from\s+)?["'](\.{1,2}/[^"'?]+?\.js)["']`, pyre.FlagMultiline)
	blockCommentRe = pyre.MustCompile(`/\*.*?\*/`, pyre.FlagDotAll)
)

// staticImports is static_assets.static_imports.
func staticImports(source string) []string {
	runes := []rune(source)
	budget := 1 << 30
	comments, _ := blockCommentRe.FindAll(runes, &budget)
	if len(comments) > 0 {
		var b []rune
		last := 0
		for _, m := range comments {
			b = append(b, runes[last:m.Start]...)
			last = m.End
		}
		runes = append(b, runes[last:]...)
	}
	matches, _ := staticImportRe.FindAll(runes, &budget)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if s, e, ok := m.Group(1); ok {
			out = append(out, string(runes[s:e]))
		}
	}
	return out
}

// moduleGraph is static_assets.module_graph(web_root, entry): every module
// entry imports statically, breadth first, entry excluded.
func moduleGraph(assets *webui.Assets, entry string) []string {
	var order []string
	seen := map[string]bool{entry: true}
	queue := []string{entry}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		raw, err := assets.Read(current)
		if err != nil {
			continue
		}
		source := universalNewlines(pyurl.DecodeReplace(raw))
		base := path.Dir(current)
		if base == "." {
			base = ""
		}
		for _, spec := range staticImports(source) {
			joined := spec
			if base != "" {
				joined = base + "/" + spec
			}
			rel := posixNormpath(joined)
			if strings.HasPrefix(rel, "..") || seen[rel] {
				continue
			}
			if info, err := assets.Stat(rel); err != nil || info.IsDir() {
				continue
			}
			seen[rel] = true
			order = append(order, rel)
			queue = append(queue, rel)
		}
	}
	return order
}

// indexHTML is server._index_html.
func indexHTML(assets *webui.Assets) func(string, string) string {
	return func(text, version string) string {
		text = VersionedHTML(text, version)
		if !strings.Contains(text, preloadMark) {
			return text
		}
		mods := []any{appEntry}
		for _, m := range moduleGraph(assets, appEntry) {
			mods = append(mods, m)
		}
		return strings.Replace(text, preloadMark,
			`<script type="application/json" id="ark-modules">`+string(pyjson.DumpsASCII(mods))+`</script>`, 1)
	}
}

// GzipBest is gzip.compress(data, compresslevel=9, mtime=0): static files
// are compressed once per release at the best level (main a765ee8).
func GzipBest(data []byte) []byte {
	var buf bytes.Buffer
	w, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	w.Write(data) //nolint:errcheck
	w.Close()
	return buf.Bytes()
}
