// Run: node --experimental-vm-modules --test tests/js/workbench_model.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

async function loadModel() {
  const source = await readFile(new URL("../../ark_studio/web/js/workbench-model.js", import.meta.url), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: "workbench-model.js" });
  await module.link(() => { throw new Error("workbench-model must stay dependency-free"); });
  await module.evaluate();
  return module.namespace;
}

const mod = await loadModel();
// The layout mechanics below use the built-in views without the session views
// (Sessions, Session properties; session_sidebar.test.mjs tests those) and
// the Topics view (tested at the end of this file).
const LEGACY = mod.VIEW_DEFAULTS.filter((view) => !["sessions", "sessionprops", "topics"].includes(view.id));
const m = {
  ...mod,
  VIEW_DEFAULTS: LEGACY,
  createWorkbench: (views = LEGACY) => mod.createWorkbench(views),
  normalizeWorkbench: (raw, views = LEGACY) => mod.normalizeWorkbench(raw, views),
  parseWorkbench: (text, legacy = {}, views = LEGACY) => mod.parseWorkbench(text, legacy, views),
  migrateLegacy: (legacy = {}, views = LEGACY) => mod.migrateLegacy(legacy, views),
  builtinPresetLayout: (id, current = null, views = LEGACY) => mod.builtinPresetLayout(id, current, views),
  normalizePresetStore: (raw, views = LEGACY) => mod.normalizePresetStore(raw, views),
  presetLayout: (id, store, current = null, views = LEGACY) => mod.presetLayout(id, store, current, views),
  defaultLayout: (store, current = null, views = LEGACY) => mod.defaultLayout(store, current, views),
  storableLayout: (layout, views = LEGACY) => mod.storableLayout(layout, views),
  withCurrentLayout: (store, layout, views = LEGACY, at = 0) => mod.withCurrentLayout(store, layout, views, at),
};
const views = (layout, name) => layout.containers[name].views;
// The built-in views before the Sessions sidebar view: the section mechanics
// tests below were written against this set.

test("default placement: filters/explorer/changes left, details right, assistant/terminal bottom (hidden)", () => {
  const layout = m.createWorkbench();
  assert.deepEqual(views(layout, "left"), ["filters", "explorer", "search", "changes"]);
  assert.deepEqual(views(layout, "right"), ["details", "localgraph"]);
  assert.deepEqual(views(layout, "bottom"), ["assistant", "terminal"]);
  assert.equal(layout.containers.left.visible, true);
  assert.equal(layout.containers.right.visible, true);
  assert.equal(layout.containers.bottom.visible, false);
  assert.equal(layout.containers.left.active, "filters");
});

test("move a view left -> right -> bottom and back", () => {
  let layout = m.createWorkbench();
  layout = m.moveView(layout, "explorer", "right");
  assert.deepEqual(views(layout, "left"), ["filters", "search", "changes"]);
  assert.deepEqual(views(layout, "right"), ["details", "localgraph", "explorer"]);
  assert.equal(layout.containers.right.active, "explorer");
  layout = m.moveView(layout, "explorer", "bottom", 0);
  assert.deepEqual(views(layout, "bottom"), ["explorer", "assistant", "terminal"]);
  assert.equal(layout.containers.bottom.visible, true, "moving into a hidden panel shows it");
  assert.equal(layout.containers.right.active, "details", "old container falls back to a remaining view");
  layout = m.moveView(layout, "explorer", "left", 0);
  assert.deepEqual(views(layout, "left"), ["explorer", "filters", "search", "changes"]);
  assert.equal(m.containerOf(layout, "explorer"), "left");
  // Reorder within the same container.
  layout = m.moveView(layout, "explorer", "left");
  assert.deepEqual(views(layout, "left"), ["filters", "search", "changes", "explorer"]);
  // Unknown target or view is a no-op.
  assert.equal(m.moveView(layout, "nope", "left"), layout);
  assert.equal(m.moveView(layout, "explorer", "top"), layout);
});

test("hide/show, activate, size clamp and maximize", () => {
  let layout = m.createWorkbench();
  layout = m.toggleContainer(layout, "left");
  assert.equal(layout.containers.left.visible, false);
  layout = m.activateView(layout, "explorer");
  assert.equal(layout.containers.left.visible, true);
  assert.equal(layout.containers.left.active, "explorer");
  layout = m.setContainerSize(layout, "right", 5000);
  assert.equal(layout.containers.right.size, m.SIZE_LIMITS.right[1]);
  layout = m.setContainerSize(layout, "bottom", 10);
  assert.equal(layout.containers.bottom.size, m.SIZE_LIMITS.bottom[0]);
  layout = m.toggleMaximized(layout, "bottom");
  assert.equal(layout.containers.bottom.maximized, true);
  assert.equal(layout.containers.bottom.visible, true);
});

