// Run: node --experimental-vm-modules --test tests/js/graph_mind_interactions.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";
import { loadGraphCore, loadNodeGraphLayout, loadNodeCard, loadGraphSprites, loadMindLayout } from "./load_graph_core.mjs";

async function fixture(layout = "mind") {
  const calls = [];
  const settings = { mindMarkerScale: 0.7, showMindImages: false };
  const context = vm.createContext({ window: {}, document: {} });
  const core = await loadGraphCore(context);
  const graphModule = new vm.SourceTextModule(
    await readFile(new URL("../../ark_studio/web/js/graph.js", import.meta.url), "utf8"), { context },
  );
  const imports = {
    "./dom.js": {
      h() {}, clear() {}, copyText() {}, typeColor: () => "#60a5fa", statusColor: () => "#14b8a6",
      clamp: (n, min, max) => Math.max(min, Math.min(max, n)),
    },
    "./i18n.js": { t: (s) => s, typeLabel: (s) => s, statusLabel() {}, relLabel() {} },
    "./icons.js": { icon() {}, drawStatusIcon: (...args) => calls.push(["status", ...args.slice(1)]) },
    "./physics.js": { normalizeGraphPhysics: (s) => s, GRAPH_DEFAULTS: {} },
    "./mind-settings.js": { getMindSettings: () => { calls.push(["settings"]); return settings; } },
    "./shortcuts.js": { getTagShortcuts: () => ({}), shortcutMatches() {} },
    "./context-menu.js": { openContextMenu() {} },
    "./api.js": { authenticatedUrl() {} },
    "./graph-force.js": { applyRepulsion() {} },
    "./assign-menu.js": { assignMenuItem() {} },
    "./export-menu.js": { exportBundleMenuItem() {} },
    "./node-history.js": { historyMenuItem() {} },
  };
  const i18n = new vm.SyntheticModule(Object.keys(imports["./i18n.js"]), function () {
    for (const [name, value] of Object.entries(imports["./i18n.js"])) this.setExport(name, value);
  }, { context });
  const markers = new vm.SourceTextModule(
    await readFile(new URL("../../ark_studio/web/js/markers.js", import.meta.url), "utf8"), { context },
  );
  await markers.link(() => i18n);
  const nodeLayout = await loadNodeGraphLayout(context);
  const nodeCard = await loadNodeCard(context);
  const graphSprites = await loadGraphSprites(context);
  const mindLayoutModule = await loadMindLayout(context, nodeCard);
  await graphModule.link((specifier) => specifier === "./graph-core.js" ? core : specifier === "./node-graph-layout.js" ? nodeLayout : specifier === "./node-card.js" ? nodeCard : specifier === "./graph-sprites.js" ? graphSprites : specifier === "./mind-layout.js" ? mindLayoutModule : specifier === "./markers.js" ? markers
    : specifier === "./i18n.js" ? i18n
      : new vm.SyntheticModule(Object.keys(imports[specifier]), function () {
        for (const [name, value] of Object.entries(imports[specifier])) this.setExport(name, value);
      }, { context }));
  await graphModule.evaluate();
  const graph = Object.create(graphModule.namespace.GraphView.prototype);
  Object.assign(graph, {
    layout, mode: "read", nodes: [], edges: [], byId: new Map(), collapsed: new Set(),
    selectedNodes: new Set(), selected: null, primaryRelation: "part_of", opts: {},
    visibleRelations: new Set(["part_of"]), pointers: new Map(), width: 800, height: 600,
    zoom: 1, pan: { x: 0, y: 0 }, history: [],
    canvas: { style: {}, getBoundingClientRect: () => ({ left: 0, top: 0 }) }, host: {},
    _transform: () => ({ x: 0, y: 0, z: 1 }), _draw() {}, _cancelCameraAnimation() {},
    _hitMindImage() {}, _hitMindResize() {}, _hitMindCollapse() {}, _hitNodeEdgeHandle() {},
    _hitEdge() {}, _positionEditor() {},
    _applyLayout: () => calls.push(["layout"]),
    _keepNodeScreen: (node) => calls.push(["anchor", node.id]),
  });
  const load = (nodes, edges = []) => {
    graph.nodes = nodes;
    graph.byId = new Map(nodes.map((n) => [n.id, n]));
    graph.edges = edges.map(([source, target]) => ({ source, target, relation: "part_of" }));
  };
  return { graph, load, calls, settings };
}

