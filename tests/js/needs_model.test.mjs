// Run: node --experimental-vm-modules --test tests/js/needs_model.test.mjs
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

test("needsCount and needsKind", () => {
  assert.equal(m.needsCount(undefined), 0);
  assert.equal(m.needsCount([{}, {}]), 2);
  assert.equal(m.needsKind({ kind: "decision" }), "decision");
  assert.equal(m.needsKind({ type: "decision" }), "decision");
  assert.equal(m.needsKind({ type: "task", status: "review" }), "review");
});

test("borrowedSources lists linked units once, in order", () => {
  const media = [
    { id: 1, from: { uid: "E-0002", title: "Sweep" } },
    { id: 2 },
    { id: 3, from: { uid: "E-0001", title: "Old" } },
    { id: 4, from: { uid: "E-0002", title: "Sweep" } },
  ];
  assert.deepEqual(m.borrowedSources(media).map((f) => f.uid), ["E-0002", "E-0001"]);
  assert.deepEqual(m.borrowedSources([{ id: 1 }]), []);
  assert.deepEqual(m.borrowedSources(undefined), []);
});

test("decisionOptions reads the Options section only", () => {
  const body = [
    "## Question", "Which LR?", "",
    "## Options", "- A: 1e-3", "- B: 3e-4", "1. C: cosine", "",
    "## Recommendation", "- B",
  ].join("\n");
  assert.deepEqual(m.decisionOptions(body), ["A: 1e-3", "B: 3e-4", "C: cosine"]);
  assert.deepEqual(m.decisionOptions("**Options**\n* yes\n* no\n**Default**\n- yes"), ["yes", "no"]);
  assert.deepEqual(m.decisionOptions("no options here\n- stray"), []);
});

test("previewKind prefers the server's preview_kind", () => {
  assert.equal(m.previewKind({ preview_kind: "video", url: "x.png" }), "video");
  assert.equal(m.previewKind({ url: "figs/a.PNG?x=1" }), "image");
  assert.equal(m.previewKind({ url: "run.mp4" }), "video");
  assert.equal(m.previewKind({ url: "notes.pdf" }), "");
});

test("needs: tag values, history tags and lifecycle sort", () => {
  assert.deepEqual([...m.NEEDS_VALUES], ["review", "decision", "accepted", "sent-back", "decided", "dismissed"]);
  assert.deepEqual([...m.needsHistoryTags()], m.NEEDS_VALUES.map((v) => `needs:${v}`));
  const sorted = m.sortNeedsValues([{ value: "dismissed" }, { value: "accepted" }, { value: "zz" }, { value: "review" }]);
  assert.deepEqual(sorted.map((v) => v.value), ["review", "accepted", "dismissed", "zz"]);
});
test("needs filters exclude values per group and keep new values visible", async () => {
  const m = await loadModel();
  const items = [
    { uid: "F-1", type: "artifact", kind: "review", assignee_effective: "alice" },
    { uid: "T-1", type: "task", kind: "review", assignee_effective: "" },
    { uid: "D-1", type: "decision", assignee_effective: "alice" },
    { uid: "F-2", type: "artifact", kind: "review", assignee_effective: "bob" },
  ];
  const facets = m.needsFacets(items);
  assert.deepEqual(facets.type.map((f) => [f.value, f.n]), [["artifact", 2], ["decision", 1], ["task", 1]]);
  assert.deepEqual(facets.kind.map((f) => f.value), ["review", "decision"]);
  assert.deepEqual(facets.assignee.map((f) => f.value), ["alice", "bob", ""]); // unassigned last
  let filter = m.normalizeNeedsFilter({});
  assert.equal(m.needsFilterActive(filter), false);
  assert.equal(m.filterNeeds(items, filter).length, 4);
  filter = m.toggleNeedsValue(filter, "type", "task");
  assert.deepEqual(m.filterNeeds(items, filter).map((i) => i.uid), ["F-1", "D-1", "F-2"]);
  filter = m.toggleNeedsValue(filter, "assignee", "alice");
  assert.deepEqual(m.filterNeeds(items, filter).map((i) => i.uid), ["F-2"]);
  filter = m.setNeedsGroupAll(filter, "kind", false, ["review", "decision"]);
  assert.deepEqual(m.filterNeeds(items, filter), []);
  filter = m.setNeedsGroupAll(filter, "kind", true);
  filter = m.toggleNeedsValue(filter, "assignee", "alice");
  assert.deepEqual(m.filterNeeds(items, filter).map((i) => i.uid), ["F-1", "D-1", "F-2"]);
  // A type that appears later is shown: only exclusions are stored.
  assert.equal(m.filterNeeds([...items, { uid: "X-1", type: "experiment" }], filter).length, 4);
  assert.deepEqual(m.normalizeNeedsFilter({ type: ["a", "a", 3], junk: [1] }), { type: ["a", "3"], kind: [], assignee: [] });
});