test("views that do not apply to a workspace are hidden; empty containers collapse", () => {
  const layout = m.createWorkbench();
  const editor = (id) => id !== "filters";
  const home = (id) => id === "details" || id === "assistant";
  assert.equal(m.displayedView(layout, "bottom", home), "assistant");
  assert.equal(m.displayedView(layout, "left", editor), "explorer", "Filters active but n/a -> Explorer shown");
  assert.deepEqual(m.applicableViews(layout, "left", editor), ["explorer", "search", "changes"]);
  assert.equal(m.containerShown(layout, "left", home), false);
  assert.equal(m.containerShown(layout, "right", home), true);
  assert.equal(m.isViewShown(layout, "explorer", editor), true);
  assert.equal(m.isViewShown(layout, "filters", editor), false);
});

test("normalize repairs duplicates, unknown and missing views", () => {
  const layout = m.normalizeWorkbench({ v: 1, containers: {
    left: { views: ["explorer", "explorer", "bogus"], active: "bogus", visible: false, size: 9999 },
    right: { views: ["filters"], active: "filters", visible: true, size: 400 },
  } });
  assert.deepEqual(views(layout, "left"), ["explorer", "search", "changes"]);
  assert.equal(layout.containers.left.active, "explorer");
  assert.equal(layout.containers.left.visible, false);
  assert.equal(layout.containers.left.size, m.SIZE_LIMITS.left[1]);
  assert.deepEqual(views(layout, "right"), ["filters", "details", "localgraph"], "missing view returns to its default container");
  assert.deepEqual(views(layout, "bottom"), ["assistant", "terminal"]);
});

test("persistence round-trip; nothing stored gives the Default preset", () => {
  let layout = m.moveView(m.createWorkbench(), "assistant", "right");
  const restored = m.parseWorkbench(m.serializeWorkbench(layout));
  assert.deepEqual(restored, m.normalizeWorkbench(layout));
  // Nothing stored: the Default preset, whatever old panel keys say (they
  // are still written by the panel code and are not a sign of a layout).
  const ignored = m.parseWorkbench(null, { filtersVisible: "0", inspectorPane: "1", sessionDock: "1", railWidth: "320" });
  assert.deepEqual(ignored, m.builtinPresetLayout("default"));
  // Fresh browser (nothing stored): defaults.
  // First run: the Default preset (the maintainer's MY_default arrangement).
  const fresh = m.parseWorkbench("not json", {});
  assert.deepEqual(fresh, m.builtinPresetLayout("default"));
});

test("built-in presets: default, focus keeps placement, research/writing/review", () => {
  const moved = m.moveView(m.createWorkbench(), "terminal", "right");
  const def = m.builtinPresetLayout("default");
  assert.deepEqual(def.containers.left.views, ["filters", "explorer", "search", "changes", "assistant", "localgraph"]);
  assert.equal(def.containers.left.active, "localgraph");
  assert.deepEqual(def.containers.right.views, ["details"]);
  assert.deepEqual(def.containers.bottom.views, ["terminal"]);
  assert.equal(def.containers.bottom.visible, false);
  assert.deepEqual([def.containers.left.size, def.containers.right.size], [269, 354]);
  const focus = m.builtinPresetLayout("focus", m.toggleMaximized(moved, "bottom"));
  assert.deepEqual(focus.containers.right.views, moved.containers.right.views, "focus keeps the placement");
  assert.ok(m.CONTAINERS.every((name) => !focus.containers[name].visible));
  assert.equal(focus.containers.bottom.maximized, undefined);
  const research = m.builtinPresetLayout("research");
  assert.equal(research.containers.left.active, "filters");
  assert.deepEqual(research.containers.right.views, ["details", "localgraph"]);
  const writing = m.builtinPresetLayout("writing");
  assert.equal(writing.containers.left.active, "explorer");
  assert.equal(writing.containers.right.active, "localgraph");
  const review = m.builtinPresetLayout("review");
  assert.equal(review.containers.left.active, "changes");
  assert.equal(review.containers.bottom.active, "terminal");
  assert.equal(review.containers.bottom.visible, true);
  for (const id of m.BUILTIN_PRESETS) {
    const layout = m.builtinPresetLayout(id, moved);
    const all = m.CONTAINERS.flatMap((name) => layout.containers[name].views).sort();
    assert.deepEqual(all, m.VIEW_DEFAULTS.map((view) => view.id).sort(), `${id} places every view once`);
  }
  assert.equal(m.builtinPresetLayout("nope"), null);
});

