// Run: node --experimental-vm-modules --test tests/js/graph_origin.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";
import { loadGraphChange, loadGraphCore } from "./load_graph_core.mjs";

async function fixture(nodeTransitions = true) {
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
      getMindSettings: () => ({ nodeTransitions }),
    };
    return new vm.SyntheticModule(names, function () {
      for (const name of names) this.setExport(name, values[name] || (() => ({})));
    }, { context: parent.context });
  });
  await module.evaluate();
  const graph = Object.create(module.namespace.GraphView.prototype);
  Object.assign(graph, {
    id: "origin-test", opts: { persistLayout: false }, layout: "force",
    nodes: [], edges: [], byId: new Map(), visibleRelations: new Set(["part_of"]),
    collapsed: new Set(), pan: { x: 0, y: 0 }, zoom: 1, width: 1000, height: 800, alpha: 0,
    physics: { repulsion: 0, distance: 0, gravity: 0.02, damping: 0.3 },
    _draw() {}, _start() {}, _warm() {}, _rebuildLegend() {}, _cancelCameraAnimation() {},
  });
  return graph;
}

test("filtering an offset subset keeps gravity at world origin", async () => {
  const graph = await fixture();
  graph.setData([{ id: 1, x: -200, y: -80 }, { id: 2, x: 200, y: 80 }], []);
  graph.setData([{ id: 2 }], [], { preserveCamera: true });
  assert.equal(graph.nodes[0].x, 200, "filtering must retain a surviving node's position");
  graph._physics();
  assert.ok(graph.nodes[0].x < 200, "gravity must pull the offset subset toward zero");
  assert.ok(graph.nodes[0].y < 80);
  for (let i = 0; i < 300; i++) graph._physics();
  assert.ok(Math.abs(graph.nodes[0].x) < 0.001);
  assert.ok(Math.abs(graph.nodes[0].y) < 0.001);
});

test("force Fit keeps the same origin for asymmetric filter results", async () => {
  const graph = await fixture();
  graph.setData([{ id: 1, x: -100, y: -100 }, { id: 2, x: 500, y: 200 }], []);
  assert.equal(Math.abs(graph.pan.x), 0);
  assert.equal(Math.abs(graph.pan.y), 0);
  graph.setData([{ id: 2 }], []);
  assert.equal(Math.abs(graph.pan.x), 0);
  assert.equal(Math.abs(graph.pan.y), 0);
  assert.ok(Math.abs(graph.nodes[0].x * graph.zoom) <= graph.width * 0.4);
});

test("data updates preserve a manually panned camera", async () => {
  const graph = await fixture();
  graph.setData([{ id: 1, x: 150, y: -80 }], []);
  graph.zoom = 0.7;
  graph.pan = { x: 90, y: -50 };
  graph.setData([{ id: 1 }], [], { preserveCamera: true });
  graph._physics();
  assert.equal(graph.zoom, 0.7);
  assert.equal(graph.pan.x, 90);
  assert.equal(graph.pan.y, -50);
});

test("other layouts still fit their content bounds", async () => {
  const graph = await fixture();
  graph.layout = "node";
  graph.nodes = [{ id: 1, x: 500, y: 200, w: 200, h: 64 }];
  graph.fit();
  assert.equal(graph.pan.x, -500 * graph.zoom);
  assert.equal(graph.pan.y, -200 * graph.zoom);
});

test("an immediate filter update paints all nodes without a synchronous force warm-up", async () => {
  const graph = await fixture();
  let warmups = 0;
  graph._warm = () => { warmups++; };
  graph.setData([{ id: 1, x: 150, y: -80 }], [], { warm: false });
  graph.setData([{ id: 1 }, { id: 2 }], [], { warm: false, preserveCamera: true });
  assert.equal(warmups, 0);
  assert.equal(graph.nodes.length, 2);
  assert.equal(graph.nodes[0].x, 150);
  assert.ok(graph.nodes.every((n) => (n._appear ?? 1) === 1));
  assert.equal(graph._ghosts.length, 0);
});

test("the title-board composition changes only its camera", async () => {
  const graph = await fixture();
  graph.opts.backdrop = true;
  graph.nodes = [{ id: 1, x: -1200, y: 800 }, { id: 2, x: 2600, y: -400 }]; // wide enough to stay under the zoom cap
  const physics = JSON.stringify(graph.physics), positions = JSON.stringify(graph.nodes);
  graph._fitBackdrop();
  const zoom = graph.zoom;
  graph.height /= 2;
  graph._fitBackdrop();
  assert.equal(graph.zoom, zoom / 2);
  assert.equal(JSON.stringify(graph.physics), physics);
  assert.equal(JSON.stringify(graph.nodes), positions);
  assert.equal(graph._transform().x, graph.width);
});

test("filter fades include every removed node and discard ghosts when nodes return", async () => {
  const graph = await fixture();
  let transitions = 0;
  graph._animateTransition = () => { transitions++; };
  const nodes = Array.from({ length: 100 }, (_, i) => ({ id: i + 1, x: i, y: 0 }));
  graph.setData(nodes, [], { warm: false });
  graph.setData([nodes[0]], [], { animate: true, warm: false });
  assert.equal(graph._ghosts.length, 99);
  graph.setData(nodes, [], { animate: true, warm: false });
  assert.equal(graph._ghosts.length, 0);
  assert.equal(graph.nodes.length, 100);
  assert.equal(graph.nodes[0]._appear, 1);
  assert.equal(graph.nodes[1]._appear, 0);
  assert.equal(transitions, 2);
});

test("disabling node transitions retains immediate filtering without force warm-up", async () => {
  const graph = await fixture(false);
  graph._animateTransition = () => { throw Error("Disabled transition started"); };
  graph._warm = () => { throw Error("Filter performed a synchronous warm-up"); };
  graph.setData([{ id: 1, x: 0, y: 0 }], [], { warm: false });
  graph.setData([{ id: 2 }], [], { animate: true, preserveCamera: true });
  assert.equal(graph._ghosts.length, 0);
  assert.equal(graph.nodes[0]._appear ?? 1, 1);
});

test("the Home backdrop never scales a small graph past its cap", async () => {
  const graph = await fixture();
  graph.opts.backdrop = true;
  graph.nodes = [{ id: 1, x: -20, y: 10 }, { id: 2, x: 30, y: -10 }];
  graph._fitBackdrop();
  assert.ok(graph.zoom <= 1.6, `zoom ${graph.zoom}`);
});

test("node size follows the current links: a parent with new children grows", async () => {
  const graph = await fixture();
  graph.nodes = [{ id: 1, degree: 0 }, { id: 2, degree: 1 }, { id: 3, degree: 1 }, { id: 4, degree: 1 }];
  graph.edges = [{ id: "a", source: 2, target: 1 }, { id: "b", source: 3, target: 1 }, { id: "c", source: 4, target: 1 }];
  graph._syncDegrees();
  assert.equal(graph.nodes[0].degree, 3);
  assert.equal(graph.nodes[1].degree, 1);
  graph.edges.push({ id: "d", source: 1, target: 4 });
  graph._syncDegrees();
  assert.equal(graph.nodes[0].degree, 4);
  assert.equal(graph.nodes[3].degree, 2);
});
