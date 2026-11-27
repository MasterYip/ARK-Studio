import assert from "node:assert/strict";
import test from "node:test";
import {
  layoutFromMembers, layoutChanges, ruleFromFilters, ruleParts, moveInOrder, visibleEdges, previewPoints,
  outlineMove, outlineDrop, dropPosition, foldable, foldLevel, subtreeIds, visibleOutline, foldChord, filterTopic, plainShortBody,
} from "../../ark_studio/web/js/topic-model.js";

const members = [
  { id: 1, uid: "S-0001", member: { x: 10, y: 20 } },
  { id: 2, uid: "C-0001", member: { x: null, y: null } },
];

const topic = {
  topic: { id: 99, type: "topic", title: "Container", status: "done" },
  members: [
    { id: 1, type: "story", title: "Opening", status: "done", importance: "p1", tags: ["topic:a"], member: { parent: null, depth: 0, children: 1, path: "1", x: 10, y: 20 } },
    { id: 2, type: "claim", title: "Result", status: "open", importance: "p0", tags: ["topic:a"], member: { parent: 1, depth: 1, children: 0, path: "1.1" } },
    { id: 3, type: "note", title: "Control", status: "open", importance: "p1", tags: [], body: "needle in the body", assignee_effective: "alice", member: { parent: null, depth: 0, children: 0, path: "2" } },
  ],
  edges: [{ id: 10, source: 1, target: 2, hidden: true }, { id: 11, source: 2, target: 3 }],
};

test("topic filters act on members, not the container, and clearing restores order and links", () => {
  const before = structuredClone(topic);
  const filtered = filterTopic(topic, { statuses: ["open"], importance: ["p0"], tags: ["topic:a"] });
  assert.deepEqual(filtered.members.map(node => node.id), [2]);
  assert.equal(filtered.topic, topic.topic);
  assert.deepEqual(filtered.edges, []);
  assert.deepEqual(topic, before);
  const restored = filterTopic(topic);
  assert.deepEqual(restored.members.map(node => node.id), [1, 2, 3]);
  assert.deepEqual(restored.edges, topic.edges);
  assert.deepEqual(restored.members.map(node => node.member), topic.members.map(node => node.member));
});

test("a matching child remains visible when its parent is hidden without changing saved hierarchy", () => {
  const filtered = filterTopic(topic, { types: ["claim", "note"] });
  assert.deepEqual(filtered.members.map(node => [node.id, node.member.parent, node.member.depth]), [[2, null, 0], [3, null, 0]]);
  assert.equal(filtered.members[0].member.path, "1.1");
  assert.equal(topic.members[1].member.parent, 1);
  assert.equal(topic.members[0].member.children, 1);
});

test("topic facets are scoped to members and ignore only their own filter", () => {
  const filtered = filterTopic(topic, { types: ["claim"], statuses: ["open"] });
  assert.deepEqual(filtered.facets.types, { claim: 1, note: 1 });
  assert.deepEqual(filtered.facets.statuses, { open: 1 });
  assert.deepEqual(filtered.facets.namespaces.topic, [{ tag: "topic:a", namespace: "topic", value: "a", count: 1 }]);
  assert.equal(filtered.facets.types.topic, undefined);
});

test("topic keyword/body, none, assignee and one-hop context use shared filter semantics", () => {
  assert.deepEqual(filterTopic(topic, { q: "needle" }).members.map(node => node.id), [3]);
  assert.deepEqual(filterTopic(topic, { q: "Container" }).members, []);
  for (const query of [{ types_none: "1" }, { statuses: "__none__" }, { importance: "__none__" }]) {
    assert.deepEqual(filterTopic(topic, query).members, []);
  }
  assert.deepEqual(filterTopic(topic, { assignee: ["me"] }, "alice").members.map(node => node.id), [3]);
  const context = filterTopic(topic, { types: ["note"], context: 1 });
  assert.deepEqual(context.members.map(node => node.id), [2, 3]);
  assert.equal(context.members[0].context, true);
  assert.deepEqual(context.edges.map(edge => edge.id), [11]);
});

test("topic facet namespaces accept arbitrary user tag names", () => {
  const data = { ...topic, members: [{ ...topic.members[0], tags: ["constructor:a", "__proto__:b"] }] };
  const { facets } = filterTopic(data);
  assert.equal(facets.namespaces.constructor[0].tag, "constructor:a");
  assert.equal(facets.namespaces.__proto__[0].tag, "__proto__:b");
  assert.equal(Object.getPrototypeOf(facets.namespaces), Object.prototype);
});

