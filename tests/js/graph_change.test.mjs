// Run: node --experimental-vm-modules --test tests/js/graph_change.test.mjs
// T-0037: a change that does not alter what a graph holds (assignee, dates,
// body, ...) or only alters how one node/edge is drawn (title, type,
// status) must not rebuild, relayout, refit or reheat the graph.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";
import {
  assignNodeData, changedFields, classifyEdgeChange, classifyGraphUpdate, classifyNodeChange,
  DRAWN_NODE_FIELDS, maxChange,
} from "../../ark_studio/web/js/graph-change.js";
import { loadGraphChange, loadGraphCore } from "./load_graph_core.mjs";

const node = (id, extra = {}) => ({
  id, uid: `N-${id}`, title: `Node ${id}`, type: "task", status: "open", importance: "p2",
  tags: [], assignee: null, start_at: null, end_at: null, ...extra,
});

test("attribute-only fields are data changes", () => {
  for (const patch of [
    { assignee: "alice" }, { assignee_effective: "bob" }, { start_at: "2026-11-20" }, { end_at: "2026-12-01" },
    { importance: "p0" }, { body: "new text" }, { updated_at: "2026-11-17T10:00:00Z" }, { excerpt: "x" },
  ]) {
    const result = classifyNodeChange(node(1), node(1, patch));
    assert.equal(result.kind, "data", JSON.stringify(patch));
    assert.deepEqual(result.fields, Object.keys(patch));
  }
});

test("drawn fields are visual changes", () => {
  for (const patch of [
    { title: "Renamed" }, { type: "claim" }, { status: "blocked" }, { tags: ["!urgent"] },
    { member_count: 4 }, { degree: 3 },
  ]) {
    assert.equal(classifyNodeChange(node(1), node(1, patch)).kind, "visual", JSON.stringify(patch));
  }
  // Mixed: the drawn field wins.
  assert.equal(classifyNodeChange(node(1), node(1, { assignee: "a", title: "t" })).kind, "visual");
});

test("importance becomes visual only for a view that draws it", () => {
  const drawn = new Set([...DRAWN_NODE_FIELDS, "importance"]);
  assert.equal(classifyNodeChange(node(1), node(1, { importance: "p0" })).kind, "data");
  assert.equal(classifyNodeChange(node(1), node(1, { importance: "p0" }), { drawn }).kind, "visual");
});

test("graph-owned fields (positions, card geometry, caches) never count", () => {
  const held = { ...node(1), x: 10, y: 20, vx: 1, w: 160, titleW: 140, _cardMemo: {}, color: "#fff" };
  assert.equal(classifyNodeChange(held, node(1)).kind, "none");
  assert.deepEqual(changedFields(held, { ...node(1), x: 999, _wrap: 1 }), []);
  const target = { ...held };
  assignNodeData(target, { ...node(1, { assignee: "z" }), x: 0, y: 0, w: 1 });
  assert.equal(target.x, 10);
  assert.equal(target.w, 160);
  assert.equal(target.assignee, "z");
});

test("equal arrays/objects are no change; same shape with nothing new is none", () => {
  assert.equal(classifyNodeChange(node(1, { tags: ["a"] }), node(1, { tags: ["a"] })).kind, "none");
  const edges = [{ id: 7, source: 1, target: 2, relation: "supports" }];
  const result = classifyGraphUpdate({ nodes: [node(1), node(2)], edges }, { nodes: [node(2), node(1)], edges: edges.map((e) => ({ ...e })) });
  assert.equal(result.kind, "none");
});

test("graph update: added/removed nodes or links are structure", () => {
  const cur = { nodes: [node(1), node(2)], edges: [{ id: 7, source: 1, target: 2, relation: "supports" }] };
  assert.equal(classifyGraphUpdate(cur, { nodes: [node(1)], edges: [] }).kind, "structure");
  assert.equal(classifyGraphUpdate(cur, { nodes: [node(1), node(3)], edges: [] }).kind, "structure");
  assert.equal(classifyGraphUpdate(cur, { nodes: cur.nodes, edges: [] }).kind, "structure");
  assert.equal(classifyGraphUpdate(cur, { nodes: cur.nodes, edges: [...cur.edges, { id: 8, source: 2, target: 1, relation: "x" }] }).kind, "structure");
  // Re-pointed link with the same id.
  assert.equal(classifyGraphUpdate(cur, { nodes: cur.nodes, edges: [{ id: 7, source: 2, target: 1, relation: "supports" }] }).kind, "structure");
  // Links to nodes outside the graph are ignored (setData drops them too).
  assert.equal(classifyGraphUpdate(cur, { nodes: cur.nodes, edges: [...cur.edges, { id: 9, source: 1, target: 99 }] }).kind, "none");
});

