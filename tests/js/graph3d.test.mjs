import assert from "node:assert/strict";
import { test } from "node:test";

import {
  createSim3D, setSimLinks, stepForce3D, exactRepulsion3D, octreeRepulsion3D, buildOctree, applyCluster3D,
  seededRandom, simBounds,
} from "../../ark_studio/web/js/graph3d-force.js";
import {
  nodeStates, pickLabels, labelFade, pickNode, fitDistance, pixelRatioCap, normalizeGraph3DSettings,
  loadViewChoice, saveViewChoice, viewStorageKey, loadGraph3DSettings, saveGraph3DSettings, GRAPH3D_DEFAULTS,
} from "../../ark_studio/web/js/graph3d-model.js";
import { GRAPH_DEFAULTS } from "../../ark_studio/web/js/graph-core.js";

const TYPES = ["task", "idea", "question", "claim", "evidence"];

function graph(n, seed = 11) {
  const rand = seededRandom(seed);
  const nodes = Array.from({ length: n }, (_, i) => ({ id: `n${i + 1}`, type: TYPES[i % TYPES.length], status: i % 2 ? "open" : "done" }));
  const edges = [];
  for (let i = 1; i < n; i++) edges.push({ source: `n${i + 1}`, target: `n${1 + Math.floor(rand() * i)}`, relation: i % 4 ? "part_of" : "supports" });
  return { nodes, edges };
}

function memoryStorage() {
  const map = new Map();
  return { getItem: (k) => (map.has(k) ? map.get(k) : null), setItem: (k, v) => map.set(k, String(v)), map };
}

test("the 3D force layout is deterministic for a seed", () => {
  const run = (seed, order = (list) => list) => {
    const { nodes, edges } = graph(220);
    const list = order(nodes);
    const sim = createSim3D(list, { seed });
    setSimLinks(sim, edges);
    for (let i = 0; i < 120; i++) stepForce3D(sim, list, GRAPH_DEFAULTS);
    return new Map(list.map((node, i) => [node.id, [sim.x[i], sim.y[i], sim.z[i]]]));
  };
  const a = run(7), b = run(7), c = run(8);
  assert.deepEqual([...a], [...b], "same seed, same positions");
  assert.notDeepEqual([...a.values()][3], [...c.values()][3], "another seed starts elsewhere");
  // Start positions depend on (seed, id), not on the node order.
  const { nodes } = graph(50);
  const s1 = createSim3D(nodes, { seed: 3 }), s2 = createSim3D([...nodes].reverse(), { seed: 3 });
  for (let i = 0; i < nodes.length; i++) {
    const j = nodes.length - 1 - i;
    assert.deepEqual([s1.x[i], s1.y[i], s1.z[i]], [s2.x[j], s2.y[j], s2.z[j]]);
  }
});

test("the octree (Barnes–Hut) repulsion matches the brute-force sum", () => {
  for (const [n, seed] of [[40, 1], [150, 2], [400, 3]]) {
    const { nodes } = graph(n, seed);
    const sim = createSim3D(nodes, { seed });
    const exact = [new Float64Array(n), new Float64Array(n), new Float64Array(n)];
    exactRepulsion3D(sim, GRAPH_DEFAULTS.repulsion, ...exact);
    // Brute force has no net force either (pairs are equal and opposite).
    for (const [theta, tolerance] of [[0.3, 0.01], [0.6, 0.04], [0.9, 0.08]]) {
      const tree = [new Float64Array(n), new Float64Array(n), new Float64Array(n)];
      octreeRepulsion3D(sim, GRAPH_DEFAULTS.repulsion, ...tree, { theta });
      let err = 0, mag = 0;
      for (let i = 0; i < n; i++) {
        err += Math.hypot(tree[0][i] - exact[0][i], tree[1][i] - exact[1][i], tree[2][i] - exact[2][i]);
        mag += Math.hypot(exact[0][i], exact[1][i], exact[2][i]);
      }
      assert.ok(err / mag < tolerance, `n=${n} θ=${theta}: relative error ${(err / mag).toFixed(4)} ≥ ${tolerance}`);
    }
    // θ = 0 opens every cell: the same as brute force up to rounding.
    const zero = [new Float64Array(n), new Float64Array(n), new Float64Array(n)];
    octreeRepulsion3D(sim, GRAPH_DEFAULTS.repulsion, ...zero, { theta: 0 });
    for (let i = 0; i < n; i++) for (let a = 0; a < 3; a++) assert.ok(Math.abs(zero[a][i] - exact[a][i]) < 1e-9);
  }
});

test("the octree holds every body once and its mass adds up", () => {
  const { nodes } = graph(300, 5);
  const sim = createSim3D(nodes, { seed: 5 });
  sim.x[7] = sim.x[8]; sim.y[7] = sim.y[8]; sim.z[7] = sim.z[8]; // coincident bodies
  const tree = buildOctree(sim);
  assert.equal(tree.mass[0], 300);
  assert.deepEqual([...tree.order].sort((a, b) => a - b), Array.from({ length: 300 }, (_, i) => i));
  let leaves = 0;
  for (let c = 0; c < tree.count; c++) if (!tree.kids[c]) leaves += tree.len[c];
  assert.equal(leaves, 300);
});

