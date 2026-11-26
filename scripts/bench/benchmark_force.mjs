// Force-layout tick time with and without "cluster by type" (headless).
//
//   node scripts/bench/benchmark_force.mjs [sizes=200,1000,5000] [ticks=150]
//
// Same seeded mixed-type graph for both runs; the Map's own integrator
// (stepForce with the Barnes–Hut repulsion above 400 nodes).  Prints the
// median of five runs of the mean tick time in ms, and the time of the
// clustering term alone on the same positions (the two whole-tick runs
// follow different trajectories, so their Barnes–Hut cost differs a little).
import { stepForce, applyClusterForces, GRAPH_DEFAULTS } from "../../ark_studio/web/js/graph-core.js";
import { applyRepulsion } from "../../ark_studio/web/js/graph-force.js";

const TYPES = ["task", "idea", "question", "claim", "decision", "experiment", "evidence", "result", "note"];
const sizes = (process.argv[2] || "200,1000,5000").split(",").map(Number);
const ticks = Number(process.argv[3] || 150);

function graph(n, seed = 7) {
  let s = seed >>> 0;
  const rand = () => ((s = (Math.imul(s, 1664525) + 1013904223) >>> 0) / 4294967296);
  const nodes = Array.from({ length: n }, (_, i) => ({
    id: i + 1, type: TYPES[Math.floor(rand() * TYPES.length)],
    x: (rand() - 0.5) * Math.sqrt(n) * 40, y: (rand() - 0.5) * Math.sqrt(n) * 30, vx: 0, vy: 0,
  }));
  const edges = [];
  for (let i = 1; i < n; i++) edges.push({ source: i + 1, target: 1 + Math.floor(rand() * i), relation: "part_of" });
  return { nodes, edges, byId: new Map(nodes.map((node) => [node.id, node])) };
}

function time(n, cfg) {
  const g = graph(n);
  const start = process.hrtime.bigint();
  for (let i = 0; i < ticks; i++) stepForce(g.nodes, g.edges, g.byId, cfg, null, null, applyRepulsion, 1);
  return Number(process.hrtime.bigint() - start) / 1e6 / ticks;
}

function termOnly(n, cfg) {
  const g = graph(n);
  const start = process.hrtime.bigint();
  for (let i = 0; i < ticks; i++) applyClusterForces(g.nodes, cfg, 1);
  return Number(process.hrtime.bigint() - start) / 1e6 / ticks;
}

const median = (list) => [...list].sort((a, b) => a - b)[Math.floor(list.length / 2)];
const on = { ...GRAPH_DEFAULTS, cluster: true, cluster_attract: 0.5, cluster_repel: 0.5, cluster_by: "type" };
console.log("nodes  off ms/tick  on ms/tick  added  term ms  term/off");
for (const n of sizes) {
  time(n, GRAPH_DEFAULTS); time(n, on); // warm up
  const off = [], with_ = [], term = [];
  for (let r = 0; r < 5; r++) { off.push(time(n, GRAPH_DEFAULTS)); with_.push(time(n, on)); term.push(termOnly(n, on)); }
  const a = median(off), b = median(with_), c = median(term);
  console.log(`${String(n).padStart(5)}  ${a.toFixed(3).padStart(11)}  ${b.toFixed(3).padStart(10)}  ${((b / a - 1) * 100).toFixed(1).padStart(5)}%`
    + `  ${c.toFixed(3).padStart(7)}  ${(c / a * 100).toFixed(1).padStart(7)}%`);
}