test("user presets: save, overwrite by name, rename, delete, default, reset", () => {
  let store = { presets: [], default: "" };
  const custom = m.toggleMaximized(m.setContainerSize(m.moveView(m.createWorkbench(), "search", "right"), "right", 512.7), "bottom");
  let saved = m.savePreset(store, "  My review ", custom);
  store = saved.store;
  assert.equal(saved.id, "u:my-review");
  assert.equal(store.presets[0].name, "My review");
  assert.equal(store.presets[0].layout.containers.right.size, 513);
  assert.equal(store.presets[0].layout.containers.bottom.maximized, true);
  const applied = m.presetLayout("u:my-review", store);
  assert.deepEqual(applied.containers.right.views, ["details", "localgraph", "search"]);
  assert.equal(applied.containers.bottom.maximized, true);
  saved = m.savePreset(store, "my review", m.createWorkbench());
  assert.equal(saved.id, "u:my-review");
  assert.equal(saved.store.presets.length, 1);
  saved = m.savePreset(saved.store, "My-review!", m.createWorkbench());
  assert.equal(saved.id, "u:my-review-2");
  store = m.renamePreset(saved.store, "u:my-review-2", "Second");
  assert.equal(store.presets[1].name, "Second");
  store = m.savePreset(store, "Again", custom).store;
  store = m.setDefaultPreset(store, "u:again");
  assert.equal(store.default, "u:again");
  assert.deepEqual(m.defaultLayout(store).containers.right.views, ["details", "localgraph", "search"]);
  store = m.deletePreset(store, "u:again");
  assert.equal(store.default, "", "deleting the default preset falls back to Default");
  assert.deepEqual(m.defaultLayout(store), m.builtinPresetLayout("default"));
  assert.equal(m.setDefaultPreset(store, "bogus").default, "");
  assert.equal(m.setDefaultPreset(store, "focus").default, "focus");
  assert.deepEqual(m.normalizePresetStore({ presets: [{ id: "x", name: "a", layout: {} }], default: "zzz" }), { presets: [], default: "" });
  assert.equal(m.savePreset(store, "   ", custom).id, "");
});

test("maximized panel and activity selection survive a save/restore", () => {
  let layout = m.activateView(m.createWorkbench(), "search");
  layout = m.toggleMaximized(layout, "bottom");
  const restored = m.parseWorkbench(m.serializeWorkbench(layout));
  assert.equal(restored.containers.left.active, "search");
  assert.equal(restored.containers.bottom.maximized, true);
});

const sec = (layout, name) => layout.containers[name].sections.map((s) => s.views);

test("sections: migration from layouts saved before sections", () => {
  const old = { v: 1, containers: {
    left: { views: ["explorer", "search"], active: "search", visible: true, size: 300 },
    right: { views: ["details", "localgraph", "assistant"], active: "localgraph", visible: true, size: 500 },
    bottom: { views: ["terminal"], active: "terminal", visible: false, size: 200, maximized: true },
  } };
  const layout = m.normalizeWorkbench(old);
  assert.deepEqual(sec(layout, "right"), [["details", "localgraph", "assistant"]]);
  assert.equal(layout.containers.right.sections[0].active, "localgraph");
  assert.equal(layout.containers.right.active, "localgraph");
  assert.deepEqual(sec(layout, "left"), [["explorer", "search", "filters", "changes"]], "missing views join the last section");
  assert.equal(layout.containers.bottom.maximized, true);
  assert.deepEqual(m.parseWorkbench(JSON.stringify(old)), layout);
});

