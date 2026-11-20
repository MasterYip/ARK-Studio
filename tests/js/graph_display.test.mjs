// T-0036: graph display settings by graph type (classification, migration
// from browser storage, project default + personal override resolution) and
// the per-project Mind node state.
// Run: node --test tests/js/graph_display.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  GRAPH_DISPLAY_TABS, GRAPH_DISPLAY_DEFAULTS, GRAPH_STORAGE_CLASSES, LEGACY_MIND_KEY, normalizeGraphDisplay,
  resolveGraphDisplay, setGraphDisplayValue, clearGraphDisplayTab, toMindSettings, legacyMindOverride, migrateLegacyMind,
} from "../../ark_studio/web/js/graph-display.js";
import { resolveAppearance, mergeOverrides } from "../../ark_studio/web/js/appearance-overrides.js";
import {
  adoptLegacy, createMindNodeStore, toServerEntry, fromServerEntry, normalizeMindNodeState,
  LEGACY_WIDTHS_KEY, LEGACY_PINNED_KEY, RETIRED_KEYS,
} from "../../ark_studio/web/js/mind-node-state.js";
import { loadLocalOptions, storableOptions } from "../../ark_studio/web/js/local-graph-model.js";

const repo = new URL("../../", import.meta.url);

// Every value the old "ark.mind.v1" record held (mind-settings.js before T-0036).
const OLD_MIND_KEYS = ["align", "autoLayout", "markerScale", "markerGap", "markerArc", "mindMarkerScale",
  "showMindImages", "nodeTransitions"];

test("classification: every old browser setting has a home", () => {
  const legacy = GRAPH_DISPLAY_TABS.flatMap((tab) => tab.fields.filter((f) => f.legacy).map((f) => f.legacy));
  assert.deepEqual([...legacy].sort(), [...OLD_MIND_KEYS].sort(), "each ark.mind.v1 value maps to one display setting");
  assert.equal(GRAPH_STORAGE_CLASSES[LEGACY_MIND_KEY].category, "a");
  assert.equal(GRAPH_STORAGE_CLASSES[LEGACY_WIDTHS_KEY].category, "b");
  assert.equal(GRAPH_STORAGE_CLASSES[LEGACY_PINNED_KEY].category, "b");
  for (const key of RETIRED_KEYS) assert.equal(GRAPH_STORAGE_CLASSES[key].category, "retired", key);
  for (const key of ["ark.graphHeight", "ark.infoHubPath", "ark.remote*"]) assert.equal(GRAPH_STORAGE_CLASSES[key].category, "c", key);
  for (const entry of Object.values(GRAPH_STORAGE_CLASSES)) assert.ok(["a", "b", "c", "retired"].includes(entry.category));
  // The camelCase view keeps the old names, so graph.js reads the same fields.
  const view = toMindSettings(GRAPH_DISPLAY_DEFAULTS);
  for (const key of OLD_MIND_KEYS) assert.ok(key in view, key);
  assert.deepEqual(
    { ...view, imageSize: undefined, localDepth: undefined, localRings: undefined, agentPresence: undefined },
    { align: "left", autoLayout: true, markerScale: 1.35, markerGap: 0.55, markerArc: 0.4, mindMarkerScale: 0.7,
      showMindImages: true, nodeTransitions: true, imageSize: undefined, localDepth: undefined, localRings: undefined,
      agentPresence: undefined },
    "built-in defaults are the old MIND_DEFAULTS");
  // T-0043: agent activity on the graphs is on by default.
  assert.equal(view.agentPresence, true);
});

test("the client table matches the Go validation (graphdisplay.go)", async () => {
  const go = await readFile(new URL("ark_studio/server/internal/settings/graphdisplay.go", repo), "utf8");
  for (const tab of GRAPH_DISPLAY_TABS) {
    assert.ok(go.includes(`{"${tab.id}", []gdField{`), tab.id);
    for (const field of tab.fields) {
      const kind = { bool: "gdBool", number: "gdNumber", int: "gdInt", choice: "gdChoice" }[field.kind];
      const range = field.min !== undefined ? `, min: ${field.min}, max: ${field.max}` : "";
      const choices = field.choices ? `, choices: []string{${field.choices.map((c) => `"${c}"`).join(", ")}}` : "";
      assert.ok(go.includes(`{key: "${field.key}", kind: ${kind}${range}${choices}}`), `${tab.id}.${field.key}`);
      assert.equal(field.default, GRAPH_DISPLAY_DEFAULTS[tab.id][field.key]);
    }
  }
});

test("normalize keeps a sparse, valid override", () => {
  assert.deepEqual(normalizeGraphDisplay({
    mind: { align: "center", show_images: "no", image_size: 300, depth: 2 }, force: {}, tree: { a: 1 },
    local: { depth: 2.5, rings: false },
  }), { mind: { align: "center", image_size: 300 }, local: { rings: false } });
  assert.deepEqual(normalizeGraphDisplay(null), {});
  assert.deepEqual(normalizeGraphDisplay([]), {});
});