test("needsLightboxItems feeds the shared media lightbox in strip order", () => {
  const media = [
    { id: 1, url: "a.png", caption: "Own plot" },
    { id: 2, url: "b.mp4", from: { uid: "S-0001", title: "Story" } },
    { id: 3, preview_kind: "image", raw_url: "/raw/c", from: { uid: "E-0002", title: "Sweep" }, caption: "Curve" },
    { id: 4, url: "notes.txt" },
  ];
  const items = m.needsLightboxItems(media, (uid) => `来自 ${uid}`);
  assert.deepEqual(items.map((item) => item.id), [1, 2, 3]);
  assert.deepEqual(items.map((item) => item.kind), ["image", "video", "image"]);
  assert.deepEqual(items.map((item) => item.caption), ["Own plot", "Story · 来自 S-0001", "Curve · 来自 E-0002"]);
  assert.equal(items[2].raw_url, "/raw/c");
  assert.equal(m.needsLightboxItems([{ url: "x.png", from: { uid: "T-0001" } }])[0].caption, "from T-0001");
  assert.deepEqual(m.needsLightboxItems(null), []);
});

test("Needs-you thumbnails open the graph's media lightbox, not a second viewer", async () => {
  // The row (thumbnails included) is shared by Home and Research › Review.
  const source = await readFile(new URL("../../ark_studio/web/js/needs-ui.js", import.meta.url), "utf8");
  assert.match(source, /import \{ openMediaLightbox \} from "\.\/image-viewer\.js";/);
  assert.match(source, /openMediaLightbox\(\{\s*items: needsLightboxItems\(media/);
  assert.doesNotMatch(source, /openImageLightbox|function openVideo/);
  const graph = await readFile(new URL("../../ark_studio/web/js/graph.js", import.meta.url), "utf8");
  assert.match(graph, /openMediaLightbox\(\{/);
});

test("isOpenDecision and inNeedsYou leave out closed statuses, as the server does", async () => {
  const go = await readFile(new URL("../../ark_studio/server/internal/store/needsopen.go", import.meta.url), "utf8");
  const server = JSON.parse(`[${go.match(/ClosedStatuses = \[\]string\{([^}]*)\}/)[1]}]`);
  assert.deepEqual([...m.CLOSED_STATUSES], server);
  const statuses = ["inbox", "open", "active", "review", "blocked", "done", "abandoned", "archived"];
  for (const status of statuses) {
    const closed = ["done", "abandoned", "archived"].includes(status);
    assert.equal(m.isOpenDecision({ type: "decision", status }), !closed, status);
    assert.equal(m.inNeedsYou({ uid: "D-1", type: "decision", status }), !closed, status);
    assert.equal(m.inNeedsYou({ uid: "E-1", type: "evidence", status }), status === "review", status);
    assert.equal(m.needsUnitKey({ uid: "D-1", type: "decision", status }), closed ? "" : "u:D-1", status);
  }
  assert.equal(m.isOpenDecision({ type: "task", status: "open" }), false);
  assert.equal(m.inNeedsYou({ uid: "D-1", type: "decision", status: "open", archived: 1 }), false);
  assert.equal(m.needsUnitKey({ type: "decision", status: "open" }), "");
});