test("coincident nodes separate deterministically and nothing drifts", () => {
  const nodes = Array.from({ length: 6 }, (_, i) => ({ id: i }));
  const sim = createSim3D(nodes, { seed: 1, spread: 1 });
  sim.x.fill(5); sim.y.fill(5); sim.z.fill(5);
  for (let i = 0; i < 30; i++) stepForce3D(sim, nodes, GRAPH_DEFAULTS);
  const pts = new Set(Array.from({ length: 6 }, (_, i) => `${sim.x[i].toFixed(3)},${sim.y[i].toFixed(3)},${sim.z[i].toFixed(3)}`));
  assert.equal(pts.size, 6);
  for (const v of [...sim.x, ...sim.y, ...sim.z]) assert.ok(Number.isFinite(v));
});

test("links, gravity, collision and damping settle a graph; pinned nodes stay", () => {
  const { nodes, edges } = graph(600, 4);
  const sim = createSim3D(nodes, { seed: 4 });
  setSimLinks(sim, edges);
  sim.fixed[10] = 1;
  const pin = [sim.x[10], sim.y[10], sim.z[10]];
  let motion;
  for (let i = 0; i < 400; i++) motion = stepForce3D(sim, nodes, GRAPH_DEFAULTS);
  assert.ok(motion.mean < 0.5, `still moving: ${motion.mean}`);
  assert.deepEqual([sim.x[10], sim.y[10], sim.z[10]], pin);
  // Linked nodes end up nearer each other than the average pair.
  let linked = 0;
  for (let l = 0; l < sim.links.length; l += 2) {
    const a = sim.links[l], b = sim.links[l + 1];
    linked += Math.hypot(sim.x[a] - sim.x[b], sim.y[a] - sim.y[b], sim.z[a] - sim.z[b]);
  }
  linked /= sim.links.length / 2;
  assert.ok(linked < simBounds(sim).radius, `links ${linked.toFixed(1)} vs radius ${simBounds(sim).radius.toFixed(1)}`);
  // A relation filter drops links.
  setSimLinks(sim, edges, new Set(["supports"]));
  assert.equal(sim.links.length / 2, edges.filter((e) => e.relation === "supports").length);
});

test("cluster by type in 3D: groups gather, no net force or spin", () => {
  const { nodes, edges } = graph(250, 6);
  const cfg = { ...GRAPH_DEFAULTS, cluster: true, cluster_attract: 0.8, cluster_repel: 0.8, cluster_by: "type" };
  const sim = createSim3D(nodes, { seed: 6 });
  const n = sim.n;
  const ax = new Float64Array(n), ay = new Float64Array(n), az = new Float64Array(n);
  const gid = applyCluster3D(sim, nodes, cfg, 1, ax, ay, az);
  assert.equal(gid.length, n);
  let fx = 0, fy = 0, fz = 0, tx = 0, ty = 0, tz = 0, ox = 0, oy = 0, oz = 0;
  for (let i = 0; i < n; i++) { ox += sim.x[i]; oy += sim.y[i]; oz += sim.z[i]; }
  ox /= n; oy /= n; oz /= n;
  for (let i = 0; i < n; i++) {
    fx += ax[i]; fy += ay[i]; fz += az[i];
    const rx = sim.x[i] - ox, ry = sim.y[i] - oy, rz = sim.z[i] - oz;
    tx += ry * az[i] - rz * ay[i]; ty += rz * ax[i] - rx * az[i]; tz += rx * ay[i] - ry * ax[i];
  }
  assert.ok(Math.hypot(fx, fy, fz) < 1e-6, "no net force");
  assert.ok(Math.hypot(tx, ty, tz) < 1e-3, `no net torque (${Math.hypot(tx, ty, tz)})`);
  // After a run, members sit nearer their own group's centre than the others'.
  setSimLinks(sim, edges);
  for (let i = 0; i < 300; i++) stepForce3D(sim, nodes, cfg);
  const centres = new Map();
  nodes.forEach((node, i) => {
    const c = centres.get(node.type) || { x: 0, y: 0, z: 0, k: 0 };
    c.x += sim.x[i]; c.y += sim.y[i]; c.z += sim.z[i]; c.k++;
    centres.set(node.type, c);
  });
  for (const c of centres.values()) { c.x /= c.k; c.y /= c.k; c.z /= c.k; }
  let own = 0;
  nodes.forEach((node, i) => {
    const d = (c) => Math.hypot(sim.x[i] - c.x, sim.y[i] - c.y, sim.z[i] - c.z);
    const mine = d(centres.get(node.type));
    if ([...centres.values()].every((c) => d(c) >= mine)) own++;
  });
  assert.ok(own / n > 0.8, `only ${own}/${n} nearest their own group`);
});

