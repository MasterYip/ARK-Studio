import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

const context = vm.createContext({});
const modules = new Map();
async function load(url) {
  if (modules.has(url.href)) return modules.get(url.href);
  const module = new vm.SourceTextModule(await readFile(url, "utf8"), { context, identifier: url.href });
  modules.set(url.href, module);
  await module.link((specifier, parent) => load(new URL(specifier, parent.identifier)));
  return module;
}
const module = await load(new URL("../../web/js/graph-force.js", import.meta.url));
await module.evaluate();
const { applyRepulsion } = module.namespace;
const copy = (nodes) => nodes.map((n) => ({ ...n, vx: 0, vy: 0 }));

test("small graphs preserve the inverse-square force and equal opposite reaction", () => {
  const nodes = copy([{ x: 0, y: 0 }, { x: 3, y: 4 }]);
  applyRepulsion(nodes, 640);
  assert.ok(Math.abs(nodes[0].vx + 6) < 1e-12);
  assert.ok(Math.abs(nodes[0].vy + 8) < 1e-12);
  assert.equal(nodes[0].vx, -nodes[1].vx);
  assert.equal(nodes[0].vy, -nodes[1].vy);
});

test("large-graph approximation stays close to exact forces without net drift", () => {
  const positions = Array.from({ length: 1200 }, (_, i) => ({
    x: Math.sin(i * 2.399963) * (30 + Math.sqrt(i + 1) * 17),
    y: Math.cos(i * 2.399963) * (30 + Math.sqrt(i + 1) * 13),
  }));
  const exact = copy(positions), approximate = copy(positions);
  applyRepulsion(exact, 2400, { exactThreshold: Infinity });
  applyRepulsion(approximate, 2400);
  let error = 0, magnitude = 0, sumX = 0, sumY = 0;
  for (let i = 0; i < exact.length; i++) {
    error += (exact[i].vx - approximate[i].vx) ** 2 + (exact[i].vy - approximate[i].vy) ** 2;
    magnitude += exact[i].vx ** 2 + exact[i].vy ** 2;
    sumX += approximate[i].vx; sumY += approximate[i].vy;
  }
  const relativeError = Math.sqrt(error / magnitude);
  assert.ok(relativeError < 0.03, `relative force error ${relativeError}`);
  assert.ok(Math.abs(sumX) < 1e-8 && Math.abs(sumY) < 1e-8);
});

test("coincident nodes remain finite and conserve the graph origin", () => {
  const nodes = copy(Array.from({ length: 450 }, () => ({ x: 0, y: 0 })));
  applyRepulsion(nodes, 2400);
  assert.ok(nodes.every((n) => Number.isFinite(n.vx) && Number.isFinite(n.vy)));
  assert.ok(Math.abs(nodes.reduce((sum, n) => sum + n.vx, 0)) < 1e-7);
  assert.ok(Math.abs(nodes.reduce((sum, n) => sum + n.vy, 0)) < 1e-7);
});
