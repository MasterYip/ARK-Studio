// Run: node --experimental-vm-modules --test tests/js/home_hero_idle.test.mjs
// Copy prompt hang (fix/copy-prompt-hang): every live-sync change re-renders
// Home.  Each render built a new header GraphView whose force layout and
// per-frame redraw restarted behind the page and behind the Copy dialog; on
// a machine without GPU compositing that kept the main thread at ~100 %.
// The header graph is kept across renders, parks under a dialog / in a
// hidden tab / off the page, and an unchanged palette is not re-applied.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import vm from "node:vm";
import { loadGraphChange, loadGraphCore } from "./load_graph_core.mjs";

async function graphModule(context) {
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
    return new vm.SyntheticModule(names, function () {
      for (const name of names) this.setExport(name, () => ({}));
    }, { context: parent.context });
  });
  await module.evaluate();
  return module.namespace;
}

test("the header graph's simulation parks under a dialog and in a hidden tab, and wakes after", async () => {
  const frames = [];
  const doc = { hidden: false, modal: false, querySelector: (sel) => (sel === ".modal-backdrop" && doc.modal ? {} : null) };
  const context = vm.createContext({
    performance, document: doc, localStorage: { setItem() {} },
    requestAnimationFrame: (fn) => { frames.push(fn); return frames.length; },
    cancelAnimationFrame: () => {},
  });
  const { GraphView } = await graphModule(context);
  const graph = Object.create(GraphView.prototype);
  let steps = 0;
  Object.assign(graph, {
    opts: { backdrop: true }, layout: "force", running: false, alpha: 1, zoom: 1,
    nodes: [{ id: 1, x: 0, y: 0, vx: 0, vy: 0 }], host: { isConnected: true },
    width: 400, height: 200, _tally() {}, _draw() {},
    _physics() { steps += 1; return { mean: 5, max: 5 }; },
  });
  const pump = (n) => { for (let i = 0; i < n && frames.length; i++) frames.shift()(); };

  graph._start();
  pump(3);
  assert.equal(steps, 3);
  assert.equal(graph.running, true);

  // The Copy dialog opens: the next frame parks, no further frames are asked for.
  doc.modal = true;
  pump(1);
  assert.equal(graph.running, false);
  assert.equal(frames.length, 0);
  assert.equal(steps, 3);
  graph.wakeBackdrop(); // still under the dialog
  assert.equal(frames.length, 0);

  // Closed: it continues where it was.
  doc.modal = false;
  graph.wakeBackdrop();
  pump(2);
  assert.equal(steps, 5);

  // A hidden tab and a header that left the page park it too.
  doc.hidden = true;
  pump(1);
  assert.equal(graph.running, false);
  doc.hidden = false;
  graph.host.isConnected = false;
  graph.wakeBackdrop();
  assert.equal(frames.length, 0);
  graph.host.isConnected = true;
  graph.wakeBackdrop();
  pump(1);
  assert.equal(steps, 6);

  // Graphs in the app (not the decoration) never park.
  graph.running = false;
  frames.length = 0;
  const main = Object.create(GraphView.prototype);
  let mainSteps = 0;
  Object.assign(main, {
    opts: {}, layout: "force", running: false, alpha: 1, zoom: 1, nodes: [{ id: 1, x: 0, y: 0 }],
    host: { isConnected: true }, _tally() {}, _draw() {}, _physics() { mainSteps += 1; return { mean: 5, max: 5 }; },
  });
  doc.modal = true;
  main._start();
  pump(2);
  assert.equal(mainSteps, 2);
});

test("Home keeps one header graph across renders and passes new data in place", () => {
  const src = readFileSync(new URL("../../ark_studio/web/js/views/overview.js", import.meta.url), "utf8");
  const hero = src.slice(src.indexOf("function heroGraph"), src.indexOf("// -------------------------------------------------------------------- pulse"));
  // Same project: the kept stage moves into the new header; data goes through setData(remote).
  assert.match(hero, /hero && hero\.project === project\) \{\s*host\.appendChild\(hero\.stage\)/);
  assert.match(hero, /hero\.graph\.setData\(nodes, edges, \{ remote: true \}\)/);
  // Only one GraphView is built per project, and it is not handed to the
  // per-render teardown (destroyVisuals) that rebuilt it on every change.
  assert.equal((hero.match(/new GraphView/g) || []).length, 1);
  assert.doesNotMatch(hero, /ctx\.trackGraph/);
  // Closing a dialog wakes a parked header graph.
  const components = readFileSync(new URL("../../ark_studio/web/js/components.js", import.meta.url), "utf8");
  assert.match(components, /backdrop\.remove\(\);[\s\S]{0,120}ark:modal-close/);
});

test("an unchanged project palette is not re-applied on every refresh", async () => {
  let writes = 0;
  const root = {
    dataset: { theme: "dark" },
    style: { colorScheme: "", setProperty() { writes += 1; }, removeProperty() { writes += 1; } },
  };
  const context = vm.createContext({
    document: { documentElement: root, querySelector: () => null },
    getComputedStyle: () => ({ getPropertyValue: () => "" }),
    localStorage: { getItem: () => null, setItem() {} },
    window: { dispatchEvent() {} }, CustomEvent: class {},
  });
  const module = new vm.SourceTextModule(
    await readFile(new URL("../../ark_studio/web/js/appearance.js", import.meta.url), "utf8"), { context },
  );
  await module.link(() => { throw new Error("appearance.js has no imports"); });
  await module.evaluate();
  const { applyProjectPalette } = module.namespace;
  const palette = { enabled: true, background: "#101222", surface: "#1c1f36", text: "#ecebff", muted: "#a5a3c4", line: "#303456", accent: "#6366f1" };
  applyProjectPalette(palette);
  const first = writes;
  assert.ok(first > 0);
  applyProjectPalette({ ...palette }); // same values, new object (every bootstrap)
  assert.equal(writes, first);
  root.dataset.theme = "light"; // a theme switch still re-applies
  applyProjectPalette(palette);
  assert.ok(writes > first);
});
