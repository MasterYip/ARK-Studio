// Run: node --experimental-vm-modules --test tests/js/editor_model.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

async function loadModel() {
  const source = await readFile(new URL("../../ark_studio/web/js/editor-model.js", import.meta.url), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: "editor-model.js" });
  await module.link(() => { throw new Error("editor-model must stay dependency-free"); });
  await module.evaluate();
  return module.namespace;
}

const m = await loadModel();
const ws = (path) => ({ kind: "ws", path });
const keys = (state) => state.tabs.map((tab) => tab.key);

test("diff tabs: key, parse, viewer and title", () => {
  const key = m.diffKey("./notes\\a b.md", "HEAD~1");
  assert.equal(key, "diff:HEAD~1/notes/a b.md");
  assert.deepEqual(m.parseKey(key), { kind: "diff", path: "notes/a b.md", base: "HEAD~1" });
  assert.deepEqual(m.parseKey(m.diffKey("x.py", "origin/main")), { kind: "diff", path: "x.py", base: "origin/main" });
  assert.equal(m.parseKey("diff:HEAD/"), null);
  assert.equal(m.parseKey("diff:/x.py"), null);
  const tab = m.makeTab({ kind: "diff", path: "src/app.js", base: "HEAD" });
  assert.equal(tab.key, "diff:HEAD/src/app.js");
  assert.equal(tab.title, "app.js (HEAD)");
  assert.equal(tab.base, "HEAD");
  assert.equal(m.viewerFor(tab), "diff");
  assert.equal(m.isEditable(tab), false);
  assert.equal(m.keyFromRoute(encodeURIComponent(tab.key)), tab.key);
});

test("rev tabs (Timeline): a commit's version, text viewers only, read-only", () => {
  const key = m.revKey("./docs\\guide.md", "ABCDEF1234");
  assert.equal(key, "rev:abcdef1234/docs/guide.md");
  assert.deepEqual(m.parseKey(key), { kind: "rev", path: "docs/guide.md", rev: "abcdef1234" });
  assert.equal(m.parseKey("rev:HEAD/x.md"), null, "only commit hashes");
  assert.equal(m.parseKey("rev:abc/x.md"), null, "at least 4 hex digits");
  assert.equal(m.parseKey("rev:abcdef12/"), null);
  const tab = m.makeTab({ kind: "rev", path: "docs/guide.md", rev: "0123456789abcdef" });
  assert.equal(tab.title, "guide.md (0123456)");
  assert.equal(tab.rev, "0123456789abcdef");
  assert.equal(m.viewerFor(tab), "markdown");
  assert.equal(m.isEditable(tab), false);
  assert.equal(m.viewerFor(m.makeTab({ kind: "rev", path: "a/pic.png", rev: "abcd" })), "unsupported");
  assert.equal(m.viewerFor(m.makeTab({ kind: "rev", path: "main.go", rev: "abcd" })), "code");
  assert.equal(m.keyFromRoute(encodeURIComponent(tab.key)), tab.key);
});

test("keys and routes round-trip", () => {
  assert.equal(m.workspaceKey("./src\\app.js"), "ws:src/app.js");
  assert.equal(m.mediaKey("12"), "media:12");
  assert.deepEqual(m.parseKey("media:7"), { kind: "media", mediaId: 7 });
  assert.equal(m.parseKey("media:x"), null);
  assert.equal(m.parseKey("bogus"), null);
  const key = m.workspaceKey("docs/a b#c.md");
  const route = m.routeForKey(key);
  assert.equal(route, "#/editor/ws%3Adocs%2Fa%20b%23c.md");
  assert.equal(m.keyFromRoute(route.replace("#/editor/", "")), key);
  assert.equal(m.keyFromRoute("ws:docs/raw.md"), "ws:docs/raw.md");
  assert.equal(m.keyFromRoute("nonsense"), "");
  assert.equal(m.routeForKey(""), "#/editor");
});

test("viewer mapping by type", () => {
  const cases = {
    "a.png": "image", "b.JPG": "image", "c.svg": "image", "d.webp": "image", "e.gif": "image",
    "v.mp4": "video", "v.webm": "video", "v.mov": "video", "v.m4v": "video",
    "s.mp3": "audio", "s.wav": "audio", "doc.pdf": "pdf",
    "README.md": "markdown", "data.json": "json", "t.csv": "csv", "t.tsv": "csv",
    "main.py": "code", "notes.txt": "code", "Makefile": "code", ".gitignore": "code",
    "weights.safetensors": "unsupported", "deck.pptx": "unsupported",
  };
  for (const [name, viewer] of Object.entries(cases)) {
    assert.equal(m.viewerFor({ name }), viewer, name);
  }
  assert.equal(m.viewerFor({ url: "https://example.com/paper" }), "link");
  assert.equal(m.viewerFor({ url: "https://example.com/fig.png" }), "image");
  assert.equal(m.viewerFor({ url: "zotero://select/items/1" }), "link");
  assert.equal(m.viewerFor({ url: "file:///C:/x/clip.mp4" }), "video");
  assert.equal(m.viewerFor({ name: "blob", mime: "video/webm" }), "video");
  assert.equal(m.viewerFor({ name: "mystery.xyz", kind: "ws" }), "code");
  assert.equal(m.viewerFor({ name: "mystery.xyz", kind: "media" }), "unsupported");
  assert.equal(m.languageFor("x.ts"), "typescript");
  assert.equal(m.languageFor("Dockerfile"), "bash");
});