test("sections: split above/below, tabs into a section, reorder and move sections", () => {
  let layout = m.createWorkbench(LEGACY);
  layout = m.splitView(layout, "localgraph", "right", 1);
  assert.deepEqual(sec(layout, "right"), [["details"], ["localgraph"]]);
  layout = m.splitView(layout, "assistant", "right", 2);
  assert.deepEqual(sec(layout, "right"), [["details"], ["localgraph"], ["assistant"]]);
  assert.deepEqual(layout.containers.right.views, ["details", "localgraph", "assistant"]);
  assert.deepEqual(sec(layout, "bottom"), [["terminal"]]);
  assert.equal(layout.containers.right.active, "assistant");
  // Split above the first section.
  layout = m.splitView(layout, "search", "right", 0);
  assert.deepEqual(sec(layout, "right"), [["search"], ["details"], ["localgraph"], ["assistant"]]);
  // A lone view split next to itself changes nothing.
  assert.equal(m.splitView(layout, "search", "right", 0), layout);
  assert.equal(m.splitView(layout, "search", "right", 1), layout);
  // Into a section as a tab; the emptied section disappears.
  layout = m.moveViewToSection(layout, "search", "right", 2, 0);
  assert.deepEqual(sec(layout, "right"), [["details"], ["search", "localgraph"], ["assistant"]]);
  assert.equal(layout.containers.right.sections[1].active, "search");
  // Reorder sections: assistant to the top, then back to the bottom.
  layout = m.moveSection(layout, "right", 2, "right", 0);
  assert.deepEqual(sec(layout, "right"), [["assistant"], ["details"], ["search", "localgraph"]]);
  assert.equal(m.moveSection(layout, "right", 0, "right", 1), layout, "dropping a section next to itself");
  layout = m.moveSection(layout, "right", 0, "right", 3);
  assert.deepEqual(sec(layout, "right"), [["details"], ["search", "localgraph"], ["assistant"]]);
  // Move a whole section to the left sidebar (above its sections).
  layout = m.moveSection(layout, "right", 1, "left", 0);
  assert.deepEqual(sec(layout, "left"), [["search", "localgraph"], ["filters", "explorer", "changes"]]);
  assert.deepEqual(sec(layout, "right"), [["details"], ["assistant"]]);
  assert.equal(m.containerOf(layout, "localgraph"), "left");
  // Merge a section into another one as tabs.
  layout = m.mergeSection(layout, "right", 1, "right", 0);
  assert.deepEqual(sec(layout, "right"), [["details", "assistant"]]);
  assert.equal(layout.containers.right.sections[0].active, "assistant");
  // moveView goes to the focused section; reordering stays in its section.
  layout = m.moveView(layout, "terminal", "left");
  assert.deepEqual(sec(layout, "left")[0], ["search", "localgraph", "terminal"]);
  layout = m.moveView(layout, "search", "left");
  assert.deepEqual(sec(layout, "left")[0], ["localgraph", "terminal", "search"]);
  // Every view is still placed exactly once.
  const all = m.CONTAINERS.flatMap((name) => layout.containers[name].views).sort();
  assert.deepEqual(all, LEGACY.map((view) => view.id).sort());
  assert.deepEqual(m.parseWorkbench(m.serializeWorkbench(layout), {}, LEGACY), layout, "round-trips");
});

test("sections: resize clamp, equalize, collapse and maximize", () => {
  assert.deepEqual(m.resizePair([200, 200, 300], 0, 1, 50), [250, 150, 300]);
  assert.deepEqual(m.resizePair([200, 200], 0, 1, 500), [400 - m.SECTION_MIN, m.SECTION_MIN], "never below the minimum");
  assert.deepEqual(m.resizePair([200, 200], 0, 1, -500), [m.SECTION_MIN, 400 - m.SECTION_MIN]);
  let layout = m.splitView(m.createWorkbench(), "localgraph", "right", 1);
  layout = m.setSectionSizes(layout, "right", { 0: 310.4, 1: 9 });
  assert.deepEqual(layout.containers.right.sections.map((s) => s.size), [310, m.SECTION_LIMITS[0]]);
  assert.equal(m.setSectionSizes(layout, "right", { 0: 310 }), layout, "unchanged sizes keep the layout");
  layout = m.equalizeSections(layout, "right");
  assert.deepEqual(layout.containers.right.sections.map((s) => s.size), [m.SECTION_SIZE, m.SECTION_SIZE]);
  layout = m.toggleSectionCollapsed(layout, "right", 1);
  assert.equal(layout.containers.right.sections[1].collapsed, true);
  assert.deepEqual(m.displayedViews(layout, "right"), ["details"]);
  assert.equal(m.isViewShown(layout, "localgraph"), false, "a collapsed section's view is not shown");
  layout = m.activateView(layout, "localgraph");
  assert.equal(layout.containers.right.sections[1].collapsed, undefined, "activating expands its section");
  layout = m.toggleSectionMaximized(layout, "right", 0);
  assert.deepEqual(m.sectionDisplay(layout, "right").map((s) => s.collapsed), [false, true]);
  assert.deepEqual(m.displayedViews(layout, "right"), ["details"]);
  layout = m.activateView(layout, "localgraph");
  assert.equal(layout.containers.right.sections[0].maximized, undefined, "showing another section restores");
  layout = m.toggleSectionMaximized(layout, "right", 1);
  layout = m.toggleSectionMaximized(layout, "right", 1);
  assert.deepEqual(m.displayedViews(layout, "right"), ["details", "localgraph"]);
});

test("sections: views that do not apply hide; empty sections collapse away", () => {
  let layout = m.splitView(m.createWorkbench(LEGACY), "localgraph", "right", 1);
  layout = m.splitView(layout, "filters", "right", 2);
  const editor = (id) => id !== "filters" && id !== "details";
  assert.deepEqual(m.sectionDisplay(layout, "right", editor).map((s) => [s.index, s.shown, s.collapsed]), [[1, "localgraph", false]]);
  // A lone remaining section is never collapsed, even when stored collapsed.
  layout = m.toggleSectionCollapsed(layout, "right", 1);
  assert.deepEqual(m.displayedViews(layout, "right", editor), ["localgraph"]);
  assert.equal(m.containerShown(layout, "right", () => false), false);
  assert.equal(m.displayedView(layout, "right"), "filters", "the focused view when on screen");
});

