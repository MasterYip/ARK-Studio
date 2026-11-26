// Run: node --experimental-vm-modules --test tests/js/tag_tree_model.test.mjs
// Hierarchical tag toggles (tag-tree-model.js): the filter rail's line tags
// and Needs you's line filter.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";
import {
  applyToggle, buildTagTree, initialFolded, loadFolded, nodeState, saveFolded, subtreePaths, toggleFold, toggleTarget,
  treePaths, visibleRows,
} from "../../ark_studio/web/js/tag-tree-model.js";

// The filter rail's facet values for the "branch" namespace (exact counts).
const RAIL = [
  { path: "sandbox/infra", n: 43 }, { path: "sandbox/agent-interface", n: 34 }, { path: "hoi-motion-gen/task-spec", n: 24 },
  { path: "hoi-motion-gen/method", n: 19 }, { path: "sandbox/simulators", n: 16 }, { path: "sandbox", n: 5 },
  { path: "hoi-motion-gen/method/diffusion", n: 3 }, { path: "wbg", n: 1 },
];

const memoryStorage = () => {
  const data = new Map();
  return { getItem: (k) => (data.has(k) ? data.get(k) : null), setItem: (k, v) => data.set(k, String(v)), removeItem: (k) => data.delete(k) };
};
const find = (roots, path) => {
  for (const item of roots) {
    if (item.path === path) return item;
    const below = find(item.children, path);
    if (below) return below;
  }
  return null;
};

test("tree: parents above children, last part as label, counts include descendants", () => {
  const roots = buildTagTree(RAIL);
  assert.deepEqual(roots.map((r) => r.path), ["sandbox", "hoi-motion-gen", "wbg"], "largest total first");
  const sandbox = find(roots, "sandbox");
  assert.equal(sandbox.own, true);
  assert.equal(sandbox.n, 5);
  assert.equal(sandbox.total, 5 + 43 + 34 + 16);
  assert.deepEqual(sandbox.children.map((c) => c.label), ["infra", "agent-interface", "simulators"]);
  const hoi = find(roots, "hoi-motion-gen");
  assert.equal(hoi.own, false, "a path above tags that is no tag itself");
  assert.equal(hoi.total, 24 + 19 + 3);
  const method = find(roots, "hoi-motion-gen/method");
  assert.equal(method.depth, 1);
  assert.equal(method.total, 22);
  assert.equal(find(roots, "hoi-motion-gen/method/diffusion").depth, 2);
  assert.deepEqual(buildTagTree(RAIL, { order: "name" }).map((r) => r.path), ["hoi-motion-gen", "sandbox", "wbg"]);
  // Malformed paths are left out.
  assert.deepEqual(treePaths(buildTagTree([{ path: "" }, { path: "a//b" }, { path: "/x" }, { path: "ok" }])), ["ok"]);
});

test("tree: rolled-up counts (Needs you) and grouping-only rows", () => {
  const roots = buildTagTree([
    { path: "a", n: 3, own: false }, { path: "a/b", n: 2 }, { path: "a/c", n: 1 }, { path: "d", n: 4 },
  ], { rolledUp: true, order: "name" });
  const a = find(roots, "a");
  assert.equal(a.total, 3, "a rolled-up count is not added up again");
  assert.equal(a.own, false);
  assert.deepEqual(subtreePaths(a), ["a/b", "a/c"]);
});

