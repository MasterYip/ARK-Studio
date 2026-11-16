// node scripts/bench/node_graph_layout_metrics.mjs [graph.json ...] [--out metrics.json] [--reverse rel,rel] [--skip-synthetic]
//
// Compares the previous node-graph layout/routing (breadth-first columns,
// orthogonal elbows through the column midpoint) with the layered layout and
// Bézier router in ark_studio/web/js/node-graph-layout.js.  Without arguments it uses
// two synthetic graphs (300 and 2,000 nodes, fixed seed); pass exported graph
// JSON ({ nodes: [{id}], edges: [{id, source, target}] }) to add real data,
// e.g. the demo project's GET /api/graph.
import { readFile, writeFile } from "node:fs/promises";
import { layeredLayout, routeNodeEdges, layoutMetrics, NODE_W, NODE_H } from "../../ark_studio/web/js/node-graph-layout.js";

function legacyPositions(nodes, edges) {
  const outgoing = new Map(nodes.map((node) => [node.id, []]));
  const indegree = new Map(nodes.map((node) => [node.id, 0]));
  for (const edge of edges) {
    if (!outgoing.has(edge.source) || !indegree.has(edge.target)) continue;
    outgoing.get(edge.source).push(edge.target);
    indegree.set(edge.target, (indegree.get(edge.target) || 0) + 1);
  }
  const depth = new Map();
  const queue = nodes.filter((node) => !indegree.get(node.id)).map((node) => [node.id, 0]);
  if (!queue.length && nodes.length) queue.push([nodes[0].id, 0]);
  const seen = new Set();
  while (queue.length) {
    const [id, level] = queue.shift();
    if (seen.has(id)) continue;
    seen.add(id);
    depth.set(id, level);
    for (const next of outgoing.get(id) || []) queue.push([next, level + 1]);
  }
  const levels = new Map();
  for (const node of nodes) {
    const level = depth.get(node.id) ?? 0;
    if (!levels.has(level)) levels.set(level, []);
    levels.get(level).push(node);
  }
  const positions = new Map();
  for (const [level, group] of levels) group.forEach((node, index) => positions.set(node.id, { x: 90 + level * 280, y: 80 + index * 100 }));
  return positions;
}

function legacyRoutes(nodes, edges) {
  const byId = new Map(nodes.map((node) => [node.id, node]));
  const routes = new Map();
  for (const edge of edges) {
    const s = byId.get(edge.source), t = byId.get(edge.target);
    if (!s || !t || s === t) continue;
    const p1 = { x: s.x + NODE_W / 2, y: s.y }, p2 = { x: t.x - NODE_W / 2, y: t.y };
    const bx = (p1.x + p2.x) / 2, by = (p1.y + p2.y) / 2;
    routes.set(edge.id, { points: [p1, { x: bx, y: p1.y }, { x: bx, y: by }, { x: p2.x, y: by }, p2] });
  }
  return routes;
}

function synthetic(count, seed) {
  let state = seed;
  const random = () => ((state = (state * 1103515245 + 12345) % 2147483648) / 2147483648);
  const nodes = Array.from({ length: count }, (_, i) => ({ id: i + 1 }));
  const edges = [];
  // A research-graph-like mix: trees of part_of, cross links, a few cycles
  // and ~5% isolated nodes.
  for (let i = 2; i <= count; i++) {
    if (random() < 0.05) continue;
    edges.push({ id: `t${i}`, source: i, target: 1 + Math.floor(random() * Math.max(1, i - 1)) });
    if (random() < 0.35) edges.push({ id: `c${i}`, source: i, target: 1 + Math.floor(random() * count) });
  }
  return { nodes, edges: edges.filter((e) => e.source !== e.target) };
}

function measure(name, graph) {
  const nodes = graph.nodes.map((node) => ({ id: node.id, w: NODE_W, h: NODE_H }));
  const ids = new Set(nodes.map((n) => n.id));
  const edges = graph.edges.filter((e) => ids.has(e.source) && ids.has(e.target) && e.source !== e.target)
    .map((e, i) => ({ id: e.id ?? i, source: e.source, target: e.target }));
  const before = legacyPositions(nodes, edges);
  const legacyNodes = nodes.map((n) => ({ ...n, ...before.get(n.id) }));
  const legacy = layoutMetrics(legacyNodes, edges, legacyRoutes(legacyNodes, edges));
  const legacyRouted = layoutMetrics(legacyNodes, edges, routeNodeEdges(legacyNodes, edges));
  const timings = [];
  let result;
  for (let i = 0; i < 5; i++) {
    const t0 = performance.now();
    result = layeredLayout(nodes, edges);
    timings.push(performance.now() - t0);
  }
  const laidOut = nodes.map((n) => ({ ...n, ...result.positions.get(n.id) }));
  const routeTimes = [];
  let routes;
  for (let i = 0; i < 5; i++) {
    const t0 = performance.now();
    routes = routeNodeEdges(laidOut, edges, { waypoints: result.waypoints });
    routeTimes.push(performance.now() - t0);
  }
  const after = layoutMetrics(laidOut, edges, routes);
  const median = (v) => [...v].sort((a, b) => a - b)[Math.floor(v.length / 2)];
  return {
    graph: name, nodes: nodes.length, edges: edges.length,
    before: legacy, before_with_new_routing: legacyRouted, after,
    layout_ms: +median(timings).toFixed(1), route_ms: +median(routeTimes).toFixed(1), stats: result.stats,
  };
}

const args = process.argv.slice(2);
const outIndex = args.indexOf("--out");
const out = outIndex >= 0 ? args.splice(outIndex, 2)[1] : null;
// --reverse derived_from,depends_on: lay out with these relations shown
// reversed (display only, as GraphView does).
const skipIndex = args.indexOf("--skip-synthetic");
const skipSynthetic = skipIndex >= 0 && Boolean(args.splice(skipIndex, 1));
const reverseIndex = args.indexOf("--reverse");
const reverse = new Set(reverseIndex >= 0 ? args.splice(reverseIndex, 2)[1].split(",") : []);
const graphs = [];
for (const file of args) {
  const data = JSON.parse(await readFile(file, "utf8"));
  data.edges = (data.edges || []).map((edge) => (reverse.has(edge.relation)
    ? { ...edge, source: edge.target, target: edge.source } : edge));
  graphs.push([file.replace(/^.*[\\/]/, ""), data]);
}
if (!skipSynthetic) graphs.push(["synthetic-300", synthetic(300, 7)], ["synthetic-2000", synthetic(2000, 11)]);
const report = graphs.map(([name, graph]) => measure(name, graph));
console.log(JSON.stringify(report, null, 2));
if (out) await writeFile(out, JSON.stringify(report, null, 2) + "\n");