test("sections: built-in presets stack views and survive storage", () => {
  const research = m.builtinPresetLayout("research", null, LEGACY);
  assert.deepEqual(sec(research, "right"), [["details"], ["localgraph"]]);
  assert.deepEqual(sec(m.builtinPresetLayout("writing", null, LEGACY), "right"), [["localgraph"], ["details"]]);
  assert.deepEqual(sec(m.builtinPresetLayout("review"), "bottom"), [["terminal"], ["assistant"]]);
  const stored = m.storableLayout(m.toggleSectionCollapsed(research, "right", 1), LEGACY);
  assert.deepEqual(stored.containers.right.sections, [
    { views: ["details"], active: "details", size: m.SECTION_SIZE },
    { views: ["localgraph"], active: "localgraph", size: m.SECTION_SIZE, collapsed: true },
  ]);
  const { store, id } = m.savePreset({ presets: [] }, "Stacked", stored);
  assert.deepEqual(sec(m.presetLayout(id, store, null, LEGACY), "right"), [["details"], ["localgraph"]]);
});

test("narrow windows draw the sidebars narrower so the workspace keeps room", () => {
  const { fitSidebars, MIN_CONTENT_W } = m;
  // Wide enough: unchanged.
  assert.deepEqual(fitSidebars(288, 430, 1600), { left: 288, right: 430 });
  // 1100 px: both shrink proportionally until the content has MIN_CONTENT_W.
  const at1100 = fitSidebars(288, 430, 1100);
  assert.equal(1100 - 44 - at1100.left - at1100.right, MIN_CONTENT_W);
  assert.ok(at1100.left >= 200 && at1100.right >= 320);
  // 1000 px: never below the minimum widths.
  assert.deepEqual(fitSidebars(288, 430, 1000), { left: 200, right: 320 });
  // One sidebar hidden: only the other shrinks.
  const right = fitSidebars(0, 760, 1000);
  assert.equal(right.left, 0);
  assert.equal(1000 - 44 - right.right, MIN_CONTENT_W);
  // Already at the minimum: unchanged.
  assert.deepEqual(fitSidebars(200, 320, 900), { left: 200, right: 320 });
});

// ------------------------------------------------- draggable views (T-0024)
const sections = (layout, name) => layout.containers[name].sections.map((section) => section.views);

test("placeView: reorder activity icons up and down without changing what is shown", () => {
  let layout = m.createWorkbench();
  // filters is shown; drag "changes" above "explorer".
  layout = m.placeView(layout, "changes", "left", { anchor: "explorer", side: "before" });
  assert.deepEqual(views(layout, "left"), ["filters", "changes", "explorer", "search"]);
  assert.equal(layout.containers.left.sections[0].active, "filters", "reorder keeps the shown view");
  assert.equal(layout.containers.left.active, "filters");
  // Drag "filters" (shown) below "search": still shown.
  layout = m.placeView(layout, "filters", "left", { anchor: "search", side: "after" });
  assert.deepEqual(views(layout, "left"), ["changes", "explorer", "search", "filters"]);
  assert.equal(layout.containers.left.sections[0].active, "filters");
  // No anchor: end of the container.
  layout = m.placeView(layout, "changes", "left");
  assert.deepEqual(views(layout, "left"), ["explorer", "search", "filters", "changes"]);
  // A drop that changes nothing returns the same object.
  assert.equal(m.placeView(layout, "search", "left", { anchor: "filters", side: "before" }), layout);
  assert.equal(m.placeView(layout, "search", "left", { anchor: "search" }), layout);
  assert.equal(m.placeView(layout, "search", "left", { anchor: "details" }), layout, "anchor must be in the target");
  assert.equal(m.placeView(layout, "nope", "left"), layout);
});

// The Default activity bar as the browser lays it out: 36px icons, 2px
// gaps, starting at y=44 (T-0033: dragging an icon up did nothing).
const DEFAULT_LEFT = ["filters", "explorer", "search", "changes", "assistant", "localgraph"];
const iconRow = (ids) => ids.map((id, k) => ({ id, start: 44 + k * 38, end: 44 + k * 38 + 36 }));
const mid = (ids, id) => 44 + ids.indexOf(id) * 38 + 18;
const dropIcon = (layout, id, y) => {
  const ids = views(layout, "left");
  const slot = m.slotSpot(iconRow(ids), id, y);
  return slot ? m.placeView(layout, id, "left", slot) : layout;
};

