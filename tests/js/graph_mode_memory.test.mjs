// Run: node --test tests/js/graph_mode_memory.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";

import {
  LEGACY_MODE_KEY, MODE_KEY, inspectorReadOnly, rememberMode, savedMode,
} from "../../ark_studio/web/js/read-edit-mode.js";

const web = new URL("../../ark_studio/web/js/", import.meta.url);

function memoryStorage(entries = {}) {
  const map = new Map(Object.entries(entries));
  return {
    getItem: (key) => (map.has(key) ? map.get(key) : null),
    setItem: (key, value) => map.set(key, String(value)),
    removeItem: (key) => map.delete(key),
    has: (key) => map.has(key),
  };
}

test("no saved choice: Node details opens in Read", () => {
  const storage = memoryStorage();
  assert.equal(savedMode(storage), "read");
  assert.equal(inspectorReadOnly({ saved: savedMode(storage) }), true);
  assert.equal(inspectorReadOnly({}), true);
});

test("a saved Edit choice opens in Edit; Read is remembered too", () => {
  const storage = memoryStorage();
  rememberMode("edit", storage);
  assert.equal(storage.getItem(MODE_KEY), "edit");
  assert.equal(inspectorReadOnly({ saved: savedMode(storage) }), false);
  rememberMode("read", storage);
  assert.equal(inspectorReadOnly({ saved: savedMode(storage) }), true);
});

test("the old key (also written by edit actions) is dropped: Read once", () => {
  const storage = memoryStorage({ [LEGACY_MODE_KEY]: "edit" });
  assert.equal(savedMode(storage), "read");
  assert.equal(storage.has(LEGACY_MODE_KEY), false);
});

test("this visit's mode and an explicit request win over the saved choice", () => {
  assert.equal(inspectorReadOnly({ sessionMode: "edit", saved: "read" }), false);
  assert.equal(inspectorReadOnly({ sessionMode: "read", saved: "edit" }), true);
  assert.equal(inspectorReadOnly({ graphMode: "edit", saved: "read" }), false);
  // Needs You review and Home's recent list ask for Read.
  assert.equal(inspectorReadOnly({ readOnly: true, sessionMode: "edit" }), true);
  // The hover preview's Edit button asks for Edit.
  assert.equal(inspectorReadOnly({ readOnly: false, saved: "read" }), false);
});

test("storage errors fall back to Read", () => {
  const broken = { getItem() { throw new Error("denied"); }, removeItem() { throw new Error("denied"); } };
  assert.equal(savedMode(broken), "read");
  assert.doesNotThrow(() => rememberMode("edit", { setItem() { throw new Error("full"); } }));
});

test("edit actions that switch to Edit on their own are not remembered", async () => {
  const graph = await readFile(new URL("graph.js", web), "utf8");
  assert.doesNotMatch(graph, /ark\.graphMode/, "graph.js uses read-edit-mode.js");
  assert.doesNotMatch(graph, /this\.setMode\("edit"\);\s*\n\s*action\(\)/, "context-menu edits do not persist");
  assert.equal((graph.match(/this\.setMode\("edit", \{ persist: false \}\)/g) || []).length, 3);
  assert.match(graph, /this\.opts\.onModeChange\?\.\(this\.mode, \{ persist: remember \}\)/);
  const mind = await readFile(new URL("mind.js", web), "utf8");
  assert.match(mind, /onModeChange: \(mode, options\) => ctx\.syncInspectorMode\?\.\(mode, options\)/);
  const app = await readFile(new URL("app.js", web), "utf8");
  assert.match(app, /syncInspectorMode: async \(mode = "", \{ persist = true \} = \{\}\)/);
  assert.match(app, /if \(persist\) rememberMode\(S\.graphMode\)/);
});

test("Node details and the multi-node panel use the shared decision", async () => {
  for (const name of ["inspector.js", "inspector-multi.js"]) {
    const src = await readFile(new URL(name, web), "utf8");
    assert.match(src, /inspectorReadOnly\(\{/, name);
    assert.doesNotMatch(src, /localStorage\.getItem\("ark\.graphMode"\)/, name);
  }
});
