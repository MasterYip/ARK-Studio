// Run: node --experimental-vm-modules --test tests/js/locate_map.test.mjs
// "Locate in research map": which nodes may be located, the menu entry in the
// shared node menu (enabled for units, disabled for a review group or a node in
// the recycle bin), and the graph filter showing a located node the filters hide.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

const context = vm.createContext({});
// Values from the vm realm compare by structure (their prototypes differ).
const eq = (actual, expected, message) => assert.deepEqual(JSON.parse(JSON.stringify(actual ?? null)), expected, message);
const web = (path) => new URL(`../../ark_studio/web/js/${path}`, import.meta.url);

// Load `path` with real modules for `real` specifiers and stubs (name -> value) for the rest.
async function load(path, stubs = {}, real = new Map()) {
  const module = new vm.SourceTextModule(await readFile(web(path), "utf8"), { context, identifier: web(path).href });
  await module.link(async (specifier) => {
    if (real.has(specifier)) return real.get(specifier);
    const exports = stubs[specifier];
    if (!exports) throw new Error(`${path}: no stub for ${specifier}`);
    const synthetic = new vm.SyntheticModule(Object.keys(exports), function () {
      for (const [name, value] of Object.entries(exports)) this.setExport(name, value);
    }, { context });
    return synthetic;
  });
  await module.evaluate();
  return module;
}

const model = await load("locate-map-model.js");
const { locateBlockReason, revealList, revealedNodes, revealNotice, locateCamera } = model.namespace;
const t = (key) => key;
const locate = await load("locate-map.js", {
  "./dom.js": { h() {} },
  "./i18n.js": { t },
  "./icons.js": { icon() {} },
  "./shortcuts.js": { getShortcut: (id) => (id === "locateMap" ? "Alt+M" : ""), shortcutMatches: () => false },
  "./mobile-core.js": { isMobileViewport: () => false },
}, new Map([["./locate-map-model.js", model]]));
const { locateInMapItem, locateUnit } = locate.namespace;

test("units can be located; review groups, the recycle bin and other projects cannot", () => {
  assert.equal(locateBlockReason({ uid: "C-0001" }), "");
  assert.equal(locateBlockReason({ uid: "R-3", reviewGroup: "R-3" }), "group");
  assert.equal(locateBlockReason({ uid: "C-2", deleted_at: "2026-11-20T10:00:00Z" }), "bin");
  assert.equal(locateBlockReason({ uid: "other/C-2" }), "external");
  assert.equal(locateBlockReason({ uid: "" }), "missing");
  assert.equal(locateBlockReason(null), "missing");
  eq(revealList(" T-0042 "), ["T-0042"]);
  eq(revealList(""), []);
});

test("the menu entry runs ctx.locateInMap for a unit and is disabled with the reason otherwise", () => {
  const calls = [];
  const ctx = { locateInMap: (uid) => calls.push(uid) };
  const item = locateInMapItem({ uid: "T-0042" }, ctx);
  assert.equal(item.id, "locate-map");
  assert.equal(item.label, "locate.menu");
  assert.equal(item.shortcut, "Alt+M");
  assert.equal(item.disabled, false);
  item.onClick();
  eq(calls, ["T-0042"]);
  for (const [unit, reason] of [[{ uid: "R-1", reviewGroup: "R-1" }, "group"], [{ uid: "C-1", deleted_at: "x" }, "bin"]]) {
    const off = locateInMapItem(unit, ctx);
    assert.equal(off.disabled, true, reason);
    assert.equal(off.title, `locate.disabled.${reason}`);
    assert.equal(off.hint, `locate.disabled.${reason}`);
  }
  assert.equal(locateUnit({ uid: "R-1", reviewGroup: "R-1" }, ctx), false);
  assert.equal(locateUnit({ uid: "C-7" }, ctx), true);
  eq(calls, ["T-0042", "C-7"]);
});

