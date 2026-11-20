// Local graph clicks (card vs re-centre) and the centre-history rings.
// Run: node --experimental-vm-modules --test tests/js/local_graph_interaction.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

const root = new URL("../../ark_studio/web/", import.meta.url);

async function loadModel() {
  const source = await readFile(new URL("js/local-graph-model.js", root), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: "local-graph-model.js" });
  await module.link(() => { throw new Error("local-graph-model must stay dependency-free"); });
  await module.evaluate();
  return module.namespace;
}

const m = await loadModel();
const node = (id) => ({ id, uid: `U-${id}` });
const up = (x = 10, y = 10, pointerType = "mouse") => ({ type: "pointerup", pointerType, clientX: x, clientY: y });

test("single click pins the card, a second click on the same node re-centres", () => {
  const taps = m.createTapClassifier();
  assert.equal(m.localGraphClickAction(taps, node(1), false, up(), 1000), "card");
  assert.equal(m.localGraphClickAction(taps, node(1), false, up(12, 11), 1200), "recenter");
  // The browser's dblclick that follows is swallowed (no second re-centre).
  assert.equal(m.localGraphClickAction(taps, node(1), true, { type: "dblclick" }, 1210), "none");
  // The re-centred layout may put another node under the pointer before
  // the dblclick arrives: it is still swallowed.
  assert.equal(m.localGraphClickAction(taps, node(2), false, up(), 1300), "card");
  assert.equal(m.localGraphClickAction(taps, node(2), false, up(), 1400), "recenter");
  assert.equal(m.localGraphClickAction(taps, node(9), true, { type: "dblclick" }, 1410), "none");
  // A third click starts over: card again.
  assert.equal(m.localGraphClickAction(taps, node(1), false, up(), 1500), "card");
});

test("slow clicks, other nodes and far clicks stay single", () => {
  const taps = m.createTapClassifier();
  assert.equal(m.localGraphClickAction(taps, node(1), false, up(), 0), "card");
  assert.equal(m.localGraphClickAction(taps, node(1), false, up(), m.DOUBLE_TAP_MS + 50), "card");
  assert.equal(m.localGraphClickAction(taps, node(2), false, up(), m.DOUBLE_TAP_MS + 100), "card");
  assert.equal(m.localGraphClickAction(taps, node(2), false, up(10 + m.DOUBLE_TAP_SLOP * 2, 10), m.DOUBLE_TAP_MS + 150), "card");
});

test("a dblclick the tap window missed (slow OS double-click) still re-centres once", () => {
  const taps = m.createTapClassifier();
  assert.equal(m.localGraphClickAction(taps, node(3), false, up(), 0), "card");
  assert.equal(m.localGraphClickAction(taps, node(3), false, up(), 450), "card");
  assert.equal(m.localGraphClickAction(taps, node(3), true, { type: "dblclick" }, 460), "recenter");
});

test("touch: tap = card, double-tap = re-centre", () => {
  const taps = m.createTapClassifier();
  assert.equal(m.localGraphClickAction(taps, node(4), false, up(50, 50, "touch"), 0), "card");
  assert.equal(m.localGraphClickAction(taps, node(4), false, up(58, 44, "touch"), 280), "recenter");
});

test("Enter / context menu re-centre at once; empty clicks close the card", () => {
  const taps = m.createTapClassifier();
  assert.equal(m.localGraphClickAction(taps, node(5), false, { type: "keydown", key: "Enter" }, 0), "recenter");
  assert.equal(m.localGraphClickAction(taps, node(5), false, { type: "click" }, 0), "recenter");
  assert.equal(m.localGraphClickAction(taps, node(5), false, undefined, 0), "recenter");
  assert.equal(m.localGraphClickAction(taps, null, false, up(), 0), "close");
  assert.equal(m.localGraphClickAction(taps, null, false, { type: "keydown" }, 0), "none");
  // An empty click between two node clicks breaks the double.
  assert.equal(m.localGraphClickAction(taps, node(6), false, up(), 100), "card");
  m.localGraphClickAction(taps, null, false, up(), 150);
  assert.equal(m.localGraphClickAction(taps, node(6), false, up(), 200), "card");
});

