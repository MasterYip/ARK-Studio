// Run: node --experimental-vm-modules --test tests/js/graph_demo.test.mjs
// The graph walkthrough: its scripts only press keys the graph handles and
// the help lists, its captions exist in both languages, and the sandbox never
// reaches the server.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

const WEB = new URL("../../ark_studio/web/js/", import.meta.url);
const read = (name) => readFile(new URL(name, WEB), "utf8");

async function loadScript() {
  const calls = [];
  const network = (name) => () => { calls.push(name); throw new Error(`${name} must not be called`); };
  const context = vm.createContext({ fetch: network("fetch"), XMLHttpRequest: network("XMLHttpRequest"), WebSocket: network("WebSocket"), Promise });
  const mod = new vm.SourceTextModule(await read("graph-demo-script.js"), { context });
  await mod.link(() => { throw new Error("graph-demo-script.js must stay dependency-free"); });
  await mod.evaluate();
  return { m: mod.namespace, calls };
}

function dictKeys(source) {
  return new Set([...source.matchAll(/^\s*"([a-z0-9_.]+)":/gim)].map((match) => match[1])
    .concat([...source.matchAll(/,\s*"([a-z0-9_.]+)":/gi)].map((match) => match[1])));
}

function dict(source) {
  const map = new Map();
  for (const match of source.matchAll(/"(graph\.demo\.[a-z0-9_.]+)":\s*"((?:[^"\\]|\\.)*)"/gi)) map.set(match[1], JSON.parse(`"${match[2]}"`));
  return map;
}

function methodSource(source, name) {
  const start = source.indexOf(`\n  ${name}(`);
  assert.ok(start > 0, `${name} not found in graph.js`);
  const end = source.indexOf("\n  }\n", start);
  return source.slice(start, end);
}

test("every layout has a script on its own sample graph", async () => {
  const { m } = await loadScript();
  assert.deepEqual([...m.DEMO_LAYOUTS], ["force", "mind", "node"]);
  assert.equal(m.demoLayoutFor("tree"), "force");
  assert.equal(m.demoLayoutFor("mind"), "mind");
  const ops = new Set(["move", "click", "dblclick", "drag", "wheel", "key", "type", "button", "wait"]);
  for (const layout of m.DEMO_LAYOUTS) {
    const data = m.demoDataset(layout, (key) => `T:${key}`);
    assert.ok(data.nodes.length >= 6 && data.nodes.length <= 10, `${layout}: ${data.nodes.length} nodes`);
    assert.ok(data.nodes.every((node) => node.title.startsWith("T:graph.demo.node.")));
    const ids = new Set(data.nodes.map((node) => node.id));
    const edges = new Set(data.edges.map((edge) => edge.id));
    for (const edge of data.edges) assert.ok(ids.has(edge.source) && ids.has(edge.target), `${layout} ${edge.id}`);
    // A fresh copy each time: a run never changes the next one's data.
    data.nodes[0].title = "changed";
    assert.notEqual(m.demoDataset(layout).nodes[0].title, "changed");
    const steps = m.DEMO_SCRIPTS[layout];
    assert.ok(steps.length >= 6, `${layout}: ${steps.length} steps`);
    for (const step of steps) {
      assert.match(step.caption, /^graph\.demo\.caption\.[a-z_]+$/);
      assert.ok(step.ops.length > 0);
      for (const op of step.ops) {
        assert.ok(ops.has(op.op), `${layout}: unknown op ${op.op}`);
        for (const target of [op.to, op.from, op.at]) {
          if (target?.node) assert.ok(ids.has(target.node), `${layout}: no node ${target.node}`);
          if (target?.edge) assert.ok(edges.has(target.edge), `${layout}: no link ${target.edge}`);
        }
      }
    }
  }
});

