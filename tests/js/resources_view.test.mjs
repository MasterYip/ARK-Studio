// Run: node --experimental-vm-modules --test tests/js/resources_view.test.mjs
// Renders the Resources graph panel (resources-view.js) against a recording
// canvas and a tiny DOM: per-GPU detail rows, storage usage bars and the
// + / - fold keys.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";
import { loadNodeGraphLayout } from "./load_graph_core.mjs";

const COLORS = { ok: "#10b981", warn: "#f59e0b", danger: "#ef4444", accent: "#3b82f6" };

class El {
  constructor(tag) {
    this.tagName = String(tag).toUpperCase();
    this.children = [];
    this.attrs = {};
    this.style = {};
    this.dataset = {};
    this.listeners = new Map();
    this.className = "";
    this.textContent = "";
    const el = this;
    this.classList = {
      add(...names) { el.className = [...new Set([...el.className.split(" ").filter(Boolean), ...names])].join(" "); },
      remove(...names) { el.className = el.className.split(" ").filter((n) => n && !names.includes(n)).join(" "); },
      toggle(name, on) { if (on ?? !this.contains(name)) this.add(name); else this.remove(name); },
      contains(name) { return el.className.split(" ").includes(name); },
    };
  }
  appendChild(child) { if (child !== null && child !== undefined && child !== false) this.children.push(child); return child; }
  append(...children) { for (const child of children) this.appendChild(child); }
  replaceChildren(...children) { this.children = []; this.append(...children); }
  setAttribute(name, value) { this.attrs[name] = String(value); }
  getAttribute(name) { return this.attrs[name] ?? null; }
  addEventListener(type, fn) { if (!this.listeners.has(type)) this.listeners.set(type, []); this.listeners.get(type).push(fn); }
  removeEventListener() {}
  emit(type, props = {}) {
    const event = { type, target: this, preventDefault() { this.defaultPrevented = true; }, ...props };
    for (const fn of this.listeners.get(type) || []) fn(event);
    return event;
  }
  contains(node) { return node === this || this.children.some((c) => c instanceof El && c.contains(node)); }
  querySelector() { return null; }
  getBoundingClientRect() { return { left: 0, top: 0, width: 1200, height: 800 }; }
  focus() {}
  setPointerCapture() {}
  get isConnected() { return true; }
  get text() {
    return [this.textContent, ...this.children.map((c) => (c instanceof El ? c.text : String(c)))].join(" ");
  }
  findAll(pred, out = []) {
    if (pred(this)) out.push(this);
    for (const c of this.children) if (c instanceof El) c.findAll(pred, out);
    return out;
  }
}

class Canvas2D {
  constructor() { this.texts = []; this.fills = []; this.fillStyle = ""; this.font = ""; this.path = null; }
  measureText(text) { return { width: String(text).length * 4.5 }; }
  fillText(text, x, y) { this.texts.push({ text: String(text), x, y, fill: this.fillStyle }); }
  fill() { this.fills.push({ color: this.fillStyle, rect: this.path }); }
  roundRectPath(x, y, w, h) { this.path = { x, y, w, h }; }
  beginPath() { this.path = null; }
  fillRect(x, y, w, h) { this.fills.push({ color: this.fillStyle, rect: { x, y, w, h } }); }
  moveTo(x, y) { if (!this.path) this.path = { x, y, w: 0, h: 0 }; }
}
for (const name of ["save", "restore", "translate", "scale", "setTransform", "clearRect", "stroke", "strokeRect", "arc",
  "lineTo", "closePath", "bezierCurveTo", "setLineDash"]) Canvas2D.prototype[name] = function () {};
// roundRect() in resources-view.js starts with beginPath + moveTo(x + r, y):
// remember the rectangle from the arcTo calls instead.
Canvas2D.prototype.arcTo = function (x1, y1) {
  const p = this.path || { x: x1, y: y1, w: 0, h: 0 };
  const x0 = Math.min(p.x, x1), y0 = Math.min(p.y, y1);
  this.path = { x: x0, y: y0, w: Math.max(p.x + p.w, x1) - x0, h: Math.max(p.y + p.h, y1) - y0 };
};

