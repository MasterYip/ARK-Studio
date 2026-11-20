// Run: node --experimental-vm-modules --test tests/js/local_graph_model.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

async function loadModel() {
  const source = await readFile(new URL("../../ark_studio/web/js/local-graph-model.js", import.meta.url), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: "local-graph-model.js" });
  await module.link(() => { throw new Error("local-graph-model must stay dependency-free"); });
  await module.evaluate();
  return module.namespace;
}

const m = await loadModel();
// 1 -> 2 -> 3 -> 4 (supports), 5 -> 1 (part_of), 6 (task) -> 1 (relates)
const node = (id, type = "idea") => ({ id, uid: `U-${id}`, type, title: `n${id}` });
const snapshot = {
  nodes: [node(1), node(2), node(3), node(4), node(5, "claim"), node(6, "task"), node(7)],
  edges: [
    { id: 1, source: 1, target: 2, relation: "supports" },
    { id: 2, source: 2, target: 3, relation: "supports" },
    { id: 3, source: 3, target: 4, relation: "supports" },
    { id: 4, source: 5, target: 1, relation: "part_of" },
    { id: 5, source: 6, target: 1, relation: "relates" },
  ],
};
const ids = (result) => result.nodes.map((n) => n.id).sort((a, b) => a - b);

test("levels 1-3 grow the neighbourhood; levels are recorded", () => {
  assert.deepEqual(ids(m.neighbourhood(snapshot, 1, { depth: 1 })), [1, 2, 5, 6]);
  assert.deepEqual(ids(m.neighbourhood(snapshot, 1, { depth: 2 })), [1, 2, 3, 5, 6]);
  const three = m.neighbourhood(snapshot, 1, { depth: 3 });
  assert.deepEqual(ids(three), [1, 2, 3, 4, 5, 6]);
  assert.equal(three.nodes.find((n) => n.id === 4).level, 3);
  assert.equal(three.center.id, 1);
  assert.deepEqual(three.edges.map((e) => e.id).sort(), [1, 2, 3, 4, 5]);
  assert.deepEqual(m.neighbourhood(snapshot, 99), { nodes: [], edges: [], parents: new Map(), truncated: false, total: 0, center: null });
  assert.equal(m.normalizeOptions({ depth: 9 }).depth, 2);
  assert.equal(m.normalizeOptions({}).depth, 2); // default: two levels
});

test("direction, relation and node-type filters", () => {
  assert.deepEqual(ids(m.neighbourhood(snapshot, 1, { depth: 1, direction: "out" })), [1, 2]);
  assert.deepEqual(ids(m.neighbourhood(snapshot, 1, { depth: 1, direction: "in" })), [1, 5, 6]);
  assert.deepEqual(ids(m.neighbourhood(snapshot, 1, { depth: 1, hiddenRelations: ["supports"] })), [1, 5, 6]);
  assert.deepEqual(ids(m.neighbourhood(snapshot, 1, { depth: 1, hiddenTypes: ["task", "claim"] })), [1, 2]);
  // The centre is kept even when its own type is hidden.
  assert.deepEqual(ids(m.neighbourhood(snapshot, 6, { depth: 1, hiddenTypes: ["task"] })), [1, 6]);
  assert.deepEqual(m.relationsIn(snapshot), ["part_of", "relates", "supports"]);
  assert.deepEqual(m.typesIn(snapshot), ["claim", "idea", "task"]);
});

test("cap stops the walk and reports truncation", () => {
  const star = { nodes: [node(0)], edges: [] };
  for (let i = 1; i <= 50; i += 1) {
    star.nodes.push(node(i));
    star.edges.push({ id: i, source: 0, target: i, relation: "supports" });
  }
  const capped = m.neighbourhood(star, 0, { depth: 1 }, 10);
  assert.equal(capped.nodes.length, 10);
  assert.equal(capped.truncated, true);
  assert.equal(capped.total, 51);
  assert.equal(m.neighbourhood(star, 0, { depth: 1 }).truncated, false);
});

test("centre follows the Editor's node tab, then the selection", () => {
  assert.equal(m.centerFor({ tab: "editor", editorKey: "unit:U-3", detail: { id: 2 } }, snapshot), 3);
  assert.equal(m.centerFor({ tab: "map", editorKey: "unit:U-3", detail: { id: 2 } }, snapshot), 2);
  assert.equal(m.centerFor({ tab: "map", selectedId: 5 }, snapshot), 5);
  assert.equal(m.centerFor({ tab: "editor", editorKey: "ws:a.md" }, snapshot), null);
});

