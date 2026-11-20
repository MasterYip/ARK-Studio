import assert from "node:assert/strict";
import { test } from "node:test";
import { resolveAdvanced, USED_BEFORE_KEYS } from "../../ark_studio/web/js/advanced.js";

function memoryStorage(entries = {}) {
  const map = new Map(Object.entries(entries));
  return {
    getItem: (key) => (map.has(key) ? map.get(key) : null),
    setItem: (key, value) => { map.set(key, String(value)); },
    removeItem: (key) => { map.delete(key); },
    map,
  };
}

test("a brand-new browser starts with advanced features off and remembers it", () => {
  // i18n.js writes these on any first visit: they do not count as "used before".
  const storage = memoryStorage({ "ark.lang": "en", "ark.theme": "light", "ark.prefsV2": "1" });
  assert.equal(resolveAdvanced(storage), false);
  assert.equal(storage.map.get("ark.advanced"), "0");
  // Keys this visit writes afterwards do not flip the stored decision.
  storage.setItem("ark.workbench.v1", "{}");
  assert.equal(resolveAdvanced(storage), false);
});

test("a browser that used ARK before starts with advanced features on", () => {
  for (const key of ["ark.workbench.v1", "ark.workbench.presets.v1", "ark.localGraph.v1", "ark.localGraph.v2",
    "ark.cache.lastProject"]) {
    assert.ok(USED_BEFORE_KEYS.includes(key), key);
    const storage = memoryStorage({ [key]: "x" });
    assert.equal(resolveAdvanced(storage), true, key);
    assert.equal(storage.map.get("ark.advanced"), "1", key);
  }
});

test("the person's own choice sticks", () => {
  assert.equal(resolveAdvanced(memoryStorage({ "ark.advanced": "0", "ark.workbench.v1": "{}" })), false);
  assert.equal(resolveAdvanced(memoryStorage({ "ark.advanced": "1" })), true);
  const storage = memoryStorage({ "ark.workbench.v1": "{}" });
  assert.equal(resolveAdvanced(storage), true);
  storage.setItem("ark.advanced", "0"); // the switch in Settings
  assert.equal(resolveAdvanced(storage), false);
});

test("storage that refuses writes still answers", () => {
  const storage = memoryStorage({ "ark.localGraph.v2": "{}" });
  storage.setItem = () => { throw new Error("quota"); };
  assert.equal(resolveAdvanced(storage), true);
});

test("GPU and Resources are never folded by the advanced switch", async () => {
  const { readFile } = await import("node:fs/promises");
  const app = await readFile(new URL("../../ark_studio/web/js/app.js", import.meta.url), "utf8");
  const fn = app.slice(app.indexOf("function workspaceSubtabs"));
  const exec = fn.slice(fn.indexOf('workspace === "execute"'), fn.indexOf('workspace === "output"'));
  assert.match(exec, /\["gpu", "GPU"\], \["resources", t\("resources\.tab"\)\]/);
  assert.doesNotMatch(exec, /advancedEnabled\(\)/);
});