async function mount({ payload, dashboard = null } = {}) {
  const store = new Map();
  const ctx2d = new Canvas2D();
  const documentEl = new El("document");
  const context = vm.createContext({
    window: { devicePixelRatio: 1 }, document: documentEl, console,
    localStorage: { getItem: (k) => store.get(k) ?? null, setItem: (k, v) => store.set(k, String(v)), removeItem: (k) => store.delete(k) },
    getComputedStyle: () => ({ getPropertyValue: () => "" }),
    ResizeObserver: class { observe() {} disconnect() {} },
    requestAnimationFrame: () => 0, cancelAnimationFrame() {}, matchMedia: () => ({ matches: true }),
    performance: { now: () => 0 }, setInterval: () => 1, clearInterval() {}, setTimeout: () => 0,
    Date, Math, JSON, Number, String, Array, Object, Set, Map, Promise, Error,
  });
  const domModule = new vm.SyntheticModule(["h", "clear"], function () {
    this.setExport("h", (tag, props, ...children) => {
      const el = new El(tag);
      if (tag === "canvas") el.getContext = () => ctx2d;
      for (const [k, v] of Object.entries(props || {})) {
        if (v === undefined || v === null || v === false) continue;
        if (k === "class") el.className = v;
        else if (k === "style") Object.assign(el.style, v);
        else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
        else el.setAttribute(k, v);
      }
      for (const child of children.flat(Infinity)) {
        if (child === null || child === undefined || child === false) continue;
        el.appendChild(child instanceof El ? child : String(child));
      }
      return el;
    });
    this.setExport("clear", (el) => { el.children = []; });
  }, { context });
  const i18n = new vm.SyntheticModule(["t", "hasKey"], function () {
    this.setExport("t", (key, vars) => (key === "resources.storage.data_tag" ? "data"
      : vars && Object.keys(vars).length ? `${key}${JSON.stringify(vars)}` : key));
    this.setExport("hasKey", () => false);
  }, { context });
  const icons = new vm.SyntheticModule(["icon"], function () { this.setExport("icon", () => new El("svg")); }, { context });
  const api = new vm.SyntheticModule(["api", "cachedThenFresh", "peekCached"], function () {
    this.setExport("api", { resourcesGraph: async () => payload });
    this.setExport("cachedThenFresh", async (path, load, apply) => { apply(payload); return payload; });
    this.setExport("peekCached", async (path) => (path === "/api/gpu/dashboard" ? dashboard : null));
  }, { context });
  const menus = [];
  const menu = new vm.SyntheticModule(["openContextMenu"], function () {
    this.setExport("openContextMenu", (spec) => { menus.push(spec); });
  }, { context });
  // resource-open.js: what the menu asks for (tests/js/resource_open_model.test.mjs covers the entries).
  const opens = [];
  const resourceOpen = new vm.SyntheticModule(["connectionStore", "openMenuItems"], function () {
    this.setExport("connectionStore", () => ({ refresh: async () => {} }));
    this.setExport("openMenuItems", (store, input) => { opens.push(input); return [{ label: "open-entry" }]; });
  }, { context });
  const model = new vm.SourceTextModule(
    await readFile(new URL("../../ark_studio/web/js/resources-model.js", import.meta.url), "utf8"), { context });
  await model.link(() => { throw new Error("resources-model.js must remain dependency-free"); });
  const sizeModel = new vm.SourceTextModule(
    await readFile(new URL("../../ark_studio/web/js/location-size-model.js", import.meta.url), "utf8"), { context });
  await sizeModel.link((specifier) => (specifier === "./resources-model.js" ? model
    : (() => { throw new Error(`unexpected import ${specifier}`); })()));
  const sizeDom = new vm.SyntheticModule(["sizeBadge", "refreshLocationSize"], function () {
    this.setExport("sizeBadge", () => new El("span"));
    this.setExport("refreshLocationSize", async () => ({ queued: true, message: "queued" }));
  }, { context });
  // device-merge.js: the merge dialog (Go tests cover the merge itself).
  const deviceMerge = new vm.SyntheticModule(["mergeDevices"], function () {
    this.setExport("mergeDevices", async () => null);
  }, { context });
  const layout = await loadNodeGraphLayout(context);
  const view = new vm.SourceTextModule(
    await readFile(new URL("../../ark_studio/web/js/resources-view.js", import.meta.url), "utf8"), { context });
  const modules = { "./dom.js": domModule, "./i18n.js": i18n, "./icons.js": icons, "./api.js": api,
    "./context-menu.js": menu, "./node-graph-layout.js": layout, "./resources-model.js": model,
    "./location-size-model.js": sizeModel, "./location-size.js": sizeDom, "./resource-open.js": resourceOpen,
    "./device-merge.js": deviceMerge };
  await view.link((specifier) => modules[specifier] || (() => { throw new Error(`unexpected import ${specifier}`); })());
  await view.evaluate();
  const panel = view.namespace.resourcesGraphPanel({ toast() {}, navigateWorkspace() {} });
  const state = panel.__resources;
  const stage = panel.findAll((el) => el.className.includes("resources-stage"))[0];
  const details = panel.findAll((el) => el.tagName === "ASIDE")[0];
  // load() runs from setTimeout in the app; drive it through Refresh here.
  const refresh = panel.findAll((el) => el.tagName === "BUTTON" && el.text.includes("resources.refresh"))[0];
  refresh.emit("click");
  for (let i = 0; i < 5; i += 1) await new Promise((resolve) => setImmediate(resolve));
  const key = (k, extra = {}) => stage.emit("keydown", { key: k, ctrlKey: false, metaKey: false, altKey: false, shiftKey: false,
    target: stage, ...extra });
  // One clean frame to inspect (F fits and redraws).
  ctx2d.texts = []; ctx2d.fills = [];
  key("f");
  return { panel, state, stage, details, ctx2d, key, menus, opens };
}

