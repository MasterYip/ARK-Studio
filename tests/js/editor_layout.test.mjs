// Run: node --experimental-vm-modules --test tests/js/editor_layout.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

async function loadModule(name, linker) {
  const source = await readFile(new URL(`../../ark_studio/web/js/${name}`, import.meta.url), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: name });
  await module.link(linker);
  await module.evaluate();
  return module;
}

const model = await loadModule("editor-model.js", () => { throw new Error("editor-model must stay dependency-free"); });
const layoutModule = await loadModule("editor-layout.js", (specifier) => {
  if (specifier === "./editor-model.js") return model;
  throw new Error(`editor-layout may only import editor-model (got ${specifier})`);
});
const m = layoutModule.namespace;
const ws = (path) => ({ kind: "ws", path });
const keys = (layout, id) => m.getGroup(layout, id).tabs.map((tab) => tab.key);

function twoTabs() {
  let layout = m.createLayout();
  layout = m.openInGroup(layout, "g1", ws("a.md"));
  layout = m.openInGroup(layout, "g1", ws("b.md"));
  return layout;
}

test("split right copies the active tab into a new focused group", () => {
  const layout = m.splitGroup(twoTabs(), "g1", "right", { key: "ws:b.md" });
  assert.equal(layout.groups.length, 2);
  assert.equal(layout.focused, "g2");
  assert.deepEqual(keys(layout, "g1"), ["ws:a.md", "ws:b.md"]);
  assert.deepEqual(keys(layout, "g2"), ["ws:b.md"]);
  assert.equal(layout.tree.type, "split");
  assert.equal(layout.tree.dir, "row");
  assert.deepEqual(layout.tree.sizes, [0.5, 0.5]);
  assert.deepEqual(m.groupOrder(layout), ["g1", "g2"]);
});

test("split down nests a column; same-direction splits share the parent", () => {
  let layout = m.splitGroup(twoTabs(), "g1", "right", { key: "ws:a.md" });
  const right = layout.focused;
  layout = m.splitGroup(layout, right, "down", { key: "ws:a.md" });
  const below = layout.focused;
  assert.equal(layout.tree.dir, "row");
  assert.equal(layout.tree.children[1].dir, "column");
  assert.deepEqual(m.groupOrder(layout), ["g1", right, below]);
  layout = m.splitGroup(layout, "g1", "left");
  assert.equal(layout.tree.children.length, 3, "left split joins the row");
  assert.equal(m.groupOrder(layout)[0], layout.focused);
  assert.equal(new Set(m.groupOrder(layout)).size, 4, "unique group ids");
  // Never more than MAX_GROUPS.
  assert.equal(m.splitGroup(layout, "g1", "right"), layout);
});

test("moving a tab between groups; an emptied group collapses", () => {
  let layout = m.splitGroup(twoTabs(), "g1", "right", { key: "ws:a.md", move: true });
  assert.deepEqual(keys(layout, "g1"), ["ws:b.md"]);
  assert.deepEqual(keys(layout, "g2"), ["ws:a.md"]);
  layout = m.moveTabToGroup(layout, "g2", "ws:a.md", "g1", 0);
  assert.equal(layout.groups.length, 1, "g2 emptied and collapsed");
  assert.equal(layout.tree.type, "group");
  assert.deepEqual(keys(layout, "g1"), ["ws:a.md", "ws:b.md"]);
  // Copy keeps the source; reorder within a group.
  layout = m.splitGroup(layout, "g1", "down");
  layout = m.moveTabToGroup(layout, "g1", "ws:b.md", layout.focused, null, { copy: true });
  assert.deepEqual(keys(layout, "g1"), ["ws:a.md", "ws:b.md"]);
  layout = m.moveTabToGroup(layout, "g1", "ws:b.md", "g1", 0);
  assert.deepEqual(keys(layout, "g1"), ["ws:b.md", "ws:a.md"]);
  // Splitting a group with its only tab beside itself is a no-op.
  const single = m.openInGroup(m.createLayout(), "g1", ws("x.md"));
  assert.equal(m.splitGroup(single, "g1", "right", { key: "ws:x.md", move: true }), single);
});

