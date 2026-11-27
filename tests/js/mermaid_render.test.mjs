// Run: node --experimental-vm-modules --test tests/js/mermaid_render.test.mjs
// Mermaid blocks in dom.js: placeholder, lazy load only when a block exists,
// rendering through a stubbed Mermaid, the SVG cache, one initialize per
// theme, and the error fallback with the original source.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

const WEB = new URL("../../ark_studio/web/js/", import.meta.url);
const m = await import(new URL("rich-model.js", WEB).href);

// A minimal DOM: enough of Element for dom.js's h(), the Mermaid code and
// querySelectorAll("[data-mermaid]").
class FakeNode {}
class FakeText extends FakeNode {
  constructor(text) { super(); this.textContent = String(text); this.parentNode = null; }
}
class FakeElement extends FakeNode {
  constructor(tag) {
    super();
    this.tagName = tag.toUpperCase();
    this.children = [];
    this.attributes = {};
    this.dataset = {};
    this.style = { setProperty() {}, removeProperty() {} };
    this.listeners = {};
    this.parentNode = null;
    this._html = "";
    const classes = new Set();
    this.classList = {
      add: (...names) => names.forEach((n) => classes.add(n)),
      remove: (...names) => names.forEach((n) => classes.delete(n)),
      contains: (n) => classes.has(n),
      toggle: (n, on) => ((on ?? !classes.has(n)) ? classes.add(n) : classes.delete(n)),
    };
    Object.defineProperty(this, "className", { get: () => [...classes].join(" "), set: (v) => { classes.clear(); String(v).split(/\s+/).filter(Boolean).forEach((n) => classes.add(n)); } });
  }
  get isConnected() { let n = this; while (n.parentNode) n = n.parentNode; return n === documentRoot; }
  setAttribute(k, v) {
    this.attributes[k] = String(v);
    if (k.startsWith("data-")) this.dataset[k.slice(5).replace(/-(\w)/g, (_, c) => c.toUpperCase())] = String(v);
  }
  getAttribute(k) { return this.attributes[k] ?? null; }
  removeAttribute(k) { delete this.attributes[k]; }
  appendChild(child) { child.parentNode = this; this.children.push(child); this._html = ""; return child; }
  replaceChildren(...nodes) { this.children = []; this._html = ""; nodes.forEach((n) => this.appendChild(n)); }
  remove() { if (this.parentNode) this.parentNode.children = this.parentNode.children.filter((c) => c !== this); this.parentNode = null; }
  set innerHTML(v) { this.children = []; this._html = String(v); }
  get innerHTML() { return this._html; }
  get textContent() { return this._html || this.children.map((c) => c.textContent).join(""); }
  set textContent(v) { this.replaceChildren(new FakeText(v)); }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  all() { return this.children.flatMap((c) => (c instanceof FakeElement ? [c, ...c.all()] : [])); }
  querySelectorAll(selector) {
    if (selector === "[data-mermaid]") return this.all().filter((el) => "mermaid" in el.dataset);
    return [];
  }
  querySelector(selector) {
    if (selector === "svg, .mermaid-pending") return this._html.includes("<svg") ? {} : this.all().find((el) => el.classList.contains("mermaid-pending")) || null;
    if (selector === "svg") return this._html.includes("<svg") ? {} : null;
    return null;
  }
  closest() { return null; }
}
const documentRoot = new FakeElement("html");

async function loadDom({ mermaid, failImport = false } = {}) {
  documentRoot.children = [];
  documentRoot.dataset = { theme: "light" };
  const imports = [];
  const document = {
    documentElement: documentRoot,
    body: documentRoot,
    createElement: (tag) => new FakeElement(tag),
    createElementNS: (_, tag) => new FakeElement(tag),
    createTextNode: (text) => new FakeText(text),
    getElementById: () => null,
    querySelectorAll: () => [],
  };
  const context = vm.createContext({
    document, Node: FakeNode, URL, Map, Set, WeakSet, Promise, Error, Object, String, Number, Math, Array, JSON, console,
    setTimeout, clearTimeout, queueMicrotask,
  });
  const synthetic = (values) => new vm.SyntheticModule(Object.keys(values), function () {
    for (const [name, value] of Object.entries(values)) this.setExport(name, value);
  }, { context });
  const model = new vm.SourceTextModule(await readFile(new URL("rich-model.js", WEB), "utf8"), { identifier: "rich-model.js", context });
  await model.link(() => { throw new Error("rich-model must stay dependency-free"); });
  await model.evaluate();
  const stubs = {
    "./icons.js": { icon: (name) => new FakeElement(`icon-${name}`) },
    "./i18n.js": { t: (key) => `T:${key}`, locale: () => "en", typeLabel: (x) => x },
    "./api.js": { authenticatedUrl: (u) => u },
    "./download-link.js": { saveDownload: () => {} },
    "./viz.js": { vizBlock: () => new FakeElement("div") },
    "./rich-html.js": { sanitizeRichHtml: () => null },
    "./markdown.js": { markdownTokens: () => [], markdownInlineTokens: () => [] },
    "./graph-core.js": { TYPE_COLORS: {}, typeColor: () => "#000" },
  };
  const source = await readFile(new URL("dom.js", WEB), "utf8");
  const dom = new vm.SourceTextModule(source, {
    identifier: "dom.js", context,
    importModuleDynamically: async (specifier) => {
      imports.push(specifier);
      if (failImport) throw new Error("offline");
      const mod = synthetic({ default: mermaid });
      await mod.link(() => {});
      await mod.evaluate();
      return mod;
    },
  });
  await dom.link(async (specifier) => {
    if (specifier === "./rich-model.js") return model;
    if (!stubs[specifier]) throw new Error(`unexpected import ${specifier}`);
    return synthetic(stubs[specifier]);
  });
  await dom.evaluate();
  return { dom: dom.namespace, imports, documentRoot };
}