const payload = {
  generated_at: "2026-11-16T09:00:30Z",
  devices: [
    { id: "ark", kind: "ark", label: "ARK server", scope: "shared", status: { state: "online" },
      metrics: { disk_pct: 72.5, disk_used_gb: 725, disk_total_gb: 1000 } },
    { id: "gpu:a", kind: "gpu-server", label: "a", scope: "shared", status: { state: "online" },
      probe: { probed_at: "2026-11-16T09:00:00Z", ok: true },
      occupancy: { gpus_total: 2, gpus_ours: 1, gpus_busy_others: 1, gpus_free: 0, mem_total_mb: 49128, mem_ours_mb: 1100, mem_others_mb: 23100 },
      storage: [
        { mount: "/data", used_bytes: 6.5 * 2 ** 40, total_bytes: 7.2 * 2 ** 40, free_bytes: 0.7 * 2 ** 40, percent: 90, data: true },
        { mount: "/", used_bytes: 403 * 2 ** 30, total_bytes: 916 * 2 ** 30, free_bytes: 466 * 2 ** 30, percent: 47 },
        { mount: "/scratch", used_bytes: 2.4 * 2 ** 40, total_bytes: 3.6 * 2 ** 40, free_bytes: 1.2 * 2 ** 40, percent: 66 },
      ] },
  ],
  resources: [
    { id: "f", device: "ark", kind: "storage", label: "Project folder", path: "/srv/p", metrics: { disk_pct: 72.5 } },
    { id: "db", device: "ark", kind: "database", label: "Project database" },
    { id: "g0", device: "gpu:a", kind: "gpu", label: "GPU 0 · NVIDIA GeForce RTX 4090",
      metrics: { index: 0, util_pct: 3, mem_used_mb: 1200, mem_total_mb: 24564, mem_ours_mb: 1100, mem_others_mb: 100,
        temperature_c: 41, job: "train", ours: true } },
    { id: "g1", device: "gpu:a", kind: "gpu", label: "GPU 1 · NVIDIA GeForce RTX 4090",
      metrics: { index: 1, util_pct: 97, mem_used_mb: 23000, mem_total_mb: 24564, mem_ours_mb: 0, mem_others_mb: 23000, temperature_c: 77 } },
    { id: "c", device: "gpu:a", kind: "cpu", label: "CPU & memory",
      metrics: { cpu_pct: 5, cores: 16, load1: 1.5, mem_used_mb: 12000, mem_total_mb: 64000 } },
  ],
  connections: [{ id: "l", source: "ark", target: "gpu:a", kind: "ssh", state: "ok" }],
};