test("only workspace text files are editable", () => {
  assert.equal(m.isEditable(m.makeTab(ws("a.py"))), true);
  assert.equal(m.isEditable(m.makeTab(ws("a.png"))), false);
  assert.equal(m.isEditable(m.makeTab({ kind: "media", mediaId: 3, name: "notes.md" }), "markdown"), false);
});

test("opening inserts after the active tab and reopening focuses", () => {
  let s = m.createState();
  s = m.openTab(s, ws("a.py"));
  s = m.openTab(s, ws("b.py"));
  s = m.activateTab(s, "ws:a.py");
  s = m.openTab(s, ws("c.py"));
  assert.deepEqual(keys(s), ["ws:a.py", "ws:c.py", "ws:b.py"]);
  s = m.openTab(s, ws("b.py"));
  assert.equal(s.active, "ws:b.py");
  assert.equal(s.tabs.length, 3);
});

test("preview tabs are replaced by the next single click and pinned by edits", () => {
  let s = m.openTab(m.createState(), ws("keep.py"));
  s = m.openTab(s, ws("p1.md"), { preview: true });
  assert.equal(m.findTab(s, "ws:p1.md").preview, true);
  s = m.openTab(s, ws("p2.md"), { preview: true });
  assert.deepEqual(keys(s), ["ws:keep.py", "ws:p2.md"]);
  assert.equal(s.active, "ws:p2.md");
  // Double-click (non-preview open) pins it.
  s = m.openTab(s, ws("p2.md"));
  assert.equal(m.findTab(s, "ws:p2.md").preview, false);
  s = m.openTab(s, ws("p3.md"), { preview: true });
  assert.deepEqual(keys(s), ["ws:keep.py", "ws:p2.md", "ws:p3.md"]);
  // Editing pins too; then the next preview opens a new tab.
  s = m.setDirty(s, "ws:p3.md", true);
  assert.equal(m.findTab(s, "ws:p3.md").preview, false);
  s = m.openTab(s, ws("p4.md"), { preview: true });
  assert.equal(s.tabs.length, 4);
  // A single click on an already pinned tab keeps it pinned.
  s = m.openTab(s, ws("keep.py"), { preview: true });
  assert.equal(m.findTab(s, "ws:keep.py").preview, false);
  s = m.pinTab(s, "ws:p4.md");
  assert.equal(m.findTab(s, "ws:p4.md").preview, false);
});

test("close actions: close, others, right, left, all, saved", () => {
  let s = m.createState();
  for (const name of ["a", "b", "c", "d"]) s = m.openTab(s, ws(`${name}.txt`));
  s = m.setDirty(s, "ws:c.txt", true);
  assert.deepEqual(m.closeTargets(s, "close", "ws:b.txt"), ["ws:b.txt"]);
  assert.deepEqual(m.closeTargets(s, "others", "ws:b.txt"), ["ws:a.txt", "ws:c.txt", "ws:d.txt"]);
  assert.deepEqual(m.closeTargets(s, "right", "ws:b.txt"), ["ws:c.txt", "ws:d.txt"]);
  assert.deepEqual(m.closeTargets(s, "left", "ws:c.txt"), ["ws:a.txt", "ws:b.txt"]);
  // Edge tabs: nothing to the left of the first / right of the last (menu item disabled).
  assert.deepEqual(m.closeTargets(s, "left", "ws:a.txt"), []);
  assert.deepEqual(m.closeTargets(s, "right", "ws:d.txt"), []);
  assert.deepEqual(m.closeTargets(s, "others", "ws:x.txt"), []);
  assert.deepEqual(m.closeTargets(m.closeTabs(s, ["ws:b.txt", "ws:c.txt", "ws:d.txt"]), "others", "ws:a.txt"), []);
  assert.deepEqual(m.closeTargets(s, "all"), keys(s));
  // Close Saved skips unsaved tabs; with only unsaved tabs it closes nothing.
  assert.deepEqual(m.closeTargets(m.closeTabs(s, ["ws:a.txt", "ws:b.txt", "ws:d.txt"]), "saved"), []);
  assert.deepEqual(m.dirtyAmong(s, m.closeTargets(s, "left", "ws:d.txt")).map((t) => t.key), ["ws:c.txt"]);
  const left = m.closeTabs(s, m.closeTargets(s, "left", "ws:c.txt"));
  assert.deepEqual(keys(left), ["ws:c.txt", "ws:d.txt"]);
  assert.deepEqual(m.closeTargets(s, "saved"), ["ws:a.txt", "ws:b.txt", "ws:d.txt"]);
  assert.deepEqual(m.dirtyAmong(s, m.closeTargets(s, "right", "ws:b.txt")).map((t) => t.key), ["ws:c.txt"]);
  const right = m.closeTabs(s, m.closeTargets(s, "right", "ws:b.txt"));
  assert.deepEqual(keys(right), ["ws:a.txt", "ws:b.txt"]);
  const all = m.closeTabs(s, m.closeTargets(s, "all"));
  assert.deepEqual(all.tabs, []);
  assert.equal(all.active, "");
});

