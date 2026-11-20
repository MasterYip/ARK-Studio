import assert from "node:assert/strict";
import test from "node:test";

import {
  OVERRIDE_GROUPS, normalizeOverrides, resolveAppearance, mergeOverrides,
} from "../../ark_studio/web/js/appearance-overrides.js";

const project = {
  palette: {
    enabled: true,
    light: { preset: "forest-light", background: "#f3f8f5", accent: "#047857" },
    dark: { preset: "forest", background: "#0d1714", accent: "#10b981" },
    saved: { Team: { light: { preset: "ark-light" }, dark: { preset: "ark" } } },
  },
  graph: { repulsion: 3000, distance: 10, gravity: 0.01, damping: 0.3 },
  arrows: { style: { head: "diamond", placement: "target", size: 1 }, relations: {}, mind_tree: false },
};
const myPalette = {
  enabled: true,
  light: { preset: "sunset-light", background: "#fbf6f3", accent: "#c2410c" },
  dark: { preset: "indigo", background: "#101222", accent: "#6366f1" },
};
const myGraph = { repulsion: 9000, distance: 120, gravity: 0.002, damping: 0.6 };

test("without overrides every group inherits the project value", () => {
  const effective = resolveAppearance(project, {});
  assert.deepEqual(effective.palette, project.palette);
  assert.deepEqual(effective.graph, project.graph);
  assert.deepEqual(effective.arrows, project.arrows);
  assert.deepEqual(effective.source, { palette: "project", graph: "project", arrows: "project", graph_display: "project" });
  assert.deepEqual(OVERRIDE_GROUPS, ["palette", "graph", "arrows", "graph_display"]);
});

test("an override wins for its group and the project value is not applied", () => {
  const effective = resolveAppearance(project, { palette: myPalette, graph: myGraph });
  assert.equal(effective.palette.light.accent, "#c2410c");
  assert.equal(effective.palette.dark.accent, "#6366f1");
  assert.deepEqual(effective.graph, myGraph);
  assert.deepEqual(effective.source, { palette: "personal", graph: "personal", arrows: "project", graph_display: "project" });
  // The project's saved-theme library stays available.
  assert.deepEqual(effective.palette.saved, project.palette.saved);
  // A personal "palette off" beats a project palette that is on.
  const off = resolveAppearance(project, { palette: { ...myPalette, enabled: false } });
  assert.equal(off.palette.enabled, false);
});

test("partial overrides only replace the groups the person set", () => {
  const paletteOnly = resolveAppearance(project, { palette: myPalette });
  assert.equal(paletteOnly.source.palette, "personal");
  assert.equal(paletteOnly.source.graph, "project");
  assert.deepEqual(paletteOnly.graph, project.graph);
  const graphOnly = resolveAppearance(project, { graph: myGraph });
  assert.deepEqual(graphOnly.palette, project.palette);
  assert.deepEqual(graphOnly.graph, myGraph);
});

test("a cleared override falls back to the project value", () => {
  const set = mergeOverrides({}, { palette: myPalette, graph: myGraph });
  assert.deepEqual(Object.keys(set).sort(), ["graph", "palette"]);
  const cleared = mergeOverrides(set, { palette: null });
  assert.deepEqual(Object.keys(cleared), ["graph"]);
  const effective = resolveAppearance(project, cleared);
  assert.deepEqual(effective.palette, project.palette);
  assert.deepEqual(effective.graph, myGraph);
  assert.deepEqual(mergeOverrides(cleared, { graph: null }), {});
});

test("malformed overrides count as not set", () => {
  assert.deepEqual(normalizeOverrides({ palette: { light: {} }, graph: { repulsion: 1 }, other: 1 }), {});
  assert.deepEqual(normalizeOverrides(null), {});
  const effective = resolveAppearance(project, { graph: { repulsion: "x", distance: 1, gravity: 0, damping: 0 } });
  assert.equal(effective.source.graph, "project");
  assert.deepEqual(resolveAppearance(null, { graph: myGraph }).graph, myGraph);
});

test("a personal arrow override wins over the project line ends", () => {
  const mine = { style: { head: "open", placement: "both", size: 1.5 }, relations: { part_of: { placement: "none" } } };
  const effective = resolveAppearance(project, { arrows: mine });
  assert.equal(effective.source.arrows, "personal");
  assert.deepEqual(effective.arrows, { ...mine, mind_tree: false });
  // An incomplete personal style is not an override.
  assert.equal(resolveAppearance(project, { arrows: { style: { head: "open" } } }).source.arrows, "project");
  assert.deepEqual(mergeOverrides({ arrows: mine }, { arrows: null }), {});
});