function fakeMermaid() {
  const calls = { init: [], render: [] };
  return {
    calls,
    initialize: (config) => calls.init.push(config),
    render: async (id, source) => {
      calls.render.push(source);
      if (source.includes("BROKEN")) throw new Error("Parse error on line 2:\nmore detail");
      return { svg: `<svg id="${id}" viewBox="0 0 120 80"><text>${source.length}</text></svg>` };
    },
  };
}

const settle = () => new Promise((resolve) => setTimeout(resolve, 20));

test("no mermaid block: Mermaid is never requested", async () => {
  const { dom, imports } = await loadDom({ mermaid: fakeMermaid() });
  const root = new FakeElement("div");
  root.appendChild(new FakeElement("p"));
  documentRoot.appendChild(root);
  dom.typesetMermaid(root);
  await settle();
  assert.equal(dom.mermaidRequested(), false);
  assert.deepEqual(imports, []);
});

test("a mermaid block shows a placeholder and starts loading the ESM entry at once", async () => {
  const { dom, imports } = await loadDom({ mermaid: fakeMermaid() });
  const block = dom.mermaidBlock("flowchart TB\n  A --> B");
  assert.ok(block.classList.contains("pending"));
  assert.equal(block.textContent, "T:mermaid.rendering");
  assert.equal(dom.mermaidRequested(), true);
  await settle();
  assert.deepEqual(imports, ["../vendor/mermaid/mermaid.esm.min.js"]);
});

test("renders in strict mode, caches the SVG and initialises once per theme", async () => {
  const mermaid = fakeMermaid();
  const { dom } = await loadDom({ mermaid });
  const source = "flowchart TB\n  subgraph 客户端\n    A[桌面] -.-> B[网页]\n  end\n  B -.-x C";
  const root = new FakeElement("div");
  documentRoot.appendChild(root);
  root.appendChild(dom.mermaidBlock(source));
  root.appendChild(dom.mermaidBlock("flowchart LR\n  甲 --> 乙"));
  dom.typesetMermaid(root);
  await settle();
  const blocks = root.querySelectorAll("[data-mermaid]");
  assert.ok(blocks.every((b) => b.classList.contains("rendered")));
  assert.match(blocks[0].innerHTML, /^<svg /);
  assert.equal(blocks[0].dataset.hint, "T:mermaid.open");
  assert.equal(blocks[0].getAttribute("role"), "button");
  assert.equal(mermaid.calls.init.length, 1);
  assert.equal(mermaid.calls.init[0].securityLevel, "strict");
  assert.equal(mermaid.calls.init[0].startOnLoad, false);
  assert.equal(mermaid.calls.render.length, 2);

  // A repaint (new block, same source) reuses the cached SVG synchronously.
  const again = dom.mermaidBlock(source);
  assert.ok(again.classList.contains("rendered"));
  assert.equal(mermaid.calls.render.length, 2);

  // Dark theme: one more initialize, then renders for the new theme.
  documentRoot.dataset.theme = "dark";
  const dark = dom.mermaidBlock(source);
  root.appendChild(dark);
  dom.typesetMermaid(root);
  await settle();
  assert.ok(dark.classList.contains("rendered"));
  assert.equal(mermaid.calls.init.length, 2);
  assert.equal(mermaid.calls.init[1].theme, "dark");
});

test("a syntax error keeps the original source with a short error line", async () => {
  const { dom } = await loadDom({ mermaid: fakeMermaid() });
  const root = new FakeElement("div");
  documentRoot.appendChild(root);
  const source = "flowchart TB\n  BROKEN [";
  const block = dom.mermaidBlock(source);
  root.appendChild(block);
  dom.typesetMermaid(root);
  await settle();
  assert.ok(block.classList.contains("error"));
  assert.ok(!block.classList.contains("rendered"));
  assert.equal(block.getAttribute("role"), null);
  const [head, pre] = block.children;
  assert.equal(head.textContent, "Mermaid: Parse error on line 2:");
  assert.equal(pre.tagName, "PRE");
  assert.equal(pre.textContent, source);
});

test("Mermaid unavailable (offline, not cached): the source is shown, not a blank", async () => {
  const { dom } = await loadDom({ failImport: true });
  const root = new FakeElement("div");
  documentRoot.appendChild(root);
  const block = dom.mermaidBlock("flowchart TB\n  A --> B");
  root.appendChild(block);
  dom.typesetMermaid(root);
  await settle();
  assert.ok(block.classList.contains("error"));
  assert.equal(block.children[0].textContent, "Mermaid: T:mermaid.unavailable");
  assert.equal(block.children[1].textContent, "flowchart TB\n  A --> B");
});

test("mermaidNaturalSize: viewBox first, then max-width, whole pixels", () => {
  assert.deepEqual(m.mermaidNaturalSize("0 0 812.5 600.2", "812.5px"), { width: 813, height: 601 });
  assert.deepEqual(m.mermaidNaturalSize("-8 -8 300,120"), { width: 300, height: 120 });
  assert.deepEqual(m.mermaidNaturalSize("", "500px"), { width: 500, height: 300 });
  assert.deepEqual(m.mermaidNaturalSize("0 0 0 0", ""), { width: 800, height: 480 });
  assert.deepEqual(m.mermaidNaturalSize("garbage", null), { width: 800, height: 480 });
});
