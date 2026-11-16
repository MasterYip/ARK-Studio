// Run: node --experimental-vm-modules --test tests/js/mobile_keyboard.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

async function loadCore() {
  const source = await readFile(new URL("../../ark_studio/web/js/mobile-core.js", import.meta.url), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: "mobile-core.js" });
  await module.link(() => { throw new Error("mobile-core must stay dependency-free"); });
  await module.evaluate();
  return module.namespace;
}

test("keyboardOpen needs a focused field and a real height drop", async () => {
  const { keyboardOpen } = await loadCore();
  assert.equal(keyboardOpen({ fullHeight: 844, visibleHeight: 520, editableFocused: true }), true);
  assert.equal(keyboardOpen({ fullHeight: 844, visibleHeight: 520, editableFocused: false }), false);
  // Address bar collapsing / expanding is ~56 px: not a keyboard.
  assert.equal(keyboardOpen({ fullHeight: 844, visibleHeight: 788, editableFocused: true }), false);
  assert.equal(keyboardOpen({ fullHeight: 0, visibleHeight: 400, editableFocused: true }), false);
  assert.equal(keyboardOpen(), false);
});

test("keyboardTracker keeps a per-width baseline and restores on close", async () => {
  const { keyboardTracker } = await loadCore();
  const sample = keyboardTracker();
  assert.equal(sample({ width: 390, visibleHeight: 722, layoutHeight: 722, editableFocused: false }), false);
  assert.equal(sample({ width: 390, visibleHeight: 778, layoutHeight: 778, editableFocused: false }), false);
  // Old Chrome / WebView shrink the layout viewport too.
  assert.equal(sample({ width: 390, visibleHeight: 443, layoutHeight: 557, editableFocused: true }), true);
  assert.equal(sample({ width: 390, visibleHeight: 443, layoutHeight: 443, editableFocused: true }), true);
  // Keyboard dismissed with the field still focused: height is back.
  assert.equal(sample({ width: 390, visibleHeight: 722, layoutHeight: 722, editableFocused: true }), false);
  // Rotation: a new width gets its own baseline (landscape is short, no keyboard).
  assert.equal(sample({ width: 844, visibleHeight: 340, layoutHeight: 340, editableFocused: true }), false);
  assert.equal(sample({ width: 844, visibleHeight: 150, layoutHeight: 150, editableFocused: true }), true);
});

test("isEditableTarget accepts text fields only", async () => {
  const { isEditableTarget } = await loadCore();
  assert.equal(isEditableTarget({ tagName: "INPUT", type: "text" }), true);
  assert.equal(isEditableTarget({ tagName: "INPUT", type: "search" }), true);
  assert.equal(isEditableTarget({ tagName: "INPUT" }), true);
  assert.equal(isEditableTarget({ tagName: "INPUT", type: "checkbox" }), false);
  assert.equal(isEditableTarget({ tagName: "INPUT", type: "text", readOnly: true }), false);
  assert.equal(isEditableTarget({ tagName: "TEXTAREA" }), true);
  assert.equal(isEditableTarget({ tagName: "TEXTAREA", disabled: true }), false);
  assert.equal(isEditableTarget({ tagName: "DIV", isContentEditable: true }), true);
  assert.equal(isEditableTarget({ tagName: "BUTTON" }), false);
  assert.equal(isEditableTarget(null), false);
});
