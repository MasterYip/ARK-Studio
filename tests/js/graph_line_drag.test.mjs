// Run: node --experimental-vm-modules --test tests/js/graph_line_drag.test.mjs
// A dragged line (Alt-drag link, node-graph route handle) must end whenever the
// button is released, wherever that happens, and never keep following the mouse.
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
  count(type) { return this.listeners.get(type)?.size || 0; }
  emit(type, props = {}) {
    const event = {
      type, button: 0, buttons: 1, pointerId: 1, pointerType: "mouse", clientX: 20, clientY: 20,
      preventDefault() { this.defaultPrevented = true; }, stopPropagation() { this.stopped = true; }, ...props,
    };
    for (const fn of [...(this.listeners.get(type) || [])]) fn(event);
    return event;
  }
  getBoundingClientRect() { return { left: 0, top: 0, right: 600, bottom: 400, width: 600, height: 400 }; }
  setPointerCapture() {}
  focus() {}
}

async function fixture(layout = "force") {
  const window = new Surface();
  const context = vm.createContext({
    window, document: new Surface(), ResizeObserver: class { observe() {} }, performance,
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
    "./markers.js": ["markersFor", "markerArc", "markerMenuChildren", "toggleMarkerChange", "isMarkerTag"],
    "./context-menu.js": ["openContextMenu"],
    "./image-copy.js": ["imageCopyMenuItems"],
    "./api.js": ["authenticatedUrl"],
    "./graph-force.js": ["applyRepulsion"],
    "./assign-menu.js": ["assignMenuItem"],
    "./export-menu.js": ["exportBundleMenuItem"],
    "./node-history.js": ["historyMenuItem"],
    "./locate-map.js": ["locateInMapItem", "isLocateShortcut", "locateShortcut", "locateUnit"],
    "./locate-map-model.js": ["locateCamera"],
    "./presence-ui.js": ["attachPresenceOverlay", "createPresenceChip"],
    "./selection-model.js": ["siblingGroup", "groupDefaults"],
    "./read-edit-mode.js": ["rememberMode", "savedMode"],
    "./graph-viewport-model.js": ["pointsOnScreen", "viewportAction", "anchorShift"],
    "./users.js": ["currentUsername"],
    "./components.js": ["toast"],
    "./graph3d-model.js": ["loadViewChoice", "saveViewChoice", "loadGraph3DSettings", "saveGraph3DSettings", "graph3DAvailable"],
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
  const source = { id: 1, x: 0, y: 0 }, target = { id: 2, x: 200, y: 0 };
  const edge = { id: 7, source: 1, target: 2, relation: "supports" };
  const connects = [];
  const saves = [];
  Object.assign(graph, {
    canvas: new Surface(), host: new Surface(), opts: { onConnect: (a, b) => { connects.push([a.id, b.id]); } },
    layout, mode: "edit", pan: { x: 0, y: 0 }, pointers: new Map(), selectedNodes: new Set(), history: [],
    edges: [edge],
    _draw() {}, _cancelCameraAnimation() {},
    // Press on the source node; anything at x >= 180 is the target node.
    _hit(event) { return event.clientX < 40 ? source : event.clientX >= 180 ? target : null; },
    _hitEdge() { return null; }, _hideTooltip() {}, _showTooltip() {},
    _hitMindImage() { return null; }, _hitMindResize() { return null; }, _hitMindCollapse() { return null; },
    _hitNodeEdgeHandle() { return layout === "node" ? edge : null; },
    _world(x, y) { return { x, y }; },
    _saveNodeGraphState() { saves.push({ ...edge.routeBend }); },
  });
  graph._bind();
  return { graph, canvas: graph.canvas, window, edge, connects, saves };
}

// Bends are created inside the vm realm; compare them by value.
const plain = (value) => (value == null ? value : JSON.parse(JSON.stringify(value)));

const startLink = (canvas, props = {}) => {
  canvas.emit("pointerdown", { altKey: true, ...props });
  canvas.emit("pointermove", { clientX: 90, clientY: 60, ...props });
};

for (const layout of ["force", "mind"]) {
  test(`${layout}: release outside the canvas ends the link drag (window pointerup)`, async () => {
    const { graph, canvas, window, connects } = await fixture(layout);
    startLink(canvas);
    assert.ok(graph.linkDrag);
    // The canvas never sees the release (inspector, menu, outside the window).
    window.emit("pointerup", { clientX: 900, clientY: 300, buttons: 0 });
    assert.equal(graph.linkDrag, null);
    canvas.emit("pointermove", { clientX: 120, clientY: 80, buttons: 0 });
    assert.equal(graph.linkDrag, null, "the line must not follow a hovering mouse");
    assert.deepEqual(connects, []);
    assert.equal(window.count("pointerup") + window.count("blur") + window.count("keydown"), 0);
  });

  test(`${layout}: a hovering mouse with no buttons ends a link drag whose release was lost`, async () => {
    const { graph, canvas, connects } = await fixture(layout);
    startLink(canvas);
    canvas.emit("pointermove", { clientX: 200, clientY: 0, buttons: 0 });
    assert.equal(graph.linkDrag, null);
    assert.deepEqual(connects, [], "a hover over a node is not a drop");
  });

  for (const [name, finish] of [
    ["window blur", ({ window }) => window.emit("blur")],
    ["Escape", ({ window }) => window.emit("keydown", { key: "Escape" })],
    ["lostpointercapture", ({ canvas }) => canvas.emit("lostpointercapture")],
    ["pointercancel", ({ canvas }) => canvas.emit("pointercancel", { clientX: 200 })],
  ]) {
    test(`${layout}: ${name} cancels the link drag without creating a link`, async () => {
      const f = await fixture(layout);
      startLink(f.canvas);
      f.canvas.emit("pointermove", { clientX: 200, clientY: 0 });
      finish(f);
      assert.equal(f.graph.linkDrag, null);
      f.canvas.emit("pointerup", { clientX: 200, clientY: 0, buttons: 0 });
      assert.deepEqual(f.connects, []);
    });
  }

  for (const pointerType of ["mouse", "pen"]) {
    test(`${layout}: ${pointerType} release over a node connects once`, async () => {
      const { graph, canvas, window, connects } = await fixture(layout);
      startLink(canvas, { pointerType });
      const up = { clientX: 200, clientY: 0, buttons: 0, pointerType };
      canvas.emit("pointerup", up);
      window.emit("pointerup", up); // The same event bubbling to the window.
      assert.equal(graph.linkDrag, null);
      await Promise.resolve();
      assert.deepEqual(connects, [[1, 2]]);
    });
  }
}

for (const pointerType of ["mouse", "pen", "touch"]) {
  test(`node graph: ${pointerType} route-handle drag ends on release and keeps the bend`, async () => {
    const { graph, canvas, edge, saves } = await fixture("node");
    canvas.emit("pointerdown", { pointerType, clientX: 100, clientY: 50 });
    canvas.emit("pointermove", { pointerType, clientX: 130, clientY: 70 });
    assert.deepEqual(plain(edge.routeBend), { x: 130, y: 70 });
    canvas.emit("pointerup", { pointerType, clientX: 130, clientY: 70, buttons: 0 });
    assert.equal(graph.edgeRouteDrag, null);
    canvas.emit("pointermove", { pointerType, clientX: 300, clientY: 300, buttons: 0 });
    assert.deepEqual(plain(edge.routeBend), { x: 130, y: 70 }, "the line no longer follows the pointer");
    assert.equal(saves.length, 1);
    graph.history.pop().undo();
    assert.equal(edge.routeBend, undefined);
  });
}

test("node graph: release outside the canvas ends the route drag", async () => {
  const { graph, canvas, window, edge } = await fixture("node");
  canvas.emit("pointerdown", { clientX: 100, clientY: 50 });
  canvas.emit("pointermove", { clientX: 140, clientY: 90 });
  window.emit("pointerup", { clientX: 1300, clientY: 400, buttons: 0 });
  assert.equal(graph.edgeRouteDrag, null);
  canvas.emit("pointermove", { clientX: 10, clientY: 10, buttons: 0 });
  assert.deepEqual(plain(edge.routeBend), { x: 140, y: 90 });
});

test("node graph: Escape restores the previous bend", async () => {
  const { graph, canvas, window, edge, saves } = await fixture("node");
  edge.routeBend = { x: 5, y: 6 };
  canvas.emit("pointerdown", { clientX: 100, clientY: 50 });
  canvas.emit("pointermove", { clientX: 140, clientY: 90 });
  window.emit("keydown", { key: "Escape" });
  assert.equal(graph.edgeRouteDrag, null);
  assert.deepEqual(plain(edge.routeBend), { x: 5, y: 6 });
  assert.equal(saves.length, 0);
});