function key(graph, value) {
  const event = { key: value, preventDefault() { this.prevented = true; } };
  graph._onKeyDown(event);
  assert.equal(event.prevented, true);
}

test("+/- and */ apply to all selected branches in read and edit modes", async () => {
  for (const mode of ["read", "edit"]) {
    const { graph, load, calls } = await fixture();
    graph.mode = mode;
    load(["a", "b", "a1", "b1", "a2", "b2"].map((id) => ({ id, x: 0, y: 0 })),
      [["a1", "a"], ["a2", "a1"], ["b1", "b"], ["b2", "b1"]]);
    graph._setSelection([graph.byId.get("a"), graph.byId.get("b")]);
    key(graph, "-");
    assert.deepEqual([...graph.collapsed].sort(), ["a", "b"]);
    assert.equal(graph._mindTree().hidden.size, 4);
    key(graph, "+");
    assert.equal(graph.collapsed.size, 0);
    key(graph, "/");
    assert.equal(graph.collapsed.size, 6);
    key(graph, "=");
    assert.deepEqual([...graph.collapsed].sort(), ["a1", "a2", "b1", "b2"]);
    key(graph, "*");
    assert.equal(graph.collapsed.size, 0);
    assert.equal(graph._mindTree().hidden.size, 0);
    assert.deepEqual([...graph.selectedNodes], ["a", "b"]);
    assert.equal(calls.filter(([name]) => name === "layout").length, 5);
  }
});

test("overlapping parent/child selections collapse once; folding re-lays out without moving the camera", async () => {
  const { graph, load, calls } = await fixture();
  load(["child", "parent", "grandchild", "other"].map((id) => ({ id, x: 0, y: 0 })),
    [["child", "parent"], ["grandchild", "child"]]);
  graph._setSelection(graph.nodes);
  key(graph, "/");
  assert.deepEqual([...graph.selectedNodes], ["parent", "other"]);
  assert.equal(graph.collapsed.size, 4);
  assert.equal(graph.selected, "other");
  assert.deepEqual(calls.at(-1), ["layout"]);
  assert.equal(calls.some(([name]) => name === "anchor"), false);
  key(graph, "*");
  assert.equal(graph.collapsed.size, 0);
  assert.equal(graph._mindTree().hidden.size, 0);
});

function box(graph, startX, startY, currentX, currentY, contain = false, additive = false) {
  graph.selectBox = { startX, startY, currentX, currentY, moved: true, contain, additive };
  graph._finishSelectBox();
  return [...graph.selectedNodes].sort();
}

for (const layout of ["mind", "node"]) {
  test(`${layout}: grazing cards, reverse drags, full containment and zoom/pan`, async () => {
    const { graph, load } = await fixture(layout);
    load([{ id: "n", x: 100, y: 100, w: 80, h: 40 }]);
    assert.deepEqual(box(graph, 58, 78, 62, 82), ["n"]);
    assert.deepEqual(box(graph, 62, 82, 58, 78), ["n"]);
    assert.deepEqual(box(graph, 90, 90, 110, 110, true), []);
    assert.deepEqual(box(graph, 60, 80, 140, 120, true), ["n"]);
    assert.deepEqual(box(graph, 0, 0, 59, 79), []);
    graph._transform = () => ({ x: 23, y: 17, z: 2 });
    assert.deepEqual(box(graph, 141, 175, 145, 179), ["n"]);
    assert.deepEqual(box(graph, 141, 175, 145, 179, true), []);
    assert.deepEqual(box(graph, 143, 177, 303, 257, true), ["n"]);
  });
}