test("graph update: per-node and per-edge kinds, the largest wins", () => {
  const cur = { nodes: [node(1), node(2), node(3)], edges: [{ id: 7, source: 1, target: 2, relation: "supports" }] };
  const assigned = classifyGraphUpdate(cur, { nodes: [node(1, { assignee: "a" }), node(2), node(3)], edges: cur.edges });
  assert.equal(assigned.kind, "data");
  assert.deepEqual(assigned.nodes, [{ id: 1, kind: "data", fields: ["assignee"] }]);
  const renamed = classifyGraphUpdate(cur, { nodes: [node(1, { assignee: "a" }), node(2, { title: "x" }), node(3)], edges: cur.edges });
  assert.equal(renamed.kind, "visual");
  assert.deepEqual(renamed.nodes.map((n) => [n.id, n.kind]), [[1, "data"], [2, "visual"]]);
  const relation = classifyGraphUpdate(cur, { nodes: cur.nodes, edges: [{ id: 7, source: 1, target: 2, relation: "refutes" }] });
  assert.equal(relation.kind, "visual");
  assert.equal(relation.edges[0].key, "#7");
  // Mind/tree: a relation change re-shapes the tree.
  assert.equal(classifyGraphUpdate(cur, { nodes: cur.nodes, edges: [{ id: 7, source: 1, target: 2, relation: "refutes" }] },
    { structuralRelations: () => true }).kind, "structure");
  assert.equal(classifyEdgeChange({ id: 1, source: 1, target: 2, note: "a" }, { id: 1, source: 1, target: 2, note: "b" }).kind, "data");
  assert.equal(maxChange("data", "visual"), "visual");
  assert.equal(maxChange("structure", "data"), "structure");
});

// GraphView with the real graph-change.js; every other import stubbed
// (as in graph_origin.test.mjs).  Counts relayouts, fits and starts.
async function graphFixture(layout = "force") {
  const context = vm.createContext({ performance, localStorage: { setItem() {} } });
  const core = await loadGraphCore(context);
  const graphChange = await loadGraphChange(context);
  const module = new vm.SourceTextModule(
    await readFile(new URL("../../ark_studio/web/js/graph.js", import.meta.url), "utf8"), { context },
  );
  await module.link(async (specifier, parent) => {
    if (specifier === "./graph-core.js") return core;
    if (specifier === "./graph-change.js") return graphChange;
    const source = await readFile(new URL(`../../ark_studio/web/js/${specifier.slice(2)}`, import.meta.url), "utf8");
    const names = [...new Set([...source.matchAll(/export (?:async )?(?:function|class|const|let) (\w+)/g)].map((m) => m[1]))];
    const values = {
      clamp: (n, min, max) => Math.max(min, Math.min(max, n)),
      getMindSettings: () => ({ nodeTransitions: false }),
      cardMetrics: (n) => ({ titleW: 40 + n.title.length * 7, imageW: 0, contentH: 30, h: 30, w: 40 + n.title.length * 7, images: [], image: null, pinned: null }),
      applyCardMetrics: (n, m) => Object.assign(n, { titleW: m.titleW, w: m.w, h: m.h }),
    };
    return new vm.SyntheticModule(names, function () {
      for (const name of names) this.setExport(name, values[name] || (() => ({})));
    }, { context: parent.context });
  });
  await module.evaluate();
  const graph = Object.create(module.namespace.GraphView.prototype);
  const calls = { draw: 0, layout: 0, start: 0, warm: 0, fit: 0 };
  Object.assign(graph, {
    id: `change-${layout}`, opts: { persistLayout: false }, layout,
    nodes: [], edges: [], byId: new Map(), visibleRelations: new Set(["part_of"]), selectedNodes: new Set(),
    collapsed: new Set(), pan: { x: 0, y: 0 }, zoom: 1, width: 1000, height: 800, alpha: 0,
    physics: { repulsion: 0, distance: 0, gravity: 0.02, damping: 0.3 },
    _draw() { calls.draw += 1; }, _start() { calls.start += 1; }, _warm() { calls.warm += 1; },
    _applyLayout() { calls.layout += 1; }, _layoutNodeGraph() { calls.layout += 1; },
    fit() { calls.fit += 1; }, _rebuildLegend() {}, _cancelCameraAnimation() {}, _syncNodeTools() {},
  });
  return { graph, calls };
}

const NODES = [node(1, { x: -50, y: 10 }), node(2, { x: 60, y: -20 }), node(3, { x: 0, y: 90 })];
const EDGES = [{ id: 11, source: 1, target: 2, relation: "supports" }, { id: 12, source: 2, target: 3, relation: "part_of" }];
const snapshotNodes = (patch = {}) => NODES.map((n) => {
  const { x, y, ...data } = n;
  return { ...data, ...(patch[n.id] || {}) };
});

