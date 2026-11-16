// Run: node --test tests/js/node_graph_layout.test.mjs
import assert from "node:assert/strict";
import { test } from "node:test";
import {
  layeredLayout, placeNewNodes, routeNodeEdges, layoutMetrics, sampleRoute, cubicAt, NODE_W, NODE_H,
} from "../../web/js/node-graph-layout.js";

const nodesOf = (ids) => ids.map((id) => ({ id, w: NODE_W, h: NODE_H }));
const edgesOf = (pairs) => pairs.map(([source, target], i) => ({ id: `e${i}`, source, target }));
const place = (nodes, positions) => nodes.map((node) => ({ ...node, ...positions.get(node.id) }));

function overlapping(nodes) {
  for (let i = 0; i < nodes.length; i++) for (let j = i + 1; j < nodes.length; j++) {
    const a = nodes[i], b = nodes[j];
    if (Math.abs(a.x - b.x) < (a.w + b.w) / 2 && Math.abs(a.y - b.y) < (a.h + b.h) / 2) return [a.id, b.id];
  }
  return null;
}

test("layers follow edge direction; long chains stay on one row", () => {
  const nodes = nodesOf(["a", "b", "c", "d"]);
  const edges = edgesOf([["a", "b"], ["b", "c"], ["c", "d"]]);
  const { positions, stats } = layeredLayout(nodes, edges);
  const x = (id) => positions.get(id).x;
  assert.ok(x("a") < x("b") && x("b") < x("c") && x("c") < x("d"));
  assert.equal(new Set(["a", "b", "c", "d"].map((id) => positions.get(id).y)).size, 1, "a chain is straight");
  assert.equal(stats.layers, 4);
  assert.equal(stats.crossings, 0);
});

test("crossing minimisation untangles a twisted bipartite graph", () => {
  // Input order would cross every edge; the layout must not.
  const nodes = nodesOf(["a1", "a2", "a3", "b3", "b2", "b1"]);
  const edges = edgesOf([["a1", "b1"], ["a2", "b2"], ["a3", "b3"]]);
  const { positions } = layeredLayout(nodes, edges);
  const laid = place(nodes, positions);
  const routes = routeNodeEdges(laid, edges);
  assert.equal(layoutMetrics(laid, edges, routes).crossings, 0);
});

test("cycles are broken and every card keeps its own space", () => {
  const nodes = nodesOf([1, 2, 3, 4, 5, 6, 7]);
  const edges = edgesOf([[1, 2], [2, 3], [3, 1], [3, 4], [4, 5], [6, 7]]);
  const { positions, stats } = layeredLayout(nodes, edges);
  assert.equal(stats.reversed, 1);
  assert.equal(stats.components, 2);
  const laid = place(nodes, positions);
  assert.equal(overlapping(laid), null);
  assert.ok(laid.every((node) => Number.isFinite(node.x) && Number.isFinite(node.y)));
});

test("isolated nodes and components are packed without overlap", () => {
  const ids = Array.from({ length: 40 }, (_, i) => i);
  const pairs = [];
  for (let i = 0; i < 30; i += 3) pairs.push([i, i + 1], [i + 1, i + 2]);
  const nodes = nodesOf(ids);
  const laid = place(nodes, layeredLayout(nodes, edgesOf(pairs)).positions);
  assert.equal(overlapping(laid), null);
  const width = Math.max(...laid.map((n) => n.x)) - Math.min(...laid.map((n) => n.x));
  const height = Math.max(...laid.map((n) => n.y)) - Math.min(...laid.map((n) => n.y));
  assert.ok(width / height < 6 && height / width < 6, "packing keeps a sensible aspect ratio");
});

test("a hub with many children and wide layers stays compact", () => {
  const ids = ["hub", ...Array.from({ length: 200 }, (_, i) => `c${i}`)];
  const nodes = nodesOf(ids);
  const edges = edgesOf(ids.slice(1).map((id) => [id, "hub"]));
  const { positions, stats } = layeredLayout(nodes, edges);
  assert.equal(overlapping(place(nodes, positions)), null);
  assert.ok(stats.layers <= 4, `wide layer wraps into a few columns (${stats.layers})`);
});

