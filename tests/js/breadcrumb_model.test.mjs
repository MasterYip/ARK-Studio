// Run: node --experimental-vm-modules --test tests/js/breadcrumb_model.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

async function loadModel() {
  const source = await readFile(new URL("../../ark_studio/web/js/breadcrumb-model.js", import.meta.url), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: "breadcrumb-model.js" });
  await module.link(() => { throw new Error("breadcrumb-model must stay dependency-free"); });
  await module.evaluate();
  return module.namespace;
}

const m = await loadModel();
const dir = (path) => ({ path, name: path.split("/").pop(), type: "dir" });
const file = (path) => ({ path, name: path.split("/").pop(), type: "file" });

test("segments: folders list themselves, the file lists its siblings", () => {
  assert.deepEqual(m.crumbSegments("assets\\img/mind-graph.png"), [
    { label: "assets", path: "assets", kind: "dir", list: "assets", highlight: "assets/img" },
    { label: "img", path: "assets/img", kind: "dir", list: "assets/img", highlight: "assets/img/mind-graph.png" },
    { label: "mind-graph.png", path: "assets/img/mind-graph.png", kind: "file", list: "assets/img", highlight: "assets/img/mind-graph.png" },
  ]);
  assert.deepEqual(m.crumbSegments("README.md"), [
    { label: "README.md", path: "README.md", kind: "file", list: "", highlight: "README.md" },
  ]);
  assert.equal(m.parentOf("a/b/c.txt"), "a/b");
  assert.equal(m.parentOf("c.txt"), "");
});

const tree = new Map([
  ["notes", { entries: [dir("notes/sub"), file("notes/a.md"), file("notes/b.txt")], truncated: true }],
  ["notes/sub", { entries: [file("notes/sub/deep.md"), file("notes/sub/x.py")] }],
]);

test("rows: inline tree, expanded folders, truncated note, pending folders", () => {
  let rows = m.dropdownRows("notes", tree, new Set());
  assert.deepEqual(rows.map((row) => `${row.kind}:${row.path}`), ["dir:notes/sub", "file:notes/a.md", "file:notes/b.txt", "truncated:notes"]);
  rows = m.dropdownRows("notes", tree, new Set(["notes/sub"]));
  assert.deepEqual(rows.map((row) => `${row.depth}:${row.path}`),
    ["0:notes/sub", "1:notes/sub/deep.md", "1:notes/sub/x.py", "0:notes/a.md", "0:notes/b.txt", "0:notes"]);
  assert.equal(m.dropdownRows("other", tree, new Set())[0].kind, "pending");
});

test("type-to-filter keeps matches and the folders leading to them", () => {
  const rows = m.dropdownRows("notes", tree, new Set(), "DEEP");
  assert.deepEqual(rows.map((row) => `${row.kind}:${row.path}`), ["dir:notes/sub", "file:notes/sub/deep.md", "truncated:notes"]);
  assert.equal(rows[0].open, true, "a folder holding a match opens");
  assert.deepEqual(m.dropdownRows("notes", tree, new Set(), "zzz").map((row) => row.kind), ["truncated"]);
});

test("keyboard selection skips non-selectable rows and clamps", () => {
  const rows = m.dropdownRows("notes", tree, new Set());
  assert.equal(m.moveSelection(rows, -1, 1), 0);
  assert.equal(m.moveSelection(rows, 0, 1), 1);
  assert.equal(m.moveSelection(rows, 2, 1), 2, "the truncated note is not selectable");
  assert.equal(m.moveSelection(rows, 0, -1), 0);
  assert.equal(m.moveSelection([], 0, 1), -1);
});

test("reveal in explorer: folders to expand and the row to highlight", () => {
  assert.deepEqual(m.revealPlan("docs/guide/intro.md"), { dirs: ["docs", "docs/guide"], target: "docs/guide/intro.md" });
  assert.deepEqual(m.revealPlan("README.md"), { dirs: [], target: "README.md" });
  assert.deepEqual(m.revealPlan("docs\\guide\\"), { dirs: ["docs"], target: "docs/guide" });
  // A breadcrumb folder: that folder opens too.
  assert.deepEqual(m.revealPlan("docs/guide", { folder: true }), { dirs: ["docs", "docs/guide"], target: "docs/guide" });
  assert.deepEqual(m.revealPlan("./a/./b.txt"), { dirs: ["a"], target: "a/b.txt" });
  assert.deepEqual(m.revealPlan(""), { dirs: [], target: "" });
});
