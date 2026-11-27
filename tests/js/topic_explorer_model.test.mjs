// Run: node --experimental-vm-modules --test tests/js/topic_explorer_model.test.mjs
import assert from "node:assert/strict";
import { test } from "node:test";
import {
  parseExplorer, sections, topicTree, lineTree, visibleRows, searchRows,
  ancestorKeys, outlineRows, foldCommand,
} from "../../ark_studio/web/js/topic-explorer-model.js";

const keys = (rows) => rows.map((row) => row.key);

test("stored view state: defaults and bad input", () => {
  const empty = parseExplorer("");
  assert.deepEqual(sections.sectionLayout(empty.sections).map((item) => item.open), [true, true, true]);
  assert.deepEqual(parseExplorer("{bad"), empty);
  const stored = parseExplorer(JSON.stringify({ open: false, width: 9999, sections: { collapsed: { lines: true }, max: "outline" },
    folded: { topics: ["TP-1", 3], lines: "x" } }));
  assert.equal(stored.sections.collapsed.lines, true);
  assert.equal(stored.sections.max, "outline");
  assert.deepEqual(stored.folded, { topics: ["TP-1"], lines: [] });
  assert.deepEqual(sections.ids, ["topics", "lines", "outline"]);
});

test("topics nest by part_of, keep their order and survive cycles", () => {
  const topics = [
    { uid: "TP-1", title: "Story", default: "story" },
    { uid: "TP-2", title: "Child", parents: [{ uid: "TP-9" }, { uid: "TP-3" }] },
    { uid: "TP-3", title: "Parent" },
    { uid: "TP-4", title: "Grandchild", parents: [{ uid: "TP-2" }] },
    { uid: "TP-5", title: "A", parents: [{ uid: "TP-6" }] },
    { uid: "TP-6", title: "B", parents: [{ uid: "TP-5" }] },
  ];
  const rows = topicTree(topics);
  assert.deepEqual(rows.map((row) => [row.key, row.depth, row.children]), [
    ["TP-1", 0, 0], ["TP-3", 0, 1], ["TP-2", 1, 1], ["TP-4", 2, 0], ["TP-5", 0, 1], ["TP-6", 1, 0],
  ]);
  assert.deepEqual(keys(visibleRows(rows, new Set(["TP-3"]))), ["TP-1", "TP-3", "TP-5", "TP-6"]);
  assert.deepEqual(ancestorKeys(rows, "TP-4"), ["TP-3", "TP-2"]);
});

test("lines form a path tree with parent rows and rolled-up counts", () => {
  const lines = [
    { name: "robot-use/sandbox", ref: "branch:robot-use/sandbox", units: 4, auto: true },
    { name: "paper", ref: "TP-7", units: 5, auto: false },
    { name: "robot-use/benchmark", ref: "branch:robot-use/benchmark", units: 3, auto: true },
    { name: "hoi/pilot/stairs", ref: "branch:hoi/pilot/stairs", units: 2, auto: true },
  ];
  const rows = lineTree(lines);
  assert.deepEqual(rows.map((row) => [row.key, row.depth, row.label, row.count, Boolean(row.line)]), [
    ["hoi", 0, "hoi", 2, false], ["hoi/pilot", 1, "pilot", 2, false], ["hoi/pilot/stairs", 2, "stairs", 2, true],
    ["paper", 0, "paper", 5, true],
    ["robot-use", 0, "robot-use", 7, false], ["robot-use/benchmark", 1, "benchmark", 3, true], ["robot-use/sandbox", 1, "sandbox", 4, true],
  ]);
  assert.equal(rows.find((row) => row.key === "robot-use").children, 2);
  assert.deepEqual(keys(visibleRows(rows, new Set(["hoi", "robot-use"]))), ["hoi", "paper", "robot-use"]);
});

test("search keeps matches and the rows above them", () => {
  const rows = lineTree([{ name: "a/b/target", units: 1 }, { name: "a/other", units: 1 }, { name: "z", units: 1 }]);
  assert.deepEqual(keys(searchRows(rows, "TARGET")), ["a", "a/b", "a/b/target"]);
  assert.deepEqual(keys(searchRows(rows, "")), keys(rows));
  assert.deepEqual(keys(searchRows(rows, "a other")), ["a", "a/other"], "every word must match");
});

const members = [
  { id: 1, member: { depth: 0, parent: null } },
  { id: 2, member: { depth: 1, parent: 1 } },
  { id: 3, member: { depth: 2, parent: 2 } },
  { id: 4, member: { depth: 1, parent: 1 } },
  { id: 5, member: { depth: 0, parent: null } },
];

test("outline rows count direct children", () => {
  const rows = outlineRows(members);
  assert.deepEqual(rows.map((row) => [row.key, row.depth, row.children]), [[1, 0, 2], [2, 1, 1], [3, 2, 0], [4, 1, 0], [5, 0, 0]]);
  assert.deepEqual(keys(visibleRows(rows, new Set([2]))), [1, 2, 4, 5]);
});

test("fold commands follow the Ctrl+K chords", () => {
  const rows = outlineRows(members);
  const sorted = (set) => [...set].sort();
  assert.deepEqual(sorted(foldCommand(rows, new Set(), { cmd: "foldAll" })), [1, 2]);
  assert.deepEqual(sorted(foldCommand(rows, new Set([1, 2]), { cmd: "unfoldAll" })), []);
  assert.deepEqual(sorted(foldCommand(rows, new Set(), { cmd: "foldLevel", level: 2 })), [2], "leaves cannot fold");
  assert.deepEqual(sorted(foldCommand(rows, new Set(), { cmd: "showLevel", level: 1 })), [1, 2]);
  assert.deepEqual(sorted(foldCommand(rows, new Set(), { cmd: "foldRecursive" }, { focus: 1 })), [1, 2]);
  assert.deepEqual(sorted(foldCommand(rows, new Set([1, 2]), { cmd: "unfoldRecursive" }, { focus: 2 })), [1]);
  assert.deepEqual(sorted(foldCommand(rows, new Set(), { cmd: "toggle" }, { focus: 2 })), [2]);
  assert.deepEqual(sorted(foldCommand(rows, new Set([2]), { cmd: "toggle" }, { focus: 2 })), []);
  assert.deepEqual(sorted(foldCommand(rows, new Set(), { cmd: "fold" }, { focus: 4 })), [], "nothing to fold");
  // A custom foldable set (the document view folds bodies too).
  assert.deepEqual(sorted(foldCommand(rows, new Set(), { cmd: "foldAll" }, { foldable: new Set([1, 4, 5]) })), [1, 4, 5]);
});