test("the shared node menu (rows, cards, inspector ⋯) has the entry after Open in Editor", async () => {
  const unitMenu = await load("unit-menu.js", {
    "./i18n.js": { t },
    "./dom.js": { copyText() {} },
    "./context-menu.js": { openContextMenu() {} },
    "./assign-menu.js": { assignMenuItem: () => null, setAssignContext() {} },
    "./unit-references.js": { installUnitReferences() {} },
    "./export-menu.js": { exportBundleMenuItem: () => null },
    "./node-history.js": { historyMenuItem: () => null, setHistoryContext() {} },
    "./node-reveal.js": { setNodeReveal() {} },
    "./agent-messages.js": { agentMessageMenuItem: () => null },
    "./project-move.js": { installMovePlanLinks() {}, projectSplitMenuItem: () => null },
  }, new Map([["./locate-map.js", locate]]));
  const { unitMenuItems } = unitMenu.namespace;
  const ctx = { locateInMap() {}, openInEditor() {} };
  const ids = (items) => items.filter(Boolean).map((item) => item.id || item.label);
  const unit = ids(unitMenuItems(ctx, { uid: "C-1", title: "A claim" }));
  assert.ok(unit.indexOf("locate-map") === unit.indexOf("editor.open_in_editor") + 1, unit.join());
  const group = unitMenuItems(ctx, { uid: "R-1", reviewGroup: "R-1" }).find((item) => item?.id === "locate-map");
  assert.equal(group.disabled, true);
});

test("the camera centres the node and zooms in to a readable level, never out", () => {
  eq(locateCamera({ x: 100, y: -50 }, 0.3, "force"), { zoom: 1, pan: { x: -100, y: 50 } });
  eq(locateCamera({ x: 10, y: 0 }, 0.5, "mind"), { zoom: 0.85, pan: { x: -8.5, y: 0 } });
  assert.equal(locateCamera({ x: 0, y: 0 }, 2.4, "force").zoom, 2.4);
});

test("the graph filter shows a located node the filters or the node limit hide, and says why", async () => {
  const filter = await load("graph-filter.js", { "./filter-store.js": { assigneeMatches: () => true } });
  const { GraphFilterIndex } = filter.namespace;
  const node = (id, extra = {}) => ({ id, uid: `N-${id}`, title: `Node ${id}`, type: "claim", status: "open",
    importance: "p2", created_at: "2026-01-01", updated_at: `2026-01-${String(30 - id).padStart(2, "0")}`, tags: [], ...extra });
  const index = new GraphFilterIndex({ nodes: [node(1), node(2), node(3, { type: "task" }), node(4)],
    edges: [{ source: 3, target: 1 }] });
  // Hidden by the type filter: shown, marked "filter", with its links to shown nodes.
  let data = index.filter({ types: ["claim"], reveal: ["N-3"] });
  eq(data.nodes.map((n) => [n.id, n.revealed || ""]), [[1, ""], [2, ""], [4, ""], [3, "filter"]]);
  eq(data.edges, [{ source: 3, target: 1 }]);
  assert.equal(data.revealed_count, 1);
  assert.equal(data.total, 3, "the count is still the filter's");
  eq(revealNotice(data), { key: "locate.revealed_filter", clearFilters: true,
    nodes: [{ id: 3, uid: "N-3", title: "Node 3", reason: "filter" }] });
  // "None" types: still shown.
  data = index.filter({ types_none: 1, reveal: ["N-2"] });
  eq(data.nodes.map((n) => n.id), [2]);
  // Beyond the node limit: shown, marked "limit" (clearing filters would not help).
  data = index.filter({ limit: 2, reveal: ["4"] });
  eq(data.nodes.map((n) => [n.id, n.revealed || ""]), [[1, ""], [2, ""], [4, "limit"]]);
  eq(revealNotice(data).key, "locate.revealed_limit");
  assert.equal(revealNotice(data).clearFilters, false);
  // Already shown: nothing is marked, no notice.
  data = index.filter({ reveal: ["N-1"] });
  assert.equal(data.nodes.filter((n) => n.revealed).length, 0);
  assert.equal(revealNotice(data), null);
  eq(revealedNodes({ nodes: [] }), []);
});
