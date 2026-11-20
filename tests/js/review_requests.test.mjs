// Review requests: anchors, card text (zh/en), headline numbers, the
// fallback grouping of one delivery's units, and heading anchors.
// Run: node --experimental-vm-modules --test tests/js/review_requests.test.mjs
import assert from "node:assert/strict";
import test from "node:test";
import {
  anchorLabel, changesText, clockLabel, diffBlocks, keyNumber, noteParts, parseAnchors, parseClock, reviewCardText,
} from "../../ark_studio/web/js/review-model.js";
import {
  bulkAcceptable, deliveryKey, groupExplain, groupNeeds, groupReason, shortTitle, unitItems,
} from "../../ark_studio/web/js/needs-model.js";
import { anchorSlug, headingAnchors } from "../../ark_studio/web/js/rich-model.js";
import { ZH } from "../../ark_studio/web/js/i18n-zh.js";
import { EN } from "../../ark_studio/web/js/i18n-en.js";

const translator = (dict) => (key, vars = {}) => {
  let text = dict[key] ?? key;
  for (const [k, v] of Object.entries(vars)) text = text.replaceAll(`{${k}}`, String(v));
  return text;
};
const zh = translator(ZH);
const en = translator(EN);

test("anchors: headings, units, media with times; once each, in order", () => {
  const note = "看 [时间重排](#时间重排)、[T](T-0042#setup)、[unit](E-0001) 和 media:421@0:08-0:12，[clip](media:7@1:02) [x](https://e.com)";
  const anchors = parseAnchors(note);
  assert.deepEqual(anchors.map((a) => [a.kind, a.unit || "", a.slug || "", a.media || 0]), [
    ["heading", "", "时间重排", 0], ["heading", "T-0042", "setup", 0], ["unit", "E-0001", "", 0],
    ["media", "", "", 7], ["media", "", "", 421]]);
  assert.equal(anchors[3].start, 62);
  assert.deepEqual([anchors[4].start, anchors[4].end], [8, 12]);
  assert.deepEqual(parseAnchors("[a](#x) [b](#x)").length, 1);
  assert.equal(parseAnchors("media:3@0:70").length, 0, "seconds past 59 do not parse");
  assert.equal(parseClock("1:02:03"), 3723);
  assert.equal(clockLabel(62), "1:02");
  assert.equal(anchorLabel({ kind: "media", media: 4, start: 8, end: 12 }), "media:4 @0:08–0:12");
  assert.equal(anchorLabel({ kind: "heading", slug: "results", unit: "T-1" }, "T-1"), "§ results");
  assert.equal(anchorLabel({ kind: "heading", slug: "results", unit: "T-2" }, "T-1"), "T-2 › results");
});

test("note parts: text runs and anchor chips; other links keep their text", () => {
  const parts = noteParts("Check **[Results](#results)** and [the paper](https://x.org) plus media:5");
  assert.deepEqual(parts.map((p) => (p.anchor ? `@${p.anchor.raw}` : p.text)),
    ["Check ", "@#results", " and ", "the paper", " plus ", "@media:5"]);
});

test("heading slugs match the server's rules", () => {
  assert.equal(anchorSlug("时间重排 (v2)"), "时间重排-v2");
  assert.equal(anchorSlug("  A -- b_c "), "a-b-c");
  assert.deepEqual(headingAnchors([{ text: "Results" }, { text: "Results" }, { text: "x", explicit: "key" }, { text: "Results" }]),
    ["results", "results-1", "key", "results-2"]);
});

test("card text in zh and en: note, fallback, round, previous round, changes", () => {
  const withNote = {
    review_round: 2, review_note: "看 [结果](#结果)", review_base: { kind: "review" },
    review_changes: { added: 3, removed: 1, media: 2 },
    review_prev: { round: 1, outcome: "sent-back", comments: [{ body: "补基线", resolved: false }] },
  };
  const z = reviewCardText(withNote, zh);
  assert.equal(z.round, "第 2 轮");
  assert.equal(z.lead, "本次请看");
  assert.equal(z.prev, "第 1 轮 · 已退回");
  assert.equal(z.open, 1);
  assert.equal(z.changes, "自上次审阅以来 · +3 −1 · 2 个新附件");
  const e = reviewCardText(withNote, en);
  assert.equal(e.round, "Round 2");
  assert.equal(e.lead, "Review this");
  assert.equal(e.changes, "Since your last review · +3 −1 · 2 new files");
  const bare = { review_round: 1, review_note: "", review_focus: "## Latest\nnew numbers", review_changes: { added: 0, removed: 0, media: 0 } };
  assert.equal(reviewCardText(bare, zh).lead, "未说明本次审阅内容");
  assert.equal(reviewCardText(bare, zh).focus, "## Latest\nnew numbers");
  assert.equal(reviewCardText(bare, en).lead, "No note on what to review");
  assert.equal(reviewCardText(bare, en).changes, "Since it entered review · no changes");
  assert.equal(reviewCardText(bare, en).prev, "");
  assert.equal(changesText(null, null, en), "", "legacy items have no toggle");
});

