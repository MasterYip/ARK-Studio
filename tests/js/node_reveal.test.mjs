// Run: node --experimental-vm-modules --test tests/js/node_reveal.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

async function load() {
  const source = await readFile(new URL("../../ark_studio/web/js/node-reveal.js", import.meta.url), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: "node-reveal.js" });
  await module.link(() => { throw new Error("node-reveal must stay dependency-free"); });
  await module.evaluate();
  return module.namespace;
}

const m = await load();

test("a reveal is taken once per node tab", () => {
  m.setNodeReveal("S-0001", { field: "body", line: 3 });
  assert.deepEqual(m.takeNodeReveal("unit:S-0001"), { field: "body", line: 3 });
  assert.equal(m.takeNodeReveal("unit:S-0001"), null, "consumed");
  m.setNodeReveal("S-0002", { field: "title" });
  m.setNodeReveal("S-0002", null);
  assert.equal(m.takeNodeReveal("unit:S-0002"), null, "cleared by a plain open");
});

test("line ranges follow the Search line numbers (\\n, \\r\\n and \\r)", () => {
  const text = "first\nsecond line\r\nthird\rfourth";
  const at = (line) => { const r = m.lineRange(text, line); return r && text.slice(r.start, r.end); };
  assert.equal(at(1), "first");
  assert.equal(at(2), "second line");
  assert.equal(at(3), "third");
  assert.equal(at(4), "fourth");
  assert.equal(m.lineRange(text, 5), null);
  assert.deepEqual(m.lineRange("", 1), { start: 0, end: 0 });
  assert.equal(at(0), "first", "line numbers below 1 mean the first line");
  assert.equal(m.lineRange("a\n\nb", 2).start, 2, "empty lines count");
});