test("300 nodes lay out well within the time budget", () => {
  let state = 3;
  const random = () => ((state = (state * 1103515245 + 12345) % 2147483648) / 2147483648);
  const ids = Array.from({ length: 300 }, (_, i) => i);
  const pairs = [];
  for (let i = 1; i < 300; i++) {
    pairs.push([i, Math.floor(random() * i)]);
    if (random() < 0.3) pairs.push([i, Math.floor(random() * 300)]);
  }
  const nodes = nodesOf(ids);
  const edges = edgesOf(pairs.filter(([a, b]) => a !== b));
  const started = performance.now();
  const { positions } = layeredLayout(nodes, edges);
  const laid = place(nodes, positions);
  routeNodeEdges(laid, edges);
  assert.ok(performance.now() - started < 400, "layout + routing of 300 nodes stays interactive");
  assert.equal(overlapping(laid), null);
});

test("routes leave and enter ports horizontally through distinct slots", () => {
  const nodes = [
    { id: "s1", x: 0, y: -80, w: NODE_W, h: NODE_H },
    { id: "s2", x: 0, y: 80, w: NODE_W, h: NODE_H },
    { id: "t", x: 400, y: 0, w: NODE_W, h: NODE_H },
  ];
  const edges = edgesOf([["s1", "t"], ["s2", "t"], ["s1", "t"]]);
  const routes = routeNodeEdges(nodes, edges);
  const ends = edges.map((e) => routes.get(e.id).end.y);
  assert.equal(new Set(ends).size, 3, "parallel edges get their own input slots");
  assert.ok(ends.every((y) => y > -NODE_H / 2 && y < NODE_H / 2));
  for (const edge of edges) {
    const { segments, start, end } = routes.get(edge.id);
    const first = segments[0], last = segments[segments.length - 1];
    assert.equal(first[1], first[3], "horizontal tangent at the output port");
    assert.equal(last[5], last[7], "horizontal tangent at the input port");
    assert.equal(start.x, NODE_W / 2);
    assert.equal(end.x, 400 - NODE_W / 2);
  }
  // Slots are ordered by the other end, so the two sources do not cross.
  assert.ok(routes.get("e0").end.y < routes.get("e1").end.y);
});

test("routes go around cards in the way; backward edges loop around", () => {
  const nodes = [
    { id: "a", x: 0, y: 0, w: NODE_W, h: NODE_H },
    { id: "block", x: 400, y: 0, w: NODE_W, h: NODE_H },
    { id: "b", x: 800, y: 0, w: NODE_W, h: NODE_H },
  ];
  const edges = edgesOf([["a", "b"], ["b", "a"]]);
  const routes = routeNodeEdges(nodes, edges);
  const metrics = layoutMetrics(nodes, edges, routes);
  assert.equal(metrics.overlaps, 0);
  const back = sampleRoute(routes.get("e1").segments);
  assert.ok(back.some((p) => Math.abs(p.y) > NODE_H / 2), "the backward edge leaves the row");
  assert.equal(cubicAt(routes.get("e1").segments.at(-1), 1).x, -NODE_W / 2);
});

test("a manual bend is honoured", () => {
  const nodes = [{ id: "a", x: 0, y: 0, w: NODE_W, h: NODE_H }, { id: "b", x: 500, y: 0, w: NODE_W, h: NODE_H }];
  const routes = routeNodeEdges(nodes, [{ id: 1, source: "a", target: "b", bend: { x: 250, y: 160 } }]);
  const route = routes.get(1);
  assert.deepEqual(route.mid, { x: 250, y: 160 });
  assert.deepEqual(route.segments[0].slice(6), [250, 160]);
});

