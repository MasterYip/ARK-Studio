// Run: node --experimental-vm-modules --test tests/js/view_plugins.test.mjs
// View plugins (T-0025): manifest validation, the registry, the postMessage
// permission gate, bundle parsing and plugin slots in the workbench layout.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";
import zlib from "node:zlib";

const PURE = ["workbench-model.js", "plugin-api.js", "view-plugins.js", "plugin-bundle.js"];
const cache = new Map();
async function load(name) {
  if (cache.has(name)) return cache.get(name);
  if (!PURE.includes(name)) throw new Error(`${name} is not a pure module`);
  const source = await readFile(new URL(`../../ark_studio/web/js/${name}`, import.meta.url), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: name });
  cache.set(name, module);
  await module.link((specifier) => load(specifier.replace(/^\.\//, "")));
  return module;
}
async function namespace(name) {
  const module = await load(name);
  if (module.status !== "evaluated") await module.evaluate();
  return module.namespace;
}

const wm = await namespace("workbench-model.js");
const vp = await namespace("view-plugins.js");
const pb = await namespace("plugin-bundle.js");
const pa = await namespace("plugin-api.js");

const good = () => ({
  id: "node-word-count", name: "Node word count", version: "1.0.0", icon: "list", location: "right",
  titles: { en: "Word count", zh: "字数统计" }, entry: "index.html", permissions: ["selection:read", "notify"],
});

test("built-in views are first-party plugin manifests of VIEW_DEFAULTS", () => {
  assert.deepEqual(vp.BUILTIN_MANIFESTS.map((m) => m.id), wm.VIEW_DEFAULTS.map((v) => v.id));
  for (const manifest of vp.BUILTIN_MANIFESTS) {
    const view = wm.VIEW_DEFAULTS.find((v) => v.id === manifest.id);
    assert.equal(manifest.location, view.container);
    assert.equal(manifest.icon, view.icon);
    assert.equal(manifest.titleKey, view.titleKey);
    assert.equal(manifest.builtin, true);
    assert.equal(manifest.entry, `builtin:${manifest.id}`);
    assert.equal(vp.pluginViewId(manifest), manifest.id);
  }
  assert.equal(vp.pluginTitle(vp.BUILTIN_MANIFESTS[0], "en", (key) => `T(${key})`), `T(${wm.VIEW_DEFAULTS[0].titleKey})`);
});

test("registry: register, list, viewDefaults order, unregister and lifecycle checks", () => {
  const registry = vp.createRegistry();
  const events = [];
  registry.onChange((type, record) => events.push(`${type}:${record.viewId}`));
  for (const manifest of [...vp.BUILTIN_MANIFESTS].reverse()) registry.register(manifest, {});
  const user = vp.validateManifest(good()).manifest;
  const record = registry.register(user, { mount() {}, update() {}, unmount() {} });
  assert.equal(record.viewId, "u-node-word-count");
  assert.equal(registry.list().length, wm.VIEW_DEFAULTS.length + 1);
  // Built-ins keep their default order whatever the registration order.
  assert.deepEqual(registry.viewDefaults().map((v) => v.id), [...wm.VIEW_DEFAULTS.map((v) => v.id), "u-node-word-count"]);
  assert.deepEqual(registry.viewDefaults().at(-1), { id: "u-node-word-count", container: "right", icon: "list", plugin: "node-word-count" });
  assert.throws(() => registry.register(user, {}), /already registered/);
  assert.throws(() => registry.register({ ...user, id: "other-one" }, { mount: 1 }), /mount must be a function/);
  assert.equal(registry.unregister("u-node-word-count"), record);
  assert.equal(registry.has("u-node-word-count"), false);
  assert.equal(registry.unregister("u-node-word-count"), null);
  assert.equal(events.at(-1), "unregister:u-node-word-count");
});

test("manifest validation", () => {
  const ok = vp.validateManifest(good());
  assert.equal(ok.ok, true);
  assert.deepEqual(ok.manifest.permissions, ["selection:read", "notify"]);
  assert.equal(ok.manifest.apiVersion, 1);
  const keys = (raw, opts) => vp.validateManifest(raw, opts).errors.map((e) => e.key);
  assert.deepEqual(keys(null), ["plugins.err.manifest"]);
  assert.deepEqual(keys({ ...good(), id: "X" }), ["plugins.err.id"]);
  assert.deepEqual(keys({ ...good(), id: "search" }), ["plugins.err.id_reserved"], "built-in ids are reserved");
  assert.deepEqual(keys({ ...good(), id: "u-thing" }), ["plugins.err.id_reserved"]);
  assert.deepEqual(keys(good(), { reserved: ["node-word-count"] }), ["plugins.err.id_taken"]);
  assert.deepEqual(keys({ ...good(), version: "1" }), ["plugins.err.version"]);
  assert.deepEqual(keys({ ...good(), location: "top" }), ["plugins.err.location"]);
  assert.deepEqual(keys({ ...good(), titles: {} }), ["plugins.err.titles"]);
  assert.deepEqual(keys({ ...good(), entry: "../evil.html" }), ["plugins.err.entry"]);
  assert.deepEqual(keys({ ...good(), entry: "/abs.html" }), ["plugins.err.entry"]);
  assert.deepEqual(keys({ ...good(), entry: "app.exe" }), ["plugins.err.entry"]);
  assert.deepEqual(keys({ ...good(), permissions: ["selection:read", "cookies:read"] }), ["plugins.err.permission"]);
  assert.deepEqual(keys({ ...good(), apiVersion: 2 }), ["plugins.err.api_version"]);
  assert.deepEqual(keys({ ...good(), name: "x".repeat(61) }), ["plugins.err.name"]);
  // Lenient bits: a plain-string title is English; an unknown icon falls back.
  const loose = vp.validateManifest({ ...good(), titles: "Count", icon: "nope", permissions: ["notify", "notify"] });
  assert.deepEqual(loose.manifest.titles, { en: "Count" });
  assert.equal(loose.manifest.icon, vp.DEFAULT_PLUGIN_ICON);
  assert.deepEqual(loose.manifest.permissions, ["notify"]);
  // Unknown fields are dropped.
  assert.equal("token" in vp.validateManifest({ ...good(), token: "x" }).manifest, false);
  // Titles by language with fallbacks.
  assert.equal(vp.pluginTitle(ok.manifest, "zh"), "字数统计");
  assert.equal(vp.pluginTitle(ok.manifest, "zh-CN"), "字数统计");
  assert.equal(vp.pluginTitle(ok.manifest, "fr"), "Word count");
});

test("bridge gate: channel, id, method allow-list, permissions, params, size", () => {
  const granted = ["selection:read", "notify", "node:open"];
  const call = (method, params, extra = {}) => ({ type: "ark-plugin-call", channel: "c1", id: 7, method, params, ...extra });
  const gate = (message, opts = {}) => vp.gateRequest(message, { granted, channel: "c1", ...opts });
  assert.deepEqual(gate(call("selection.get")), { ok: true, id: 7, method: "selection.get", params: {}, bucket: "" });
  assert.equal(gate({ ...call("selection.get"), type: "other" }).code, "bad_message");
  assert.equal(gate(call("selection.get", {}, { channel: "c2" })).code, "bad_channel");
  assert.equal(gate(call("selection.get"), { channel: "" }).code, "bad_channel", "no channel yet: nothing passes");
  assert.equal(gate(call("selection.get", {}, { id: "7" })).code, "bad_id");
  assert.equal(gate(call("selection.get", {}, { id: -1 })).code, "bad_id");
  assert.equal(gate(call("cookies.read")).code, "unknown_method");
  assert.equal(gate(call("__proto__")).code, "unknown_method");
  assert.equal(gate(call("toString")).code, "unknown_method");
  assert.equal(gate(call("graph.snapshot")).code, "permission_denied");
  assert.equal(gate(call("comment.create", { uid: "T-0001", body: "hi" })).code, "permission_denied");
  assert.equal(gate(call("node.open", { uid: "../../api" })).code, "bad_params");
  assert.deepEqual(gate(call("node.open", { uid: "T-0024" })).params, { uid: "T-0024", target: "details" });
  assert.equal(gate(call("notify", { message: "   " })).code, "bad_params");
  const note = gate(call("notify", { message: `  ${"x".repeat(500)}`, kind: "evil" }));
  assert.equal(note.params.message.length, 200);
  assert.equal(note.params.kind, "info");
  assert.equal(gate(call("selection.get"), { size: vp.MAX_MESSAGE_BYTES + 1 }).code, "too_large");
  const writer = { granted: ["comment:write"], channel: "c1" };
  assert.equal(vp.gateRequest(call("comment.create", { uid: "TP-0005", body: " ok " }), writer).params.body, "ok");
  assert.equal(vp.gateRequest(call("comment.create", { uid: "TP-0005", body: "x".repeat(4001) }), writer).code, "bad_params");
});

test("what plugins see: state filtered by permission, nodes and graph without internals", () => {
  const host = { visible: true, theme: "dark", lang: "zh", location: "bottom",
    selection: { id: 5, uid: "S-0001", title: "T", type: "story", status: "active", body: "secret?", path: "/srv/x", token: "arka_x", tags: ["a"] } };
  const none = vp.pluginState(host, []);
  assert.deepEqual(none, { visible: true, theme: "dark", lang: "zh", location: "bottom" });
  const withSel = vp.pluginState(host, ["selection:read"]);
  assert.deepEqual(withSel.selection, { uid: "S-0001", title: "T", type: "story", status: "active", importance: "", tags: ["a"] });
  assert.equal(vp.publicNode(host.selection, { body: true }).body, "secret?");
  assert.equal("token" in vp.publicNode(host.selection, { body: true }), false);
  const graph = vp.publicGraph({
    nodes: [{ id: 1, uid: "A-1", title: "a", path: "/x" }, { id: 2, uid: "B-2", title: "b" }],
    edges: [{ source: 1, target: 2, relation: "supports", id: 9 }, { source: 1, target: 3, relation: "x" }],
  });
  assert.deepEqual(graph.links, [{ source: "A-1", target: "B-2", relation: "supports" }]);
  assert.equal(graph.nodes[0].path, undefined);
  assert.equal(vp.publicGraph({ nodes: [{ id: 1, uid: "A-1" }, { id: 2, uid: "B-2" }] }, { maxNodes: 1 }).truncated, true);
});

test("rate limit", () => {
  let now = 0;
  const allow = vp.createRateLimit(3, 1000, () => now);
  assert.deepEqual([allow(), allow(), allow(), allow()], [true, true, true, false]);
  now = 1000;
  assert.equal(allow(), true);
});

// ------------------------------------------------------------------ bundles
const MANIFEST_BLOCK = (m) => `<script type="application/ark-plugin+json">${JSON.stringify(m)}</script>`;

test("html and js bundles", () => {
  const html = `<!doctype html><html><head>${MANIFEST_BLOCK(good())}</head><body><script>ark.view({})</script></body></html>`;
  const parsed = pb.parseHtmlBundle(html);
  assert.equal(parsed.manifest.id, "node-word-count");
  assert.equal(parsed.html, html);
  const js = `/*! ark-plugin ${JSON.stringify({ ...good(), entry: "main.js" })} */\nark.view({ mount(el) { el.textContent = "</script><b>x</b>"; } });`;
  const fromJs = pb.parseJsBundle(js);
  assert.equal(fromJs.manifest.entry, "main.js");
  assert.match(fromJs.html, /^<!doctype html>/);
  assert.equal((fromJs.html.match(/<\/script>/g) || []).length, 1, "a </script> inside the code cannot end the block");
  assert.equal(pb.parseTextBundle(js, "x.js").manifest.id, "node-word-count");
  assert.equal(pb.parseTextBundle(html, "x.html").manifest.id, "node-word-count");
  assert.equal(pb.parseTextBundle(js, "/download?id=1").manifest.id, "node-word-count", "content decides without an extension");
  const errorKey = (fn) => { try { fn(); } catch (err) { return err.key; } return ""; };
  assert.equal(errorKey(() => pb.parseHtmlBundle("<html></html>")), "plugins.err.no_manifest");
  assert.equal(errorKey(() => pb.parseHtmlBundle(`<script type="application/ark-plugin+json">{oops</script>`)), "plugins.err.manifest_json");
  assert.equal(errorKey(() => pb.parseHtmlBundle(MANIFEST_BLOCK({ ...good(), id: "Bad" }))), "plugins.err.id");
  assert.equal(errorKey(() => pb.parseHtmlBundle("x".repeat(2 * 1024 * 1024 + 1))), "plugins.err.too_large");
});

function zip(entries, { deflate = true, root = "" } = {}) {
  const locals = [];
  const centrals = [];
  let offset = 0;
  for (const [rawName, text] of entries) {
    const name = Buffer.from(root + rawName);
    const data = Buffer.isBuffer(text) ? text : Buffer.from(text);
    const body = deflate ? zlib.deflateRawSync(data) : data;
    const local = Buffer.alloc(30);
    local.writeUInt32LE(0x04034b50, 0); local.writeUInt16LE(20, 4); local.writeUInt16LE(deflate ? 8 : 0, 8);
    local.writeUInt32LE(body.length, 18); local.writeUInt32LE(data.length, 22); local.writeUInt16LE(name.length, 26);
    const central = Buffer.alloc(46);
    central.writeUInt32LE(0x02014b50, 0); central.writeUInt16LE(20, 6); central.writeUInt16LE(deflate ? 8 : 0, 10);
    central.writeUInt32LE(body.length, 20); central.writeUInt32LE(data.length, 24); central.writeUInt16LE(name.length, 28);
    central.writeUInt32LE(offset, 42);
    locals.push(local, name, body);
    centrals.push(central, name);
    offset += 30 + name.length + body.length;
  }
  const dir = Buffer.concat(centrals);
  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0); end.writeUInt16LE(entries.length, 8); end.writeUInt16LE(entries.length, 10);
  end.writeUInt32LE(dir.length, 12); end.writeUInt32LE(offset, 16);
  return new Uint8Array(Buffer.concat([...locals, dir, end]));
}
const inflate = async (bytes) => new Uint8Array(zlib.inflateRawSync(Buffer.from(bytes)));
const PNG = Buffer.from("89504e470d0a1a0a", "hex");

test("zip bundles: manifest.json + entry, relative assets inlined", async () => {
  const files = [
    ["manifest.json", JSON.stringify(good())],
    ["index.html", `<html><head><link rel="stylesheet" href="css/app.css"></head><body><img src="img/logo.png"><script src="./app.js"></script><script src="https://cdn.example/x.js"></script></body></html>`],
    ["app.js", "ark.view({}); // </script> inside"],
    ["css/app.css", "body{color:red}"],
    ["img/logo.png", PNG],
  ];
  for (const options of [{}, { deflate: false }, { root: "my-plugin/" }]) {
    const bundle = await pb.parseZipBundle(zip(files, options), inflate);
    assert.equal(bundle.manifest.id, "node-word-count");
    assert.match(bundle.html, /<style>body\{color:red\}<\/style>/);
    assert.match(bundle.html, /<script>ark\.view\(\{\}\); \/\/ <\\\/script> inside<\/script>/);
    assert.match(bundle.html, /src="data:image\/png;base64,iVBORw0KGgo="/);
    assert.match(bundle.html, /src="https:\/\/cdn\.example\/x\.js"/, "external refs stay (the frame's CSP blocks them)");
  }
  const js = await pb.parseZipBundle(zip([["manifest.json", JSON.stringify({ ...good(), entry: "main.js" })], ["main.js", "ark.view({})"]]), inflate);
  assert.match(js.html, /<script>ark\.view\(\{\}\)<\/script>/);
  const key = async (bytes) => { try { await pb.parseZipBundle(bytes, inflate); } catch (err) { return err.key; } return ""; };
  assert.equal(await key(zip([["index.html", "x"]])), "plugins.err.no_manifest");
  assert.equal(await key(zip([["manifest.json", JSON.stringify(good())]])), "plugins.err.entry_missing");
  assert.equal(await key(new Uint8Array([1, 2, 3])), "plugins.err.zip");
});

test("the example plugin imports cleanly", async () => {
  const text = await readFile(new URL("../../ark_studio/web/plugins-examples/node-word-count.html", import.meta.url), "utf8");
  const bundle = pb.parseTextBundle(text, "node-word-count.html");
  assert.equal(bundle.manifest.id, "node-word-count");
  assert.deepEqual(bundle.manifest.permissions, ["selection:read", "notify"]);
  assert.deepEqual(bundle.manifest.titles, { en: "Word count", zh: "字数统计" });
  assert.match(bundle.html, /window\.ark\.view\(/);
});

test("workbench layout keeps plugin slots while the plugin is not loaded", () => {
  const catalog = [...wm.VIEW_DEFAULTS, { id: "u-node-word-count", container: "right", icon: "list" }];
  let layout = wm.createWorkbench(catalog);
  assert.deepEqual(layout.containers.right.views, ["details", "localgraph", "sessionprops", "u-node-word-count"]);
  layout = wm.placeView(layout, "u-node-word-count", "left", { anchor: "filters", side: "before" });
  // Without the plugin in the catalog (disabled, or not loaded yet) the slot stays.
  const reloaded = wm.parseWorkbench(wm.serializeWorkbench(layout));
  assert.deepEqual(reloaded.containers.left.views, ["u-node-word-count", "filters", "explorer", "search", "changes", "sessions"]);
  assert.deepEqual(wm.storableLayout(reloaded).containers.left.views, reloaded.containers.left.views);
  // Arbitrary unknown ids are still dropped.
  const junk = wm.normalizeWorkbench({ v: 1, containers: { left: { views: ["nope", "u-", "u-X"] } } });
  assert.deepEqual(junk.containers.left.views, ["filters", "explorer", "search", "changes", "sessions"]);
  // A removed plugin forgets its place.
  const removed = wm.removeViewSlot(reloaded, "u-node-word-count");
  assert.deepEqual(removed.containers.left.views, ["filters", "explorer", "search", "changes", "sessions"]);
  assert.equal(wm.removeViewSlot(removed, "u-node-word-count"), removed);
});

// ------------------------------------------------- D-0003: permission set
// One valid call per bridge method (params) and the permission it needs.
const VALID_PARAMS = {
  "selection.get": {}, "media.get": { id: 3 },
  "graph.snapshot": {}, "node.get": { uid: "S-0001" }, "nodes.search": { q: "control" }, "topics.list": {}, "topic.members": { uid: "TP-0001" },
  "events.subscribe": { events: ["selection"] }, "events.unsubscribe": {},
  "project.get": {},
  "storage.get": { key: "k" }, "storage.set": { key: "k", value: { a: 1 } }, "storage.remove": { key: "k" }, "storage.keys": {}, "storage.clear": {},
  "node.open": { uid: "S-0001", target: "editor" }, notify: { message: "hi" }, "clipboard.write": { text: "copy me" },
  "comment.create": { uid: "S-0001", body: "x" },
  "nodes.write": { ops: [{ op: "create", title: "New" }] },
  "link.open": { url: "https://example.org/a?b=1" },
  "ai.chat": { messages: [{ role: "user", content: "hi" }] },
};

test("D-0003 permissions: manifest validation and confirmation modes", () => {
  assert.deepEqual(vp.PLUGIN_PERMISSIONS, ["selection:read", "graph:read", "events", "project:read", "storage", "node:open", "notify",
    "clipboard:write", "comment:write", "node:write", "link:open", "ai:chat"]);
  const all = vp.validateManifest({ ...good(), permissions: [...vp.PLUGIN_PERMISSIONS].reverse() });
  assert.equal(all.ok, true);
  assert.equal(all.manifest.permissions.length, 12);
  for (const bad of ["cookies:read", "node:delete", "project:write", "settings:write", "token:read", "ai", "Node:write"]) {
    assert.deepEqual(vp.validateManifest({ ...good(), permissions: ["notify", bad] }).errors.map((e) => [e.key, e.field]), [["plugins.err.permission", bad]]);
  }
  assert.deepEqual(vp.PLUGIN_PERMISSIONS.filter((p) => vp.confirmMode(p) !== "none"), ["comment:write", "node:write", "link:open"]);
  assert.equal(vp.confirmMode("comment:write"), "each");
  assert.equal(vp.confirmMode("link:open"), "each");
  assert.equal(vp.confirmMode("node:write"), "session");
  assert.equal(vp.confirmMode("ai:chat"), "none");
});

test("D-0003 gate: every method needs its own permission (a plugin without it is denied)", () => {
  assert.deepEqual(Object.keys(VALID_PARAMS).sort(), Object.keys(vp.BRIDGE_METHODS).sort(), "a valid call for every method");
  const call = (method, params) => ({ type: "ark-plugin-call", channel: "c", id: 1, method, params });
  for (const [method, spec] of Object.entries(vp.BRIDGE_METHODS)) {
    assert.ok(vp.PLUGIN_PERMISSIONS.includes(spec.permission), method);
    const others = vp.PLUGIN_PERMISSIONS.filter((p) => p !== spec.permission);
    assert.equal(vp.gateRequest(call(method, VALID_PARAMS[method]), { granted: others, channel: "c" }).code, "permission_denied", method);
    const ok = vp.gateRequest(call(method, VALID_PARAMS[method]), { granted: [spec.permission], channel: "c" });
    assert.equal(ok.ok, true, `${method}: ${ok.code}`);
    assert.equal(ok.bucket, spec.bucket || "");
  }
  // Buckets with stricter limits.
  assert.equal(vp.BRIDGE_METHODS["ai.chat"].bucket, "ai");
  assert.equal(vp.BRIDGE_METHODS["nodes.write"].bucket, "write");
  assert.equal(vp.BRIDGE_METHODS["clipboard.write"].bucket, "clipboard");
  assert.equal(vp.BRIDGE_METHODS["link.open"].bucket, "link");
  // No method reaches tokens, settings, members or project deletion.
  for (const name of ["token.get", "settings.set", "project.delete", "members.add", "db.query", "fetch", "storage.__proto__"]) {
    assert.equal(vp.gateRequest(call(name, {}), { granted: vp.PLUGIN_PERMISSIONS, channel: "c" }).code, "unknown_method", name);
  }
  assert.equal(vp.gateRequest(call("selection.get", []), { granted: ["selection:read"], channel: "c" }).ok, true, "array params read as {}");
});

test("D-0003 parameters: links, chat, search, storage, clipboard, open, events", () => {
  const p = (method, params) => vp.BRIDGE_METHODS[method].params(params);
  // link:open: http(s) only, no credentials, normalised; the host is shown in the dialog.
  assert.deepEqual(p("link.open", { url: " https://Example.org/x " }), { url: "https://example.org/x", host: "example.org" });
  for (const url of ["javascript:alert(1)", "data:text/html,x", "file:///c:/x", "ftp://x.org", "https://u:p@x.org/", "//x.org", "", `https://x.org/${"a".repeat(2000)}`]) {
    assert.equal(p("link.open", { url }), null, url);
  }
  // ai:chat
  assert.deepEqual(p("ai.chat", { messages: [{ role: "user", content: " q " }], system: " s ", tools: false }),
    { messages: [{ role: "user", content: "q" }], tools: false, system: "s" });
  assert.equal(p("ai.chat", { messages: [{ role: "user", content: "q" }, { role: "assistant", content: "a" }] }), null, "last must be the user");
  assert.equal(p("ai.chat", { messages: [{ role: "system", content: "q" }] }), null);
  assert.equal(p("ai.chat", { messages: [] }), null);
  assert.equal(p("ai.chat", { messages: Array.from({ length: 21 }, () => ({ role: "user", content: "q" })) }), null);
  assert.equal(p("ai.chat", { messages: [{ role: "user", content: "x".repeat(8001) }] }), null);
  assert.equal(p("ai.chat", { messages: [{ role: "user", content: "q" }], system: "x".repeat(4001) }), null);
  const big = Array.from({ length: 7 }, (_, i) => ({ role: i % 2 ? "assistant" : "user", content: "x".repeat(7000) }));
  assert.equal(p("ai.chat", { messages: big }), null, "48,000 characters in all");
  // graph:read search is size-capped.
  assert.deepEqual(p("nodes.search", { q: "x", limit: 500, type: "claim" }), { q: "x", limit: pa.MAX_SEARCH_RESULTS, type: "claim" });
  assert.equal(p("nodes.search", { q: "  " }), null);
  assert.equal(p("nodes.search", { q: "x", type: "../x" }), null);
  assert.deepEqual(p("node.get", { id: 12 }), { id: 12 });
  assert.equal(p("node.get", { id: -1 }), null);
  // storage keys and values.
  assert.deepEqual(p("storage.set", { key: "notes", value: [1, "a"] }), { key: "notes", json: "[1,\"a\"]" });
  assert.equal(p("storage.set", { key: "notes" }), null, "a value is required");
  assert.equal(p("storage.get", { key: "x".repeat(201) }), null);
  assert.equal(p("storage.get", { key: "a\u0000b" }), null);
  // clipboard, node.open, events, media
  assert.equal(p("clipboard.write", { text: "x".repeat(100001) }), null);
  assert.equal(p("clipboard.write", { text: "" }), null);
  assert.equal(p("node.open", { uid: "S-0001", target: "settings" }), null);
  assert.deepEqual(p("events.subscribe", { events: ["graph", "graph", "project"] }), { events: ["graph", "project"] });
  assert.equal(p("events.subscribe", { events: ["cookies"] }), null);
  assert.equal(p("events.subscribe", {}), null);
  assert.deepEqual(p("events.unsubscribe", {}), { events: pa.PLUGIN_EVENTS });
  assert.equal(p("media.get", { id: "1" }), null);
});

test("D-0003 node:write ops: validation, refs, limits", () => {
  const ops = pa.validateWriteOps([
    { op: "create", ref: "$c", type: "claim", title: " Summary ", body: "b", tags: ["x", "x", " y "], importance: "p1", extra: 1 },
    { op: "update", uid: "T-0012", status: "done" },
    { op: "link", source: "$c", target: "S-0001", relation: "derived_from", note: "n" },
    { op: "unlink", id: 7 },
  ]);
  assert.deepEqual(ops, [
    { op: "create", title: "Summary", type: "claim", body: "b", importance: "p1", tags: ["x", "y"], ref: "$c" },
    { op: "update", uid: "T-0012", status: "done" },
    { op: "link", source: "$c", target: "S-0001", relation: "derived_from", note: "n" },
    { op: "unlink", id: 7 },
  ]);
  const bad = [
    [], null, [{ op: "delete", uid: "S-0001" }], [{ op: "create" }], [{ op: "create", title: "a\nb" }],
    [{ op: "update", uid: "S-0001" }], [{ op: "update", uid: "../x", title: "t" }], [{ op: "update", uid: "S-0001", importance: "p9" }],
    [{ op: "link", source: "$c", target: "S-0001", relation: "supports" }], // ref used before it is created
    [{ op: "create", ref: "$a", title: "a" }, { op: "create", ref: "$a", title: "b" }],
    [{ op: "link", source: "S-0001", target: "S-0001", relation: "supports" }],
    [{ op: "link", source: "S-0001", target: "S-0002", relation: "drop table" }],
    [{ op: "unlink", id: "7" }], [{ op: "update", uid: "S-0001", tags: "a,b" }],
    [{ op: "update", uid: "S-0001", type: "Claim!" }], [{ op: "update", uid: "S-0001", body: "x".repeat(pa.MAX_BODY_CHARS + 1) }],
    Array.from({ length: 51 }, () => ({ op: "create", title: "t" })),
  ];
  for (const raw of bad) assert.equal(pa.validateWriteOps(raw), null, String(JSON.stringify(raw)).slice(0, 80));
});

test("D-0003 node:write summary for the confirmation dialog", () => {
  const ops = pa.validateWriteOps([
    { op: "create", ref: "$a", type: "claim", title: "One" }, { op: "create", title: "Two" }, { op: "create", title: "Three" },
    { op: "update", uid: "T-0012", status: "done" }, { op: "link", source: "$a", target: "S-0001", relation: "supports" },
    { op: "link", source: "S-0002", target: "S-0001", relation: "cites" },
  ]);
  const summary = pa.summarizeWriteOps(ops);
  assert.deepEqual(summary.headline, [
    { key: "plugins.write.create_n", vars: { n: 3 } }, { key: "plugins.write.update_n", vars: { n: 1 } }, { key: "plugins.write.link_n", vars: { n: 2 } },
  ]);
  assert.deepEqual(summary.lines[3], { key: "plugins.write.line_update", vars: { uid: "T-0012", changes: "status → done" } });
  assert.deepEqual(summary.lines[4].vars, { source: "“One”", relation: "supports", target: "S-0001" });
  assert.equal(summary.lines[1].vars.type, "note");
});

test("D-0003 node:write confirmation: once, session, revoke, project switch", async () => {
  const grants = pa.createWriteGrants();
  const changes = [];
  grants.onChange((id) => changes.push(id));
  let asked = 0;
  const answer = (value) => () => { asked += 1; return Promise.resolve(value); };
  const write = (value, project = "p1", pluginId = "demo") => pa.authorizeWrite({ grants, pluginId, project, ask: answer(value) });
  // Declined: nothing is granted.
  await assert.rejects(write(null), (err) => err.code === "declined");
  // Once: asks every time.
  assert.equal(await write("once"), "once");
  assert.equal(await write("once"), "once");
  assert.equal(asked, 3);
  assert.deepEqual(grants.list(), []);
  // Session: asks once, then not again for this plugin and project.
  assert.equal(await write("session"), "session");
  assert.equal(await write(null), "session");
  assert.equal(asked, 4);
  assert.equal(grants.has("demo", "p1"), true);
  assert.equal(grants.has("other", "p1"), false, "per plugin");
  assert.equal(grants.has("demo", "p2"), false, "per project");
  // Revoke: asks again.
  assert.equal(grants.revoke("demo"), true);
  await assert.rejects(write(null), (err) => err.code === "declined");
  assert.equal(asked, 5);
  // A project switch ends grants given for the old project.
  await write("session", "p1");
  await write("session", "p1", "second");
  grants.projectChanged("p2");
  assert.deepEqual(grants.list(), []);
  assert.deepEqual(changes, ["demo", "demo", "demo", "second", "demo", "second"]);
});

test("D-0003 node:write apply: refs resolve to created uids, stops at the first failure", async () => {
  const ops = pa.validateWriteOps([
    { op: "create", ref: "$a", type: "claim", title: "Summary" },
    { op: "link", source: "$a", target: "S-0001", relation: "derived_from" },
    { op: "update", uid: "$a", status: "active" },
    { op: "unlink", id: 4 },
  ]);
  const calls = [];
  const result = await pa.applyWriteOps(ops, async (kind, payload) => {
    calls.push([kind, payload]);
    if (kind === "create") return { unit: { uid: "C-0042" } };
    if (kind === "link") return { id: 99 };
    return {};
  });
  assert.equal(result.ok, true);
  assert.deepEqual(result.created, { $a: "C-0042" });
  assert.deepEqual(calls, [
    ["create", { type: "claim", title: "Summary" }],
    ["link", { source: "C-0042", target: "S-0001", relation: "derived_from" }],
    ["update", { uid: "C-0042", fields: { status: "active" } }],
    ["unlink", { id: 4 }],
  ]);
  const failed = await pa.applyWriteOps(ops, async (kind) => {
    if (kind === "link") throw Object.assign(new Error("x"), { serverMessage: "unit not found" });
    return { unit: { uid: "C-1" } };
  });
  assert.equal(failed.ok, false);
  assert.deepEqual(failed.results.map((r) => [r.op, r.ok]), [["create", true], ["link", false]]);
  assert.equal(failed.results[1].error, "unit not found");
});

function mapBackend() {
  const map = new Map();
  return {
    map,
    list: async (prefix) => [...map].filter(([k]) => k.startsWith(prefix)).map(([key, value]) => ({ key, value })),
    get: async (key) => map.get(key),
    put: async (key, value) => { map.set(key, value); },
    remove: async (key) => { map.delete(key); },
  };
}

test("D-0003 storage: per user, plugin and project; about 1 MB per plugin", async () => {
  const backend = mapBackend();
  const store = pa.createPluginStorage(backend);
  assert.equal(pa.STORAGE_CAP_BYTES, 1024 * 1024);
  const a = store.scope("ana", "demo", "p1");
  await a.set("prefs", JSON.stringify({ dark: true }));
  assert.deepEqual(await a.get("prefs"), { dark: true });
  assert.equal(await a.get("missing"), null);
  // Isolation: another plugin, project or user sees nothing.
  assert.equal(await store.scope("ana", "other", "p1").get("prefs"), null);
  assert.equal(await store.scope("ana", "demo", "p2").get("prefs"), null);
  assert.equal(await store.scope("bob", "demo", "p1").get("prefs"), null);
  // The cap counts keys and values (UTF-8); replacing a key does not double count.
  const half = JSON.stringify("x".repeat(600 * 1024));
  await a.set("big", half);
  await a.set("big", half);
  await assert.rejects(a.set("big2", half), (err) => err.code === "quota_exceeded");
  assert.deepEqual(await a.keys(), ["big", "prefs"]);
  const usage = await a.usage();
  assert.ok(usage > 600 * 1024 && usage <= pa.STORAGE_CAP_BYTES);
  const cjk = JSON.stringify("字".repeat(150 * 1024)); // 150 K characters, 450 KB in UTF-8
  await assert.rejects(a.set("cjk", cjk), (err) => err.code === "quota_exceeded", "bytes, not characters");
  // Another project has its own megabyte.
  await store.scope("ana", "demo", "p2").set("big", half);
  await a.remove("big");
  assert.deepEqual(await a.keys(), ["prefs"]);
  await a.clear();
  assert.deepEqual(await a.keys(), []);
  assert.notEqual(await store.scope("ana", "demo", "p2").get("big"), null);
  // Removing the plugin forgets every project's data of that user only.
  await store.scope("bob", "demo", "p1").set("k", "1");
  await store.forget("ana", "demo");
  assert.deepEqual([...backend.map.keys()].map((k) => k.split("\u0000")[0]), ["bob"]);
});

test("D-0003 events: subscriptions, selection and project at once, graph debounced", () => {
  const delivered = [];
  let timer = null;
  const hub = pa.createEventHub({
    deliver: (key, event, data) => delivered.push([key, event, data]),
    debounceMs: 500, setTimer: (fn) => { timer = fn; return 1; }, clearTimer: () => { timer = null; },
  });
  hub.emit("selection", {});
  assert.deepEqual(delivered, [], "nobody subscribed");
  assert.deepEqual(hub.subscribe("u-a", ["selection", "graph"]), ["selection", "graph"]);
  hub.subscribe("u-b", ["project"]);
  hub.emit("selection", {});
  hub.emit("project", {});
  assert.deepEqual(delivered, [["u-a", "selection", {}], ["u-b", "project", {}]]);
  delivered.length = 0;
  // Graph changes are merged until the timer fires.
  hub.emit("graph", { uids: ["S-0001"] });
  hub.emit("graph", { uids: ["S-0002", "S-0001"] });
  assert.deepEqual(delivered, []);
  timer();
  assert.deepEqual(delivered, [["u-a", "graph", { uids: ["S-0001", "S-0002"] }]]);
  delivered.length = 0;
  hub.emit("graph", { uids: ["S-0003"] });
  hub.emit("graph", { uids: null });
  hub.flush();
  assert.deepEqual(delivered, [["u-a", "graph", { uids: null }]], "a reset means everything");
  // Unsubscribe and drop.
  assert.deepEqual(hub.unsubscribe("u-a", ["graph"]), ["selection"]);
  assert.equal(hub.subscribed("u-a", "graph"), false);
  hub.drop("u-a");
  delivered.length = 0;
  timer = null;
  hub.emit("selection", {});
  hub.emit("graph", { uids: ["x"] });
  assert.equal(timer, null, "no graph subscriber: nothing scheduled");
  assert.deepEqual(delivered, []);
});

test("D-0003 rate limits: 20 calls/s per plugin, stricter AI, writes, clipboard and links", () => {
  let now = 0;
  const allow = pa.createRateLimits(pa.RATE_LIMITS, () => now);
  const run = (bucket, n) => Array.from({ length: n }, () => allow(bucket)).filter(Boolean).length;
  assert.equal(run("calls", 25), 20);
  now = 1000;
  assert.equal(run("calls", 5), 5, "a new second");
  assert.equal(run("ai", 5), 3, "3 AI calls per 10 s");
  now += 10_000;
  assert.equal(run("ai", 5), 3);
  // At most 20 AI calls in any minute, whatever the pace.
  const limits = pa.createRateLimits({ ai: [{ limit: 20, windowMs: 60_000 }] }, () => now);
  assert.equal(Array.from({ length: 30 }, () => limits("ai")).filter(Boolean).length, 20);
  assert.equal(pa.RATE_LIMITS.ai.some((w) => w.limit === 20 && w.windowMs === 60_000), true);
  now += 120_000;
  assert.equal(run("write", 10), 5);
  assert.equal(run("clipboard", 10), 3);
  assert.equal(run("link", 10), 3);
  assert.equal(run("comment", 10), 5);
  assert.equal(run("unknown-bucket", 3), 3, "buckets without rules are free");
  // Refused calls do not count: the window opens again on time.
  now += 5_000;
  assert.equal(run("clipboard", 1), 1);
  // The legacy limiter stays.
  let t2 = 0;
  const legacy = vp.createRateLimit(2, 1000, () => t2);
  assert.deepEqual([legacy(), legacy(), legacy()], [true, true, false]);
});

test("D-0003 what plugins see: node detail with media and links, palette without url()", () => {
  const unit = {
    id: 1, uid: "S-0001", title: "T", type: "story", status: "active", importance: "p0", tags: ["a"], body: "text", path: "/srv", raw_url: "/api/units/1",
    media: [{ id: 1, url: "assets/mind-graph.png", kind: "figure", caption: "Graph", is_image: true, raw_url: "/api/media/1/raw" }],
    links_out: [], links_in: [{ id: 5, relation: "part_of", note: "", created_by: "system", source: { id: 2, uid: "S-0002", title: "M", type: "story", status: "open" } }],
    activity: [{ actor: "agent:x" }], comments: [{ body: "secret" }],
  };
  const detail = pa.publicNodeDetail(unit, vp.publicNode);
  assert.deepEqual(detail.media, [{ id: 1, kind: "figure", caption: "Graph", image: true, name: "mind-graph.png" }]);
  assert.deepEqual(detail.links.in, [{ id: 5, relation: "part_of", note: "", uid: "S-0002", title: "M", type: "story", status: "open" }]);
  assert.equal(detail.body, "text");
  for (const key of ["path", "raw_url", "activity", "comments", "id"]) assert.equal(key in detail, false, key);
  assert.equal(JSON.stringify(detail).includes("/api/"), false, "no ARK URLs");
  const theme = pa.publicTheme("dark", (name) => ({ "--bg": " #0f141c ", "--ink": "url(https://evil/x)", "--font": "system-ui,\"Segoe UI\"", "--line": "red;}body{" })[name] || "");
  assert.deepEqual(theme, { mode: "dark", vars: { "--bg": "#0f141c", "--font": "system-ui,\"Segoe UI\"" } });
});

test("D-0003 the assistant example imports with the permissions it needs; the SDK only calls bridge methods", async () => {
  const text = await readFile(new URL("../../ark_studio/web/plugins-examples/assistant-plugin.html", import.meta.url), "utf8");
  const bundle = pb.parseTextBundle(text, "assistant-plugin.html");
  assert.equal(bundle.manifest.id, "assistant-example");
  assert.deepEqual(bundle.manifest.permissions, ["selection:read", "ai:chat", "node:write", "node:open", "notify"]);
  assert.match(bundle.html, /api\.chat\(/);
  assert.match(bundle.html, /api\.writeNodes\(/);
  const frame = await readFile(new URL("../../ark_studio/web/plugin-frame.html", import.meta.url), "utf8");
  const called = new Set([...frame.matchAll(/call\("([a-z.]+)"/g)].map((m) => m[1]));
  for (const method of called) assert.ok(Object.hasOwn(vp.BRIDGE_METHODS, method), `SDK calls unknown ${method}`);
  assert.deepEqual([...called].sort(), Object.keys(vp.BRIDGE_METHODS).sort(), "the SDK covers every method");
  assert.match(frame, /ark-plugin-event/);
});