for (const layout of ["force", "mind"]) {
  test(`${layout}: remote setData with an assignee change patches in place`, async () => {
    const { graph, calls } = await graphFixture(layout);
    graph.setData(NODES, EDGES, { layout });
    const before = graph.nodes.map((n) => [n.id, n.x, n.y]);
    const objects = [...graph.nodes];
    graph.zoom = 1.7; graph.pan = { x: 33, y: -12 };
    Object.keys(calls).forEach((key) => { calls[key] = 0; });
    graph.setData(snapshotNodes({ 1: { assignee: "alice", end_at: "2026-12-01" } }), EDGES.map((e) => ({ ...e })),
      { layout, preserveCamera: true, remote: true, animate: true });
    assert.deepEqual(calls, { draw: 0, layout: 0, start: 0, warm: 0, fit: 0 });
    assert.deepEqual(graph.nodes.map((n) => [n.id, n.x, n.y]), before);
    assert.ok(graph.nodes.every((n, i) => n === objects[i]), "same node objects");
    assert.equal(graph.byId.get(1).assignee, "alice");
    assert.equal(graph.zoom, 1.7);
    assert.deepEqual(graph.pan, { x: 33, y: -12 });
    assert.equal(graph.counts.patch, 1);
  });

  test(`${layout}: a title change redraws that node only`, async () => {
    const { graph, calls } = await graphFixture(layout);
    graph.setData(NODES, EDGES, { layout });
    const before = graph.nodes.map((n) => [n.id, n.x, n.y]);
    graph.byId.get(2)._forceLabelLayout = { key: "old" };
    graph.byId.get(2)._cardMemo = { hash: "old" };
    graph.byId.get(2).w = 80;
    Object.keys(calls).forEach((key) => { calls[key] = 0; });
    graph.setData(snapshotNodes({ 2: { title: "A much longer title" } }), EDGES, { layout, preserveCamera: true, remote: true });
    assert.deepEqual(calls, { draw: 1, layout: 0, start: 0, warm: 0, fit: 0 });
    assert.deepEqual(graph.nodes.map((n) => [n.id, n.x, n.y]), before);
    const renamed = graph.byId.get(2);
    assert.equal(renamed.title, "A much longer title");
    assert.equal(renamed._forceLabelLayout, null);
    assert.equal(renamed._cardMemo, null);
    if (layout === "mind") assert.equal(renamed.w, 40 + "A much longer title".length * 7, "card re-measured in place");
  });
}

test("a new node still goes through the full setData (remote: camera kept)", async () => {
  const { graph, calls } = await graphFixture("force");
  graph.setData(NODES, EDGES, { layout: "force" });
  graph.zoom = 2; graph.pan = { x: 5, y: 6 };
  Object.keys(calls).forEach((key) => { calls[key] = 0; });
  graph.setData([...snapshotNodes(), node(4)], EDGES, { layout: "force", preserveCamera: true, remote: true });
  assert.equal(graph.nodes.length, 4);
  assert.equal(calls.fit, 0);
  assert.equal(graph.zoom, 2);
  assert.equal(graph.byId.get(1).x, -50, "existing nodes keep their place");
});

test("an edge relation change updates the edge in place (force)", async () => {
  const { graph, calls } = await graphFixture("force");
  graph.setData(NODES, EDGES, { layout: "force" });
  const edge = graph.edges[0];
  Object.keys(calls).forEach((key) => { calls[key] = 0; });
  graph.setData(snapshotNodes(), [{ ...EDGES[0], relation: "refutes" }, EDGES[1]], { layout: "force", preserveCamera: true, remote: true });
  assert.equal(graph.edges[0], edge);
  assert.equal(edge.relation, "refutes");
  assert.deepEqual(calls, { draw: 1, layout: 0, start: 0, warm: 0, fit: 0 });
});

test("patchNode: a saved unit updates its node without adding unit-only fields", async () => {
  const { graph, calls } = await graphFixture("force");
  graph.setData(NODES, EDGES, { layout: "force" });
  Object.keys(calls).forEach((key) => { calls[key] = 0; });
  const unit = { ...snapshotNodes()[0], assignee: "bob", links_in: [{ id: 1 }], history: [] };
  assert.equal(graph.patchNode(unit), "data");
  assert.equal(graph.byId.get(1).assignee, "bob");
  assert.equal("links_in" in graph.byId.get(1), false);
  assert.equal(calls.draw, 0);
  assert.equal(graph.patchNode({ ...unit, status: "done" }), "visual");
  assert.equal(calls.draw, 1);
  assert.equal(graph.patchNode({ ...unit, id: 99 }), null);
  assert.deepEqual([calls.layout, calls.start, calls.fit], [0, 0, 0]);
});
