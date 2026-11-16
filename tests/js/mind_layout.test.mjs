// Run: node --test tests/js/mind_layout.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import { mindLayout, cardOverlaps, ORPHAN_GAP_Y } from "../../web/js/mind-layout.js";

function synthetic(count, orphans, seed) {
  let state = seed;
  const random = () => ((state = (state * 1103515245 + 12345) % 2147483648) / 2147483648);
  const words = "graph policy evidence tracker latency control benchmark ablation dataset model".split(" ");
  const title = () => Array.from({ length: 1 + Math.floor(random() * 24) }, () => words[Math.floor(random() * words.length)]).join(" ");
  const nodes = Array.from({ length: count + orphans }, (_, i) => {
    const node = { id: i + 1, title: title(), type: ["idea", "task", "claim"][i % 3], status: ["open", "done"][i % 2] };
    if (random() < 0.15) node.media = [{ id: `m${i}`, url: `m${i}.png` }, { id: `n${i}`, url: `n${i}.png` }];
    if (random() < 0.06) {
      node.media = [{ id: `p${i}`, url: `p${i}.png` }];
      node.pinnedImage = { mediaId: `p${i}`, side: ["right", "top", "left", "bottom"][i % 4], w: 160 + random() * 240, aspect: 0.5 + random() };
    }
    if (random() < 0.1) node.customW = 140 + random() * 450;
    return node;
  });
  const edges = [];
  for (let i = 2; i <= count; i++) {
    if (random() < 0.1) continue;
    edges.push({ id: `e${i}`, source: i, target: 1 + Math.floor(random() * (i - 1)), relation: "part_of" });
  }
  return { nodes, edges };
}

const positions = (nodes) => new Map(nodes.map((n) => [n.id, [n.x, n.y]]));

test("no card overlaps with varied sizes, media, widths and many orphans", () => {
  for (const [count, orphans, seed] of [[40, 80, 1], [150, 60, 2], [500, 300, 5]]) {
    const { nodes, edges } = synthetic(count, orphans, seed);
    mindLayout(nodes, edges);
    assert.deepEqual(cardOverlaps(nodes), [], `${count}+${orphans}`);
    const orphanNodes = nodes.filter((n) => n.mindOrphan);
    assert.ok(orphanNodes.length >= orphans, "orphans are packed in the orphan area");
    // Orphans sit right of the forest, never over it.
    const forestRight = Math.max(...nodes.filter((n) => !n.mindOrphan).map((n) => n.x + n.w / 2));
    assert.ok(orphanNodes.every((n) => n.x - n.w / 2 > forestRight), "orphans never overlap the tree");
  }
});

test("orphans: sorted by type, status, title; even vertical gaps in each column", () => {
  const nodes = [
    { id: 1, title: "b", type: "task", status: "open" }, { id: 2, title: "a", type: "task", status: "done" },
    { id: 3, title: "z", type: "idea", status: "open" }, { id: 4, title: "a very long title ".repeat(6), type: "claim", status: "open" },
  ];
  nodes.push({ id: 10, title: "root", type: "topic" }, { id: 11, title: "leaf", type: "idea" });
  mindLayout(nodes, [{ id: "t", source: 11, target: 10, relation: "part_of" }]);
  nodes.splice(4, 2);
  // Column-major: down each column, then the next column.
  const order = nodes.slice().sort((a, b) => (a.x - a.w / 2) - (b.x - b.w / 2) || a.y - b.y).map((n) => n.id);
  assert.deepEqual(order, [4, 3, 2, 1]);
  const sorted = nodes.slice().sort((a, b) => a.y - b.y);
  for (let i = 1; i < sorted.length; i++) {
    if (Math.abs(sorted[i].x - sorted[i].w / 2 - (sorted[i - 1].x - sorted[i - 1].w / 2)) > 0.01) continue;
    const gap = (sorted[i].y - sorted[i].h / 2) - (sorted[i - 1].y + sorted[i - 1].h / 2);
    assert.ok(Math.abs(gap - ORPHAN_GAP_Y) < 1e-9);
  }
});

