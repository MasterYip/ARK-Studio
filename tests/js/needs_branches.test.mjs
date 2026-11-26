// Run: node --experimental-vm-modules --test tests/js/needs_branches.test.mjs
// Lines of work in Needs you and the Topics bar (needs-model.js; server:
// store/branches.go).
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

async function loadModel() {
  const source = await readFile(new URL("../../ark_studio/web/js/needs-model.js", import.meta.url), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: "needs-model.js" });
  await module.link(() => { throw new Error("needs-model must stay dependency-free"); });
  await module.evaluate();
  return module.namespace;
}

const m = await loadModel();

const items = [
  { uid: "S-1", kind: "review", type: "story", updated_at: "2026-11-25T10:00:00+00:00", branches: ["pilot"] },
  { uid: "X-1", kind: "review", type: "experiment", updated_at: "2026-11-26T09:00:00+00:00", branches: ["robot", "pilot"] },
  { uid: "D-1", kind: "decision", type: "decision", updated_at: "2026-11-20T09:00:00+00:00", branches: ["old"] },
  { uid: "F-1", kind: "review", type: "task", updated_at: "2026-11-26T08:00:00+00:00" },
  { uid: "RG-2", kind: "review_group", type: "review_group", updated_at: "2026-11-24T08:00:00+00:00", branches: ["pilot"] },
];

test("groupNeedsByBranch: one section per line, untagged last, each item once", () => {
  const sections = m.groupNeedsByBranch(items, []);
  // X-1 is on two lines: it shows once, under the line listed first (pilot and
  // robot tie on their newest item; names break the tie), the other as a chip.
  assert.deepEqual(sections.map((s) => s.key), ["pilot", "old", ""]);
  assert.deepEqual(sections[0].items.map((i) => i.uid), ["S-1", "X-1", "RG-2"]);
  assert.deepEqual(sections[0].extra, { "X-1": ["robot"] });
  assert.deepEqual(sections[2].items.map((i) => i.uid), ["F-1"]);
  const shown = sections.flatMap((s) => s.items.map((i) => i.uid)).sort();
  assert.deepEqual(shown, items.map((i) => i.uid).sort());
  // A newer item on robot alone puts robot first; X-1 then shows under robot.
  const more = [...items, { uid: "T-9", kind: "review", updated_at: "2026-11-26T12:00:00+00:00", branches: ["robot"] }];
  const again = m.groupNeedsByBranch(more, []);
  assert.deepEqual(again.map((s) => s.key), ["robot", "pilot", "old", ""]);
  assert.deepEqual(again[0].items.map((i) => i.uid), ["X-1", "T-9"]);
  assert.deepEqual(again[0].extra, { "X-1": ["pilot"] });
});

test("groupNeedsByBranch: stalled lines lead and carry their info", () => {
  const infos = [{ name: "old", status: "stalled", last_activity: "2026-11-20T09:00:00+00:00" }, { name: "pilot", status: "active" }];
  const sections = m.groupNeedsByBranch(items, infos);
  assert.equal(sections[0].key, "old");
  assert.equal(sections[0].info.status, "stalled");
  assert.equal(sections.find((s) => s.key === "pilot").info.status, "active");
  assert.equal(sections.find((s) => s.key === "robot"), undefined);
  assert.deepEqual(m.groupNeedsByBranch([], infos), []);
});

test("branch filter: multi-line items, no line as its own value", () => {
  const facets = m.needsFacets(items);
  // A tree in path order, "no line" last.
  assert.deepEqual(facets.branch.map((f) => [f.value, f.n]), [["old", 1], ["pilot", 3], ["robot", 1], ["", 1]]);
  // Hiding pilot keeps X-1 (still on robot); hiding pilot and robot drops it.
  let filter = m.toggleNeedsValue(m.normalizeNeedsFilter({}), "branch", "pilot");
  assert.deepEqual(m.filterNeeds(items, filter).map((i) => i.uid), ["X-1", "D-1", "F-1"]);
  filter = m.toggleNeedsValue(filter, "branch", "robot");
  assert.deepEqual(m.filterNeeds(items, filter).map((i) => i.uid), ["D-1", "F-1"]);
  filter = m.toggleNeedsValue(filter, "branch", "");
  assert.deepEqual(m.filterNeeds(items, filter).map((i) => i.uid), ["D-1"]);
  // A project without lines shows no line filter.
  assert.deepEqual(m.needsFacets([{ uid: "A", type: "task" }]).branch, []);
});

