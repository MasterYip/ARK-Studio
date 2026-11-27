// Run: node --experimental-vm-modules --test tests/js/graph_viewport_model.test.mjs
// The camera decision after a filter change (graph-viewport-model.js).
import assert from "node:assert/strict";
import { test } from "node:test";
import { pointsOnScreen, viewportAction, anchorShift } from "../../ark_studio/web/js/graph-viewport-model.js";

test("points count only inside the inset viewport", () => {
  const w = 1000, h = 600; // inset: 6% of 600 = 36 px
  assert.equal(pointsOnScreen([{ x: 500, y: 300 }], w, h), 1);
  assert.equal(pointsOnScreen([{ x: 10, y: 300 }], w, h), 0, "a node cut off at the edge is not on screen");
  assert.equal(pointsOnScreen([{ x: -400, y: 300 }, { x: 500, y: 900 }], w, h), 0);
  assert.equal(pointsOnScreen([{ x: NaN, y: 1 }, null, { x: 100, y: 100 }], w, h), 1);
  assert.equal(pointsOnScreen([{ x: 5, y: 5 }], w, h, 0), 1, "explicit margin");
  assert.equal(pointsOnScreen([{ x: 5, y: 5 }], 0, 0), 0, "no viewport yet");
});

test("keep while any node is on screen, fit when none, empty without nodes", () => {
  assert.equal(viewportAction({ total: 0, onScreen: 0 }), "empty");
  assert.equal(viewportAction({ total: 12, onScreen: 0 }), "fit");
  assert.equal(viewportAction({ total: 12, onScreen: 1 }), "keep");
  assert.equal(viewportAction({ total: 3, onScreen: 3 }), "keep");
});

test("the anchor is the surviving node nearest the view centre", () => {
  const before = new Map([[1, { x: 0, y: 0 }], [2, { x: 900, y: 40 }], [3, { x: 1000, y: 0 }]]);
  // Node 3 was removed; the relayout moved the rest.
  const after = new Map([[1, { x: -300, y: 10 }], [2, { x: 100, y: 50 }]]);
  assert.deepEqual(anchorShift(before, after, { x: 950, y: 0 }), { dx: 800, dy: -10 }, "node 2 keeps its place");
  assert.deepEqual(anchorShift(before, after, { x: 0, y: 0 }), { dx: 300, dy: -10 }, "node 1 keeps its place");
  assert.equal(anchorShift(before, new Map([[9, { x: 0, y: 0 }]]), { x: 0, y: 0 }), null, "no shared node");
  assert.equal(anchorShift(new Map(), after, null), null);
});