test("headline numbers of result titles", () => {
  assert.equal(keyNumber("Single-world GPU latency 4.7-6.4 ms per env step (up to 9.2 ms)"), "4.7-6.4 ms");
  assert.equal(keyNumber("GRPO group sync idles sandboxes ~41% by the workload model (measured 33–45%)"), "~41%");
  assert.equal(keyNumber("GPU memory is not the limit: 0.17-0.35 MB per world"), "0.17-0.35 MB");
  assert.equal(keyNumber("Keeping the LIBERO scene across turns: 7.4x per 20-turn episode"), "7.4x");
  assert.equal(keyNumber("PEAR is fully open source"), "");
  assert.equal(keyNumber("PEAR is fully open source (Apache-2.0, faff474)"), "", "not a version or licence");
});

test("diff blocks keep skip counts and join lines", () => {
  assert.deepEqual(diffBlocks([{ op: "same", lines: ["a"] }, { op: "skip", n: 4 }, { op: "add", lines: ["b", "c"] }]),
    [{ op: "same", text: "a" }, { op: "skip", n: 4 }, { op: "add", text: "b\nc" }]);
});

// A Needs you list shaped like a real one: three deliveries of one task
// (B1, B2, B3), a summary without a branch, an unrelated unit, a decision
// and an agent-submitted review group.
const T = { uid: "T-0020", title: "HOI-215 · Large sandbox environments", type: "task", relation: "part_of" };
const at = (hh, mm, ss = 0) => `2026-11-19T${String(hh).padStart(2, "0")}:${String(mm).padStart(2, "0")}:${String(ss).padStart(2, "0")}+00:00`;
const delivery = (label, branch, commit) => ({ label, branch, commit });
const B1 = delivery("HOI-215 B1", "agent/HOI-215-b1-codebase", "4cf0d04");
const B2 = delivery("HOI-215 B2", "agent/HOI-215-b2-gpusim", "0035966");
const B3 = delivery("HOI-215 B3", "agent/HOI-215-b3-bench", "c9e7ad4");
const unit = (uid, type, since, extra = {}) => ({
  uid, type, kind: "review", status: "review", title: `${uid} claim 1.5 ms`, review_since: since, parents: [T], links: [], ...extra,
});
const LIST = [
  { uid: "D-0001", type: "decision", kind: "decision", title: "Which seed?" },
  { uid: "RG-3", type: "review_group", kind: "review_group", title: "Agent group", members: [] },
  unit("E-0068", "evidence", at(21, 29, 40), { links: ["E-0064", "E-0065", "E-0066", "E-0067"].map((uid) => ({ uid, relation: "derived_from", dir: "out" })) }),
  ...["E-0064", "E-0065", "E-0066", "E-0067"].map((uid, i) => unit(uid, "evidence", at(21, 28, 32 + i), {
    delivery: B3, links: [{ uid: "E-0068", relation: "derived_from", dir: "in" }],
  })),
  ...["E-0058", "E-0059", "E-0060", "E-0061", "E-0062", "E-0063"].map((uid, i) => unit(uid, "evidence", at(17, 45, 7 + i), { delivery: B1 })),
  unit("C-0007", "claim", at(17, 45, 13), { delivery: B1 }),
  ...["E-0049", "E-0050", "E-0051", "E-0052", "E-0053", "E-0054", "E-0055", "E-0056", "E-0057"].map((uid, i) =>
    unit(uid, "evidence", at(17, 30, 39 + i), { delivery: B2 })),
  unit("C-0006", "claim", at(17, 31, 21), { delivery: B2 }),
  unit("E-0047", "evidence", at(17, 31, 22), { delivery: B2, parents: [] }),
  unit("E-0048", "evidence", at(13, 18, 53), { parents: [{ uid: "T-0018", title: "HOI-214 · Literature", type: "task" }] }),
];

