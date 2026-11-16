import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

const listeners = {};
const context = vm.createContext({ document: { addEventListener(name, fn) { listeners[name] = fn; } } });
const module = new vm.SourceTextModule(await readFile(new URL("../../web/js/floating-position.js", import.meta.url), "utf8"), { context });
await module.link(() => {}); await module.evaluate();
const { floatingPosition, activationPoint, trackActivationPoint } = module.namespace;

test("floating panels stay on screen and prefer the available side of every click", () => {
  for (const [vw, vh] of [[1440, 900], [1920, 1080], [390, 844], [700, 390]]) {
    const width = Math.min(430, vw - 24), height = Math.min(468, vh - 82);
    for (const x of [4, 40, vw / 2, vw - 40, vw - 4]) for (const y of [4, 100, vh / 2, vh - 4]) {
      const p = floatingPosition({ x, y }, width, height, vw, vh, 58);
      assert.ok(p.x >= 12 && p.x + width <= vw - 12);
      assert.ok(p.y >= 68 && p.y + height <= vh - 12);
      if (x + 16 + width <= vw - 12) assert.equal(p.x, x + 16);
      else if (x - width - 16 >= 12) assert.equal(p.x + width, x - 16);
    }
  }
});

test("central activation tracking covers pointer and keyboard without sharing mutable anchors", () => {
  const current = trackActivationPoint();
  assert.equal(current(), null);
  listeners.pointermove({ type: "pointermove", clientX: 322, clientY: 417 });
  assert.equal(current().x, 322); assert.equal(current().y, 417);
  const saved = current();
  listeners.click({ type: "click", detail: 1, clientX: 888, clientY: 44 });
  assert.equal(saved.x, 322); assert.equal(current().x, 888);
  const event = { type: "click", detail: 0, target: { getBoundingClientRect: () => ({ left: 100, top: 200, width: 60, height: 40 }) } };
  listeners.click(event);
  assert.equal(current().x, 130); assert.equal(current().y, 220);
  assert.equal(activationPoint(event).y, 220);
});
