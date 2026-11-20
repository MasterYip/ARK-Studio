package server

// T-0036: graph display defaults (project settings), personal overrides
// (prefs.appearance.graph_display) and per-project Mind layout
// (prefs.mind_layout) over HTTP.

import "testing"

func TestProjectGraphDisplaySettings(t *testing.T) {
	app, _ := activityApp(t, "Display")
	r := gpuCall(t, app, "GET", "/api/project/settings", nil, nil)
	if r.code != 200 {
		t.Fatalf("GET settings: %d %s", r.code, r.raw)
	}
	if _, has := r.body["settings"].(map[string]any)["graph_display"]; has {
		t.Error("graph_display is absent until set")
	}
	r = gpuCall(t, app, "PATCH", "/api/project/settings",
		map[string]any{"graph_display": map[string]any{"mind": map[string]any{"show_images": false, "image_size": 320}}}, nil)
	if r.code != 200 {
		t.Fatalf("PATCH: %d %s", r.code, r.raw)
	}
	mind := r.body["settings"].(map[string]any)["graph_display"].(map[string]any)["mind"].(map[string]any)
	if mind["show_images"] != false || mind["image_size"] != float64(320) {
		t.Errorf("stored %v", mind)
	}
	r = gpuCall(t, app, "GET", "/api/project/settings", nil, nil)
	if _, has := r.body["settings"].(map[string]any)["graph_display"]; !has {
		t.Error("a saved default is returned on read")
	}
	if r := gpuCall(t, app, "PATCH", "/api/project/settings",
		map[string]any{"graph_display": map[string]any{"mind": map[string]any{"align": "up"}}}, nil); r.code != 400 {
		t.Errorf("invalid value: %d %s", r.code, r.raw)
	}
	r = gpuCall(t, app, "PATCH", "/api/project/settings", map[string]any{"graph_display": nil}, nil)
	if _, has := r.body["settings"].(map[string]any)["graph_display"]; r.code != 200 || has {
		t.Errorf("null clears: %d %s", r.code, r.raw)
	}
}

func TestProfileGraphDisplayAndMindLayout(t *testing.T) {
	app, _ := activityApp(t, "Prefs")
	patch := func(prefs map[string]any) gpuReply {
		return gpuCall(t, app, "PATCH", "/api/me", map[string]any{"prefs": prefs}, nil)
	}
	r := patch(map[string]any{"appearance": map[string]any{"graph_display": map[string]any{"force": map[string]any{"marker_scale": 2}}}})
	if r.code != 200 {
		t.Fatalf("appearance: %d %s", r.code, r.raw)
	}
	prefs := r.body["profile"].(map[string]any)["prefs"].(map[string]any)
	force := prefs["appearance"].(map[string]any)["graph_display"].(map[string]any)["force"].(map[string]any)
	if force["marker_scale"] != float64(2) {
		t.Errorf("override %v", force)
	}
	if r := patch(map[string]any{"appearance": map[string]any{"graph_display": map[string]any{"force": map[string]any{"marker_scale": 9}}}}); r.code != 400 {
		t.Errorf("out of range: %d", r.code)
	}
	r = patch(map[string]any{"mind_layout": map[string]any{"prj_x": map[string]any{
		"widths":        map[string]any{"U-1": 240},
		"pinned_images": map[string]any{"U-1": map[string]any{"media_id": "m1", "side": "right", "w": 260, "h": 130}},
	}}})
	if r.code != 200 {
		t.Fatalf("mind_layout: %d %s", r.code, r.raw)
	}
	r = gpuCall(t, app, "GET", "/api/me", nil, nil)
	layout := r.body["profile"].(map[string]any)["prefs"].(map[string]any)["mind_layout"].(map[string]any)
	if layout["prj_x"].(map[string]any)["widths"].(map[string]any)["U-1"] != float64(240) {
		t.Errorf("read back %v", layout)
	}
	if r := patch(map[string]any{"mind_layout": map[string]any{"prj_x": map[string]any{"widths": map[string]any{"U-1": "wide"}}}}); r.code != 400 {
		t.Errorf("invalid width: %d", r.code)
	}
	r = patch(map[string]any{"mind_layout": nil, "appearance": map[string]any{"graph_display": nil}})
	prefs = r.body["profile"].(map[string]any)["prefs"].(map[string]any)
	if _, has := prefs["mind_layout"]; r.code != 200 || has {
		t.Errorf("null clears mind_layout: %d %s", r.code, r.raw)
	}
	if _, has := prefs["appearance"]; has {
		t.Errorf("clearing the only group removes appearance: %s", r.raw)
	}
}
