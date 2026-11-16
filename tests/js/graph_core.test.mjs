import assert from "node:assert/strict";
import { test } from "node:test";
import vm from "node:vm";
import { readFile } from "node:fs/promises";
import { loadGraphCore } from "./load_graph_core.mjs";
import { existsSync } from "node:fs";
// These cases compare against the frozen Python server's sources; trees
// without ark_studio/python (the public export) skip them.
const noPython = !existsSync(new URL("../../ark_studio/python/arkstudio/", import.meta.url))
  && "the Python reference sources are not in this tree";

test("shared entry/map integrator preserves the pre-extraction trajectory", async () => {
  const { stepForce, GRAPH_DEFAULTS } = (await loadGraphCore(vm.createContext({}))).namespace;
  const nodes = [{ id: 1, x: 0, y: 0, vx: 0, vy: 0 }, { id: 2, x: 0, y: 0, vx: 5, vy: -2 },
    { id: 3, x: 130, y: -90, vx: -30, vy: 100 }];
  const edges = [{ source: 1, target: 2, relation: "part_of" }, { source: 2, target: 3, relation: "supports" },
    { source: 8, target: 1, relation: "part_of" }];
  const byId = new Map(nodes.map((node) => [node.id, node]));
  for (let i = 0; i < 30; i++) stepForce(nodes, edges, byId, { ...GRAPH_DEFAULTS, distance: 36 }, new Set(["part_of"]), nodes[2]);
  // Golden positions from 8842cde's GraphView._physics: coincident pair, hidden spring, dragged node.
  const expected = [[16.17140722885055, -18.79038971520185, -0.006741063846699918, -0.11459886920753211],
    [-25.267480316330538, 25.402376818096382, -0.3073743180116533, 0.3063478443921687], [130, -90, 0, 0]];
  nodes.forEach((node, i) => ["x", "y", "vx", "vy"].forEach((key, j) => assert.ok(Math.abs(node[key] - expected[i][j]) < 1e-10)));
});

test("focus neighborhoods retain direct visible neighbors without transitive expansion", async () => {
  const { focusNeighborhood } = (await loadGraphCore(vm.createContext({}))).namespace;
  const edges = [
    { source: 1, target: 2, relation: "part_of" },
    { source: 2, target: 3, relation: "part_of" },
    { source: 1, target: 4, relation: "supports" },
    { source: 5, target: 1, relation: "part_of" },
    { source: 1, target: 1, relation: "part_of" },
  ];
  assert.equal(focusNeighborhood(edges, new Set(), new Set(["part_of"])), null);
  const seeds = new Set([1]);
  assert.deepEqual([...focusNeighborhood(edges, seeds, new Set(["part_of"]))].sort(), [1, 2, 5]);
  assert.deepEqual([...seeds], [1]);
  assert.deepEqual([...focusNeighborhood(edges, new Set([1, 3]), new Set(["supports"]))].sort(), [1, 3, 4]);
  assert.deepEqual([...focusNeighborhood(edges, new Set([99]), new Set())], [99]);
});

test("focus rendering inspects edges once even when every node is selected", async () => {
  const { focusNeighborhood } = (await loadGraphCore(vm.createContext({}))).namespace;
  const seeds = new Set(Array.from({ length: 10000 }, (_, i) => i));
  let reads = 0;
  const edges = Array.from(seeds, source => ({ source, target: (source + 1) % seeds.size,
    get relation() { reads++; return "part_of"; } }));
  assert.equal(focusNeighborhood(edges, seeds, new Set(["part_of"])).size, seeds.size);
  assert.equal(reads, edges.length);
});

test("only symmetric relations from tags.py are drawn without an arrow", { skip: noPython }, async () => {
  const { relationIsDirected, UNDIRECTED_RELATIONS } = (await loadGraphCore(vm.createContext({}))).namespace;
  const tags = await readFile(new URL("../../ark_studio/python/arkstudio/tags.py", import.meta.url), "utf8");
  const rows = [...tags.matchAll(/\("([a-z_]+)", \{"label": "([^"]+)", "reverse": "([^"]+)"\}\)/g)];
  assert.ok(rows.length >= 17);
  const symmetric = rows.filter(([, , label, reverse]) => label === reverse).map(([, id]) => id).sort();
  assert.deepEqual([...UNDIRECTED_RELATIONS].sort(), symmetric);
  for (const [, id] of rows) assert.equal(relationIsDirected(id), !symmetric.includes(id), id);
});

