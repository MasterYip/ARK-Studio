// Run: node --experimental-vm-modules --test tests/js/gpu_note_composer.test.mjs
// T-0026: the GPU "not registered" note links to Setup (no one-click
// registration, no server names), and the assistant composer keeps a
// narrow-width layout (icon-only access badge, truncating model, visible send).
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

const web = (path) => new URL(`../../ark_studio/web/${path}`, import.meta.url);

class El {
  constructor(tag) {
    this.tagName = String(tag).toUpperCase();
    this.children = [];
    this.attrs = {};
    this.style = {};
    this.listeners = new Map();
    this.className = "";
  }
  appendChild(child) { this.children.push(child); return child; }
  setAttribute(name, value) { this.attrs[name] = String(value); }
  getAttribute(name) { return this.attrs[name] ?? null; }
  addEventListener(type, fn) { if (!this.listeners.has(type)) this.listeners.set(type, []); this.listeners.get(type).push(fn); }
  emit(type) { for (const fn of this.listeners.get(type) || []) fn({ type, target: this, preventDefault() {} }); }
  get text() { return this.children.map((c) => (c instanceof El ? c.text : String(c))).join(" "); }
  findAll(pred, out = []) {
    if (pred(this)) out.push(this);
    for (const c of this.children) if (c instanceof El) c.findAll(pred, out);
    return out;
  }
  has(cls) { return this.className.split(/\s+/).includes(cls); }
}

async function load(file, extraGlobals = {}) {
  const store = new Map();
  const context = vm.createContext({
    console, Event: class { constructor(type) { this.type = type; } },
    localStorage: { getItem: (k) => store.get(k) ?? null, setItem: (k, v) => store.set(k, String(v)) },
    ...extraGlobals,
  });
  const dom = new vm.SyntheticModule(["h"], function () {
    this.setExport("h", (tag, props, ...children) => {
      const el = new El(tag);
      for (const [k, v] of Object.entries(props || {})) {
        if (v === undefined || v === null || v === false) continue;
        if (k === "class") el.className = v;
        else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
        else el.setAttribute(k, v);
      }
      for (const child of children.flat(Infinity)) {
        if (child !== null && child !== undefined && child !== false) el.appendChild(child);
      }
      return el;
    });
  }, { context });
  const i18n = new vm.SyntheticModule(["t"], function () {
    this.setExport("t", (key, vars) => (vars && Object.keys(vars).length ? `${key}${JSON.stringify(vars)}` : key));
  }, { context });
  const icons = new vm.SyntheticModule(["icon"], function () { this.setExport("icon", (name) => { const el = new El("svg"); el.className = `icon ${name}`; return el; }); }, { context });
  const mod = new vm.SourceTextModule(await readFile(web(file), "utf8"), { context });
  const modules = { "./dom.js": dom, "./i18n.js": i18n, "./icons.js": icons };
  await mod.link((specifier) => modules[specifier] || (() => { throw new Error(`unexpected import ${specifier}`); })());
  await mod.evaluate();
  return { ns: mod.namespace, store };
}

const NAMES = ["gpu-1", "gpu-2", "gpu-3", "a100-box"];

test("GPU note: no register button, no server list, an Open Setup link", async () => {
  const { ns } = await load("js/gpu-note.js");
  let opened = 0;
  const note = ns.gpuRegistrationNote({ registered: [], available: NAMES, suggested: ["gpu-2"], hint_code: "not_registered" },
    () => { opened += 1; });
  assert.ok(note, "note rendered");
  for (const name of NAMES) assert.ok(!note.text.includes(name), `note names ${name}`);
  assert.ok(!note.text.includes("gpu.register.action"), "one-click register button is gone");
  assert.ok(note.text.includes("gpu.register.title") && note.text.includes("gpu.register.hint"));
  const buttons = note.findAll((el) => el.tagName === "BUTTON");
  assert.equal(buttons.length, 1);
  assert.ok(buttons[0].text.includes("gpu.register.open_setup"));
  buttons[0].emit("click");
  assert.equal(opened, 1);
  // Wrapping: the body may shrink below its content (no horizontal overflow).
  assert.ok(note.findAll((el) => el.has("gpu-register-body")).length === 1);
  // Nothing to say when the project registers a server.
  assert.equal(ns.gpuRegistrationNote({ registered: ["gpu-1"], available: [] }, () => {}), null);
  // Without a handler there is no dead button.
  assert.equal(ns.gpuRegistrationNote({ hint_code: "not_registered" }).findAll((el) => el.tagName === "BUTTON").length, 0);
});

test("openSetupResources opens Settings at Setup / Resources", async () => {
  const { ns, store } = await load("js/gpu-note.js");
  const seen = [];
  ns.openSetupResources({ navigateWorkspace: (id) => seen.push(id) });
  assert.deepEqual(seen, ["settings"]);
  assert.equal(store.get("ark.settingsTab"), "setupResources");
  ns.openSetupResources({ navigate: (id) => seen.push(`nav:${id}`) });
  assert.deepEqual(seen, ["settings", "nav:settings"]);
});

test("api.js no longer has the one-click gpuRegister helper", async () => {
  const api = await readFile(web("js/api.js"), "utf8");
  assert.ok(!api.includes("gpuRegister"));
  for (const lang of ["en", "zh"]) {
    const dict = await readFile(web(`js/i18n-${lang}.js`), "utf8");
    assert.ok(!dict.includes("gpu.register.action") && !dict.includes("gpu.register.done"), lang);
    assert.ok(dict.includes("\"gpu.register.open_setup\""), lang);
  }
});

test("assistant composer: narrow-layout classes", async () => {
  const { ns } = await load("js/assistant-composer.js");
  const ui = ns.createAssistantComposer({ placeholder: "chat.placeholder", onSubmit() {}, model: "DeepSeek" });
  const find = (cls) => ui.el.findAll((el) => el.has(cls));
  const access = find("ai-composer-access")[0];
  assert.equal(access.getAttribute("aria-label"), "composer.full_access", "label survives the icon-only collapse");
  assert.ok(access.getAttribute("title").includes("composer.full_access"), "tooltip names the mode");
  assert.equal(find("ai-composer-access-label").length, 1);
  assert.equal(find("ai-composer-model-label")[0].text, "DeepSeek");
  const bar = find("ai-composer-bar")[0];
  assert.equal(bar.children.at(-1), ui.send, "send is the last item of the bar");

  const css = (await readFile(web("css/app.css"), "utf8")).replace(/\s+/g, " ");
  const rule = (selector) => {
    const match = css.match(new RegExp(`(?:^|[}\\s])${selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}\\{([^}]*)\\}`));
    return match ? match[1] : "";
  };
  assert.match(rule(".ai-composer"), /container-type:inline-size/);
  assert.match(rule(".ai-composer-access"), /white-space:nowrap/);
  assert.match(rule(".ai-composer-access"), /min-width:0/);
  assert.match(rule(".ai-composer-model"), /min-width:0/);
  assert.match(rule(".ai-composer-access-label,.ai-composer-model-label"), /text-overflow:ellipsis/);
  assert.match(rule(".ai-composer-send"), /flex:0 0 auto/);
  assert.match(css, /@container \(max-width:\d+px\)\{[^@]*\.ai-composer-access-label\{display:none\}/);
});