test("slotSpot: dropping on the icon above moves up, on the icon below moves down (anywhere on it)", () => {
  const ids = DEFAULT_LEFT;
  const row = iconRow(ids);
  for (const dy of [-17, -9, 0, 1, 9, 17]) {
    // Up: search onto explorer - upper half, centre and the near (lower) half alike.
    assert.deepEqual(m.slotSpot(row, "search", mid(ids, "explorer") + dy), { anchor: "explorer", side: "before" }, `up dy=${dy}`);
    // Down: explorer onto search - the near (upper) half, centre and lower half.
    assert.deepEqual(m.slotSpot(row, "explorer", mid(ids, "search") + dy), { anchor: "search", side: "after" }, `down dy=${dy}`);
  }
  // Two places up / down.
  assert.deepEqual(m.slotSpot(row, "localgraph", mid(ids, "filters")), { anchor: "filters", side: "before" });
  assert.deepEqual(m.slotSpot(row, "filters", mid(ids, "localgraph")), { anchor: "localgraph", side: "after" });
  // Over the dragged icon itself: nothing to do.
  assert.equal(m.slotSpot(row, "search", mid(ids, "search")), null);
  // Gaps and ends: before the next icon; above the first; below the last.
  assert.deepEqual(m.slotSpot(row, "search", 44 + 36 + 1), { anchor: "explorer", side: "before" });
  assert.deepEqual(m.slotSpot(row, "search", 10), { anchor: "filters", side: "before" });
  assert.deepEqual(m.slotSpot(row, "search", 900), { anchor: "localgraph", side: "after" });
  // A view from another row (a tab dragged onto the activity bar): halves.
  assert.deepEqual(m.slotSpot(row, "details", mid(ids, "search") - 5), { anchor: "search", side: "before" });
  assert.deepEqual(m.slotSpot(row, "details", mid(ids, "search") + 5), { anchor: "search", side: "after" });
  assert.equal(m.slotSpot([], "search", 50), null);
});

test("slotSpot + placeView: Default activity bar reorders in both directions", () => {
  const base = m.presetLayout("default", m.normalizePresetStore(null));
  assert.deepEqual(views(base, "left"), DEFAULT_LEFT);
  // search up above explorer, dropped on explorer's centre and on its lower half.
  for (const dy of [0, 10]) {
    const up = dropIcon(base, "search", mid(DEFAULT_LEFT, "explorer") + dy);
    assert.deepEqual(views(up, "left"), ["filters", "search", "explorer", "changes", "assistant", "localgraph"]);
  }
  // localgraph up to the top.
  assert.deepEqual(views(dropIcon(base, "localgraph", mid(DEFAULT_LEFT, "filters") + 10), "left"),
    ["localgraph", "filters", "explorer", "search", "changes", "assistant"]);
  // Down still works: explorer onto search (upper half and centre), filters to the bottom.
  for (const dy of [-10, 0]) {
    assert.deepEqual(views(dropIcon(base, "explorer", mid(DEFAULT_LEFT, "search") + dy), "left"),
      ["filters", "search", "explorer", "changes", "assistant", "localgraph"]);
  }
  assert.deepEqual(views(dropIcon(base, "filters", 800), "left"),
    ["explorer", "search", "changes", "assistant", "localgraph", "filters"]);
  // On itself: unchanged.
  assert.equal(dropIcon(base, "search", mid(DEFAULT_LEFT, "search")), base);
});

test("slotSpot: tab strips (x axis) reorder left and right", () => {
  const row = [{ id: "terminal", start: 50, end: 130 }, { id: "assistant", start: 132, end: 220 }, { id: "changes", start: 222, end: 300 }];
  assert.deepEqual(m.slotSpot(row, "changes", 140), { anchor: "assistant", side: "before" });
  assert.deepEqual(m.slotSpot(row, "changes", 60), { anchor: "terminal", side: "before" });
  assert.deepEqual(m.slotSpot(row, "terminal", 210), { anchor: "assistant", side: "after" });
  assert.deepEqual(m.slotSpot(row, "terminal", 230), { anchor: "changes", side: "after" });
  assert.equal(m.slotSpot(row, "assistant", 176), null);
});

