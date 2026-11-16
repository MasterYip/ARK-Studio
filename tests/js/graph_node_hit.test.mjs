// Run: node --experimental-vm-modules --test tests/js/graph_node_hit.test.mjs
// Node-graph edge hit testing only samples bezier segments near the pointer;
// it must pick exactly the edge a full scan of every route's polyline picks.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";
import { loadGraphCore, loadNodeGraphLayout, loadNodeCard, loadGraphSprites, loadMindLayout } from "./load_graph_core.mjs";

async function load() {
  const stub = { addEventListener() {}, removeEventListener() {} };
  const context = vm.createContext({ window: stub, document: stub, ResizeObserver: class { observe() {} }, performance });
  const core = await loadGraphCore(context);
  const graphModule = new vm.SourceTextModule(
    await readFile(new URL("../../web/js/graph.js", import.meta.url), "utf8"), { context },
  );
  const imports = {
    "./dom.js": ["h", "clear", "typeColor", "statusColor", "clamp", "copyText"],
    "./i18n.js": ["t", "typeLabel", "statusLabel", "relLabel"],
    "./icons.js": ["icon", "drawStatusIcon"],
    "./physics.js": ["normalizeGraphPhysics", "GRAPH_DEFAULTS"],
    "./mind-settings.js": ["getMindSettings"],
    "./shortcuts.js": ["getTagShortcuts", "shortcutMatches"],
    "./markers.js": ["MARKERS", "markersFor", "markerArc"],
    "./context-menu.js": ["openContextMenu"],
    "./api.js": ["authenticatedUrl"],
    "./graph-force.js": ["applyRepulsion"],
    "./assign-menu.js": ["assignMenuItem"],
    "./export-menu.js": ["exportBundleMenuItem"],
    "./node-history.js": ["historyMenuItem"],
  };
  const nodeLayout = await loadNodeGraphLayout(context);
  const nodeCard = await loadNodeCard(context);
  const graphSprites = await loadGraphSprites(context);
  const mindLayout = await loadMindLayout(context, nodeCard);
  const modules = { "./graph-core.js": core, "./node-graph-layout.js": nodeLayout, "./node-card.js": nodeCard,
    "./graph-sprites.js": graphSprites, "./mind-layout.js": mindLayout };
  await graphModule.link((specifier) => modules[specifier] || new vm.SyntheticModule(imports[specifier], function () {
    for (const name of imports[specifier]) this.setExport(name, () => ({}));
  }, { context }));
  await graphModule.evaluate();
  return { GraphView: graphModule.namespace.GraphView, layout: nodeLayout.namespace };
}

test("node-graph edge hits match a full polyline scan", async () => {
  const { GraphView, layout } = await load();
  let state = 5;
  const random = () => ((state = (state * 1103515245 + 12345) % 2147483648) / 2147483648);
  const nodes = Array.from({ length: 160 }, (_, i) => ({ id: i + 1, w: 180, h: 60 }));
  const edges = [];
  for (let i = 2; i <= nodes.length; i++) {
    edges.push({ id: `t${i}`, source: i, target: 1 + Math.floor(random() * (i - 1)), relation: "part_of" });
    if (random() < 0.3) edges.push({ id: `c${i}`, source: i, target: 1 + Math.floor(random() * nodes.length), relation: "supports" });
  }
  const shown = edges.filter((e) => e.source !== e.target);
  const { positions, waypoints } = layout.layeredLayout(nodes, shown);
  for (const node of nodes) Object.assign(node, positions.get(node.id));
  const routes = layout.routeNodeEdges(nodes, shown, { waypoints });
  const graph = Object.create(GraphView.prototype);
  const camera = { x: 40, y: 300, z: 0.35 };
  Object.assign(graph, {
    layout: "node", edges: shown, visibleRelations: new Set(["part_of", "supports"]),
    canvas: { getBoundingClientRect: () => ({ left: 0, top: 0 }) },
    _shownEdges: () => shown, _nodeRoutes: () => routes, _transform: () => camera,
  });
  const reference = (px, py) => {
    let best = null, bestD = 8;
    for (const edge of shown) {
      const pts = layout.sampleRoute(routes.get(edge.id).segments, 10).map((p) => ({ x: p.x * camera.z + camera.x, y: p.y * camera.z + camera.y }));
      for (let i = 1; i < pts.length; i++) {
        const ax = pts[i - 1].x, ay = pts[i - 1].y, dx = pts[i].x - ax, dy = pts[i].y - ay, len = dx * dx + dy * dy;
        const t = len ? Math.max(0, Math.min(1, ((px - ax) * dx + (py - ay) * dy) / len)) : 0;
        const d = Math.hypot(px - (ax + t * dx), py - (ay + t * dy));
        if (d < bestD) { best = edge; bestD = d; }
      }
    }
    return best?.id ?? null;
  };
  let hits = 0;
  // Points on routes (sure hits) and random points (mostly misses).
  const probes = [];
  for (const edge of shown.slice(0, 120)) {
    const p = layout.cubicAt(routes.get(edge.id).segments[0], 0.37);
    probes.push([p.x * camera.z + camera.x + 2, p.y * camera.z + camera.y - 3]);
  }
  for (let i = 0; i < 300; i++) probes.push([random() * 1400, random() * 900]);
  for (const [px, py] of probes) {
    const expected = reference(px, py);
    assert.equal(graph._hitEdge({ clientX: px, clientY: py })?.id ?? null, expected, `at ${px},${py}`);
    if (expected) hits += 1;
  }
  assert.ok(hits >= 120, `probes hit edges (${hits})`);
});
