// Run: node --experimental-vm-modules --test tests/js/graph_right_drag.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";
import { loadGraphCore, loadNodeGraphLayout, loadNodeCard, loadGraphSprites, loadMindLayout, loadGraphChange } from "./load_graph_core.mjs";

class Surface {
  listeners = new Map();
  style = {};
  addEventListener(type, fn) {
    if (!this.listeners.has(type)) this.listeners.set(type, new Set());
    this.listeners.get(type).add(fn);
  }
  removeEventListener(type, fn) { this.listeners.get(type)?.delete(fn); }
  emit(type, props = {}) {
    const event = {
      type, button: 2, pointerId: 1, clientX: 20, clientY: 20,
      preventDefault() { this.defaultPrevented = true; }, ...props,
    };
    for (const fn of this.listeners.get(type) || []) fn(event);
    return event;
  }
  getBoundingClientRect() { return { left: 0, top: 0 }; }
  setPointerCapture() {}
  focus() {}
}

async function fixture() {
  const window = new Surface();
  const context = vm.createContext({
    window, document: new Surface(), ResizeObserver: class { observe() {} },
  });
  const core = await loadGraphCore(context);
  const graphModule = new vm.SourceTextModule(
    await readFile(new URL("../../ark_studio/web/js/graph.js", import.meta.url), "utf8"), { context },
  );
  const imports = {
    "./dom.js": ["h", "clear", "typeColor", "statusColor", "clamp", "copyText"],
    "./i18n.js": ["t", "typeLabel", "statusLabel", "relLabel"],
    "./icons.js": ["icon", "drawStatusIcon"],
    "./physics.js": ["normalizeGraphPhysics", "GRAPH_DEFAULTS"],
    "./mind-settings.js": ["getMindSettings"],
    "./shortcuts.js": ["getTagShortcuts", "shortcutMatches"],
    "./markers.js": ["MARKERS", "markersFor", "markerArc"],
    "./context-menu.js": ["openContextMenu"],
    "./image-copy.js": ["imageCopyMenuItems"],
    "./api.js": ["authenticatedUrl"],
    "./graph-force.js": ["applyRepulsion"],
    "./assign-menu.js": ["assignMenuItem"],
    "./export-menu.js": ["exportBundleMenuItem"],
    "./node-history.js": ["historyMenuItem"],
    "./presence-ui.js": ["attachPresenceOverlay", "createPresenceChip"],
    "./selection-model.js": ["siblingGroup", "groupDefaults"],
  };
  const nodeLayout = await loadNodeGraphLayout(context);
  const nodeCard = await loadNodeCard(context);
  const graphSprites = await loadGraphSprites(context);
  const mindLayoutModule = await loadMindLayout(context, nodeCard);
  const graphChange = await loadGraphChange(context);
  await graphModule.link((specifier) => specifier === "./graph-change.js" ? graphChange : specifier === "./graph-core.js" ? core : specifier === "./node-graph-layout.js" ? nodeLayout : specifier === "./node-card.js" ? nodeCard : specifier === "./graph-sprites.js" ? graphSprites : specifier === "./mind-layout.js" ? mindLayoutModule : new vm.SyntheticModule(imports[specifier], function () {
    for (const name of imports[specifier]) this.setExport(name, () => ({}));
  }, { context }));
  await graphModule.evaluate();
  const graph = Object.create(graphModule.namespace.GraphView.prototype);
  const menus = [];
  Object.assign(graph, {
    canvas: new Surface(), host: new Surface(), opts: {}, layout: "force",
    pan: { x: 0, y: 0 }, pointers: new Map(), selectedNodes: new Set(),
    _draw() {}, _cancelCameraAnimation() {}, _hit() { return null; },
    _hitMindImage() { return null; }, _openGraphContextMenu(node, event) { menus.push(event); },
  });
  graph._bind();
  return { graph, canvas: graph.canvas, window, menus };
}

for (const early of [true, false]) {
  const order = early ? "press-time (Firefox/Linux)" : "release-time (Edge/Windows)";
  test(`${order}: drag pans without a menu`, async () => {
    const { graph, canvas, menus } = await fixture();
    canvas.emit("pointerdown");
    if (early) assert.equal(canvas.emit("contextmenu").defaultPrevented, true);
    assert.equal(menus.length, 0);
    canvas.emit("pointermove", { clientX: 60 });
    assert.equal(graph.pan.x, 40);
    canvas.emit("pointerup", { clientX: 60 });
    if (!early) canvas.emit("contextmenu", { clientX: 60 });
    assert.equal(menus.length, 0);
  });
  test(`${order}: stationary click opens once, only on release`, async () => {
    const { canvas, menus } = await fixture();
    canvas.emit("pointerdown");
    if (early) canvas.emit("contextmenu");
    assert.equal(menus.length, 0);
    canvas.emit("pointerup");
    if (!early) canvas.emit("contextmenu");
    assert.equal(menus.length, 1);
  });
}

test("cancelled right press never opens a menu", async () => {
  const { canvas, menus } = await fixture();
  canvas.emit("pointerdown");
  canvas.emit("contextmenu");
  canvas.emit("pointercancel");
  assert.equal(menus.length, 0);
});

test("release displacement catches a missing move event; next click still works", async () => {
  const { canvas, menus } = await fixture();
  canvas.emit("pointerdown");
  canvas.emit("contextmenu");
  canvas.emit("pointerup", { clientX: 60 });
  assert.equal(menus.length, 0);
  canvas.emit("pointerdown");
  canvas.emit("contextmenu");
  canvas.emit("pointerup");
  canvas.emit("contextmenu"); // Do not duplicate a deferred menu.
  assert.equal(menus.length, 1);
});

for (const moved of [false, true]) {
  test(`mouse fallback ${moved ? "drag" : "click"} respects release`, async () => {
    const { graph, canvas, window, menus } = await fixture();
    canvas.emit("mousedown");
    canvas.emit("contextmenu");
    assert.equal(menus.length, 0);
    if (moved) window.emit("mousemove", { clientX: 60 });
    window.emit("mouseup", { clientX: moved ? 60 : 20 });
    assert.equal(menus.length, moved ? 0 : 1);
    assert.equal(graph.pan.x, moved ? 40 : 0);
    assert.equal(window.listeners.get("mousemove").size, 0);
    assert.equal(window.listeners.get("mouseup").size, 0);
  });
}

test("keyboard context menu without a pointer gesture still works", async () => {
  const { canvas, menus } = await fixture();
  canvas.emit("contextmenu", { button: 0 });
  assert.equal(menus.length, 1);
});

test("a suppressed drag does not swallow a later keyboard menu", async () => {
  const { canvas, menus } = await fixture();
  canvas.emit("pointerdown");
  canvas.emit("contextmenu");
  canvas.emit("pointermove", { clientX: 60 });
  canvas.emit("pointerup", { clientX: 60 });
  canvas.emit("contextmenu", { button: 0 });
  assert.equal(menus.length, 1);
});