test("placeView: icon to a tab strip and a tab back to the activity bar", () => {
  let layout = m.createWorkbench();
  // Left icon -> right panel tab strip, between Details and Local graph.
  layout = m.placeView(layout, "search", "right", { anchor: "details", side: "after" });
  assert.deepEqual(views(layout, "right"), ["details", "search", "localgraph"]);
  assert.equal(layout.containers.right.active, "search", "a view moved to another bar is shown there");
  assert.equal(layout.containers.right.visible, true);
  assert.deepEqual(views(layout, "left"), ["filters", "explorer", "changes"]);
  // Bottom tab (hidden panel) -> activity bar before "explorer".
  layout = m.placeView(layout, "terminal", "left", { anchor: "explorer", side: "before" });
  assert.deepEqual(views(layout, "left"), ["filters", "terminal", "explorer", "changes"]);
  assert.deepEqual(views(layout, "bottom"), ["assistant"]);
  assert.equal(layout.containers.left.active, "terminal");
  // Tab reorder inside a strip.
  layout = m.placeView(layout, "localgraph", "right", { anchor: "details", side: "before" });
  assert.deepEqual(views(layout, "right"), ["localgraph", "details", "search"]);
  assert.equal(layout.containers.right.sections[0].active, "search", "the shown tab stays shown");
  // Into an empty container: a new section.
  layout = m.placeView(layout, "assistant", "left");
  assert.deepEqual(views(layout, "bottom"), []);
  layout = m.placeView(layout, "terminal", "bottom");
  assert.deepEqual(sections(layout, "bottom"), [["terminal"]]);
});

test("placeView across stacked sections keeps an open view open", () => {
  let layout = m.builtinPresetLayout("writing");
  assert.deepEqual(sections(layout, "left"), [["explorer", "filters", "changes"], ["search"]]);
  // explorer is shown in the first section; move it next to search.
  layout = m.placeView(layout, "explorer", "left", { anchor: "search", side: "after" });
  assert.deepEqual(sections(layout, "left"), [["filters", "changes"], ["search", "explorer"]]);
  assert.equal(layout.containers.left.sections[1].active, "explorer", "it stays open in its new section");
  // changes (not shown) moved into the second section does not take over.
  layout = m.placeView(layout, "changes", "left", { anchor: "search", side: "before" });
  assert.deepEqual(sections(layout, "left"), [["filters"], ["changes", "search", "explorer"]]);
  assert.equal(layout.containers.left.sections[1].active, "explorer");
});

test("shiftView: keyboard move up/down, across sections and skipping hidden views", () => {
  let layout = m.createWorkbench();
  layout = m.shiftView(layout, "search", -1);
  assert.deepEqual(views(layout, "left"), ["filters", "search", "explorer", "changes"]);
  layout = m.shiftView(layout, "search", 1);
  layout = m.shiftView(layout, "search", 1);
  assert.deepEqual(views(layout, "left"), ["filters", "explorer", "changes", "search"]);
  assert.equal(m.canShiftView(layout, "search", 1), false);
  assert.equal(m.shiftView(layout, "search", 1), layout, "last view cannot move down");
  assert.equal(m.canShiftView(layout, "filters", -1), false);
  assert.equal(m.canShiftView(layout, "filters", 1), true);
  // Only applicable views count as neighbours (explorer/changes hidden outside the Editor).
  const applies = (id) => !["explorer", "changes"].includes(id);
  layout = m.shiftView(layout, "search", -1, applies);
  assert.deepEqual(views(layout, "left"), ["search", "filters", "explorer", "changes"]);
  // Across sections.
  let writing = m.builtinPresetLayout("writing");
  writing = m.shiftView(writing, "search", -1);
  assert.deepEqual(sections(writing, "left"), [["explorer", "filters", "search", "changes"]]);
});

test("reset view layout returns to the default layout (the user's default preset, else Default)", () => {
  let store = m.normalizePresetStore(null);
  const base = m.defaultLayout(store);
  assert.deepEqual(base, m.builtinPresetLayout("default"));
  assert.equal(m.isDefaultPlacement(base, base), true);
  let layout = m.placeView(base, "search", "right");
  layout = m.placeView(layout, "terminal", "left", { anchor: "filters" });
  layout = m.splitView(layout, "changes", "left", 0);
  assert.equal(m.isDefaultPlacement(layout, base), false);
  // Sizes or visibility alone do not count as a different placement.
  assert.equal(m.isDefaultPlacement(m.setContainerSize(m.setContainerVisible(base, "right", false), "left", 400), base), true);
  const reset = m.defaultLayout(store, layout);
  assert.deepEqual(reset.containers.left.views, ["filters", "explorer", "search", "changes", "assistant", "localgraph"]);
  assert.equal(m.isDefaultPlacement(reset, base), true);
  // With a default preset, reset goes there.
  // Only Default and Focus are offered; an old default of a removed preset is ignored.
  assert.deepEqual(m.BUILTIN_PRESETS, ["default", "focus"]);
  store = m.setDefaultPreset(store, "review");
  assert.deepEqual(m.defaultLayout(store, layout), m.builtinPresetLayout("default"));
});

