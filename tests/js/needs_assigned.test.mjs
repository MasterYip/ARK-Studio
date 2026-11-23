// Run: node --experimental-vm-modules --test tests/js/needs_assigned.test.mjs
// Needs you › Assigned to me (needs-model.js, server: store/assignments.go).
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

async function loadModel() {
  const source = await readFile(new URL("../../ark_studio/web/js/needs-model.js", import.meta.url), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: "needs-model.js" });
  await module.link(() => { throw new Error("needs-model must stay dependency-free"); });
  await module.evaluate();
  return module.namespace;
}

const m = await loadModel();
const plain = (v) => JSON.parse(JSON.stringify(v));

const assigned = [
  { batch: "b_1", by: "ana", by_name: "Ana", new: true, units: [
    { uid: "T-1", title: "One", key: "a:T-1", new: true },
    { uid: "T-2", title: "Two", key: "a:T-2", new: false },
    { uid: "T-3", title: "Three", key: "a:T-3", new: false },
  ] },
  { batch: "a_2", by: "bo", new: false, units: [{ uid: "T-9", title: "Nine", key: "a:T-9", new: false }] },
];

test("a unit already in Needs you for review is shown there only", () => {
  const entries = m.assignedEntries(assigned, [{ uid: "T-2", kind: "review" }, { uid: "T-9", kind: "decision" }]);
  assert.deepEqual(entries.map((e) => e.batch), ["b_1"]);
  assert.deepEqual(entries[0].units.map((u) => u.uid), ["T-1", "T-3"]);
  assert.equal(m.assignedCount(entries), 2);
});

test("an entry is new while one of its units is unread", () => {
  const [first] = m.assignedEntries(assigned, [{ uid: "T-1" }]);
  assert.equal(first.new, false);
  const read = m.markAssignedRead(m.assignedEntries(assigned, []), "T-1");
  assert.equal(read[0].new, false);
  assert.equal(read[0].units[0].new, false);
  assert.deepEqual(plain(m.markAssignedRead(read, "T-404")), plain(read));
});

test("clear sends every unit of the entry and drops it", () => {
  assert.deepEqual(m.assignedClearKeys(assigned[0]), ["a:T-1", "a:T-2", "a:T-3"]);
  assert.deepEqual(m.assignedClearKeys({ units: [{ uid: "T-5" }] }), ["a:T-5"]);
  assert.deepEqual(m.dropAssigned(assigned, "b_1").map((e) => e.batch), ["a_2"]);
});

test("opening an assigned unit sends its seen key", () => {
  assert.equal(m.needsUnitKey({ uid: "T-9", status: "active", type: "task" }, assigned), "u:T-9");
  assert.equal(m.needsUnitKey({ uid: "T-8", status: "active", type: "task" }, assigned), "");
  assert.equal(m.needsUnitKey({ uid: "T-8", status: "review", type: "task" }), "u:T-8");
  assert.equal(m.isAssigned(assigned, "T-2"), true);
  assert.equal(m.isAssigned([], "T-2"), false);
});

test("empty or missing lists", () => {
  assert.deepEqual(m.assignedEntries(undefined, undefined), []);
  assert.deepEqual(m.assignedEntries([{ batch: "x", units: [] }], []), []);
  assert.equal(m.assignedCount([]), 0);
});