test("layout choice per person and project, grouped by default", () => {
  assert.equal(m.needsLayout(null), "branch");
  assert.equal(m.needsLayout("branch"), "branch");
  assert.equal(m.needsLayout("flat"), "flat");
  assert.notEqual(m.needsLayoutKey("ana", "prj_1"), m.needsLayoutKey("bob", "prj_1"));
  assert.notEqual(m.needsLayoutKey("ana", "prj_1"), m.needsLayoutKey("ana", "prj_2"));
});

test("branchTopicEntries: lines without a topic first, then by last activity", () => {
  const entries = m.branchTopicEntries([
    { name: "hoi-pilot", tag: "branch:hoi-pilot", ref: "TP-0010", topic: { uid: "TP-0010", title: "HOI pilot" }, units: 287, last_activity: "2026-11-25" },
    { name: "robot-use", tag: "branch:robot-use", ref: "branch:robot-use", topic: null, units: 46, pending: 1, status: "stalled", last_activity: "2026-11-20" },
    { name: "new", tag: "branch:new", topic: null, units: 2, last_activity: "2026-11-26" },
  ]);
  assert.deepEqual(entries.map((e) => [e.name, e.auto, e.ref]), [
    ["new", true, "branch:new"], ["robot-use", true, "branch:robot-use"], ["hoi-pilot", false, "TP-0010"]]);
  assert.equal(entries[1].status, "stalled");
  assert.equal(m.isBranchRef("branch:robot-use"), true);
  assert.equal(m.isBranchRef("TP-0010"), false);
  assert.equal(m.isBranchRef(""), false);
});

test("itemBranches dedupes and ignores junk", () => {
  assert.deepEqual(m.itemBranches({ branches: ["a", "a", "", null, "b"] }), ["a", "b"]);
  assert.deepEqual(m.itemBranches({}), []);
});