test("resolution is per setting: personal, then project, then built-in", () => {
  const project = { mind: { show_images: false, align: "center" }, local: { depth: 3 } };
  const personal = { mind: { align: "left" } };
  const { values, source } = resolveGraphDisplay(project, personal);
  assert.equal(values.mind.align, "left");
  assert.equal(source.mind.align, "personal");
  assert.equal(values.mind.show_images, false);
  assert.equal(source.mind.show_images, "project");
  assert.equal(values.local.depth, 3);
  assert.equal(values.mind.auto_layout, true);
  assert.equal(source.mind.auto_layout, "default");
  // Without a project default the built-in value applies to everyone.
  assert.deepEqual(resolveGraphDisplay(null, null).values, JSON.parse(JSON.stringify(GRAPH_DISPLAY_DEFAULTS)));
});

test("a project default reaches everyone who has not overridden it", () => {
  const project = { graph_display: { force: { marker_scale: 2 } } };
  const other = resolveAppearance(project, {});
  assert.equal(other.graph_display.values.force.marker_scale, 2);
  assert.equal(other.source.graph_display, "project");
  const me = resolveAppearance(project, { graph_display: { force: { marker_scale: 0.8 } } });
  assert.equal(me.graph_display.values.force.marker_scale, 0.8);
  assert.equal(me.source.graph_display, "personal");
  // Other groups are unaffected.
  assert.equal(me.source.graph, "project");
});

test("reset to project default: per setting and per tab", () => {
  let mine = setGraphDisplayValue({}, "mind", "align", "center");
  mine = setGraphDisplayValue(mine, "mind", "auto_layout", false);
  mine = setGraphDisplayValue(mine, "local", "rings", false);
  assert.deepEqual(mine, { mind: { align: "center", auto_layout: false }, local: { rings: false } });
  mine = setGraphDisplayValue(mine, "mind", "align", null);
  assert.deepEqual(mine, { mind: { auto_layout: false }, local: { rings: false } });
  mine = clearGraphDisplayTab(mine, "mind");
  assert.deepEqual(mine, { local: { rings: false } });
  // Invalid values and unknown settings do not become overrides.
  assert.deepEqual(setGraphDisplayValue({}, "mind", "align", "up"), {});
  assert.deepEqual(setGraphDisplayValue({}, "mind", "zoom", 1), {});
  // The account store: an empty group clears it.
  assert.deepEqual(mergeOverrides({ graph_display: { local: { rings: false } } }, { graph_display: {} }), {});
  assert.deepEqual(mergeOverrides({}, { graph_display: mine }), { graph_display: mine });
});

test("migration keeps only what the person changed", () => {
  // setMindSettings always wrote the full record.
  const raw = JSON.stringify({ align: "center", autoLayout: true, markerScale: 1.35, markerGap: 1.2, markerArc: 0.4,
    mindMarkerScale: 0.7, showMindImages: false, nodeTransitions: true });
  assert.deepEqual(legacyMindOverride(raw), {
    force: { marker_gap: 1.2 }, mind: { align: "center", show_images: false },
  });
  // Clamped like the old reader; junk and defaults give nothing.
  assert.deepEqual(legacyMindOverride(JSON.stringify({ markerScale: 9, align: "weird" })), { force: { marker_scale: 2.5 } });
  assert.deepEqual(legacyMindOverride("not json"), {});
  assert.deepEqual(legacyMindOverride(JSON.stringify(toMindSettings(GRAPH_DISPLAY_DEFAULTS))), {});
});

test("migration: the account wins, and the look does not change", () => {
  const raw = JSON.stringify({ align: "center", showMindImages: false, nodeTransitions: false });
  const account = { mind: { align: "left" } };
  const { override, changed } = migrateLegacyMind(raw, account);
  assert.equal(changed, true);
  assert.deepEqual(override, { general: { transitions: false }, mind: { align: "left", show_images: false } });
  // Nothing new: no save needed.
  assert.equal(migrateLegacyMind(JSON.stringify({ align: "left" }), account).changed, false);
  // Before the move no project had defaults, so the migrated look equals the old one.
  const before = toMindSettings(resolveGraphDisplay(null, legacyMindOverride(raw)).values);
  assert.equal(before.align, "center");
  assert.equal(before.showMindImages, false);
  assert.equal(before.nodeTransitions, false);
});

