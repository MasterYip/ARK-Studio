package resources

import (
	"path/filepath"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

// Every placed location gets its stored size; a private device's
// resources and hidden locations never show one.
func TestGraphLocationSizes(t *testing.T) {
	checked := 0
	for _, item := range golden(t).Value("graphs").([]any) {
		c := item.(*pyjson.Object)
		setupName, ok := c.Value("setup").(string)
		if !ok {
			continue
		}
		src := c.Value("sources").(*pyjson.Object).Copy()
		dir := filepath.Join("testdata", "setup", setupName)
		layers, err := ReadLayers(dir)
		if err != nil {
			t.Fatal(err)
		}
		stats := []any{}
		records := append([]any{layers.Value("project")}, layers.Value("users").([]any)...)
		for _, r := range records {
			for _, l := range iterate(r.(*pyjson.Object).Value("locations")) {
				loc := l.(*pyjson.Object)
				stats = append(stats, pyjson.NewObject("resource", loc.Value("resource"), "host_id", loc.Value("host_id"),
					"path", loc.Value("path_or_ref"), "kind", "dir", "bytes", int64(42), "files", int64(1),
					"measured_at", "2026-11-20T00:00:00+00:00", "source", "agent"))
			}
		}
		src.Set("layers", layers).Set("history", []any{}).Set("location_stats", stats)
		g, err := BuildGraph(src, pyjson.Str(c.Value("viewer")))
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range g.Value("resources").([]any) {
			res := x.(*pyjson.Object)
			size, has := res.Get("size")
			if res.Value("redacted") == true && has {
				t.Fatalf("%s: redacted resource with size: %s", c.Value("name"), pyjson.Dumps(res))
			}
			if has {
				if size.(*pyjson.Object).Value("bytes") != int64(42) || size.(*pyjson.Object).Has("resource") {
					t.Fatalf("size: %s", pyjson.Dumps(size))
				}
				checked++
			}
		}
	}
	if checked == 0 {
		t.Fatal("no resource got a size")
	}
}