test("saved member positions load as GraphView node-layout state", () => {
  assert.deepEqual(layoutFromMembers(members), { positions: { "S-0001": { x: 10, y: 20 } }, bends: {}, widths: {} });
  const sized = members.map((unit, i) => (i ? unit : { ...unit, member: { ...unit.member, w: 320 } }));
  assert.deepEqual(layoutFromMembers(sized).widths, { "S-0001": 320 });
});

test("card widths are saved per member, and cleared with null", () => {
  const sized = members.map((unit, i) => (i ? unit : { ...unit, member: { ...unit.member, w: 320 } }));
  const positions = { "S-0001": { x: 10, y: 20 } };
  // Unchanged width and position: nothing to save.
  assert.deepEqual(layoutChanges(sized, { positions, widths: { "S-0001": 320.02 } }), []);
  // A new width alone, then back to automatic.
  assert.deepEqual(layoutChanges(sized, { positions, widths: { "S-0001": 410 } }), [{ unit: 1, w: 410 }]);
  assert.deepEqual(layoutChanges(sized, { positions, widths: { "S-0001": null } }), [{ unit: 1, w: null }]);
  // Moved and resized together; an automatic width on an automatic member is no change.
  assert.deepEqual(layoutChanges(sized, { positions: { "S-0001": { x: 40, y: 20 } }, widths: { "S-0001": 400, "C-0001": null } }),
    [{ unit: 1, x: 40, y: 20, w: 400 }]);
  // Callers without widths never touch them.
  assert.deepEqual(layoutChanges(sized, { positions }), []);
});

test("only moved members are saved", () => {
  const state = { positions: { "S-0001": { x: 10.2, y: 20 }, "C-0001": { x: 300.004, y: 80 }, "X-9": { x: 1, y: 1 } } };
  assert.deepEqual(layoutChanges(members, state), [{ unit: 2, x: 300, y: 80 }]);
  assert.deepEqual(layoutChanges(members, { positions: { "S-0001": { x: 50, y: 20 } } }), [{ unit: 1, x: 50, y: 20 }]);
});

test("filters become a topic rule that keeps the tab's scope", () => {
  assert.equal(ruleFromFilters({}, []), null);
  assert.deepEqual(ruleFromFilters({ q: " warm ", types: [], tags: ["topic:x"], statuses: ["open"] }, ["idea", "claim"]),
    { q: "warm", types: ["idea", "claim"], statuses: ["open"], tags: ["topic:x"] });
  assert.deepEqual(ruleFromFilters({ types: ["story"], statusesNone: true, statuses: ["open"], roots: true,
    timeMode: "timeline", from: "2026-01-01" }, ["idea"]),
  { types: ["story"], roots: true, start_from: "2026-01-01" });
  assert.deepEqual(ruleFromFilters({ typesNone: true, tags: ["a"] }, ["idea"]), { tags: ["a"] });
});

test("rule chips", () => {
  assert.deepEqual(ruleParts({ q: "x", types: ["idea", "claim"], roots: true }),
    [["topic.rule.q", "x"], ["topic.rule.types", "idea, claim"], ["topic.rule.roots", ""]]);
  assert.deepEqual(ruleParts(null), []);
});

test("narrative order moves", () => {
  assert.deepEqual(moveInOrder([1, 2, 3], 2, -1), [2, 1, 3]);
  assert.deepEqual(moveInOrder([1, 2, 3], 3, 1), [1, 2, 3]);
  assert.deepEqual(moveInOrder([1, 2, 3], 9, 1), [1, 2, 3]);
});

test("hidden links only while choosing links", () => {
  const edges = [{ id: 1 }, { id: 2, hidden: true }];
  assert.deepEqual(visibleEdges(edges).map((e) => e.id), [1]);
  assert.deepEqual(visibleEdges(edges, { showHidden: true }).map((e) => e.id), [1, 2]);
});

test("thumbnail points stay inside the unit square", () => {
  const placed = previewPoints([{ id: 1, x: 0, y: 0 }, { id: 2, x: 200, y: 100 }]);
  assert.deepEqual(placed.map((p) => [p.x, p.y]), [[0, 0.25], [1, 0.75]]);
  const ring = previewPoints([{ id: 1 }, { id: 2 }, { id: 3 }]);
  for (const p of ring) assert.ok(p.x >= 0 && p.x <= 1 && p.y >= 0 && p.y <= 1);
  assert.deepEqual(previewPoints([{ id: 7, type: "idea" }]), [{ id: 7, type: "idea", x: 0.5, y: 0.5 }]);
});

// 1 ─ 2
//   └ 3 ─ 4
// 5
const mk = (id, depth, parent, children = 0, extra = {}) => ({ id, member: { depth, parent, children }, ...extra });
const tree = [mk(1, 0, null, 2), mk(2, 1, 1), mk(3, 1, 1, 1), mk(4, 2, 3, 0, { body: "x" }), mk(5, 0, null)];

