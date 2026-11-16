// Run: node --experimental-vm-modules --test tests/js/assign_model.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

const source = await readFile(new URL("../../ark_studio/web/js/assign-model.js", import.meta.url), "utf8");
const module = new vm.SourceTextModule(source, { identifier: "assign-model.js" });
await module.link(() => { throw new Error("assign-model must stay dependency-free"); });
await module.evaluate();
const m = module.namespace;

test("assignChoice marks the shared own value", () => {
  assert.deepEqual({ ...m.assignChoice([{ assignee: "Alice", assignee_effective: "Alice" }]) },
    { known: true, own: "Alice", effective: "Alice", inheritedFrom: "" });
  const inherited = m.assignChoice([{ assignee: null, assignee_effective: "bob", assignee_from: { uid: "T-1", title: "Root" } }]);
  assert.equal(inherited.known, true);
  assert.equal(inherited.own, "");
  assert.equal(inherited.inheritedFrom, "Root");
  assert.equal(m.assignChoice([{ assignee: "a" }, { assignee: "A" }]).known, true);
  assert.equal(m.assignChoice([{ assignee: "a" }, { assignee: "b" }]).known, false);
  assert.equal(m.assignChoice([{ uid: "T-1" }]).known, false); // row without loaded data: no check mark
  assert.equal(m.assignChoice([{ assignee: null }, { assignee: null, assignee_effective: "x" }]).inheritedFrom, "");
});

test("assignNames: members only, me first when I am a member, former owners apart", () => {
  const users = [{ username: "owner" }, { username: "Alice" }];
  const both = m.assignNames(users, "alice", true, { own: "", effective: "zed" });
  assert.deepEqual([...both.members], ["alice", "owner"]);
  assert.deepEqual([...both.former], ["zed"]);
  const outsider = m.assignNames(users, "bob", false, {});
  assert.deepEqual([...outsider.members], ["owner", "Alice"]); // no "Me" for a non-member
  assert.deepEqual([...m.assignNames(users, "bob", true, {}).members], ["owner", "Alice"]); // me must be listed
  assert.equal(m.sameName("Alice", "alice"), true);
});