test("centre history: most recent first, deduplicated, capped at 4", () => {
  let history = [];
  let last = null;
  for (const id of [1, 2, 3, 4, 5, 6]) {
    history = m.pushCenterHistory(history, last, id);
    last = id;
  }
  assert.deepEqual(history, [5, 4, 3, 2]);
  // Returning to 3 removes it from the history (it is the centre now) and
  // 6 becomes the most recent step.
  history = m.pushCenterHistory(history, 6, 3);
  assert.deepEqual(history, [6, 5, 4, 2]);
  // Going back to 6 moves it out again; 3 keeps only its newest position.
  history = m.pushCenterHistory(history, 3, 6);
  assert.deepEqual(history, [3, 5, 4, 2]);
  // String / number ids compare equal; no previous centre adds nothing.
  assert.deepEqual(m.pushCenterHistory([1, 2], "2", 3), ["2", 1]);
  assert.deepEqual(m.pushCenterHistory([1], null, 2), [1]);
  assert.deepEqual(m.pushCenterHistory([1], 2, 2), [1]);
  assert.equal(m.CENTER_HISTORY_MAX, 4);
});

test("ring opacities fade per step and only visible nodes get one", () => {
  assert.deepEqual([0, 1, 2, 3, 4].map((step) => m.centerRingAlpha(step)), [0.8, 0.55, 0.35, 0.2, 0]);
  assert.equal(m.centerRingAlpha(-1), 0);
  const nodes = [{ id: 10 }, { id: 11 }, { id: 13 }, { id: 14 }];
  const rings = m.centerRings([11, 12, 13, 14, 15], nodes, 10);
  // 12 is not in the local graph: its step still counts for the fade.
  assert.deepEqual([...rings], [[11, 0.8], [13, 0.35], [14, 0.2]]);
  // Never a ring on the current centre; ids from Details may be strings.
  assert.deepEqual([...m.centerRings(["10", "11"], nodes, 10)], [[11, 0.55]]);
  for (const alpha of m.CENTER_RING_ALPHAS) assert.ok(alpha > 0 && alpha <= 1);
});

test("the Local graph view routes clicks through the model and draws the rings", async () => {
  const view = await readFile(new URL("js/local-graph-view.js", root), "utf8");
  assert.match(view, /onSelect: onNodeSelect/);
  assert.match(view, /localGraphClickAction\(taps, node, open, event, now\)/);
  // Rings follow Settings › Appearance › Graphs › Local graph (on by default).
  assert.match(view, /view\.rings = getMindSettings\(\)\.localRings === false \? new Map\(\) : centerRings\(centerHistory, input\.nodes, data\.center\.id\)/);
  // Double click re-centres; only Alt+Enter (openInEditor) opens the Editor.
  const recenterSrc = view.slice(view.indexOf("function recenter"), view.indexOf("function openInEditor"));
  assert.ok(recenterSrc.length > 0);
  assert.doesNotMatch(recenterSrc, /openUnitInEditor/);
  const graph = await readFile(new URL("js/graph.js", root), "utf8");
  for (const name of ["showNodeCard(node, extra = null)", "hideNodeCard()", "setRings(rings)"]) assert.ok(graph.includes(`  ${name} {`), name);
  assert.match(graph, /this\.rings\?\.get\(node\.id\)/);
});

test("Alt+Enter in the Local graph opens the card's node (else the centre) in the Editor", async () => {
  const { readFile } = await import("node:fs/promises");
  const src = await readFile(new URL("../../ark_studio/web/js/local-graph-view.js", import.meta.url), "utf8");
  assert.match(src, /onOpenEditor:\s*openInEditor/);
  assert.match(src, /const node = cardNodeObj \|\| selected;/);
  assert.match(src, /ctx\.openUnitInEditor\(node\)/);
});

test("Local graph click opens the shared floating inspector preview at page level", async () => {
  const src = await readFile(new URL("js/local-graph-view.js", root), "utf8");
  assert.match(src, /createInspectorPreview\(ctx, unit, point, \(\) => closeCard\(\)\)/);
  assert.match(src, /getElementById\("overlay-root"\)/);
  assert.doesNotMatch(src, /showNodeCard\(/);
  assert.match(src, /addEventListener\("focusout"/);
});