test("force/tree: circle intersection excludes bounding-box corners; Shift needs the entire circle", async () => {
  for (const layout of ["force", "tree"]) {
    const { graph, load } = await fixture(layout);
    load([{ id: "n", x: 100, y: 100, radius: 10 }]);
    assert.deepEqual(box(graph, 89, 98, 91, 102), ["n"]);
    assert.deepEqual(box(graph, 89, 89, 91, 91), []);
    assert.deepEqual(box(graph, 95, 95, 105, 105, true), []);
    assert.deepEqual(box(graph, 90, 90, 110, 110, true), ["n"]);
  }
});

test("collapsed descendants never participate in box selection, including with stale hiddenIds", async () => {
  const { graph, load } = await fixture();
  load([{ id: "a", x: 100, y: 100 }, { id: "b", x: 100, y: 100 }], [["b", "a"]]);
  graph.collapsed.add("a");
  graph.hiddenIds = new Set();
  assert.deepEqual(box(graph, 0, 0, 200, 200), ["a"]);
});

test("pointer gestures latch Shift containment independently from Ctrl/Meta additive selection", async () => {
  for (const [shiftKey, ctrlKey, metaKey] of [[false, false, false], [true, false, false],
    [false, true, false], [true, true, false], [true, false, true]]) {
    const { graph, load } = await fixture();
    load([{ id: "n", x: 100, y: 100, w: 80, h: 40 }]);
    graph._pointerDown({ button: 0, pointerId: 1, clientX: 10, clientY: 10,
      shiftKey, ctrlKey, metaKey });
    assert.equal(Boolean(graph.selectBox.contain), shiftKey);
    assert.equal(Boolean(graph.selectBox.additive), ctrlKey || metaKey);
    graph._pointerMove({ pointerId: 1, clientX: 70, clientY: 90 });
    graph._pointerUp({ pointerId: 1, clientX: 70, clientY: 90 });
    assert.deepEqual([...graph.selectedNodes], shiftKey ? [] : ["n"]);
  }
});

test("Ctrl box keeps existing toggle behavior", async () => {
  const { graph, load } = await fixture();
  load([{ id: "a", x: 100, y: 100 }, { id: "b", x: 400, y: 400 }]);
  graph._setSelection(graph.nodes);
  assert.deepEqual(box(graph, 0, 0, 200, 200, false, true), ["b"]);
});

test("Mind status is always the first marker, including unmarked nodes, and follows marker scale", async () => {
  const { graph, load, calls, settings } = await fixture();
  const ctx = new Proxy({
    measureText: (text) => ({ width: String(text).length * 6 }),
    fillText: (...args) => calls.push(["text", ...args]),
  }, { get: (target, prop) => prop in target ? target[prop] : () => {} });
  graph.ctx = ctx;
  graph._palette = () => ({ card: "#ffffff", ink: "#111827", sub: "#64748b", accent: "#2563eb" });
  load([{ id: "a", title: "A", type: "note", status: "done", x: 200, y: 100, w: 160, h: 38 }]);
  graph._drawMind();
  assert.equal(calls.filter(([name]) => name === "status").length, 1);
  assert.equal(calls.find(([name]) => name === "status")[1], "done");
  const originalRadius = calls.find(([name]) => name === "status")[4];
  settings.mindMarkerScale = 1.4;
  graph.nodes[0].tags = ["marker:star-yellow", "marker:question", "marker:flag-red", "marker:check-green", "marker:priority"];
  calls.length = 0;
  graph._drawMind();
  const status = calls.find(([name]) => name === "status");
  assert.equal(status[4], originalRadius * 2);
  const texts = calls.filter(([name, text]) => name === "text" && ["★", "?", "⚑", "✓", "!"].includes(text));
  assert.equal(texts.length, 5);
  assert.ok(texts.every(([, , x, y]) => x > status[2] && y === status[3]));
  // Cards are painted in card-local world units (top-left at 0, 0).
  assert.ok(status[2] - status[4] >= 0 && texts.at(-1)[2] + status[4] <= 160);
});