test("captions, typed titles and labels exist in English and Chinese", async () => {
  const { m } = await loadScript();
  const en = dict(await read("i18n-en.js"));
  const zh = dict(await read("i18n-zh.js"));
  const used = new Set(["graph.demo.show_me", "graph.demo.show_keys", "graph.demo.title", "graph.demo.done"]);
  for (const layout of m.DEMO_LAYOUTS) {
    for (const step of m.DEMO_SCRIPTS[layout]) {
      used.add(step.caption);
      for (const op of step.ops) {
        if (op.op === "type") used.add(op.text);
        for (const token of m.opKeys(op)) if (token.startsWith("input.")) used.add(`graph.demo.${token}`);
      }
    }
    for (const node of m.demoDataset(layout).nodes) used.add(node.title);
  }
  for (const key of used) {
    assert.ok(en.get(key), `missing EN ${key}`);
    assert.ok(zh.get(key), `missing ZH ${key}`);
    for (const text of [en.get(key), zh.get(key)]) {
      assert.doesNotMatch(text, /[*_`#]{2}|\]\(|TODO|TBD|\{\w+\}/, `${key}: ${text}`);
    }
  }
  assert.deepEqual([...en.keys()].sort(), [...zh.keys()].sort());
});

test("each demo runs between 20 and 40 seconds", async () => {
  const { m } = await loadScript();
  const en = dict(await read("i18n-en.js"));
  for (const layout of m.DEMO_LAYOUTS) {
    const ms = m.estimateDuration(layout, (key) => en.get(key) || key);
    assert.ok(ms >= 20000 && ms <= 40000, `${layout}: ${ms} ms`);
  }
});

test("every key the demos press is handled by GraphView._onKeyDown in that layout and mode", async () => {
  const { m } = await loadScript();
  const graph = await read("graph.js");
  const handler = methodSource(graph, "_onKeyDown");
  const mindOnly = handler.slice(handler.indexOf('if (this.layout === "mind" && !event.ctrlKey'), handler.indexOf('if (this.mode === "edit") {'));
  for (const layout of m.DEMO_LAYOUTS) {
    let mode = m.DEMO_START_MODE[layout];
    let typed = false;
    for (const step of m.DEMO_SCRIPTS[layout]) {
      for (const op of step.ops) {
        if (op.op === "type" || (op.op === "dblclick" && step.ops.some((item) => item.op === "type"))) {
          assert.equal(mode, "edit", `${layout}: title editing needs Edit mode`);
        }
        if (op.op === "drag" && op.from.edge) assert.equal(mode, "edit", `${layout}: route handles show in Edit mode`);
        if (op.op === "button") assert.match(graph, new RegExp(`class: "${op.selector.slice(1)}[ "]`));
        if (op.op !== "key") { typed = op.op === "type"; continue; }
        const key = op.key;
        if (op.ctrl) {
          assert.ok(handler.includes(`event.key.toLowerCase() === "${key}"`), `Ctrl+${key}`);
        } else if (/^[a-z]$/.test(key)) {
          assert.ok(handler.includes(`event.key === "${key}" || event.key === "${key.toUpperCase()}"`), key);
        } else {
          const inList = handler.split("\n").some((line) => line.includes(".includes(event.key)") && line.includes(`"${key}"`));
          assert.ok(handler.includes(`event.key === ${JSON.stringify(key)}`) || inList, key);
        }
        if (["+", "-", "*", "/"].includes(key)) {
          assert.equal(layout, "mind", `${key} folds Mind cards only`);
          assert.ok(mindOnly.includes(`event.key === ${JSON.stringify(key)}`), `${key} is in the Mind block`);
        }
        // Tab and Enter (outside the title editor) add cards in Edit mode only.
        if (key === "Tab" || (key === "Enter" && !typed)) assert.equal(mode, "edit", `${layout}: ${key} adds a card in Edit mode`);
        if (key === "e") mode = "edit";
        if (key === "r") mode = "read";
        typed = false;
      }
    }
  }
});

test("every key and gesture shown in the corner is documented in the shortcut help", async () => {
  const { m } = await loadScript();
  const graph = await read("graph.js");
  const help = methodSource(graph, "_renderHelp");
  for (const layout of m.DEMO_LAYOUTS) {
    for (const step of m.DEMO_SCRIPTS[layout]) {
      for (const op of step.ops) {
        for (const token of m.opKeys(op)) {
          const ref = m.HELP_ROWS[token];
          assert.ok(ref, `${layout}: no help row for ${token}`);
          const listed = ref.startsWith("graph.") ? help.includes(`t("${ref}")`) : help.includes(`row("${ref}"`) || help.includes(`row(${JSON.stringify(ref)}`);
          assert.ok(listed, `${token} -> ${ref} is not in _renderHelp`);
        }
      }
    }
  }
});

test("overlay text for scripted and real input", async () => {
  const { m } = await loadScript();
  const tr = (key) => ({ "graph.demo.input.drag": "Drag", "graph.demo.input.click": "Click", "graph.demo.input.right_button": "Right button" })[key] || key;
  assert.equal(m.formatKeys(m.opKeys({ op: "key", key: "z", ctrl: true }), tr), "Ctrl + Z");
  assert.equal(m.formatKeys(m.opKeys({ op: "drag", alt: true }), tr), "Alt + Drag");
  assert.equal(m.formatKeys(m.opKeys({ op: "key", key: "ArrowLeft" }), tr), "←");
  assert.equal(m.formatKeys(m.opKeys({ op: "key", key: "Escape" }), tr), "Esc");
  assert.deepEqual([...m.opKeys({ op: "move" })], []);
  assert.equal(m.eventKeyTokens({ key: "Shift", shiftKey: true }), null);
  assert.deepEqual([...m.eventKeyTokens({ key: "*", shiftKey: true })], ["*"]);
  assert.deepEqual([...m.eventKeyTokens({ key: "Enter", shiftKey: true })], ["Shift", "Enter"]);
  assert.deepEqual([...m.eventKeyTokens({ key: "a", ctrlKey: true, shiftKey: true })], ["Ctrl", "Shift", "A"]);
  assert.equal(m.formatKeys(m.eventPointerTokens({ button: 2 }), tr), "Right button");
  assert.equal(m.formatKeys(m.eventPointerTokens({ button: 0, altKey: true }), tr), "Alt + Click");
});

test("the sandbox callbacks change only the sample graph and never call the network", async () => {
  const { m, calls } = await loadScript();
  const graph = { edges: [{ id: "e1", source: "d2", target: "d1", relation: "part_of" }] };
  const opts = m.demoGraphOptions({ newTitle: "New node", getGraph: () => graph });
  const child = opts.onAddNode({ id: "d1", type: "topic" }, "child");
  assert.equal(child.parentId, "d1");
  assert.equal(child.unit.title, "New node");
  const sibling = opts.onAddNode({ id: "d2", type: "note" }, "below");
  assert.equal(sibling.parentId, "d1");
  assert.notEqual(sibling.unit.id, child.unit.id);
  assert.equal(opts.onAddNode({ id: "d1" }, "below").orphan, true);
  assert.ok((await opts.onConnect({ id: "a" }, { id: "b" })).link.id);
  assert.equal(await opts.onDelete([{ id: "d1" }]), false);
  assert.equal(await opts.onRename({ id: "d1" }, "x"), true);
  assert.ok((await opts.onAddOrphan({ x: 0, y: 0 })).unit.id.startsWith("demo-new-"));
  for (const [name, fn] of Object.entries(opts)) if (typeof fn === "function") await fn({ id: "d1" }, { id: "d2" });
  opts.nodeLayoutStore.save({ positions: { a: { x: 1, y: 2 } }, bends: {}, widths: {} });
  assert.deepEqual({ ...opts.nodeLayoutStore.load().positions.a }, { x: 1, y: 2 });
  assert.deepEqual(calls, []);
});

test("the walkthrough builds its sandbox without the API module or storage of the real graph", async () => {
  const demo = await read("graph-demo.js");
  const imports = [...demo.matchAll(/^import [^;]*? from "(\.\/[^"]+)";$/gm)].map((match) => match[1]).sort();
  assert.deepEqual(imports, ["./dom.js", "./graph-demo-script.js", "./graph.js", "./i18n.js", "./icons.js"]);
  assert.doesNotMatch(demo, /\bapi\.|\bfetch\(|XMLHttpRequest|WebSocket|localStorage|ctx\./);
  for (const option of ["sandbox: true", "persistLayout: false", "persistMode: false", "allRelations: true"]) {
    assert.ok(demo.includes(option), option);
  }
  // demoGraphOptions supplies the node-layout store, so card positions stay in memory.
  assert.ok(demo.includes("...options"));
  const graph = await read("graph.js");
  assert.match(graph, /ark:graph-hover/);
  assert.match(graph, /!this\.opts\?\.sandbox && typeof CustomEvent === "function"/);
  // graph.js loads the walkthrough only when it is opened.
  assert.match(graph, /import\("\.\/graph-demo\.js"\)/);
  assert.doesNotMatch(graph, /^import .*graph-demo/m);
});