test("toggle: a parent switches its subtree, a child only itself", () => {
  const roots = buildTagTree(RAIL);
  const sandbox = find(roots, "sandbox");
  let selected = [];
  const on = (path) => selected.includes(path);
  assert.equal(nodeState(sandbox, on), "off");
  let target = toggleTarget(sandbox, on);
  assert.equal(target.on, true);
  selected = applyToggle(selected, target.paths, target.on);
  assert.deepEqual(selected.sort(), ["sandbox", "sandbox/agent-interface", "sandbox/infra", "sandbox/simulators"]);
  assert.equal(nodeState(sandbox, on), "on");
  // A child off: the parent is mixed.
  const infra = find(roots, "sandbox/infra");
  target = toggleTarget(infra, on);
  assert.deepEqual(target, { paths: ["sandbox/infra"], on: false });
  selected = applyToggle(selected, target.paths, target.on);
  assert.equal(nodeState(infra, on), "off");
  assert.equal(nodeState(sandbox, on), "mixed");
  // A mixed parent switches everything on; then everything off.
  target = toggleTarget(sandbox, on);
  assert.equal(target.on, true);
  selected = applyToggle(selected, target.paths, true);
  assert.equal(nodeState(sandbox, on), "on");
  selected = applyToggle(selected, toggleTarget(sandbox, on).paths, false);
  assert.deepEqual(selected, []);
  // A grouping-only parent: mixed and on follow its children.
  const hoi = find(roots, "hoi-motion-gen");
  selected = ["hoi-motion-gen/method"];
  assert.equal(nodeState(hoi, on), "mixed");
  assert.equal(nodeState(find(roots, "hoi-motion-gen/method"), on), "mixed", "its own child is still off");
  selected = applyToggle(selected, toggleTarget(hoi, on).paths, true);
  assert.deepEqual([...selected].sort(), ["hoi-motion-gen/method", "hoi-motion-gen/method/diffusion", "hoi-motion-gen/task-spec"]);
  // Other values keep their place.
  assert.deepEqual(applyToggle(["x", "y"], ["y", "z"], true), ["x", "y", "z"]);
  assert.deepEqual(applyToggle(["x", "y", "z"], ["y"], false), ["x", "z"]);
});

test("fold: hidden children, remembered per tree, long trees start folded", () => {
  const roots = buildTagTree(RAIL);
  assert.equal(visibleRows(roots).length, 9);
  let folded = toggleFold(new Set(), "sandbox");
  assert.deepEqual(visibleRows(roots, folded).map((r) => r.path),
    ["sandbox", "hoi-motion-gen", "hoi-motion-gen/task-spec", "hoi-motion-gen/method", "hoi-motion-gen/method/diffusion", "wbg"]);
  folded = toggleFold(folded, "hoi-motion-gen/method");
  assert.equal(visibleRows(roots, folded).some((r) => r.path === "hoi-motion-gen/method/diffusion"), false);
  assert.deepEqual([...toggleFold(folded, "sandbox")], ["hoi-motion-gen/method"]);

  const storage = memoryStorage();
  assert.equal(loadFolded("rail:p1:branch", { storage }), null, "nothing stored yet");
  saveFolded("rail:p1:branch", folded, { storage });
  assert.deepEqual([...loadFolded("rail:p1:branch", { storage })].sort(), ["hoi-motion-gen/method", "sandbox"]);
  assert.equal(loadFolded("rail:p2:branch", { storage }), null, "per tree");
  saveFolded("rail:p1:branch", new Set(), { storage });
  assert.deepEqual([...loadFolded("rail:p1:branch", { storage })], [], "everything unfolded is remembered too");

  // Without a stored state: short trees open, long ones with every parent folded.
  assert.deepEqual([...initialFolded(roots, null)], []);
  assert.deepEqual([...initialFolded(roots, null, { maxRows: 5 })].sort(), ["hoi-motion-gen", "hoi-motion-gen/method", "sandbox"]);
  assert.deepEqual([...initialFolded(roots, new Set(["wbg"]), { maxRows: 5 })], ["wbg"]);
});