test("Mind node state: server shape, bounds and lazy adoption of the old maps", () => {
  const state = normalizeMindNodeState({
    widths: { "U-1": 240.4, "U-2": 20, "": 300 },
    pinned: { "U-1": { mediaId: 7, side: "middle", w: 260, h: 130, aspect: 2 }, "U-3": { mediaId: null, w: 1, h: 1 } },
  });
  assert.deepEqual(state, { widths: { "U-1": 240 }, pinned: { "U-1": { mediaId: 7, side: "right", w: 260, h: 130, aspect: 2 } } });
  const entry = toServerEntry(state);
  assert.deepEqual(entry, { widths: { "U-1": 240 }, pinned_images: { "U-1": { media_id: 7, side: "right", w: 260, h: 130, aspect: 2 } } });
  assert.deepEqual(fromServerEntry(entry), state);
  assert.equal(toServerEntry({}), null);

  const legacy = { widths: { "U-1": 300, "U-9": 200, "U-5": 100 }, pinned: { "U-9": { mediaId: "m", w: 50, h: 50 } } };
  const moved = adoptLegacy({ widths: { "U-1": 250 } }, legacy, ["U-1", "U-9", "U-5"]);
  assert.equal(moved.changed, true);
  // The project's own value wins; widths under 126 px were ignored before and stay so.
  assert.deepEqual(moved.state.widths, { "U-1": 250, "U-9": 200 });
  assert.deepEqual(moved.state.pinned, { "U-9": { mediaId: "m", side: "right", w: 50, h: 50 } });
  assert.deepEqual(moved.legacy, { widths: {}, pinned: {} });
});

test("Mind node store: per project, saved once per change, old keys cleaned up", async () => {
  const storage = new Map([
    [LEGACY_WIDTHS_KEY, JSON.stringify({ "U-1": 300, "U-2": 200 })],
    ["ark.mindImageSizes", "{}"], ["ark.mindPinnedMedia", "[]"],
  ]);
  const local = { getItem: (k) => storage.get(k) ?? null, setItem: (k, v) => storage.set(k, v), removeItem: (k) => storage.delete(k) };
  const saved = [];
  let project = "p1";
  const server = { p2: { widths: { "U-7": 222 } } };
  const store = createMindNodeStore({
    projectId: () => project, loadEntry: (id) => server[id] || null,
    saveEntry: (id, value) => saved.push([id, value]), storage: local, delay: 0,
  });
  for (const key of RETIRED_KEYS) assert.equal(storage.has(key), false, key);
  // p1 shows U-1 only: it is adopted, U-2 waits for its project.
  assert.deepEqual(store.get(["U-1"]).widths, { "U-1": 300 });
  assert.deepEqual(JSON.parse(storage.get(LEGACY_WIDTHS_KEY)), { "U-2": 200 });
  store.update({ widths: { "U-3": 180 }, pinned: { "U-1": { mediaId: 1, side: "left", w: 100, h: 50 } } });
  await new Promise((resolve) => setTimeout(resolve, 5));
  assert.deepEqual(saved, [["p1", {
    widths: { "U-1": 300, "U-3": 180 }, pinned_images: { "U-1": { media_id: 1, side: "left", w: 100, h: 50 } },
  }]]);
  // Another project has its own state.
  project = "p2";
  assert.deepEqual(store.get(["U-7"]).widths, { "U-7": 222 });
  store.update({ widths: { "U-7": null } });
  await new Promise((resolve) => setTimeout(resolve, 5));
  assert.deepEqual(saved.at(-1), ["p2", null]);
  // Adopting a fresher profile sends a pending change first, never an empty entry.
  const slow = createMindNodeStore({ projectId: () => "p3", loadEntry: () => null,
    saveEntry: (id, value) => saved.push([id, value]), storage: null, delay: 60_000 });
  slow.update({ widths: { "U-4": 300 } });
  slow.reset();
  assert.deepEqual(saved.at(-1), ["p3", { widths: { "U-4": 300 } }]);
  slow.reset();
  assert.equal(saved.at(-1)[0], "p3");
  assert.deepEqual(slow.get(["U-4"]).widths, {});
});

test("Local graph: the default depth applies until a depth is picked", () => {
  // Old records stored depth 2 (the old default) always: that reads as not chosen.
  let loaded = loadLocalOptions(null, JSON.stringify({ depth: 2, layout: "mind" }), null, 3);
  assert.equal(loaded.options.depth, 3);
  assert.equal(loaded.depthChosen, false);
  assert.equal(loaded.options.layout, "mind");
  loaded = loadLocalOptions(null, JSON.stringify({ depth: 1 }), null, 3);
  assert.equal(loaded.options.depth, 1);
  assert.equal(loaded.depthChosen, true);
  // v3 stores depth only when chosen.
  assert.equal("depth" in storableOptions({ depth: 3, direction: "both" }, false), false);
  assert.equal(storableOptions({ depth: 1 }, true).depth, 1);
  loaded = loadLocalOptions(JSON.stringify({ direction: "out" }), JSON.stringify({ depth: 1 }), null, 2);
  assert.deepEqual([loaded.options.depth, loaded.options.direction, loaded.depthChosen], [2, "out", false]);
  loaded = loadLocalOptions(JSON.stringify({ depth: 3 }), null, null, 2);
  assert.deepEqual([loaded.options.depth, loaded.depthChosen], [3, true]);
});