test("closing the active tab activates the most recently used one", () => {
  let s = m.createState();
  for (const name of ["a", "b", "c"]) s = m.openTab(s, ws(`${name}.txt`));
  s = m.activateTab(s, "ws:a.txt");
  s = m.activateTab(s, "ws:c.txt");
  s = m.closeTabs(s, ["ws:c.txt"]);
  assert.equal(s.active, "ws:a.txt");
});

test("moveTab reorders", () => {
  let s = m.createState();
  for (const name of ["a", "b", "c"]) s = m.openTab(s, ws(`${name}.txt`));
  s = m.moveTab(s, "ws:c.txt", 0);
  assert.deepEqual(keys(s), ["ws:c.txt", "ws:a.txt", "ws:b.txt"]);
});

test("persistence keeps order, active and preview but never dirty flags", () => {
  let s = m.createState();
  s = m.openTab(s, ws("a.md"));
  s = m.openTab(s, { kind: "media", mediaId: 5, title: "clip.mp4", name: "clip.mp4", unit: "U-1" });
  s = m.openTab(s, ws("z.md"), { preview: true });
  s = m.activateTab(s, "media:5");
  s = m.setDirty(s, "ws:a.md", true);
  const recent = m.pushRecent([], { kind: "ws", path: "a.md" });
  const text = m.serializeState(s, recent);
  const { state, recent: restoredRecent } = m.parseStoredState(text);
  assert.deepEqual(keys(state), ["ws:a.md", "media:5", "ws:z.md"]);
  assert.equal(state.active, "media:5");
  assert.equal(m.findTab(state, "ws:z.md").preview, true);
  assert.equal(m.findTab(state, "ws:a.md").dirty, false);
  assert.equal(m.findTab(state, "media:5").unit, "U-1");
  assert.equal(restoredRecent[0].key, "ws:a.md");
  assert.equal(m.storeKey("p1"), "ark.editor.v1.p1");
});

test("stored garbage is rejected safely", () => {
  assert.deepEqual(m.parseStoredState("{not json").state, m.createState());
  const { state } = m.parseStoredState(JSON.stringify({
    tabs: [{ key: "ws:a" }, { key: "ws:a" }, { key: "evil" }, { key: "media:0" },
      { key: "ws:b", preview: true }, { key: "ws:c", preview: true }],
    active: "ws:missing",
  }));
  assert.deepEqual(keys(state), ["ws:a", "ws:b", "ws:c"]);
  assert.equal(state.tabs.filter((tab) => tab.preview).length, 1);
  assert.equal(state.active, "ws:a");
});

test("recent list is deduplicated and capped", () => {
  let recent = [];
  for (let i = 0; i < 20; i += 1) recent = m.pushRecent(recent, ws(`f${i}.txt`));
  recent = m.pushRecent(recent, ws("f5.txt"));
  assert.equal(recent.length, m.RECENT_LIMIT);
  assert.equal(recent[0].key, "ws:f5.txt");
  assert.equal(new Set(recent.map((item) => item.key)).size, recent.length);
});

test("tab limit evicts the least recently used clean tab", () => {
  let s = m.createState();
  for (let i = 0; i < m.EDITOR_TAB_LIMIT; i += 1) s = m.openTab(s, ws(`f${i}.txt`));
  s = m.setDirty(s, "ws:f0.txt", true);
  s = m.openTab(s, ws("new.txt"));
  assert.equal(s.tabs.length, m.EDITOR_TAB_LIMIT);
  assert.ok(m.findTab(s, "ws:f0.txt"), "dirty tab survives");
  assert.equal(m.findTab(s, "ws:f1.txt"), null);
});