test("layout persistence: moves survive storage and the account preset store", () => {
  let layout = m.createWorkbench();
  layout = m.placeView(layout, "terminal", "left", { anchor: "filters", side: "before" });
  layout = m.placeView(layout, "explorer", "bottom", { anchor: "assistant", side: "after" });
  const restored = m.parseWorkbench(m.serializeWorkbench(layout));
  assert.deepEqual(views(restored, "left"), ["terminal", "filters", "search", "changes"]);
  assert.deepEqual(views(restored, "bottom"), ["assistant", "explorer"]);
  // The account copy (prefs.workbench.layout) rides along with the presets.
  let store = m.withCurrentLayout(m.normalizePresetStore(null), layout);
  assert.deepEqual(store.layout.containers.left.views, ["terminal", "filters", "search", "changes"]);
  store = m.savePreset(store, "Mine", m.createWorkbench()).store;
  store = m.renamePreset(store, store.presets[0].id, "Renamed");
  store = m.setDefaultPreset(store, "research");
  store = m.deletePreset(store, store.presets[0].id);
  assert.ok(store.layout, "preset edits keep the current layout");
  const round = m.normalizePresetStore(JSON.parse(JSON.stringify(store)));
  assert.deepEqual(m.normalizeWorkbench(round.layout).containers.bottom.views, ["assistant", "explorer"]);
  assert.equal(m.withCurrentLayout(store, null).layout, undefined);
  // The save time rides along; only a newer account copy replaces this browser's.
  store = m.withCurrentLayout(store, layout, undefined, 2000);
  assert.equal(m.normalizePresetStore(JSON.parse(JSON.stringify(store))).layout_at, 2000);
  assert.equal(m.accountLayoutIsNewer(store, 1000), true, "changed on another device later");
  assert.equal(m.accountLayoutIsNewer(store, 3000), false, "a stale (cached) profile must not undo a later local change");
  assert.equal(m.accountLayoutIsNewer(store, 0), true, "first visit on this device");
  assert.equal(m.accountLayoutIsNewer(m.withCurrentLayout(store, layout), 5), false, "an undated copy never beats a dated local one");
  assert.equal(m.accountLayoutIsNewer({ presets: [] }, 0), false);
  assert.equal(m.withCurrentLayout(store, null).layout_at, undefined);
  assert.equal(m.normalizePresetStore({ presets: [], layout_at: 5 }).layout_at, undefined, "no stamp without a layout");
  // A broken account layout is dropped, not fatal.
  assert.equal(m.normalizePresetStore({ presets: [], layout: { v: 2 } }).layout, undefined);
});

test("layouts that still list the former Outline and Timeline views drop them quietly", () => {
  const raw = { v: 1, containers: {
    left: { visible: true, active: "outline", sections: [
      { views: ["filters", "explorer", "search", "changes"], active: "explorer" },
      { views: ["outline"], active: "outline" }, { views: ["timeline"], active: "timeline", collapsed: true }] },
    right: { visible: true, sections: [{ views: ["details", "localgraph", "timeline"] }] },
    bottom: { visible: false, sections: [{ views: ["assistant", "terminal"] }] },
  } };
  const layout = m.normalizeWorkbench(raw);
  const all = Object.values(layout.containers).flatMap((box) => box.views);
  assert.ok(!all.includes("outline") && !all.includes("timeline"), all.join(" "));
  assert.equal(layout.containers.left.sections.length, 1, "their empty sections are gone");
  assert.equal(layout.containers.left.active, "explorer");
  assert.deepEqual(layout.containers.right.views, ["details", "localgraph"]);
});

test("the Topics view: a left view, last in the default preset, placed for stored layouts without it", () => {
  const topics = mod.VIEW_DEFAULTS.find((view) => view.id === "topics");
  assert.deepEqual({ container: topics.container, icon: topics.icon, titleKey: topics.titleKey },
    { container: "left", icon: "layers", titleKey: "workbench.view.topics" });
  const def = mod.builtinPresetLayout("default");
  assert.equal(def.containers.left.views.at(-1), "topics");
  assert.equal(def.containers.left.active, "localgraph", "the default preset still opens the Local graph");
  // A layout saved before the view existed gets it in the left sidebar.
  const old = { v: 1, containers: { left: { views: ["filters", "explorer"], sections: [{ views: ["filters", "explorer"] }] } } };
  const layout = mod.normalizeWorkbench(old);
  assert.equal(mod.containerOf(layout, "topics"), "left");
  // Movable like the other views.
  const moved = mod.placeView(layout, "topics", "right");
  assert.equal(mod.containerOf(moved, "topics"), "right");
  assert.equal(mod.containerOf(mod.normalizeWorkbench(JSON.parse(mod.serializeWorkbench(moved))), "topics"), "right");
});
