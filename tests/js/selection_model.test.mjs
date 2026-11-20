// Run: node --experimental-vm-modules --test tests/js/selection_model.test.mjs
import assert from "node:assert/strict";
import { test } from "node:test";
import {
  BATCH_FIELDS, commonValue, sharedAttributes, ownTags, tagKey, tagPresence, topicPresence,
  batchFieldPayload, batchTagsPayload, undoPayload, changedBefore, siblingGroup, groupDefaults,
} from "../../ark_studio/web/js/selection-model.js";

const units = [
  { id: 1, uid: "T-0001", type: "task", status: "open", importance: "p2", assignee: "ana", start_at: "2026-01-01", end_at: null,
    tags: ["type:task", "status:open", "importance:p2", "tag:x", "tag:y", "needs:review"], topics: [{ id: 9, uid: "P-0009", title: "Alpha" }] },
  { id: 2, uid: "T-0002", type: "task", status: "active", importance: "p2", assignee: null, start_at: "2026-01-01", end_at: "",
    tags: ["type:task", "tag:x"], topics: [{ id: 9, uid: "P-0009", title: "Alpha" }, { id: 8, uid: "P-0008", title: "Beta" }] },
  { id: 3, uid: "T-0003", type: "task", status: "open", importance: "p2", assignee: "", start_at: "2026-01-01",
    tags: ["tag:x", "seq:2"], topics: [{ id: 9, uid: "P-0009", title: "Alpha" }] },
];

test("a field shows the shared value, or Mixed when the nodes differ", () => {
  assert.deepEqual(commonValue(units, "type"), { value: "task", mixed: false });
  assert.deepEqual(commonValue(units, "status"), { value: "", mixed: true });
  // missing, null and "" are the same empty value
  assert.deepEqual(commonValue(units, "end_at"), { value: "", mixed: false });
  assert.deepEqual(commonValue(units, "assignee"), { value: "", mixed: true });
  const shared = sharedAttributes(units);
  assert.deepEqual(Object.keys(shared), BATCH_FIELDS);
  assert.equal(shared.importance.value, "p2");
  assert.equal(shared.start_at.value, "2026-01-01");
  assert.equal(shared.status.mixed, true);
  assert.deepEqual(commonValue([], "type"), { value: "", mixed: false });
});

test("tags and topics on all nodes vs some", () => {
  assert.deepEqual(ownTags(units[0]), ["tag:x", "tag:y"]);
  assert.deepEqual(tagPresence(units), { all: ["tag:x"], some: [{ tag: "seq:2", count: 1 }, { tag: "tag:y", count: 1 }] });
  const topics = topicPresence(units);
  assert.deepEqual(topics.all.map((topic) => topic.uid), ["P-0009"]);
  assert.deepEqual(topics.some.map(({ topic, count }) => [topic.uid, count]), [["P-0008", 1]]);
  assert.equal(tagKey("drop"), "tag:drop");
  assert.equal(tagKey(" Area : ml "), "area:ml");
});

test("batch edit payloads: one call for every selected node", () => {
  assert.deepEqual(batchFieldPayload(units, "status", "done"), { units: ["T-0001", "T-0002", "T-0003"], patch: { status: "done" } });
  assert.deepEqual(batchFieldPayload(units, "assignee", "").patch, { assignee: null });
  assert.deepEqual(batchFieldPayload(units, "end_at", "").patch, { end_at: null });
  assert.throws(() => batchFieldPayload(units, "title", "x"));
  assert.deepEqual(batchTagsPayload(units, { add: ["x", " x ", "status:done", ""], remove: ["y"] }),
    { units: ["T-0001", "T-0002", "T-0003"], tags_add: ["x"], tags_remove: ["y"] });
});

test("undo puts back each node's own previous value in one call", () => {
  assert.deepEqual(undoPayload(units.slice(0, 2), { field: "status" }), { updates: [
    { unit: "T-0001", patch: { status: "open", end_at: null } },
    { unit: "T-0002", patch: { status: "active", end_at: "" } },
  ] });
  assert.deepEqual(undoPayload([units[0]], { field: "assignee" }).updates[0].patch, { assignee: "ana" });
  assert.deepEqual(undoPayload([units[0]], { tags: true }).updates[0].patch, { tags: ["tag:x", "tag:y"] });
  // only the nodes the batch really changed are undone
  assert.deepEqual(changedBefore(units, { updated: [{ id: 2 }] }).map((unit) => unit.id), [2]);
});

test("siblings: one shared parent (or all top level) is required", () => {
  const parentOf = new Map([["a", "root"], ["b", "root"], ["c", "root"], ["a1", "a"]]);
  assert.deepEqual(siblingGroup(["a", "b", "c"], parentOf), { ok: true, parent: "root" });
  assert.deepEqual(siblingGroup(["root", "x"], parentOf), { ok: true, parent: null });
  assert.deepEqual(siblingGroup(["a", "a1"], parentOf), { ok: false, reason: "not_siblings" });
  assert.deepEqual(siblingGroup(["b", "root"], parentOf), { ok: false, reason: "not_siblings" });
  assert.deepEqual(siblingGroup(["a"], parentOf), { ok: false, reason: "too_few" });
  assert.deepEqual(siblingGroup(["a", "a"], parentOf), { ok: false, reason: "too_few" });
});

test("the new parent takes the selection's type, else the parent's, and the first sibling's place", () => {
  assert.deepEqual(groupDefaults([{ type: "task", tags: ["seq:3"] }, { type: "task", tags: ["seq:1.5"] }], { type: "note" }),
    { type: "task", tags: ["seq:1.5"] });
  assert.deepEqual(groupDefaults([{ type: "task" }, { type: "idea" }], { type: "claim" }), { type: "claim", tags: [] });
  assert.deepEqual(groupDefaults([{ type: "task" }, { type: "idea" }], null), { type: "note", tags: [] });
  assert.equal(groupDefaults([{ type: "topic" }, { type: "topic" }], null).type, "note");
});
