// Run: node --experimental-vm-modules --test tests/js/status_icons.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

const paths = [];
const context = vm.createContext({ Path2D: class { constructor(d) { this.d = d; paths.push(d); } } });
const module = new vm.SourceTextModule(
  await readFile(new URL("../../web/js/icons.js", import.meta.url), "utf8"), { context },
);
await module.link(() => {});
await module.evaluate();
const { statusIconName, statusIconInk, drawStatusIcon, hasIcon } = module.namespace;

test("all persisted ARK statuses have distinct valid icons; unknowns stay neutral", async () => {
  const tags = await readFile(new URL("../../arkstudio/tags.py", import.meta.url), "utf8");
  const block = tags.split("KNOWN_STATUSES =")[1].split("KNOWN_IMPORTANCE =")[0];
  const statuses = [...block.matchAll(/\("(\w+)",/g)].map((m) => m[1]);
  const names = statuses.map(statusIconName);
  assert.equal(names.length, 8);
  assert.equal(new Set(names).size, names.length);
  assert.ok(names.every(hasIcon));
  assert.equal(statusIconName("done"), "check");
  for (const status of [undefined, "unknown", "toString"]) assert.equal(statusIconName(status), "info");
});

test("ink remains legible on type colors and custom dark fills", () => {
  assert.equal(statusIconInk("#60a5fa"), "#111827");
  assert.equal(statusIconInk("#22d3ee"), "#111827");
  assert.equal(statusIconInk("#111827"), "#ffffff");
  assert.equal(statusIconInk("#000"), "#ffffff");
  assert.equal(statusIconInk("#fff"), "#111827");
});

test("centered geometry stays within nodes, preserves canvas state and caches paths", () => {
  for (const radius of [3.5, 7, 16, 28]) {
    const calls = [];
    const ctx = {
      save() { calls.push(["save"]); }, restore() { calls.push(["restore"]); },
      translate(...args) { calls.push(["translate", ...args]); },
      scale(...args) { calls.push(["scale", ...args]); },
      stroke(path) { calls.push(["stroke", path.d]); },
    };
    drawStatusIcon(ctx, "done", 100, 80, radius, "#60a5fa");
    assert.deepEqual(calls[0], ["save"]);
    assert.deepEqual(calls[1], ["translate", 100 - radius * 0.66, 80 - radius * 0.66]);
    assert.deepEqual(calls.at(-1), ["restore"]);
    assert.equal(ctx.lineCap, "round");
    assert.ok(radius * 0.66 * Math.SQRT2 < radius);
  }
  assert.equal(paths.length, 1);
});