test("fallback grouping: one card per delivery, never across deliveries", () => {
  const cards = groupNeeds(LIST);
  const summary = cards.map((c) => (c.kind === "group" ? `${c.title}:${c.members.length}` : c.item.uid));
  assert.deepEqual(summary, ["D-0001", "RG-3", "HOI-215 B3:5", "HOI-215 B1:7", "HOI-215 B2:11", "E-0048"]);
  const b2 = cards.find((c) => c.kind === "group" && c.title === "HOI-215 B2");
  assert.equal(b2.primary.uid, "C-0006", "a claim before its evidence");
  assert.equal(b2.members[0].uid, "C-0006");
  assert.equal(b2.parent.uid, "T-0020");
  assert.equal(b2.delivery, "branch:agent/HOI-215-b2-gpusim");
  assert.deepEqual(groupExplain(b2), { parents: ["T-0020"], linked: false, delivery: "branch:agent/HOI-215-b2-gpusim", minutes: 1 });
  const b3 = cards.find((c) => c.kind === "group" && c.members.some((m) => m.uid === "E-0068"));
  assert.equal(b3.delivery, "", "the summary names no branch: grouped by time");
  assert.ok(groupExplain(b3).linked);
  // Deterministic: the same input gives the same cards.
  assert.deepEqual(groupNeeds(LIST).map((c) => c.key || c.item.uid), cards.map((c) => c.key || c.item.uid));
});

test("grouping rules: window, links, different deliveries, single members", () => {
  const a = unit("E-1", "evidence", at(10, 0));
  const b = unit("E-2", "evidence", at(10, 20));
  const c = unit("E-3", "evidence", at(10, 45));
  assert.ok(groupReason(a, b), "same parent, 20 min apart");
  assert.equal(groupReason(a, c), null, "45 min apart, no delivery");
  assert.ok(groupReason({ ...a, delivery: B1 }, { ...c, delivery: B1 }), "same branch: no window");
  assert.ok(groupReason({ ...a, delivery: B1, parents: [] }, { ...c, delivery: B1, parents: [] }), "same branch, no link, same day");
  assert.equal(groupReason({ ...a, delivery: B1, parents: [] }, { ...c, delivery: B1, parents: [], review_since: "2026-11-21T10:00:00Z" }),
    null, "same branch, no link, days apart");
  assert.equal(groupReason({ ...a, delivery: B1 }, { ...b, delivery: B2 }), null, "different deliveries");
  const lone = { ...a, parents: [] };
  const other = { ...b, parents: [] };
  assert.equal(groupReason(lone, other), null, "no link, no shared parent");
  assert.ok(groupReason({ ...lone, links: [{ uid: "E-2", relation: "supports", dir: "out" }] }, other), "linked");
  assert.deepEqual(groupNeeds([a]).map((x) => x.kind), ["item"], "one member is never a group");
  assert.equal(deliveryKey({ delivery: { commit: "abc1234" } }), "commit:abc1234");
  assert.equal(shortTitle("HOI-215 · Large sandbox"), "HOI-215");
});

test("review groups are not units for bulk actions", () => {
  assert.deepEqual(unitItems(LIST).map((i) => i.uid).includes("RG-3"), false);
  assert.deepEqual(bulkAcceptable(LIST).map((i) => i.uid).includes("RG-3"), false);
});

test("inspector Accept / Send back only for units in review that the person may change", async () => {
  const { canDecideReview } = await import("../../ark_studio/web/js/review-model.js");
  assert.equal(canDecideReview({ status: "review" }), true);
  assert.equal(canDecideReview({ status: "review" }, { permission: "write" }), true);
  assert.equal(canDecideReview({ status: "active" }, { permission: "admin" }), false);
  assert.equal(canDecideReview({ status: "review" }, { permission: "read" }), false);
  assert.equal(canDecideReview({ status: "review" }, { mobilePermission: "read_only" }), false);
  const { readFile } = await import("node:fs/promises");
  const src = await readFile(new URL("../../ark_studio/web/js/comments.js", import.meta.url), "utf8");
  assert.match(src, /review\.sendBack\(text\(\), clearInput\)/, "send back takes the typed text as the reason");
  assert.match(src, /review\.accept\(text\(\), clearInput\)/, "accept takes the typed text as the note");
});

test("comment row: Accept, Send back, Add comment on one row; only Add comment outside review", async () => {
  const { readFile } = await import("node:fs/promises");
  const src = await readFile(new URL("../../ark_studio/web/js/comments.js", import.meta.url), "utf8");
  const fn = src.slice(src.indexOf("export function commentActionButtons"));
  assert.match(fn, /if \(!review\) return \[addBtn\];/);
  assert.ok(fn.indexOf('reviewAction: "accept"') < fn.indexOf('reviewAction: "send-back"'));
  assert.ok(fn.indexOf('reviewAction: "send-back"') < fn.lastIndexOf("addBtn,"));
  assert.match(fn, /addBtn\.className = review \? "btn sm" : "btn sm primary"/, "Accept is the primary button in review");
  const css = await readFile(new URL("../../ark_studio/web/css/app.css", import.meta.url), "utf8");
  assert.match(css, /\.comments-actions\{display:flex;flex-wrap:wrap;gap:6px;justify-content:flex-start/);
  assert.doesNotMatch(css, /comments-review-actions/);
});