test("outline moves keep subtrees together", () => {
  assert.deepEqual(outlineMove(tree, 3, "up"), { order: [1, 3, 4, 2, 5], items: [] });
  assert.deepEqual(outlineMove(tree, 1, "down"), { order: [5, 1, 2, 3, 4], items: [] });
  assert.equal(outlineMove(tree, 1, "up"), null);
  assert.deepEqual(outlineMove(tree, 5, "indent"), { order: [1, 2, 3, 4, 5], items: [{ unit: 5, parent: 1 }] });
  assert.equal(outlineMove(tree, 2, "indent"), null);
  assert.deepEqual(outlineMove(tree, 2, "outdent"), { order: [1, 3, 4, 2, 5], items: [{ unit: 2, parent: null }] });
  assert.deepEqual(outlineMove(tree, 4, "outdent"), { order: [1, 2, 3, 4, 5], items: [{ unit: 4, parent: 1 }] });
  assert.equal(outlineMove(tree, 5, "outdent"), null);
});

test("folding follows VS Code levels", () => {
  assert.deepEqual([...foldable(tree)].sort(), [1, 3, 4]);
  assert.deepEqual([...foldLevel(tree, new Set(), 2)], [2, 3]);
  assert.deepEqual(subtreeIds(tree, 3), [3, 4]);
  assert.deepEqual(visibleOutline(tree, new Set([1])).map((u) => u.id), [1, 5]);
  assert.deepEqual(visibleOutline(tree, new Set([3])).map((u) => u.id), [1, 2, 3, 5]);
});

test("Ctrl+K chords", () => {
  assert.deepEqual(foldChord({ key: "j" }), { cmd: "unfoldAll" });
  assert.deepEqual(foldChord({ key: "Unidentified", code: "KeyJ" }), { cmd: "unfoldAll" });
  assert.deepEqual(foldChord({ key: "0" }), { cmd: "foldAll" });
  assert.deepEqual(foldChord({ key: "3", code: "Digit3" }), { cmd: "foldLevel", level: 3 });
  assert.deepEqual(foldChord({ key: "[" }), { cmd: "foldRecursive" });
  assert.deepEqual(foldChord({ key: "]" }), { cmd: "unfoldRecursive" });
  assert.equal(foldChord({ key: "x" }), null);
});

test("drag and drop in the outline", () => {
  // tree: 1 [2, 3 [4]], 5
  assert.deepEqual(outlineDrop(tree, 5, 2, "before"), { order: [1, 5, 2, 3, 4], items: [{ unit: 5, parent: 1 }] });
  assert.deepEqual(outlineDrop(tree, 2, 3, "inside"), { order: [1, 3, 4, 2, 5], items: [{ unit: 2, parent: 3 }] });
  assert.deepEqual(outlineDrop(tree, 3, 5, "after"), { order: [1, 2, 5, 3, 4], items: [{ unit: 3, parent: null }] });
  assert.equal(outlineDrop(tree, 1, 4, "inside"), null);   // never inside itself
  assert.equal(outlineDrop(tree, 2, 3, "before"), null);   // no change
  assert.deepEqual([dropPosition(0.1), dropPosition(0.5), dropPosition(0.9)], ["before", "inside", "after"]);
});

test("topic min links use the topic's own links and report the slider end", () => {
  const all = filterTopic(topic, {});
  assert.equal(all.facets.links_max, 2, "member 2 has an in and an out link");
  const filtered = filterTopic(topic, { min_links: 2 });
  assert.deepEqual(filtered.members.map((m) => m.id), [2]);
  assert.equal(filtered.facets.links_max, 2, "the end does not shrink with the filter");
  assert.deepEqual(filterTopic(topic, { min_links: 1 }).members.map((m) => m.id), [1, 2, 3]);
});

test("a topic's body shows in the head only when it is short plain text", () => {
  assert.equal(plainShortBody("灵巧手 tracking 的数据、方法和实验。"), true);
  assert.equal(plainShortBody("Why this topic exists: **the plan**, see [[S-0001]].\nSecond line."), true, "inline marks and refs are fine");
  assert.equal(plainShortBody(""), false);
  assert.equal(plainShortBody("字".repeat(201)), false, "too long");
  assert.equal(plainShortBody("one\ntwo\nthree"), false, "more than two lines");
  for (const rich of ["## Plan\n\ntext", "- one\n- two", "1. first", "> quote", "```\ncode\n```", "| a | b |",
    "![img](media:1)", "Energy $E=mc^2$ here", "$$x$$", "<div>html</div>", "[only a link](https://x.example)", "https://example.com"]) {
    assert.equal(plainShortBody(rich), false, rich);
  }
});