test("close-collapse keeps focus on a neighbour and the last group stays", () => {
  let layout = m.splitGroup(twoTabs(), "g1", "right", { key: "ws:a.md" });
  layout = m.closeInGroup(layout, "g2", ["ws:a.md"]);
  assert.equal(layout.groups.length, 1);
  assert.equal(layout.focused, "g1");
  layout = m.closeInGroup(layout, "g1", ["ws:a.md", "ws:b.md"]);
  assert.equal(layout.groups.length, 1, "the last group never collapses");
  assert.deepEqual(keys(layout, "g1"), []);
});

test("one document in two groups shares dirty state and titles", () => {
  let layout = m.splitGroup(twoTabs(), "g1", "right", { key: "ws:b.md" });
  layout = m.setDirtyEverywhere(layout, "ws:b.md", true);
  assert.equal(model.namespace.findTab(m.getGroup(layout, "g1"), "ws:b.md").dirty, true);
  assert.equal(model.namespace.findTab(m.getGroup(layout, "g2"), "ws:b.md").dirty, true);
  assert.equal(m.isDirtyAnywhere(layout, "ws:b.md"), true);
  assert.deepEqual(m.groupsWithKey(layout, "ws:b.md"), ["g1", "g2"]);
  // Opening a dirty document in a third group inherits the flag.
  layout = m.splitGroup(layout, "g2", "down");
  layout = m.openInGroup(layout, layout.focused, ws("b.md"));
  assert.equal(model.namespace.findTab(m.getGroup(layout), "ws:b.md").dirty, true);
  layout = m.setDirtyEverywhere(layout, "ws:b.md", false);
  assert.equal(m.hasDirty(layout), false);
  layout = m.renameEverywhere(layout, "ws:b.md", "B");
  assert.ok(layout.groups.every((group) => !model.namespace.findTab(group, "ws:b.md") || model.namespace.findTab(group, "ws:b.md").title === "B"));
  // Untouched groups keep identity (lets the view repaint only changes).
  const before = m.getGroup(layout, "g1");
  assert.equal(m.getGroup(m.focusGroup(layout, "g2"), "g1"), before);
});

test("resize, equalize, join and neighbours", () => {
  let layout = m.splitGroup(twoTabs(), "g1", "right", { key: "ws:a.md" });
  const splitId = layout.tree.id;
  layout = m.resizeSplit(layout, splitId, [3, 1]);
  assert.deepEqual(layout.tree.sizes, [0.75, 0.25]);
  layout = m.equalizeSplit(layout, splitId);
  assert.deepEqual(layout.tree.sizes, [0.5, 0.5]);
  assert.equal(m.neighbourGroup(layout, "g1", 1), "g2");
  assert.equal(m.neighbourGroup(layout, "g1", -1), "");
  layout = m.focusGroup(layout, "g1");
  const joined = m.joinGroups(layout);
  assert.equal(joined.groups.length, 1);
  assert.deepEqual(keys(joined, "g1"), ["ws:a.md", "ws:b.md"]);
});

test("persistence round-trip and migration from a single tab state", () => {
  let layout = m.splitGroup(twoTabs(), "g1", "down", { key: "ws:a.md" });
  layout = m.setDirtyEverywhere(layout, "ws:a.md", true);
  const { layout: restored, recent } = m.parseStoredLayout(m.serializeLayout(layout, [{ key: "ws:r.md", kind: "ws", path: "r.md" }]));
  assert.deepEqual(m.groupOrder(restored), m.groupOrder(layout));
  assert.equal(restored.tree.dir, "column");
  assert.equal(restored.focused, layout.focused);
  assert.equal(m.hasDirty(restored), false, "unsaved state is never restored");
  assert.equal(recent[0].key, "ws:r.md");
  // A v1 store (one tab state) migrates to a single group.
  const v1 = model.namespace.serializeState(model.namespace.openTab(model.namespace.createState(), ws("old.md")));
  const migrated = m.parseStoredLayout(v1).layout;
  assert.equal(migrated.groups.length, 1);
  assert.deepEqual(keys(migrated, "g1"), ["ws:old.md"]);
  // A broken tree is rebuilt from the groups.
  const broken = JSON.parse(m.serializeLayout(layout));
  broken.tree = { type: "split", dir: "row", children: [{ type: "group", id: "g1" }], sizes: [1] };
  const repaired = m.parseStoredLayout(JSON.stringify(broken)).layout;
  assert.deepEqual(m.groupOrder(repaired), m.groupOrder(layout));
  assert.equal(m.parseStoredLayout("garbage").layout.groups.length, 1);
});
