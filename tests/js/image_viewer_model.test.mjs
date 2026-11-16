// Run: node --experimental-vm-modules --test tests/js/image_viewer_model.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

async function loadModel() {
  const source = await readFile(new URL("../../ark_studio/web/js/image-viewer-model.js", import.meta.url), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: "image-viewer-model.js" });
  await module.link(() => { throw new Error("image-viewer-model must stay dependency-free"); });
  await module.evaluate();
  return module.namespace;
}

const m = await loadModel();
const near = (a, b, eps = 1e-9) => assert.ok(Math.abs(a - b) < eps, `${a} != ${b}`);

test("clampScale keeps zoom within 0.1x-20x", () => {
  assert.equal(m.clampScale(0.01), 0.1);
  assert.equal(m.clampScale(50), 20);
  assert.equal(m.clampScale(3), 3);
  assert.equal(m.clampScale(Number.NaN), 1);
});

test("fitScale fits large images and never upscales small ones by default", () => {
  near(m.fitScale(2000, 1000, 1000, 1000), 0.5);
  near(m.fitScale(1000, 2000, 1000, 1000), 0.5);
  assert.equal(m.fitScale(100, 100, 1000, 1000), 1);
  near(m.fitScale(100, 100, 1000, 500, { upscale: true }), 5);
  near(m.fitScale(1000, 1000, 1020, 1020, { padding: 10 }), 1);
  assert.equal(m.fitScale(0, 0, 500, 500), 1);
  assert.equal(m.fitScale(1e6, 1e6, 100, 100), 0.1);
});

test("fitView centres the image", () => {
  const view = m.fitView(2000, 1000, 1000, 1000);
  near(view.scale, 0.5);
  near(view.x, 0);
  near(view.y, 250);
});

test("zoomAt keeps the image point under the cursor fixed", () => {
  const view = { scale: 1, x: 100, y: 50 };
  const cursor = { x: 400, y: 300 };
  const imagePoint = { x: (cursor.x - view.x) / view.scale, y: (cursor.y - view.y) / view.scale };
  const next = m.zoomAt(view, 2.5, cursor.x, cursor.y);
  near(next.scale, 2.5);
  near(next.x + imagePoint.x * next.scale, cursor.x);
  near(next.y + imagePoint.y * next.scale, cursor.y);
  const clamped = m.zoomAt(view, 100, cursor.x, cursor.y);
  assert.equal(clamped.scale, 20);
  near(clamped.x + imagePoint.x * 20, cursor.x);
});

test("wheelFactor zooms in on negative delta, out on positive, and is clamped", () => {
  assert.ok(m.wheelFactor(-100) > 1);
  assert.ok(m.wheelFactor(100) < 1);
  near(m.wheelFactor(100) * m.wheelFactor(-100), 1);
  assert.equal(m.wheelFactor(-100000), 2);
  assert.equal(m.wheelFactor(100000), 0.5);
  assert.ok(m.wheelFactor(3, 1) < m.wheelFactor(3, 0));
  assert.equal(m.wheelFactor(0), 1);
});

test("stepScale multiplies by STEP within limits", () => {
  near(m.stepScale(1, 1), 1.25);
  near(m.stepScale(1, -1), 0.8);
  assert.equal(m.stepScale(19, 1), 20);
});

test("panBy and clampPan keep part of the image visible", () => {
  assert.deepEqual(m.panBy({ scale: 2, x: 1, y: 2 }, 10, -5), { scale: 2, x: 11, y: -3 });
  const far = m.clampPan({ scale: 1, x: 5000, y: -5000 }, 400, 300, 800, 600, 48);
  assert.equal(far.x, 800 - 48);
  assert.equal(far.y, 48 - 300);
  const ok = m.clampPan({ scale: 1, x: 10, y: 20 }, 400, 300, 800, 600);
  assert.deepEqual(ok, { scale: 1, x: 10, y: 20 });
  const tiny = m.clampPan({ scale: 1, x: -100, y: 0 }, 10, 10, 800, 600, 48);
  assert.equal(tiny.x, 0);
});

test("pinchView scales around the midpoint and follows it", () => {
  const start = { scale: 1, x: 0, y: 0 };
  const same = m.pinchView(start, 100, 100, { x: 50, y: 50 }, { x: 50, y: 50 });
  assert.deepEqual(same, start);
  const twice = m.pinchView(start, 100, 200, { x: 50, y: 50 }, { x: 60, y: 50 });
  near(twice.scale, 2);
  near(twice.x, -50 + 10);
  near(twice.y, -50);
  near(m.distance({ x: 0, y: 0 }, { x: 3, y: 4 }), 5);
  assert.deepEqual(m.midpoint({ x: 0, y: 0 }, { x: 4, y: 2 }), { x: 2, y: 1 });
});

test("formatZoom and sameView", () => {
  assert.equal(m.formatZoom(1), "100%");
  assert.equal(m.formatZoom(1.256), "126%");
  assert.equal(m.formatZoom(0.1), "10%");
  assert.equal(m.formatZoom(0.05), "5%");
  assert.equal(m.formatZoom(0.075), "7.5%");
  assert.ok(m.sameView({ scale: 1, x: 0, y: 0 }, { scale: 1, x: 0.001, y: 0 }));
  assert.ok(!m.sameView({ scale: 1, x: 0, y: 0 }, { scale: 1, x: 2, y: 0 }));
});