test("Mind reads marker preferences once per frame and applies changes on the next draw", async () => {
  const { graph, load, calls, settings } = await fixture();
  graph.ctx = new Proxy({ measureText: (text) => ({ width: String(text).length * 6 }) }, {
    get: (target, prop) => prop in target ? target[prop] : () => {},
  });
  graph._palette = () => ({ card: "#ffffff", ink: "#111827", sub: "#64748b", accent: "#2563eb" });
  load(Array.from({ length: 30 }, (_, i) => ({ id: i, title: "Node", type: "note", status: "done",
    x: 200 + i, y: 100 + i, w: 160, h: 38, tags: ["marker:star-yellow"] })));
  graph._drawMind();
  assert.equal(calls.filter(([name]) => name === "settings").length, 1);
  const radius = calls.find(([name]) => name === "status")[4];
  settings.mindMarkerScale *= 2;
  calls.length = 0;
  graph._drawMind();
  assert.equal(calls.filter(([name]) => name === "settings").length, 1);
  assert.equal(calls.find(([name]) => name === "status")[4], radius * 2);
});

// Topic canvas opens a GraphView directly in "node"; its layout picker used to
// keep showing its first option, "Force", over a node graph.
for (const layout of ["node", "force", "mind"]) {
  test(`layout picker shows the ${layout} layout the view was created with`, async () => {
    const { graph } = await fixture(layout);
    const select = { value: "force" };
    const auto = { hidden: true, classList: { toggle(name, on) { auto.hidden = on; } } };
    graph.tools = { querySelector: (sel) => sel === "select.graph-sel" ? select : sel === ".graph-node-auto" ? auto : null };
    graph._syncNodeTools();
    assert.equal(select.value, layout);
    assert.equal(auto.hidden, layout === "force", "auto layout: node graph and Mind");
  });
}

// ---- Display-only link reversal ------------------------------------------
function linked(graph, triples) {
  graph.nodes = [...new Set(triples.flatMap(([a, b]) => [a, b]))].map((id) => ({ id, x: 0, y: 0, w: 200, h: 64 }));
  graph.byId = new Map(graph.nodes.map((n) => [n.id, n]));
  graph.edges = triples.map(([source, target, relation], i) => ({ id: i + 1, source, target, relation }));
  graph.edgeFilterKey = "test-graph";
  graph.relationTypes = ["part_of", "derived_from", "supports", "depends_on"];
}

test("derived_from and depends_on are shown reversed by default; stored edges never change", async () => {
  const { graph } = await fixture("node");
  linked(graph, [["paper", "data", "derived_from"], ["claim", "paper", "supports"], ["fig", "paper", "part_of"]]);
  const shown = graph._shownEdges();
  assert.deepEqual(shown.map((e) => [e.source, e.target, Boolean(e.shownReversed)]),
    [["data", "paper", true], ["claim", "paper", false], ["fig", "paper", false]]);
  assert.deepEqual(graph.edges.map((e) => [e.source, e.target]), [["paper", "data"], ["claim", "paper"], ["fig", "paper"]]);
  assert.equal(graph._isReversed("depends_on"), true);
});

test("project default, personal override and the quick toggle decide the shown direction", async () => {
  const { graph } = await fixture("node");
  linked(graph, [["paper", "data", "derived_from"], ["claim", "paper", "supports"]]);
  // Project setting turns the default off.
  graph.arrowSettings = { style: { head: "triangle", placement: "target", size: 1 }, relations: { derived_from: { reverse: false } }, mind_tree: false };
  assert.equal(graph._isReversed("derived_from"), false);
  // A personal override (whole arrows group) reverses supports instead.
  graph.arrowSettings = { style: { head: "open", placement: "target", size: 1 }, relations: { supports: { reverse: true } }, mind_tree: false };
  assert.equal(graph._isReversed("supports"), true);
  assert.equal(graph._isReversed("derived_from"), true, "missing entry keeps the built-in default");
  // The link filter's quick toggle wins for this graph, and is dropped when it matches the setting.
  graph._setQuickReverse("supports", false);
  assert.equal(graph._isReversed("supports"), false);
  assert.deepEqual({ ...graph._reverseOverrides() }, { supports: false });
  graph._setQuickReverse("supports", true);
  assert.deepEqual({ ...graph._reverseOverrides() }, {});
  assert.equal(graph._shownEdges()[1].source, "paper");
});