const dashboard = {
  checked_at: "2026-11-16T09:00:05Z",
  probe: { servers: [{ server: "a", ok: true, gpus: [{ index: 0, power_w: 70.25 }, { index: 1, power_w: 350.5 }],
    system: { processes: [{ pid: 1234, gpu_index: 0, memory_mb: 1100, job_id: "gpu_abc", sm_pct: 12 }] } }] },
  jobs: [{ id: "gpu_abc", task: "T-0042", job: "train", owner: "alice" }],
};

const textsWith = (ctx2d, needle) => ctx2d.texts.filter((item) => item.text.includes(needle));

test("an expanded GPU server card draws one detail row per GPU with bars, temperature and power", async () => {
  const { state, ctx2d } = await mount({ payload, dashboard });
  assert.ok(state.model, "loaded");
  const card = state.layout.get("gpu:a");
  assert.equal(card.collapsed, false);
  for (const title of ["GPU 0", "GPU 1"]) assert.equal(textsWith(ctx2d, title).length, 1, title);
  assert.ok(textsWith(ctx2d, "NVIDIA GeForce RTX 4090").length >= 1 || textsWith(ctx2d, "NVIDIA").length >= 2, "model names");
  assert.ok(textsWith(ctx2d, "41°C · 70 W").length === 1, "GPU 0 temperature and power (from the same probe)");
  assert.ok(textsWith(ctx2d, "77°C · 351 W").length === 1, "GPU 1 temperature and power");
  assert.equal(textsWith(ctx2d, "resources.bar.util").length, 2, "a utilisation bar per GPU");
  assert.equal(textsWith(ctx2d, "resources.bar.mem").length, 2, "a memory bar per GPU");
  assert.equal(textsWith(ctx2d, "97%").length, 1);
  assert.equal(textsWith(ctx2d, "1.2 / 24 GiB").length, 1, "memory used / total");
  assert.equal(textsWith(ctx2d, "22.5 / 24 GiB").length, 1);
  assert.equal(textsWith(ctx2d, "resources.gpu.jobs").length, 1, "jobs line on the busy GPU");
  assert.ok(textsWith(ctx2d, "resources.bar.cpu").length && textsWith(ctx2d, "resources.bar.ram").length, "CPU & RAM bars");
  // GPU 1's 97% utilisation and 94% memory are red; GPU 0's 3% is green.
  const fills = ctx2d.fills.map((f) => f.color);
  assert.ok(fills.includes(COLORS.danger) && fills.includes(COLORS.ok));
});

test("storage mounts are usage bars labelled mount · used / total · %, the data mount marked", async () => {
  const { state, ctx2d } = await mount({ payload });
  const card = state.layout.get("gpu:a");
  const top = card.y - card.h / 2;
  for (const [mount, sizes, tone] of [["/data", "6.5 / 7.2 TiB · 90%", "danger"], ["/", "403 / 916 GiB · 47%", "ok"],
    ["/scratch", "2.4 / 3.6 TiB · 66%", "warn"]]) {
    const label = ctx2d.texts.find((item) => item.text === mount);
    assert.ok(label, mount);
    const row = ctx2d.texts.find((item) => item.text === sizes);
    assert.ok(row, sizes);
    assert.equal(row.y, label.y, `${mount}: one line`);
    // The bar under the label: a fill in the threshold colour on that row.
    const bar = ctx2d.fills.find((f) => f.color === COLORS[tone] && f.rect && f.rect.y > label.y && f.rect.y < label.y + 20
      && f.rect.w > 20);
    assert.ok(bar, `${mount} bar in ${tone}`);
    assert.ok(label.y > top, "inside the card");
  }
  const tag = ctx2d.texts.filter((item) => item.text === "DATA");
  assert.equal(tag.length, 1, "only the data mount is tagged");
  assert.equal(ctx2d.texts.find((item) => item.text === "/data").fill, COLORS.accent);
});

test("the ARK server's project folder disk is a bar too, in the usage tones", async () => {
  const { ctx2d } = await mount({ payload });
  const folder = ctx2d.texts.find((item) => item.text === "Project folder");
  assert.ok(folder);
  const sizes = ctx2d.texts.find((item) => item.text === "675 / 931 GiB · 73%");
  assert.ok(sizes, "decimal GB shown in binary units");
  assert.ok(ctx2d.fills.some((f) => f.color === COLORS.warn && f.rect && f.rect.y > folder.y && f.rect.y < folder.y + 20), "amber at 72.5%");
  assert.ok(textsWith(ctx2d, "resources.disk_summary").length === 1, "summary line with sizes");
});