function foldFixture() {
  const { nodes, edges } = synthetic(120, 50, 4);
  const parentOf = new Map(edges.map((e) => [e.source, e.target]));
  const rootOf = (id) => { while (parentOf.has(id)) id = parentOf.get(id); return id; };
  const withKids = new Set(edges.map((e) => e.target));
  // A branch with children inside a tree (not a root).
  const branch = nodes.find((n) => withKids.has(n.id) && parentOf.has(n.id));
  return { nodes, edges, parentOf, rootOf, branch };
}

test("a folded branch is compact: no gap where its children would be", () => {
  const { nodes, edges, branch } = foldFixture();
  const { hidden } = mindLayout(nodes, edges, { collapsed: new Set([branch.id]) });
  assert.ok(hidden.size > 0);
  // The forest is the same as laying out the graph without the hidden cards
  // at all (orphans: same rows, but right of the expanded forest).
  const visible = nodes.filter((n) => !hidden.has(n.id)).map((n) => ({ ...n }));
  const ids = new Set(visible.map((n) => n.id));
  mindLayout(visible, edges.filter((e) => ids.has(e.source) && ids.has(e.target)));
  const shift = new Set();
  for (const node of visible) {
    const laid = nodes.find((n) => n.id === node.id);
    if (laid.mindOrphan) { assert.equal(laid.y, node.y, `orphan ${node.id} row`); shift.add(Math.round((laid.x - node.x) * 1e6)); continue; }
    assert.deepEqual([laid.x, laid.y], [node.x, node.y], `node ${node.id}`);
  }
  assert.equal(shift.size, 1, "the orphan area only moves sideways, as a block");
  assert.deepEqual(cardOverlaps(nodes.filter((n) => !hidden.has(n.id))), []);
  // The orphan area starts right of the expanded forest: unfolding never reaches it.
  mindLayout(nodes, edges);
  const forestRight = Math.max(...nodes.filter((n) => !n.mindOrphan).map((n) => n.x + n.w / 2));
  mindLayout(nodes, edges, { collapsed: new Set([branch.id]) });
  assert.ok(nodes.filter((n) => n.mindOrphan).every((n) => n.x - n.w / 2 > forestRight));
});

test("folded roots and inner nodes are never orphans; they stay in their tree", () => {
  const { nodes, edges, branch, rootOf } = foldFixture();
  const root = nodes.find((n) => n.id === rootOf(branch.id));
  const orphansOpen = new Set(nodes.filter((n) => (mindLayout(nodes, edges), n.mindOrphan)).map((n) => n.id));
  mindLayout(nodes, edges);
  const orphanIds = new Set(nodes.filter((n) => n.mindOrphan).map((n) => n.id));
  const { hidden, treeOf } = mindLayout(nodes, edges, { collapsed: new Set([root.id, branch.id]) });
  assert.ok(hidden.has(branch.id), "the inner branch is inside the folded root");
  assert.equal(root.mindOrphan, false, "a folded root is not an orphan");
  assert.equal(branch.mindOrphan, false, "a folded inner node is not an orphan");
  assert.equal(treeOf.get(root.id), root.id);
  assert.equal(treeOf.get(branch.id), root.id);
  assert.deepEqual(new Set(nodes.filter((n) => n.mindOrphan).map((n) => n.id)), orphanIds, "orphans depend only on relations");
  assert.ok(root.x - root.w / 2 < Math.min(...nodes.filter((n) => n.mindOrphan).map((n) => n.x - n.w / 2)), "the folded root stays left of the orphan area");
  // Folding only the inner node: also not an orphan, still in the tree.
  mindLayout(nodes, edges, { collapsed: new Set([branch.id]) });
  assert.equal(branch.mindOrphan, false);
  assert.equal(orphansOpen.size, orphanIds.size);
});

test("the demo project lays out without overlaps", async () => {
  let demo;
  try {
    demo = JSON.parse(await readFile(new URL("../fixtures/demo-graph.json", import.meta.url), "utf8"));
  } catch {
    return; // fixture optional
  }
  mindLayout(demo.nodes, demo.edges);
  assert.deepEqual(cardOverlaps(demo.nodes), []);
});