test("node layout input and mind hierarchy use the shown direction", async () => {
  const { graph } = await fixture("node");
  linked(graph, [["result", "dataset", "derived_from"], ["model", "result", "derived_from"]]);
  graph.visibleRelations = new Set(["derived_from"]);
  const positions = graph._nodeAutoPositions();
  const x = (id) => positions.get(id).x;
  assert.ok(x("dataset") < x("result") && x("result") < x("model"), "flows from the source material to the derived node");
  graph._setQuickReverse("derived_from", false);
  const stored = graph._nodeAutoPositions();
  assert.ok(stored.get("model").x < stored.get("dataset").x, "stored direction when the toggle is off");
  // Mind: with derived_from as the hierarchy, the source material is the parent.
  graph._setQuickReverse("derived_from", true);
  graph.layout = "mind";
  graph.primaryRelation = "derived_from";
  const { parentOf } = graph._mindTree();
  assert.equal(parentOf.get("dataset"), "result");
  assert.equal(parentOf.get("result"), "model");
});

// ---- Node cards share the Mind card model -------------------------------
test("node cards size like Mind cards; widths persist in the node layout store and survive Auto layout", async () => {
  const { graph } = await fixture("node");
  let stored = { positions: {}, bends: {}, widths: { "N-1": 360 } };
  graph.opts = { nodeLayoutStore: { load: () => stored, save: (state) => { stored = JSON.parse(JSON.stringify(state)); } } };
  graph.history = [];
  graph.fit = () => {};
  graph.visibleRelations = new Set(["part_of"]);
  graph.edgeFilterKey = "test-cards";
  const long = "A long title that wraps over several lines in a narrow card, then keeps going for a while longer";
  graph.nodes = [{ id: 1, uid: "N-1", title: long }, { id: 2, uid: "N-2", title: long }];
  graph.byId = new Map(graph.nodes.map((n) => [n.id, n]));
  graph.edges = [{ id: 9, source: 1, target: 2, relation: "part_of" }];
  graph._layoutNodeGraph(false);
  const [wide, auto] = graph.nodes;
  assert.equal(wide.w, 360, "stored width applied");
  assert.ok(auto.w < 360 && auto.h > wide.h, "the automatic card is narrower and taller");
  assert.equal(stored.widths["N-1"], 360);
  assert.equal(stored.widths["N-2"], null);
  // Resize (as the grip does on release) and undo.
  graph._setCardWidth(auto, 420);
  assert.equal(auto.w, 420);
  assert.equal(stored.widths["N-2"], 420);
  graph.history.push({ undo: () => graph._setCardWidth(auto, null) });
  await graph._undoLast();
  assert.equal(stored.widths["N-2"], null);
  assert.ok(auto.w < 420);
  // Auto layout replaces positions but keeps widths.
  graph._layoutNodeGraph(false, true);
  assert.equal(graph.nodes[0].w, 360);
  // The left neighbour's right edge sits left of the right card's left edge.
  const [a, b] = graph.nodes;
  assert.ok(a.x + a.w / 2 < b.x - b.w / 2 || b.x + b.w / 2 < a.x - a.w / 2, "layout uses real widths");
});

