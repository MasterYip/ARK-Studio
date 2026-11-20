// Research › Review: selection helpers, routes, Home header links and strings.
// Run: node --experimental-vm-modules tests/js/needs_review.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import {
  filterNeeds, needsOutcomeTags, needsValueOf, pruneSelection, reviewHash, reviewView, selectAll, selectedItems,
  selectionState, sortMineFirst, splitForReview, toggleNeedsValue,
} from "../../ark_studio/web/js/needs-model.js";

const web = new URL("../../ark_studio/web/", import.meta.url);
const read = (path) => readFile(new URL(path, web), "utf8");

const ITEMS = [
  { uid: "T-1", type: "task", kind: "review", assignee_effective: "bob" },
  { uid: "D-1", type: "decision", kind: "decision", assignee_effective: "ann" },
  { uid: "E-1", type: "experiment", kind: "review", assignee_effective: "" },
  { uid: "D-2", type: "decision", kind: "decision", assignee_effective: "Ann" },
];

test("mine first, otherwise server order", () => {
  assert.deepEqual(sortMineFirst(ITEMS, "ANN").map((i) => i.uid), ["D-1", "D-2", "T-1", "E-1"]);
  assert.deepEqual(sortMineFirst(ITEMS, "").map((i) => i.uid), ["T-1", "D-1", "E-1", "D-2"]);
});

test("select all respects the filter and keeps hidden selections untouched", () => {
  const shown = filterNeeds(ITEMS, toggleNeedsValue({}, "kind", "decision"));
  assert.deepEqual(shown.map((i) => i.uid), ["T-1", "E-1"]);
  let selected = selectAll(new Set(["D-1"]), shown, true);
  assert.deepEqual([...selected].sort(), ["D-1", "E-1", "T-1"]);
  assert.deepEqual(selectionState(selected, shown), { n: 2, all: true, some: false });
  selected = selectAll(selected, shown, false);
  assert.deepEqual([...selected], ["D-1"]);
  assert.deepEqual(selectionState(new Set(["T-1"]), shown), { n: 1, all: false, some: true });
  assert.deepEqual(selectionState(new Set(), []), { n: 0, all: false, some: false });
});

test("selection is pruned to listed items and read back in list order", () => {
  const selected = pruneSelection(new Set(["E-1", "gone", "T-1"]), ITEMS);
  assert.deepEqual([...selected].sort(), ["E-1", "T-1"]);
  assert.deepEqual(selectedItems(selected, ITEMS).map((i) => i.uid), ["T-1", "E-1"]);
  assert.equal(pruneSelection(null, ITEMS).size, 0);
});

test("accept and send back take reviews; decisions are counted as skipped", () => {
  const { reviews, decisions } = splitForReview(ITEMS);
  assert.deepEqual(reviews.map((i) => i.uid), ["T-1", "E-1"]);
  assert.deepEqual(decisions.map((i) => i.uid), ["D-1", "D-2"]);
  assert.deepEqual(splitForReview([]), { reviews: [], decisions: [] });
});

test("history: outcome tags and a unit's needs value", () => {
  assert.deepEqual(needsOutcomeTags(), ["needs:accepted", "needs:sent-back", "needs:decided", "needs:dismissed"]);
  assert.equal(needsValueOf(["topic:x", "needs:accepted"]), "accepted");
  assert.equal(needsValueOf(["needs:sent-back", "needs:accepted"]), "sent-back");
  assert.equal(needsValueOf(["needs:accepted", "needs:review"]), "review");
  assert.equal(needsValueOf([]), "");
  assert.equal(needsValueOf(undefined), "");
});

test("review routes", () => {
  assert.equal(reviewView("history"), "history");
  assert.equal(reviewView(undefined), "waiting");
  assert.equal(reviewView("junk"), "waiting");
  assert.equal(reviewHash("waiting"), "#/research/review");
  assert.equal(reviewHash("history"), "#/research/review/history");
});

test("app.js routes #/research/review like the other Research sub-tabs", async () => {
  const app = await read("js/app.js");
  assert.match(app, /research: \["ideas", "plan", "review"\]/);
  assert.match(app, /if \(view === "review"\) return \{ tab: "review", sub: "story", workspace, reviewView: reviewView\(parts\[2\]\) \}/);
  assert.match(app, /if \(tab === "review"\) return reviewHash\(S\.reviewView\)/);
  assert.match(app, /add\(t\("nav\.review"\), S\.tab === "review", \(\) => navigateWorkspace\("research", "review"\)/);
  assert.match(app, /case "review": return lazyView\(host, VIEW_MODULES\.review, "renderReview"\)/);
  // Back/forward between waiting and history re-renders; a deep link sets it on boot too.
  assert.match(app, /route\.reviewView === reviewView\(S\.reviewView\)/);
  assert.equal(app.match(/if \(route\.reviewView\) S\.reviewView = route\.reviewView;/g)?.length, 2);
  const state = await read("js/state.js");
  assert.match(state, /review: "list"/);
  assert.match(state, /review: \[\]/);
});

test("Home header: Accept several and View all are plain links, no history box class", async () => {
  const overview = await read("js/views/overview.js");
  assert.match(overview, /class: "link needs-bulk"/);
  assert.match(overview, /class: "link needs-view-all"/);
  assert.match(overview, /ctx\.navigateWorkspace\("research", "review"\)/);
  assert.doesNotMatch(overview, /needs-history/);
  assert.doesNotMatch(overview, /btn sm ghost needs-bulk/);
  const mobile = await read("css/mobile.css");
  assert.doesNotMatch(mobile, /panel-head \.needs-history/);
});

test("strings exist in both languages", async () => {
  const keys = [
    "nav.review", "needs.view_all", "needs.view_all_title", "review.title", "review.history", "review.history_sub",
    "review.empty_hint", "review.show_history", "review.count", "review.select_all_n", "review.select_item",
    "review.selection", "review.clear", "review.skip_decisions", "review.only_decisions", "review.accept_title",
    "review.send_back_title", "review.comment_title", "review.reason_each", "review.comment_each", "review.sent_back_n",
    "review.sent_back_failed", "review.commented_n", "review.comment_failed", "review.open_map", "review.history_empty",
    "review.history_more",
  ];
  const en = await read("js/i18n-en.js");
  const zh = await read("js/i18n-zh.js");
  for (const key of keys) {
    assert.ok(en.includes(`"${key}"`), `i18n-en.js ${key}`);
    assert.ok(zh.includes(`"${key}"`), `i18n-zh.js ${key}`);
  }
  assert.match(zh, /"needs\.view_all": "查看详情"/);
  assert.match(zh, /"nav\.review": "审阅"/);
  // The old Map-only link is gone in both languages.
  assert.ok(!en.includes("\"needs.view_history\""));
  assert.ok(!zh.includes("\"needs.view_history\""));
  // Every t("review.*") used by the view exists.
  const view = await read("js/views/review.js") + await read("js/needs-ui.js");
  for (const [, key] of view.matchAll(/t\("((?:review|needs)\.[a-z_.]+)"/g)) {
    assert.ok(en.includes(`"${key}"`), `en ${key}`);
    assert.ok(zh.includes(`"${key}"`), `zh ${key}`);
  }
});