test("mind input: the focused node is the root, nodes hang under their BFS parent by level", () => {
  const data = m.neighbourhood(snapshot, 1, { depth: 3 });
  const mind = m.mindInput(data);
  assert.equal(mind.root, 1);
  assert.equal(mind.parents.has(1), false, "the root has no parent");
  assert.deepEqual([...mind.parents].sort((a, b) => a[0] - b[0]), [[2, 1], [3, 2], [4, 3], [5, 1], [6, 1]]);
  assert.deepEqual(mind.levels.map((ids) => ids.slice().sort((a, b) => a - b)), [[1], [2, 5, 6], [3], [4]]);
  // Mind draws a branch for child -> parent edges: outgoing links from the
  // root (1 -> 2) are turned around and marked; 5 -> 1 already points up.
  const byId = new Map(mind.edges.map((edge) => [edge.id, edge]));
  assert.deepEqual([byId.get(1).source, byId.get(1).target, byId.get(1).reversed], [2, 1, true]);
  assert.deepEqual([byId.get(4).source, byId.get(4).target, byId.get(4).reversed], [5, 1, undefined]);
  assert.equal(mind.edges.length, data.edges.length, "no edge is added or lost");
  for (const [child, parent] of mind.parents) {
    assert.ok(mind.edges.some((edge) => edge.source === child && edge.target === parent), `branch ${child} -> ${parent}`);
  }
});

test("mind input respects depth, direction, relation and type filters", () => {
  const out = m.mindInput(m.neighbourhood(snapshot, 1, { depth: 3, direction: "out" }));
  assert.deepEqual(out.levels.map((ids) => ids.length), [1, 1, 1, 1], "a chain 1 > 2 > 3 > 4");
  const incoming = m.mindInput(m.neighbourhood(snapshot, 1, { depth: 2, direction: "in" }));
  assert.deepEqual([...incoming.parents.keys()].sort(), [5, 6]);
  const noSupports = m.mindInput(m.neighbourhood(snapshot, 1, { depth: 3, hiddenRelations: ["supports"] }));
  assert.deepEqual(noSupports.nodes.map((n) => n.id).sort(), [1, 5, 6]);
  const noTasks = m.mindInput(m.neighbourhood(snapshot, 1, { depth: 1, hiddenTypes: ["task"] }));
  assert.equal(noTasks.parents.has(6), false);
  // A node reachable through two parents keeps exactly one branch; the other
  // link stays a cross-link.
  const diamond = {
    nodes: [node(1), node(2), node(3), node(4)],
    edges: [
      { id: 1, source: 1, target: 2, relation: "supports" },
      { id: 2, source: 1, target: 3, relation: "supports" },
      { id: 3, source: 2, target: 4, relation: "supports" },
      { id: 4, source: 3, target: 4, relation: "supports" },
    ],
  };
  const mind = m.mindInput(m.neighbourhood(diamond, 1, { depth: 2 }));
  assert.equal(mind.parents.get(4), 2);
  assert.equal(mind.edges.filter((edge) => edge.source === 4).length, 1);
  assert.deepEqual(m.mindInput(null), { nodes: [], edges: [], parents: new Map(), root: null, levels: [] });
});

test("the chosen layout is remembered with the options", () => {
  assert.equal(m.normalizeOptions({}).layout, "force");
  assert.equal(m.normalizeOptions({ layout: "mind" }).layout, "mind");
  assert.equal(m.normalizeOptions({ layout: "node" }).layout, "force");
  assert.deepEqual(m.LOCAL_LAYOUTS, ["force", "mind"]);
});

test("direction filters follow the shown (display-reversed) direction", async () => {
  const { neighbourhood } = await loadModel();
  const snapshot = {
    nodes: [{ id: 1, type: "task" }, { id: 2, type: "task" }, { id: 3, type: "task" }],
    edges: [{ id: "a", source: 1, target: 2, relation: "derived_from" }, { id: "b", source: 1, target: 3, relation: "supports" }],
  };
  const ids = (data) => data.nodes.map((node) => node.id).sort();
  // Stored: 1 -> 2 and 1 -> 3, so "out" from 1 reaches both.
  assert.deepEqual(ids(neighbourhood(snapshot, 1, { depth: 1, direction: "out" })), [1, 2, 3]);
  // derived_from shown reversed: 2 -> 1, so it is incoming for 1.
  const reversed = (relation) => relation === "derived_from";
  assert.deepEqual(ids(neighbourhood(snapshot, 1, { depth: 1, direction: "out" }, undefined, reversed)), [1, 3]);
  assert.deepEqual(ids(neighbourhood(snapshot, 1, { depth: 1, direction: "in" }, undefined, reversed)), [1, 2]);
  assert.deepEqual(ids(neighbourhood(snapshot, 2, { depth: 1, direction: "out" }, undefined, reversed)), [1, 2]);
});

test("stored options: v1 settings move to v2 with two levels, other choices kept", () => {
  assert.equal(m.migrateStoredOptions(null, null).depth, 2);
  const moved = m.migrateStoredOptions(null, JSON.stringify({ depth: 1, layout: "mind", hiddenTypes: ["task"] }));
  assert.equal(moved.depth, 2);
  assert.equal(moved.layout, "mind");
  assert.deepEqual(moved.hiddenTypes, ["task"]);
  assert.equal(m.migrateStoredOptions(JSON.stringify({ depth: 1 }), JSON.stringify({ depth: 3 })).depth, 1); // a v2 choice wins
});
