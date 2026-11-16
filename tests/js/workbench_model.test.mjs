// Run: node --experimental-vm-modules --test tests/js/workbench_model.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

async function loadModel() {
  const source = await readFile(new URL("../../web/js/workbench-model.js", import.meta.url), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: "workbench-model.js" });
  await module.link(() => { throw new Error("workbench-model must stay dependency-free"); });
  await module.evaluate();
  return module.namespace;
}

const m = await loadModel();
const views = (layout, name) => layout.containers[name].views;

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

test("persistence round-trip and migration from the legacy panel preferences", () => {
  let layout = m.moveView(m.createWorkbench(), "assistant", "right");
  const restored = m.parseWorkbench(m.serializeWorkbench(layout));
  assert.deepEqual(restored, m.normalizeWorkbench(layout));
  // No stored workbench yet: map the old toggles and sizes.
  const migrated = m.parseWorkbench(null, {
    filtersVisible: "0", inspectorPane: "1", sessionDock: "1",
    railWidth: "320", inspectorWidth: "500", sessionHeight: "300",
  });
  assert.equal(migrated.containers.left.visible, false);
  assert.equal(migrated.containers.right.visible, true);
  assert.equal(migrated.containers.bottom.visible, true);
  assert.equal(migrated.containers.left.size, 320);
  assert.equal(migrated.containers.right.size, 500);
  assert.equal(migrated.containers.bottom.size, 300);
  // Fresh browser (nothing stored): defaults.
  const fresh = m.parseWorkbench("not json", {});
  assert.deepEqual(fresh, m.createWorkbench());
});

test("built-in presets: default, focus keeps placement, research/writing/review", () => {
  const moved = m.moveView(m.createWorkbench(), "terminal", "right");
  assert.deepEqual(m.builtinPresetLayout("default"), m.createWorkbench());
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
  assert.deepEqual(m.defaultLayout(store), m.createWorkbench());
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
  let layout = m.createWorkbench();
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
  assert.deepEqual(all, m.VIEW_DEFAULTS.map((view) => view.id).sort());
  assert.deepEqual(m.parseWorkbench(m.serializeWorkbench(layout)), layout, "round-trips");
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
  let layout = m.splitView(m.createWorkbench(), "localgraph", "right", 1);
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
  const research = m.builtinPresetLayout("research");
  assert.deepEqual(sec(research, "right"), [["details"], ["localgraph"]]);
  assert.deepEqual(sec(m.builtinPresetLayout("writing"), "right"), [["localgraph"], ["details"]]);
  assert.deepEqual(sec(m.builtinPresetLayout("review"), "bottom"), [["terminal"], ["assistant"]]);
  const stored = m.storableLayout(m.toggleSectionCollapsed(research, "right", 1));
  assert.deepEqual(stored.containers.right.sections, [
    { views: ["details"], active: "details", size: m.SECTION_SIZE },
    { views: ["localgraph"], active: "localgraph", size: m.SECTION_SIZE, collapsed: true },
  ]);
  const { store, id } = m.savePreset({ presets: [] }, "Stacked", stored);
  assert.deepEqual(sec(m.presetLayout(id, store), "right"), [["details"], ["localgraph"]]);
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
