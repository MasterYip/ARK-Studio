// Run: node --experimental-vm-modules --test tests/js/topic_explorer_model.test.mjs
import assert from "node:assert/strict";
import { test } from "node:test";
import {
  parseExplorer, sections, topicTree, lineTree, visibleRows, searchRows,
  ancestorKeys, outlineRows, foldCommand, lineRowRef, lineAssociations, rowActive,
} from "../../ark_studio/web/js/topic-explorer-model.js";
import { branchTopicEntries } from "../../ark_studio/web/js/needs-model.js";

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

// The report (2026-11-27): the line wm-hoi carries a hand-curated topic
// (TP-0009 tags itself branch:wm-hoi, no rule).  Clicking the line opened the
// topic, and both rows lit up as one thing.
test("lines and topics: a line opens as itself, only one row lights up", () => {
  const lines = branchTopicEntries([
    { name: "wm-hoi", tag: "branch:wm-hoi", ref: "TP-0009", units: 1,
      topic: { id: 355, uid: "TP-0009", title: "世界模型与人形 HOI（N-0006）", via: "tag" } },
    { name: "wm-hoi/gcrl", tag: "branch:wm-hoi/gcrl", ref: "branch:wm-hoi/gcrl", units: 16, topic: null },
    { name: "hoi/pilot", tag: "branch:hoi/pilot", units: 3, topic: { uid: "TP-0011", title: "Pilot", via: "rule" } },
  ]);
  const rows = lineTree(lines);
  const row = (key) => rows.find((item) => item.key === key);
  // Every line row opens its tag, never the associated topic.
  assert.equal(lineRowRef(row("wm-hoi"), lines), "branch:wm-hoi");
  assert.equal(lineRowRef(row("wm-hoi/gcrl"), lines), "branch:wm-hoi/gcrl");
  assert.equal(lineRowRef(row("hoi/pilot"), lines), "branch:hoi/pilot", "a pinned (rule) topic does not take over either");
  assert.equal(lineRowRef(row("hoi"), lines), "branch:hoi", "a path-only row opens by its tag");
  assert.equal(row("wm-hoi").count, 17, "the line's count covers its sub-lines");

  // The line view is open: only the line row is on, not the topic.
  const topicRow = { key: "TP-0009" };
  assert.equal(rowActive("lines", row("wm-hoi"), "branch:wm-hoi", lines), true);
  assert.equal(rowActive("lines", row("wm-hoi/gcrl"), "branch:wm-hoi", lines), false);
  assert.equal(rowActive("topics", topicRow, "branch:wm-hoi", lines), false);
  // The topic is open: only the topic row is on.
  assert.equal(rowActive("topics", topicRow, "TP-0009", lines), true);
  assert.equal(rows.some((item) => rowActive("lines", item, "TP-0009", lines)), false);
  assert.equal(rowActive("lines", row("wm-hoi"), "", lines), false);

  // The association is kept both ways for the chip and the badge.
  const { topicOf, linesOf } = lineAssociations(lines);
  assert.equal(topicOf.get("wm-hoi").uid, "TP-0009");
  assert.equal(topicOf.has("wm-hoi/gcrl"), false);
  assert.deepEqual(linesOf.get("TP-0009").map((line) => line.name), ["wm-hoi"]);
  assert.deepEqual(linesOf.get("TP-0011").map((line) => line.name), ["hoi/pilot"]);
  assert.equal(linesOf.has("TP-0001"), false, "a topic without a line gets no badge");
});

test("lines and topics: junk input", () => {
  assert.deepEqual([...lineAssociations(null).topicOf], []);
  assert.equal(rowActive("lines", null, "branch:x"), false);
  assert.equal(lineRowRef({ key: "a/b" }, []), "branch:a/b");
  assert.equal(lineRowRef({ key: "a" }, [{ name: "z", tag: "line:z" }]), "line:a", "the project's line prefix");
});