test("new nodes are placed beside their neighbours without moving saved ones", () => {
  const nodes = [
    { id: "a", x: 0, y: 0, w: NODE_W, h: NODE_H },
    { id: "b", x: 0, y: 100, w: NODE_W, h: NODE_H },
    { id: "new", w: NODE_W, h: NODE_H },
    { id: "lonely", w: NODE_W, h: NODE_H },
  ];
  const fresh = placeNewNodes(nodes, edgesOf([["new", "a"]]));
  assert.deepEqual([...fresh.keys()].sort(), ["lonely", "new"]);
  assert.ok(fresh.get("new").x < 0, "a source goes left of its target");
  const laid = nodes.map((n) => ({ ...n, ...(fresh.get(n.id) || {}) }));
  assert.equal(overlapping(laid), null);
  assert.equal(nodes[0].x, 0);
  assert.equal(nodes[2].x, undefined, "inputs are not mutated");
});

test("metrics count crossings, card overlaps and length", () => {
  const nodes = [
    { id: "a", x: 0, y: 0, w: 100, h: 40 }, { id: "b", x: 0, y: 200, w: 100, h: 40 },
    { id: "c", x: 400, y: 0, w: 100, h: 40 }, { id: "d", x: 400, y: 200, w: 100, h: 40 },
    { id: "m", x: 200, y: 100, w: 60, h: 40 },
  ];
  const edges = edgesOf([["a", "d"], ["b", "c"]]);
  const routes = new Map([
    ["e0", { points: [{ x: 50, y: 0 }, { x: 350, y: 200 }] }],
    ["e1", { points: [{ x: 50, y: 200 }, { x: 350, y: 0 }] }],
  ]);
  const metrics = layoutMetrics(nodes, edges, routes);
  assert.equal(metrics.crossings, 1);
  assert.equal(metrics.overlaps, 2, "both lines pass through the middle card");
  assert.equal(metrics.length, Math.round(2 * Math.hypot(300, 200)));
});

test("links from a narrow card run straight to its column edge before curving", () => {
  // Column 0: a narrow source between two wide cards; the target sits well below.
  const nodes = [
    { id: "wide1", x: 0, y: -80, w: 400, h: 60 },
    { id: "narrow", x: 0, y: 0, w: 160, h: 48 },
    { id: "wide2", x: 0, y: 80, w: 400, h: 60 },
    { id: "target", x: 600, y: 160, w: 200, h: 64 },
  ];
  const edges = [{ id: "e", source: "narrow", target: "target" }];
  const route = routeNodeEdges(nodes, edges).get("e");
  const [first] = route.segments;
  assert.equal(first[0], 80, "starts at the narrow card's port");
  assert.equal(first[6], 200, "straight to the column edge");
  assert.equal(first[1], first[7], "horizontal");
  assert.equal(layoutMetrics(nodes, edges, new Map([["e", route]])).overlaps, 0);
});

test("backward links make a compact rounded loop next to the cards", () => {
  // Target well left of and below its source.
  const nodes = [
    { id: "s", x: 600, y: 0, w: 200, h: 64 },
    { id: "t", x: 0, y: 300, w: 200, h: 64 },
  ];
  const route = routeNodeEdges(nodes, [{ id: "e", source: "s", target: "t" }]).get("e");
  const pts = sampleRoute(route.segments, 12);
  const xs = pts.map((p) => p.x), ys = pts.map((p) => p.y);
  assert.ok(Math.max(...xs) <= 700 + 22 + 0.01, "at most a short stub right of the source");
  assert.ok(Math.min(...xs) >= -100 - 22 - 0.01, "at most a short stub left of the target");
  assert.ok(Math.min(...ys) >= -1 && Math.max(...ys) <= 301, "runs in the gap between the cards, not around them");
  assert.equal(layoutMetrics(nodes, [{ id: "e", source: "s", target: "t" }], new Map([["e", route]])).overlaps, 0);
});

test("forward links use xyflow tangents: half the horizontal gap", () => {
  const nodes = [{ id: "a", x: 0, y: 0, w: 200, h: 64 }, { id: "b", x: 500, y: 200, w: 200, h: 64 }];
  const [seg] = routeNodeEdges(nodes, [{ id: "e", source: "a", target: "b" }]).get("e").segments;
  assert.equal(seg[2] - seg[0], 150);
  assert.equal(seg[6] - seg[4], 150);
});