test("Home and Review share one layout choice per person and project", async () => {
  const src = await readFile(new URL("../../ark_studio/web/js/views/review.js", import.meta.url), "utf8");
  const home = await readFile(new URL("../../ark_studio/web/js/views/overview.js", import.meta.url), "utf8");
  for (const text of [src, home]) {
    assert.match(text, /storedNeedsLayout\(ctx\)/);
    assert.match(text, /needsLayoutToggle\(ctx,/);
    assert.match(text, /groupNeedsTree\(items, branchesData\(ctx\)\?\.items \|\| \[\]\)/);
    assert.match(text, /lineTreeSections\(ctx, tree,/);
  }
});

test("Review by line: selecting a group selects its units, the rest stays", () => {
  const sections = m.groupNeedsByBranch(items, []);
  const pilot = m.unitItems(sections[0].items);
  let selected = new Set(["F-1"]);
  selected = m.selectAll(selected, pilot, true);
  assert.deepEqual([...selected].sort(), ["F-1", "S-1", "X-1"]);
  assert.deepEqual(m.selectionState(selected, pilot), { n: 2, all: true, some: false });
  selected = m.selectAll(selected, pilot, false);
  assert.deepEqual([...selected], ["F-1"]);
});

// Line paths (主线/分线/分分线).
const tree = [
  { uid: "A", kind: "review", updated_at: "2026-11-26T10:00:00+00:00", branches: ["hoi/pilot/stairs"] },
  { uid: "B", kind: "review", updated_at: "2026-11-25T10:00:00+00:00", branches: ["hoi/pilot"] },
  { uid: "C", kind: "decision", updated_at: "2026-11-20T10:00:00+00:00", branches: ["hoi/world"] },
  { uid: "D", kind: "review", updated_at: "2026-11-24T10:00:00+00:00", branches: ["hoi/pilot", "robot/use"] },
  { uid: "E", kind: "review", updated_at: "2026-11-24T09:00:00+00:00" },
];

test("line paths: labels, ancestors, validation", () => {
  assert.equal(m.lineLabel("hoi/pilot/stairs"), "hoi › pilot › stairs");
  assert.deepEqual(m.lineAncestors("a/b/c"), ["a", "a/b"]);
  assert.deepEqual(m.lineAncestors("a"), []);
  for (const ok of ["hoi", "hoi/pilot", "主线/分线"]) assert.equal(m.validLinePath(ok), true, ok);
  for (const bad of ["", "hoi/", "/hoi", "a//b", "a b"]) assert.equal(m.validLinePath(bad), false, bad);
});

test("Needs you as a tree: parents roll up count, activity and stalled", () => {
  const infos = [{ name: "hoi/world", status: "stalled", last_activity: "2026-11-20T10:00:00+00:00" },
    { name: "hoi/pilot", status: "active" }, { name: "hoi/pilot/stairs", status: "active" }];
  const { nodes, loose } = m.groupNeedsTree(tree, infos);
  // robot/use's only item shows under hoi/pilot, so robot has no group.
  assert.deepEqual(nodes.map((n) => n.key), ["hoi"]);
  const hoi = nodes[0];
  assert.equal(hoi.items.length, 0); // nobody tagged "hoi" itself
  assert.equal(hoi.total, 4); // A, B, D under pilot (+stairs), C under world
  assert.equal(hoi.status, "stalled"); // a child is stalled
  assert.equal(hoi.last, "2026-11-26T10:00:00+00:00");
  assert.deepEqual(hoi.children.map((c) => c.key), ["hoi/world", "hoi/pilot"]); // stalled first
  const pilot = hoi.children[1];
  assert.deepEqual(pilot.items.map((i) => i.uid), ["B", "D"]);
  assert.deepEqual(pilot.children.map((c) => c.label), ["stairs"]);
  assert.deepEqual(m.treeItems(pilot).map((i) => i.uid).sort(), ["A", "B", "D"]);
  // D is on two subtrees: shown once (under hoi/pilot), robot/use as "+".
  assert.deepEqual(pilot.extra, { D: ["robot/use"] });
  assert.deepEqual(loose.items.map((i) => i.uid), ["E"]);
});

test("line filter is a tree; hiding a parent hides its children", () => {
  const facets = m.needsFacets(tree);
  assert.deepEqual(facets.branch.map((f) => [f.value, f.n, f.depth]), [
    ["hoi", 4, 0], ["hoi/pilot", 3, 1], ["hoi/pilot/stairs", 1, 2], ["hoi/world", 1, 1], ["robot", 1, 0], ["robot/use", 1, 1], ["", 1, 0]]);
  const values = facets.branch.map((f) => f.value);
  let filter = m.toggleNeedsValue(m.normalizeNeedsFilter({}), "branch", "hoi/pilot", values);
  assert.deepEqual(filter.branch.sort(), ["hoi/pilot", "hoi/pilot/stairs"]);
  // D stays: it is also on robot/use.
  assert.deepEqual(m.filterNeeds(tree, filter).map((i) => i.uid), ["C", "D", "E"]);
  filter = m.toggleNeedsValue(filter, "branch", "hoi/pilot", values);
  assert.deepEqual(filter.branch, []);
  assert.deepEqual(m.keepLineChildren({ branch: ["a/b", "c"] }, "a").branch, ["c"]);
});

// Folding the line tree like the Editor outline (needs-model.js foldTree).
test("line tree folding: all, none, level N, toolbar levels, current", () => {
  const items2 = [
    { uid: "A", kind: "review", updated_at: "2026-11-26T10:00:00+00:00", branches: ["sandbox/infra"] },
    { uid: "B", kind: "review", updated_at: "2026-11-26T09:00:00+00:00", branches: ["sandbox/infra/ci"] },
    { uid: "C", kind: "review", updated_at: "2026-11-25T09:00:00+00:00", branches: ["paper"] },
  ];
  const tree2 = m.groupNeedsTree(items2, []);
  assert.deepEqual(m.treeFoldPaths(tree2).map((p) => [p.path, p.depth]),
    [["sandbox", 0], ["sandbox/infra", 1], ["sandbox/infra/ci", 2], ["paper", 0]]);
  assert.equal(m.treeDepth(tree2), 3);
  const all = m.foldTree(new Set(), tree2, { cmd: "foldAll" });
  assert.deepEqual([...all].sort(), ["paper", "sandbox", "sandbox/infra", "sandbox/infra/ci"]);
  assert.deepEqual([...m.foldTree(all, tree2, { cmd: "unfoldAll" })], []);
  // Ctrl+K Ctrl+2: fold level 2, leave the rest as it was.
  assert.deepEqual([...m.foldTree(new Set(["paper"]), tree2, { cmd: "foldLevel", level: 2 })].sort(), ["paper", "sandbox/infra"]);
  // Toolbar L2: level 1 open, level 2 and deeper folded.
  assert.deepEqual([...m.foldTree(all, tree2, { cmd: "showLevel", level: 2 })].sort(), ["sandbox/infra", "sandbox/infra/ci"]);
  assert.deepEqual([...m.foldTree(new Set(), tree2, { cmd: "fold", focus: "paper" })], ["paper"]);
  assert.deepEqual([...m.foldTree(new Set(["paper"]), tree2, { cmd: "unfold", focus: "paper" })], []);
  assert.deepEqual([...m.foldTree(new Set(), tree2, { cmd: "toggle", focus: "sandbox" })], ["sandbox"]);
  // Paths that left the tree are forgotten; "No line" never folds.
  assert.deepEqual([...m.foldTree(new Set(["gone"]), tree2, { cmd: "fold", focus: "" })], []);
});

test("Editor fold shortcuts map to line tree commands", () => {
  assert.deepEqual(m.foldCommandOf("editorFoldAll"), { cmd: "foldAll" });
  assert.deepEqual(m.foldCommandOf("editorUnfoldAll"), { cmd: "unfoldAll" });
  assert.deepEqual(m.foldCommandOf("editorFoldLevel3"), { cmd: "foldLevel", level: 3 });
  assert.deepEqual(m.foldCommandOf("editorFold"), { cmd: "fold" });
  assert.deepEqual(m.foldCommandOf("editorUnfold"), { cmd: "unfold" });
  assert.equal(m.foldCommandOf("editorSave"), null);
});

test("Home and Review fold with the shared toolbar, keys and palette", async () => {
  const ui = await readFile(new URL("../../ark_studio/web/js/needs-ui.js", import.meta.url), "utf8");
  assert.match(ui, /FOLD_ACTIONS/);
  assert.match(ui, /matchChord\(event, foldSpecs\(\)/);
  for (const file of ["views/review.js", "views/overview.js"]) {
    const src = await readFile(new URL(`../../ark_studio/web/js/${file}`, import.meta.url), "utf8");
    assert.match(src, /lineFoldBar\(ctx, tree,/);
    assert.match(src, /registerLineTree\(wrap, ctx, tree,/);
  }
  const app = await readFile(new URL("../../ark_studio/web/js/app.js", import.meta.url), "utf8");
  assert.match(app, /\.\.\.needsPaletteCommands\(\)/);
});

// Count bubbles: orange while an item was not opened in Node details, grey once all were.
test("unseen items and count bubbles", () => {
  const list = [{ uid: "A", kind: "review" }, { uid: "B", kind: "review" }, { id: "RG-3", uid: "RG-3", kind: "review_group" }];
  const fresh = new Set(["u:B", "rg:RG-3"]);
  assert.equal(m.itemUnseen(list[0], fresh), false);
  assert.equal(m.itemUnseen(list[1], fresh), true);
  assert.equal(m.unseenCount(list, fresh), 2);
  assert.equal(m.unseenCount(list, null), 3); // unknown: all new
  assert.deepEqual(m.countBubble(3, 0), { text: "3", unseen: 0, state: "seen" });
  assert.deepEqual(m.countBubble(3, 3), { text: "3", unseen: 3, state: "new" });
  assert.deepEqual(m.countBubble(3, 1, (n) => `${n} 新`), { text: "3 · 1 新", unseen: 1, state: "new" });
  // A tree group counts the items of its sub-lines.
  const tree = m.groupNeedsTree([{ uid: "A", kind: "review", branches: ["p/a"] }, { uid: "B", kind: "review", branches: ["p/b"] }], []);
  assert.equal(m.unseenCount(m.treeItems(tree.nodes[0]), new Set(["u:B"])), 1);
  assert.equal(m.unseenCount(m.treeItems(tree.nodes[0]), new Set()), 0);
});

test("opening a unit marks it and its review groups", () => {
  const needs = [{ kind: "review_group", id: "RG-2", uid: "RG-2", members: [{ uid: "X-1" }] }, { kind: "review_group", id: "RG-4", members: [{ uid: "Y" }] }];
  assert.deepEqual(m.openedKeys({ uid: "X-1", status: "review" }, needs), ["u:X-1", "rg:RG-2"]);
  assert.deepEqual(m.openedKeys({ uid: "X-1", status: "done" }, needs), []);
  assert.deepEqual(m.openedKeys({ uid: "D-1", type: "decision", status: "open" }, []), ["u:D-1"]);
});

test("a done parent with an active sub-line rolls up as active", () => {
  const { nodes } = m.groupNeedsTree([{ uid: "X", kind: "review", branches: ["wm/pool"] }],
    [{ name: "wm", status: "done" }, { name: "wm/pool", status: "active" }]);
  assert.equal(nodes[0].status, "active");
});

test("review rows: owner at the far right, quiet line chips", async () => {
  const ui = await readFile(new URL("../../ark_studio/web/js/needs-ui.js", import.meta.url), "utf8");
  const row = ui.slice(ui.indexOf("export function needsRow("), ui.indexOf("export function stripMd("));
  assert.ok(row.indexOf("assigneeChip(item)") > row.indexOf('class: "needs-age"'), "avatar after the time");
  assert.ok(row.indexOf("lineChips(ctx, lines") < row.indexOf('class: "needs-round"'), "line chip before the round");
  assert.match(ui, /name\.split\("\/"\)\.at\(-1\)/);
  const css = await readFile(new URL("../../ark_studio/web/css/app.css", import.meta.url), "utf8");
  const chip = /button\.needs-line-chip\{([^}]*)\}/.exec(css)?.[1] || "";
  assert.match(chip, /font-size:0\.625rem/);
  assert.match(chip, /background:none/);
  assert.doesNotMatch(chip, /violet/);
});

test("unseen dot and mark as read / unread", async () => {
  const group = { kind: "review_group", id: "RG-3", uid: "RG-3", members: [{ uid: "X-1" }, { uid: "S-3" }] };
  assert.deepEqual(m.cardKeys(group), ["rg:RG-3", "u:X-1", "u:S-3"]);
  assert.deepEqual(m.cardKeys({ uid: "F-1", kind: "review" }), ["u:F-1"]);
  // A group shows the dot while any member is unopened.
  assert.equal(m.cardUnseen(m.cardKeys(group), new Set(["u:S-3"])), true);
  assert.equal(m.cardUnseen(m.cardKeys(group), new Set()), false);
  assert.equal(m.cardUnseen(["u:F-1"], null), false); // unknown: no dot
  const ui = await readFile(new URL("../../ark_studio/web/js/needs-ui.js", import.meta.url), "utf8");
  for (const fn of ["export function needsRow(", "function reviewGroupCard(", "function deliveryCard("]) {
    const body = ui.slice(ui.indexOf(fn), ui.indexOf("\n}\n", ui.indexOf(fn)));
    assert.match(body, /unseenDot\(ctx, keys\)/, fn);
    assert.match(body, /attachSeenMenu\(ctx,/, fn);
  }
  assert.match(ui, /\{ unopened: keys \}/);
  assert.match(ui, /needs\.mark_unread/);
  const menu = await readFile(new URL("../../ark_studio/web/js/unit-menu.js", import.meta.url), "utf8");
  assert.match(menu, /row\.menuExtras/);
});

test("personal setting: highlight others' reviews", async () => {
  const settings = await readFile(new URL("../../ark_studio/web/js/views/settings.js", import.meta.url), "utf8");
  assert.match(settings, /othersReviewsPanel\(ctx\)/);
  const profile = await readFile(new URL("../../ark_studio/web/js/profile.js", import.meta.url), "utf8");
  assert.match(profile, /highlight_others_reviews: Boolean\(on\)/);
});