test("arrowheads scale with stroke and zoom inside a legible band", async () => {
  const { arrowSize, addArrowhead } = (await loadGraphCore(vm.createContext({}))).namespace;
  assert.ok(arrowSize(2.6, 1) > arrowSize(1, 1));
  assert.ok(arrowSize(1, 1.2) > arrowSize(1, 1));
  assert.equal(arrowSize(1, 0.05), 6);
  assert.equal(arrowSize(3, 40), 14);
  const calls = [];
  const path = { moveTo: (x, y) => calls.push([x, y]), lineTo: (x, y) => calls.push([x, y]), closePath() {} };
  addArrowhead(path, 100, 50, 1, 0, 10);
  assert.deepEqual(calls[0], [100, 50]);
  assert.ok(calls.slice(1).every(([x]) => x === 90), "base sits behind the tip");
  assert.ok(Math.abs(calls[1][1] - calls[2][1] - 9.6) < 1e-9);
});

test("the Force simulation stops once motion stays under the threshold, or at the safety cap", async () => {
  const { forceSettleStep, FORCE_SETTLE } = (await loadGraphCore(vm.createContext({}))).namespace;
  const run = (motions, zoom = 1, alpha = 0.5, held = false) => {
    const state = {};
    const verdicts = motions.map((m) => forceSettleStep(state, m, zoom, alpha, held));
    return { verdicts, state };
  };
  const still = { mean: 0.05, max: 0.3 };
  const moving = { mean: 0.5, max: 2 };
  // Settles only after FORCE_SETTLE.ticks consecutive quiet ticks.
  let r = run(Array(FORCE_SETTLE.ticks - 1).fill(still));
  assert.ok(r.verdicts.every((v) => v === "run"), "19 quiet ticks are not enough");
  r = run(Array(FORCE_SETTLE.ticks).fill(still));
  assert.equal(r.verdicts.at(-1), "settled");
  // A single moving tick (mean or max) resets the count.
  r = run([...Array(15).fill(still), moving, ...Array(15).fill(still)]);
  assert.ok(r.verdicts.every((v) => v === "run"));
  r = run([...Array(15).fill(still), { mean: 0.01, max: 0.8 }, ...Array(FORCE_SETTLE.ticks).fill(still)]);
  assert.equal(r.verdicts.indexOf("settled"), 15 + FORCE_SETTLE.ticks, "one node above max resets too");
  // Screen pixels: the same world motion is still at zoom 0.1, moving at zoom 4.
  assert.equal(run(Array(FORCE_SETTLE.ticks).fill({ mean: 0.5, max: 3 }), 0.1).verdicts.at(-1), "settled");
  assert.equal(run(Array(FORCE_SETTLE.ticks).fill(still), 4).verdicts.at(-1), "run");
  // A drag never settles.
  assert.equal(run(Array(40).fill(still), 1, 0.5, true).verdicts.at(-1), "run");
  // Safety: an oscillating graph stops at the tick cap or when alpha is spent.
  r = run(Array(FORCE_SETTLE.tickCap).fill(moving));
  assert.equal(r.verdicts.at(-1), "cap");
  assert.equal(r.verdicts.slice(0, -1).includes("cap"), false);
  assert.equal(run([moving], 1, FORCE_SETTLE.alphaFloor / 2).verdicts[0], "cap");
});

test("Home backdrop keeps the best-connected nodes of a large project and their links", async () => {
  const { backdropSample, BACKDROP_NODE_LIMIT } = (await loadGraphCore(vm.createContext({}))).namespace;
  const small = { nodes: [{ id: 1, degree: 0 }, { id: 2, degree: 1 }], edges: [{ source: 1, target: 2 }] };
  const same = backdropSample(small.nodes, small.edges);
  assert.equal(same.nodes, small.nodes);
  assert.equal(same.edges, small.edges);

  const nodes = Array.from({ length: 5000 }, (_, i) => ({ id: i + 1, degree: i % 97 }));
  const edges = Array.from({ length: 8000 }, (_, i) => ({ source: (i % 5000) + 1, target: ((i * 7) % 5000) + 1 }));
  const { nodes: kept, edges: links } = backdropSample(nodes, edges);
  assert.equal(kept.length, BACKDROP_NODE_LIMIT);
  const minKept = Math.min(...kept.map((node) => node.degree));
  assert.ok(nodes.filter((node) => !kept.includes(node)).every((node) => node.degree <= minKept));
  // Server order is kept; only links between kept nodes remain.
  assert.deepEqual(kept.map((node) => node.id), [...kept.map((node) => node.id)].sort((a, b) => a - b));
  const ids = new Set(kept.map((node) => node.id));
  assert.ok(links.length > 0);
  assert.ok(links.every((edge) => ids.has(edge.source) && ids.has(edge.target)));
  // Links already bound to node objects (a GraphView reused the data) count too.
  const bound = edges.map((edge) => ({ source: { id: edge.source }, target: { id: edge.target } }));
  assert.equal(backdropSample(nodes, bound).edges.length, links.length);
});