test("the details panel shows every GPU, CPU & memory and every mount as bars, with processes", async () => {
  const { state, details, key } = await mount({ payload, dashboard });
  state.selectedIds = new Set(["gpu:a"]);
  state.selected = { device: "gpu:a", resource: null };
  key("Enter");
  const blocks = details.findAll((el) => el.attrs["data-resources-detail"]);
  assert.deepEqual(blocks.map((el) => el.attrs["data-resources-detail"]),
    ["g0", "g1", "c", "gpu:a/storage/df/0", "gpu:a/storage/df/1", "gpu:a/storage/df/2"]);
  const bars = details.findAll((el) => el.className === "resources-bar");
  assert.equal(bars.length, 2 + 2 + 2 + 3);
  const fillsOf = (block) => block.findAll((el) => el.tagName === "I" && el.className.startsWith("fill")).map((el) => el.className);
  assert.deepEqual(fillsOf(blocks[3]), ["fill danger"], "/data 90%");
  assert.deepEqual(fillsOf(blocks[5]), ["fill warn"], "/scratch 66%");
  assert.deepEqual(fillsOf(blocks[1]), ["fill danger", "fill danger"], "GPU 1 97% / 94%");
  assert.ok(blocks[0].text.includes("PID 1234"), "processes listed");
  assert.ok(blocks[0].text.includes("train") && blocks[0].text.includes("alice"), "job and owner");
  assert.ok(blocks[0].text.includes("70 W"));
  assert.ok(blocks[3].text.includes("data"), "data mount marked");
});

test("+ / = expand and - collapses the selection, else the card under the pointer, else nothing", async () => {
  const { state, key } = await mount({ payload });
  key("-");
  assert.deepEqual([...state.collapsed], [], "nothing selected or focused");
  state.hover = { device: "gpu:a", card: state.layout.get("gpu:a") };
  key("-");
  assert.deepEqual([...state.collapsed], ["gpu:a"], "the focused card");
  assert.equal(state.layout.get("gpu:a").rows.length, 0);
  key("+", { shiftKey: true });
  assert.deepEqual([...state.collapsed], []);
  key("-");
  key("=");
  assert.deepEqual([...state.collapsed], [], "= is + without Shift");
  state.hover = null;
  state.selectedIds = new Set(["ark", "gpu:a"]);
  state.selected = { device: "ark", resource: null };
  key("-");
  assert.deepEqual([...state.collapsed].sort(), ["ark", "gpu:a"]);
  key("*", { shiftKey: true });
  assert.deepEqual([...state.collapsed], [], "* is an alias of +");
  key("/");
  assert.deepEqual([...state.collapsed].sort(), ["ark", "gpu:a"], "/ is an alias of -");
  key("+", { ctrlKey: true });
  assert.equal(state.collapsed.size, 2, "Ctrl++ stays browser zoom");
});

test("right-clicking a location offers the open entries for its path; a card for the device", async () => {
  const { panel, state, menus, opens } = await mount({ payload });
  const canvas = panel.findAll((el) => el.tagName === "CANVAS")[0];
  const card = state.layout.get("ark");
  const click = (wx, wy) => {
    const ev = { button: 2, pointerId: 1, pointerType: "mouse", clientX: wx * state.zoom + state.pan.x,
      clientY: wy * state.zoom + state.pan.y, preventDefault() {}, stopPropagation() {} };
    canvas.emit("pointerdown", ev);
    canvas.emit("pointerup", ev);
  };
  click(card.x, card.y - card.h / 2 + 8); // the header
  assert.equal(opens.at(-1).device.id, "ark");
  assert.equal(opens.at(-1).path, null);
  assert.ok(menus.at(-1).items.some((item) => item?.label === "open-entry"));
  const row = card.rows.findIndex((r) => r.id === "f");
  const top = card.y - card.h / 2 + 50 + (card.summary ? 22 : 0) + card.rowTops[row] + 6;
  click(card.x, top);
  assert.equal(opens.at(-1).path, "/srv/p");
});
