import assert from "node:assert/strict";
import { test } from "node:test";
import { normalizeGraphPhysics, GRAPH_DEFAULTS as PHYSICS_DEFAULTS } from "../../ark_studio/web/js/physics.js";
import { normalizeOverrides } from "../../ark_studio/web/js/appearance-overrides.js";

import {
  stepForce, applyClusterForces, clusterCentroids, clusterKey, GRAPH_DEFAULTS,
} from "../../ark_studio/web/js/graph-core.js";

const TYPES = ["task", "idea", "question", "claim", "evidence"];

// A seeded mixed-type graph: positions from a small LCG, a few links.
function seededGraph(n, seed = 7) {
  let s = seed >>> 0;
  const rand = () => ((s = (Math.imul(s, 1664525) + 1013904223) >>> 0) / 4294967296);
  const nodes = Array.from({ length: n }, (_, i) => ({
    id: i + 1, type: TYPES[i % TYPES.length], status: i % 2 ? "open" : "done",
    tags: i % 3 ? [`topic:${i % 3}`] : [],
    x: (rand() - 0.5) * 600, y: (rand() - 0.5) * 400, vx: 0, vy: 0,
  }));
  const edges = [];
  for (let i = 1; i < n; i++) edges.push({ id: i, source: i + 1, target: 1 + Math.floor(rand() * i), relation: "part_of" });
  return { nodes, edges, byId: new Map(nodes.map((node) => [node.id, node])) };
}

function run(graph, cfg, ticks) {
  for (let i = 0; i < ticks; i++) stepForce(graph.nodes, graph.edges, graph.byId, cfg, null, null, undefined, Math.min(1, (i + 1) / 40));
  return graph.nodes.map((node) => [node.x, node.y]);
}

const ON = { ...GRAPH_DEFAULTS, cluster: true, cluster_attract: 0.8, cluster_repel: 0.8, cluster_by: "type" };

test("centroids and sizes per group", () => {
  const nodes = [
    { type: "task", x: 0, y: 0 }, { type: "task", x: 10, y: 20 },
    { type: "idea", x: -5, y: 5 }, { type: "", x: 99, y: 99 },
  ];
  const groups = clusterCentroids(nodes, "type");
  assert.equal(groups.k, 2);
  assert.deepEqual([...groups.gid], [0, 0, 1, -1]);
  assert.deepEqual(groups.count, [2, 1]);
  assert.deepEqual([groups.cx[0], groups.cy[0], groups.cx[1], groups.cy[1]], [5, 10, -5, 5]);
  assert.equal(clusterKey({ tags: ["", "b"] }, "tag"), "b");
  assert.equal(clusterKey({ tags: [] }, "tag"), null);
  assert.equal(clusterKey({ status: "done" }, "status"), "done");
});

test("same-type pull points to the own centroid, cross-type push away from the others", () => {
  // Two tasks left, two ideas right; attraction only, then repulsion only.
  const make = () => [
    { type: "task", x: -110, y: 0, vx: 0, vy: 0 }, { type: "task", x: -90, y: 0, vx: 0, vy: 0 },
    { type: "idea", x: 90, y: 0, vx: 0, vy: 0 }, { type: "idea", x: 110, y: 0, vx: 0, vy: 0 },
  ];
  const pull = make();
  applyClusterForces(pull, { ...ON, cluster_repel: 0 });
  assert.ok(pull[0].vx > 0 && pull[1].vx < 0, "tasks pulled toward x = -100");
  assert.ok(pull[2].vx > 0 && pull[3].vx < 0, "ideas pulled toward x = 100");
  const push = make();
  applyClusterForces(push, { ...ON, cluster_attract: 0 });
  assert.ok(push[0].vx < 0 && push[1].vx < 0, "tasks pushed left, away from the ideas");
  assert.ok(push[2].vx > 0 && push[3].vx > 0, "ideas pushed right");
  const net = push.reduce((sum, node) => sum + node.vx, 0);
  assert.ok(Math.abs(net) < 1e-9, "no net drift");
  // Off-axis groups: the centroid pushes add no net force and no spin.
  const skew = [
    { type: "task", x: -100, y: 30, vx: 0, vy: 0 }, { type: "task", x: -60, y: -20, vx: 0, vy: 0 },
    { type: "idea", x: 80, y: 70, vx: 0, vy: 0 }, { type: "note", x: 10, y: -90, vx: 0, vy: 0 },
  ];
  applyClusterForces(skew, ON);
  const cx = skew.reduce((sum, node) => sum + node.x, 0) / 4, cy = skew.reduce((sum, node) => sum + node.y, 0) / 4;
  const torque = skew.reduce((sum, node) => sum + (node.x - cx) * node.vy - (node.y - cy) * node.vx, 0);
  assert.ok(Math.abs(torque) < 1e-9 && Math.abs(skew.reduce((sum, node) => sum + node.vy, 0)) < 1e-9);
  const none = make();
  applyClusterForces(none, { ...ON, cluster_attract: 0, cluster_repel: 0 });
  assert.ok(none.every((node) => node.vx === 0 && node.vy === 0));
});