test("csv and json helpers", () => {
  const { rows } = m.parseCsv('a,b\n"x, y","say ""hi"""\r\n1,2');
  assert.deepEqual(rows, [["a", "b"], ["x, y", 'say "hi"'], ["1", "2"]]);
  const capped = m.parseCsv("1\n2\n3\n4\n", { maxRows: 2 });
  assert.equal(capped.rows.length, 2);
  assert.equal(capped.truncated, true);
  assert.equal(m.csvDelimiter("t.tsv"), "\t");
  assert.equal(m.csvDelimiter("t.csv", "a\tb\tc"), "\t");
  assert.equal(m.prettyJson('{"a":1}').text, '{\n  "a": 1\n}');
  assert.equal(m.prettyJson("{").ok, false);
});

test("unit (node) tabs: keys, routes, viewer and editability", () => {
  assert.equal(m.unitKey("IDEA-0007"), "unit:IDEA-0007");
  assert.deepEqual(m.parseKey("unit:IDEA-0007"), { kind: "unit", uid: "IDEA-0007" });
  assert.equal(m.parseKey("unit:"), null);
  assert.equal(m.parseKey("unit:../etc"), null);
  assert.equal(m.parseKey("unit:a b"), null);
  const route = m.routeForKey("unit:TASK-0001");
  assert.equal(m.keyFromRoute(route.replace("#/editor/", "")), "unit:TASK-0001");
  const tab = m.makeTab({ kind: "unit", uid: "TASK-0001", title: "Ship it" });
  assert.equal(tab.key, "unit:TASK-0001");
  assert.equal(tab.uid, "TASK-0001");
  assert.equal(tab.title, "Ship it");
  assert.equal(m.makeTab({ key: "unit:X-1" }).title, "X-1");
  assert.equal(m.viewerFor(tab), "node");
  assert.equal(m.isEditable(tab), true);
  assert.ok(m.VIEWERS.includes("node"));
});

test("unit tabs share preview/pin/dirty/persist behaviour with files", () => {
  let s = m.openTab(m.createState(), { kind: "unit", uid: "A-1" }, { preview: true });
  s = m.openTab(s, { kind: "unit", uid: "A-2" }, { preview: true });
  assert.deepEqual(keys(s), ["unit:A-2"]);
  s = m.setDirty(s, "unit:A-2", true);
  assert.equal(m.findTab(s, "unit:A-2").preview, false);
  s = m.openTab(s, ws("x.md"));
  s = m.openTab(s, { kind: "unit", uid: "A-2" });
  assert.equal(s.active, "unit:A-2");
  assert.equal(s.tabs.length, 2);
  const restored = m.parseStoredState(m.serializeState(s)).state;
  assert.equal(restored.tabs.find((tb) => tb.key === "unit:A-2").uid, "A-2");
  assert.equal(restored.tabs.some((tb) => tb.dirty), false);
});

test("mermaid and html files get their own viewers", () => {
  assert.equal(m.viewerFor({ name: "flow.mmd" }), "mermaid");
  assert.equal(m.viewerFor({ name: "flow.mermaid" }), "mermaid");
  assert.equal(m.viewerFor({ name: "chart.html", source: "media" }), "html");
  assert.equal(m.viewerFor({ name: "index.htm" }), "html");
  assert.equal(m.isTextViewer("html"), true);
  assert.equal(m.isEditable({ kind: "ws", path: "a.html" }), true);
  assert.equal(m.isEditable({ kind: "media", mediaId: 1, name: "a.html" }), false);
});

test("reopening by bare uid keeps the existing tab title", () => {
  let s = m.openTab(m.createState(), { kind: "unit", uid: "A-1", title: "Alpha" });
  s = m.openTab(s, { kind: "unit", uid: "A-1" });
  assert.equal(m.findTab(s, "unit:A-1").title, "Alpha");
  s = m.openTab(s, { kind: "unit", uid: "A-1", title: "Alpha 2" });
  assert.equal(m.findTab(s, "unit:A-1").title, "Alpha 2");
});

test("view first: files open for reading, Edit (or unsaved changes) opens the editor", () => {
  assert.equal(m.isEditing({ canEdit: true }), false, "view mode by default");
  assert.equal(m.isEditing({ canEdit: true, chosen: true }), true);
  assert.equal(m.isEditing({ canEdit: true, dirty: true }), true, "unsaved edits stay editable");
  assert.equal(m.isEditing({ canEdit: true, viewFirst: false }), true, "setting off: edit at once");
  assert.equal(m.isEditing({ canEdit: false, chosen: true, viewFirst: false }), false, "read-only people only view");
  const store = new Map();
  const storage = { getItem: (k) => store.get(k) ?? null, setItem: (k, v) => store.set(k, v) };
  assert.equal(m.viewFirstEnabled(storage), true, "on by default");
  m.setViewFirst(false, storage);
  assert.equal(m.viewFirstEnabled(storage), false);
  m.setViewFirst(true, storage);
  assert.equal(m.viewFirstEnabled(storage), true);
});
