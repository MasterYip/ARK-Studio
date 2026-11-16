// node scripts/bench/node_link_styles.mjs [demo-graph.json] [--out file.json]
// Compares link curve models (forward tangents from Blender, xyflow, Rete,
// LiteGraph and ARK's previous rule; backward: previous loop, compact
// smooth-step loop, xyflow tight S) on the layered node layout: crossings,
// links through cards, link length, link bounding-box area, route time.
import { readFile, writeFile } from "node:fs/promises";
import { layeredLayout, routeNodeEdges, layoutMetrics, sampleRoute } from "../../ark_studio/web/js/node-graph-layout.js";
import { cardMetrics } from "../../ark_studio/web/js/node-card.js";

const args = process.argv.slice(2);
const outIndex = args.indexOf("--out");
const out = outIndex >= 0 ? args.splice(outIndex, 2)[1] : null;
const graphs = [];
if (args[0]) {
  const demo = JSON.parse(await readFile(args[0], "utf8"));
  // Shown direction as the app draws it (derived_from / depends_on reversed).
  demo.edges = demo.edges.map((e) => (["derived_from", "depends_on"].includes(e.relation) ? { ...e, source: e.target, target: e.source } : e));
  graphs.push(["demo", demo]);
}
let state = 7;
const random = () => ((state = (state * 1103515245 + 12345) % 2147483648) / 2147483648);
const nodes = Array.from({ length: 300 }, (_, i) => ({ id: i + 1, title: `Node ${i + 1} ${"words ".repeat(Math.floor(random() * 8))}` }));
const edges = [];
for (let i = 2; i <= 300; i++) {
  edges.push({ id: `t${i}`, source: i, target: 1 + Math.floor(random() * (i - 1)) });
  if (random() < 0.35) edges.push({ id: `c${i}`, source: i, target: 1 + Math.floor(random() * 300) });
}
graphs.push(["synthetic-300", { nodes, edges: edges.filter((e) => e.source !== e.target) }]);
// A hand-arranged map has many backward links: the 300-node graph with 20%
// of its cards moved to another layout column.
graphs.push(["synthetic-300-manual", { nodes, edges: edges.filter((e) => e.source !== e.target), shuffle: 0.2 }]);

const combos = [
  ["current", "loop"], ["current", "compact"], ["xyflow", "compact"], ["blender", "compact"],
  ["rete", "compact"], ["litegraph", "compact"], ["xyflow", "xyflow"],
];
const report = [];
for (const [name, graph] of graphs) {
  const laid = graph.nodes.map((n) => ({ id: n.id, ...(({ w, h }) => ({ w, h }))(cardMetrics(n, { kind: "node", showImages: false })) }));
  const links = graph.edges.map((e, i) => ({ id: e.id ?? i, source: e.source, target: e.target }));
  const { positions, waypoints } = layeredLayout(laid, links);
  for (const n of laid) Object.assign(n, positions.get(n.id));
  if (graph.shuffle) {
    const columns = [...new Set(laid.map((n) => n.x))];
    let seed = 5;
    const rnd = () => ((seed = (seed * 1103515245 + 12345) % 2147483648) / 2147483648);
    for (const n of laid) if (rnd() < graph.shuffle) { n.x = columns[Math.floor(rnd() * columns.length)]; n.y += 37; }
  }
  const byId = new Map(laid.map((n) => [n.id, n]));
  const backward = links.filter((e) => byId.get(e.target).x - byId.get(e.target).w / 2 < byId.get(e.source).x + byId.get(e.source).w / 2 + 48);
  for (const [forward, back] of combos) {
    const t0 = performance.now();
    let routes;
    for (let i = 0; i < 3; i++) routes = routeNodeEdges(laid, links, { waypoints, forward, backward: back });
    const ms = (performance.now() - t0) / 3;
    const m = layoutMetrics(laid, links, routes);
    const bbox = (list) => list.reduce((sum, e) => {
      const pts = sampleRoute(routes.get(e.id).segments, 12);
      const xs = pts.map((p) => p.x), ys = pts.map((p) => p.y);
      return sum + (Math.max(...xs) - Math.min(...xs)) * (Math.max(...ys) - Math.min(...ys));
    }, 0);
    const backLength = backward.reduce((sum, e) => {
      const pts = sampleRoute(routes.get(e.id).segments, 12);
      let l = 0; for (let i = 1; i < pts.length; i++) l += Math.hypot(pts[i].x - pts[i - 1].x, pts[i].y - pts[i - 1].y);
      return sum + l;
    }, 0);
    report.push({ graph: name, links: links.length, backward: backward.length, forward, back,
      crossings: m.crossings, through_cards: m.overlaps, length: m.length,
      link_bbox_area_k: Math.round(bbox(links) / 1000), back_bbox_area_k: Math.round(bbox(backward) / 1000),
      back_length: Math.round(backLength), route_ms: +ms.toFixed(1) });
  }
}
console.table(report);
if (out) await writeFile(out, JSON.stringify(report, null, 2) + "\n");