test("off by default: the layout is identical to the plain force model", () => {
  assert.equal(normalizeGraphPhysics({}).cluster, false);
  assert.equal(PHYSICS_DEFAULTS.cluster, false);
  const plain = run(seededGraph(80), { ...GRAPH_DEFAULTS }, 150);
  const off = run(seededGraph(80), { ...normalizeGraphPhysics(GRAPH_DEFAULTS), cluster_attract: 1, cluster_repel: 1 }, 150);
  assert.deepEqual(off, plain);
  const graph = seededGraph(80);
  assert.ok(graph.nodes.every((node) => !("_cluster" in node)));
});

test("deterministic with the same seed", () => {
  assert.deepEqual(run(seededGraph(150, 3), ON, 200), run(seededGraph(150, 3), ON, 200));
  assert.notDeepEqual(run(seededGraph(150, 3), ON, 200), run(seededGraph(150, 3), GRAPH_DEFAULTS, 200));
});

test("groups separate: same-type spread shrinks relative to cross-type distance", () => {
  const ratio = (positions, nodes) => {
    const groups = clusterCentroids(nodes.map((node, i) => ({ ...node, x: positions[i][0], y: positions[i][1] })), "type");
    let within = 0;
    positions.forEach(([x, y], i) => { const g = groups.gid[i]; within += Math.hypot(x - groups.cx[g], y - groups.cy[g]); });
    within /= positions.length;
    let between = 0, pairs = 0;
    for (let a = 0; a < groups.k; a++) for (let b = a + 1; b < groups.k; b++) {
      between += Math.hypot(groups.cx[a] - groups.cx[b], groups.cy[a] - groups.cy[b]); pairs++;
    }
    return within / (between / pairs);
  };
  const base = seededGraph(200, 11);
  const off = ratio(run(seededGraph(200, 11), GRAPH_DEFAULTS, 400), base.nodes);
  const on = ratio(run(seededGraph(200, 11), ON, 400), base.nodes);
  assert.ok(on < off * 0.6, `clustered ratio ${on.toFixed(3)} vs plain ${off.toFixed(3)}`);
});

test("no NaN: one-node types, all nodes on one spot, untagged nodes", () => {
  const single = [{ type: "task", x: 5, y: 5, vx: 0, vy: 0 }, { type: "idea", x: 5, y: 5, vx: 0, vy: 0 }];
  applyClusterForces(single, ON);
  assert.ok(single.every((node) => Number.isFinite(node.vx) && Number.isFinite(node.vy)));
  assert.ok(single[0].vx !== 0 || single[0].vy !== 0, "coincident groups still part");
  const stack = Array.from({ length: 60 }, (_, i) => ({ id: i + 1, type: TYPES[i % 3], tags: i % 2 ? ["a"] : [], x: 0, y: 0, vx: 0, vy: 0 }));
  const byId = new Map(stack.map((node) => [node.id, node]));
  for (const by of ["type", "status", "tag"]) {
    for (let i = 0; i < 50; i++) stepForce(stack, [], byId, { ...ON, cluster_by: by }, null);
  }
  assert.ok(stack.every((node) => [node.x, node.y, node.vx, node.vy].every(Number.isFinite)));
  const lone = [{ type: "task", x: 1, y: 2, vx: 0, vy: 0 }];
  applyClusterForces(lone, ON);
  assert.deepEqual([lone[0].vx, lone[0].vy], [0, 0]);
});

test("settings: normalize clamps and keeps the clustering fields", () => {
  const value = normalizeGraphPhysics({ cluster: true, cluster_attract: 3, cluster_repel: -1, cluster_by: "nope" });
  assert.equal(value.cluster, true);
  assert.equal(value.cluster_attract, 1);
  assert.equal(value.cluster_repel, 0);
  assert.equal(value.cluster_by, "type");
  assert.equal(normalizeGraphPhysics({ cluster: "true" }).cluster, false);
  const mine = normalizeOverrides({ graph: { repulsion: 1, distance: 0, gravity: 0, damping: 0.3, cluster: true, cluster_by: "tag", cluster_attract: 0.2 } });
  assert.deepEqual(mine.graph, { repulsion: 1, distance: 0, gravity: 0, damping: 0.3, cluster: true, cluster_attract: 0.2, cluster_by: "tag" });
});
