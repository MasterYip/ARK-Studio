import assert from "node:assert/strict";
import test from "node:test";

import { FOLD_PREFIX, foldIsOpen, rememberFold } from "../../ark_studio/web/js/fold-section.js";

function memoryStorage() {
  const data = new Map();
  return {
    data,
    getItem: (key) => (data.has(key) ? data.get(key) : null),
    setItem: (key, value) => data.set(key, String(value)),
  };
}

test("folded sections start collapsed unless told otherwise", () => {
  const storage = memoryStorage();
  assert.equal(foldIsOpen("gpu-queue", { storage }), false);
  assert.equal(foldIsOpen("gpu-queue", { storage, fallback: true }), true);
});

test("open/closed state is remembered per section", () => {
  const storage = memoryStorage();
  rememberFold("gpu-queue", true, { storage });
  rememberFold("blackbox-history", false, { storage });
  assert.equal(storage.data.get(`${FOLD_PREFIX}gpu-queue`), "1");
  assert.equal(foldIsOpen("gpu-queue", { storage }), true);
  assert.equal(foldIsOpen("blackbox-history", { storage, fallback: true }), false);
  assert.equal(foldIsOpen("mobile-pairs", { storage }), false);
  rememberFold("gpu-queue", false, { storage });
  assert.equal(foldIsOpen("gpu-queue", { storage }), false);
});

test("unavailable storage falls back quietly", () => {
  const broken = { getItem() { throw new Error("denied"); }, setItem() { throw new Error("denied"); } };
  assert.equal(foldIsOpen("x", { storage: broken, fallback: true }), true);
  assert.doesNotThrow(() => rememberFold("x", true, { storage: broken }));
  assert.equal(foldIsOpen("x", { storage: null }), false);
});