test("Needs you: parent rows hide their lines, the filter keeps the rest", async () => {
  const source = await readFile(new URL("../../ark_studio/web/js/needs-model.js", import.meta.url), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: "needs-model.js" });
  await module.link(() => { throw new Error("needs-model must stay dependency-free"); });
  await module.evaluate();
  const m = module.namespace;
  const items = [
    { uid: "A", type: "task", branches: ["sandbox/infra"] },
    { uid: "B", type: "task", branches: ["sandbox/simulators"] },
    { uid: "C", type: "task", branches: ["sandbox"] },
    { uid: "D", type: "task", branches: ["planner", "sandbox/infra"] },
    { uid: "E", type: "task" },
  ];
  const values = m.needsFacets(items).branch;
  assert.deepEqual(values.map((v) => [v.value, v.n, v.exact]),
    [["planner", 1, 1], ["sandbox", 4, 1], ["sandbox/infra", 2, 2], ["sandbox/simulators", 1, 1], ["", 1, 1]]);
  const roots = buildTagTree(values.filter((v) => v.value).map((v) => ({ path: v.value, n: v.n, own: v.exact > 0 })),
    { rolledUp: true, order: "name" });
  const sandbox = find(roots, "sandbox");
  assert.equal(sandbox.total, 4);
  // Needs you stores hidden values: a value is on while not hidden.
  let hidden = [];
  const on = (path) => !hidden.includes(path);
  const target = toggleTarget(sandbox, on);
  assert.equal(target.on, false);
  hidden = [...new Set([...hidden, ...target.paths])];
  const kept = m.filterNeeds(items, { branch: hidden });
  assert.deepEqual(kept.map((i) => i.uid), ["D", "E"], "D stays: it is on planner too");
  assert.equal(nodeState(sandbox, on), "off");
  hidden = hidden.filter((v) => v !== "sandbox/simulators");
  assert.equal(nodeState(sandbox, on), "mixed");
  assert.deepEqual(m.filterNeeds(items, { branch: hidden }).map((i) => i.uid), ["B", "D", "E"]);
});

test("filter rail: a parent selects its tag and every tag below, OR-ed in the namespace", async () => {
  // GraphFilterIndex is the client-side filter of the rail (graph-filter.js).
  const context = vm.createContext({});
  const loaded = new Map();
  const load = async (url) => {
    if (loaded.has(url.href)) return loaded.get(url.href);
    const mod = new vm.SourceTextModule(await readFile(url, "utf8"), { context, identifier: url.href });
    loaded.set(url.href, mod);
    await mod.link((specifier, parent) => load(new URL(specifier, parent.identifier)));
    return mod;
  };
  const mod = await load(new URL("../../ark_studio/web/js/graph-filter.js", import.meta.url));
  await mod.evaluate();
  const node = (id, tags) => ({ id, uid: `T-${id}`, title: `T ${id}`, type: "task", status: "open", importance: "p2",
    created_at: "2026-01-01", updated_at: "2026-01-01", tags });
  const index = new mod.namespace.GraphFilterIndex({ nodes: [
    node(1, ["branch:sandbox/infra"]), node(2, ["branch:sandbox/simulators"]), node(3, ["branch:sandbox"]),
    node(4, ["branch:hoi-motion-gen/method"]), node(5, ["topic:x"]),
  ] });
  const roots = buildTagTree([
    { path: "sandbox/infra", n: 1 }, { path: "sandbox/simulators", n: 1 }, { path: "sandbox", n: 1 }, { path: "hoi-motion-gen/method", n: 1 },
  ]);
  const paths = toggleTarget(find(roots, "sandbox"), () => false).paths;
  const tags = applyToggle([], paths.map((p) => `branch:${p}`), true);
  const uids = (filters) => [...index.filter({ limit: 100, ...filters }).nodes.map((n) => n.uid)].sort();
  assert.deepEqual(uids({ tags }), ["T-1", "T-2", "T-3"]);
  // Saved filters keep naming single tags; a child alone still works.
  assert.deepEqual(uids({ tags: ["branch:sandbox/infra"] }), ["T-1"]);
  assert.deepEqual(uids({ tags: ["branch:sandbox/infra", "branch:hoi-motion-gen/method"] }), ["T-1", "T-4"]);
});
