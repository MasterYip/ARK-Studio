import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

const source = await readFile(new URL("../../ark_studio/web/js/inspector-width.js", import.meta.url), "utf8");
const module = new vm.SourceTextModule(source, { context: vm.createContext({}) });
await module.link(() => {}); await module.evaluate();
const { clampInspectorWidth, dragInspectorWidth, INSPECTOR_MIN_W, INSPECTOR_MAX_W, INSPECTOR_DEFAULT_W, INSPECTOR_WIDTH_KEY } = module.namespace;

test("inspector width is clamped and falls back to the default", () => {
  assert.equal(INSPECTOR_WIDTH_KEY, "ark.inspectorWidth");
  assert.equal(clampInspectorWidth(500), 500);
  assert.equal(clampInspectorWidth(10), INSPECTOR_MIN_W);
  assert.equal(clampInspectorWidth(5000), INSPECTOR_MAX_W);
  for (const bad of [null, undefined, "", "abc", 0, NaN]) assert.equal(clampInspectorWidth(bad), INSPECTOR_DEFAULT_W);
  assert.equal(clampInspectorWidth("512.6"), 513);
});

test("dragging the left edge of the right-docked pane widens it", () => {
  assert.equal(dragInspectorWidth(430, 1000, 900), 530);
  assert.equal(dragInspectorWidth(430, 1000, 1080), 350);
  assert.equal(dragInspectorWidth(430, 1000, 0), INSPECTOR_MAX_W);
  assert.equal(dragInspectorWidth(430, 1000, 2000), INSPECTOR_MIN_W);
});

test("the placeholder and the unit pane both mount the shared resize handle", async () => {
  const inspector = await readFile(new URL("../../ark_studio/web/js/inspector.js", import.meta.url), "utf8");
  const start = inspector.indexOf("export function showInspectorPlaceholder");
  const end = inspector.indexOf("\n}\n", start);
  assert.match(inspector.slice(start, end), /inspectorResizer\(ctx\)/);
  assert.equal(inspector.match(/appendChild\(inspectorResizer\(ctx\)\)/g).length, 2);
});
