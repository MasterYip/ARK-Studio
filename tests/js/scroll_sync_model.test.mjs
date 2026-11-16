// Run: node --experimental-vm-modules --test tests/js/scroll_sync_model.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

async function load() {
  const source = await readFile(new URL("../../web/js/scroll-sync-model.js", import.meta.url), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: "scroll-sync-model.js" });
  await module.link(() => { throw new Error("scroll-sync-model must stay dependency-free"); });
  await module.evaluate();
  return module.namespace;
}

const m = await load();
const near = (a, b) => assert.ok(Math.abs(a - b) < 1e-9, `${a} ≈ ${b}`);

test("anchors: sorted, one per line, monotonic, start at line 1 and closed by an end", () => {
  const anchors = m.normalizeAnchors([
    { line: 10, top: 500 }, { line: 3, top: 120 }, { line: 3, top: 140 }, { line: 6, top: 90 }, { line: "x", top: 1 },
  ], { line: 21, top: 900 });
  assert.deepEqual(anchors, [
    { line: 1, top: 0 }, { line: 3, top: 120 }, { line: 6, top: 120 }, { line: 10, top: 500 }, { line: 21, top: 900 },
  ]);
  assert.deepEqual(m.normalizeAnchors([], null), [{ line: 1, top: 0 }]);
  assert.deepEqual(m.normalizeAnchors([{ line: 1, top: 30 }], { line: 1, top: 99 }), [{ line: 1, top: 30 }], "an end before the last anchor is ignored");
});

test("line -> position interpolates between anchors and clamps at the ends", () => {
  const anchors = m.normalizeAnchors([{ line: 5, top: 200 }, { line: 9, top: 600 }], { line: 13, top: 1000 });
  near(m.positionForLine(anchors, 1), 0);
  near(m.positionForLine(anchors, 3), 100);
  near(m.positionForLine(anchors, 5), 200);
  near(m.positionForLine(anchors, 7), 400);
  near(m.positionForLine(anchors, 7.5), 450);
  near(m.positionForLine(anchors, 13), 1000);
  near(m.positionForLine(anchors, 99), 1000);
  near(m.positionForLine(anchors, 0), 0);
  assert.equal(m.positionForLine([], 4), 0);
});

test("position -> line is the inverse (round trip) and handles flat stretches", () => {
  const anchors = m.normalizeAnchors([{ line: 5, top: 200 }, { line: 9, top: 600 }, { line: 10, top: 600 }], { line: 13, top: 1000 });
  for (const line of [1, 2.5, 5, 6.25, 8.5, 11, 13]) near(m.lineForPosition(anchors, m.positionForLine(anchors, line)), line);
  near(m.lineForPosition(anchors, -50), 1);
  near(m.lineForPosition(anchors, 5000), 13);
  // Lines 9-10 share a top (e.g. a collapsed block): the map moves on to 10.
  near(m.lineForPosition(anchors, 600), 10);
  assert.equal(m.lineForPosition([], 10), 1);
});

test("source and preview stay aligned through their own anchors", () => {
  // Source: 20 px per line; preview: a tall image after line 4.
  const source = m.normalizeAnchors([], { line: 11, top: 200 });
  const preview = m.normalizeAnchors([{ line: 1, top: 0 }, { line: 4, top: 60 }, { line: 5, top: 460 }, { line: 8, top: 520 }], { line: 11, top: 580 });
  const sourceToPreview = (top) => m.positionForLine(preview, m.lineForPosition(source, top));
  near(sourceToPreview(60), 60);        // line 4
  near(sourceToPreview(70), 260);       // halfway through the image
  near(sourceToPreview(80), 460);       // line 5
  near(m.positionForLine(source, m.lineForPosition(preview, 260)), 70);
});

test("caret block and wrapped line tops", () => {
  assert.equal(m.blockLineFor([1, 4, 9], 6), 4);
  assert.equal(m.blockLineFor([1, 4, 9], 9), 9);
  assert.equal(m.blockLineFor([3, 4], 2), null);
  assert.deepEqual(m.lineTops([20, 40, 20], 10), { tops: [10, 30, 70], end: 90 });
});