test("Mind auto layout releases dragged cards, re-lays out, and one undo restores them", async () => {
  const { graph, load } = await fixture("mind");
  delete graph._applyLayout; // the real layout
  Object.assign(graph, { history: [], fit() {}, _mindParents: () => null, _visibleMindNodes() { return this.nodes; } });
  load(["root", "a", "b", "orphan"].map((id) => ({ id, x: 0, y: 0, w: 120, h: 40 })), [["a", "root"], ["b", "root"]]);
  graph._applyLayout(false);
  const laidOut = new Map(graph.nodes.map((n) => [n.id, { x: n.x, y: n.y }]));
  // Drag "a" and the orphan: they keep their places through later layouts.
  const a = graph.byId.get("a"), orphan = graph.byId.get("orphan");
  a.x = 900; a.y = -400; a.mindManual = true;
  orphan.x = -700; orphan.y = 650; orphan.mindManual = true;
  graph._applyLayout(false);
  assert.deepEqual([a.x, a.y, orphan.x, orphan.y], [900, -400, -700, 650]);
  graph.autoLayout();
  for (const node of graph.nodes) {
    assert.equal(node.mindManual, undefined, `${node.id} released`);
    assert.deepEqual({ x: node.x, y: node.y }, laidOut.get(node.id), `${node.id} back in its slot`);
  }
  assert.equal(graph.history.length, 1, "one undo step");
  await graph._undoLast();
  assert.deepEqual([a.x, a.y, orphan.x, orphan.y], [900, -400, -700, 650]);
  assert.equal(a.mindManual, true);
  assert.equal(orphan.mindManual, true);
  assert.equal(graph.byId.get("b").mindManual, undefined);
});

function forest(graph, load) {
  delete graph._applyLayout; // the real layout
  Object.assign(graph, { history: [], fit() {}, _mindParents: () => null });
  // Tree A (root a, branch a1 with a1x/a1y, leaf a2), tree B, orphan o.
  load(["a", "a1", "a1x", "a1y", "a2", "b", "b1", "b2", "o"].map((id) => ({ id, x: 0, y: 0, w: 120, h: 40 })),
    [["a1", "a"], ["a2", "a"], ["a1x", "a1"], ["a1y", "a1"], ["b1", "b"], ["b2", "b"]]);
  graph._applyLayout(false);
  return () => new Map(graph.nodes.map((n) => [n.id, [n.x, n.y]]));
}

test("folding reflows only its tree, compactly, around the clicked card; undo restores", async () => {
  const { graph, load } = await fixture("mind");
  const at = forest(graph, load);
  const open = at();
  const a1 = graph.byId.get("a1"), a2 = graph.byId.get("a2");
  // A dragged card in tree B stays; one in tree A rejoins the layout.
  const b2 = graph.byId.get("b2");
  b2.x = 800; b2.y = 900; b2.mindManual = true;
  a2.x = -500; a2.y = -500; a2.mindManual = true;
  const dragged = at();
  graph._toggleNodeCollapse(a1);
  const folded = at();
  assert.deepEqual(folded.get("a1"), dragged.get("a1"), "the clicked card does not move");
  assert.equal(a2.mindManual, undefined, "a dragged card in the reflowed tree rejoins it");
  assert.ok(Math.abs((a2.y - a2.h / 2) - (a1.y + a1.h / 2) - 10) < 1e-9, "no gap under the folded branch");
  for (const id of ["b", "b1", "b2", "o"]) assert.deepEqual(folded.get(id), dragged.get(id), `${id} (other tree / orphan / dragged elsewhere) stays`);
  assert.equal(b2.mindManual, true);
  // Unfold: the tree opens around the same card again, nothing else moves.
  graph._toggleNodeCollapse(a1);
  const reopened = at();
  assert.deepEqual(reopened.get("a1"), dragged.get("a1"));
  for (const id of ["b", "b1", "b2", "o"]) assert.deepEqual(reopened.get(id), dragged.get(id));
  assert.equal(graph.history.length, 2);
  // Undo twice: back to the dragged positions (flags included).
  await graph._undoLast();
  await graph._undoLast();
  assert.deepEqual(at(), dragged);
  assert.equal(a2.mindManual, true);
  assert.equal(graph.collapsed.size, 0);
  // Auto layout: everything from scratch, dragged cards released.
  graph.autoLayout();
  assert.deepEqual(at(), open);
  assert.equal(b2.mindManual, undefined);
});