test("positions carry over by node id (filters keep the layout)", () => {
  const { nodes, edges } = graph(120, 8);
  const sim = createSim3D(nodes, { seed: 8 });
  setSimLinks(sim, edges);
  for (let i = 0; i < 50; i++) stepForce3D(sim, nodes, GRAPH_DEFAULTS);
  const previous = new Map(nodes.map((node, i) => [node.id, { x: sim.x[i], y: sim.y[i], z: sim.z[i], fixed: i === 3 }]));
  const subset = nodes.filter((_, i) => i % 3 === 0);
  const next = createSim3D(subset, { seed: 8, previous });
  subset.forEach((node, k) => {
    const i = nodes.indexOf(node);
    assert.deepEqual([next.x[k], next.y[k], next.z[k]], [sim.x[i], sim.y[i], sim.z[i]]);
  });
  assert.equal(next.fixed[1], 1, "a pinned node stays pinned (index 3 is subset[1])");
});

test("node states: selection and hover highlight the neighbourhood (relation filter applies)", () => {
  const ids = ["a", "b", "c", "d"];
  const edges = [{ source: "a", target: "b", relation: "part_of" }, { source: "a", target: "c", relation: "supports" }];
  assert.deepEqual([...nodeStates(ids, edges)], [0, 0, 0, 0]);
  assert.deepEqual([...nodeStates(ids, edges, { hover: "a" })], [3, 2, 2, 1]);
  // The hovered node keeps its ring outside the selected neighbourhood.
  assert.deepEqual([...nodeStates(ids, edges, { selected: new Set(["a"]), hover: "d" })], [4, 2, 2, 3]);
  assert.deepEqual([...nodeStates(ids, edges, { selected: new Set(["a"]), relations: new Set(["part_of"]) })], [4, 2, 1, 1]);
  // A node selected in another view that this graph does not show dims nothing.
  assert.deepEqual([...nodeStates(ids, edges, { selected: new Set(["zz"]) })], [0, 0, 0, 0]);
  assert.deepEqual([...nodeStates(ids, edges, { selected: new Set(["zz"]), hover: "a" })], [3, 2, 2, 1]);
});

test("labels: forced ones first, then the nearest within reach, fading out", () => {
  assert.equal(labelFade(10, 100), 1);
  assert.equal(labelFade(100, 100), 0);
  assert.ok(labelFade(80, 100) > 0 && labelFade(80, 100) < 1);
  const dist = Float32Array.from([50, 10, 500, 20, 90]);
  const picks = pickLabels({ n: 5, dist, reach: 100, budget: 3, forced: [2] });
  assert.deepEqual(picks.map((p) => p.i), [2, 1, 3]);
  assert.equal(picks[0].alpha, 1);
  assert.deepEqual(pickLabels({ n: 5, dist, reach: 0, forced: [] }), []);
  assert.deepEqual(pickLabels({ n: 5, dist, reach: 100, onScreen: (i) => i !== 1 }).map((p) => p.i), [3, 0, 4]);
});

test("screen picking prefers the nearest of overlapping nodes", () => {
  const sx = Float32Array.from([100, 104, 300]), sy = Float32Array.from([100, 100, 300]);
  const sz = Float32Array.from([0.5, 0.2, 0.1]), sr = Float32Array.from([10, 10, 10]);
  assert.equal(pickNode(102, 100, sx, sy, sz, sr, 3), 1);
  assert.equal(pickNode(200, 200, sx, sy, sz, sr, 3), -1);
  sz[1] = 1.5; // behind the camera / clipped
  assert.equal(pickNode(102, 100, sx, sy, sz, sr, 3), 0);
});

test("view choice and 3D settings are remembered per user and page", () => {
  const storage = memoryStorage();
  assert.equal(loadViewChoice(storage, "ann", "p1:map"), "2d");
  saveViewChoice(storage, "ann", "p1:map", "3d");
  assert.equal(loadViewChoice(storage, "ann", "p1:map"), "3d");
  assert.equal(loadViewChoice(storage, "bob", "p1:map"), "2d");
  assert.equal(loadViewChoice(storage, "ann", "p1:topic:T-1"), "2d");
  assert.notEqual(viewStorageKey("ann", "a"), viewStorageKey("ann", "b"));
  assert.deepEqual(loadGraph3DSettings(storage, "ann"), { ...GRAPH3D_DEFAULTS });
  saveGraph3DSettings(storage, "ann", { nodeSize: 9, labelDistance: -1, lowPower: true, junk: 1 });
  assert.deepEqual(loadGraph3DSettings(storage, "ann"), { labelDistance: 0, nodeSize: 2, lowPower: true, autoRotate: false });
  assert.deepEqual(normalizeGraph3DSettings("nope"), { ...GRAPH3D_DEFAULTS });
  assert.equal(loadViewChoice({ getItem() { throw new Error("denied"); } }, "a", "b"), "2d");
});

test("camera fit and pixel-ratio cap", () => {
  const d = fitDistance(100, 50, 1.6);
  assert.ok(d > 100 / Math.sin((25 * Math.PI) / 180) && d < 300);
  assert.ok(fitDistance(100, 50, 0.5) > d, "a narrow view backs off further");
  assert.equal(pixelRatioCap(3, true), 1);
  assert.equal(pixelRatioCap(3, false), 2);
  assert.equal(pixelRatioCap(3, false, true), 1.5);
  assert.equal(pixelRatioCap(1, false), 1);
});
