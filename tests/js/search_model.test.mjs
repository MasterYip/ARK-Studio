// Run: node --experimental-vm-modules --test tests/js/search_model.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

async function loadModel() {
  const source = await readFile(new URL("../../ark_studio/web/js/search-model.js", import.meta.url), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: "search-model.js" });
  await module.link(() => { throw new Error("search-model must stay dependency-free"); });
  await module.evaluate();
  return module.namespace;
}

const m = await loadModel();

test("query options and request parameters", () => {
  assert.equal(m.searchParams({ q: "   " }), null);
  assert.deepEqual(m.searchParams({ q: "zebra", case: true, regex: false, include: "*.md", exclude: "drafts" }), {
    nodes: { q: "zebra", case: 1 },
    files: { q: "zebra", case: 1, include: "*.md", exclude: "drafts" },
  });
  assert.deepEqual(m.normalizeQuery({ q: 5, word: 1, junk: true }),
    { q: "5", case: false, word: true, regex: false, include: "", exclude: "", details: false });
  assert.equal(m.queryError({ q: "(", regex: true }) !== "", true);
  assert.equal(m.queryError({ q: "(", regex: false }), "");
});

const nodes = { nodes: [
  { id: 7, uid: "I-0007", type: "idea", title: "Zebra", matches: [{ field: "title", line: 1, text: "Zebra", ranges: [[0, 5]] },
    { field: "body", line: 3, text: "a zebra", ranges: [[2, 7]] }] },
], truncated: false };
const files = { files: [{ path: "docs/z.md", name: "z.md", matches: [{ line: 4, text: "zebra!", ranges: [[0, 5]] }] }], truncated: true };

test("grouping, counts and summary", () => {
  const groups = m.groupResults(nodes, files);
  assert.deepEqual(groups.map((g) => [g.kind, g.items.length, g.count]), [["nodes", 1, 2], ["files", 1, 1]]);
  assert.deepEqual(m.resultSummary(groups), { count: 3, items: 2, truncated: true });
  assert.equal(groups[1].items[0].title, "z.md");
  const empty = m.groupResults(null, { files: [] });
  assert.deepEqual(m.resultSummary(empty), { count: 0, items: 0, truncated: false });
});

test("rows honour collapsed groups and items; F4 cycles through matches", () => {
  const groups = m.groupResults(nodes, files);
  let rows = m.resultRows(groups);
  assert.deepEqual(rows.map((r) => r.type), ["group", "item", "match", "match", "group", "item", "match"]);
  rows = m.resultRows(groups, new Set(["node:7"]));
  assert.deepEqual(rows.map((r) => r.type), ["group", "item", "group", "item", "match"]);
  rows = m.resultRows(groups, new Set(["group:files"]));
  assert.deepEqual(rows.map((r) => r.type), ["group", "item", "match", "match", "group"]);
  rows = m.resultRows(groups);
  assert.equal(m.nextMatch(rows, -1, 1), 2);
  assert.equal(m.nextMatch(rows, 2, 1), 3);
  assert.equal(m.nextMatch(rows, 3, 1), 6);
  assert.equal(m.nextMatch(rows, 6, 1), 2, "wraps to the first match");
  assert.equal(m.nextMatch(rows, 2, -1), 6, "Shift+F4 wraps to the last match");
  assert.equal(m.nextMatch([], 0, 1), -1);
  assert.equal(m.moveRow(rows, -1, 1), 0);
  assert.equal(m.moveRow(rows, 6, 1), 6);
});

test("match preview starts just before the first match", () => {
  const long = "Whole-body guidance and tracker-level stability";
  const preview = m.matchPreview(long, [[24, 31]]);
  assert.equal(preview.text, "…guidance and tracker-level stability");
  const [a, b] = preview.ranges[0];
  assert.equal(preview.text.slice(a, b), "tracker");
  assert.deepEqual(m.matchPreview("    # tracker", [[6, 13]]), { text: "# tracker", ranges: [[2, 9]] });
  assert.deepEqual(m.matchPreview("run,tracker", [[4, 11]]), { text: "run,tracker", ranges: [[4, 11]] });
});

test("highlight segments", () => {
  assert.deepEqual(m.highlightSegments("a zebra here", [[2, 7]]),
    [{ text: "a ", hit: false }, { text: "zebra", hit: true }, { text: " here", hit: false }]);
  assert.deepEqual(m.highlightSegments("zz", [[0, 1], [1, 2]]), [{ text: "z", hit: true }, { text: "z", hit: true }]);
  assert.deepEqual(m.highlightSegments("", []), [{ text: "", hit: false }]);
  assert.deepEqual(m.highlightSegments("abc", [[5, 9]]), [{ text: "abc", hit: false }]);
});

test("streamed events fold into route-shaped payloads", () => {
  const nodes = m.emptyPayload("nodes");
  assert.equal(m.applySearchEvent(nodes, "nodes", { type: "node", node: { id: 1, uid: "I-1", title: "A", matches: [{ line: 1, ranges: [[0, 1]] }] } }), true);
  assert.equal(m.applySearchEvent(nodes, "nodes", { type: "file", file: { path: "x" } }), false, "other kinds are ignored");
  assert.equal(nodes.partial, true);
  m.applySearchEvent(nodes, "nodes", { type: "done", total: 1, truncated: true, timed_out: false, cancelled: false });
  assert.deepEqual({ ...nodes, nodes: nodes.nodes.length }, { nodes: 1, partial: false, total: 1, truncated: true, timed_out: false, cancelled: false });
  const files = m.emptyPayload("files");
  m.applySearchEvent(files, "files", { type: "file", file: { path: "docs/a.md", name: "a.md", matches: [{ line: 2, ranges: [[0, 1]] }] } });
  m.applySearchEvent(files, "files", { type: "error", message: "boom" });
  assert.equal(files.error, "boom");
  const groups = m.groupResults(nodes, files);
  assert.deepEqual(groups.map((g) => [g.kind, g.items.length, g.count, g.truncated]), [["nodes", 1, 1, true], ["files", 1, 1, false]]);
  assert.equal(m.applySearchEvent(files, "files", null), false);
});

test("virtualised window and keyboard reveal", () => {
  const H = m.SEARCH_ROW_HEIGHT;
  assert.deepEqual(m.visibleRange(0, 10 * H, 1000, H, 0), { start: 0, end: 10 });
  assert.deepEqual(m.visibleRange(100 * H, 10 * H, 1000, H, 5), { start: 95, end: 115 });
  assert.deepEqual(m.visibleRange(0, 10 * H, 3, H, 5), { start: 0, end: 3 });
  assert.deepEqual(m.visibleRange(5000 * H, 10 * H, 3, H, 5), { start: 3, end: 3 }, "scrolled past the end after results shrank");
  assert.deepEqual(m.visibleRange(0, 0, 0), { start: 0, end: 0 });
  assert.equal(m.scrollForRow(5, 0, 10 * H, H), 0, "visible: unchanged");
  assert.equal(m.scrollForRow(20, 0, 10 * H, H), 11 * H, "below: bottom-aligned");
  assert.equal(m.scrollForRow(2, 10 * H, 10 * H, H), 2 * H, "above: top-aligned");
  assert.equal(m.scrollForRow(-1, 40, 100, H), 40);
});