test("a reflowed tree that would overlap another tree moves just clear of it", async () => {
  const { graph, load } = await fixture("mind");
  const at = forest(graph, load);
  // Fold a1, then Auto layout (compact), then unfold: tree A grows into B.
  graph._toggleNodeCollapse(graph.byId.get("a1"));
  graph.autoLayout();
  const compact = at();
  graph._toggleNodeCollapse(graph.byId.get("a1"));
  const after = at();
  for (const id of ["b", "b1", "b2", "o"]) assert.deepEqual(after.get(id), compact.get(id), `${id} stays`);
  const box = (ids) => {
    const ns = ids.map((id) => graph.byId.get(id));
    return { y0: Math.min(...ns.map((n) => n.y - n.h / 2)), y1: Math.max(...ns.map((n) => n.y + n.h / 2)) };
  };
  const a = box(["a", "a1", "a1x", "a1y", "a2"]), b = box(["b", "b1", "b2"]);
  assert.ok(a.y1 + 28 <= b.y0 + 1e-9 || b.y1 + 28 <= a.y0 + 1e-9, "trees A and B do not overlap");
});

test("the auto layout button is shown for Mind and the node graph only", async () => {
  const { graph } = await fixture("mind");
  const classes = new Set(["hidden"]);
  const attrs = {};
  const button = { classList: { toggle: (name, on) => (on ? classes.add(name) : classes.delete(name)) }, setAttribute: (k, v) => { attrs[k] = v; } };
  graph.tools = { querySelector: (selector) => (selector === ".graph-node-auto" ? button : null) };
  for (const [layout, hidden, label] of [["mind", false, "graph.auto_layout_mind"], ["node", false, "graph.auto_layout_node"], ["force", true], ["tree", true]]) {
    graph.layout = layout;
    graph._syncNodeTools();
    assert.equal(classes.has("hidden"), hidden, layout);
    if (label) assert.equal(button.title, label);
  }
});

test("a data refresh keeps a dragged Mind card's position and flag", async () => {
  const { graph } = await fixture("mind");
  const proto = Object.getPrototypeOf(graph);
  assert.match(proto.setData.toString(), /if \(prev\.mindManual\) node\.mindManual = true;/);
});

// ---- Card media thumbnails ----------------------------------------------
test("thumbnail hit test finds each image thumbnail; the preview opens the node's media at it", async () => {
  for (const layout of ["mind", "node"]) {
    const { graph, settings } = await fixture(layout);
    settings.showMindImages = true;
    delete graph._hitMindImage; // the real hit test
    const previews = [];
    graph.opts = { onMediaPreview: (items, index, node) => previews.push({ ids: items.map((m) => m.id), index, node: node.id }) };
    graph._cardHidden = () => new Set();
    const media = [
      { id: 1, url: "a.png", is_image: true }, { id: 2, url: "b.jpg" }, { id: 3, url: "clip.mp4", kind: "video" }, { id: 4, url: "paper.pdf" },
    ];
    const node = { id: "n", x: 300, y: 200, w: 300, h: 60, titleW: 180, media };
    graph.nodes = [node];
    graph.byId = new Map([["n", node]]);
    // Thumbnails: 44 px squares 4 px right of the title box, centred vertically.
    const left = 300 - 150 + 180 + 4;
    const at = (x, y) => graph._hitMindImage({ clientX: x, clientY: y });
    assert.equal(at(left + 20, 200)?.item.id, 1, `${layout}: first thumbnail`);
    assert.equal(at(left + 48 + 20, 200)?.item.id, 2, `${layout}: second thumbnail`);
    assert.equal(at(left + 2 * 48 + 20, 200), null, `${layout}: videos have no thumbnail`);
    assert.equal(at(left - 20, 200), null, `${layout}: the title is not a thumbnail`);
    assert.equal(at(left + 20, 200 + 30), null, `${layout}: below the thumbnail strip`);
    graph._previewCardMedia(at(left + 48 + 20, 200));
    assert.deepEqual(previews.at(-1), { ids: [1, 2, 3, 4], index: 1, node: "n" }, "all the node's media, starting at the clicked one");
  }
});
